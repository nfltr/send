// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"context"
	"flag"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nfltr/relay-core/relaycoretest"
	"github.com/nfltr/relay/client/nfnet"
	"github.com/nfltr/send/internal/xfer"
	"github.com/spf13/viper"
)

// anonRelay is a relay core taking nf1 addresses (host relay.test), and
// addresses served with no account when anon (nf1.anon.enabled), as nfltr
// docs/decisions/nf1-anon-2026-11-16.md describes; relay-core's own
// relaycoretest tests set the same keys.
func anonRelay(t testing.TB, anon bool) *relaycoretest.Relay {
	set := map[string]any{"nf1.enabled": true, "nf1.hosts": []string{"relay.test"}, "nf1.anon.enabled": anon}
	for k, v := range set {
		viper.Set(k, v)
	}
	t.Cleanup(func() { // after the relay's own cleanup
		viper.Set("nf1.enabled", false)
		viper.Set("nf1.hosts", nil)
		viper.Set("nf1.anon.enabled", false)
	})
	return relaycoretest.New(t)
}

// serveAnon serves dir's files with no account (serve with no machine)
// and returns the address and serve's exit status.
func serveAnon(t *testing.T, r *relaycoretest.Relay, dir string) (string, <-chan int) {
	src, err := xfer.NewSource([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	pr, pw := io.Pipe()
	code := make(chan int, 1)
	stderr := os.Stderr // read here, not in the goroutine: stderrOf swaps it (-race)
	go func() {
		code <- serve(t.Context(), nfnet.Config{Relay: "relay.test", AgentRelay: r.GRPC, Plaintext: true}, nfnet.DirectOn, nil, src, true, pw, stderr)
		_ = pw.Close()
	}()
	addr, err := bufio.NewReader(pr).ReadString('\n')
	if err != nil {
		t.Fatalf("no address (serve exit %d): %v", <-code, err)
	}
	go func() { _, _ = io.Copy(io.Discard, pr) }()
	return addr[:len(addr)-1], code
}

// connections is the receives' --connections in this package's tests and
// benchmarks (BenchmarkDirectCPU: the transfer's sessions at once).
var connections = flag.Int("connections", 4, "receives: sessions at once")

func receiveFrom(t testing.TB, r *relaycoretest.Relay, addr string, mode nfnet.DirectMode, to string) int {
	return receiveFromK(t, r, addr, mode, to, *connections)
}

func receiveFromK(t testing.TB, r *relaycoretest.Relay, addr string, mode nfnet.DirectMode, to string, k int) int {
	tg, err := nfnet.ParseTarget(addr)
	if err != nil {
		t.Fatal(err)
	}
	cfg := nfnet.Config{Relay: tg.Address.Relay, Plaintext: true, DialRelay: func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", r.Proxy)
	}}
	return receiveTo(t.Context(), os.Stderr, cfg, tg, mode, to, 3, k, false)
}

func offer(t *testing.T) string {
	dir := filepath.Join(t.TempDir(), "offer")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("sent with no account\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestAnonymousSendDirect: a sender with no machine certificate serves
// with no account; a receiver with direct paths gets the files whole over
// the direct path (loopback host candidates), and --once ends the sender.
func TestAnonymousSendDirect(t *testing.T) {
	r := anonRelay(t, true)
	addr, served := serveAnon(t, r, offer(t))
	to := t.TempDir()
	if code := receiveFrom(t, r, addr, nfnet.DirectOn, to); code != 0 {
		t.Fatalf("receive exit %d, want 0", code)
	}
	b, err := os.ReadFile(filepath.Join(to, "offer", "a.txt"))
	if err != nil || string(b) != "sent with no account\n" {
		t.Fatalf("landed %q, %v", b, err)
	}
	select {
	case code := <-served:
		if code != 0 {
			t.Fatalf("serve exit %d, want 0", code)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("--once: the sender still serves after the transfer")
	}
}

// TestAnonymousAddressRelayedExit12: a receiver that takes no direct path
// (--direct off) from a sender with no account exits 12 at once, and
// nothing lands.
func TestAnonymousAddressRelayedExit12(t *testing.T) {
	r := anonRelay(t, true)
	addr, _ := serveAnon(t, r, offer(t))
	to := t.TempDir()
	t0 := time.Now()
	if code := receiveFrom(t, r, addr, nfnet.DirectOff, to); code != 12 {
		t.Fatalf("receive exit %d, want 12 (direct_required)", code)
	}
	if el := time.Since(t0); el > 20*time.Second {
		t.Errorf("exit 12 after %v: want at once", el)
	}
	if es, _ := os.ReadDir(to); len(es) != 0 {
		t.Errorf("landed %v", es)
	}
}

// TestRelayRefusesAnonymousServing: a relay that takes no address served
// without an account (nf1.anon.enabled off, the hosted relay's setting)
// refuses the sender, exit 2 (auth).
func TestRelayRefusesAnonymousServing(t *testing.T) {
	r := anonRelay(t, false)
	src, err := xfer.NewSource([]string{offer(t)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	code := serve(ctx, nfnet.Config{Relay: "relay.test", AgentRelay: r.GRPC, Plaintext: true}, nfnet.DirectOn, nil, src, true, io.Discard, pw)
	_ = pw.Close()
	msg, _ := io.ReadAll(pr)
	if code != 2 || !strings.Contains(string(msg), "serves no address without an account") {
		t.Fatalf("serve exit %d (%s), want 2 saying the relay serves no address without an account", code, msg)
	}
}

// TestNoCertificateDirectOffIsUsage: with no machine certificate and
// --direct off nothing is served: exit 1 before any relay.
func TestNoCertificateDirectOffIsUsage(t *testing.T) {
	t.Setenv("NFLTR_CONFIG", filepath.Join(t.TempDir(), "nfltr.json"))
	if code := sendCmd(t.Context(), []string{"--relay", "127.0.0.1:1", "--direct", "off", offer(t)}); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
}
