// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package xfer

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// renameNoReplace: renameat2 RENAME_NOREPLACE; a file system that refuses
// the flag gets linkRename.
func renameNoReplace(root *os.Root, old, new string) error {
	err := renameAt(root, old, new, func(od, nd *os.File, ob, nb string) error {
		return unix.Renameat2(int(od.Fd()), ob, int(nd.Fd()), nb, unix.RENAME_NOREPLACE)
	})
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) {
		return linkRename(root, old, new)
	}
	return err
}
