// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package xfer

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// renameNoReplace: renameat2 RENAME_NOREPLACE; a kernel without it
// (ENOSYS) or a file system that refuses the flag (EINVAL, EOPNOTSUPP)
// gets linkRename.
func renameNoReplace(root *os.Root, old, new string) error {
	err := renameAt(root, old, new, func(od, nd *os.File, ob, nb string) error {
		return renameat2(int(od.Fd()), ob, int(nd.Fd()), nb, unix.RENAME_NOREPLACE)
	})
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP) {
		return linkRename(root, old, new)
	}
	return err
}

// renameat2 is unix.Renameat2 (a test plants a file system's refusal).
var renameat2 = unix.Renameat2
