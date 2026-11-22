// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package xfer

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestWriteChunkDirect: on a file system that takes O_DIRECT, a whole
// chunk is written directly and a file's short last chunk through the
// page cache, and the file reads back as written (lane RECVCPU). The
// receive's other tests cover the same writes end to end.
func TestWriteChunkDirect(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close() //nolint:errcheck
	const size = 2*maxData + 1000
	f, err := root.OpenFile("f", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck
	preallocate(f, size)
	d := openDirect(root, "f")
	if d == (direct{}) {
		t.Skipf("%s takes no O_DIRECT (tmpfs)", dir)
	}
	defer d.close()
	want := make([]byte, size)
	for i := range want {
		want[i] = byte(i*31 + i>>10)
	}
	r := &receiver{m: Manifest{Entries: []Entry{{Path: "f", Size: size}}}}
	for _, off := range []int64{maxData, 0, 2 * maxData} { // out of order, as stripes arrive
		p := make([]byte, min(maxData, size-off))
		copy(p, want[off:])
		if err := r.writeChunk(0, f, &d, p, off); err != nil {
			t.Fatal(err)
		}
	}
	if n := r.directs.Load(); n != 2 {
		t.Fatalf("%d chunks written directly, want the 2 whole ones", n)
	}
	got, err := os.ReadFile(filepath.Join(dir, "f"))
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("read back %d bytes (want %d), equal %v, %v", len(got), len(want), bytes.Equal(got, want), err)
	}
}
