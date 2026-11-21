// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

//go:build sendtrace

package xfer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"hash/fnv"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Trace mode (go test -tags sendtrace, SENDTRACE=<dir>) writes one JSON
// line per step of spec/Commit.tla and spec/Resume.tla a receiver takes,
// per generation of a stage (from its creation to its removal):
// <dir>/<hash of the receiving dir>-<stage>-g<n>-commit.ndjson, and
// ...-f<entry>.ndjson per non-empty file. spec/TraceCommit.tla and
// spec/TraceResume.tla check each file (make spec-traces).
//
// The tests run receivers (and senders) as goroutines of one process. A
// receiver meets another only through the stage's lock, and the recorder
// writes the line of each acquire and release atomically with it (traceMu),
// so a file's order is a linearization of the receivers' steps; no clock is
// needed. Senders are not recorded: Resume's src and Change are hidden.
const traceOn = true

var (
	traceMu sync.Mutex // a lock's acquire or release with its line
	tabMu   sync.Mutex // stages and the files
	stages  = map[string]*stageTrace{}
	gens    = map[string]int{}
)

type stageTrace struct {
	f        *os.File // the commit trace
	name     string   // the files' prefix
	m        Manifest
	topOf    []int    // entry -> top (1-based)
	state    []string // per top: "none", "part", "ok" (the model's stage)
	verified map[int]bool
	active   map[int]bool // receivers in it
	files    map[int]*fileTrace
	stripes  map[int]*os.File // entry -> its Stripe.tla trace
}

type fileTrace struct {
	f    *os.File
	done bool // verified: Resume's committed
	pend bool // verified in this session, its Finish written at the commit's recheck
	open bool // opened in this session, not yet verified: a Cut if it ends first
}

type recvTrace struct {
	st     *stageTrace
	p      int
	pc     string // "recv", "renaming", "done"; "" once ended
	pend   map[string]any
	held   bool // traceMu held by the caller (the sweep)
	single bool // one session: Resume.tla's per-file lines (a striped receive has only Stripe.tla's)
}

// sessTrace is a session's: the entry it receives, and per entry whether
// its set there is still being read (Stripe.tla's st[k] = "open").
type sessTrace struct {
	cur  int
	open map[int]bool
}

func traceAtomic() func() { traceMu.Lock(); return traceMu.Unlock }

func (r *receiver) trAtomic() func() {
	if r.tr.held {
		return func() {}
	}
	return traceAtomic()
}

func stageKey(root *os.Root, stage string) string { return root.Name() + "\x00" + stage }

func (st *stageTrace) emit(f *os.File, line map[string]any) {
	b, err := json.Marshal(line)
	if err != nil {
		panic(err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		panic(err)
	}
}

func (r *receiver) line(ev string, t int, kv ...any) {
	l := map[string]any{"ev": ev, "p": r.tr.p}
	if t >= 0 {
		l["t"] = t + 1
	}
	for i := 0; i+1 < len(kv); i += 2 {
		l[kv[i].(string)] = kv[i+1]
	}
	tabMu.Lock()
	defer tabMu.Unlock()
	r.tr.st.emit(r.tr.st.f, l)
}

// trOpen: the receiver's open of the stage (newReceiver, under traceMu):
// "Busy" (the lock held), "Taken" (refused, a name taken) or "Open".
func (r *receiver) trOpen(created bool, outcome string) {
	dir := os.Getenv("SENDTRACE")
	if dir == "" {
		return
	}
	key := stageKey(r.root, r.stage)
	tabMu.Lock()
	if created {
		gens[key]++
		h := fnv.New32a()
		_, _ = h.Write([]byte(r.root.Name()))
		name := filepath.Join(dir, fmt.Sprintf("%08x-%s-g%d", h.Sum32(), strings.TrimPrefix(r.stage, StagePrefix), gens[key]))
		f, err := os.Create(name + "-commit.ndjson")
		if err != nil {
			panic(err)
		}
		st := &stageTrace{f: f, name: name, m: r.m, state: make([]string, len(r.tops)), verified: map[int]bool{}, active: map[int]bool{}, files: map[int]*fileTrace{}, stripes: map[int]*os.File{}}
		for i := range st.state {
			st.state[i] = "none"
		}
		for _, e := range r.m.Entries {
			top, _, _ := strings.Cut(e.Path, "/")
			for j, t := range r.tops {
				if t == top {
					st.topOf = append(st.topOf, j)
				}
			}
		}
		stages[key] = st
		st.emit(f, map[string]any{"ev": "Init", "n": len(r.tops)})
	}
	r.tr.st = stages[key]
	tabMu.Unlock()
	if r.tr.st == nil { // a stage this process did not make
		return
	}
	r.tr.p = r.tr.st.idle()
	switch outcome {
	case "Open":
		r.tr.st.active[r.tr.p] = true
		r.tr.pc = "recv"
		if r.finishing {
			r.tr.pc = "renaming"
		}
		r.line("Open", -1, "rec", r.finishing)
	default:
		r.line(outcome, -1)
		r.tr.st = nil
	}
}

func (st *stageTrace) idle() int {
	p := 1
	for st.active[p] {
		p++
	}
	return p
}

// trSwept: the sweep opens a stale stage holding its intent (under
// traceMu, held by the sweeping receive's newReceiver).
func (r *receiver) trSwept() {
	tabMu.Lock()
	r.tr.st = stages[stageKey(r.root, r.stage)]
	tabMu.Unlock()
	r.tr.held = true
	if r.tr.st == nil {
		return
	}
	r.tr.p = r.tr.st.idle()
	r.tr.st.active[r.tr.p] = true
	r.tr.pc = "renaming"
	r.line("Open", -1, "rec", true)
}

// traceExpire: the sweep removes a stage without its intent.
func traceExpire(root *os.Root, stage string) {
	tabMu.Lock()
	st := stages[stageKey(root, stage)]
	if st != nil {
		st.emit(st.f, map[string]any{"ev": "Expire"})
	}
	tabMu.Unlock()
}

// chunk is a Resume.tla chunk's value: its bytes' digest, short.
func chunk(h hash.Hash) string { return hex.EncodeToString(h.Sum(nil))[:12] }

// trStart: a file's start frame (Resume Open: the part on disk, a chunk's
// digest or "h" where it has no byte, and the chunks asked for; Commit
// Write of its top).
func (r *receiver) trStart(s *sess, i int, todo [][2]int64) {
	s.tr.cur = i
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.tr.st
	if st == nil || !r.tr.single {
		return
	}
	e := r.m.Entries[i]
	if t := st.topOf[i]; st.state[t] == "none" {
		st.state[t] = "part"
		r.line("Write", t)
	}
	if e.Size == 0 { // Resume does not model an empty file
		return
	}
	ft := st.files[i]
	if ft == nil {
		f, err := os.Create(fmt.Sprintf("%s-f%d.ndjson", st.name, i))
		if err != nil {
			panic(err)
		}
		ft = &fileTrace{f: f}
		st.files[i] = ft
		st.emit(f, map[string]any{"ev": "Init", "n": chunks(e.Size)})
	}
	part := make([]string, chunks(e.Size))
	g, err := r.sr.Open(e.Path)
	for k := range part {
		part[k] = "h"
		if err != nil {
			continue
		}
		h := sha256.New()
		if n, _ := io.Copy(h, io.NewSectionReader(g, int64(k)*maxData, min(maxData, e.Size-int64(k)*maxData))); n > 0 {
			part[k] = chunk(h)
		}
	}
	if err == nil {
		_ = g.Close()
	}
	asked := []int64{}
	for _, t := range todo {
		for c := t[0]; c < t[1]; c++ {
			asked = append(asked, c+1)
		}
	}
	st.emit(ft.f, map[string]any{"ev": "Open", "part": part, "need": asked})
	ft.open = true
}

// trData: a chunk verified and written at off (Resume Send).
func (r *receiver) trData(s *sess, p []byte, off int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stripeLine(s, "Send", off/maxData+1)
	if r.tr.st == nil || !r.tr.single || r.tr.st.files[s.tr.cur] == nil {
		return
	}
	h := sha256.New()
	h.Write(p)
	r.tr.st.emit(r.tr.st.files[s.tr.cur].f, map[string]any{"ev": "Send", "i": off/maxData + 1, "v": chunk(h)})
}

// trBad: a chunk that does not verify, refused (Resume Bad).
func (r *receiver) trBad(s *sess, p []byte, off int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stripeLine(s, "Bad", off/maxData+1)
	if r.tr.st == nil || !r.tr.single || r.tr.st.files[s.tr.cur] == nil {
		return
	}
	h := sha256.New()
	h.Write(p)
	r.tr.st.emit(r.tr.st.files[s.tr.cur].f, map[string]any{"ev": "Bad", "i": off/maxData + 1, "v": chunk(h)})
}

// trVerified: a file's digest over what is on disk compared (Resume
// Finish, written now when it fails; when it verifies, at the commit's
// recheck, trRechecked, or a Cut if the session ends first: Resume's
// Finish commits, and the offer commits only after its recheck, Offer.tla);
// its top's last file verified is Commit Verify.
func (r *receiver) trVerified(i int, ok bool) {
	st := r.tr.st
	if st == nil {
		return
	}
	if ft := st.files[i]; ft != nil {
		ft.open = false
		if ok {
			ft.pend = true
		} else {
			st.emit(ft.f, map[string]any{"ev": "Finish", "ok": false})
		}
	}
	if !ok {
		return
	}
	st.verified[i] = true
	if t := st.topOf[i]; st.state[t] == "part" && st.topVerified(t) {
		st.state[t] = "ok"
		r.line("Verify", t)
	}
}

func (st *stageTrace) topVerified(t int) bool {
	for i, e := range st.m.Entries {
		if st.topOf[i] == t && !e.Dir && !st.verified[i] {
			return false
		}
	}
	return true
}

// trRechecked: the commit's recheck (Offer.tla Commit) passed (bad < 0)
// or refused entry bad: each file verified in this session takes its
// Finish, bad's failed (Resume Finish after a hidden Rot).
func (r *receiver) trRechecked(bad int) {
	st := r.tr.st
	if st == nil {
		return
	}
	for i, ft := range st.files {
		if ft.pend {
			st.emit(ft.f, map[string]any{"ev": "Finish", "ok": i != bad})
			ft.done = ft.done || i != bad
			ft.pend = false
		}
	}
}

// trCutPending: the receive ended before the commit's recheck: each file
// verified in it, or received whole but not yet verified, takes a Cut (its part stays, verified again next session).
func (r *receiver) trCutPending() {
	if r.tr.st == nil {
		return
	}
	for _, ft := range r.tr.st.files {
		if ft.pend || ft.open { // verified, or received whole, before the final check
			r.tr.st.emit(ft.f, map[string]any{"ev": "Cut"})
			ft.pend, ft.open = false, false
		}
	}
}

// trCut: the session ended inside a file (Resume Cut).
func (r *receiver) trCut(s *sess) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tr.st != nil && r.tr.single && r.tr.st.files[s.tr.cur] != nil {
		r.tr.st.emit(r.tr.st.files[s.tr.cur].f, map[string]any{"ev": "Cut"})
		r.tr.st.files[s.tr.cur].open = false
	}
}

// Stripe.tla's lines, one file per non-empty entry of a stage's
// generation (<name>-s<entry>.ndjson, spec/TraceStripe.tla): Start (a
// receive's pool there: the chunks not verifying on disk), Open (a
// session k's set there), Send and Bad (Read: a chunk written, refused),
// Cut and Close (a session ended before its set there was read: died, or
// closed by the receiver) and Finish (the commit). Chunks are 1-based.

// stripeLine writes ev of session s's entry (r.mu held), while its set
// there is being read; an ev that ends it (its last chunk, Bad) ends it.
func (r *receiver) stripeLine(s *sess, ev string, i int64) {
	st := r.tr.st
	if st == nil || !s.tr.open[s.tr.cur] {
		return
	}
	f := st.stripes[s.tr.cur]
	if f == nil {
		return
	}
	st.emit(f, map[string]any{"ev": ev, "k": s.slot, "i": i})
	if ev == "Bad" {
		s.tr.open[s.tr.cur] = false
		return
	}
	last := int64(0)
	for _, p := range s.set {
		if p.i == s.tr.cur {
			last = p.r[1]
		}
	}
	if i == last {
		s.tr.open[s.tr.cur] = false
	}
}

func chunkList(ps []piece, i int) []int64 {
	out := []int64{}
	for _, p := range ps {
		for c := p.r[0]; p.i == i && c < p.r[1]; c++ {
			out = append(out, c+1)
		}
	}
	return out
}

// trPool: a receive starts on the stage with pool, k sessions at most.
func (r *receiver) trPool(pool []piece, k int) {
	r.tr.single = k == 1
	st := r.tr.st
	if st == nil {
		return
	}
	for i, e := range r.m.Entries {
		if e.Dir || e.Size == 0 {
			continue
		}
		f := st.stripes[i]
		if f == nil {
			var err error
			if f, err = os.Create(fmt.Sprintf("%s-s%d.ndjson", st.name, i)); err != nil {
				panic(err)
			}
			st.stripes[i] = f
			st.emit(f, map[string]any{"ev": "Init", "n": chunks(e.Size)})
		}
		st.emit(f, map[string]any{"ev": "Start", "pool": chunkList(pool, i)})
	}
}

// trSet: session s opens with its set (Open, per entry it holds chunks of).
func (r *receiver) trSet(s *sess) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s.tr.open = map[int]bool{}
	st := r.tr.st
	if st == nil {
		return
	}
	for i, f := range st.stripes {
		if set := chunkList(s.set, i); len(set) > 0 {
			s.tr.open[i] = true
			st.emit(f, map[string]any{"ev": "Open", "k": s.slot, "set": set})
		}
	}
}

// trDone: session s ended (err nil: its set in); each entry whose set it
// had not read is Cut, or Close when the receiver closed it.
func (r *receiver) trDone(s *sess, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.tr.st
	if st == nil || err == nil {
		return
	}
	ev := "Cut"
	if s.closing {
		ev = "Close"
	}
	for i, open := range s.tr.open {
		if open {
			st.emit(st.stripes[i], map[string]any{"ev": ev, "k": s.slot})
			s.tr.open[i] = false
		}
	}
}

// trFinish: the offer committed (Stripe Finish, every file).
func (r *receiver) trFinish() {
	if st := r.tr.st; st != nil {
		for _, f := range st.stripes {
			st.emit(f, map[string]any{"ev": "Finish"})
		}
	}
}

// trBegin: the commit's intent written (Commit Begin), or a name taken
// (Begin's refusal, at the end of the receive). A top holding no file is
// made in the stage here: its Write and Verify.
func (r *receiver) trBegin(ok bool) {
	st := r.tr.st
	if st == nil {
		return
	}
	for t, s := range st.state {
		if s != "ok" && st.topVerified(t) {
			if s == "none" {
				r.line("Write", t)
			}
			st.state[t] = "ok"
			r.line("Verify", t)
		}
	}
	if !ok {
		r.trPend("BeginRefused", -1)
		return
	}
	r.tr.pc = "renaming"
	r.line("Begin", -1)
}

// trRename: a top-level entry put (or found) in place, or the commit
// refused there (a name taken, a moved entry not ours: RenameRefused) or
// stopped (Stop), both at the end of the receive.
func (r *receiver) trRename(t int, err error) {
	switch {
	case r.tr.st == nil:
	case err == nil:
		r.line("Rename", t)
	case errors.Is(err, errExists), errors.Is(err, ErrNotOurs), errors.Is(err, fs.ErrNotExist):
		r.trPend("RenameRefused", t)
	default:
		r.trPend("Stop", -1)
	}
}

func (r *receiver) trPend(ev string, t int) {
	if r.tr.st != nil {
		r.tr.pend = map[string]any{"ev": ev, "t": t}
	}
}

// trCommit: Finish, written before the stage's removal under its
// lock (the caller holds traceMu across both).
func (r *receiver) trCommit(ev string, t int) {
	if r.tr.st == nil {
		return
	}
	r.line(ev, t)
	r.tr.pc = ""
	if ev == "Finish" {
		r.tr.pc = "done"
	}
	delete(r.tr.st.active, r.tr.p)
}

// trEnd: the receive releases the stage's lock (under traceMu): what
// stopped it, or Cut (a session that ended before the commit), or Stop (a
// receiver that died in its commit).
func (r *receiver) trEnd() {
	if r.tr.st == nil {
		return
	}
	switch {
	case r.tr.pend != nil:
		r.line(r.tr.pend["ev"].(string), r.tr.pend["t"].(int))
	case r.tr.pc == "recv":
		r.line("Cut", -1)
	case r.tr.pc == "renaming":
		r.line("Stop", -1)
	}
	delete(r.tr.st.active, r.tr.p)
	r.tr.st, r.tr.pend = nil, nil
}
