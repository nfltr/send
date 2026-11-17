// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// nfltr-send builds on the public modules only: the relay client and its
// protocol, link and the quic-go fork; never nfltr's closed code. Its
// tests also run a relay core (relay-core's relaycoretest), which the
// binary never links.
func TestRequiresOnlyPublicModules(t *testing.T) {
	b, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	public := []string{"github.com/nfltr/send", "github.com/nfltr/relay/client ", "github.com/nfltr/relay/proto ", "github.com/nfltr/link ",
		"github.com/nfltr/link/peerlink/quiclink ", "github.com/nfltr/quic-go ", "github.com/nfltr/relay-core "}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "require"))
		if !strings.HasPrefix(f, "github.com/nfltr/") {
			continue
		}
		ok := false
		for _, p := range public {
			ok = ok || strings.HasPrefix(f+" ", p)
		}
		if !ok {
			t.Errorf("go.mod requires %q: not one of the public modules", f)
		}
	}
}

// The binary links no relay-core package (the tests' relay only).
func TestBinaryLinksNoRelayCore(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range strings.Fields(string(out)) {
		if strings.HasPrefix(p, "github.com/nfltr/relay-core") {
			t.Errorf("nfltr-send links %s", p)
		}
	}
}
