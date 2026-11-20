// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nfltr/send/xfer"
)

// The sender serves at most its cap of sessions at once and refuses the
// next one with a reason; a session whose peer vanishes mid-transfer ends
// when its connection does (in production the agent link's ping liveness
// or the direct path's QUIC idle timeout ends it; here the pipe's close)
// and frees its slot (review 2026-11-17, F3).
func TestSessionsAreCappedAndEndWithTheirPeer(t *testing.T) {
	dir := offer(t)
	big := make([]byte, 4<<20)
	if err := os.WriteFile(filepath.Join(dir, "big.bin"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	src, err := xfer.NewSource([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	l := newLimiter(1)
	served := make(chan error, 2)
	serveOne := func(c net.Conn) {
		_, _, err := src.Serve(c)
		_ = c.Close()
		served <- err
	}
	gone, peer := net.Pipe()
	l.take(gone, serveOne)
	buf := make([]byte, 1<<20) // the transfer is under way, then the peer vanishes
	if _, err := peer.Read(buf); err != nil {
		t.Fatal(err)
	}

	x, y := net.Pipe()
	l.take(x, serveOne)
	a, err := xfer.Receive(y, t.TempDir(), nil)
	if !errors.Is(err, xfer.ErrRefused) || !strings.Contains(a.Message, "busy") {
		t.Fatalf("session past the cap: %+v, %v; want refused, busy", a, err)
	}

	_ = peer.Close()
	select {
	case err := <-served:
		if err == nil {
			t.Fatal("the session whose peer vanished ended without an error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a session whose peer vanished still holds its slot")
	}
	// served is sent from inside serve; take frees the slot once serve
	// returned, so wait for that state (it read busy on the CI runner).
	for deadline := time.Now().Add(10 * time.Second); len(l.slots) > 0; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("a session whose peer vanished still holds its slot")
		}
	}
	x, y = net.Pipe()
	l.take(x, serveOne)
	if a, err := xfer.Receive(y, t.TempDir(), nil); err != nil || !a.OK {
		t.Fatalf("after the vanished session ended: %+v, %v", a, err)
	}
}

// A flood of sessions past the cap costs a bounded number of goroutines:
// a few are refused with a reason, the rest closed outright (review
// 2026-11-17 #2, finding C).
func TestRefusalsPastTheCapAreBounded(t *testing.T) {
	l := newLimiter(1)
	hold := make(chan struct{})
	defer close(hold)
	a, _ := net.Pipe()
	l.take(a, func(net.Conn) { <-hold })
	base := runtime.NumGoroutine()
	var peers []net.Conn
	for range 200 { // peers that never read: a refusal waits out its deadline
		x, y := net.Pipe()
		peers = append(peers, y)
		l.take(x, func(net.Conn) { t.Error("served past the cap") })
	}
	if n := runtime.NumGoroutine() - base; n > maxRefusing+5 {
		t.Fatalf("%d goroutines for 200 refused sessions", n)
	}
	_ = peers[len(peers)-1].SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := peers[len(peers)-1].Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("a session past the refusal cap: %v, want closed", err)
	}
}
