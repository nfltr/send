// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package xfer

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// renameNoReplace: MoveFileEx without MOVEFILE_REPLACE_EXISTING, which
// os.Rename passes (it replaces an existing file). MoveFileEx takes paths:
// the two are root's name joined with names root has already resolved
// (the stage's, and a top-level name), so root's confinement holds unless
// the receiving directory itself is swapped meanwhile.
func renameNoReplace(root *os.Root, old, new string) error {
	if _, err := root.Lstat(old); err != nil {
		return err
	}
	from, err := windows.UTF16PtrFromString(filepath.Join(root.Name(), filepath.FromSlash(old)))
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(filepath.Join(root.Name(), filepath.FromSlash(new)))
	if err != nil {
		return err
	}
	if err := windows.MoveFileEx(from, to, 0); err != nil {
		return &os.LinkError{Op: "rename", Old: old, New: new, Err: err}
	}
	return nil
}
