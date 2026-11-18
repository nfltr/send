// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

//go:build !sendtrace

package xfer

import "os"

// The trace recorder (trace_on.go) is compiled only with -tags sendtrace:
// every call site is behind the constant traceOn, so this build has none.
const traceOn = false

type recvTrace struct{}

func traceAtomic() func()                { return nil }
func traceExpire(*os.Root, string)       {}
func (r *receiver) trAtomic() func()     { return nil }
func (r *receiver) trOpen(bool, string)  {}
func (r *receiver) trSwept()             {}
func (r *receiver) trStart(int, int64)   {}
func (r *receiver) trData([]byte)        {}
func (r *receiver) trBad([]byte)         {}
func (r *receiver) trVerified(bool)      {}
func (r *receiver) trCut()               {}
func (r *receiver) trBegin(bool)         {}
func (r *receiver) trRename(int, error)  {}
func (r *receiver) trPend(string, int)   {}
func (r *receiver) trCommit(string, int) {}
func (r *receiver) trEnd()               {}
