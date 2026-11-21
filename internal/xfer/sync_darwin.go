// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package xfer

import (
	"os"

	"golang.org/x/sys/unix"
)

// syncFile hands a received file's data to the drive: fsync(2), which on
// macOS does not flush the drive's cache (os.File.Sync is F_FULLFSYNC, a
// cache flush per file: milliseconds each). The commit's intent record is
// synced with F_FULLFSYNC before any rename, which flushes the drive's
// whole cache, these files' data with it (fcntl(2) F_FULLFSYNC).
func syncFile(f *os.File) error { return unix.Fsync(int(f.Fd())) }

// syncStage: nothing more before the intent record (see syncFile).
func syncStage(*os.Root) error { return nil }

// preallocate: none here (Linux only).
func preallocate(*os.File, int64) {}
