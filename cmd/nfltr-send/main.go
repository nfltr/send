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
	"net"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nfltr/relay/client/agent"
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
secret: whoever holds it can receive. With this machine's nfltr machine
certificate (the one ` + "`nfltr config login`" + ` left beside the nfltr config
file, renewed while serving, or --machine-credential) the relay carries a
transfer whose direct path does not form. With none the address is served
with no account: direct paths only, and a receiver with no direct path to
the sender exits 12.

receive reads the address from --address-file (default -, stdin; never
from the command line, where every user of the machine sees it; a file
others may read is refused, except on Windows, where the file is as
private as its directory's ACL) and writes
the files into --to (default .). A name that already exists there refuses
the transfer. Each chunk is verified against the file's BLAKE3 root the
sender offered as it arrives, and nothing lands until every file's BLAKE3
over what is on disk is that root; a cut transfer is resumed, by itself and by the
next receive of the same address, from what already arrived and verifies.

Exit status: 0 done, 1 usage, 2 refused by the other side, 3 failed,
12 no direct path to a sender serving with no account (relaying needs the
sender's account).

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
		relayHTTP: fs.String("relay-http", "", "relay HTTP `address` the address names; default: --relay without its grpc. prefix and port 443"),
		pin:       fs.String("relay-pin", os.Getenv(relaytrust.PinEnv), "accept the relay certificate with this SHA-256 `fingerprint` (a private relay's); $"+relaytrust.PinEnv),
		direct:    fs.String("direct", "on", "direct paths: `mode` on (LAN first, then the lowest round trip) or off (relayed only)"),
	}
}

func (c common) config() (nfnet.Config, nfnet.DirectMode, error) {
	mode, err := agent.ParseDirectMode(*c.direct)
	if err != nil {
		return nfnet.Config{}, nfnet.DirectOff, fmt.Errorf("--direct: %w", err)
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
	// The address names the relay as its nf1.hosts do: no port 443.
	http = strings.TrimSuffix(http, ":443")
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
	switch {
	case *machineFile == "" && errors.Is(err, errNoMachine): // serve with no account
		if mode == nfnet.DirectOff {
			return fail(1, errors.New("--direct off: with no machine certificate the address is served with no account, on direct paths only; run `nfltr config login` for the relay to carry it"))
		}
		m = nil
		fmt.Fprintln(os.Stderr, "no machine certificate: serving with no account, on direct paths only (a receiver with no direct path exits 12; after `nfltr config login` the relay carries those)")
	case err == nil:
		if err = m.forRelay(cfg.AgentRelay); err != nil {
			return fail(1, err)
		}
	default:
		return fail(1, err)
	}
	cfg.Logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return serve(ctx, cfg, mode, m, src, *once, os.Stdout)
}

// serve serves src under a fresh address until ctx ends (once: until the
// first complete transfer), writing the address to out. With m nil the
// address is served with no account (cfg holds no credential): direct
// paths only.
func serve(ctx context.Context, cfg nfnet.Config, mode nfnet.DirectMode, m *machine, src *xfer.Source, once bool, out io.Writer) int {
	anon := m == nil
	if !anon {
		cfg.ClientCert = m.cert
		go m.run(ctx, cfg.Logger)
	}
	var id [4]byte
	_, _ = rand.Read(id[:])
	cfg.Agent = "send-" + hex.EncodeToString(id[:])
	l, _, err := nf1.Mint(cfg.Relay)
	if err == nil && mode != nfnet.DirectOff {
		l, err = l.WithDirect()
	}
	if err != nil {
		return fail(3, err)
	}
	ln, err := nfnet.Listen(ctx, cfg, "send", nfnet.ListenOptions{Address: l, Direct: mode})
	if err != nil {
		return fail(3, enrolHint(err, anon))
	}
	defer ln.Close() //nolint:errcheck
	files, bytes := src.Files()
	fmt.Fprintf(os.Stderr, "serving %d %s, %d bytes; give the receiver this address (a secret: whoever holds it can receive):\n", files, filesWord(files), bytes)
	fmt.Fprintln(out, ln.Address())
	// A --once transfer ends the listener itself; done closes once that
	// Close returned: the process may exit only then. Close resets what is
	// still open and returns once each peer answered; a second Close (the
	// deferred one) returns at once, so a sender that exited on the
	// listener's first error left a receiver waiting for its end's
	// acknowledgement until its 30 s bound (TestOnceExitsAfterTheReceiversEnd).
	var ending atomic.Bool
	var end sync.Once
	done := make(chan struct{})
	sessions := newLimiter(maxSessions)
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
			if ending.Load() {
				<-done
				return 0
			}
			return fail(3, enrolHint(err, anon))
		}
		sessions.take(c, func(c net.Conn) {
			defer c.Close() //nolint:errcheck
			t0 := time.Now()
			a, sent, err := src.Serve(c)
			switch {
			case err != nil && anon && nfnet.CodeOf(err) == nfnet.CodeDirectRequired:
				slog.Warn("transfer ended: no direct path to the receiver; relaying it needs this machine's account (`nfltr config login`)", "sent", sent, "err", err)
			case err != nil:
				slog.Warn("transfer ended", "sent", sent, "err", err)
			case !a.OK:
				slog.Warn("receiver refused", "reason", a.Message)
			default:
				slog.Info("transfer done", "files", a.Files, "bytes", a.Bytes, "sent", sent, "seconds", time.Since(t0).Seconds())
				fmt.Fprintf(os.Stderr, "sent %d %s, %d bytes (%d this session) in %.1f s\n", a.Files, filesWord(a.Files), a.Bytes, sent, time.Since(t0).Seconds())
				if once {
					end.Do(func() {
						_ = c.Close() // this session's clean end first: the listener's Close resets what is open
						ending.Store(true)
						_ = ln.Close()
						close(done)
					})
				}
			}
		})
	}
}

// maxSessions bounds the sessions served at once: the address is a bearer
// capability, whoever holds it can open sessions. There is no idle bound: a
// session whose peer is gone ends when its connection does (the agent
// link's ping liveness, the direct path's QUIC idle timeout), and a live
// peer's long pause (a receiver reading a big file back) is legitimate.
const maxSessions = 16

// maxRefusing bounds the sessions refused with a reason at once.
const maxRefusing = 4

// limiter caps the sessions served at once, and the refusals: past the
// cap a session is refused with a reason while fewer than maxRefusing
// refusals are under way, else closed outright, so a flood costs a bounded
// number of goroutines.
type limiter struct{ slots, refusing chan struct{} }

func newLimiter(n int) limiter {
	return limiter{slots: make(chan struct{}, n), refusing: make(chan struct{}, maxRefusing)}
}

// take serves c with serve in a goroutine while a slot is free; with none
// it refuses c (xfer.Refuse: the receiver exits refused) and closes it.
func (l limiter) take(c net.Conn, serve func(net.Conn)) {
	select {
	case l.slots <- struct{}{}:
		go func() {
			defer func() { <-l.slots }()
			serve(c)
		}()
		return
	default:
	}
	select {
	case l.refusing <- struct{}{}:
		go func() {
			defer func() { <-l.refusing }()
			_ = c.SetDeadline(time.Now().Add(5 * time.Second))
			_ = xfer.Refuse(c, fmt.Sprintf("sender busy: %d sessions already; try again later", cap(l.slots)))
			_ = c.Close()
		}()
	default:
		_ = c.Close()
	}
}

func receiveCmd(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("nfltr-send receive", flag.ContinueOnError)
	cf := addCommon(fs)
	addrFile := fs.String("address-file", "-", "read the address from this `file` (mode 0600), or - for stdin")
	to := fs.String("to", ".", "receive into this `directory`")
	tries := fs.Int("tries", 10, "sessions to try before giving up; each resumes the last")
	asJSON := fs.Bool("json", false, "end with one JSON line on stderr: files, bytes, written (file bytes this receive wrote, resumes included), seconds, first_byte_s, sessions, paths (each path used, in order); on failure instead exit, code (usage, refused, failed, direct_required) and error")
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage); fs.PrintDefaults() }
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		if err == nil {
			fmt.Fprintln(os.Stderr, "receive: no arguments (the address comes from --address-file)")
		}
		return 1
	}
	fail := func(code int, err error) int { return failTo(os.Stderr, *asJSON, code, err) }
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
	return receiveTo(ctx, os.Stderr, cfg, t, mode, *to, *tries, *asJSON)
}

// receiveTo receives the offer at t into dir, resuming up to tries sessions.
func receiveTo(ctx context.Context, w io.Writer, cfg nfnet.Config, t nfnet.Target, mode nfnet.DirectMode, dir string, tries int, asJSON bool) (code int) {
	var mu sync.Mutex
	var path string
	var paths []string
	// onPath records each session's path, the one it opened on and every
	// move, in order (a reason with a relayed one when the SDK gives one):
	// a session that opens relayed and never moves sends no path event.
	onPath := func(p, reason string) {
		mu.Lock()
		defer mu.Unlock()
		e := strings.TrimSuffix(p+"/"+reason, "/")
		if n := len(paths); n > 0 && p == path {
			if reason != "" {
				paths[n-1] = e
			}
			return
		}
		path, paths = p, append(paths, e)
	}
	cfg.Events = func(e nfnet.Event) {
		if e.Kind == "path" {
			onPath(e.Path, e.Reason)
		}
	}
	fail := func(code int, err error) int { return failTo(w, asJSON, code, err) }
	d, err := nfnet.NewDialer(cfg)
	if err != nil {
		return fail(1, err)
	}
	// The dialer's close drains its direct connections until the sender
	// closes them, up to a second. After a transfer the session's close
	// (receiveOnce) returned once the sender acknowledged every byte, the
	// answer with them: the drain runs beside the exit.
	defer func() {
		if code == 0 {
			go d.Close() //nolint:errcheck
		} else {
			_ = d.Close()
		}
	}()
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
		a, err := receiveOnce(ctx, d, t, mode, dir, progress, onPath)
		switch {
		case err == nil && a.OK:
			el := time.Since(t0).Seconds()
			mu.Lock()
			fmt.Fprintf(w, "received %d %s, %d bytes in %.1f s (%.0f MB/s), path %s\n", a.Files, filesWord(a.Files), a.Bytes, el, float64(a.Bytes)/1e6/el, path)
			if asJSON {
				b, _ := json.Marshal(map[string]any{"files": a.Files, "bytes": a.Bytes, "written": written, "seconds": el,
					"first_byte_s": first.Seconds(), "sessions": try, "paths": paths})
				fmt.Fprintln(w, string(b))
			}
			mu.Unlock()
			return 0
		case err == nil || errors.Is(err, xfer.ErrRefused):
			return fail(2, fmt.Errorf("refused: %s", firstOf(a.Message, fmt.Sprint(err))))
		case ctx.Err() != nil:
			return fail(3, fmt.Errorf("stopped; receive the same address again to resume (%w)", err))
		case nfnet.CodeOf(err) == nfnet.CodeDirectRequired && errors.As(err, new(openError)):
			// a session that opened and lost its direct path resumes; one
			// that found none ends here
			return fail(12, fmt.Errorf("%w (the sender runs `nfltr config login`); receive the same address again to resume", err))
		case try >= tries || errors.Is(err, xfer.ErrProtocol) || errors.As(err, new(*tls.CertificateVerificationError)):
			return fail(3, fmt.Errorf("%w; receive the same address again to resume", err))
		}
		fmt.Fprintf(w, "session %d ended (%v); resuming\n", try, err)
		select {
		case <-ctx.Done():
		case <-time.After(time.Duration(min(try, 5)) * time.Second):
		}
	}
}

// openError is a session that did not open.
type openError struct{ error }

func (e openError) Unwrap() error { return e.error }

// openBound bounds a session's open: with direct paths past the SDK's
// direct-path bound (about 55 s), within which an address served with no
// account either forms its direct path or fails CodeDirectRequired.
func openBound(mode nfnet.DirectMode) time.Duration {
	if mode == nfnet.DirectOff {
		return 30 * time.Second
	}
	return 60 * time.Second
}

func receiveOnce(ctx context.Context, d *nfnet.Dialer, t nfnet.Target, mode nfnet.DirectMode, dir string, progress xfer.Progress, onPath func(path, reason string)) (xfer.Answer, error) {
	open, cancel := context.WithTimeout(ctx, openBound(mode))
	c, err := d.DialTarget(open, t, nfnet.DialOptions{Direct: mode})
	cancel()
	if err != nil {
		return xfer.Answer{}, openError{err}
	}
	onPath(c.Info().Path, "")
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
		// Windows reports every writable file as 0666 whatever its ACL
		// says: there the file is as private as its directory's ACL, and
		// the mode is not checked (as machinecert.LoadKey).
		if fi, serr := os.Stat(name); serr == nil && runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
			return "", fmt.Errorf("%s: readable by others (chmod 600 it): an address is a secret", name)
		}
		b, err = os.ReadFile(name) // #nosec G304 -- the file the user named
	}
	return strings.TrimSpace(string(b)), err
}

// enrolHint says what to do when the relay refused the certificate, or
// (anon) an address served with no account.
func enrolHint(err error, anon bool) error {
	code := nfnet.CodeOf(err)
	switch {
	case anon && code == nfnet.CodeAuth: // the relay refused with no credential it accepts (ERROR_REASON_UNAUTHENTICATED: auth since relay client v0.22.4)
		return fmt.Errorf("%w; this relay serves no address without an account: run `nfltr config login` to serve with this machine's certificate", err)
	case code == nfnet.CodeAuth:
		return fmt.Errorf("%w; run `nfltr config login` to enrol this machine again", err)
	}
	return err
}

func filesWord(n int) string {
	if n == 1 {
		return "file"
	}
	return "files"
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

func fail(code int, err error) int { return failTo(os.Stderr, false, code, err) }

// exitCodes names receive's exit statuses in its --json failure object.
var exitCodes = map[int]string{1: "usage", 2: "refused", 3: "failed", 12: "direct_required"}

// failTo says why to w once: as a line, or (asJSON) as one JSON object
// with the exit status, its code and the message.
func failTo(w io.Writer, asJSON bool, code int, err error) int {
	if !asJSON {
		fmt.Fprintln(w, "nfltr-send:", err)
		return code
	}
	b, _ := json.Marshal(map[string]any{"exit": code, "code": exitCodes[code], "error": err.Error()})
	fmt.Fprintln(w, string(b))
	return code
}
