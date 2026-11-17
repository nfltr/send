// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package xfer

import (
	"io/fs"
	"os"
	"path"
)

// renameNoReplace renames old to new inside root, never replacing an
// existing new (spec Commit.tla Rename, NoReplace): an existing new is an
// error satisfying errors.Is(err, fs.ErrExist). Each platform's primitive
// is in rename_<goos>.go; it works on directories opened through root and
// base names, so root's confinement holds (a name resolved through root,
// the last component never followed).

// at opens name's directory through root and returns it with name's base.
func at(root *os.Root, name string) (*os.File, string, error) {
	d, err := root.Open(path.Dir(name))
	return d, path.Base(name), err
}

// renameAt is the primitive over two directories opened through root.
func renameAt(root *os.Root, old, new string, prim func(od, nd *os.File, ob, nb string) error) error {
	od, ob, err := at(root, old)
	if err != nil {
		return err
	}
	defer od.Close() //nolint:errcheck
	nd, nb, err := at(root, new)
	if err != nil {
		return err
	}
	defer nd.Close() //nolint:errcheck
	if err := prim(od, nd, ob, nb); err != nil {
		return &os.LinkError{Op: "rename", Old: old, New: new, Err: err}
	}
	return nil
}

// linkRename is the fallback where no primitive exists or a file system
// refuses it: a file is linked (link fails on an existing name) and its
// old name removed; a directory is renamed after a check, which is not
// atomic against a name that appears between the two.
func linkRename(root *os.Root, old, new string) error {
	fi, err := root.Lstat(old)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		if err := root.Link(old, new); err != nil {
			return err
		}
		return root.Remove(old)
	}
	if _, err := root.Lstat(new); err == nil {
		return &os.LinkError{Op: "rename", Old: old, New: new, Err: fs.ErrExist}
	}
	return root.Rename(old, new)
}
