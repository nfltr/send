// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package verified

import (
	"bytes"
	"crypto/rand"
	"io"
	"runtime"
	"sync"
	"testing"

	"github.com/nfltr/blake3"
	"github.com/nfltr/blake3/bao"
)

func file(t *testing.T, n int) ([]byte, Tree) {
	t.Helper()
	b := make([]byte, n)
	_, _ = rand.Read(b)
	tr, err := New(bytes.NewReader(b), int64(n))
	if err != nil {
		t.Fatal(err)
	}
	return b, tr
}

// The root is the file's BLAKE3 hash, whatever the chunk group.
func TestRootIsBLAKE3(t *testing.T) {
	for _, n := range []int{0, 1, GroupSize, 3*GroupSize + 7} {
		b, tr := file(t, n)
		if tr.Root != blake3.Sum256(b) || !tr.Valid() {
			t.Fatalf("%d bytes: root %x, want %x (valid %v)", n, tr.Root, blake3.Sum256(b), tr.Valid())
		}
		if s, _ := Sum(bytes.NewReader(b)); s != tr.Root {
			t.Fatalf("%d bytes: Sum differs from the root", n)
		}
	}
}

// Every chunk at its offset verifies; a corrupted one, a chunk at another
// offset, a misaligned or cut one, and any chunk under another root do not.
func TestChunk(t *testing.T) {
	b, tr := file(t, 3*GroupSize+7)
	for off := 0; off < len(b); off += GroupSize {
		p := b[off:min(off+GroupSize, len(b))]
		if !tr.Chunk(p, int64(off)) {
			t.Fatalf("chunk at %d refused", off)
		}
		bad := bytes.Clone(p)
		bad[len(bad)/2] ^= 1
		if tr.Chunk(bad, int64(off)) {
			t.Fatalf("corrupted chunk at %d verified", off)
		}
	}
	other := tr
	other.Root[0] ^= 1
	if other.Chunk(b[:GroupSize], 0) {
		t.Fatal("a chunk verified under the wrong root")
	}
	short := tr
	short.Outboard = short.Outboard[:len(short.Outboard)-1]
	for name, ok := range map[string]bool{
		"another offset": tr.Chunk(b[:GroupSize], GroupSize),
		"misaligned":     tr.Chunk(b[1:GroupSize+1], 1),
		"cut":            tr.Chunk(b[:GroupSize-1], 0),
		"last too long":  tr.Chunk(b[3*GroupSize:], 3*GroupSize) && tr.Chunk(append(bytes.Clone(b[3*GroupSize:]), 0), 3*GroupSize),
		"past the end":   tr.Chunk(b[:1], 4*GroupSize),
		"torn outboard":  short.Chunk(b[:GroupSize], 0),
	} {
		if ok {
			t.Errorf("%s: verified", name)
		}
	}
}

// Verify keeps whole verified chunks: up to a corrupted chunk, a cut
// (truncated) one, or the end; nothing under the wrong root; an empty file
// only under the empty file's root.
func TestVerify(t *testing.T) {
	b, tr := file(t, 3*GroupSize+7)
	other := tr
	other.Root[0] ^= 1
	bad := bytes.Clone(b)
	bad[2*GroupSize+5] ^= 1
	bad2 := bytes.Clone(bad)
	bad2[GroupSize] ^= 1
	_, none := file(t, 0)
	noneWrong := none
	noneWrong.Root[0] ^= 1
	for name, c := range map[string]struct {
		tr    Tree
		data  []byte
		want  int64
		whole bool
	}{
		"whole":             {tr, b, int64(len(b)), true},
		"longer":            {tr, append(bytes.Clone(b), 1, 2), int64(len(b)), true},
		"truncated":         {tr, b[:2*GroupSize+100], 2 * GroupSize, false},
		"chunk aligned":     {tr, b[:GroupSize], GroupSize, false},
		"corrupted":         {tr, bad, 2 * GroupSize, false},
		"two corrupted":     {tr, bad2, GroupSize, false},
		"empty part":        {tr, nil, 0, false},
		"wrong root":        {other, b, 0, false},
		"empty file":        {none, nil, 0, true},
		"empty, wrong root": {noneWrong, nil, 0, false},
	} {
		if got, whole, err := c.tr.Verify(bytes.NewReader(c.data)); err != nil || got != c.want || whole != c.whole {
			t.Errorf("%s: %d, %v, %v; want %d, %v", name, got, whole, err, c.want, c.whole)
		}
	}
}

// reads records the length of each ReadAt.
type reads struct {
	io.ReaderAt
	mu sync.Mutex
	n  []int
}

func (r *reads) ReadAt(p []byte, off int64) (int, error) {
	r.mu.Lock()
	r.n = append(r.n, len(p))
	r.mu.Unlock()
	return r.ReaderAt.ReadAt(p, off)
}

// New, Verify and Good read maxSpan chunks at once (the cold-cache read
// rate, docs/performance.md) and keep their results across a span: a
// corrupted chunk inside one, a cut one, the last short one.
func TestSpans(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	b, tr := file(t, 40*GroupSize+7)
	if tr.Root != blake3.Sum256(b) {
		t.Fatal("root differs from BLAKE3")
	}
	r := &reads{ReaderAt: bytes.NewReader(b)}
	if _, whole, err := tr.Verify(r); !whole || err != nil {
		t.Fatalf("whole file: %v, %v", whole, err)
	}
	if r.n[0] != maxSpan*GroupSize {
		t.Fatalf("read %d bytes at once, want %d", r.n[0], maxSpan*GroupSize)
	}
	bad := bytes.Clone(b)
	bad[20*GroupSize+3] ^= 1
	if n, whole, err := tr.Verify(bytes.NewReader(bad)); n != 20*GroupSize || whole || err != nil {
		t.Fatalf("corrupted chunk 20: %d, %v, %v", n, whole, err)
	}
	if n, whole, err := tr.Verify(bytes.NewReader(b[:33*GroupSize+9])); n != 33*GroupSize || whole || err != nil {
		t.Fatalf("cut in chunk 33: %d, %v, %v", n, whole, err)
	}
	good, err := tr.Good(bytes.NewReader(bad[:33*GroupSize+9]))
	if err != nil || len(good) != 41 {
		t.Fatal(len(good), err)
	}
	for i, g := range good {
		if g != (i != 20 && i < 33) {
			t.Errorf("chunk %d good %v", i, g)
		}
	}
	if _, err := New(bytes.NewReader(b[:33*GroupSize+9]), int64(len(b))); err != io.ErrUnexpectedEOF {
		t.Fatalf("New of a short file: %v", err)
	}
}

// New builds the same root and outboard as bao.Encode on one core, for
// sizes around the chunk (GroupSize), the hasher's 16 KiB buffer and the
// subtree (power of two chunks) boundaries.
func TestNewMatchesSequential(t *testing.T) {
	var sizes []int
	for _, base := range []int{0, 1 << 10, 16 << 10, GroupSize, 2 * GroupSize, 3 * GroupSize, 4 * GroupSize, 5 * GroupSize, 8 * GroupSize, 13*GroupSize + 16<<10} {
		for _, d := range []int{-1025, -1024, -1, 0, 1, 1024, 1025} {
			if base+d >= 0 {
				sizes = append(sizes, base+d)
			}
		}
	}
	b := make([]byte, 13*GroupSize+16<<10+1025)
	_, _ = rand.Read(b)
	for _, n := range sizes {
		tr, err := New(bytes.NewReader(b[:n]), int64(n))
		if err != nil {
			t.Fatal(err)
		}
		want := make([]byte, OutboardSize(int64(n)))
		root, err := bao.Encode(at(want), bytes.NewReader(b[:n]), int64(n), Group, true)
		if err != nil {
			t.Fatal(err)
		}
		if tr.Root != root || !bytes.Equal(tr.Outboard, want) {
			t.Fatalf("%d bytes: New differs from bao.Encode", n)
		}
	}
	if _, err := New(bytes.NewReader(b[:GroupSize+1]), GroupSize+2); err == nil {
		t.Fatal("a short read built a tree")
	}
}
