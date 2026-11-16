// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

// Command nfltr-send sends files and directories between two machines,
// end to end encrypted, directly between them when their networks allow
// it and through an nfltr relay otherwise. The sender prints an address;
// whoever holds it receives.
package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nfltr/relay/client/nf1"
	"github.com/nfltr/relay/client/nfnet"
	"github.com/nfltr/relay/client/relaytrust"
	"github.com/nfltr/send/xfer"
)

const usage = `nfltr-send: send files between two machines, end to end encrypted

Usage:
  nfltr-send send [flags] <file|dir>...
  nfltr-send receive [flags]

send prints an address (nf1...) and serves the files under it until it is
stopped (--once: until the first complete transfer). The address is a
secret: whoever holds it can receive. Serving needs this machine's nfltr
machine certificate: the one ` + "`nfltr config login`" + ` left beside the nfltr
config file (renewed while serving), or --machine-credential.

receive reads the address from --address-file (default -, stdin; never
from the command line, where every user of the machine sees it) and writes
the files into --to (default .). A name that already exists there refuses
the transfer. Nothing lands until every file's SHA-256 over what is on
disk equals the sender's; a cut transfer is resumed, by itself and by the
next receive of the same address, from what already arrived.

Exit status: 0 done, 1 usage, 2 refused by the other side, 3 failed.

Flags:
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var code int
	switch os.Args[1] {
	case "send":
		code = sendCmd(ctx, os.Args[2:])
	case "receive":
		code = receiveCmd(ctx, os.Args[2:])
	case "-h", "--help", "help":
		fmt.Fprint(os.Stdout, usage)
	default:
		fmt.Fprintf(os.Stderr, "nfltr-send: unknown command %q\n\n%s", os.Args[1], usage)
		code = 1
	}
	os.Exit(code)
}

// common are both commands' relay flags.
type common struct {
	relay, relayHTTP, pin, direct *string
}

func addCommon(fs *flag.FlagSet) common {
	return common{
		relay:     fs.String("relay", env("NFLTR_SEND_RELAY", "grpc.nfltr.xyz:443"), "relay agent-link `address` (send); $NFLTR_SEND_RELAY"),
		relayHTTP: fs.String("relay-http", "", "relay HTTP `address` the address names; default: --relay without its grpc. prefix"),
		pin:       fs.String("relay-pin", os.Getenv(relaytrust.PinEnv), "accept the relay certificate with this SHA-256 `fingerprint` (a private relay's); $"+relaytrust.PinEnv),
		direct:    fs.String("direct", "on", "direct paths: `mode` on, public (no LAN addresses) or off (relayed only)"),
	}
}

func (c common) config() (nfnet.Config, nfnet.DirectMode, error) {
	mode, ok := map[string]nfnet.DirectMode{"on": nfnet.DirectOn, "public": nfnet.DirectPublic, "off": nfnet.DirectOff}[*c.direct]
	if !ok {
		return nfnet.Config{}, nfnet.DirectOff, fmt.Errorf("--direct %q: want on, public or off", *c.direct)
	}
	if *c.pin != "" {
		if _, err := relaytrust.ParsePin(*c.pin); err != nil {
			return nfnet.Config{}, nfnet.DirectOff, err
		}
	}
	http := *c.relayHTTP
	if http == "" {
		http = strings.TrimPrefix(*c.relay, "grpc.")
	}
	return nfnet.Config{Relay: http, AgentRelay: *c.relay, RelayTLS: relaytrust.WithPin(nil, *c.pin)}, mode, nil
}

func sendCmd(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("nfltr-send send", flag.ContinueOnError)
	cf := addCommon(fs)
	machineFile := fs.String("machine-credential", "", "this machine's certificate `file` (machine.json, machine.key beside it); default: the one nfltr config login saved")
	once := fs.Bool("once", false, "exit after the first complete transfer")
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage); fs.PrintDefaults() }
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() == 0 {
		return fail(1, errors.New("send: name the files and directories to send"))
	}
	src, err := xfer.NewSource(fs.Args())
	if err != nil {
		return fail(1, err)
	}
	cfg, mode, err := cf.config()
	if err != nil {
		return fail(1, err)
	}
	m, err := loadMachine(machinePath(*machineFile))
	if err == nil {
		err = m.forRelay(cfg.AgentRelay)
	}
	if err != nil {
		return fail(1, err)
	}
	cfg.ClientCert = m.cert
	var id [4]byte
	_, _ = rand.Read(id[:])
	cfg.Agent = "send-" + hex.EncodeToString(id[:])
	cfg.Logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	go m.run(ctx, cfg.Logger)
	l, _, err := nf1.Mint(cfg.Relay)
	if err == nil && mode != nfnet.DirectOff {
		l, err = l.WithDirect()
	}
	if err != nil {
		return fail(3, err)
	}
	ln, err := nfnet.Listen(ctx, cfg, "send", nfnet.ListenOptions{Address: l, Direct: mode})
	if err != nil {
		return fail(3, enrolHint(err))
	}
	defer ln.Close() //nolint:errcheck
	files, bytes := src.Files()
	fmt.Fprintf(os.Stderr, "serving %d files, %d bytes; give the receiver this address (a secret: whoever holds it can receive):\n", files, bytes)
	fmt.Println(ln.Address())
	done := make(chan struct{}, 1)
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return 0
			}
			select {
			case <-done:
				return 0
			default:
			}
			return fail(3, enrolHint(err))
		}
		go func() {
			defer c.Close() //nolint:errcheck
			t0 := time.Now()
			a, sent, err := src.Serve(c)
			switch {
			case err != nil:
				slog.Warn("transfer ended", "sent", sent, "err", err)
			case !a.OK:
				slog.Warn("receiver refused", "reason", a.Message)
			default:
				slog.Info("transfer done", "files", a.Files, "bytes", a.Bytes, "sent", sent, "seconds", time.Since(t0).Seconds())
				fmt.Fprintf(os.Stderr, "sent %d files, %d bytes (%d this session) in %.1f s\n", a.Files, a.Bytes, sent, time.Since(t0).Seconds())
				if *once {
					done <- struct{}{}
					_ = ln.Close()
				}
			}
		}()
	}
}

func receiveCmd(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("nfltr-send receive", flag.ContinueOnError)
	cf := addCommon(fs)
	addrFile := fs.String("address-file", "-", "read the address from this `file` (mode 0600), or - for stdin")
	to := fs.String("to", ".", "receive into this `directory`")
	tries := fs.Int("tries", 10, "sessions to try before giving up; each resumes the last")
	asJSON := fs.Bool("json", false, "end with one JSON line on stderr: files, bytes, written (file bytes this receive wrote, resumes included), seconds, first_byte_s, sessions, paths")
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage); fs.PrintDefaults() }
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		if err == nil {
			fmt.Fprintln(os.Stderr, "receive: no arguments (the address comes from --address-file)")
		}
		return 1
	}
	addr, err := readAddress(*addrFile)
	if err != nil {
		return fail(1, err)
	}
	t, err := nfnet.ParseTarget(addr)
	if err != nil || t.Kind != nfnet.AddressTarget {
		return fail(1, fmt.Errorf("--address-file: want an nf1... address (%v)", err))
	}
	cfg, mode, err := cf.config()
	if err != nil {
		return fail(1, err)
	}
	cfg.Relay = t.Address.Relay // --relay-pin verifies the address's relay
	var mu sync.Mutex
	path, paths := "relayed", []string{}
	cfg.Events = func(e nfnet.Event) {
		if e.Kind == "path" {
			mu.Lock()
			path = e.Path
			paths = append(paths, strings.TrimSuffix(e.Path+"/"+e.Reason, "/"))
			mu.Unlock()
		}
	}
	d, err := nfnet.NewDialer(cfg)
	if err != nil {
		return fail(1, err)
	}
	defer d.Close() //nolint:errcheck
	t0 := time.Now()
	var first time.Duration
	var written, base int64
	progress := func(n int64) {
		if first == 0 {
			first = time.Since(t0)
		}
		written = base + n
	}
	for try := 1; ; try++ {
		base = written
		a, err := receiveOnce(ctx, d, t, mode, *to, progress)
		switch {
		case err == nil && a.OK:
			el := time.Since(t0).Seconds()
			mu.Lock()
			fmt.Fprintf(os.Stderr, "received %d files, %d bytes in %.1f s (%.0f MB/s), path %s\n", a.Files, a.Bytes, el, float64(a.Bytes)/1e6/el, path)
			if *asJSON {
				b, _ := json.Marshal(map[string]any{"files": a.Files, "bytes": a.Bytes, "written": written, "seconds": el,
					"first_byte_s": first.Seconds(), "sessions": try, "paths": paths})
				fmt.Fprintln(os.Stderr, string(b))
			}
			mu.Unlock()
			return 0
		case err == nil || errors.Is(err, xfer.ErrRefused):
			return fail(2, fmt.Errorf("refused: %s", firstOf(a.Message, fmt.Sprint(err))))
		case ctx.Err() != nil:
			return fail(3, fmt.Errorf("stopped; receive the same address again to resume (%w)", err))
		case try >= *tries || errors.Is(err, xfer.ErrProtocol) || errors.As(err, new(*tls.CertificateVerificationError)):
			return fail(3, fmt.Errorf("%w; receive the same address again to resume", err))
		}
		fmt.Fprintf(os.Stderr, "session %d ended (%v); resuming\n", try, err)
		select {
		case <-ctx.Done():
		case <-time.After(time.Duration(min(try, 5)) * time.Second):
		}
	}
}

func receiveOnce(ctx context.Context, d *nfnet.Dialer, t nfnet.Target, mode nfnet.DirectMode, dir string, progress xfer.Progress) (xfer.Answer, error) {
	open, cancel := context.WithTimeout(ctx, 30*time.Second)
	c, err := d.DialTarget(open, t, nfnet.DialOptions{Direct: mode})
	cancel()
	if err != nil {
		return xfer.Answer{}, err
	}
	defer c.Close() //nolint:errcheck
	stop := context.AfterFunc(ctx, func() { _ = c.Reset() })
	defer stop()
	return xfer.Receive(c, dir, progress)
}

func readAddress(name string) (string, error) {
	var b []byte
	var err error
	if name == "-" {
		b, err = io.ReadAll(io.LimitReader(os.Stdin, 4096))
	} else {
		if fi, serr := os.Stat(name); serr == nil && fi.Mode().Perm()&0o077 != 0 {
			return "", fmt.Errorf("%s: readable by others (chmod 600 it): an address is a secret", name)
		}
		b, err = os.ReadFile(name) // #nosec G304 -- the file the user named
	}
	return strings.TrimSpace(string(b)), err
}

// enrolHint says what to do when the relay refused the certificate.
func enrolHint(err error) error {
	if nfnet.CodeOf(err) == nfnet.CodeAuth {
		return fmt.Errorf("%w; run `nfltr config login` to enrol this machine again", err)
	}
	return err
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func fail(code int, err error) int {
	fmt.Fprintln(os.Stderr, "nfltr-send:", err)
	return code
}
