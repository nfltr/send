// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nfltr/relay/client/relaytrust"
)

// saveRelay writes an nfltr config as `nfltr config set-relay` leaves it,
// with a credential beside the relay fields that must never be read.
func saveRelay(t *testing.T, server, proxy, pin string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("NFLTR_CONFIG", filepath.Join(dir, "nfltr.json"))
	t.Setenv("NFLTR_SEND_RELAY", "")
	t.Setenv(relaytrust.PinEnv, "")
	b := `{"server":"` + server + `","proxy_url":"` + proxy + `","relay_cert_sha256":"` + pin + `","api_key":"never-read"}`
	if err := os.WriteFile(filepath.Join(dir, "nfltr.json"), []byte(b), 0o600); err != nil {
		t.Fatal(err)
	}
}

func relayConfig(t *testing.T, args ...string) (agentRelay, relay string, pinned bool) {
	t.Helper()
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	c := addCommon(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := c.config()
	if err != nil {
		t.Fatal(err)
	}
	return cfg.AgentRelay, cfg.Relay, cfg.RelayTLS != nil
}

// The relay is --relay, else $NFLTR_SEND_RELAY, else the one `nfltr config
// set-relay` saved (its HTTPS URL and certificate pin with it), else
// nfltr.xyz (TRYOUT 2026-11-19 item 3: the saved relay was ignored).
func TestRelayPrecedence(t *testing.T) {
	pin := strings.Repeat("ab", 32)
	saveRelay(t, "relay.lan:8443", "https://relay.lan:8444", pin)
	if a, r, p := relayConfig(t); a != "relay.lan:8443" || r != "relay.lan:8444" || !p {
		t.Errorf("saved relay: %s %s pinned %v, want relay.lan:8443 relay.lan:8444 pinned", a, r, p)
	}
	if a, r, _ := relayConfig(t, "--relay", "grpc.other.example:443"); a != "grpc.other.example:443" || r != "other.example" {
		t.Errorf("--relay: %s %s, want grpc.other.example:443 other.example", a, r)
	}
	t.Setenv("NFLTR_SEND_RELAY", "env.example:443")
	if a, r, _ := relayConfig(t); a != "env.example:443" || r != "env.example" {
		t.Errorf("$NFLTR_SEND_RELAY: %s %s, want env.example:443 env.example", a, r)
	}
	t.Setenv("NFLTR_CONFIG", filepath.Join(t.TempDir(), "nfltr.json")) // none saved
	t.Setenv("NFLTR_SEND_RELAY", "")
	if a, r, p := relayConfig(t); a != "grpc.nfltr.xyz:443" || r != "nfltr.xyz" || p {
		t.Errorf("no saved relay: %s %s pinned %v, want grpc.nfltr.xyz:443 nfltr.xyz unpinned", a, r, p)
	}
}

// A relay that is not reached is named in the error, with --relay.
func TestUnreachableRelayNamesItAndTheFlag(t *testing.T) {
	saveRelay(t, "127.0.0.1:1", "", "") // never a real relay
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	var code int
	msg := stderrOf(t, func() { code = sendCmd(ctx, []string{offer(t)}) })
	if code != 3 || !strings.Contains(msg, "127.0.0.1:1") || !strings.Contains(msg, "--relay") {
		t.Fatalf("exit %d (%s), want 3 naming relay 127.0.0.1:1 and --relay", code, msg)
	}
}

func stderrOf(t *testing.T, f func()) string {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = pw
	f()
	os.Stderr = stderr
	_ = pw.Close()
	b, _ := io.ReadAll(pr)
	return string(b)
}
