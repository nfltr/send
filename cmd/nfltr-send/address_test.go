// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// An address file group or others may read is refused, except on Windows,
// where the mode does not reflect the file's ACL (every writable file is
// 0666) and the file is as private as its directory (review 2026-11-17,
// F4).
func TestReadAddressMode(t *testing.T) {
	f := filepath.Join(t.TempDir(), "addr")
	if err := os.WriteFile(f, []byte("nf1example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readAddress(f)
	if runtime.GOOS == "windows" {
		if err != nil || got != "nf1example" {
			t.Fatalf("windows: %q, %v; want the address read", got, err)
		}
		return
	}
	if err == nil {
		t.Fatal("an address file others may read was read")
	}
	if err := os.Chmod(f, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := readAddress(f); err != nil || got != "nf1example" {
		t.Fatalf("mode 0600: %q, %v", got, err)
	}
}
