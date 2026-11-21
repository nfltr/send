// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package xfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/nfltr/send/internal/xfer/verified"
)

// Session is one session of a transfer, the receiver's end. Reset cuts
// it (a slow one closed, the receive stopped); every session a receive
// takes ends in its Close or Reset.
type Session interface {
	io.ReadWriter
	Close() error
	Reset() error
}

// Opener opens one more session of the transfer: its own connection, for
// a core of its own at both ends (nfltr docs/decisions/send-line-rate-2026-11-21.md
// "Step 3").
type Opener func(ctx context.Context) (Session, error)

// Receiving is a receive's settings.
type Receiving struct {
	Dir string
	// K is the sessions at once (0: 1). A transfer of fewer than K runs
	// of stripeRun chunks takes one per run.
	K int
	// Open opens the sessions after the first; nil: the first only.
	Open     Opener
	Progress Progress
	// End runs on the final answer's session after an OK answer (the
	// sender's clean end: cmd's senderEnd); its error is returned with
	// the answer, wrapped in ErrUnconfirmed. nil: none.
	End func(Session) error
}

// ErrUnconfirmed: a committed receive whose final session ended before
// Receiving.End saw the sender's clean end.
var ErrUnconfirmed = errors.New("committed; the sender did not confirm the end")

// stripeRun is a set's run, in chunks (64 MiB): session k takes runs k,
// k+K, ..., so each reads and writes long runs.
var stripeRun int64 = 256 // a test makes it shorter

// piece is a run of entry i's chunks, [r[0], r[1]), at most stripeRun.
type piece struct {
	i int
	r [2]int64
}

// sess is one session of a receive: its set (pieces in order), the
// chunks of it written so far (a prefix: chunks are written in order).
type sess struct {
	s       Session
	c       *conn
	slot    int // 1..K, the trace's session
	set     []piece
	have    map[int][][2]int64 // the want: per file, the runs not in the set
	done    atomic.Int64
	closing bool // cut by the receiver (the slow tail), not a failure
	tr      sessTrace
}

// ReceiveFrom takes one transfer into o.Dir over first and up to o.K-1
// more sessions at once (spec/Stripe.tla): the chunks not yet on disk
// (verified there: a resume) are the pool; each session's want names a
// set taken out of it, which the sender sends in order and the receiver
// writes in place as it verifies; a session that ends before its set is
// in puts what it did not write back into the pool, for another. The
// offer is committed once, after the last session ended, the pool empty
// and every file verified from disk, on that session's answer; the others
// get Answer.Part. An error before the final answer leaves the stage for
// the next receive to resume from; a committed transfer returns no error
// (but ErrUnconfirmed), whether or not its answer reached the sender.
func ReceiveFrom(ctx context.Context, first Session, o Receiving) (Answer, error) {
	c := newConn(first)
	m, raw, err := manifest(c)
	if err != nil {
		_ = first.Close()
		var rf refused
		if errors.As(err, &rf) {
			return rf.a, err
		}
		return Answer{}, err
	}
	r, err := newReceiver(o.Dir, m, raw)
	if err != nil {
		a := Answer{Message: err.Error()}
		_ = c.writeJSON(frameAnswer, a)
		_ = c.flush()
		_ = first.Close()
		return a, fmt.Errorf("%w: %w", ErrRefused, err)
	}
	defer r.close()
	r.progress = o.Progress
	if r.finishing { // a commit stopped part way: finish it, nothing is sent
		a := Answer{Files: countFiles(m), Bytes: countBytes(m), OK: true}
		if err := r.commit(); err != nil {
			a = Answer{Message: err.Error()}
		}
		return r.answer(&sess{s: first, c: c}, a, o.End)
	}
	parts, err := r.parts()
	if err != nil {
		_ = first.Close()
		return Answer{}, err
	}
	return r.stripe(ctx, &sess{s: first, c: c}, r.pool(parts), o)
}

// answer sends a on s, runs end on it after an OK one, and ends s.
func (r *receiver) answer(s *sess, a Answer, end func(Session) error) (Answer, error) {
	a, err := answer(s.c, a)
	if err == nil && a.OK && end != nil {
		if eerr := end(s.s); eerr != nil {
			err = fmt.Errorf("%w: %w", ErrUnconfirmed, eerr)
		}
	}
	_ = s.s.Close()
	return a, err
}

// stripe runs the sessions until the pool is empty and every session
// ended, then verifies, commits and answers on the last.
func (r *receiver) stripe(ctx context.Context, first *sess, pool []piece, o Receiving) (Answer, error) {
	k := max(1, min(o.K, len(pool)))
	if o.Open == nil {
		k = 1
	}
	r.trPool(pool, k)
	defer r.trCutPending()
	type ended struct {
		s   *sess
		err error
	}
	type opened struct {
		s   *sess
		err error
	}
	var (
		ends     = make(chan ended)
		opens    = make(chan opened)
		active   = map[*sess]bool{}
		slots    = make([]bool, k+1)
		opening  int
		failures int
		closes   int
		lastErr  error
		bg       sync.WaitGroup // Part answers and closes
	)
	defer bg.Wait()
	start := func(s *sess) {
		s.slot = slices.Index(slots[1:], false) + 1
		slots[s.slot] = true
		s.set, pool = take(pool, k-len(active))
		s.have = r.haves(s.set)
		active[s] = true
		r.trSet(s)
		go func() {
			err := s.c.writeJSON(frameWant, want{Version: Version, Parts: wantParts(s.have)})
			if err == nil {
				err = s.c.flush()
			}
			if err == nil {
				err = r.read(s)
			}
			ends <- ended{s, err}
		}()
	}
	part := func(s *sess) { // a session of the transfer that is not the last: its own end
		bg.Go(func() {
			_, _ = answer(s.c, Answer{Part: true})
			_ = s.s.Close()
		})
	}
	open := func() {
		opening++
		go func() {
			s, err := o.Open(ctx)
			if err != nil {
				opens <- opened{nil, err}
				return
			}
			c := newConn(s)
			m, raw, err := manifest(c)
			if err == nil && string(raw) != string(r.raw) {
				err = fmt.Errorf("%w: a session of another offer", ErrProtocol)
				_ = m
			}
			if err != nil {
				_ = s.Reset()
				opens <- opened{nil, err}
				return
			}
			opens <- opened{&sess{s: s, c: c}, nil}
		}()
	}
	// abort cuts every session and waits for them: nothing writes into
	// the stage once the receive returns.
	abort := func(err error) (Answer, error) {
		for s := range active {
			_ = s.s.Reset()
		}
		for len(active) > 0 || opening > 0 {
			select {
			case e := <-ends:
				delete(active, e.s)
				_ = e.s.s.Reset()
			case op := <-opens:
				opening--
				if op.s != nil {
					_ = op.s.s.Reset()
				}
			}
		}
		return Answer{}, err
	}
	start(first)
	for {
		// more sessions for the pool, or one to commit on when the last
		// ended without its answer (a closed one that had its set in)
		for (len(pool) > 0 || len(active) == 0) && len(active)+opening < k && failures+opening < k && o.Open != nil {
			open()
		}
		if len(active) == 0 && opening == 0 {
			return abort(lastErr)
		}
		select {
		case <-ctx.Done():
			return abort(ctx.Err())
		case op := <-opens:
			opening--
			switch {
			case op.err != nil:
				failures++
				if lastErr == nil || errors.Is(op.err, ErrRefused) || errors.Is(op.err, ErrProtocol) {
					lastErr = op.err
				}
			case len(pool) == 0 && len(active) > 0:
				part(op.s) // nothing left for it
			default:
				start(op.s) // with the pool empty and no other session: it commits
			}
		case e := <-ends:
			s := e.s
			delete(active, s)
			slots[s.slot] = false
			pool = putBack(pool, rest(s.set, s.done.Load()))
			r.trDone(s, e.err)
			switch {
			case e.err == nil && len(pool) == 0 && len(active) == 0 && opening == 0:
				return r.final(s, o.End)
			case e.err == nil:
				part(s)
			case errors.Is(e.err, ErrProtocol) || errors.Is(e.err, ErrRefused):
				_ = s.s.Reset()
				return abort(e.err)
			case s.closing:
				closes++
				_ = s.s.Reset()
			default:
				_ = s.s.Reset()
				failures++
				lastErr = e.err
			}
			if len(pool) == 0 && closes < 4*k {
				tail(active)
			}
		}
	}
}

// tailRuns: a session is closed for the slow tail when at least this many
// runs of its set are left (a test sets it).
var tailRuns int64 = 4

// tail closes the session with the most left of its set, when that is at
// least tailRuns runs and no session is closing already: its rest goes to the
// sessions that finished (spec Stripe.tla Close, the slow tail).
func tail(active map[*sess]bool) {
	var slow *sess
	for s := range active {
		if s.closing {
			return
		}
		if left := setLen(s.set) - s.done.Load(); left >= tailRuns*stripeRun && (slow == nil || left > setLen(slow.set)-slow.done.Load()) {
			slow = s
		}
	}
	if slow != nil {
		slow.closing = true
		_ = slow.s.Reset()
	}
}

// final verifies from disk every file this receive did not verify and
// write whole (spec Finish, Trusted), commits and answers on s, the
// transfer's last session.
func (r *receiver) final(s *sess, end func(Session) error) (Answer, error) {
	var a Answer
	for i, e := range r.m.Entries {
		if e.Dir {
			continue
		}
		var err error
		// A file received whole is not read back: about 1 CPU s per GiB
		// (nfltr docs/performance-ledger.md, lane SPEED-RECV; guard
		// TestFreshFileIsTrustedAndATouchAfterItRefuses).
		if !r.trusted(i) {
			err = r.verify(i, verified.Tree{Root: root(e), Size: e.Size, Outboard: r.tree(i)})
		}
		r.trVerified(i, err == nil)
		if err != nil {
			_ = s.s.Close()
			return Answer{}, err
		}
		if afterVerify != nil {
			afterVerify(filepath.Join(r.root.Name(), r.stage, e.Path))
		}
		a.Files++
		a.Bytes += e.Size
	}
	if err := r.commit(); err != nil { // verified: the stage stays for the next session
		return r.answer(s, Answer{Message: err.Error()}, end)
	}
	r.trFinish()
	a.OK = true
	return r.answer(s, a, end)
}

// afterVerify, when set, runs after each file verified (a test writes one).
var afterVerify func(name string)

// pool is every chunk the parts do not hold, in stripeRun pieces, in
// manifest order.
func (r *receiver) pool(parts []Part) (out []piece) {
	have := map[int][][2]int64{}
	for _, p := range parts {
		have[p.Index] = p.Have
	}
	for i, e := range r.m.Entries {
		if !e.Dir {
			out = appendPieces(out, i, need(have[i], chunks(e.Size)))
		}
	}
	return out
}

// appendPieces appends entry i's runs to ps, cut into stripeRun pieces.
func appendPieces(ps []piece, i int, runs [][2]int64) []piece {
	for _, rn := range runs {
		for a := rn[0]; a < rn[1]; a += stripeRun {
			ps = append(ps, piece{i, [2]int64{a, min(a+stripeRun, rn[1])}})
		}
	}
	return ps
}

// take is every free-th piece of pool from its first (a session's share
// of free sessions'), and what is left.
func take(pool []piece, free int) (set, left []piece) {
	for j, p := range pool {
		if j%max(free, 1) == 0 {
			set = append(set, p)
		} else {
			left = append(left, p)
		}
	}
	return set, left
}

// rest is set but its first done chunks.
func rest(set []piece, done int64) (out []piece) {
	for _, p := range set {
		n := p.r[1] - p.r[0]
		if done >= n {
			done -= n
			continue
		}
		out = append(out, piece{p.i, [2]int64{p.r[0] + done, p.r[1]}})
		done = 0
	}
	return out
}

// putBack merges ps into pool, in order.
func putBack(pool, ps []piece) []piece {
	pool = append(pool, ps...)
	slices.SortFunc(pool, func(a, b piece) int {
		if a.i != b.i {
			return a.i - b.i
		}
		return int(a.r[0] - b.r[0])
	})
	return pool
}

func setLen(set []piece) (n int64) {
	for _, p := range set {
		n += p.r[1] - p.r[0]
	}
	return n
}

// haves is a set's want: per file, the runs of its chunks not in the set
// (every file named: a file with none of the set is kept whole).
func (r *receiver) haves(set []piece) map[int][][2]int64 {
	in := map[int][][2]int64{}
	for _, p := range set {
		in[p.i] = append(in[p.i], p.r)
	}
	out := map[int][][2]int64{}
	for i, e := range r.m.Entries {
		if e.Dir {
			continue
		}
		if h := need(merge(in[i]), chunks(e.Size)); len(h) > 0 {
			out[i] = h
		}
	}
	return out
}

// merge joins adjacent runs (in order).
func merge(runs [][2]int64) (out [][2]int64) {
	for _, rn := range runs {
		if n := len(out); n > 0 && out[n-1][1] == rn[0] {
			out[n-1][1] = rn[1]
		} else {
			out = append(out, rn)
		}
	}
	return out
}

func wantParts(have map[int][][2]int64) []Part {
	out := make([]Part, 0, len(have))
	for i, h := range have {
		out = append(out, Part{Index: i, Have: h})
	}
	slices.SortFunc(out, func(a, b Part) int { return a.Index - b.Index })
	return out
}

// tree is entry i's tree once a session read it whole, else nil.
func (r *receiver) tree(i int) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.trees[i]
}

// keepTree keeps ob as entry i's tree unless one is kept already (it
// returns the kept one), written to the stage for a later session's
// resume (keptTree).
func (r *receiver) keepTree(i int, ob []byte) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t := r.trees[i]; t != nil {
		return t, nil
	}
	if !oneChunk(r.m.Entries[i].Size) {
		if err := r.sr.WriteFile(treeName(i), ob, 0o600); err != nil {
			return nil, err
		}
	}
	if r.trees == nil {
		r.trees = map[int][]byte{}
	}
	r.trees[i] = ob
	return ob, nil
}

// wrote counts n file bytes written, for the progress.
func (r *receiver) wrote(n int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.written += n
	if r.progress != nil {
		r.progress(r.written)
	}
}
