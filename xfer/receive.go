// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package xfer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// StagePrefix starts the name of the directory a transfer is staged in,
// inside the receiving directory, until it commits; a cut transfer's stays
// there and the next receive of the same offer resumes from it.
const StagePrefix = ".nfltr-send-"

// ErrRefused is a transfer the receiver will not take; nothing changed.
var ErrRefused = errors.New("refused")

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
		return a, fmt.Errorf("%w: %v", ErrRefused, err)
	}
	defer r.close()
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
	a, err := r.run(c, progress)
	if err != nil {
		return a, err
	}
	if err := c.writeJSON(frameAnswer, a); err != nil {
		return a, err
	}
	return a, c.flush()
}

type receiver struct {
	m     Manifest
	root  *os.Root // dir
	stage string   // its name in dir
	sr    *os.Root // the stage
	tops  []string
}

// newReceiver checks the manifest (relative names inside dir, files and
// directories only, no name twice, no top-level name dir already holds)
// and opens the offer's stage, named by the manifest's digest.
func newReceiver(dir string, m Manifest, raw []byte) (*receiver, error) {
	if m.Version != Version {
		return nil, fmt.Errorf("protocol version %d, this nfltr-send speaks %d: update both ends", m.Version, Version)
	}
	r := &receiver{m: m}
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
		if e.Size < 0 || (e.Dir && e.Size != 0) {
			return nil, fmt.Errorf("%q: bad size", e.Path)
		}
		if top, _, _ := strings.Cut(name, "/"); !slices.Contains(r.tops, top) {
			r.tops = append(r.tops, top)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	r.root = root
	if top := r.taken(); top != "" {
		_ = root.Close()
		return nil, fmt.Errorf("%q exists in the receiving directory", top)
	}
	sum := sha256.Sum256(raw)
	r.stage = StagePrefix + hex.EncodeToString(sum[:8])
	if err := root.Mkdir(r.stage, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		_ = root.Close()
		return nil, err
	}
	if r.sr, err = root.OpenRoot(r.stage); err != nil {
		_ = root.Close()
		return nil, err
	}
	return r, nil
}

func (r *receiver) close() {
	_ = r.sr.Close()
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

// parts are the files the stage holds a part of, each with its SHA-256.
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
		f, err := r.sr.Open(e.Path)
		if err != nil {
			return nil, err
		}
		sum, err := prefixSum(f, min(fi.Size(), e.Size))
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		if sum != "" {
			out = append(out, Part{Index: i, Offset: min(fi.Size(), e.Size), SHA256: sum})
		}
	}
	return out, nil
}

// run takes the files, verifies each from disk, and commits the whole
// offer once every file is verified (spec Finish).
func (r *receiver) run(c *conn, progress Progress) (Answer, error) {
	var (
		buf     []byte
		f       *os.File
		cur     = -1 // the entry being received
		left    int64
		next    int // entries before it are done
		written int64
		a       Answer
	)
	fail := func(format string, args ...any) (Answer, error) {
		if f != nil {
			_ = f.Close()
		}
		return Answer{}, fmt.Errorf("%w: "+format, append([]any{ErrProtocol}, args...)...)
	}
	for {
		t, p, err := c.read(buf)
		if err != nil {
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
			if s.Offset < 0 || s.Offset > e.Size {
				return fail("offset %d of %q", s.Offset, e.Path)
			}
			if f, err = r.open(e, s.Offset); err != nil {
				return Answer{}, err
			}
			cur, left = s.Index, e.Size-s.Offset
		case frameData:
			if f == nil || int64(len(p)) > left {
				return fail("data past the end of entry %d", cur)
			}
			if _, err := f.Write(p); err != nil {
				_ = f.Close()
				return Answer{}, err
			}
			left -= int64(len(p))
			written += int64(len(p))
			if progress != nil {
				progress(written)
			}
		case frameEnd:
			var e end
			if err := json.Unmarshal(p, &e); err != nil || f == nil || e.Index != cur || left != 0 {
				return fail("end of entry %d", cur)
			}
			err := f.Sync() // on stable storage before it can be committed
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			f = nil
			if err != nil {
				return Answer{}, err
			}
			if err := r.verify(r.m.Entries[cur], e.SHA256); err != nil {
				return Answer{Message: err.Error()}, nil
			}
			a.Files++
			a.Bytes += r.m.Entries[cur].Size
			next = cur + 1
		case frameDone:
			if f != nil || a.Files != countFiles(r.m) {
				return fail("done with %d of %d files", a.Files, countFiles(r.m))
			}
			if err := r.commit(); err != nil {
				return Answer{Message: err.Error()}, nil
			}
			a.OK = true
			return a, nil
		default:
			return fail("frame %q", t)
		}
	}
}

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

// verify compares the SHA-256 of what is on disk with the sender's; an
// unequal part is removed, so the next attempt sends it whole.
func (r *receiver) verify(e Entry, want string) error {
	f, err := r.sr.Open(e.Path)
	if err != nil {
		return err
	}
	got, err := prefixSum(f, e.Size)
	_ = f.Close()
	if err == nil && got == want {
		return nil
	}
	_ = r.sr.Remove(e.Path)
	if err != nil {
		return err
	}
	return fmt.Errorf("%q: SHA-256 of what landed is not the sender's; removed, the next receive sends it again", e.Path)
}

// commit makes the directories, gives files their mtimes, and moves every
// top-level name into place, each only while dir still does not hold it;
// then syncs dir and removes the stage.
func (r *receiver) commit() error {
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
	if top := r.taken(); top != "" {
		return fmt.Errorf("%q exists in the receiving directory", top)
	}
	for _, top := range r.tops {
		if err := r.root.Rename(path.Join(r.stage, top), top); err != nil {
			return err
		}
	}
	// ponytail: the receiving directory is synced, not the stage's nested
	// directories; a power loss can still lose a nested entry's name.
	if d, err := r.root.Open("."); err == nil { // the renames on stable storage
		err = d.Sync()
		_ = d.Close()
		if err != nil {
			return err
		}
	}
	return r.root.RemoveAll(r.stage)
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
