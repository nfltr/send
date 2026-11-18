// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

// Package xfer is nfltr-send's transfer protocol inside one end-to-end
// encrypted session (any io.ReadWriter: the session's TLS orders and
// authenticates every byte). The receiver pulls; the sender answers:
//
//  1. sender: manifest (the entries: paths, sizes, modes, mtimes, and each
//     file's BLAKE3 root, taken when it was offered);
//  2. receiver: want (per file it holds a part of: the length of the
//     part's prefix that verifies against the root, in whole chunks);
//  3. sender, per file in manifest order: start (the offset it sends
//     from: the want's, else 0), tree (the file's Bao outboard,
//     package verified), data (one chunk per frame), end; then done;
//  4. receiver: answer. It verifies each chunk on arrival against the
//     manifest's root and writes only one that verifies (a chunk that does
//     not ends the session, ErrUnverified, the verified part kept); it
//     commits (renames into place) only after every file's BLAKE3 over
//     what is on its disk is the manifest's root, so a cut session leaves
//     nothing in place, only the verified parts it resumes from.
//
// The resume rules are spec/Resume.tla's (Open, Send, Bad, Finish); the
// mutants there are the failures they prevent.
package xfer

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/nfltr/send/xfer/verified"
)

// Version is the protocol's, in the manifest.
const Version = 3

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

// Part is a file the receiver holds a verified part of.
type Part struct {
	Index  int   `json:"i"`
	Offset int64 `json:"offset"`
}

type want struct {
	Parts []Part `json:"parts"`
}

type start struct {
	Index  int   `json:"i"`
	Offset int64 `json:"offset"`
}

type end struct {
	Index int `json:"i"`
}

// Answer is the receiver's last word.
type Answer struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	Files   int    `json:"files"`
	Bytes   int64  `json:"bytes"`
}

// conn frames one session.
type conn struct {
	r *bufio.Reader
	w *bufio.Writer
}

func newConn(rw io.ReadWriter) *conn {
	return &conn{r: bufio.NewReaderSize(rw, 256<<10), w: bufio.NewWriterSize(rw, 256<<10)}
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
