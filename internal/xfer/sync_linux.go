// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package xfer

import (
	"os"

	"golang.org/x/sys/unix"
)

// syncFile: nothing per file; syncStage puts them all on stable storage
// before the commit's intent record.
func syncFile(*os.File) error { return nil }

// syncStage puts the file system holding the stage on stable storage,
// every received file with it, in one call: syncfs(2) (reporting a write
// error since Linux 5.8), not an fsync(2) per file.
func syncStage(sr *os.Root) error {
	d, err := sr.Open(".")
	if err != nil {
		return err
	}
	defer d.Close() //nolint:errcheck
	return unix.Syncfs(int(d.Fd()))
}

// preallocate reserves the blocks of a file about to be written whole
// (fallocate(2) FALLOC_FL_KEEP_SIZE: its size unchanged, what is not
// written reads as zeros), so the writers allocate nothing; best effort
// (a file system without it allocates as it writes). BenchmarkStripedWrite
// (lane SPEED-RECV, ext4): with writeLock, 1.47 -> 0.58 CPU s per GiB.
func preallocate(f *os.File, size int64) {
	if size > 0 {
		_ = unix.Fallocate(int(f.Fd()), unix.FALLOC_FL_KEEP_SIZE, 0, size)
	}
}
