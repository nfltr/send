// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package xfer

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
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
	afterBig := frame(s.Manifest) + frame(start{Index: 1}) + 16*(5+maxData) + frame(end{Index: 1, SHA256: strings.Repeat("0", 64)})
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

// A part that is not a prefix of the sender's file (a torn write, another
// file) is sent again from 0 (spec Open: the prefix check).
func TestResumeOfAForeignPartStartsOver(t *testing.T) {
	src := tree(t, 2<<20)
	s, err := NewSource([]string{src})
	must(t, err)
	dst := t.TempDir()
	_, _ = session(t, s, dst, 1<<20)
	stage := filepath.Join(dst, names(t, dst)[0])
	part := filepath.Join(stage, "data", "big.bin")
	b, err := os.ReadFile(part)
	must(t, err)
	b[10] ^= 0xff
	must(t, os.WriteFile(part, b, 0o600))
	sr, rr := session(t, s, dst, 0)
	if rr.err != nil || !rr.a.OK || sr.sent != 2<<20+5 {
		t.Fatalf("sender %+v, receiver %+v, want the whole file sent again", sr, rr)
	}
	if digest(t, src) != digest(t, filepath.Join(dst, "data")) {
		t.Fatal("differs from the source")
	}
}

// The receiver's digest over what is on its disk must equal the sender's:
// a file whose bytes differ refuses the transfer and nothing is committed
// (spec Finish, mutant no-final-check); a refused transfer is not resumed,
// so its stage is removed (spec Crash to an empty part).
func TestDigestMismatchCommitsNothing(t *testing.T) {
	dst := t.TempDir()
	a, b := net.Pipe()
	go func() {
		c := newConn(a)
		_ = c.writeJSON(frameManifest, Manifest{Version: Version, Entries: []Entry{{Path: "f", Size: 3, Mode: 0o644}}})
		_ = c.flush()
		var w want
		_ = c.readJSON(frameWant, &w)
		_ = c.writeJSON(frameStart, start{Index: 0})
		_ = c.write(frameData, []byte("abc"))
		_ = c.writeJSON(frameEnd, end{Index: 0, SHA256: strings.Repeat("0", 64)})
		_ = c.write(frameDone, nil)
		_ = c.flush()
		_, _ = io.Copy(io.Discard, a)
	}()
	ans, err := Receive(b, dst, nil)
	_ = b.Close()
	if err != nil || ans.OK || !strings.Contains(ans.Message, "SHA-256") {
		t.Fatalf("answer %+v, %v: want a refused digest", ans, err)
	}
	if got := names(t, dst); len(got) != 0 {
		t.Fatalf("dst holds %q, want nothing (the refused transfer's stage removed)", got)
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
	} {
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
