// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package xfer

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// striped serves s over every session the receive opens (pipes), each
// sender side wrapped by wrap(n, conn) for the nth session (1: the
// first); it returns the receive's result and what each session sent.
type striped struct {
	t     *testing.T
	s     *Source
	wrap  func(n int, c net.Conn) io.ReadWriter
	mu    sync.Mutex
	sent  []int64
	parts int // sessions answered Part
	wg    sync.WaitGroup
}

func (x *striped) open(context.Context) (Session, error) {
	a, b := net.Pipe()
	x.mu.Lock()
	n := len(x.sent) + 1
	x.sent = append(x.sent, 0)
	x.mu.Unlock()
	var w io.ReadWriter = a
	if x.wrap != nil {
		w = x.wrap(n, a)
	}
	x.wg.Go(func() {
		ans, sent, _ := x.s.Serve(w)
		_ = a.Close()
		x.mu.Lock()
		x.sent[n-1] = sent
		if ans.Part {
			x.parts++
		}
		x.mu.Unlock()
	})
	return one{b}, nil
}

func (x *striped) receive(ctx context.Context, dir string, k int) (Answer, error) {
	first, _ := x.open(ctx)
	a, err := ReceiveFrom(ctx, first, Receiving{Dir: dir, K: k, Open: x.open})
	x.wg.Wait()
	return a, err
}

func (x *striped) total() (n int64) {
	for _, s := range x.sent {
		n += s
	}
	return n
}

func shortRuns(t *testing.T, n int64) {
	old := stripeRun
	stripeRun = n
	t.Cleanup(func() { stripeRun = old })
}

// A file striped over K sessions: each sends its share only (the sum is
// the file once), every session but the last is answered Part, the last
// commits (spec Stripe.tla Open, Read, Finish).
func TestStripedReceiveSendsEachChunkOnce(t *testing.T) {
	shortRuns(t, 2)
	defer func(n int64) { tailRuns = n }(tailRuns)
	tailRuns = 1 << 40 // no slow-tail close: every byte sent once
	src := tree(t, 20*maxData+7)
	s, err := NewSource([]string{src})
	must(t, err)
	dst := t.TempDir()
	x := &striped{t: t, s: s}
	a, err := x.receive(t.Context(), dst, 3)
	if err != nil || !a.OK {
		t.Fatalf("receive %+v, %v", a, err)
	}
	if len(x.sent) != 3 || x.parts != 2 || x.total() != 20*maxData+7+5 {
		t.Fatalf("sessions %v (parts %d): want 3, 2 answered Part, the bytes sent once", x.sent, x.parts)
	}
	for _, n := range x.sent {
		if n == 0 {
			t.Fatalf("sessions sent %v: want each a share", x.sent)
		}
	}
	if digest(t, src) != digest(t, dst+"/data") {
		t.Fatal("differs from the source")
	}
}

// A connection cut mid-chunk: its session's unwritten rest goes back into
// the pool and another session fetches it; the transfer commits, every
// chunk written once (spec Stripe.tla Die, Notice; mutant lost-on-death).
func TestStripedReceiveSurvivesAConnectionCutMidChunk(t *testing.T) {
	shortRuns(t, 2)
	src := tree(t, 20*maxData)
	s, err := NewSource([]string{src})
	must(t, err)
	dst := t.TempDir()
	x := &striped{t: t, s: s, wrap: func(n int, c net.Conn) io.ReadWriter {
		if n == 2 {
			return &cutter{Conn: c, left: 3*maxData + 1000} // inside its third chunk
		}
		return c
	}}
	a, err := x.receive(t.Context(), dst, 3)
	if err != nil || !a.OK {
		t.Fatalf("receive %+v, %v", a, err)
	}
	if len(x.sent) < 4 {
		t.Fatalf("sessions %v: want the cut one replaced", x.sent)
	}
	if digest(t, src) != digest(t, dst+"/data") {
		t.Fatal("differs from the source")
	}
}

// flip corrupts one byte of the data it passes after skip bytes: a chunk
// that does not verify on one session.
type flip struct {
	net.Conn
	mu   sync.Mutex
	skip int64
}

func (f *flip) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.skip >= 0 && f.skip < int64(len(p)) {
		q := append([]byte(nil), p...)
		q[f.skip] ^= 0xff
		f.skip = -1
		return f.Conn.Write(q)
	}
	if f.skip >= 0 {
		f.skip -= int64(len(p))
	}
	return f.Conn.Write(p)
}

// A chunk that does not verify ends its session only (Resume.tla Bad):
// the chunk and the rest of the set go back into the pool, another
// session fetches them, and the transfer commits the offered bytes (spec
// Stripe.tla Corrupt, Read; mutant accept-unverified).
func TestStripedReceiveRefusesABadChunkOnOneSession(t *testing.T) {
	shortRuns(t, 2)
	src := tree(t, 20*maxData)
	s, err := NewSource([]string{src})
	must(t, err)
	dst := t.TempDir()
	x := &striped{t: t, s: s, wrap: func(n int, c net.Conn) io.ReadWriter {
		if n == 2 {
			return &flip{Conn: c, skip: 2*maxData + 4096} // inside its second chunk
		}
		return c
	}}
	a, err := x.receive(t.Context(), dst, 3)
	if err != nil || !a.OK {
		t.Fatalf("receive %+v, %v", a, err)
	}
	if len(x.sent) < 4 {
		t.Fatalf("sessions %v: want the refused one replaced", x.sent)
	}
	if digest(t, src) != digest(t, dst+"/data") {
		t.Fatal("differs from the source")
	}
}

// slow delays every write: a session on a slow path.
type slow struct {
	net.Conn
	d time.Duration
}

func (s slow) Write(p []byte) (int, error) {
	time.Sleep(s.d)
	return s.Conn.Write(p)
}

// The slow tail: once the pool is empty, a session with two runs or more
// left is closed (tailRuns) and its rest split among new sessions (spec Stripe.tla
// Close); nothing is written twice and the transfer commits.
func TestStripedReceiveClosesTheSlowTail(t *testing.T) {
	shortRuns(t, 2)
	src := tree(t, 24*maxData)
	s, err := NewSource([]string{src})
	must(t, err)
	dst := t.TempDir()
	var slowSent atomic.Int64
	x := &striped{t: t, s: s, wrap: func(n int, c net.Conn) io.ReadWriter {
		if n == 1 {
			return slow{c, 20 * time.Millisecond}
		}
		return c
	}}
	a, err := x.receive(t.Context(), dst, 2)
	_ = slowSent.Load()
	if err != nil || !a.OK {
		t.Fatalf("receive %+v, %v", a, err)
	}
	if len(x.sent) < 3 {
		t.Fatalf("sessions %v: want the slow one closed and replaced", x.sent)
	}
	if digest(t, src) != digest(t, dst+"/data") {
		t.Fatal("differs from the source")
	}
}

// A receiver stopped mid-transfer (its context: a crash's in-process
// stand-in) cuts every session and returns once none writes; the next
// receive verifies the stage and fetches only what is not on it
// (spec Stripe.tla Crash, Restart; mutant resume-trust-log).
func TestStripedReceiveResumesAfterAStop(t *testing.T) {
	shortRuns(t, 2)
	src := tree(t, 30*maxData)
	s, err := NewSource([]string{src})
	must(t, err)
	dst := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	var written atomic.Int64
	x := &striped{t: t, s: s}
	first, _ := x.open(ctx)
	_, err = ReceiveFrom(ctx, first, Receiving{Dir: dst, K: 3, Open: x.open, Progress: func(n int64) {
		if written.Store(n); n >= 10*maxData {
			cancel()
		}
	}})
	x.wg.Wait()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped receive: %v, want context.Canceled", err)
	}
	y := &striped{t: t, s: s}
	a, err := y.receive(t.Context(), dst, 3)
	if err != nil || !a.OK {
		t.Fatalf("resumed receive %+v, %v", a, err)
	}
	if got := y.total(); got > 30*maxData+5-written.Load()+5 || got >= 30*maxData {
		t.Fatalf("resumed sent %d of %d, %d written before: want only what was not", got, 30*maxData, written.Load())
	}
	if digest(t, src) != digest(t, dst+"/data") {
		t.Fatal("differs from the source")
	}
}

// The pool's arithmetic: shares interleave runs; rest is a set past its
// written prefix; putBack keeps the pool in order.
func TestPoolShares(t *testing.T) {
	pool := []piece{{0, [2]int64{0, 2}}, {0, [2]int64{2, 4}}, {0, [2]int64{4, 5}}, {1, [2]int64{0, 2}}}
	set, left := take(pool, 2)
	if len(set) != 2 || set[0] != pool[0] || set[1] != pool[2] || len(left) != 2 {
		t.Fatalf("take 2: %v, %v", set, left)
	}
	if r := rest(set, 3); len(r) != 0 {
		t.Fatalf("rest of all written: %v", r)
	}
	if r := rest(set, 1); len(r) != 2 || r[0] != (piece{0, [2]int64{1, 2}}) {
		t.Fatalf("rest past 1: %v", r)
	}
	if p := putBack(left, set); len(p) != 4 || p[0] != pool[0] || p[3] != pool[3] {
		t.Fatalf("putBack: %v", p)
	}
}
