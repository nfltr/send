// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !darwin && !windows

package xfer

import "os"

// renameNoReplace: no primitive here; linkRename.
func renameNoReplace(root *os.Root, old, new string) error { return linkRename(root, old, new) }
