// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package xfer

import "os"

// direct: writes through the page cache here (O_DIRECT is Linux's).
type direct struct{}

func openDirect(*os.Root, string) direct { return direct{} }

func (*direct) close() {}

func (*direct) writeAt([]byte, int64) bool { return false }
