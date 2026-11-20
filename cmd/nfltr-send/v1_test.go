// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nfltr/relay/client/nfnet"
	"github.com/nfltr/send/xfer"
)

// receive refuses a relay flag (the address names its relay) as a usage
// error, never ignores it, and ignores $NFLTR_SEND_RELAY (send's, set in
// a profile); --direct takes on or off only.
func TestReceiveRefusesARelayAndDirectIsOnOrOff(t *testing.T) {
	t.Setenv("NFLTR_SEND_RELAY", "")
	for name, c := range map[string]struct {
		env  string
		args []string
		say  string
	}{
		"--relay":            {"", []string{"--relay", "x:443"}, "takes no --relay"},
		"--relay=":           {"", []string{"--relay=x:443"}, "takes no --relay"},
		"--relay-http":       {"", []string{"--relay-http", "x"}, "takes no --relay-http"},
		"$NFLTR_SEND_RELAY":  {"x:443", nil, "open /nonexistent"}, // ignored: on to the address file
		"--direct true":      {"", []string{"--direct", "true"}, `--direct "true": want on or off`},
		"--direct (empty)":   {"", []string{"--direct="}, `--direct "": want on or off`},
		"--relay-pin (gone)": {"", []string{"--relay-pin", "00"}, "not defined: -relay-pin"},
	} {
		t.Setenv("NFLTR_SEND_RELAY", c.env)
		var code int
		msg := stderrOf(t, func() {
			code = receiveCmd(t.Context(), append(c.args, "--address-file", "/nonexistent"))
		})
		if code != 1 || !strings.Contains(msg, c.say) || strings.Count(msg, "\n") != 1 {
			t.Errorf("%s: exit %d, %q: want 1 saying %q on one line", name, code, msg, c.say)
		}
	}
	t.Setenv("NFLTR_SEND_RELAY", "")
	if code := sendCmd(t.Context(), []string{"--direct", "false", offer(t)}); code != 1 {
		t.Errorf("send --direct false: exit %d, want 1", code)
	}
}

// A sender stopped by a signal exits 128 + its number, apart from a
// --once sender whose transfer completed (0).
func TestStoppedSenderExits130Or143(t *testing.T) {
	r := anonRelay(t, true)
	for sig, want := range map[syscall.Signal]int{syscall.SIGINT: 130, syscall.SIGTERM: 143} {
		src, err := xfer.NewSource([]string{offer(t)})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancelCause(t.Context())
		pr, pw := io.Pipe()
		code := make(chan int, 1)
		stderr := os.Stderr // read here, not in the goroutine: stderrOf swaps it (-race)
		go func() {
			code <- serve(ctx, nfnet.Config{Relay: "relay.test", AgentRelay: r.GRPC, Plaintext: true}, nfnet.DirectOn, nil, src, false, pw, stderr)
		}()
		if _, err := readLine(pr); err != nil { // serving
			t.Fatal(err)
		}
		cancel(stopped{sig})
		select {
		case got := <-code:
			if got != want {
				t.Errorf("stopped by %v: exit %d, want %d", sig, got, want)
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("stopped by %v: serve did not return", sig)
		}
	}
}

func TestVersion(t *testing.T) {
	if v := versionLine(); !strings.HasPrefix(v, "nfltr-send ") || !strings.HasSuffix(v, " "+runtime.Version()) || len(strings.Fields(v)) != 4 {
		t.Fatalf("version %q: want nfltr-send <version> <commit> <go version>", v)
	}
}
