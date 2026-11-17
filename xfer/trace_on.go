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
}

type fileTrace struct {
	f    *os.File
	off  int64 // the next byte the receiver writes
	cur  hash.Hash
	done bool // verified: Resume's committed
}

type recvTrace struct {
	st   *stageTrace
	p    int
	pc   string // "recv", "renaming", "done"; "" once ended
	pend map[string]any
	held bool // traceMu held by the caller (the sweep)
	cur  int  // the entry being received
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
		st := &stageTrace{f: f, name: name, m: r.m, state: make([]string, len(r.tops)), verified: map[int]bool{}, active: map[int]bool{}, files: map[int]*fileTrace{}}
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

func chunks(n int64) int { return int((n + maxData - 1) / maxData) }

// trStart: a file's start frame (Resume Open, with the part on disk before
// the receiver cuts it to the offset; Commit Write of its top).
func (r *receiver) trStart(i int, off int64) {
	st := r.tr.st
	if st == nil {
		return
	}
	r.tr.cur = i
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
	part := []string{}
	if g, err := r.sr.Open(e.Path); err == nil {
		for {
			h := sha256.New()
			n, err := io.CopyN(h, g, maxData)
			if n > 0 {
				part = append(part, chunk(h))
			}
			if err != nil || len(part) == chunks(e.Size) {
				break
			}
		}
		_ = g.Close()
	}
	k := -1
	switch {
	case off == e.Size:
		k = chunks(e.Size)
	case off%maxData == 0:
		k = int(off / maxData)
	}
	ft.off, ft.cur = off, sha256.New()
	st.emit(ft.f, map[string]any{"ev": "Open", "part": part, "off": k})
}

// trData: a data frame written; a chunk complete on disk is Resume Send.
func (r *receiver) trData(p []byte) {
	if r.tr.st == nil || r.tr.st.files[r.tr.cur] == nil {
		return
	}
	ft, size := r.tr.st.files[r.tr.cur], r.m.Entries[r.tr.cur].Size
	for len(p) > 0 {
		end := min((ft.off/maxData+1)*maxData, size)
		n := min(int64(len(p)), end-ft.off)
		ft.cur.Write(p[:n])
		ft.off += n
		p = p[n:]
		if ft.off == end {
			r.tr.st.emit(ft.f, map[string]any{"ev": "Send", "i": chunks(ft.off), "v": chunk(ft.cur)})
			ft.cur = sha256.New()
		}
	}
}

// trVerified: a file's digest over what is on disk compared (Resume
// Finish); its top's last file verified is Commit Verify.
func (r *receiver) trVerified(ok bool) {
	st := r.tr.st
	if st == nil {
		return
	}
	if ft := st.files[r.tr.cur]; ft != nil {
		st.emit(ft.f, map[string]any{"ev": "Finish", "ok": ok})
		ft.done = ft.done || ok
	}
	if !ok {
		return
	}
	st.verified[r.tr.cur] = true
	if t := st.topOf[r.tr.cur]; st.state[t] == "part" && st.topVerified(t) {
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

// trCut: the session ended inside a file (Resume Cut).
func (r *receiver) trCut() {
	if r.tr.st != nil && r.tr.st.files[r.tr.cur] != nil {
		r.tr.st.emit(r.tr.st.files[r.tr.cur].f, map[string]any{"ev": "Cut"})
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

// trCommit: Finish or Refuse, written before the stage's removal under its
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
