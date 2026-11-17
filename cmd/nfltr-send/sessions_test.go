// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/nfltr/send/xfer"
)

// The sender serves at most its cap of sessions at once and refuses the
// next one with a reason; a session that makes no progress for the idle
// bound ends and frees its slot (review 2026-11-17, F3).
func TestSessionsAreCappedAndIdleOnesEnd(t *testing.T) {
	src, err := xfer.NewSource([]string{offer(t)})
	if err != nil {
		t.Fatal(err)
	}
	l := newLimiter(1, 200*time.Millisecond)
	served := make(chan error, 2)
	serveOne := func(c net.Conn) {
		_, _, err := src.Serve(c)
		_ = c.Close()
		served <- err
	}
	stalled, peer := net.Pipe() // a receiver that never reads
	defer peer.Close()          //nolint:errcheck
	l.take(stalled, serveOne)

	x, y := net.Pipe()
	l.take(x, serveOne)
	a, err := xfer.Receive(y, t.TempDir(), nil)
	if !errors.Is(err, xfer.ErrRefused) || !strings.Contains(a.Message, "busy") {
		t.Fatalf("session past the cap: %+v, %v; want refused, busy", a, err)
	}

	select {
	case err := <-served:
		if err == nil {
			t.Fatal("the stalled session ended without an error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a stalled session still holds its slot")
	}
	x, y = net.Pipe()
	l.take(x, serveOne)
	if a, err := xfer.Receive(y, t.TempDir(), nil); err != nil || !a.OK {
		t.Fatalf("after the stalled session ended: %+v, %v", a, err)
	}
}
