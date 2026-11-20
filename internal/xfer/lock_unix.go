// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

//go:build unix && !aix

package xfer

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// lockFile: flock(2) LOCK_EX, not waiting; per open file description, so
// it excludes another open of the file in this process too.
func lockFile(f *os.File) error {
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); errors.Is(err, unix.EWOULDBLOCK) {
		return errLocked
	} else if err != nil {
		return &os.PathError{Op: "flock", Path: f.Name(), Err: err}
	}
	return nil
}

func unlockFile(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_UN) }
