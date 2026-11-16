// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nfltr/relay/client/machinecert"
)

// enrol writes machine.json and machine.key into dir as `nfltr config
// login` saves them, for relay host; it returns the key's certificate.
func enrol(t *testing.T, dir, host string) *machinecert.Cert {
	t.Helper()
	key, err := machinecert.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := machinecert.SaveKey(filepath.Join(dir, "machine.key"), key); err != nil {
		t.Fatal(err)
	}
	c := certFor(t, key.Public())
	c.MachineID, c.Name, c.Group = "m1", "laptop", "default"
	b, _ := json.Marshal(savedCert{Cert: *c, RelayHost: host, BaseURL: "https://" + host})
	if err := os.WriteFile(filepath.Join(dir, "machine.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return c
}

func certFor(t *testing.T, pub any) *machinecert.Cert {
	t.Helper()
	ca, _ := machinecert.NewKey()
	nb := time.Now().Add(-time.Hour)
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: nb, NotAfter: nb.Add(24 * time.Hour)},
		&x509.Certificate{SerialNumber: big.NewInt(1)}, pub, ca)
	if err != nil {
		t.Fatal(err)
	}
	c, err := machinecert.ParseCert(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// skipKeyFileOnWindows: machinecert.LoadKey refuses a key file group or
// others may read, and Windows reports every writable file as 0666.
func skipKeyFileOnWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("relay client machinecert.LoadKey's owner-only check reads Windows file modes as 0666")
	}
}

// The default credential is the one `nfltr config login` left beside the
// nfltr config file ($NFLTR_CONFIG, else ~/.config/nfltr/nfltr.json).
func TestMachinePathDefaultsToNfltrConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NFLTR_CONFIG", filepath.Join(dir, "nfltr.json"))
	if got, want := machinePath(""), filepath.Join(dir, "machine.json"); got != want {
		t.Fatalf("machinePath = %q, want %q", got, want)
	}
	if got := machinePath("/x/m.json"); got != "/x/m.json" {
		t.Fatalf("--machine-credential not taken: %q", got)
	}
	t.Setenv("NFLTR_CONFIG", "")
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	if got, want := machinePath(""), filepath.Join(dir, ".config", "nfltr", "machine.json"); got != want {
		t.Fatalf("machinePath = %q, want %q", got, want)
	}
}

// No machine certificate: a typed error naming `nfltr config login`,
// whatever API key the environment holds (there is no API key path).
func TestNoMachineCredential(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NFLTR_CONFIG", filepath.Join(dir, "nfltr.json"))
	t.Setenv("NFLTR_API_KEY", "k")
	t.Setenv("NFLTR_SEND_RELAY", "127.0.0.1:1") // never a real relay
	_, err := loadMachine(machinePath(""))
	if !errors.Is(err, errNoMachine) || !strings.Contains(err.Error(), "nfltr config login") {
		t.Fatalf("loadMachine = %v, want errNoMachine naming nfltr config login", err)
	}
	f := filepath.Join(dir, "f")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if code := sendCmd(ctx, []string{f}); code != 1 {
		t.Fatalf("send without a machine certificate exited %d, want 1", code)
	}
	if code := sendCmd(ctx, []string{"--api-key-file", f, f}); code != 1 {
		t.Fatalf("send --api-key-file exited %d, want 1 (no such flag)", code)
	}
}

// The certificate is presented as the TLS client certificate, and a
// renewal is kept in machine.json (0600) and presented from then on.
func TestMachineCredentialPresentsAndKeepsRenewals(t *testing.T) {
	skipKeyFileOnWindows(t)
	dir := t.TempDir()
	c := enrol(t, dir, "grpc.relay.example")
	m, err := loadMachine(filepath.Join(dir, "machine.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := m.cert(); got == nil || !got.Leaf.Equal(c.Leaf) {
		t.Fatal("the loaded certificate is not presented")
	}
	next := certFor(t, m.key.Public())
	next.MachineID = "m1"
	if err := m.save(next); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.cert(); !got.Leaf.Equal(next.Leaf) {
		t.Fatal("the renewed certificate is not presented")
	}
	again, err := loadMachine(filepath.Join(dir, "machine.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !again.saved.Leaf.Equal(next.Leaf) || again.saved.RelayHost != "grpc.relay.example" || again.saved.BaseURL != "https://grpc.relay.example" {
		t.Fatalf("machine.json after renewal: %+v", again.saved)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "machine.json")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("machine.json mode %o, want 0600", fi.Mode().Perm())
	}
}

// A certificate is for the relay it was issued by: serving on another is
// refused before connecting, naming both.
func TestMachineCredentialIsForItsRelay(t *testing.T) {
	skipKeyFileOnWindows(t)
	dir := t.TempDir()
	enrol(t, dir, "grpc.relay.example")
	m, err := loadMachine(filepath.Join(dir, "machine.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, ok := range []string{"grpc.relay.example:443", "relay.example:443", "relay.example"} {
		if err := m.forRelay(ok); err != nil {
			t.Errorf("relay %s: %v", ok, err)
		}
	}
	if err := m.forRelay("grpc.other.example:443"); err == nil || !strings.Contains(err.Error(), "relay.example") {
		t.Errorf("another relay: %v, want refused naming the certificate's relay", err)
	}
}
