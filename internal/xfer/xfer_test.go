// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package xfer

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nfltr/send/internal/xfer/verified"
)

// tree makes src/data (a directory: a file of n random bytes, a small
// one, an empty directory) and returns src/data.
func tree(t *testing.T, n int) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "data")
	must(t, os.MkdirAll(filepath.Join(d, "sub", "empty"), 0o755))
	big := make([]byte, n)
	_, _ = rand.Read(big)
	must(t, os.WriteFile(filepath.Join(d, "big.bin"), big, 0o644))
	must(t, os.WriteFile(filepath.Join(d, "sub", "small.txt"), []byte("small"), 0o600))
	return d
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// digest is one SHA-256 over every name and file under dir.
func digest(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	must(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		io.WriteString(h, rel+"\n")
		if d.Type().IsRegular() {
			b, err := os.ReadFile(p)
			h.Write(b)
			return err
		}
		return nil
	}))
	return string(h.Sum(nil))
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	must(t, err)
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

type result struct {
	a    Answer
	sent int64
	err  error
}

// session runs one transfer of s into dir over a pipe whose sender side is
// cut (closed) after cut bytes (0: never); it returns both ends' results.
func session(t *testing.T, s *Source, dir string, cut int64) (result, result) {
	t.Helper()
	a, b := net.Pipe()
	var w io.ReadWriter = a
	if cut > 0 {
		w = &cutter{Conn: a, left: cut}
	}
	var sr result
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sr.a, sr.sent, sr.err = s.Serve(w)
		_ = a.Close()
	}()
	var rr result
	rr.a, rr.err = Receive(b, dir, nil)
	_ = b.Close()
	wg.Wait()
	return sr, rr
}

// cutter closes the connection, as a cleanly ended session does, once it
// has written left bytes: the reader sees EOF there.
type cutter struct {
	net.Conn
	mu   sync.Mutex
	left int64
}

func (c *cutter) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.left <= 0 {
		return 0, io.ErrClosedPipe
	}
	if int64(len(p)) > c.left {
		n, _ := c.Conn.Write(p[:c.left])
		c.left = 0
		_ = c.Conn.Close()
		return n, io.ErrClosedPipe
	}
	n, err := c.Conn.Write(p)
	c.left -= int64(n)
	return n, err
}

func TestTransferLandsWhole(t *testing.T) {
	src := tree(t, 3<<20)
	s, err := NewSource([]string{src})
	must(t, err)
	dst := t.TempDir()
	sr, rr := session(t, s, dst, 0)
	if sr.err != nil || rr.err != nil || !sr.a.OK || !rr.a.OK || rr.a.Files != 2 || rr.a.Bytes != 3<<20+5 {
		t.Fatalf("sender %+v, receiver %+v", sr, rr)
	}
	if digest(t, src) != digest(t, filepath.Join(dst, "data")) {
		t.Fatal("what landed differs from the source")
	}
	if got := names(t, dst); !slices.Equal(got, []string{"data"}) {
		t.Fatalf("dst holds %q, want only data (no stage left)", got)
	}
	if fi, err := os.Stat(filepath.Join(dst, "data", "sub", "empty")); err != nil || !fi.IsDir() {
		t.Fatal("the empty directory did not land")
	}
	want, _ := os.Stat(filepath.Join(src, "big.bin"))
	if got, err := os.Stat(filepath.Join(dst, "data", "big.bin")); err != nil || !got.ModTime().Equal(want.ModTime()) {
		t.Fatal("a file did not keep its mtime")
	}
}

// A cut session commits nothing; the next resumes from the stage's part
// and sends only the rest (spec Open, WitnessResumedCommit); a session cut
// cleanly at a file's end is a cut too (mutant commit-cut).
func TestCutTransferResumes(t *testing.T) {
	src := tree(t, 4<<20)
	s, err := NewSource([]string{src})
	must(t, err)
	frame := func(v any) int64 { b, _ := json.Marshal(v); return 5 + int64(len(b)) }
	afterBig := frame(s.Manifest) + frame(start{Index: 1}) + 5 + int64(verified.OutboardSize(4<<20)) + 16*(5+maxData) + frame(end{Index: 1})
	for _, cut := range []int64{1 << 20, afterBig} { // inside big.bin; right after its end frame
		dst := t.TempDir()
		_, rr := session(t, s, dst, cut)
		if rr.err == nil || rr.a.OK {
			t.Fatalf("cut at %d: receiver %+v, want an error", cut, rr)
		}
		if got := names(t, dst); len(got) != 1 || !strings.HasPrefix(got[0], StagePrefix) {
			t.Fatalf("cut at %d: dst holds %q, want only the stage", cut, got)
		}
		sr, rr := session(t, s, dst, 0)
		if rr.err != nil || !rr.a.OK || sr.sent >= 4<<20 {
			t.Fatalf("cut at %d, resumed: sender %+v, receiver %+v (want fewer than 4 MiB sent again)", cut, sr, rr)
		}
		if digest(t, src) != digest(t, filepath.Join(dst, "data")) {
			t.Fatalf("cut at %d: resumed transfer differs from the source", cut)
		}
	}
}

// A part is verified chunk by chunk before it is resumed: a corrupted
// chunk (a torn write, another program) is sent again with the chunks
// that are missing; every chunk that verifies is kept, also after it (spec
// Open; mutants resume-unverified, resend-verified).
func TestResumeOfACorruptedPartKeepsItsVerifiedChunks(t *testing.T) {
	src := tree(t, 2<<20)
	s, err := NewSource([]string{src})
	must(t, err)
	for _, at := range []int{10, 2*maxData + 10} {
		dst := t.TempDir()
		_, _ = session(t, s, dst, 1<<20)
		stage := filepath.Join(dst, names(t, dst)[0])
		part := filepath.Join(stage, "data", "big.bin")
		b, err := os.ReadFile(part)
		must(t, err)
		b[at] ^= 0xff
		must(t, os.WriteFile(part, b, 0o600))
		sr, rr := session(t, s, dst, 0)
		if want := int64(2<<20 + 5 - (len(b)/maxData-1)*maxData); rr.err != nil || !rr.a.OK || sr.sent != want {
			t.Fatalf("corrupted at %d: sender %+v, receiver %+v, want %d bytes sent", at, sr, rr, want)
		}
		if digest(t, src) != digest(t, filepath.Join(dst, "data")) {
			t.Fatal("differs from the source")
		}
	}
}

// corrupter flips one byte of what the sender writes, at stream offset at.
type corrupter struct {
	net.Conn
	at int64
}

func (c *corrupter) Write(p []byte) (int, error) {
	if 0 <= c.at && c.at < int64(len(p)) {
		p = bytes.Clone(p)
		p[c.at] ^= 1
	}
	c.at -= int64(len(p))
	return c.Conn.Write(p)
}

// A chunk that does not verify against the manifest's root is refused at
// once: the session ends (ErrUnverified), the chunks verified before it
// stay, nothing is committed, and the next session resumes after them
// (spec Bad, Kept; mutant accept-unverified).
func TestUnverifiedChunkIsRefused(t *testing.T) {
	src := tree(t, 4<<20)
	s, err := NewSource([]string{src})
	must(t, err)
	frame := func(v any) int64 { b, _ := json.Marshal(v); return 5 + int64(len(b)) }
	chunk3 := frame(s.Manifest) + frame(start{Index: 1}) + 5 + int64(verified.OutboardSize(4<<20)) + 2*(5+maxData) + 5 + 100
	dst := t.TempDir()
	x, y := net.Pipe()
	go func() { _, _, _ = s.Serve(&corrupter{Conn: x, at: chunk3}); _ = x.Close() }()
	a, err := Receive(y, dst, nil)
	_ = y.Close()
	if !errors.Is(err, ErrUnverified) || a.OK {
		t.Fatalf("receiver %+v, %v: want ErrUnverified", a, err)
	}
	part := filepath.Join(dst, names(t, dst)[0], "data", "big.bin")
	if fi, err := os.Stat(part); err != nil || fi.Size() != 2*maxData {
		t.Fatalf("part %v, %v: want the two chunks before the refused one", fi, err)
	}
	sr, rr := session(t, s, dst, 0)
	if rr.err != nil || !rr.a.OK || sr.sent != 4<<20+5-2*maxData {
		t.Fatalf("resumed: sender %+v, receiver %+v", sr, rr)
	}
	if digest(t, src) != digest(t, filepath.Join(dst, "data")) {
		t.Fatal("differs from the source")
	}
}

// A manifest's root that the sender's bytes do not match (a sender
// whose file is not the offered one) refuses the first chunk: nothing is
// written, nothing committed (spec Bad, mutant root-unbound: the root is
// the pinned manifest's, not the tree the session carries).
func TestWrongRootCommitsNothing(t *testing.T) {
	dst := t.TempDir()
	tr, _ := verified.New(strings.NewReader("abc"), 3)
	a, b := net.Pipe()
	go func() {
		c := newConn(a)
		_ = c.writeJSON(frameManifest, Manifest{Version: Version, Entries: []Entry{{Path: "f", Size: 3, Mode: 0o644, BLAKE3: strings.Repeat("0", 64)}}})
		_ = c.flush()
		var w want
		_ = c.readJSON(frameWant, &w)
		_ = c.writeJSON(frameStart, start{Index: 0})
		_ = c.write(frameTree, tr.Outboard)
		_ = c.write(frameData, []byte("abc"))
		_ = c.writeJSON(frameEnd, end{Index: 0})
		_ = c.write(frameDone, nil)
		_ = c.flush()
		_, _ = io.Copy(io.Discard, a)
	}()
	ans, err := Receive(b, dst, nil)
	_ = b.Close()
	if !errors.Is(err, ErrUnverified) || ans.OK {
		t.Fatalf("answer %+v, %v: want ErrUnverified", ans, err)
	}
	got := names(t, dst)
	if len(got) != 1 || !strings.HasPrefix(got[0], StagePrefix) {
		t.Fatalf("dst holds %q, want only the stage", got)
	}
	if fi, err := os.Stat(filepath.Join(dst, got[0], "f")); err != nil || fi.Size() != 0 {
		t.Fatalf("part %v, %v: want nothing written", fi, err)
	}
}

// A chunk that lands wrong on the receiver's disk after it verified is
// caught by the file's BLAKE3 over what is on disk: nothing is committed,
// the file is kept and the next session sends that chunk only (spec
// Finish, mutant no-final-check).
func TestChunkThatLandedWrongIsCaught(t *testing.T) {
	src := tree(t, 2<<20)
	s, err := NewSource([]string{src})
	must(t, err)
	dst := t.TempDir()
	var stage string
	afterWrite = func(f *os.File, off int64) {
		if off == maxData { // the second chunk lands wrong
			_, _ = f.WriteAt([]byte{0xff, 0xfe}, off+7)
			stage = filepath.Dir(filepath.Dir(f.Name()))
		}
	}
	_, rr := session(t, s, dst, 0)
	afterWrite = nil
	if !errors.Is(rr.err, ErrUnverified) || rr.a.OK {
		t.Fatalf("receiver %+v: want ErrUnverified", rr)
	}
	if fi, err := os.Stat(filepath.Join(stage, "data", "big.bin")); err != nil || fi.Size() != 2<<20 {
		t.Fatalf("part %v, %v: want the whole file kept", fi, err)
	}
	sr, rr := session(t, s, dst, 0) // the small file landed before the final check: kept
	if rr.err != nil || !rr.a.OK || sr.sent != maxData {
		t.Fatalf("resumed: sender %+v, receiver %+v", sr, rr)
	}
	if digest(t, src) != digest(t, filepath.Join(dst, "data")) {
		t.Fatal("differs from the source")
	}
}

// A stage nobody wrote to for StageLife is an abandoned offer's: the next
// receive into that directory removes it, and keeps fresh stages (another
// offer's, resumable) and names that are not stages.
func TestStaleStagesAreRemoved(t *testing.T) {
	dst := t.TempDir()
	old := time.Now().Add(-StageLife - time.Hour)
	for _, name := range []string{StagePrefix + "old", StagePrefix + "fresh", ".other"} {
		f := filepath.Join(dst, name, "sub", "part")
		must(t, os.MkdirAll(filepath.Dir(f), 0o700))
		must(t, os.WriteFile(f, []byte("x"), 0o600))
		if name == StagePrefix+"fresh" {
			continue
		}
		for _, p := range []string{f, filepath.Dir(f), filepath.Join(dst, name)} {
			must(t, os.Chtimes(p, old, old))
		}
	}
	s, err := NewSource([]string{tree(t, 1<<10)})
	must(t, err)
	if _, rr := session(t, s, dst, 0); rr.err != nil || !rr.a.OK {
		t.Fatalf("receiver %+v", rr)
	}
	if got := names(t, dst); !slices.Equal(got, []string{StagePrefix + "fresh", ".other", "data"}) {
		t.Fatalf("dst holds %q, want the fresh stage, .other and data", got)
	}
}

// Names outside dir, an existing top-level name, a duplicate: refused
// before anything is sent; dir unchanged; the sender gets the answer.
func TestReceiverRefusesUnsafeOffers(t *testing.T) {
	dst := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dst, "taken"), nil, 0o600))
	for name, es := range map[string][]Entry{
		"dotdot":     {{Path: "../x", Size: 1}},
		"inner":      {{Path: "a/../../x", Size: 1}},
		"absolute":   {{Path: "/etc/x", Size: 1}},
		"backslash":  {{Path: `a\..\x`, Size: 1}},
		"unclean":    {{Path: "a//x", Size: 1}},
		"stage-name": {{Path: StagePrefix + "x", Size: 1}},
		"taken":      {{Path: "taken", Size: 1}},
		"twice":      {{Path: "x", Size: 1}, {Path: "x", Size: 1}},
		"negative":   {{Path: "x", Size: -1}},
		"huge":       {{Path: "x", Size: 1<<50 + 1}},
		"no-digest":  {{Path: "x", Size: 1}},
		"bad-digest": {{Path: "x", Size: 1, BLAKE3: "00"}},
	} {
		for i := range es { // a valid root, so each is refused for its own reason
			if es[i].BLAKE3 == "" && name != "no-digest" {
				es[i].BLAKE3 = strings.Repeat("0", 64)
			}
		}
		a, b := net.Pipe()
		got := make(chan Answer, 1)
		go func() {
			c := newConn(a)
			_ = c.writeJSON(frameManifest, Manifest{Version: Version, Entries: es})
			_ = c.flush()
			t, p, _ := c.read(nil)
			var ans Answer
			if t == frameAnswer {
				_ = json.Unmarshal(p, &ans)
			}
			got <- ans
		}()
		_, err := Receive(b, dst, nil)
		ans := <-got
		_ = a.Close()
		_ = b.Close()
		if err == nil || ans.OK || ans.Message == "" {
			t.Errorf("%s: err %v, sender got %+v: want refused with a reason", name, err, ans)
		}
		if got := names(t, dst); !slices.Equal(got, []string{"taken"}) {
			t.Errorf("%s: dst holds %q", name, got)
		}
	}
}

// A file changed after it was offered is not sent as the offered one.
func TestSenderRefusesAChangedFile(t *testing.T) {
	src := tree(t, 1<<10)
	s, err := NewSource([]string{src})
	must(t, err)
	must(t, os.WriteFile(filepath.Join(src, "big.bin"), bytes.Repeat([]byte("x"), 1<<10+1), 0o644))
	sr, rr := session(t, s, t.TempDir(), 0)
	if sr.err == nil || !strings.Contains(sr.err.Error(), "changed") || rr.a.OK {
		t.Fatalf("sender %+v, receiver %+v: want the change refused", sr, rr)
	}
}

func TestNewSourceRefusesLinksAndDuplicates(t *testing.T) {
	d := t.TempDir()
	must(t, os.WriteFile(filepath.Join(d, "f"), nil, 0o600))
	must(t, os.Symlink("f", filepath.Join(d, "l")))
	if _, err := NewSource([]string{d}); err == nil {
		t.Fatal("a symlink in a source was taken")
	}
	other := filepath.Join(t.TempDir(), "f")
	must(t, os.WriteFile(other, nil, 0o600))
	if _, err := NewSource([]string{filepath.Join(d, "f"), other}); err == nil {
		t.Fatal("two sources named f were taken")
	}
}

// BenchmarkTransfer is one 64 MiB file over a pipe: the protocol's own cost
// (framing, SHA-256 on both sides, the receiver's verify read), no network.
func BenchmarkTransfer(b *testing.B) {
	d := filepath.Join(b.TempDir(), "f")
	buf := make([]byte, 64<<20)
	_, _ = rand.Read(buf)
	if err := os.WriteFile(d, buf, 0o600); err != nil {
		b.Fatal(err)
	}
	s, err := NewSource([]string{d})
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(64 << 20)
	for b.Loop() {
		dst := b.TempDir()
		x, y := net.Pipe()
		go func() { _, _, _ = s.Serve(x); _ = x.Close() }()
		if a, err := Receive(y, dst, nil); err != nil || !a.OK {
			b.Fatal(a, err)
		}
		_ = y.Close()
	}
}

// A commit whose rename fails part way keeps the verified stage and its
// intent record: the next session resends nothing and commits every
// top-level entry (review 2026-11-17, finding 1: the first rename landed,
// the stage was deleted, the retry was refused).
func TestFailedCommitKeepsTheStage(t *testing.T) {
	base := t.TempDir()
	var srcs []string
	for _, n := range []string{"a.txt", "b.txt", "c.txt"} {
		p := filepath.Join(base, n)
		must(t, os.WriteFile(p, []byte("content of "+n), 0o644))
		srcs = append(srcs, p)
	}
	s, err := NewSource(srcs)
	must(t, err)
	dst := t.TempDir()
	calls := 0
	rename = func(r *os.Root, old, new string) error {
		if calls++; calls == 2 {
			return errors.New("planted rename failure")
		}
		return renameNoReplace(r, old, new)
	}
	defer func() { rename = renameNoReplace }()
	_, rr := session(t, s, dst, 0)
	if rr.a.OK {
		t.Fatalf("receiver %+v: a failed commit answered ok", rr)
	}
	if got := len(names(t, dst)); got != 2 { // a.txt and the stage
		t.Fatalf("after a failed commit dst holds %q", names(t, dst))
	}
	rename = renameNoReplace
	sr, rr := session(t, s, dst, 0)
	if sr.err != nil || rr.err != nil || !rr.a.OK || sr.sent != 0 {
		t.Fatalf("retry: sender %+v, receiver %+v (want ok, nothing resent)", sr, rr)
	}
	if got := names(t, dst); !slices.Equal(got, []string{"a.txt", "b.txt", "c.txt"}) {
		t.Fatalf("dst holds %q", got)
	}
}

// rewriter rewrites file in place (other bytes, the same size and mtime)
// once the sender has written after bytes to the session.
type rewriter struct {
	net.Conn
	t     *testing.T
	file  string
	after int64
	done  bool
}

func (w *rewriter) Write(p []byte) (int, error) {
	if w.after -= int64(len(p)); w.after < 0 && !w.done {
		w.done = true
		fi, err := os.Stat(w.file)
		must(w.t, err)
		b := make([]byte, fi.Size())
		_, _ = rand.Read(b)
		must(w.t, os.WriteFile(w.file, b, 0o644))
		must(w.t, os.Chtimes(w.file, fi.ModTime(), fi.ModTime()))
	}
	return w.Conn.Write(p)
}

// A file rewritten in place while it is sent, its size and mtime
// restored, is refused by the receiver at its first chunk that is not the
// offered one (ErrUnverified) rather than landed mixed (review
// 2026-11-17, finding F2; spec Change, Bad).
func TestSenderRefusesAFileRewrittenWhileSent(t *testing.T) {
	f := filepath.Join(t.TempDir(), "f.bin")
	b := make([]byte, 4<<20)
	_, _ = rand.Read(b)
	must(t, os.WriteFile(f, b, 0o644))
	s, err := NewSource([]string{f})
	must(t, err)
	dst := t.TempDir()
	x, y := net.Pipe()
	errc := make(chan error, 1)
	go func() {
		_, _, err := s.Serve(&rewriter{Conn: x, t: t, file: f, after: 1 << 20})
		_ = x.Close()
		errc <- err
	}()
	a, rerr := Receive(y, dst, nil)
	_ = y.Close()
	if err := <-errc; err == nil || !errors.Is(rerr, ErrUnverified) || a.OK {
		t.Fatalf("sender %v, receiver %+v, %v: want ErrUnverified and nothing committed", err, a, rerr)
	}
	if _, err := os.Lstat(filepath.Join(dst, "f.bin")); err == nil {
		t.Fatal("a file changed while sent landed")
	}
}

// A receiver that dies after some of a commit's renames (runtime.Goexit
// in the rename seam: no code of commit runs after it) leaves the intent
// record; the next receive of the same offer finishes the commit, sending
// nothing again (spec Commit.tla Recover, WitnessRecoveredAfterCrash).
func TestCommitStoppedByACrashIsFinished(t *testing.T) {
	base := t.TempDir()
	var srcs []string
	for _, n := range []string{"a.txt", "b", "c.txt"} {
		p := filepath.Join(base, n)
		if n == "b" {
			must(t, os.MkdirAll(filepath.Join(p, "in"), 0o755))
			must(t, os.WriteFile(filepath.Join(p, "in", "f"), []byte("nested"), 0o644))
		} else {
			must(t, os.WriteFile(p, []byte("content of "+n), 0o644))
		}
		srcs = append(srcs, p)
	}
	s, err := NewSource(srcs)
	must(t, err)
	for k := 1; k <= 2; k++ {
		dst := t.TempDir()
		calls := 0
		rename = func(r *os.Root, old, new string) error {
			if calls++; calls > k {
				runtime.Goexit() // the receiver dies here
			}
			return renameNoReplace(r, old, new)
		}
		x, y := net.Pipe()
		go func() { _, _, _ = s.Serve(x) }()
		done := make(chan struct{})
		go func() { defer close(done); _, _ = Receive(y, dst, nil) }()
		<-done
		_ = y.Close()
		_ = x.Close()
		rename = renameNoReplace
		if got := len(names(t, dst)); got != k+1 { // k entries and the stage
			t.Fatalf("after a crash at rename %d: dst holds %q", k+1, names(t, dst))
		}
		sr, rr := session(t, s, dst, 0)
		if sr.err != nil || rr.err != nil || !rr.a.OK || sr.sent != 0 || rr.a.Files != 3 {
			t.Fatalf("crash at rename %d, then: sender %+v, receiver %+v (want ok, nothing resent)", k+1, sr, rr)
		}
		if got := names(t, dst); !slices.Equal(got, []string{"a.txt", "b", "c.txt"}) {
			t.Fatalf("dst holds %q", got)
		}
		if b, err := os.ReadFile(filepath.Join(dst, "b", "in", "f")); err != nil || string(b) != "nested" {
			t.Fatalf("b/in/f: %q, %v", b, err)
		}
	}
}

type countingReader struct {
	r io.Reader
	n *int64
}

func (c countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	*c.n += int64(n)
	return n, err
}

// A session reads each file once: the manifest carries its root and the
// sender its tree (taken when it was offered); the receiver checks the
// bytes served against them.
func TestServeReadsEachFileOnce(t *testing.T) {
	src := tree(t, 1<<20)
	s, err := NewSource([]string{src})
	must(t, err)
	var n int64
	readFile = func(f *os.File) io.Reader { return countingReader{f, &n} }
	defer func() { readFile = func(f *os.File) io.Reader { return f } }()
	sr, rr := session(t, s, t.TempDir(), 0)
	if sr.err != nil || !rr.a.OK {
		t.Fatalf("sender %+v, receiver %+v", sr, rr)
	}
	if n != 1<<20+5 {
		t.Fatalf("read %d bytes of files for %d", n, 1<<20+5)
	}
	for _, e := range s.Manifest.Entries {
		if !e.Dir && len(e.BLAKE3) != 64 {
			t.Fatalf("%s: no digest in the manifest", e.Path)
		}
	}
}

// threeTops offers a.txt, b/in/f and c.txt.
func threeTops(t *testing.T) *Source {
	base := t.TempDir()
	must(t, os.WriteFile(filepath.Join(base, "a.txt"), []byte("content of a"), 0o644))
	must(t, os.MkdirAll(filepath.Join(base, "b", "in"), 0o755))
	must(t, os.WriteFile(filepath.Join(base, "b", "in", "f"), []byte("nested"), 0o644))
	must(t, os.WriteFile(filepath.Join(base, "c.txt"), []byte("content of c"), 0o644))
	s, err := NewSource([]string{filepath.Join(base, "a.txt"), filepath.Join(base, "b"), filepath.Join(base, "c.txt")})
	must(t, err)
	return s
}

// crashAfter receives s into dst and dies after k renames of its commit.
func crashAfter(t *testing.T, s *Source, dst string, k int) {
	calls := 0
	rename = func(r *os.Root, old, new string) error {
		if calls++; calls > k {
			runtime.Goexit()
		}
		return renameNoReplace(r, old, new)
	}
	defer func() { rename = renameNoReplace }()
	x, y := net.Pipe()
	go func() { _, _, _ = s.Serve(x) }()
	done := make(chan struct{})
	go func() { defer close(done); _, _ = Receive(y, dst, nil) }()
	<-done
	_ = y.Close()
	_ = x.Close()
}

// A stale stage holding its intent record (a commit stopped part way, its
// offer never received again) is finished by the next receive's sweep, not
// removed with its verified entries (spec Commit.tla Expire, mutant
// sweep-deletes-intent).
func TestStaleStageWithIntentIsFinished(t *testing.T) {
	dst := t.TempDir()
	crashAfter(t, threeTops(t), dst, 1)
	old := time.Now().Add(-StageLife - time.Hour)
	must(t, filepath.WalkDir(dst, func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.Contains(p, StagePrefix) {
			err = os.Chtimes(p, old, old)
		}
		return err
	}))
	other := filepath.Join(t.TempDir(), "other.txt")
	must(t, os.WriteFile(other, []byte("another offer"), 0o644))
	s2, err := NewSource([]string{other})
	must(t, err)
	if sr, rr := session(t, s2, dst, 0); !rr.a.OK {
		t.Fatalf("another offer: sender %+v, receiver %+v", sr, rr)
	}
	if got := names(t, dst); !slices.Equal(got, []string{"a.txt", "b", "c.txt", "other.txt"}) {
		t.Fatalf("dst holds %q, want the stopped offer finished and its stage gone", got)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "c.txt")); err != nil || string(b) != "content of c" {
		t.Fatalf("c.txt: %q, %v", b, err)
	}
}

// A name that appears in the receiving directory between the commit's check
// and its rename is kept, not replaced; the commit stops with the stage and
// its intent kept (spec Commit.tla Rename, mutant replace).
func TestRenameKeepsANameThatAppeared(t *testing.T) {
	dst := t.TempDir()
	s := threeTops(t)
	rename = func(r *os.Root, old, new string) error {
		if new == "c.txt" {
			must(t, os.WriteFile(filepath.Join(dst, "c.txt"), []byte("someone else's"), 0o600))
		}
		return renameNoReplace(r, old, new)
	}
	defer func() { rename = renameNoReplace }()
	_, rr := session(t, s, dst, 0)
	if rr.a.OK || !strings.Contains(rr.a.Message, "exists") {
		t.Fatalf("receiver %+v: want the commit stopped, the name existing", rr)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "c.txt")); err != nil || string(b) != "someone else's" {
		t.Fatalf("c.txt: %q, %v: replaced", b, err)
	}
	if got := names(t, dst); len(got) != 4 { // a.txt, b, c.txt, the stage
		t.Fatalf("dst holds %q, want the stage kept", got)
	}
}

// A second receive of an offer another receive is taking into the same
// directory refuses, busy, naming the stage, and changes nothing; once the
// first is cut, the next receive resumes from its stage (review 2026-11-17
// #2, finding A; spec Commit.tla Open, mutant no-lock).
func TestConcurrentReceiveOfAnOfferIsBusy(t *testing.T) {
	src := tree(t, 4<<20) // more chunks than are in flight: the cut lands mid-file
	s, err := NewSource([]string{src})
	must(t, err)
	dst := t.TempDir()
	x, y, resume, first := pausedReceive(t, s, dst)

	sr, rr := session(t, s, dst, 0)
	var busy *StageBusyError
	if !errors.Is(rr.err, ErrRefused) || !errors.As(rr.err, &busy) || !strings.HasPrefix(busy.Stage, StagePrefix) || !strings.Contains(rr.a.Message, busy.Stage) {
		t.Fatalf("second receive: sender %+v, receiver %+v; want refused, busy naming the stage", sr, rr)
	}
	if got := names(t, dst); len(got) != 1 || !strings.HasPrefix(got[0], StagePrefix) {
		t.Fatalf("dst holds %q, want only the first receive's stage", got)
	}

	_ = x.Close() // the first receive is cut
	_ = y.Close()
	close(resume)
	if err := <-first; err == nil {
		t.Fatal("the cut receive ended without an error")
	}
	sr, rr = session(t, s, dst, 0)
	if sr.err != nil || rr.err != nil || !rr.a.OK || sr.sent >= 4<<20 {
		t.Fatalf("after the cut: sender %+v, receiver %+v; want ok, resumed", sr, rr)
	}
	if digest(t, filepath.Join(dst, "data")) != digest(t, src) {
		t.Fatal("received tree differs")
	}
}

// pausedReceive starts a receive of s into dst and returns once it holds
// the stage, part way through its first file; closing resume lets it go on,
// and first gets its error.
func pausedReceive(t *testing.T, s *Source, dst string) (x, y net.Conn, resume chan struct{}, first chan error) {
	x, y = net.Pipe()
	go func() { _, _, _ = s.Serve(x) }()
	paused, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	first = make(chan error, 1)
	go func() {
		_, err := Receive(y, dst, func(int64) { once.Do(func() { close(paused); <-resume }) })
		first <- err
	}()
	<-paused
	t.Cleanup(func() { _ = x.Close(); _ = y.Close() })
	return x, y, resume, first
}

// The sweep of another offer's receive skips a stale stage whose lock a
// receive holds (spec Commit.tla Expire).
func TestSweepSkipsAHeldStage(t *testing.T) {
	src := tree(t, 1<<20)
	s, err := NewSource([]string{src})
	must(t, err)
	dst := t.TempDir()
	_, _, resume, first := pausedReceive(t, s, dst)
	old := time.Now().Add(-StageLife - time.Hour)
	must(t, filepath.WalkDir(dst, func(p string, d fs.DirEntry, err error) error {
		if err == nil && p != dst {
			err = os.Chtimes(p, old, old)
		}
		return err
	}))
	other := filepath.Join(t.TempDir(), "other.txt")
	must(t, os.WriteFile(other, []byte("another offer"), 0o644))
	s2, err := NewSource([]string{other})
	must(t, err)
	if sr, rr := session(t, s2, dst, 0); !rr.a.OK {
		t.Fatalf("another offer: sender %+v, receiver %+v", sr, rr)
	}
	close(resume)
	if err := <-first; err != nil {
		t.Fatalf("the held receive, after another's sweep: %v", err)
	}
	if digest(t, filepath.Join(dst, "data")) != digest(t, src) {
		t.Fatal("received tree differs")
	}
}

// Recovery takes an entry the stage no longer holds as moved only when what
// the directory holds there has the manifest's content: one deleted and
// made again by someone else between the crash and the recovery refuses
// the commit and stays (review 2026-11-17 #2, finding B; spec Commit.tla
// Rename, mutant trust-moved).
func TestRecoveryRefusesAReplacedEntry(t *testing.T) {
	for _, tc := range []struct{ name, path, body string }{
		{"file", "a.txt", "content of A"},               // same size, other bytes
		{"dir", filepath.Join("b", "in", "f"), "other"}, // a file under a moved directory
	} {
		t.Run(tc.name, func(t *testing.T) {
			dst := t.TempDir()
			s := threeTops(t)
			crashAfter(t, s, dst, 2) // a.txt and b moved, c.txt staged
			must(t, os.RemoveAll(filepath.Join(dst, strings.Split(filepath.ToSlash(tc.path), "/")[0])))
			must(t, os.MkdirAll(filepath.Dir(filepath.Join(dst, tc.path)), 0o755))
			must(t, os.WriteFile(filepath.Join(dst, tc.path), []byte(tc.body), 0o600))
			_, rr := session(t, s, dst, 0)
			if rr.a.OK || !strings.Contains(rr.a.Message, ErrNotOurs.Error()) {
				t.Fatalf("receiver %+v: want the recovery refused, the entry not ours", rr)
			}
			if b, err := os.ReadFile(filepath.Join(dst, tc.path)); err != nil || string(b) != tc.body {
				t.Fatalf("%s: %q, %v: the foreign entry changed", tc.path, b, err)
			}
			if got := names(t, dst); len(got) != 3 { // a.txt, b, the stage (c.txt in it)
				t.Fatalf("dst holds %q, want the stage kept, c.txt not moved", got)
			}
		})
	}
}

// Where no rename can refuse an existing name atomically, a file is linked
// (link refuses one) and a directory is not renamed: its commit refuses
// (review 2026-11-17 #2, finding D).
func TestLinkRenameRefusesDirectories(t *testing.T) {
	d := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(d, "s", "dir"), 0o755))
	must(t, os.WriteFile(filepath.Join(d, "s", "f"), []byte("f"), 0o600))
	must(t, os.WriteFile(filepath.Join(d, "g"), []byte("g"), 0o600))
	root, err := os.OpenRoot(d)
	must(t, err)
	defer root.Close() //nolint:errcheck
	if err := linkRename(root, "s/dir", "dir"); !errors.Is(err, ErrNoAtomicRename) {
		t.Fatalf("directory: %v, want ErrNoAtomicRename", err)
	}
	if _, err := os.Stat(filepath.Join(d, "s", "dir")); err != nil {
		t.Fatalf("the refused directory moved: %v", err)
	}
	if err := linkRename(root, "s/f", "g"); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("file onto an existing name: %v, want ErrExist", err)
	}
	must(t, linkRename(root, "s/f", "f"))
	if b, err := os.ReadFile(filepath.Join(d, "f")); err != nil || string(b) != "f" {
		t.Fatalf("f: %q, %v", b, err)
	}
}

// The sweep that finishes a stale stage holding its intent compares an
// entry the stopped commit moved with the manifest, as recovery does: one
// replaced by someone else refuses the finish, and the stage stays with
// what it still holds (lane SENDTRACE; spec Commit.tla Rename, mutant
// sweep-trust-moved).
func TestSweepRefusesAReplacedEntry(t *testing.T) {
	dst := t.TempDir()
	crashAfter(t, threeTops(t), dst, 2) // a.txt and b moved, c.txt staged
	must(t, os.WriteFile(filepath.Join(dst, "a.txt"), []byte("content of A"), 0o600))
	old := time.Now().Add(-StageLife - time.Hour)
	must(t, filepath.WalkDir(dst, func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.Contains(p, StagePrefix) {
			err = os.Chtimes(p, old, old)
		}
		return err
	}))
	other := filepath.Join(t.TempDir(), "other.txt")
	must(t, os.WriteFile(other, []byte("another offer"), 0o644))
	s2, err := NewSource([]string{other})
	must(t, err)
	if sr, rr := session(t, s2, dst, 0); !rr.a.OK {
		t.Fatalf("another offer: sender %+v, receiver %+v", sr, rr)
	}
	got := names(t, dst)
	if len(got) != 4 || slices.Contains(got, "c.txt") { // a.txt, b, other.txt, the stage (c.txt in it)
		t.Fatalf("dst holds %q, want the stage kept, c.txt not moved", got)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "a.txt")); err != nil || string(b) != "content of A" {
		t.Fatalf("a.txt: %q, %v: the foreign entry changed", b, err)
	}
}

// A receiver crash that lost the tail of a part (the trace check's
// Resume.tla Crash) resumes from what the part still holds.
func TestPartWhoseTailWasLostResumes(t *testing.T) {
	src := tree(t, 2<<20)
	s, err := NewSource([]string{src})
	must(t, err)
	dst := t.TempDir()
	_, _ = session(t, s, dst, 1<<20)
	must(t, os.Truncate(filepath.Join(dst, names(t, dst)[0], "data", "big.bin"), maxData))
	sr, rr := session(t, s, dst, 0)
	if rr.err != nil || !rr.a.OK || sr.sent != 2<<20-maxData+5 {
		t.Fatalf("sender %+v, receiver %+v, want all but the part's chunk sent", sr, rr)
	}
	if digest(t, src) != digest(t, filepath.Join(dst, "data")) {
		t.Fatal("differs from the source")
	}
}

// A name that appears while the files are received refuses the commit
// before its intent: the stage stays, the name is kept (spec Commit.tla
// Begin).
func TestNameTakenDuringTheTransferRefusesTheCommit(t *testing.T) {
	src := tree(t, 1<<20)
	s, err := NewSource([]string{src})
	must(t, err)
	dst := t.TempDir()
	_, _, resume, first := pausedReceive(t, s, dst)
	must(t, os.WriteFile(filepath.Join(dst, "data"), []byte("someone else's"), 0o600))
	close(resume)
	if err := <-first; err != nil {
		t.Fatalf("the receive: %v, want an answer refusing the commit", err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "data")); err != nil || string(b) != "someone else's" {
		t.Fatalf("data: %q, %v: replaced", b, err)
	}
	if got := names(t, dst); len(got) != 2 { // data and the stage
		t.Fatalf("dst holds %q, want the stage kept", got)
	}
}

// A cut transfer's stage, not written to for StageLife and holding no
// intent, is removed by another offer's receive (spec Commit.tla Expire).
func TestStaleStageOfACutTransferIsRemoved(t *testing.T) {
	src := tree(t, 1<<20)
	s, err := NewSource([]string{src})
	must(t, err)
	dst := t.TempDir()
	_, _ = session(t, s, dst, 1<<19)
	old := time.Now().Add(-StageLife - time.Hour)
	must(t, filepath.WalkDir(dst, func(p string, d fs.DirEntry, err error) error {
		if err == nil && p != dst {
			err = os.Chtimes(p, old, old)
		}
		return err
	}))
	other := filepath.Join(t.TempDir(), "other.txt")
	must(t, os.WriteFile(other, []byte("another offer"), 0o644))
	s2, err := NewSource([]string{other})
	must(t, err)
	if sr, rr := session(t, s2, dst, 0); !rr.a.OK {
		t.Fatalf("another offer: sender %+v, receiver %+v", sr, rr)
	}
	if got := names(t, dst); !slices.Equal(got, []string{"other.txt"}) {
		t.Fatalf("dst holds %q, want only other.txt", got)
	}
}

// A file of one chunk keeps no tree in the stage (its tree is its size):
// a cut after two such files resumes without them, and only the third is
// sent again.
func TestSmallFilesResumeWithoutKeptTrees(t *testing.T) {
	d := filepath.Join(t.TempDir(), "small")
	must(t, os.MkdirAll(d, 0o755))
	for _, n := range []string{"a", "b", "c"} {
		b := make([]byte, 4096)
		_, _ = rand.Read(b)
		must(t, os.WriteFile(filepath.Join(d, n), b, 0o644))
	}
	s, err := NewSource([]string{d})
	must(t, err)
	frame := func(v any) int64 { b, _ := json.Marshal(v); return 5 + int64(len(b)) }
	cut := frame(s.Manifest)
	for i := 1; i <= 2; i++ {
		cut += frame(start{Index: i}) + 5 + int64(verified.OutboardSize(4096)) + 5 + 4096 + frame(end{Index: i})
	}
	dst := t.TempDir()
	if _, rr := session(t, s, dst, cut); rr.err == nil {
		t.Fatalf("cut: receiver %+v, want an error", rr)
	}
	stage := filepath.Join(dst, names(t, dst)[0])
	for _, n := range names(t, stage) {
		if strings.HasPrefix(n, StagePrefix+"tree-") {
			t.Fatalf("stage holds %s: a one-chunk file's tree is not kept", n)
		}
	}
	sr, rr := session(t, s, dst, 0)
	if rr.err != nil || !rr.a.OK || sr.sent != 4096 {
		t.Fatalf("resumed: sender %+v, receiver %+v (want only c's 4096 bytes sent)", sr, rr)
	}
	if digest(t, d) != digest(t, filepath.Join(dst, "small")) {
		t.Fatal("resumed transfer differs from the source")
	}
}

// A part whose every chunk arrived but one that no longer verifies, in its
// middle, has that one chunk sent again, nothing else of it (spec Open,
// Minimal; mutant resend-verified: the 969 MB resent of a 1 GiB file).
func TestResumeSendsOnlyTheBadChunk(t *testing.T) {
	src := tree(t, 8*maxData)
	s, err := NewSource([]string{src})
	must(t, err)
	frame := func(v any) int64 { b, _ := json.Marshal(v); return 5 + int64(len(b)) }
	allData := frame(s.Manifest) + frame(start{Index: 1}) + 5 + int64(verified.OutboardSize(8*maxData)) + 8*(5+maxData) // cut before its end frame
	dst := t.TempDir()
	_, _ = session(t, s, dst, allData)
	part := filepath.Join(dst, names(t, dst)[0], "data", "big.bin")
	b, err := os.ReadFile(part)
	must(t, err)
	if len(b) != 8*maxData {
		t.Fatalf("part of %d bytes, want the whole file", len(b))
	}
	b[3*maxData+1] ^= 0xff
	must(t, os.WriteFile(part, b, 0o600))
	sr, rr := session(t, s, dst, 0)
	if rr.err != nil || !rr.a.OK || sr.sent != maxData+5 {
		t.Fatalf("resumed: sender %+v, receiver %+v, want one chunk and small.txt sent", sr, rr)
	}
	if digest(t, src) != digest(t, filepath.Join(dst, "data")) {
		t.Fatal("differs from the source")
	}
}

// answerCut fails every Write after the first (the want), as a session cut
// before the receiver's answer went.
type answerCut struct {
	net.Conn
	writes int
}

func (c *answerCut) Write(p []byte) (int, error) {
	if c.writes++; c.writes > 1 {
		_ = c.Conn.Close()
		return 0, io.ErrClosedPipe
	}
	return c.Conn.Write(p)
}

// TestCommitStandsWhenTheAnswerDoesNotGo (lane SENDFIX, docs/reliability.md
// "Open defects" 2; spec End.tla ExitRight, mutant exit-on-answer): a
// receive that committed returns its answer and no error when the session
// was cut before the answer went. It returned the write's error, so the
// receive retried the dead address and exited 3 with every file in place,
// and a receive of a new address then refused (the names were taken).
func TestCommitStandsWhenTheAnswerDoesNotGo(t *testing.T) {
	src := tree(t, 1<<20)
	s, err := NewSource([]string{src})
	must(t, err)
	dst := t.TempDir()
	x, y := net.Pipe()
	go func() { _, _, _ = s.Serve(x); _ = x.Close() }()
	a, err := Receive(&answerCut{Conn: y}, dst, nil)
	if err != nil || !a.OK {
		t.Fatalf("receive: %+v, %v; want the commit's answer and no error", a, err)
	}
	if digest(t, src) != digest(t, filepath.Join(dst, "data")) {
		t.Fatal("what landed differs from the source")
	}
}

// Version negotiation: a sender names its highest version in the
// manifest, the receiver answers the lower of the two in its want (none:
// MinVersion) and refuses one below MinVersion; a sender refuses a want
// naming a version it does not speak.
func TestVersionNegotiation(t *testing.T) {
	src := tree(t, 1<<10)
	t.Run("newer sender", func(t *testing.T) {
		s, err := NewSource([]string{src})
		must(t, err)
		s.Manifest.Version = Version + 1 // a later 1.x sender
		sr, rr := session(t, s, t.TempDir(), 0)
		if sr.err != nil || rr.err != nil || !sr.a.OK || !rr.a.OK {
			t.Fatalf("sender %+v, receiver %+v: want the receiver's version spoken", sr, rr)
		}
	})
	t.Run("older than MinVersion", func(t *testing.T) {
		s, err := NewSource([]string{src})
		must(t, err)
		s.Manifest.Version = MinVersion - 1
		sr, rr := session(t, s, t.TempDir(), 0)
		if !errors.Is(rr.err, ErrRefused) || sr.a.OK || !strings.Contains(sr.a.Message, "version") {
			t.Fatalf("sender %+v, receiver %+v: want refused", sr, rr)
		}
	})
	for name, w := range map[string]struct {
		want string
		ok   bool
	}{
		"want names the sender's": {`{"version":5}`, true},
		"want names none":         {`{}`, true},
		"want names a newer":      {`{"version":6}`, false},
		"want names an older":     {`{"version":4}`, false},
	} {
		t.Run(name, func(t *testing.T) {
			s, err := NewSource([]string{src})
			must(t, err)
			a, b := net.Pipe()
			defer a.Close() //nolint:errcheck
			go func() {
				c := newConn(b)
				_, _, _ = c.read(nil) // the manifest
				_ = c.write(frameWant, []byte(w.want))
				_ = c.flush()
				for {
					if t, _, err := c.read(nil); err != nil || t == frameDone {
						break
					}
				}
				_ = c.writeJSON(frameAnswer, Answer{OK: true})
				_ = c.flush()
			}()
			_ = a.SetDeadline(time.Now().Add(5 * time.Second))
			_, sent, err := s.Serve(a)
			_ = b.Close()
			if (err == nil) != w.ok || (w.ok && sent == 0) || (!w.ok && !errors.Is(err, ErrProtocol)) {
				t.Fatalf("Serve: sent %d, err %v; want ok %v", sent, err, w.ok)
			}
		})
	}
}

// twoFiles makes a.bin and b.bin (n random bytes each) and their Source.
func twoFiles(t *testing.T, n int) (base string, s *Source) {
	t.Helper()
	base = t.TempDir()
	var srcs []string
	for _, name := range []string{"a.bin", "b.bin"} {
		b := make([]byte, n)
		_, _ = rand.Read(b)
		p := filepath.Join(base, name)
		must(t, os.WriteFile(p, b, 0o644))
		srcs = append(srcs, p)
	}
	s, err := NewSource(srcs)
	must(t, err)
	return base, s
}

// An earlier file written on the sender's disk after its own check, while
// a later file is sent, is refused before the done frame (ErrChanged):
// nothing is committed (spec Offer.tla Done, mutant no-sender-recheck;
// lane SENDTAIL's finding).
func TestSenderRefusesAnEarlierFileChangedBeforeTheCommit(t *testing.T) {
	base, s := twoFiles(t, 1<<20)
	a := filepath.Join(base, "a.bin")
	readFile = func(f *os.File) io.Reader {
		if filepath.Base(f.Name()) == "b.bin" {
			fi, err := os.Stat(a)
			must(t, err)
			if fi.Size() == 1<<20 {
				w, err := os.OpenFile(a, os.O_WRONLY|os.O_APPEND, 0)
				must(t, err)
				_, err = w.Write([]byte("more"))
				must(t, errors.Join(err, w.Close()))
			}
		}
		return f
	}
	defer func() { readFile = func(f *os.File) io.Reader { return f } }()
	dst := t.TempDir()
	sr, rr := session(t, s, dst, 0)
	if !errors.Is(sr.err, ErrChanged) || rr.a.OK {
		t.Fatalf("sender %+v, receiver %+v: want ErrChanged and nothing committed", sr, rr)
	}
	if _, err := os.Lstat(filepath.Join(dst, "a.bin")); err == nil {
		t.Fatal("an earlier file changed before the commit landed")
	}
}

// A staged file written by another program after it was verified, while a
// later file is received, refuses the commit (ErrUnverified in the
// answer), the stage kept (written here once a.bin verified, while b.bin
// is verified); the next session sends what no longer verifies
// and commits the offered files (spec Offer.tla Commit, mutant
// no-receiver-recheck).
func TestStagedFileWrittenAfterItsCheckRefusesTheCommit(t *testing.T) {
	base, s := twoFiles(t, 1<<20)
	dst := t.TempDir()
	afterVerify = func(name string) {
		if filepath.Base(name) == "a.bin" {
			a := name
			w, err := os.OpenFile(a, os.O_WRONLY, 0)
			must(t, err)
			_, err = w.WriteAt([]byte("another program"), 3*maxData+5)
			must(t, errors.Join(err, w.Close()))
			later := time.Now().Add(time.Hour)
			must(t, os.Chtimes(a, later, later))
		}
	}
	_, rr := session(t, s, dst, 0)
	afterVerify = nil
	if rr.a.OK || !strings.Contains(rr.a.Message, ErrUnverified.Error()) {
		t.Fatalf("receiver %+v: want the commit refused, ErrUnverified", rr)
	}
	if got := names(t, dst); len(got) != 1 { // the stage
		t.Fatalf("dst holds %q, want only the stage", got)
	}
	sr, rr := session(t, s, dst, 0)
	if sr.err != nil || rr.err != nil || !rr.a.OK {
		t.Fatalf("next session: sender %+v, receiver %+v", sr, rr)
	}
	if digest(t, base) != digest(t, dst) {
		t.Fatal("differs from the source")
	}
}
