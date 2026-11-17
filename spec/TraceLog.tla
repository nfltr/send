------------------------------- MODULE TraceLog -------------------------------
\* Copyright 2026 nfltr
\* SPDX-License-Identifier: Apache-2.0
\*
\* A recorded trace (xfer -tags sendtrace), read once: its first line is
\* {"ev":"Init","n":...}, the others the steps (spec/README.md "Traces").
EXTENDS Json, Sequences

CONSTANT TraceFile

Log == ndJsonDeserialize(TraceFile)
Lines == SubSeq(Log, 2, Len(Log))
=============================================================================
