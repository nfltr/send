------------------------------ MODULE TraceCommit ------------------------------
\* Copyright 2026 nfltr
\* SPDX-License-Identifier: Apache-2.0
\*
\* Trace validation of Commit.tla: the steps the receivers of one stage
\* recorded (xfer -tags sendtrace, <stage>-g<n>-commit.ndjson, from the
\* stage's creation to its removal) must be a behaviour of Commit. Each
\* line names its receiver (p) and top-level entry (t, in manifest order).
\* Busy and Taken are a receiver that left without opening the stage: a
\* stutter step, with the model's reason for it. Expire of a stage holding
\* nothing is a stutter too. Foreign and Replace (someone else's names in
\* the directory) are hidden steps. Accepted when a behaviour takes every
\* line (TraceNotDone violated).
EXTENDS TraceLog, Commit, TLC

TraceN == Log[1].n
TraceP == LET ps == {Lines[k].p : k \in {k \in DOMAIN Lines : "p" \in DOMAIN Lines[k]}} \cup {1}
          IN CHOOSE m \in ps : \A q \in ps : q <= m

VARIABLE l
traceVars == <<vars, l>>

E == Lines[l]
\* A recorded step takes the next line; a hidden one takes none.
Adv == l <= Len(Lines) /\ l' = l + 1
Hid == l <= Len(Lines) /\ UNCHANGED l

TOpen == Adv /\ E.ev = "Open" /\ Open(E.p) /\ (pc'[E.p] = "renaming") = E.rec
TBusy == Adv /\ E.ev = "Busy" /\ pc[E.p] = "idle" /\ (holds # {} \/ \E q \in Procs : pc[q] = "done") /\ UNCHANGED vars
TTaken == Adv /\ E.ev = "Taken" /\ pc[E.p] = "idle" /\ Free /\ ~Ours(intent) /\ (\E t \in Tops : dir[t] # "none") /\ UNCHANGED vars
TWrite == Adv /\ E.ev = "Write" /\ Write(E.p, E.t)
TVerify == Adv /\ E.ev = "Verify" /\ Verify(E.p, E.t)
TCut == Adv /\ E.ev = "Cut" /\ Cut(E.p)
TRefuse == Adv /\ E.ev = "Refuse" /\ Refuse(E.p)
TBegin == Adv /\ E.ev = "Begin" /\ Begin(E.p) /\ pc'[E.p] = "renaming"
TBeginRefused == Adv /\ E.ev = "BeginRefused" /\ Begin(E.p) /\ pc'[E.p] = "idle"
TRename == Adv /\ E.ev = "Rename" /\ next[E.p] = E.t /\ Rename(E.p) /\ pc'[E.p] = "renaming"
TRenameRefused == Adv /\ E.ev = "RenameRefused" /\ next[E.p] = E.t /\ Rename(E.p) /\ pc'[E.p] = "idle"
TStop == Adv /\ E.ev = "Stop" /\ Stop(E.p)
TFinish == Adv /\ E.ev = "Finish" /\ Finish(E.p)
TExpire == Adv /\ E.ev = "Expire" /\ IF \E t \in Tops : stage[t] # "none" THEN Expire
                               ELSE holds = {} /\ intent # "ours" /\ UNCHANGED vars

HForeign == Hid /\ Foreign
HReplace == Hid /\ Replace

TraceInit == Init /\ l = 1
TraceNext == TOpen \/ TBusy \/ TTaken \/ TWrite \/ TVerify \/ TCut \/ TRefuse \/ TBegin \/ TBeginRefused \/ TRename \/ TRenameRefused \/ TStop \/ TFinish \/ TExpire \/ HForeign \/ HReplace
TraceNotDone == l <= Len(Lines)
=============================================================================
