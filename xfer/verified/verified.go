// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

// Package verified is BLAKE3 verified streaming (Bao, outboard) for one
// file: the file is its BLAKE3 root; its tree's outboard (the parent
// nodes, BaoEncodedSize bytes) lets a receiver verify each GroupSize
// chunk on arrival against the root, and a part it kept on resume. The
// tree and its checks are lukechampine.com/blake3's bao package, with
// chunk groups of 2^Group BLAKE3 chunks; the root is the file's BLAKE3
// hash whatever the group. (Chosen over github.com/zeebo/blake3, which has
// no Bao, and over writing an outboard tree on a BLAKE3: it is the
// maintained Go BLAKE3 with Bao encode, decode and slices; MIT.)
package verified

import (
	"encoding/binary"
	"errors"
	"io"
	"runtime"
	"sync"
	"sync/atomic"

	"lukechampine.com/blake3"
	"lukechampine.com/blake3/bao"
)

// Group is the chunk group's size as a power of two of BLAKE3's 1 KiB
// chunks: a verified chunk is GroupSize bytes (a file's last may be
// shorter).
const (
	Group     = 8
	GroupSize = 1024 << Group
)

// Tree is a file's root, size and outboard.
type Tree struct {
	Root     [32]byte
	Size     int64
	Outboard []byte
}

// OutboardSize is the outboard's size for a file of size bytes.
func OutboardSize(size int64) int { return bao.EncodedSize(int(size), Group, true) }

// New reads size bytes of r into their tree.
func New(r io.Reader, size int64) (Tree, error) {
	t := Tree{Size: size, Outboard: make([]byte, OutboardSize(size))}
	var err error
	t.Root, err = bao.Encode(at(t.Outboard), r, size, Group, true)
	return t, err
}

type at []byte

func (b at) WriteAt(p []byte, off int64) (int, error) {
	if copy(b[off:], p) != len(p) {
		return 0, errors.New("verified: outboard too short")
	}
	return len(p), nil
}

// Valid reports whether t's outboard is shaped for t's size (its length
// and the size it starts with); a chunk is verified only by a valid tree.
func (t Tree) Valid() bool {
	return t.Size >= 0 && len(t.Outboard) == OutboardSize(t.Size) && binary.LittleEndian.Uint64(t.Outboard) == uint64(t.Size)
}

// Chunk reports whether p is the file's chunk at off: off a multiple of
// GroupSize, p GroupSize bytes or the file's short last chunk, verified
// against the root through the outboard.
func (t Tree) Chunk(p []byte, off int64) bool {
	n := min(GroupSize, t.Size-off)
	return t.Valid() && off >= 0 && off%GroupSize == 0 && n > 0 && int64(len(p)) == n &&
		bao.VerifyChunk(p, t.Outboard, Group, uint64(off), t.Root) // #nosec G115 -- off >= 0
}

// Verify checks r's chunks against the root through the outboard, each
// GroupSize chunk on its own, GOMAXPROCS at a time (one core hashes a
// chunk at a fraction of the disk's speed; docs/performance.md). It
// returns the length of r's longest prefix of whole chunks (or the whole
// file) whose every chunk verifies, and whether that is the whole file;
// it reads at most t.Size bytes.
func (t Tree) Verify(r io.ReaderAt) (n int64, whole bool, err error) {
	if !t.Valid() {
		return 0, false, nil
	}
	chunks := (t.Size + GroupSize - 1) / GroupSize
	var next, bad atomic.Int64 // the next chunk to check; the first that did not verify
	bad.Store(chunks)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range min(int64(runtime.GOMAXPROCS(0)), chunks) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, GroupSize)
			for i := next.Add(1) - 1; i < min(chunks, bad.Load()); i = next.Add(1) - 1 {
				off := i * GroupSize
				p := buf[:min(GroupSize, t.Size-off)]
				k, rerr := r.ReadAt(p, off)
				if k == len(p) && t.Chunk(p, off) {
					continue
				}
				if rerr != nil && !errors.Is(rerr, io.EOF) {
					mu.Lock()
					err = rerr
					mu.Unlock()
				}
				for b := bad.Load(); i < b && !bad.CompareAndSwap(b, i); b = bad.Load() {
				}
			}
		}()
	}
	wg.Wait()
	b := bad.Load()
	return min(b*GroupSize, t.Size), b == chunks && (t.Size > 0 || t.Root == empty), err
}

var empty = blake3.Sum256(nil)

// Sum is the BLAKE3 hash of r: a whole file's root. It writes 4 MiB at a
// time: the hasher spreads a large write over the cores (io.Copy's 32 KiB
// writes hash at a third of the speed).
func Sum(r io.Reader) ([32]byte, error) {
	h := blake3.New(32, nil)
	buf := make([]byte, 4<<20)
	var s [32]byte
	for {
		n, err := io.ReadFull(r, buf)
		h.Write(buf[:n])
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			copy(s[:], h.Sum(nil))
			return s, nil
		}
		if err != nil {
			return s, err
		}
	}
}
