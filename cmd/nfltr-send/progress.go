// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nfltr/relay/client/nfnet"
	"github.com/nfltr/send/internal/xfer"
)

// meter is a transfer's progress on w (a transfer of minutes otherwise
// printed nothing until its end), at the receiver and the sender: on a
// terminal one line redrawn in place, off one a plain line each tick;
// nothing while no session is open. Messages written through it (Write)
// clear the drawn line first.
type meter struct {
	mu    sync.Mutex
	w     io.Writer
	tty   bool
	verb  string // received, sent
	now   func() time.Time
	state func() (path string, sessions int) // called outside mu
	// total and held: the offer's file bytes and those the stage held at
	// the session's start (xfer.Receiving.Start); n: what the session
	// wrote since; written: what this receive wrote, for the rate
	total, held, n, written int64
	first                   time.Time // the first byte
	width                   int       // the drawn line's length; 0: none drawn
	quit                    chan struct{}
	stopOnce                sync.Once
	loop                    sync.WaitGroup
}

func newMeter(w io.Writer, tty bool, verb string, state func() (string, int)) *meter {
	return &meter{w: w, tty: tty, verb: verb, now: time.Now, state: state, quit: make(chan struct{})}
}

// isTerminal: w is a terminal (a character device).
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// progressEvery is the pause between progress lines.
func progressEvery(tty bool) time.Duration {
	if tty {
		return time.Second
	}
	return 30 * time.Second
}

func (m *meter) start(total, held int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.total, m.held, m.n = total, held, 0
}

// reset ends a transfer: no line until the next start.
func (m *meter) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.total, m.held, m.n, m.written, m.first = 0, 0, 0, 0, time.Time{}
}

func (m *meter) wrote(n, written int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.first.IsZero() {
		m.first = m.now()
	}
	m.n, m.written = n, written
}

// run ticks every d until stop.
func (m *meter) run(d time.Duration) {
	m.loop.Go(func() {
		t := time.NewTicker(d)
		defer t.Stop()
		for {
			select {
			case <-m.quit:
				return
			case <-t.C:
				m.tick()
			}
		}
	})
}

// stop ends the loop and clears the drawn line: no line after it.
func (m *meter) stop() {
	m.stopOnce.Do(func() { close(m.quit) })
	m.loop.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clear()
}

func (m *meter) tick() {
	path, sessions := m.state()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.total == 0 || sessions == 0 {
		return // no transfer under way, or no session open
	}
	done := min(m.held+m.n, m.total) // a part sent again counts twice
	var rate float64
	var left time.Duration
	if el := m.now().Sub(m.first).Seconds(); !m.first.IsZero() && el > 0 {
		rate = float64(m.written) / el
	}
	if rate > 0 {
		left = time.Duration(float64(m.total-done) / rate * float64(time.Second))
	}
	line := progressLine(m.verb, done, m.total, rate, left)
	if path != "" {
		line += ", path " + path
	}
	line += fmt.Sprintf(", %d %s", sessions, plural(sessions, "session"))
	if !m.tty {
		fmt.Fprintln(m.w, line)
		return
	}
	fmt.Fprint(m.w, "\r"+line+strings.Repeat(" ", max(0, m.width-len(line))))
	m.width = len(line)
}

func (m *meter) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clear()
	return m.w.Write(p)
}

// clear erases the drawn line; m.mu held.
func (m *meter) clear() {
	if m.width > 0 {
		fmt.Fprint(m.w, "\r"+strings.Repeat(" ", m.width)+"\r")
		m.width = 0
	}
}

// progressLine is "<verb> <done> of <total> (<%>)", then the rate and
// the time left when known (rate in bytes/s, left 0: unknown).
func progressLine(verb string, done, total int64, rate float64, left time.Duration) string {
	unit, div, prec := "MB", 1e6, 1
	if total >= 1e9 {
		unit, div, prec = "GB", 1e9, 2
	}
	s := fmt.Sprintf("%s %.*f of %.*f %s (%d %%)", verb, prec, float64(done)/div, prec, float64(total)/div, unit, done*100/total)
	if rate <= 0 {
		return s
	}
	if mb := rate / 1e6; mb < 10 {
		s += fmt.Sprintf(", %.1f MB/s", mb)
	} else {
		s += fmt.Sprintf(", %.0f MB/s", mb)
	}
	switch {
	case left <= 0:
	case left < 90*time.Second:
		s += fmt.Sprintf(", %.0f s left", math.Ceil(left.Seconds()))
	case left < 90*time.Minute:
		s += fmt.Sprintf(", %.0f min left", left.Minutes())
	default:
		s += fmt.Sprintf(", %.1f h left", left.Hours())
	}
	return s
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// sending is the sender's progress: the sessions served at once are one
// transfer; a session opening while none is open starts the receive's
// next run (its want's held: what the receiver's stage held). ponytail:
// one line for the address; two receivers at once are summed as one
// transfer (held from the latest run): a line per receiver needs the want
// to name its receive.
type sending struct {
	m     *meter
	mu    sync.Mutex
	total int64         // the source's file bytes
	open  int           // sessions past their want
	run   int64         // bytes sent since the run's first want
	all   int64         // bytes sent since the transfer's first
	path  func() string // the latest session's path
}

func newSending(w io.Writer, tty bool, total int64) *sending {
	t := &sending{total: total}
	t.m = newMeter(w, tty, "sent", t.state)
	return t
}

func (t *sending) state() (string, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.path == nil {
		return "", t.open
	}
	return t.path(), t.open
}

// want: a session read its want.
func (t *sending) want(held int64, path func() string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.open == 0 {
		t.run = 0
		t.m.start(t.total, held)
	}
	t.open++
	t.path = path
}

func (t *sending) sent(n int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.run += n
	t.all += n
	t.m.wrote(t.run, t.all)
}

// end: a session ended (wanted: it read its want; complete: the
// transfer's).
func (t *sending) end(wanted, complete bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if wanted {
		t.open--
	}
	if complete {
		t.all = 0
		t.m.reset()
	}
}

// served is how one served session ended, for report.
type served struct {
	a    xfer.Answer
	sent int64 // file bytes this session sent
	err  error
	anon bool
	open int           // sessions open at its end, this one included
	took time.Duration // a transfer's: since its first session began
	path string        // the session's path: direct or relayed
}

// report logs how a served session ended to l and a complete transfer's
// line to w. A cut session is no ended transfer: the receiver resumes its
// part on another session (or a new one, up to its --tries); "transfer
// ended" is a transfer that ended.
func report(l *slog.Logger, w io.Writer, s served) {
	var ne *nfnet.Error
	cut := errors.As(s.err, &ne) && ne.Code == nfnet.CodeReset
	switch {
	case s.err != nil && s.anon && nfnet.CodeOf(s.err) == nfnet.CodeDirectRequired:
		l.Warn("transfer ended: no direct path to the receiver; relaying it needs this machine's account (`nfltr config login`)", "sent", s.sent, "err", s.err)
	case cut && s.open > 1:
		l.Info(fmt.Sprintf("one of the receiver's %d sessions ended after %d bytes; the receiver resumes its part", s.open, s.sent), "err", s.err)
	case cut:
		l.Info(fmt.Sprintf("the receiver's session ended after %d bytes; the receiver resumes with another (up to its --tries)", s.sent), "err", s.err)
	case s.err != nil:
		l.Warn("transfer ended", "sent", s.sent, "err", s.err)
	case s.a.Part:
		l.Debug("part of the transfer in (another session gets the final answer)", "sent", s.sent)
	case !s.a.OK:
		l.Warn("receiver refused", "reason", s.a.Message)
	default:
		el := s.took.Seconds()
		l.Info("transfer done", "files", s.a.Files, "bytes", s.a.Bytes, "sent", s.sent, "seconds", el)
		fmt.Fprintf(w, "sent %d %s, %d bytes (%d this session) in %.1f s (%.0f MB/s), path %s\n", s.a.Files, filesWord(s.a.Files), s.a.Bytes, s.sent, el, float64(s.a.Bytes)/1e6/el, s.path)
	}
}
