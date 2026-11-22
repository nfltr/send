// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package xfer

import (
	"bytes"
	"io"
	"testing"
)

// readsInto records the start of each buffer a Read was given.
type readsInto struct {
	r     io.Reader
	bufs  []*byte
	reads int
}

func (x *readsInto) Read(p []byte) (int, error) {
	x.reads++
	if len(p) > 0 {
		x.bufs = append(x.bufs, &p[0])
	}
	return x.r.Read(p)
}

// TestConnReadsPayloadInPlace: a data frame's payload is read from the
// session straight into the caller's buffer (no copy through a read
// buffer first; lane RECVCPU), and the frames around it still read whole.
func TestConnReadsPayloadInPlace(t *testing.T) {
	var wire bytes.Buffer
	w := newConn(&wire)
	payload := bytes.Repeat([]byte{7}, maxData)
	for _, f := range []struct {
		t byte
		p []byte
	}{{frameStart, []byte(`{"index":0}`)}, {frameData, payload}, {frameEnd, []byte(`{"index":0}`)}} {
		if err := w.write(f.t, f.p); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.flush(); err != nil {
		t.Fatal(err)
	}
	rec := &readsInto{r: &wire}
	c := newConn(struct {
		io.Reader
		io.Writer
	}{rec, io.Discard})
	if ty, p, err := c.read(nil); err != nil || ty != frameStart || string(p) != `{"index":0}` {
		t.Fatalf("start: %q %q %v", ty, p, err)
	}
	buf := make([]byte, maxData)
	ty, p, err := c.read(buf)
	if err != nil || ty != frameData || !bytes.Equal(p, payload) {
		t.Fatalf("data: %q %d bytes %v", ty, len(p), err)
	}
	inPlace := false
	for _, b := range rec.bufs {
		inPlace = inPlace || b == &buf[0]
	}
	if !inPlace {
		t.Fatal("the payload was not read straight into the caller's buffer")
	}
	if ty, p, err := c.read(nil); err != nil || ty != frameEnd || string(p) != `{"index":0}` {
		t.Fatalf("end: %q %q %v", ty, p, err)
	}
}
