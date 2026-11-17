// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package xfer

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// renameNoReplace: renamex_np RENAME_EXCL (renameatx_np, over the
// directories); a file system that refuses it gets linkRename.
func renameNoReplace(root *os.Root, old, new string) error {
	err := renameAt(root, old, new, func(od, nd *os.File, ob, nb string) error {
		return unix.RenameatxNp(int(od.Fd()), ob, int(nd.Fd()), nb, unix.RENAME_EXCL)
	})
	if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EINVAL) {
		return linkRename(root, old, new)
	}
	return err
}
