// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nfltr/relay/client/machinecert"
)

// machine is this machine's account machine certificate as `nfltr config
// login` saves it: machine.json (the relay's answer, the relay's host and
// its HTTP base for renewals) and machine.key beside it (owner only). The
// sender presents it to the relay as its TLS client certificate and renews
// it while it serves (relay client machinecert.Loop).
type machine struct {
	path    string
	pin     string // the relay's pinned certificate (--cert-sha256), for renewals
	key     *ecdsa.PrivateKey
	mu      sync.Mutex // one renewal at a time (run's, a connection's); holds saved
	saved   savedCert
	cur     atomic.Pointer[tls.Certificate]
	refused atomic.Bool // a renewal was refused for good (revoked, offline past the grace)
}

// savedCert is machine.json, written back whole on each renewal.
type savedCert struct {
	machinecert.Cert
	RelayHost string `json:"relay_host"`
	BaseURL   string `json:"base_url"`
}

var errNoMachine = errors.New("this machine holds no machine certificate: run `nfltr config login` (or name one with --machine-credential)")

// machinePath is name, else machine.json beside the nfltr config file
// ($NFLTR_CONFIG, else ~/.config/nfltr/nfltr.json), where `nfltr config
// login` keeps it.
func machinePath(name string) string {
	if name != "" {
		return name
	}
	return filepath.Join(filepath.Dir(nfltrConfigPath()), "machine.json")
}

// loadMachine reads path and machine.key beside it.
func loadMachine(path string) (*machine, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- this machine's certificate file
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", path, errNoMachine)
	}
	if err != nil {
		return nil, err
	}
	m := &machine{path: path}
	if err := json.Unmarshal(b, &m.saved); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c, err := machinecert.ParseCert([]byte(m.saved.Chain))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	m.saved.Leaf, m.saved.NotAfter = c.Leaf, c.NotAfter
	if m.key, err = machinecert.LoadKey(filepath.Join(filepath.Dir(path), "machine.key")); err != nil {
		return nil, err
	}
	t := m.saved.TLS(m.key)
	m.cur.Store(&t)
	return m, nil
}

// cert is the newest certificate (nfnet.Config.ClientCert). An expired
// one (the machine was off) is renewed before a connection presents it:
// presented expired, the relay refuses the sender for good (lane
// CERTGAPS, from CERTPROOF2; relay client machinecred does the same).
// While the relay does not answer the connection fails and is redialed;
// after a final refusal the expired one is presented, and the relay's
// refusal ends the command.
func (m *machine) cert() (*tls.Certificate, error) {
	t := m.cur.Load()
	if m.refused.Load() || time.Now().Before(t.Leaf.NotAfter) {
		return t, nil
	}
	if _, err := m.renew(context.Background(), nil); err != nil && !m.refused.Load() {
		return nil, fmt.Errorf("the machine certificate expired and is renewing (the relay does not answer yet): %w", err)
	}
	return m.cur.Load(), nil
}

// forRelay refuses a relay other than the certificate's (a hosted relay's
// gRPC host is its HTTP host with "grpc." in front).
func (m *machine) forRelay(addr string) error {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	if strings.TrimPrefix(host, "grpc.") != strings.TrimPrefix(m.saved.RelayHost, "grpc.") {
		return fmt.Errorf("%s: the machine certificate is for relay %s, not %s: name its relay with --relay (default: the relay nfltr config set-relay saved, else %s)", m.path, m.saved.RelayHost, addr, defaultRelay)
	}
	return nil
}

// run renews the certificate until ctx ends; a final refusal or an expiry
// is logged and the relay then refuses the certificate (enrol again).
func (m *machine) run(ctx context.Context, log *slog.Logger) {
	m.mu.Lock()
	cur := m.saved.Cert
	m.mu.Unlock()
	err := machinecert.Loop{
		Renew: m.renew,
		Save:  func(*machinecert.Cert) error { return nil }, // renew saved it
	}.Run(ctx, &cur)
	if err != nil {
		log.Error("machine certificate not renewed: run `nfltr config login` to enrol this machine again", "error", err)
	}
}

// renew renews the certificate once, under mu, and saves it; from is the
// certificate the caller holds (nil: a connection's, which needs only an
// unexpired one): a renewal that went through meanwhile is the answer,
// never a second request (the relay answers 409 renew_too_early). A final
// refusal marks the certificate refused.
func (m *machine) renew(ctx context.Context, from *machinecert.Cert) (*machinecert.Cert, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if (from != nil && m.saved.NotAfter.After(from.NotAfter)) || (from == nil && time.Now().Before(m.saved.NotAfter)) {
		saved := m.saved.Cert
		return &saved, nil
	}
	cur := m.saved.Cert
	next, err := m.client().Renew(ctx, &cur, m.key)
	if err != nil {
		var refused *machinecert.RefusedError
		if errors.As(err, &refused) && refused.Final() {
			m.refused.Store(true)
		}
		return nil, err
	}
	return next, m.save(next)
}

// client renews at the certificate's relay, trusting its pinned
// certificate as the sessions do: --cert-sha256, else
// $NFLTR_RELAY_CERT_SHA256 (relay client machinecert.Client.Pin; a relay
// of one's own with a self-signed certificate refused Go's default
// transport, and the renewal never went through).
func (m *machine) client() *machinecert.Client {
	return &machinecert.Client{BaseURL: m.saved.BaseURL, Pin: m.pin}
}

// save (under mu, or before the machine is shared) keeps a renewed
// certificate in machine.json (replaced whole, 0600) and presents it from
// the next handshake on.
func (m *machine) save(c *machinecert.Cert) error {
	s := m.saved
	s.Cert = *c
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(m.path), ".machine-*.json")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, err = tmp.Write(append(b, '\n'))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), m.path); err != nil {
		return err
	}
	m.saved = s
	t := c.TLS(m.key)
	m.cur.Store(&t)
	return nil
}
