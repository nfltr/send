// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package xfer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// A file system that refuses RENAME_NOREPLACE (EOPNOTSUPP, EINVAL) gets the
// link fallback for a file and a typed refusal for a directory, not an
// opaque error (review 2026-11-17 #2, finding D).
func TestRenameat2RefusedTakesTheFallback(t *testing.T) {
	for _, errno := range []error{unix.EOPNOTSUPP, unix.EINVAL} {
		renameat2 = func(int, string, int, string, uint) error { return errno }
		d := t.TempDir()
		must(t, os.MkdirAll(filepath.Join(d, "s", "dir"), 0o755))
		must(t, os.WriteFile(filepath.Join(d, "s", "f"), []byte("f"), 0o600))
		root, err := os.OpenRoot(d)
		must(t, err)
		if err := renameNoReplace(root, "s/f", "f"); err != nil {
			t.Errorf("%v: file: %v, want linked", errno, err)
		}
		if err := renameNoReplace(root, "s/dir", "dir"); !errors.Is(err, ErrNoAtomicRename) {
			t.Errorf("%v: directory: %v, want ErrNoAtomicRename", errno, err)
		}
		_ = root.Close()
	}
	renameat2 = unix.Renameat2
}
