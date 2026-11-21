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
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nfltr/relay/client/agent"
	"github.com/nfltr/relay/client/nf1"
	"github.com/nfltr/relay/client/nfnet"
	"github.com/nfltr/relay/client/relaytrust"
	"github.com/nfltr/send/internal/xfer"
)

const usage = `nfltr-send: send files between two machines, end to end encrypted

Usage:
  nfltr-send send [flags] <file|dir>...
  nfltr-send receive [flags]
  nfltr-send version

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

receive takes no --relay: the address names its relay.

Exit status (the relay SDK's table, shared with nfltr-pipe):
  0  done (send: --once after its transfer)
  1  usage: bad flags, address or configuration
  2  auth: the relay refused this machine's certificate
  3  not_found: nothing serves at the address
  4  refused: the other end or the relay refused the transfer
  5  unreachable: the relay or the sender was not reached in time
  6  reset: the session was cut, every try (or this receive was stopped)
  7  e2ee_pin_mismatch: the sender is not the one the address names
  9  balance_exhausted: the serving account's balance is exhausted
  10 e2ee_client_refused: the sender refused this receive's proof
  12 direct_required: no direct path to a sender serving with no account
     (relaying needs the sender's account)
  129 send stopped by SIGHUP, 130 by SIGINT, 143 by SIGTERM

Flags:
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}
	ctx, stop := stopContext()
	defer stop()
	var code int
	switch os.Args[1] {
	case "send":
		code = sendCmd(ctx, os.Args[2:])
	case "receive":
		code = receiveCmd(ctx, os.Args[2:])
	case "-h", "--help", "help":
		fmt.Fprint(os.Stdout, usage)
	case "version", "--version":
		fmt.Fprintln(os.Stdout, versionLine())
	default:
		fmt.Fprintf(os.Stderr, "nfltr-send: unknown command %q\n\n%s", os.Args[1], usage)
		code = 1
	}
	os.Exit(code)
}

// versionLine is the release (the module version the go command stamped
// from the tag), its commit and the Go version that built it.
func versionLine() string {
	v, commit := "(devel)", "unknown"
	if bi, ok := debug.ReadBuildInfo(); ok {
		v = firstOf(bi.Main.Version, v)
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" {
				commit = s.Value
			}
		}
	}
	return fmt.Sprintf("nfltr-send %s %s %s", v, commit, runtime.Version())
}

// stopped is the cause of a context a signal ended.
type stopped struct{ sig syscall.Signal }

func (s stopped) Error() string { return "stopped: " + s.sig.String() }

// stopContext ends at SIGINT, SIGTERM or SIGHUP (a closed terminal, ssh
// ending its session) with the signal as its cause; a second signal has
// its default effect.
func stopContext() (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(context.Background())
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		if s, ok := <-sigc; ok {
			signal.Stop(sigc)
			cancel(stopped{s.(syscall.Signal)})
		}
	}()
	return ctx, func() { signal.Stop(sigc); cancel(nil) }
}

// stopCode is the exit of a sender a signal stopped (ctx's cause): 128
// plus the signal's number, 129 for SIGHUP, 130 for SIGINT, 143 for
// SIGTERM; 0 for none.
func stopCode(ctx context.Context) int {
	if s, ok := errors.AsType[stopped](context.Cause(ctx)); ok {
		return 128 + int(s.sig)
	}
	return 0
}

// common are the relay flags: --relay and --relay-http send's only (the
// address names its relay).
type common struct {
	relay, relayHTTP, pin, direct *string
	saved                         savedRelay
}

// addCommon adds the relay flags (serving: send's). The relay is --relay,
// else $NFLTR_SEND_RELAY, else the one `nfltr config set-relay` saved
// (with its HTTPS URL and certificate pin), else grpc.nfltr.xyz:443.
func addCommon(fs *flag.FlagSet, serving bool) common {
	saved := loadSavedRelay()
	c := common{
		pin:    fs.String("cert-sha256", env(relaytrust.PinEnv, saved.Pin), "accept the relay certificate with this SHA-256 `fingerprint` (a private relay's); $"+relaytrust.PinEnv+", else the saved relay's"),
		direct: fs.String("direct", "on", "direct paths: `mode` on (LAN first, then the lowest round trip) or off (relayed only)"),
		saved:  saved,
	}
	if serving {
		c.relay = fs.String("relay", env("NFLTR_SEND_RELAY", firstOf(saved.Server, defaultRelay)), "relay agent-link `address`; $NFLTR_SEND_RELAY, else the relay nfltr config set-relay saved")
		c.relayHTTP = fs.String("relay-http", "", "relay HTTP `address` the address names; default: the saved relay's --proxy-url host with the saved relay, else --relay without its grpc. prefix and port 443")
	}
	return c
}

// parse parses args into fs quietly: -h prints the usage and fs's flags
// on stderr (help), and an error is the caller's to say: one line, a
// relay flag receive does not take named as such.
func parse(fs *flag.FlagSet, args []string) (help bool, err error) {
	fs.SetOutput(io.Discard)
	err = parseInterspersed(fs, args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprint(os.Stderr, usage)
		fs.SetOutput(os.Stderr)
		fs.PrintDefaults()
		return true, nil
	case err == nil:
		return false, nil
	}
	for _, f := range []string{"relay", "relay-http"} {
		if err.Error() == "flag provided but not defined: -"+f {
			return false, fmt.Errorf("receive takes no --%s: the address names its relay", f)
		}
	}
	return false, fmt.Errorf("%w (%s -h lists the flags)", err, fs.Name())
}

// parseInterspersed parses args into fs with flags before, between or
// after the arguments (as nfltr's commands take them); every word after
// the first -- is an argument. fs.Args() is the arguments, in order.
func parseInterspersed(fs *flag.FlagSet, args []string) error {
	var tail []string
	if i := slices.Index(args, "--"); i >= 0 {
		args, tail = args[:i], args[i+1:]
	}
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		pos, args = append(pos, fs.Arg(0)), fs.Args()[1:]
	}
	return fs.Parse(append(append([]string{"--"}, pos...), tail...))
}

func (c common) config() (nfnet.Config, nfnet.DirectMode, error) {
	if *c.direct != "on" && *c.direct != "off" {
		return nfnet.Config{}, nfnet.DirectOff, fmt.Errorf("--direct %q: want on or off", *c.direct)
	}
	mode, err := agent.ParseDirectMode(*c.direct)
	if err != nil {
		return nfnet.Config{}, nfnet.DirectOff, fmt.Errorf("--direct: %w", err)
	}
	if *c.pin != "" {
		if _, err := relaytrust.ParsePin(*c.pin); err != nil {
			return nfnet.Config{}, nfnet.DirectOff, fmt.Errorf("--cert-sha256: %w", err)
		}
	}
	if c.relay == nil { // receive: the address names the relay; this machine's own (the saved one) serves its direct paths' discovery
		return nfnet.Config{AgentRelay: firstOf(c.saved.Server, defaultRelay), RelayTLS: relaytrust.WithPin(nil, *c.pin)}, mode, nil
	}
	http := *c.relayHTTP
	if http == "" && *c.relay == c.saved.Server {
		http = c.saved.httpHost()
	}
	if http == "" {
		http = strings.TrimPrefix(*c.relay, "grpc.")
	}
	// The address names the relay as its nf1.hosts do: no port 443.
	http = strings.TrimSuffix(http, ":443")
	return nfnet.Config{Relay: http, AgentRelay: *c.relay, RelayTLS: relaytrust.WithPin(nil, *c.pin)}, mode, nil
}

func sendCmd(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("nfltr-send send", flag.ContinueOnError)
	cf := addCommon(fs, true)
	machineFile := fs.String("machine-credential", "", "this machine's certificate `file` (machine.json, machine.key beside it); default: the one nfltr config login saved")
	once := fs.Bool("once", false, "exit after the first complete transfer")
	if help, err := parse(fs, args); help || err != nil {
		if help {
			return 0
		}
		return fail(1, err)
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
		m.pin = *cf.pin
		if err = m.forRelay(cfg.AgentRelay); err != nil {
			return fail(1, err)
		}
	default:
		return fail(1, err)
	}
	cfg.Logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return serve(ctx, cfg, mode, m, src, *once, os.Stdout, os.Stderr)
}

// serve serves src under a fresh address until ctx ends (once: until the
// first complete transfer), writing the address to out and its messages to
// errw (a parameter, not os.Stderr: its session goroutines outlive a test
// that swaps os.Stderr, -race). With m nil the
// address is served with no account (cfg holds no credential): direct
// paths only.
func serve(ctx context.Context, cfg nfnet.Config, mode nfnet.DirectMode, m *machine, src *xfer.Source, once bool, out, errw io.Writer) int {
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
		return failTo(errw, false, 1, err)
	}
	ln, err := nfnet.Listen(ctx, cfg, "send", nfnet.ListenOptions{Address: l, Direct: mode})
	if err != nil {
		if code := stopCode(ctx); code != 0 {
			return code
		}
		return failTo(errw, false, codeOr(err, nfnet.CodeUnreachable), relayHint(enrolHint(err, anon), cfg.AgentRelay))
	}
	defer ln.Close() //nolint:errcheck
	files, bytes := src.Files()
	fmt.Fprintf(errw, "serving %d %s, %d bytes; give the receiver this address (a secret: whoever holds it can receive):\n", files, filesWord(files), bytes)
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
			if ending.Load() {
				<-done
				return 0
			}
			if ctx.Err() != nil {
				return stopCode(ctx)
			}
			return failTo(errw, false, codeOr(err, nfnet.CodeReset), relayHint(enrolHint(err, anon), cfg.AgentRelay))
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
				fmt.Fprintf(errw, "sent %d %s, %d bytes (%d this session) in %.1f s\n", a.Files, filesWord(a.Files), a.Bytes, sent, time.Since(t0).Seconds())
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
	cf := addCommon(fs, false)
	addrFile := fs.String("address-file", "-", "read the address from this `file` (mode 0600), or - for stdin")
	to := fs.String("to", ".", "receive into this `directory`")
	tries := fs.Int("tries", 10, "sessions to try before giving up; each resumes the last, or after a commit whose session was cut tells the sender again")
	asJSON := fs.Bool("json", false, "end with one JSON line on stderr: files, bytes, written (file bytes this receive wrote, resumes included), seconds, first_byte_seconds, sessions, paths (each path used, in order); on failure instead exit, code (the exit status's name) and message")
	help, err := parse(fs, args)
	if help {
		return 0
	}
	if err == nil && fs.NArg() != 0 {
		err = errors.New("receive: no arguments (the address comes from --address-file)")
	}
	if err != nil {
		return failTo(os.Stderr, *asJSON, 1, err)
	}
	fail := func(code int, err error) int { return failTo(os.Stderr, *asJSON, code, err) }
	cfg, mode, err := cf.config()
	if err != nil {
		return fail(1, err)
	}
	addr, err := readAddress(*addrFile)
	if err != nil {
		return fail(1, err)
	}
	t, err := nfnet.ParseTarget(addr)
	if err != nil || t.Kind != nfnet.AddressTarget {
		return fail(1, fmt.Errorf("--address-file: want an nf1... address (%v)", err))
	}
	cfg.Relay = t.Address.Relay // --cert-sha256 verifies the address's relay
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
	// told is the answer of a commit whose session was cut before the
	// sender confirmed it: the next sessions tell it again (spec End.tla
	// Retell; a --once sender serves until it reads an answer). A commit
	// stands whether or not they do (ExitRight).
	var told *xfer.Answer
	for try := 1; ; try++ {
		base = written
		var a xfer.Answer
		var err error
		if told == nil {
			a, err = receiveOnce(ctx, d, t, mode, dir, progress, onPath)
			if errors.As(err, new(unconfirmed)) {
				told = &a
			}
		} else {
			a, err = *told, retellOnce(ctx, d, t, mode, *told)
		}
		if told != nil && err != nil {
			if try < tries && ctx.Err() == nil {
				fmt.Fprintf(w, "session %d ended after the commit (%v); telling the sender again\n", try, err)
				wait(ctx, try)
				continue
			}
			fmt.Fprintf(w, "the sender did not confirm the end (%v); a --once sender may serve on\n", err)
			err = nil
		}
		switch {
		case err == nil && a.OK:
			el := time.Since(t0).Seconds()
			mu.Lock()
			fmt.Fprintf(w, "received %d %s, %d bytes in %.1f s (%.0f MB/s), path %s\n", a.Files, filesWord(a.Files), a.Bytes, el, float64(a.Bytes)/1e6/el, path)
			if asJSON {
				b, _ := json.Marshal(map[string]any{"files": a.Files, "bytes": a.Bytes, "written": written, "seconds": el,
					"first_byte_seconds": first.Seconds(), "sessions": try, "paths": paths})
				fmt.Fprintln(w, string(b))
			}
			mu.Unlock()
			return 0
		case err == nil || errors.Is(err, xfer.ErrRefused):
			return fail(int(nfnet.CodeRefused), fmt.Errorf("refused: %s", firstOf(a.Message, fmt.Sprint(err))))
		case ctx.Err() != nil:
			return fail(int(nfnet.CodeReset), fmt.Errorf("stopped; receive the same address again to resume (%w)", err))
		case nfnet.CodeOf(err) == nfnet.CodeDirectRequired && errors.As(err, new(openError)):
			// a session that opened and lost its direct path resumes; one
			// that found none ends here
			return fail(int(nfnet.CodeDirectRequired), fmt.Errorf("%w (the sender runs `nfltr config login`); receive the same address again to resume", err))
		case try >= tries || final(err):
			def := nfnet.CodeReset
			if errors.As(err, new(openError)) {
				def = nfnet.CodeUnreachable
			}
			return fail(codeOr(err, def), fmt.Errorf("%w; receive the same address again to resume", err))
		}
		fmt.Fprintf(w, "session %d ended (%v); resuming\n", try, err)
		wait(ctx, try)
	}
}

// final: err is no reason to try another session (a protocol error, the
// relay's certificate refused, or a code another session would only
// repeat).
func final(err error) bool {
	switch nfnet.CodeOf(err) {
	case nfnet.CodeUsage, nfnet.CodeAuth, nfnet.CodeRefused, nfnet.CodeE2EEPinMismatch, nfnet.CodeBalanceExhausted, nfnet.CodeE2EEClientRefused:
		return true
	}
	return errors.Is(err, xfer.ErrProtocol) || errors.As(err, new(*tls.CertificateVerificationError))
}

// codeOr is err's exit status on nfnet's table, def where it types none.
func codeOr(err error, def nfnet.Code) int {
	if code := nfnet.CodeOf(err); code != 0 {
		return int(code)
	}
	return int(def)
}

// wait is the pause before session try+1.
func wait(ctx context.Context, try int) {
	select {
	case <-ctx.Done():
	case <-time.After(time.Duration(min(try, 5)) * time.Second):
	}
}

// unconfirmed is a committed receive whose session was cut before the
// sender's clean end: the sender may not have read the answer.
type unconfirmed struct{ error }

func (e unconfirmed) Unwrap() error { return e.error }

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
	stop := context.AfterFunc(ctx, func() { _ = c.Reset() })
	defer stop()
	a, err := receive(c, dir, progress)
	if err == nil && a.OK {
		if eerr := senderEnd(c); eerr != nil {
			err = unconfirmed{eerr} // receiveTo tells the sender again
		}
	}
	_ = c.Close()
	return a, err
}

// senderEnd waits for the sender's clean end of the session, its word that
// it read the answer (it ends a session cleanly once Serve returned; spec
// End.tla Exit). The receiver's own close is no such word: it returns once
// the relay has the bytes, and the relay can still reset the sender's
// session under the answer (mutant close-confirms).
func senderEnd(c *nfnet.Conn) error {
	_, err := io.Copy(io.Discard, c)
	return err
}

// retellOnce dials t again and answers its manifest with a, the answer
// of the commit whose session was cut (xfer.Retell).
func retellOnce(ctx context.Context, d *nfnet.Dialer, t nfnet.Target, mode nfnet.DirectMode, a xfer.Answer) error {
	open, cancel := context.WithTimeout(ctx, openBound(mode))
	c, err := d.DialTarget(open, t, nfnet.DialOptions{Direct: mode})
	cancel()
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = c.Reset() })
	defer stop()
	err = xfer.Retell(c, a)
	if err == nil {
		err = senderEnd(c)
	}
	_ = c.Close()
	return err
}

// receive is xfer.Receive (a test cuts the session under its answer).
var receive = xfer.Receive

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

// relayHint names the relay a connection was for and the flag that names
// another.
func relayHint(err error, relay string) error {
	if nfnet.CodeOf(err) == nfnet.CodeAuth { // a refused machine: enrolHint's (another relay is no fix)
		return fmt.Errorf("relay %s: %w", relay, err)
	}
	return fmt.Errorf("relay %s (--relay names another): %w", relay, err)
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

// failTo says why to w once: as a line, or (asJSON) as one JSON object
// with the exit status, its code and the message.
func failTo(w io.Writer, asJSON bool, code int, err error) int {
	if !asJSON {
		fmt.Fprintln(w, "nfltr-send:", err)
		return code
	}
	b, _ := json.Marshal(map[string]any{"exit": code, "code": nfnet.Code(code).String(), "message": err.Error()})
	fmt.Fprintln(w, string(b))
	return code
}
