// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

// Package verified is BLAKE3 verified streaming (Bao, outboard) for one
// file: the file is its BLAKE3 root; its tree's outboard (the parent
// nodes, BaoEncodedSize bytes) lets a receiver verify each GroupSize
// chunk on arrival against the root, and a part it kept on resume. The
// tree and its checks are the bao package of github.com/nfltr/blake3
// (lukechampine.com/blake3 v1.4.1 plus arm64 NEON assembly, offered
// upstream), with chunk groups of 2^Group BLAKE3 chunks; the root is the
// file's BLAKE3 hash whatever the group. (Chosen over
// github.com/zeebo/blake3, which has no Bao, and over writing an outboard
// tree on a BLAKE3: it is the maintained Go BLAKE3 with Bao encode, decode
// and slices; MIT.)
package verified

import (
	"encoding/binary"
	"errors"
	"io"
	"math/bits"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/nfltr/blake3"
	"github.com/nfltr/blake3/bao"
	"github.com/nfltr/blake3/guts"
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

// New reads size bytes of r into their tree: the same root and outboard
// as bao.Encode, but each GroupSize chunk's chaining value is hashed on
// its own, GOMAXPROCS at a time, as Verify does (bao.Encode hashes on one
// core); the parents above the chunks, about 1/4096 of the work, are then
// merged on one.
func New(r io.ReaderAt, size int64) (Tree, error) {
	t := Tree{Size: size, Outboard: make([]byte, OutboardSize(size))}
	var err error
	if size <= GroupSize { // one chunk: it is the root
		t.Root, err = bao.Encode(at(t.Outboard), io.NewSectionReader(r, 0, size), size, Group, true)
		return t, err
	}
	cvs, err := groupCVs(r, size)
	if err != nil {
		return t, err
	}
	binary.LittleEndian.PutUint64(t.Outboard, uint64(size)) // #nosec G115 -- size > 0
	root := parents(t.Outboard, cvs, 8, guts.FlagRoot)
	copy(t.Root[:], cvBytes(root))
	return t, nil
}

// groupCVs is the chaining value of each GroupSize chunk of r's size
// bytes, GOMAXPROCS chunks at a time.
func groupCVs(r io.ReaderAt, size int64) ([][8]uint32, error) {
	chunks := (size + GroupSize - 1) / GroupSize
	cvs := make([][8]uint32, chunks)
	var next atomic.Int64
	var mu sync.Mutex
	var err error
	var wg sync.WaitGroup
	for range min(int64(runtime.GOMAXPROCS(0)), chunks) {
		wg.Go(func() {
			buf := make([]byte, GroupSize)
			for i := next.Add(1) - 1; i < chunks; i = next.Add(1) - 1 {
				off := i * GroupSize
				p := buf[:min(GroupSize, size-off)]
				if _, rerr := r.ReadAt(p, off); rerr != nil {
					if errors.Is(rerr, io.EOF) {
						rerr = io.ErrUnexpectedEOF
					}
					mu.Lock()
					err = rerr
					mu.Unlock()
					next.Store(chunks)
					return
				}
				cvs[i] = groupCV(p, uint64(off)/guts.ChunkSize) // #nosec G115 -- off >= 0
			}
		})
	}
	wg.Wait()
	return cvs, err
}

// groupCV is the chaining value of the BLAKE3 subtree of p, whose first
// 1 KiB chunk is chunk counter of the file: guts.MaxSIMD chunks at a time,
// merged as merge does.
func groupCV(p []byte, counter uint64) [8]uint32 {
	const n = guts.MaxSIMD * guts.ChunkSize
	cvs := make([][8]uint32, 0, GroupSize/n)
	var buf [n]byte
	for off := 0; off < len(p); off += n {
		k := copy(buf[:], p[off:])
		cvs = append(cvs, guts.ChainingValue(guts.CompressBuffer(&buf, k, &guts.IV, counter+uint64(off/guts.ChunkSize), 0)))
	}
	return merge(cvs)
}

// merge is the chaining value of the BLAKE3 subtree over cvs: the left
// subtree takes the largest power of two of them short of all.
func merge(cvs [][8]uint32) [8]uint32 {
	if len(cvs) == 1 {
		return cvs[0]
	}
	mid := 1 << (bits.Len(uint(len(cvs)-1)) - 1)
	return guts.ChainingValue(guts.ParentNode(merge(cvs[:mid]), merge(cvs[mid:]), &guts.IV, 0))
}

// parents writes the outboard's parent nodes over the chunks' cvs into
// out at off, in pre-order as bao.Encode does (a parent's two children,
// then its left subtree's parents, then its right's), and returns their
// root's chaining value, with flags.
func parents(out []byte, cvs [][8]uint32, off int, flags uint32) [8]uint32 {
	if len(cvs) == 1 {
		return cvs[0]
	}
	mid := 1 << (bits.Len(uint(len(cvs)-1)) - 1)
	l := parents(out, cvs[:mid], off+64, 0)
	r := parents(out, cvs[mid:], off+64+(2*mid-2)*32, 0)
	copy(out[off:], cvBytes(l))
	copy(out[off+32:], cvBytes(r))
	return guts.ChainingValue(guts.ParentNode(l, r, &guts.IV, flags))
}

func cvBytes(cv [8]uint32) []byte {
	b := make([]byte, 32)
	for i, w := range cv {
		binary.LittleEndian.PutUint32(b[4*i:], w)
	}
	return b
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

// Good checks every chunk of r against the root, GOMAXPROCS at a time,
// and reports which verify (a short or missing chunk does not).
func (t Tree) Good(r io.ReaderAt) ([]bool, error) {
	chunks := (t.Size + GroupSize - 1) / GroupSize
	good := make([]bool, chunks)
	if !t.Valid() {
		return good, nil
	}
	var next atomic.Int64
	var mu sync.Mutex
	var err error
	var wg sync.WaitGroup
	for range min(int64(runtime.GOMAXPROCS(0)), chunks) {
		wg.Go(func() {
			buf := make([]byte, GroupSize)
			for i := next.Add(1) - 1; i < chunks; i = next.Add(1) - 1 {
				off := i * GroupSize
				p := buf[:min(GroupSize, t.Size-off)]
				k, rerr := r.ReadAt(p, off)
				good[i] = k == len(p) && t.Chunk(p, off)
				if rerr != nil && !errors.Is(rerr, io.EOF) {
					mu.Lock()
					err = rerr
					mu.Unlock()
				}
			}
		})
	}
	wg.Wait()
	return good, err
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
