// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !darwin

package xfer

import "os"

// syncFile puts a received file on stable storage (FlushFileBuffers on
// Windows, fsync elsewhere), before the commit can begin.
func syncFile(f *os.File) error { return f.Sync() }

// syncStage: each file was synced (syncFile).
func syncStage(*os.Root) error { return nil }

// preallocate: none here (Linux only).
func preallocate(*os.File, int64) {}
