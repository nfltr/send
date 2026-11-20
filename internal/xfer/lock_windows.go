// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package xfer

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockFile: LockFileEx LOCKFILE_EXCLUSIVE_LOCK on the first byte, not
// waiting; per handle.
func lockFile(f *os.File) error {
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, new(windows.Overlapped))
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return errLocked
	} else if err != nil {
		return &os.PathError{Op: "LockFileEx", Path: f.Name(), Err: err}
	}
	return nil
}

// unlockFile: before the close, which releases the lock only eventually
// (LockFileEx's documentation).
func unlockFile(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, new(windows.Overlapped))
}
