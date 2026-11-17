// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package xfer

import (
	"errors"
	"fmt"
	"os"
	"path"
	"runtime"
)

// StageBusyError is a receive refused because another receive of the same
// offer into the same directory holds its stage (spec Commit.tla Open).
type StageBusyError struct{ Stage string }

func (e *StageBusyError) Error() string {
	return fmt.Sprintf("stage %s is in use by another receive of this offer", e.Stage)
}

// ErrNotOurs is an entry in place, taken as moved by a stopped commit,
// whose content is not the manifest's: someone else's (spec Commit.tla
// Rename).
var ErrNotOurs = errors.New("not this offer's")

// ErrNoAtomicRename is a directory commit refused where no rename can
// refuse an existing name atomically (README).
var ErrNoAtomicRename = errors.New("no atomic no-replace rename for a directory on this file system")

// lockName is the stage's lock file, in the stage (entries never start
// with StagePrefix).
const lockName = StagePrefix + "lock"

// errLocked is a lock another holder has.
var errLocked = errors.New("locked")

// lockStage takes the exclusive lock of the stage in root, held until
// release: a receive holds it from opening the stage to its end, commit and
// recovery included, so no two receives of one offer share a stage (spec
// Commit.tla Open). One held elsewhere is a StageBusyError, as is a stage
// removed while it was being locked (a receive that just finished).
func lockStage(root *os.Root, stage string) (*os.File, error) {
	name := path.Join(stage, lockName)
	f, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		_ = f.Close()
		if errors.Is(err, errLocked) {
			return nil, &StageBusyError{Stage: stage}
		}
		return nil, err
	}
	held, err1 := f.Stat()
	now, err2 := root.Lstat(name)
	if err1 != nil || err2 != nil || !os.SameFile(held, now) {
		release(f)
		return nil, &StageBusyError{Stage: stage}
	}
	return f, nil
}

// release releases a stage's lock; nil: none held.
func release(lk *os.File) {
	if lk != nil {
		_ = unlockFile(lk)
		_ = lk.Close()
	}
}

// removeStage removes a stage whose lock lk is held, then releases it. On
// Windows a file open without FILE_SHARE_DELETE (os.OpenFile's) cannot be
// removed, so the lock is released first there.
func removeStage(root *os.Root, stage string, lk *os.File) error {
	if runtime.GOOS == "windows" {
		release(lk)
		return root.RemoveAll(stage)
	}
	err := root.RemoveAll(stage)
	release(lk)
	return err
}
