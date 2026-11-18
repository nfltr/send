// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package xfer

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nfltr/send/xfer/verified"
)

// StagePrefix starts the name of the directory a transfer is staged in,
// inside the receiving directory, until it commits; a cut transfer's stays
// there and the next receive of the same offer resumes from it.
const StagePrefix = ".nfltr-send-"

// StageLife is an offer's life on the receiving side: a stage nobody wrote
// to for this long is abandoned, and the next receive into its directory
// removes it.
const StageLife = 7 * 24 * time.Hour

// ErrRefused is a transfer the receiver will not take; nothing changed.
var ErrRefused = errors.New("refused")

// ErrUnverified is a chunk that is not the offered file's (it does not
// verify against the manifest's root): the session ends, the verified
// part stays for the next.
var ErrUnverified = errors.New("not the offered bytes")

// Progress gets the file bytes written so far; nil: none.
type Progress func(written int64)

// Receive takes one transfer over a session into dir. It returns the
// answer it sent; an error before the answer (a cut session) leaves the
// stage for the next Receive of the same offer to resume from.
func Receive(rw io.ReadWriter, dir string, progress Progress) (Answer, error) {
	c := newConn(rw)
	t, raw, err := c.read(nil)
	if err != nil {
		return Answer{}, noEOF(err)
	}
	if t == frameAnswer { // the sender refused the session (Refuse)
		var a Answer
		if err := json.Unmarshal(raw, &a); err != nil {
			return Answer{}, fmt.Errorf("%w: answer: %v", ErrProtocol, err)
		}
		return a, fmt.Errorf("%w: %s", ErrRefused, a.Message)
	}
	if t != frameManifest {
		return Answer{}, fmt.Errorf("%w: frame %q, want the manifest", ErrProtocol, t)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Answer{}, fmt.Errorf("%w: manifest: %v", ErrProtocol, err)
	}
	r, err := newReceiver(dir, m, raw)
	if err != nil {
		a := Answer{Message: err.Error()}
		_ = c.writeJSON(frameAnswer, a)
		_ = c.flush()
		return a, fmt.Errorf("%w: %w", ErrRefused, err)
	}
	defer r.close()
	if r.finishing { // a commit stopped part way: finish it, nothing is sent
		a := Answer{Files: countFiles(m), Bytes: countBytes(m), OK: true}
		if err := r.commit(); err != nil {
			a = Answer{Message: err.Error()}
		}
		if err := c.writeJSON(frameAnswer, a); err != nil {
			return a, err
		}
		return a, c.flush()
	}
	parts, err := r.parts()
	if err != nil {
		return Answer{}, err
	}
	if err := c.writeJSON(frameWant, want{Parts: parts}); err != nil {
		return Answer{}, err
	}
	if err := c.flush(); err != nil {
		return Answer{}, err
	}
	a, err := r.run(c, parts, progress)
	if err != nil {
		return a, err
	}
	if err := c.writeJSON(frameAnswer, a); err != nil {
		return a, err
	}
	return a, c.flush()
}

type receiver struct {
	m         Manifest
	raw       []byte   // the manifest as the sender sent it
	sum       string   // the manifest's SHA-256
	root      *os.Root // dir
	stage     string   // its name in dir
	sr        *os.Root // the stage
	lk        *os.File // the stage's lock, held until the receive ends
	tops      []string
	finishing bool // the stage holds this offer's intent: its commit stopped part way
	tr        recvTrace
}

// intentName is the intent record's name in the stage: the offer's
// manifest digest and the manifest itself, written and synced before a
// commit's first rename (spec Commit.tla Begin), so whoever finishes the
// commit can compare what is in place with it. Entries never start with
// StagePrefix.
const intentName = StagePrefix + "intent"

type intent struct {
	Manifest string `json:"manifest"`
	Offer    []byte `json:"offer"` // the manifest's bytes (base64 in JSON: exact)
}

// newReceiver checks the manifest (relative names inside dir, files and
// directories only, no name twice, no top-level name dir already holds)
// and opens the offer's stage, named by the manifest's digest, taking its
// lock (spec Commit.tla Open): another receive holding it refuses this one
// (StageBusyError).
func newReceiver(dir string, m Manifest, raw []byte) (*receiver, error) {
	if traceOn {
		defer traceAtomic()()
	}
	r := &receiver{m: m, raw: raw}
	var err error
	if r.tops, err = checkManifest(m); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	r.root = root
	sum := sha256.Sum256(raw)
	r.sum = hex.EncodeToString(sum[:])
	r.stage = StagePrefix + r.sum[:16]
	err = root.Mkdir(r.stage, 0o700)
	created := err == nil
	if err != nil && !errors.Is(err, fs.ErrExist) {
		_ = root.Close()
		return nil, err
	}
	if r.lk, err = lockStage(root, r.stage); err != nil {
		if traceOn {
			r.trOpen(created, "Busy")
		}
		_ = root.Close()
		return nil, err
	}
	// The offer's own intent (spec Open, the recovery): its names in dir are
	// its own commit's; another record (torn, another offer's) is not trusted.
	var in intent
	if b, err := root.ReadFile(path.Join(r.stage, intentName)); err == nil && json.Unmarshal(b, &in) == nil && in.Manifest == r.sum {
		r.finishing = true
	} else if top := r.taken(); top != "" {
		if traceOn {
			r.trOpen(created, "Taken")
		}
		if created {
			_ = removeStage(root, r.stage, r.lk)
		} else {
			release(r.lk)
		}
		_ = root.Close()
		return nil, fmt.Errorf("%q %w", top, errExists)
	}
	if traceOn {
		r.trOpen(created, "Open")
	}
	r.sweep()
	if r.sr, err = root.OpenRoot(r.stage); err != nil {
		release(r.lk)
		_ = root.Close()
		return nil, err
	}
	return r, nil
}

// checkManifest checks m (relative names inside the directory, files and
// directories only, no name twice) and returns its top-level names.
func checkManifest(m Manifest) (tops []string, err error) {
	if m.Version != Version {
		return nil, fmt.Errorf("protocol version %d, this nfltr-send speaks %d: update both ends", m.Version, Version)
	}
	seen := map[string]bool{}
	for _, e := range m.Entries {
		name := path.Clean(e.Path)
		if name != e.Path || strings.Contains(e.Path, `\`) || !filepath.IsLocal(filepath.FromSlash(name)) || strings.HasPrefix(name, StagePrefix) {
			return nil, fmt.Errorf("%q: not a relative name inside the directory", e.Path)
		}
		if seen[name] {
			return nil, fmt.Errorf("%q: offered twice", e.Path)
		}
		seen[name] = true
		// ponytail: a file's tree is held in memory while it is received
		// (64 bytes per chunk: 256 MiB for a 1 TiB file), grown only as the
		// sender sends it; maxSize keeps its size arithmetic exact.
		if e.Size < 0 || e.Size > maxSize || (e.Dir && e.Size != 0) {
			return nil, fmt.Errorf("%q: bad size", e.Path)
		}
		if d, err := hex.DecodeString(e.BLAKE3); !e.Dir && (err != nil || len(d) != 32) {
			return nil, fmt.Errorf("%q: bad BLAKE3 root", e.Path)
		}
		if top, _, _ := strings.Cut(name, "/"); !slices.Contains(tops, top) {
			tops = append(tops, top)
		}
	}
	return tops, nil
}

// sweep removes the stages, other than this offer's, that nobody wrote to
// for StageLife (abandoned offers'), and finishes those of them holding
// their intent record (a commit stopped part way); best effort. A stage
// whose lock another receive holds is skipped (spec Commit.tla Expire).
func (r *receiver) sweep() {
	es, _ := fs.ReadDir(r.root.FS(), ".")
	for _, e := range es {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), StagePrefix) || e.Name() == r.stage {
			continue
		}
		stale := true
		_ = fs.WalkDir(r.root.FS(), e.Name(), func(_ string, d fs.DirEntry, err error) error {
			if err != nil {
				stale = false
				return fs.SkipAll
			}
			if fi, err := d.Info(); err != nil || time.Since(fi.ModTime()) < StageLife {
				stale = false
				return fs.SkipAll
			}
			return nil
		})
		if !stale {
			continue
		}
		lk, err := lockStage(r.root, e.Name())
		if err != nil {
			continue
		}
		r.finishStage(e.Name(), lk)
	}
}

// finishStage takes a stale stage whose lock lk it holds. One holding its
// own intent record (named by the digest of the manifest the record
// carries) has its commit finished as the offer's next receive would
// finish it (commit: an entry the stage no longer holds compared with the
// manifest, the stage removed only once every entry is in place; spec
// Commit.tla Open by another process); one whose record is not its own,
// or that has none, is abandoned and removed (Expire).
func (r *receiver) finishStage(stage string, lk *os.File) {
	var in intent
	b, err := r.root.ReadFile(path.Join(stage, intentName))
	if err != nil || json.Unmarshal(b, &in) != nil || len(in.Manifest) != 64 || StagePrefix+in.Manifest[:16] != stage {
		if traceOn {
			traceExpire(r.root, stage)
		}
		_ = removeStage(r.root, stage, lk)
		return
	}
	s := &receiver{root: r.root, stage: stage, lk: lk, sum: in.Manifest, finishing: true}
	if sum := sha256.Sum256(in.Offer); hex.EncodeToString(sum[:]) != in.Manifest || json.Unmarshal(in.Offer, &s.m) != nil {
		release(lk) // not readable as this stage's: kept, never trusted
		return
	}
	if s.tops, err = checkManifest(s.m); err == nil {
		s.sr, err = r.root.OpenRoot(stage)
	}
	if err == nil {
		if traceOn {
			s.trSwept()
		}
		_ = s.commit()
	}
	s.end()
}

// end ends the receive's hold of its stage: its lock released, if a
// commit did not remove the stage with it.
func (r *receiver) end() {
	if traceOn {
		defer r.trAtomic()()
		r.trEnd()
	}
	release(r.lk)
	r.lk = nil
	if r.sr != nil {
		_ = r.sr.Close()
	}
}

func (r *receiver) close() {
	r.end()
	_ = r.root.Close()
}

// taken is the first top-level name dir already holds, or "".
func (r *receiver) taken() string {
	for _, top := range r.tops {
		if _, err := r.root.Lstat(top); !errors.Is(err, fs.ErrNotExist) {
			return top
		}
	}
	return ""
}

// treeName is entry i's tree (its outboard, as the sender sent it) in the
// stage: what a part is verified with when a later session resumes it.
func treeName(i int) string { return StagePrefix + "tree-" + strconv.Itoa(i) }

// keptTree is entry i's tree as the stage kept it. A file of one chunk has
// no parent nodes: its tree is only its size, never written (a file per
// small file costs as much as the file).
func (r *receiver) keptTree(i int, e Entry) ([]byte, error) {
	if oneChunk(e.Size) {
		return binary.LittleEndian.AppendUint64(nil, uint64(e.Size)), nil // #nosec G115 -- checkManifest bounds the size
	}
	return r.sr.ReadFile(treeName(i))
}

func oneChunk(size int64) bool { return size <= verified.GroupSize }

// root is entry e's BLAKE3 root (checkManifest checked it).
func root(e Entry) (r [32]byte) {
	_, _ = hex.Decode(r[:], []byte(e.BLAKE3))
	return r
}

// parts are the files the stage holds a part of, each with the length of
// its prefix that verifies against the root, by the tree the stage kept
// (spec Open). A part without its tree is not resumed.
func (r *receiver) parts() ([]Part, error) {
	var out []Part
	for i, e := range r.m.Entries {
		if e.Dir {
			continue
		}
		fi, err := r.sr.Lstat(e.Path)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 {
			continue
		}
		ob, err := r.keptTree(i, e)
		if err != nil {
			continue
		}
		f, err := r.sr.Open(e.Path)
		if err != nil {
			return nil, err
		}
		n, _, err := verified.Tree{Root: root(e), Size: e.Size, Outboard: ob}.Verify(f)
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		if n > 0 {
			out = append(out, Part{Index: i, Offset: n})
		}
	}
	return out, nil
}

// run takes the files, each chunk verified on arrival against the
// manifest's root and written only then, in order (spec Send; one that
// does not verify ends the session, Bad, nothing after it written),
// verifies each file from disk, and commits the whole offer once every
// file is verified (spec Finish). Up to inFlight chunks are verified at
// once while the next frames are read (docs/performance.md); a chunk not
// yet written when the session ends is not kept (spec Cut).
func (r *receiver) run(c *conn, parts []Part, progress Progress) (Answer, error) {
	var (
		buf     []byte
		f       *os.File
		cur     = -1 // the entry being received
		left    int64
		off     int64         // its next byte read
		tr      verified.Tree // its tree
		q       []pending     // its chunks read, oldest first, not yet written
		free    [][]byte      // their buffers, for the next
		next    int           // entries before it are done
		written int64
		a       Answer
	)
	asked := map[int]int64{}
	for _, p := range parts {
		asked[p.Index] = p.Offset
	}
	if traceOn {
		defer func() {
			if f != nil {
				r.trCut()
			}
		}()
	}
	fail := func(format string, args ...any) (Answer, error) {
		if f != nil {
			_ = f.Close()
		}
		return Answer{}, fmt.Errorf("%w: "+format, append([]any{ErrProtocol}, args...)...)
	}
	// keep writes the oldest pending chunk once it verified, or ends the
	// session at it (Bad).
	keep := func() error {
		pd := q[0]
		q = q[1:]
		if !<-pd.ok {
			if traceOn {
				r.trBad(pd.p)
			}
			_ = f.Close()
			f = nil
			return fmt.Errorf("%q: the chunk at %d is %w", r.m.Entries[cur].Path, pd.off, ErrUnverified)
		}
		if _, err := f.Write(pd.p); err != nil {
			_ = f.Close()
			f = nil
			return err
		}
		if afterWrite != nil {
			afterWrite(f, pd.off)
		}
		if traceOn {
			r.trData(pd.p)
		}
		free = append(free, pd.p)
		written += int64(len(pd.p))
		if progress != nil {
			progress(written)
		}
		return nil
	}
	for {
		t, p, err := c.read(buf)
		if err != nil { // a cut: the chunks read that verify are kept
			for len(q) > 0 && keep() == nil {
			}
			if f != nil {
				_ = f.Close()
			}
			return Answer{}, noEOF(err)
		}
		buf = p[:0]
		switch t {
		case frameStart:
			var s start
			if err := json.Unmarshal(p, &s); err != nil || f != nil || s.Index < next || s.Index >= len(r.m.Entries) || r.m.Entries[s.Index].Dir {
				return fail("start of entry %d", s.Index)
			}
			e := r.m.Entries[s.Index]
			if s.Offset != 0 && s.Offset != asked[s.Index] {
				return fail("offset %d of %q", s.Offset, e.Path)
			}
			if traceOn {
				r.trStart(s.Index, s.Offset)
			}
			if f, err = r.open(e, s.Offset); err != nil {
				return Answer{}, err
			}
			cur, left, off = s.Index, e.Size-s.Offset, s.Offset
			tr = verified.Tree{Root: root(e), Size: e.Size}
		case frameTree:
			need := verified.OutboardSize(tr.Size)
			if f == nil || len(tr.Outboard)+len(p) > need {
				return fail("tree past its end in entry %d", cur)
			}
			tr.Outboard = append(tr.Outboard, p...)
			if len(tr.Outboard) == need && !oneChunk(tr.Size) { // kept for a later session's resume (keptTree)
				if err := r.sr.WriteFile(treeName(cur), tr.Outboard, 0o600); err != nil {
					_ = f.Close()
					return Answer{}, err
				}
			}
		case frameData:
			if f == nil || len(tr.Outboard) != verified.OutboardSize(tr.Size) || int64(len(p)) > left {
				return fail("data before the tree or past the end of entry %d", cur)
			}
			if len(q) == inFlight {
				if err := keep(); err != nil {
					return Answer{}, err
				}
			}
			// p is read into buf: it becomes the chunk's, the next frame is
			// read into a free buffer (no copy; docs/performance.md).
			buf = nil
			if n := len(free); n > 0 {
				buf, free = free[n-1][:0], free[:n-1]
			}
			pd, t := pending{p: p, off: off, ok: make(chan bool, 1)}, tr
			go func() { pd.ok <- t.Chunk(pd.p, pd.off) }()
			q = append(q, pd)
			left -= int64(len(p))
			off += int64(len(p))
		case frameEnd:
			var e end
			if err := json.Unmarshal(p, &e); err != nil || f == nil || e.Index != cur || left != 0 || len(tr.Outboard) != verified.OutboardSize(tr.Size) {
				return fail("end of entry %d", cur)
			}
			for len(q) > 0 {
				if err := keep(); err != nil {
					return Answer{}, err
				}
			}
			err := syncFile(f) // on stable storage before the commit begins (syncStage)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			f = nil
			if err != nil {
				return Answer{}, err
			}
			err = r.verify(r.m.Entries[cur], tr)
			if traceOn {
				r.trVerified(err == nil)
			}
			if err != nil {
				return Answer{}, err
			}
			a.Files++
			a.Bytes += r.m.Entries[cur].Size
			next = cur + 1
		case frameDone:
			if f != nil || a.Files != countFiles(r.m) {
				return fail("done with %d of %d files", a.Files, countFiles(r.m))
			}
			if err := r.commit(); err != nil { // verified: the stage stays for the next session
				return Answer{Message: err.Error()}, nil
			}
			a.OK = true
			return a, nil
		default:
			return fail("frame %q", t)
		}
	}
}

// inFlight bounds the chunks read and being verified, not yet written.
const inFlight = 8

type pending struct {
	p   []byte
	off int64
	ok  chan bool
}

// afterWrite, when set, runs after each chunk is written (a test lands one
// wrong).
var afterWrite func(f *os.File, off int64)

// open opens entry e's part, cut to off (the offset the sender resumes at).
func (r *receiver) open(e Entry, off int64) (*os.File, error) {
	if d := path.Dir(e.Path); d != "." {
		if err := r.sr.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	f, err := r.sr.OpenFile(e.Path, os.O_RDWR|os.O_CREATE, fs.FileMode(e.Mode)&0o755|0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Truncate(off); err == nil {
		_, err = f.Seek(off, io.SeekStart)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// verify checks every chunk of what is on disk against the root, in
// parallel (verified.Verify); a file that does not verify (a chunk that
// landed wrong) is cut to its verified prefix, which the next session
// resumes from (spec Finish).
func (r *receiver) verify(e Entry, tr verified.Tree) error {
	f, err := r.sr.OpenFile(e.Path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck
	n, whole, err := tr.Verify(f)
	if err != nil || whole {
		return err
	}
	if err := f.Truncate(n); err != nil {
		return err
	}
	return fmt.Errorf("%q: what landed is %w after %d bytes; the next session resumes there", e.Path, ErrUnverified, n)
}

// commit makes the directories, gives files their mtimes, syncs them,
// writes the intent record, and moves every top-level name into place;
// then syncs dir and removes the stage (spec Commit.tla Begin, Rename,
// Finish). Finishing a commit that stopped part way, it moves only what
// the stage still holds, each only while dir does not hold it (Recover).
// A rename that fails leaves things as they are: the next receive
// finishes (Stop).
// rename is os.Root.Rename (a test fails one).
var rename = renameNoReplace

func (r *receiver) commit() error {
	if !r.finishing {
		for _, e := range r.m.Entries {
			var err error
			if e.Dir {
				err = r.sr.MkdirAll(e.Path, 0o755)
			} else if e.MTime != 0 {
				t := time.Unix(0, e.MTime)
				err = r.sr.Chtimes(e.Path, t, t)
			}
			if err != nil {
				return err
			}
		}
		if err := syncStage(r.sr); err != nil {
			return err
		}
		if err := r.syncDirs(); err != nil {
			return err
		}
		if top := r.taken(); top != "" {
			if traceOn {
				r.trBegin(false)
			}
			return fmt.Errorf("%q %w", top, errExists)
		}
		if err := r.writeIntent(); err != nil {
			return err
		}
		if traceOn {
			r.trBegin(true)
		}
	}
	for i, top := range r.tops {
		_, err := r.sr.Lstat(top)
		switch {
		case errors.Is(err, fs.ErrNotExist): // moved before the stop
			err = r.placed(top)
		case err == nil:
			err = r.move(top)
		}
		if traceOn {
			r.trRename(i, err)
		}
		if err != nil {
			return err
		}
	}
	// Windows has no directory sync (FlushFileBuffers on a directory is
	// refused); NTFS journals the renames.
	if err := r.syncDir(r.root); err != nil { // the renames on stable storage
		if traceOn {
			r.trPend("Stop", -1)
		}
		return err
	}
	if traceOn {
		defer r.trAtomic()()
		r.trCommit("Finish", -1)
	}
	_ = r.sr.Close()
	r.sr = nil
	err := removeStage(r.root, r.stage, r.lk)
	r.lk = nil
	return err
}

// errExists is a top-level name the receiving directory already holds.
var errExists = errors.New("exists in the receiving directory")

// move renames top from the stage into place, never replacing a name the
// directory holds (spec Commit.tla Rename).
func (r *receiver) move(top string) error {
	if _, err := r.root.Lstat(top); !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%q %w", top, errExists)
	}
	err := rename(r.root, path.Join(r.stage, top), top)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%q %w", top, errExists)
	}
	return err
}

// placed checks that top, which a stopped commit moved into place, is still
// this offer's: every manifest entry under it is in dir, a directory as a
// directory, a file with the manifest's size and BLAKE3 root. Someone may have
// deleted it and made another since; that one is ErrNotOurs, and stays
// (review 2026-11-17 #2, finding B; spec Commit.tla Rename).
func (r *receiver) placed(top string) error {
	if _, err := r.root.Lstat(top); err != nil {
		return fmt.Errorf("%q is neither staged nor in place: %w", top, err)
	}
	for _, e := range r.m.Entries {
		if e.Path != top && !strings.HasPrefix(e.Path, top+"/") {
			continue
		}
		if !r.holds(e) {
			return fmt.Errorf("%q in the receiving directory is %w: its content is not the offered one", e.Path, ErrNotOurs)
		}
	}
	return nil
}

// holds reports whether dir holds entry e as the manifest has it.
func (r *receiver) holds(e Entry) bool {
	fi, err := r.root.Lstat(e.Path)
	switch {
	case err != nil:
		return false
	case e.Dir:
		return fi.IsDir()
	case !fi.Mode().IsRegular() || fi.Size() != e.Size:
		return false
	}
	f, err := r.root.Open(e.Path)
	if err != nil {
		return false
	}
	defer f.Close() //nolint:errcheck
	sum, err := verified.Sum(f)
	return err == nil && sum == root(e)
}

// writeIntent writes the intent record and puts it on stable storage.
func (r *receiver) writeIntent() error {
	b, err := json.Marshal(intent{Manifest: r.sum, Offer: r.raw})
	if err != nil {
		return err
	}
	f, err := r.sr.OpenFile(intentName, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return r.syncDir(r.sr)
}

// syncDir syncs root's directory; not on Windows.
func (r *receiver) syncDir(root *os.Root) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := root.Open(".")
	if err != nil {
		return err
	}
	err = d.Sync()
	_ = d.Close()
	return err
}

// syncDirs puts the offer's directories' entries on stable storage,
// deepest first, before the renames (review 2026-11-17, F5); not on
// Windows (see commit).
func (r *receiver) syncDirs() error {
	if runtime.GOOS == "windows" {
		return nil
	}
	var dirs []string
	seen := map[string]bool{}
	for _, e := range r.m.Entries {
		d := path.Dir(e.Path)
		if e.Dir {
			d = e.Path
		}
		if d != "." && !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	slices.SortFunc(dirs, func(a, b string) int { return strings.Count(b, "/") - strings.Count(a, "/") })
	for _, d := range dirs {
		f, err := r.sr.Open(d)
		if err != nil {
			return err
		}
		err = f.Sync()
		_ = f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func countBytes(m Manifest) (n int64) {
	for _, e := range m.Entries {
		n += e.Size
	}
	return n
}

func countFiles(m Manifest) int {
	n := 0
	for _, e := range m.Entries {
		if !e.Dir {
			n++
		}
	}
	return n
}
