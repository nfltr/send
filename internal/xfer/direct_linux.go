// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package xfer

import (
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// dioAlign: the address, length and offset of an O_DIRECT write are
// multiples of it (the largest logical block size of common file systems).
const dioAlign = 4096

// direct is a received file opened a second time for writes that bypass
// the page cache (O_DIRECT): the device reads a verified chunk straight
// from its buffer, with no copy into the page cache, no page zeroing and
// no writeback later. The receiver's CPU per GiB written (lane RECVCPU,
// fio on GCP local NVMe, 256 KiB writes): 0.67 -> 0.08 CPU-s.
// syncStage's syncfs still makes the files durable before the commit.
type direct struct{ f *os.File }

// openDirect opens name for direct writes; none where the file system
// refuses O_DIRECT (tmpfs).
func openDirect(r *os.Root, name string) direct {
	f, err := r.OpenFile(name, os.O_WRONLY|unix.O_DIRECT, 0)
	if err != nil {
		return direct{}
	}
	return direct{f}
}

func (d *direct) close() {
	if d != nil && d.f != nil {
		_ = d.f.Close()
		d.f = nil
	}
}

// writeAt writes p at off directly when p's address, length and off are
// aligned (every whole chunk; a file's short last one is not): false when
// it did not, and the caller writes through the page cache. A refused
// direct write ends direct writes to the file.
func (d *direct) writeAt(p []byte, off int64) bool {
	if d == nil || d.f == nil || len(p)%dioAlign != 0 || off%dioAlign != 0 ||
		uintptr(unsafe.Pointer(unsafe.SliceData(p)))%dioAlign != 0 { // #nosec G103 -- the buffer's alignment, read only
		return false
	}
	if _, err := d.f.WriteAt(p, off); err != nil {
		d.close()
		return false
	}
	return true
}
