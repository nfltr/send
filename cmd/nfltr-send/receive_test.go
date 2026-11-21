// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/nfltr/relay-core/relaycoretest"
	"github.com/nfltr/relay/client/nfnet"
	"github.com/nfltr/send/internal/xfer"
)

// receiveJSON is receiveFrom with --json, returning the exit status and
// what receive wrote to stderr.
func receiveJSON(t *testing.T, r *relaycoretest.Relay, addr string, mode nfnet.DirectMode, to string) (int, string) {
	tg, err := nfnet.ParseTarget(addr)
	if err != nil {
		t.Fatal(err)
	}
	cfg := nfnet.Config{Relay: tg.Address.Relay, Plaintext: true, DialRelay: func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", r.Proxy)
	}}
	var out bytes.Buffer
	code := receiveTo(t.Context(), &out, cfg, tg, mode, to, 3, 4, true)
	return code, out.String()
}

// jsonLines is every line of out that is a JSON object.
func jsonLines(t *testing.T, out string) []map[string]any {
	var objs []map[string]any
	for l := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(l, "{") {
			var m map[string]any
			if err := json.Unmarshal([]byte(l), &m); err != nil {
				t.Fatalf("%q: %v", l, err)
			}
			objs = append(objs, m)
		}
	}
	return objs
}

// A relayed transfer's --json lists the path it used, as the text line
// says, though no path event came (the session opened relayed and never
// moved; public check 2026-11-18: "paths":[] on 2 of 4 relayed runs).
func TestReceiveJSONListsTheRelayedPath(t *testing.T) {
	r := anonRelay(t, true)
	src, err := xfer.NewSource([]string{offer(t)})
	if err != nil {
		t.Fatal(err)
	}
	pr, pw := io.Pipe()
	stderr := os.Stderr // read here, not in the goroutine: stderrOf swaps it (-race)
	go func() {
		serve(t.Context(), nfnet.Config{Relay: "relay.test", AgentRelay: r.GRPC, Plaintext: true, Token: r.Key}, nfnet.DirectOn, nil, src, true, pw, stderr)
		_ = pw.Close()
	}()
	addr, err := readLine(pr)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, pr) }()
	code, out := receiveJSON(t, r, addr, nfnet.DirectOff, t.TempDir())
	objs := jsonLines(t, out)
	if code != 0 || len(objs) != 1 || !strings.Contains(out, "path relayed") {
		t.Fatalf("exit %d, output %q", code, out)
	}
	if p, _ := objs[0]["paths"].([]any); len(p) != 1 || p[0] != "relayed" {
		t.Fatalf("paths %v, want [relayed]", objs[0]["paths"])
	}
}

// A receive that fails says why once, and with --json ends with one JSON
// object: the exit status, its code and the message (public check
// 2026-11-18: exit 12 repeated its account sentence and --json printed
// nothing).
func TestReceiveFailureOnceAndJSON(t *testing.T) {
	r := anonRelay(t, true)
	addr, _ := serveAnon(t, r, offer(t))
	code, out := receiveJSON(t, r, addr, nfnet.DirectOff, t.TempDir())
	if code != 12 {
		t.Fatalf("exit %d, want 12; output %q", code, out)
	}
	objs := jsonLines(t, out)
	if len(objs) != 1 || strings.Count(out, "\n") != 1 {
		t.Fatalf("want one JSON object and nothing else: %q", out)
	}
	o := objs[0]
	msg, _ := o["message"].(string)
	if o["exit"] != float64(12) || o["code"] != "direct_required" || strings.Count(msg, "relaying needs") != 1 || !strings.Contains(msg, "nfltr config login") {
		t.Fatalf("JSON %v", o)
	}
	var text bytes.Buffer
	tg, _ := nfnet.ParseTarget(addr)
	cfg := nfnet.Config{Relay: tg.Address.Relay, Plaintext: true, DialRelay: func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", r.Proxy)
	}}
	if code := receiveTo(t.Context(), &text, cfg, tg, nfnet.DirectOff, t.TempDir(), 3, 4, false); code != 12 || strings.Count(text.String(), "relaying needs") != 1 || strings.Count(text.String(), "\n") != 1 {
		t.Fatalf("text: exit %d, %q: want the reason once, on one line", code, text.String())
	}
}

func readLine(r io.Reader) (string, error) {
	var b []byte
	c := make([]byte, 1)
	for {
		if _, err := r.Read(c); err != nil {
			return "", err
		}
		if c[0] == '\n' {
			return string(b), nil
		}
		b = append(b, c[0])
	}
}
