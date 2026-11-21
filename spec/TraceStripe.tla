------------------------------ MODULE TraceStripe ------------------------------
\* Copyright 2026 nfltr
\* SPDX-License-Identifier: Apache-2.0
\*
\* Trace validation of Stripe.tla: the steps one file's receiver recorded
\* across the sessions of its receives (xfer -tags sendtrace,
\* <stage>-s<entry>.ndjson; internal/xfer/trace_on.go) must be a behaviour
\* of Stripe. Lines: Start (a receive begins: its pool, the file's chunks
\* that do not verify on disk), Open (session k's set), Send (k's next
\* chunk verified and written at index i: Read), Bad (refused there: Read
\* of a corrupted chunk), Cut (k died before its set there was read:
\* Notice), Close (the receiver closed k: Close) and Finish (the commit).
\* The sender is not recorded: its Send, a Corrupt, a Die and a Crash are
\* hidden steps, each taken only towards the next line. K is the most
\* sessions the trace names, W the file's chunks (the window is the
\* sender's and QUIC's: the receiver's lines do not show it, so Bounded is
\* checked by the model's no-window mutant, not here). Accepted when a
\* behaviour takes every line (TraceNotDone violated).
EXTENDS TraceLog, Stripe, TLC

TraceC == Log[1].n
Ks == {Lines[j].k : j \in {j \in DOMAIN Lines : "k" \in DOMAIN Lines[j]}}
TraceK == IF Ks = {} THEN 1 ELSE CHOOSE m \in Ks : \A x \in Ks : x <= m
TraceW == TraceC
TraceFaults == Len(Lines) + 1
Set(s) == {s[j] : j \in DOMAIN s}

VARIABLE l
traceVars == <<vars, l>>

E == Lines[l]
Adv == l <= Len(Lines) /\ l' = l + 1
Hid == l <= Len(Lines) /\ UNCHANGED l
NE == IF l <= Len(Lines) THEN Lines[l] ELSE [ev |-> "none"]
NK == IF "k" \in DOMAIN NE THEN NE.k ELSE 0

\* A receive starts on a stage nothing else holds: the first (Init's pool),
\* or a later one after the earlier ended, its sessions with it (hidden
\* Crash, then Restart: the pool from the disk).
TStart == Adv /\ E.ev = "Start" /\ phase = "run" /\ pool = Set(E.pool)
          /\ (\A k \in Conns : st[k] = "idle") /\ UNCHANGED vars
TRestart == Adv /\ E.ev = "Start" /\ Restart /\ pool' = Set(E.pool)
TOpen == Adv /\ E.ev = "Open" /\ Open(E.k, Set(E.set))
TSend == Adv /\ E.ev = "Send" /\ wire[E.k] /= <<>> /\ Head(wire[E.k]) = E.i
         /\ Min(want[E.k]) = E.i /\ Read(E.k)
TBad == Adv /\ E.ev = "Bad" /\ wire[E.k] /= <<>> /\ Head(wire[E.k]) = -E.i
        /\ Min(want[E.k]) = E.i /\ Read(E.k)
TCut == Adv /\ E.ev = "Cut" /\ Notice(E.k)
TClose == Adv /\ E.ev = "Close" /\ Close(E.k)
TFinish == Adv /\ E.ev = "Finish" /\ Finish /\ phase' = "done"

HTree == Hid /\ NE.ev \in {"Send", "Bad"} /\ Tree
HSend == Hid /\ NE.ev \in {"Send", "Bad"} /\ NK \in Conns /\ wire[NK] = <<>> /\ Send(NK)
HCorrupt == Hid /\ NE.ev = "Bad" /\ NK \in Conns /\ Corrupt(NK)
HDie == Hid /\ NE.ev = "Cut" /\ NK \in Conns /\ Die(NK)
\* Crash, its losses those the next Start's pool names (a member of
\* Crash's choice: the chunks written there).
HCrash == Hid /\ NE.ev = "Start" /\ Faults < MaxFaults /\ phase = "run"
          /\ {i \in Chunks : disk[i] /= i} \subseteq Set(NE.pool)
          /\ disk' = [i \in Chunks |-> IF i \in Set(NE.pool) THEN 0 ELSE disk[i]]
          /\ \E keep \in BOOLEAN : tree' = (tree /\ keep)
          /\ phase' = "down" /\ pool' = {}
          /\ want' = [k \in Conns |-> {}] /\ unsent' = [k \in Conns |-> {}]
          /\ wire' = [k \in Conns |-> <<>>] /\ st' = [k \in Conns |-> "idle"]
          /\ fresh' = {} /\ ev' = Bump("crash") /\ UNCHANGED <<writes, logged, touched>>

TraceInit == Init /\ l = 1
TraceNext == TStart \/ TRestart \/ TOpen \/ TSend \/ TBad \/ TCut \/ TClose \/ TFinish
             \/ HTree \/ HSend \/ HCorrupt \/ HDie \/ HCrash
TraceNotDone == l <= Len(Lines)
=============================================================================
