// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// nfltr-send builds on the public modules only: the relay client and its
// protocol, link, the quic-go fork and the blake3 fork; never nfltr's closed code. Its
// tests also run a relay core (relay-core's relaycoretest), which the
// binary never links.
func TestRequiresOnlyPublicModules(t *testing.T) {
	b, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	public := []string{"github.com/nfltr/send", "github.com/nfltr/relay/client ", "github.com/nfltr/relay/proto ", "github.com/nfltr/link ",
		"github.com/nfltr/link/peerlink/quiclink ", "github.com/nfltr/link/pathswitch ", "github.com/nfltr/quic-go ", "github.com/nfltr/blake3 ", "github.com/nfltr/relay-core "}
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

// No built binary is tracked (a go build in cmd/ once committed one):
// git ls-files' contents start with no ELF, Mach-O or PE magic.
func TestNoTrackedBinaries(t *testing.T) {
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = "../.."
	for _, e := range os.Environ() { // the checkout's own git, not an outer one's
		if !strings.HasPrefix(e, "GIT_") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("not a git checkout (a module download): %v", err)
	}
	magic := [][]byte{{0x7f, 'E', 'L', 'F'}, {0xcf, 0xfa, 0xed, 0xfe}, {0xce, 0xfa, 0xed, 0xfe}, {0xca, 0xfe, 0xba, 0xbe}, []byte("MZ")}
	for _, name := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		f, err := os.Open("../../" + name)
		if err != nil {
			continue // deleted in the working tree
		}
		head := make([]byte, 4)
		n, _ := f.Read(head)
		_ = f.Close()
		for _, m := range magic {
			if bytes.HasPrefix(head[:n], m) {
				t.Errorf("%s is a binary: git rm --cached it (.gitignore keeps built ones out)", name)
			}
		}
	}
}
