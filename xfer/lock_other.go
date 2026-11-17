// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

//go:build (!unix && !windows) || aix

package xfer

import (
	"errors"
	"os"
)

// lockFile: no file lock here; a receive refuses rather than share a stage.
func lockFile(*os.File) error { return errors.New("no file lock on this platform") }

func unlockFile(*os.File) error { return nil }
