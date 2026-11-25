// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

// Package xfer is nfltr-send's transfer protocol inside one end-to-end
// encrypted session (any io.ReadWriter: the session's TLS orders and
// authenticates every byte). The receiver pulls; the sender answers:
//
//  1. sender: manifest (the entries: paths, sizes, modes, mtimes, and each
//     file's BLAKE3 root, taken when it was offered);
//  2. receiver: want (the protocol version both speak, Version; per file it holds a part of: the runs of the
//     part's chunks that verify against the root);
//  3. sender, per file in manifest order: start (whether it keeps the
//     want's runs), tree (the file's Bao outboard, package verified),
//     data (one chunk per frame: every chunk outside the kept runs, in
//     order), end; then done;
//  4. receiver: answer. It verifies each chunk on arrival against the
//     manifest's root and writes only one that verifies (a chunk that does
//     not ends the session, ErrUnverified, the verified part kept); it
//     commits (renames into place) only after every file's BLAKE3 over
//     what is on its disk is the manifest's root, so a cut session leaves
//     nothing in place, only the verified parts it resumes from.
//
// The resume rules are spec/Resume.tla's (Open, Send, Bad, Finish): a
// session sends again only the chunks missing or not verifying; the
// mutants there are the failures they prevent.
package xfer

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/nfltr/send/internal/xfer/verified"
)

// Version is the highest protocol version this end speaks; MinVersion the
// lowest it accepts. The manifest names the sender's highest, the
// receiver's want the version both then speak: the lower of the two (a
// want naming none: MinVersion). Version 5 (a hard cut: a 4 end is
// refused at both ends with an answer naming the versions) is the
// striped receive: up to K sessions of one transfer at once, each want
// naming the session's set (spec/Stripe.tla), and Answer.Part.
const (
	Version    = 5
	MinVersion = 5
)

// Frame types.
const (
	frameManifest = 'M'
	frameWant     = 'W'
	frameStart    = 'F'
	frameTree     = 'T'
	frameData     = 'D'
	frameEnd      = 'E'
	frameDone     = 'Z'
	frameAnswer   = 'A'
)

// Bounds: a data or tree frame's payload (a data frame is one verified
// chunk), a control frame's.
const (
	maxData    = verified.GroupSize
	maxControl = 16 << 20
	maxSize    = 1 << 50 // a file's
)

// Entry is one manifest entry: a directory or a regular file, path
// slash-separated and relative.
type Entry struct {
	Path   string `json:"path"`
	Dir    bool   `json:"dir,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Mode   uint32 `json:"mode"`
	MTime  int64  `json:"mtime,omitempty"`  // Unix nanoseconds
	BLAKE3 string `json:"blake3,omitempty"` // a file's root, when it was offered
}

// Manifest is what a sender offers.
type Manifest struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// Part is a file the receiver holds a verified part of: the runs of its
// chunks that verify, each [first, end) in chunks, in order.
type Part struct {
	Index int        `json:"i"`
	Have  [][2]int64 `json:"have"`
}

type want struct {
	Version int    `json:"version,omitempty"` // the version both speak; 0: MinVersion
	Parts   []Part `json:"parts"`
	// Held is the file bytes the receiver's stage held verified at the
	// receive's start, for the sender's progress only (a resume's are
	// done); an older receiver sends none.
	Held int64 `json:"held,omitempty"`
}

type start struct {
	Index int  `json:"i"`
	Kept  bool `json:"kept,omitempty"` // the want's runs are not sent
}

// chunks is the number of chunks of a file of size bytes.
func chunks(size int64) int64 { return (size + maxData - 1) / maxData }

// need is what is sent of a file of n chunks whose runs have are kept:
// the runs between them, in order. have must be valid (validRuns).
func need(have [][2]int64, n int64) (out [][2]int64) {
	var at int64
	for _, r := range append(have, [2]int64{n, n}) {
		if r[0] > at {
			out = append(out, [2]int64{at, r[0]})
		}
		at = r[1]
	}
	return out
}

// validRuns: runs in order, each non-empty, inside n chunks.
func validRuns(runs [][2]int64, n int64) bool {
	var at int64
	for _, r := range runs {
		if r[0] < at || r[1] <= r[0] || r[1] > n {
			return false
		}
		at = r[1]
	}
	return true
}

type end struct {
	Index int `json:"i"`
}

// Answer is the receiver's last word on a session. Part: the session's
// set is in and the offer is not yet committed (another session of the
// transfer gets the final answer): a --once sender serves on.
type Answer struct {
	OK      bool   `json:"ok"`
	Part    bool   `json:"part,omitempty"`
	Message string `json:"message,omitempty"`
	Files   int    `json:"files"`
	Bytes   int64  `json:"bytes"`
}

// conn frames one session. It reads unbuffered: a data frame's payload is
// read from rw straight into its chunk's buffer, not copied through a
// bufio.Reader first (lane RECVCPU; TestConnReadsPayloadInPlace).
type conn struct {
	r io.Reader
	w *bufio.Writer
}

func newConn(rw io.ReadWriter) *conn {
	return &conn{r: rw, w: bufio.NewWriterSize(rw, 256<<10)}
}

func (c *conn) write(t byte, p []byte) error {
	var h [5]byte
	h[0] = t
	binary.BigEndian.PutUint32(h[1:], uint32(len(p))) // #nosec G115 -- callers bound len(p)
	if _, err := c.w.Write(h[:]); err != nil {
		return err
	}
	_, err := c.w.Write(p)
	return err
}

// writeFrame writes a frame whose payload is f[5:], f[:5] its header's
// room: a whole data frame is larger than the write buffer, so an empty
// buffer passes it on in one write.
func (c *conn) writeFrame(t byte, f []byte) error {
	f[0] = t
	binary.BigEndian.PutUint32(f[1:5], uint32(len(f)-5)) // #nosec G115 -- callers bound len(f)
	_, err := c.w.Write(f)
	return err
}

func (c *conn) writeJSON(t byte, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.write(t, b)
}

func (c *conn) flush() error { return c.w.Flush() }

// Refuse answers a session, before the manifest, with a refusal (a sender
// that serves no more sessions now): the receiver's Receive returns
// ErrRefused with message.
func Refuse(w io.Writer, message string) error {
	b, err := json.Marshal(Answer{Message: message})
	if err != nil {
		return err
	}
	h := [5]byte{frameAnswer}
	binary.BigEndian.PutUint32(h[1:], uint32(len(b))) // #nosec G115 -- a short message
	_, err = w.Write(append(h[:], b...))
	return err
}

// ErrProtocol is a frame the peer should not have sent.
var ErrProtocol = errors.New("nfltr-send: protocol error")

// read reads one frame into buf (grown as needed) and returns its type and
// payload.
func (c *conn) read(buf []byte) (byte, []byte, error) {
	var h [5]byte
	if _, err := io.ReadFull(c.r, h[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(h[1:])
	limit := uint32(maxControl)
	if h[0] == frameData || h[0] == frameTree {
		limit = maxData
	}
	if n > limit {
		return 0, nil, fmt.Errorf("%w: frame %q of %d bytes", ErrProtocol, h[0], n)
	}
	if cap(buf) < int(n) {
		buf = make([]byte, n)
	}
	buf = buf[:n]
	if _, err := io.ReadFull(c.r, buf); err != nil {
		return 0, nil, noEOF(err)
	}
	return h[0], buf, nil
}

// readJSON reads one frame of type t into v.
func (c *conn) readJSON(t byte, v any) error {
	got, p, err := c.read(nil)
	if err != nil {
		return noEOF(err)
	}
	if got != t {
		return fmt.Errorf("%w: frame %q, want %q", ErrProtocol, got, t)
	}
	return json.Unmarshal(p, v)
}

// noEOF: a session that ends inside the protocol was cut.
func noEOF(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}
