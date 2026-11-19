// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/nfltr/relay/client/nfnet"
	"github.com/nfltr/send/xfer"
)

// onceChild, set in a child's environment to a relay's agent-link
// address, makes this test binary a --once sender of the directory in
// onceDir that exits when serve returns, as nfltr-send's main does.
const onceChild, onceDir = "NFLTR_SEND_TEST_ONCE_RELAY", "NFLTR_SEND_TEST_ONCE_DIR"

func TestMain(m *testing.M) {
	if relay := os.Getenv(onceChild); relay != "" {
		src, err := xfer.NewSource([]string{os.Getenv(onceDir)})
		if err != nil {
			os.Exit(1)
		}
		os.Exit(serve(context.Background(), nfnet.Config{Relay: "relay.test", AgentRelay: relay, Plaintext: true}, nfnet.DirectOn, nil, src, true, os.Stdout))
	}
	os.Exit(m.Run())
}

// TestOnceExitsAfterTheReceiversEnd (lane SENDBEST, box netem lab,
// 100 Mbit/s, 159 ms: 2 of 25 runs took 78 s instead of 48): a --once
// sender's process exits only after its session ended both ways. It
// returned as soon as its listener started closing, before its session's
// close, so a receiver whose end it had not yet acknowledged waited out
// psCloseBound (30 s) for a peer that was gone.
func TestOnceExitsAfterTheReceiversEnd(t *testing.T) {
	r := anonRelay(t, true)
	dir := offer(t)
	for i := range 8 {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run", "^$") // #nosec G204 -- this test binary
		cmd.Env = append(os.Environ(), onceChild+"="+r.GRPC, onceDir+"="+dir)
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		addr, err := bufio.NewReader(out).ReadString('\n')
		if err != nil {
			t.Fatalf("run %d: no address: %v", i, err)
		}
		t0 := time.Now()
		code := receiveFrom(t, r, addr[:len(addr)-1], nfnet.DirectOn, t.TempDir())
		el := time.Since(t0)
		if err := cmd.Wait(); err != nil || code != 0 {
			t.Fatalf("run %d: receive exit %d, sender %v", i, code, err)
		}
		if el > 10*time.Second {
			t.Fatalf("run %d: receive took %v: it waited for a sender that had exited", i, el)
		}
	}
}

// TestOnceRelayedSenderReadsTheAnswer (lane SENDFIX, docs/reliability.md
// "Defects found and fixed" 1: 74 of 130 relayed runs; spec End.tla Told,
// mutant close-cuts): a --once sender reads the receiver's answer on the
// relayed path and exits. The receiver's close cut its nf1 data
// connection at once, with the answer's last frames and the session's
// CLOSE still unwritten: the relay reset the session and the sender logged
// "session reset by the peer", serving on.
func TestOnceRelayedSenderReadsTheAnswer(t *testing.T) {
	r := anonRelay(t, true)
	dir := offer(t)
	for i := range 20 {
		src, err := xfer.NewSource([]string{dir})
		if err != nil {
			t.Fatal(err)
		}
		pr, pw := io.Pipe()
		served := make(chan int, 1)
		go func() {
			served <- serve(t.Context(), nfnet.Config{Relay: "relay.test", AgentRelay: r.GRPC, Plaintext: true, Token: r.Key}, nfnet.DirectOn, nil, src, true, pw)
			_ = pw.Close()
		}()
		addr, err := readLine(pr)
		if err != nil {
			t.Fatal(err)
		}
		go func() { _, _ = io.Copy(io.Discard, pr) }()
		if code := receiveFrom(t, r, addr, nfnet.DirectOff, t.TempDir()); code != 0 {
			t.Fatalf("run %d: receive exit %d", i, code)
		}
		select {
		case code := <-served:
			if code != 0 {
				t.Fatalf("run %d: serve exit %d", i, code)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("run %d: --once sender still serving after the receiver committed", i)
		}
	}
}
