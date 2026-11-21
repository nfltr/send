// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nfltr/relay/client/machinecert"
	"github.com/nfltr/relay/client/nfnet"
)

// expiredMachine is a machine whose certificate expired a day ago (it is
// machine was off), its relay's reissue route answering status (200: a
// new certificate). It returns the machine and the renewals asked.
func expiredMachine(t *testing.T, status *atomic.Int32) (*machine, *atomic.Int32) {
	t.Helper()
	skipKeyFileOnWindows(t)
	key, err := machinecert.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	leaf := func(nb time.Time) string {
		der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(nb.UnixNano()), NotBefore: nb, NotAfter: nb.Add(24 * time.Hour)},
			&x509.Certificate{SerialNumber: big.NewInt(1)}, key.Public(), key)
		if err != nil {
			t.Fatal(err)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	}
	var renewals atomic.Int32
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		renewals.Add(1)
		if s := int(status.Load()); s != http.StatusOK {
			http.Error(w, "no", s)
			return
		}
		status.Store(http.StatusConflict) // a second renewal is too early
		now := time.Now()
		_ = json.NewEncoder(w).Encode(map[string]any{"machine_id": "m1", "chain": leaf(now), "not_after": now.Add(24 * time.Hour)})
	}))
	t.Cleanup(relay.Close)
	dir := t.TempDir()
	if err := machinecert.SaveKey(filepath.Join(dir, "machine.key"), key); err != nil {
		t.Fatal(err)
	}
	c, err := machinecert.ParseCert([]byte(leaf(time.Now().Add(-48 * time.Hour))))
	if err != nil {
		t.Fatal(err)
	}
	c.MachineID = "m1"
	b, _ := json.Marshal(savedCert{Cert: *c, RelayHost: "127.0.0.1", BaseURL: relay.URL})
	if err := os.WriteFile(filepath.Join(dir, "machine.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := loadMachine(filepath.Join(dir, "machine.json"))
	if err != nil {
		t.Fatal(err)
	}
	return m, &renewals
}

// A machine off past its certificate's life renews it before the sender.s
// first connection presents it, once however many connections ask (lane
// CERTGAPS, from CERTPROOF2: presented expired, the relay refused the
// sender for good and it exited 6). As relay client machinecred does.
func TestExpiredCertificateRenewsBeforeTheFirstConnection(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusOK)
	m, renewals := expiredMachine(t, &status)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if c, err := m.cert(); err != nil || !time.Now().Before(c.Leaf.NotAfter) {
				t.Errorf("first connection's certificate: %v", err)
			}
		})
	}
	wg.Wait()
	if n := renewals.Load(); n != 1 {
		t.Fatalf("%d renewals, want 1", n)
	}
}

// While the relay does not answer the renewal the connection fails (it is
// redialed); after a final refusal (revoked, offline past the
// grace) the expired certificate is presented and the relay's refusal is
// the command's.
func TestExpiredCertificateWhileTheRelayRefusesOrIsDown(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusServiceUnavailable)
	m, _ := expiredMachine(t, &status)
	if _, err := m.cert(); err == nil {
		t.Fatal("the relay is down: the expired certificate was presented")
	}
	status.Store(http.StatusUnauthorized)
	if c, err := m.cert(); err != nil || c == nil {
		t.Fatalf("after a final refusal: %v, want the expired certificate", err)
	}
}

// The relay refusing this machine is not a relay to name with --relay: the
// hint says only to enrol again (lane CERTGAPS, from CERTPROOF2: it said
// "--relay names another" before the refusal).
func TestRefusedMachineIsNotAnotherRelay(t *testing.T) {
	auth := &nfnet.Error{Code: nfnet.CodeAuth, Message: "refused"}
	if got := relayHint(enrolHint(auth, false), "relay.example:443").Error(); strings.Contains(got, "--relay") || !strings.Contains(got, "nfltr config login") {
		t.Errorf("a refused machine: %q", got)
	}
	if got := relayHint(&nfnet.Error{Code: nfnet.CodeUnreachable, Message: "timeout"}, "relay.example:443").Error(); !strings.Contains(got, "--relay names another") {
		t.Errorf("an unreachable relay lost its hint: %q", got)
	}
}
