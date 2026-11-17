---------------------------- MODULE Commit ----------------------------
\* Copyright 2026 nfltr
\* SPDX-License-Identifier: Apache-2.0
\*
\* The commit of one offer of nfltr-send, receiver side (spec/README.md):
\* every file is verified in the stage; the commit renames the offer's
\* top-level entries one by one from the stage into the receiving
\* directory, and any rename can fail. A failed rename moves the entries
\* before it back into the stage and keeps the stage (the next session
\* commits it without resending); only a commit whose every rename landed
\* removes the stage. Resume.tla is one file's way to the stage; this is
\* the step after it.

EXTENDS Naturals

CONSTANTS N,         \* the offer's top-level entries
          MaxFaults, \* rename failures
          Mutant     \* "none", or the planted failure (README "Mutants")

VARIABLES stage,  \* the entries in the stage, verified
          dir,    \* the offer's entries in the receiving directory
          pc,     \* "idle" (between sessions) | "renaming" | "done"
          next,   \* the entry the commit renames next
          faults  \* rename failures so far

vars == <<stage, dir, pc, next, faults>>

Tops == 1..N

TypeOK ==
    /\ stage \subseteq Tops /\ dir \subseteq Tops
    /\ pc \in {"idle", "renaming", "done"}
    /\ next \in 1..N+1
    /\ faults \in 0..MaxFaults

Init ==
    /\ stage = Tops /\ dir = {} /\ pc = "idle" /\ next = 1 /\ faults = 0

\* Begin: a session whose files are all verified commits, unless the
\* receiving directory already holds one of the offer's names (refused,
\* the stage kept).
Begin ==
    /\ pc = "idle" /\ dir = {}
    /\ pc' = "renaming" /\ next' = 1
    /\ UNCHANGED <<stage, dir, faults>>

\* Rename: the next entry lands.
Rename ==
    /\ pc = "renaming" /\ next <= N
    /\ stage' = stage \ {next} /\ dir' = dir \cup {next} /\ next' = next + 1
    /\ UNCHANGED <<pc, faults>>

\* Fail: the next entry's rename fails; the ones before it move back and
\* the stage stays, for the next session.
Fail ==
    /\ pc = "renaming" /\ next <= N /\ faults < MaxFaults
    /\ LET back == IF Mutant = "no-rollback" THEN {} ELSE 1..(next - 1)
       IN /\ dir' = dir \ back
          /\ stage' = IF Mutant = "drop-stage" THEN {} ELSE stage \cup back
    /\ pc' = "idle" /\ faults' = faults + 1
    /\ UNCHANGED next

\* Finish: every entry landed; the directory is synced and the (empty)
\* stage removed.
Finish ==
    /\ pc = "renaming" /\ next = N + 1
    /\ pc' = "done"
    /\ UNCHANGED <<stage, dir, next, faults>>

Served == pc = "done" /\ UNCHANGED vars

Next == Begin \/ Rename \/ Fail \/ Finish \/ Served

Spec == Init /\ [][Next]_vars /\ WF_vars(Begin) /\ WF_vars(Rename) /\ WF_vars(Finish)

\* Kept: verified data is never deleted: every entry is in the stage or in
\* place.
Kept == \A t \in Tops : t \in stage \cup dir

\* AllOrNone: outside a commit the receiving directory holds none or all
\* of the offer's entries.
AllOrNone == pc # "renaming" => dir \in {{}, Tops}

\* Done: with finitely many rename failures, the offer is committed.
Done == <>(pc = "done" /\ dir = Tops)

\* Witnesses (each must be violated: the behaviour is reachable).
WitnessPartWay == ~(pc = "renaming" /\ dir # {} /\ stage # {})
WitnessRolledBack == ~(pc = "idle" /\ faults > 0)
WitnessCommitAfterFailure == ~(pc = "done" /\ faults = MaxFaults)
=============================================================================
