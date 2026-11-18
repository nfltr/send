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
