------------------------------ MODULE TraceResume ------------------------------
\* Copyright 2026 nfltr
\* SPDX-License-Identifier: Apache-2.0
\*
\* Trace validation of Resume.tla: the steps one file's receiver recorded
\* (xfer -tags sendtrace, <stage>-f<entry>.ndjson) must be a behaviour of
\* Resume. A chunk is maxData bytes of the file, its value the digest of
\* its bytes. The receiver records Open (its part on disk, before the cut to
\* the offset the sender answered, in chunks; -1 when not a chunk
\* boundary), Send (a chunk complete on disk), Finish (the digest compared)
\* and Cut; once the file is verified (Resume's committed: the offer's
\* commit is Commit.tla's), a later session's Open, Finish and Cut of it are
\* Served. The sender is not recorded: its file (src) is one whose chunk at
\* each position is a chunk sent there (Srcs), and Rot, Crash and Change
\* are hidden steps, each taken only towards the next line. Accepted when a
\* behaviour takes every line (TraceNotDone violated).
EXTENDS TraceLog, Resume, TLC

TraceN == Log[1].n
SentAt(j) == {Lines[k].v : k \in {k \in DOMAIN Lines : Lines[k].ev = "Send" /\ Lines[k].i = j}}
OpenVals == UNION {{Lines[k].part[j] : j \in DOMAIN Lines[k].part} : k \in {k \in DOMAIN Lines : Lines[k].ev = "Open"}}
TraceVals == {"x"} \cup OpenVals \cup UNION {SentAt(j) : j \in 1..TraceN}
C(j) == IF SentAt(j) = {} THEN {"x"} ELSE SentAt(j)
RECURSIVE Prod(_)
Prod(j) == IF j = 0 THEN {<<>>} ELSE {Append(s, v) : s \in Prod(j - 1), v \in C(j)}
Srcs == Prod(TraceN)

VARIABLE l
traceVars == <<vars, l>>

E == Lines[l]
\* A recorded step takes the next line; a hidden one takes none.
Adv == l <= Len(Lines) /\ l' = l + 1
Hid == l <= Len(Lines) /\ UNCHANGED l
NE == IF l <= Len(Lines) THEN Lines[l] ELSE [ev |-> "none"]

TOpen == Adv /\ E.ev = "Open" /\ committed = <<>> /\ part = E.part /\ Open /\ Len(part') = E.off
TSend == Adv /\ E.ev = "Send" /\ Send /\ Len(part') = E.i /\ part'[E.i] = E.v
TFinish == Adv /\ E.ev = "Finish" /\ committed = <<>> /\ Finish /\ (committed' # <<>>) = E.ok
TCut == Adv /\ E.ev = "Cut" /\ committed = <<>> /\ Cut
TServed == Adv /\ E.ev \in {"Open", "Finish", "Cut"} /\ committed # <<>> /\ Served
           /\ (E.ev = "Open" => E.part = committed /\ E.off = N) /\ (E.ev = "Finish" => E.ok)

HRot == Hid /\ Rot
        /\ \/ /\ NE.ev = "Open" /\ Len(NE.part) = Len(part)
              /\ \E i \in DOMAIN part : part[i] # NE.part[i] /\ part'[i] = NE.part[i]
           \/ /\ NE.ev = "Finish" /\ ~NE.ok /\ part = src
              /\ \E i \in DOMAIN part : part'[i] = "x"
HCrash == Hid /\ NE.ev = "Open" /\ part # NE.part /\ part' = NE.part /\ Crash
HChange == Hid /\ NE.ev = "Open" /\ src' \in Srcs \ {src} /\ Change

TraceInit == src \in Srcs /\ Init /\ l = 1
TraceNext == TOpen \/ TSend \/ TFinish \/ TCut \/ TServed \/ HRot \/ HCrash \/ HChange
TraceNotDone == l <= Len(Lines)
=============================================================================
