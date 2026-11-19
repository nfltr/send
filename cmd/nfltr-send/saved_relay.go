// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// defaultRelay is the relay used when neither --relay, its environment
// variable nor the nfltr config names one.
const defaultRelay = "grpc.nfltr.xyz:443"

// nfltrConfigPath is the nfltr config file: $NFLTR_CONFIG, else
// ~/.config/nfltr/nfltr.json.
func nfltrConfigPath() string {
	if cfg := os.Getenv("NFLTR_CONFIG"); cfg != "" {
		return cfg
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "nfltr", "nfltr.json")
	}
	return "nfltr.json"
}

// savedRelay is the relay `nfltr config set-relay` saved in the nfltr
// config: its gRPC address, HTTPS URL and certificate pin. Only these
// fields are read; nothing of an account.
type savedRelay struct {
	Server   string `json:"server"`
	ProxyURL string `json:"proxy_url"`
	Pin      string `json:"relay_cert_sha256"`
}

// loadSavedRelay is the saved relay; zero when none is saved or the file
// does not parse.
func loadSavedRelay() savedRelay {
	var s savedRelay
	b, err := os.ReadFile(nfltrConfigPath()) // #nosec G304 -- this machine's nfltr config
	if err != nil || json.Unmarshal(b, &s) != nil {
		return savedRelay{}
	}
	s.Server, s.Pin = strings.TrimSpace(s.Server), strings.TrimSpace(s.Pin)
	return s
}

// httpHost is the saved HTTPS URL's host:port, "" for none.
func (s savedRelay) httpHost() string {
	u, err := url.Parse(strings.TrimSpace(s.ProxyURL))
	if err != nil {
		return ""
	}
	return u.Host
}
