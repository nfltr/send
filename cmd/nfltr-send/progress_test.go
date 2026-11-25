// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nfltr/relay/client/nfnet"
	"github.com/nfltr/send/internal/xfer"
)

// meterRun drives a meter through a receive: no byte yet, 40 % in, a
// message (a session ended), then the end in sight.
func meterRun(tty bool) string {
	var out bytes.Buffer
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	path, sessions := "relayed", 1
	m := newMeter(&out, tty, "received", func() (string, int) { return path, sessions })
	m.now = func() time.Time { return now }
	m.tick() // before the offer: nothing to say
	m.start(1_000_000_000, 0)
	m.tick()
	now = now.Add(time.Second)
	m.wrote(1_000_000, 1_000_000)
	now = now.Add(4 * time.Second)
	m.wrote(400_000_000, 400_000_000)
	path, sessions = "direct", 4
	m.tick()
	fmt.Fprintf(m, "session 1 ended (reset); resuming\n")
	now = now.Add(5 * time.Second)
	m.start(1_000_000_000, 400_000_000) // the next session: the stage holds 40 %
	m.wrote(590_000_000, 990_000_000)
	m.tick()
	m.stop()
	return out.String()
}

// The receiver's progress on a terminal: one line redrawn in place (a
// carriage return, spaces over a longer line's rest), cleared under a
// message and at the end.
func TestProgressOnATerminal(t *testing.T) {
	l1 := "received 0.00 of 1.00 GB (0 %), path relayed, 1 session"
	l2 := "received 0.40 of 1.00 GB (40 %), 100 MB/s, 6 s left, path direct, 4 sessions"
	l3 := "received 0.99 of 1.00 GB (99 %), 110 MB/s, 1 s left, path direct, 4 sessions"
	want := "\r" + l1 +
		"\r" + l2 +
		"\r" + strings.Repeat(" ", len(l2)) + "\r" + "session 1 ended (reset); resuming\n" +
		"\r" + l3 +
		"\r" + strings.Repeat(" ", len(l3)) + "\r"
	if got := meterRun(true); got != want {
		t.Fatalf("terminal output\n%q\nwant\n%q", got, want)
	}
}

// Off a terminal: plain lines (the loop prints one every 30 s).
func TestProgressOffATerminal(t *testing.T) {
	want := `received 0.00 of 1.00 GB (0 %), path relayed, 1 session
received 0.40 of 1.00 GB (40 %), 100 MB/s, 6 s left, path direct, 4 sessions
session 1 ended (reset); resuming
received 0.99 of 1.00 GB (99 %), 110 MB/s, 1 s left, path direct, 4 sessions
`
	if got := meterRun(false); got != want {
		t.Fatalf("output\n%s\nwant\n%s", got, want)
	}
}

// The loop ticks until stop, and stop returns with the line cleared and
// no tick after it (stop runs before the receive's last message).
func TestProgressLoopStops(t *testing.T) {
	var out syncBuffer
	m := newMeter(&out, true, "received", func() (string, int) { return "direct", 1 })
	m.start(1000, 0)
	m.run(time.Millisecond)
	for !strings.Contains(out.String(), "received") {
		time.Sleep(time.Millisecond)
	}
	m.stop()
	m.stop() // twice: a no-op
	got := out.String()
	fmt.Fprintf(m, "received 1 file\n")
	if !strings.HasSuffix(got, "\r") || out.String() != got+"received 1 file\n" {
		t.Fatalf("after stop %q", out.String())
	}
}

// syncBuffer is a bytes.Buffer the meter's loop and the test share.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// senderRun drives the sender's progress through a resumed transfer: two
// sessions of a receive whose stage held 40 %, both cut (no receiver:
// nothing new), then one session of the next receive (the stage 60 %),
// its end and the "sent" line.
func senderRun(tty bool) string {
	var out bytes.Buffer
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	tr := newSending(&out, tty, 1_000_000_000)
	tr.m.now = func() time.Time { return now }
	path := func(p string) func() string { return func() string { return p } }
	tr.m.tick() // the address served, no receiver: nothing
	tr.want(400_000_000, path("relayed"))
	tr.want(400_000_000, path("direct"))
	now = now.Add(time.Second)
	tr.sent(1_000_000)
	now = now.Add(4 * time.Second)
	tr.sent(199_000_000)
	tr.m.tick()
	tr.end(true, false)
	tr.end(true, false)
	tr.m.tick() // no receiver connected: nothing new
	now = now.Add(5 * time.Second)
	tr.want(600_000_000, path("direct"))
	tr.sent(300_000_000)
	tr.m.tick()
	tr.sent(100_000_000)
	tr.end(true, true)
	tr.m.tick() // done: nothing
	fmt.Fprintf(tr.m, "sent 1 file, 1000000000 bytes (400000000 this session) in 11.0 s (91 MB/s), path direct\n")
	tr.m.stop()
	return out.String()
}

// The sender's progress: what the receiver held counts as done (60 % on
// the first line: 40 % held, 20 % sent), the line waits while no receiver
// is connected, and the final "sent" line ends it.
func TestSenderProgress(t *testing.T) {
	l1 := "sent 0.60 of 1.00 GB (60 %), 50 MB/s, 8 s left, path direct, 2 sessions"
	l2 := "sent 0.90 of 1.00 GB (90 %), 56 MB/s, 2 s left, path direct, 1 session"
	end := "sent 1 file, 1000000000 bytes (400000000 this session) in 11.0 s (91 MB/s), path direct\n"
	want := "\r" + l1 + "\r" + l2 + " " + "\r" + strings.Repeat(" ", len(l2)) + "\r" + end
	if got := senderRun(true); got != want {
		t.Fatalf("terminal output\n%q\nwant\n%q", got, want)
	}
	if got, want := senderRun(false), l1+"\n"+l2+"\n"+end; got != want {
		t.Fatalf("output\n%s\nwant\n%s", got, want)
	}
}

func TestProgressUnits(t *testing.T) {
	for _, c := range []struct {
		done, total int64
		rate        float64
		left        time.Duration
		want        string
	}{
		{5_000, 2_000_000, 0, 0, "received 0.0 of 2.0 MB (0 %)"},
		{1_500_000, 2_000_000, 0.5e6, 0, "received 1.5 of 2.0 MB (75 %), 0.5 MB/s"},
		{0, 300_000_000_000, 85e6, 3529 * time.Second, "received 0.00 of 300.00 GB (0 %), 85 MB/s, 59 min left"},
		{0, 300_000_000_000, 8.5e6, 35294 * time.Second, "received 0.00 of 300.00 GB (0 %), 8.5 MB/s, 9.8 h left"},
	} {
		if got := progressLine("received", c.done, c.total, c.rate, c.left); got != c.want {
			t.Errorf("progressLine(%d, %d, %g, %v) = %q, want %q", c.done, c.total, c.rate, c.left, got, c.want)
		}
	}
}

// The sender's line per session: a cut session of a striped receive is
// INFO (the receiver resumes its part), "transfer ended" WARN only for a
// transfer that ended; a part is debug; the final line has rate and path.
func TestSentMessages(t *testing.T) {
	cut := &nfnet.Error{Code: nfnet.CodeReset, Err: errors.New("session reset: link: session reset by the peer")}
	for _, c := range []struct {
		name string
		s    served
		want string // the log, then the stderr line
	}{
		{"one of four cut", served{sent: 52428800, err: cut, open: 4},
			"level=INFO msg=\"one of the receiver's 4 sessions ended after 52428800 bytes; the receiver resumes its part\" err=\"session reset: link: session reset by the peer\"\n"},
		{"the only one cut", served{sent: 52428800, err: cut, open: 1},
			"level=INFO msg=\"the receiver's session ended after 52428800 bytes; the receiver resumes with another (up to its --tries)\" err=\"session reset: link: session reset by the peer\"\n"},
		{"protocol error", served{sent: 0, err: fmt.Errorf("%w: frame %q", xfer.ErrProtocol, "x"), open: 4},
			"level=WARN msg=\"transfer ended\" sent=0 err=\"nfltr-send: protocol error: frame \\\"x\\\"\"\n"},
		{"direct required", served{err: &nfnet.Error{Code: nfnet.CodeDirectRequired, Err: errors.New("no direct path")}, anon: true, open: 1},
			"level=WARN msg=\"transfer ended: no direct path to the receiver; relaying it needs this machine's account (`nfltr config login`)\" sent=0 err=\"no direct path\"\n"},
		{"part", served{a: xfer.Answer{Part: true}, sent: 1 << 20, open: 4}, ""},
		{"refused", served{a: xfer.Answer{Message: "no space"}, open: 1}, "level=WARN msg=\"receiver refused\" reason=\"no space\"\n"},
		{"done", served{a: xfer.Answer{OK: true, Files: 1, Bytes: 1_000_000_000}, sent: 250_000_000, open: 1, took: 10 * time.Second, path: "direct"},
			"level=INFO msg=\"transfer done\" files=1 bytes=1000000000 sent=250000000 seconds=10\n" +
				"sent 1 file, 1000000000 bytes (250000000 this session) in 10.0 s (100 MB/s), path direct\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			l := slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
				if a.Key == slog.TimeKey {
					return slog.Attr{}
				}
				return a
			}}))
			report(l, &out, c.s)
			if got := out.String(); got != c.want {
				t.Fatalf("\n%s\nwant\n%s", got, c.want)
			}
		})
	}
}
