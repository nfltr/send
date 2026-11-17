// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package xfer

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// frame is one frame's bytes.
func frame(t byte, p []byte) []byte {
	h := [5]byte{t}
	binary.BigEndian.PutUint32(h[1:], uint32(len(p)))
	return append(h[:], p...)
}

func manifestFrame(es ...Entry) []byte {
	b, _ := json.Marshal(Manifest{Version: Version, Entries: es})
	return frame(frameManifest, b)
}

// FuzzReceive feeds a receiver any sender's bytes: it must not panic, and
// nothing may land outside the receiving directory (make check runs it
// briefly; review 2026-11-17).
func FuzzReceive(f *testing.F) {
	whole := append(manifestFrame(Entry{Path: "a", Size: 2, Mode: 0o644}), frame(frameStart, []byte(`{"i":0,"offset":0}`))...)
	whole = append(whole, frame(frameData, []byte("hi"))...)
	whole = append(whole, frame(frameEnd, []byte(`{"i":0,"sha256":"8f434346648f6b96df89dda901c5176b10a6d83961dd3c1ac88b59b2dc327aa4"}`))...)
	whole = append(whole, frame(frameDone, nil)...)
	for _, seed := range [][]byte{
		whole,
		manifestFrame(Entry{Path: "../escape", Size: 1}),
		manifestFrame(Entry{Path: "/etc/passwd", Size: 1}),
		manifestFrame(Entry{Path: `a\..\..\b`, Size: 1}),
		manifestFrame(Entry{Path: "d", Dir: true}, Entry{Path: "d/../../x", Size: 1}),
		manifestFrame(Entry{Path: ".nfltr-send-x", Dir: true}),
		manifestFrame(Entry{Path: "a", Size: -1}),
		frame(frameManifest, []byte(`{"version":1,"entries":[`)),
		{frameManifest, 0xff, 0xff, 0xff, 0xff},
		append(manifestFrame(Entry{Path: "a", Size: 1}), frameData, 0x7f, 0xff, 0xff, 0xff),
		append(manifestFrame(Entry{Path: "a", Size: 1}), frame(frameStart, []byte(`{"i":0,"offset":5}`))...),
		frame(frameAnswer, []byte(`{"message":"busy"}`)),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		base := t.TempDir()
		dst := filepath.Join(base, "dst")
		if err := os.Mkdir(dst, 0o755); err != nil {
			t.Fatal(err)
		}
		_, _ = Receive(struct {
			io.Reader
			io.Writer
		}{bytes.NewReader(in), io.Discard}, dst, nil)
		if es, err := os.ReadDir(base); err != nil || len(es) != 1 {
			t.Fatalf("outside the receiving directory: %v, %v", es, err)
		}
	})
}
