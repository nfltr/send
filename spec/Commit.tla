---------------------------- MODULE Commit ----------------------------
\* Copyright 2026 nfltr
\* SPDX-License-Identifier: Apache-2.0
\*
\* The commit of one offer of nfltr-send, receiver side (spec/README.md).
\* Files arrive verified in the offer's stage (Resume.tla). Once all have,
\* the commit writes an intent record into the stage (the offer's digest,
\* written and synced before the first rename: a write-ahead record, as a
\* journaling file system's), then renames the top-level entries one by one
\* into the receiving directory; any rename can fail, and the receiver can
\* crash at any point: either leaves things as they are. A later
\* receive of the same offer that finds its intent rolls forward: it renames
\* what the stage still holds, refusing a name the directory holds that the
\* stage holds too (not ours), and takes a name the stage no longer holds as
\* already moved. Only a commit whose every rename landed removes the stage.

EXTENDS Naturals

CONSTANTS N,         \* the offer's top-level entries
          MaxFaults, \* rename failures, crashes and foreign names together
          Mutant     \* "none", or the planted failure (README "Mutants")

VARIABLES stage,   \* the entries in the stage, verified
          dir,     \* each name in the receiving directory: "none" | "ours" | "foreign"
          pc,      \* "idle" (no commit running) | "renaming" | "done"
          next,    \* the entry the commit renames next
          intent,  \* the intent record the stage holds: "none" | "ours" | "foreign"
          faults,  \* faults so far
          arrived, \* ghost: the entries that arrived verified
          crashed  \* ghost: a commit stopped part way

vars == <<stage, dir, pc, next, intent, faults, arrived, crashed>>

Tops == 1..N

TypeOK ==
    /\ stage \subseteq Tops /\ arrived \subseteq Tops
    /\ dir \in [Tops -> {"none", "ours", "foreign"}]
    /\ pc \in {"idle", "renaming", "done"}
    /\ next \in 1..N+1
    /\ intent \in {"none", "ours", "foreign"}
    /\ faults \in 0..MaxFaults
    /\ crashed \in BOOLEAN

\* A record "foreign" stands for one that is not this offer's: a torn
\* write, or another offer's (the code compares the whole digest).
Init ==
    /\ stage = {} /\ arrived = {} /\ dir = [t \in Tops |-> "none"]
    /\ pc = "idle" /\ next = 1 /\ intent \in {"none", "foreign"}
    /\ faults = 0 /\ crashed = FALSE

\* Whether the receiver takes the stage's intent record as this offer's.
Ours(i) == i = "ours" \/ (Mutant = "trust-foreign" /\ i = "foreign")

\* Arrive: an entry arrives verified in the stage (Resume.tla).
Arrive ==
    /\ pc = "idle" /\ ~Ours(intent)
    /\ \E t \in Tops \ stage : stage' = stage \cup {t} /\ arrived' = arrived \cup {t}
    /\ UNCHANGED <<dir, pc, next, intent, faults, crashed>>

\* Foreign: a name of the offer's appears in the directory, someone else's,
\* at any time, a commit's renames included.
Foreign ==
    /\ pc # "done" /\ faults < MaxFaults
    /\ \E t \in Tops : dir[t] = "none" /\ dir' = [dir EXCEPT ![t] = "foreign"]
    /\ faults' = faults + 1
    /\ UNCHANGED <<stage, pc, next, intent, arrived, crashed>>

\* Begin: every entry arrived and the directory holds none of the names:
\* the intent is written and synced, then the renames start.
Begin ==
    /\ pc = "idle" /\ ~Ours(intent) /\ stage = Tops
    /\ \A t \in Tops : dir[t] = "none"
    /\ intent' = "ours" /\ pc' = "renaming" /\ next' = 1
    /\ UNCHANGED <<stage, dir, faults, arrived, crashed>>

\* Recover: a later receive finds the offer's intent (mutant no-intent:
\* rolls forward without one) and no name the stage holds is taken.
Recover ==
    /\ pc = "idle" /\ (Ours(intent) \/ Mutant = "no-intent")
    /\ \A t \in stage : dir[t] = "none"
    /\ pc' = "renaming" /\ next' = 1
    /\ UNCHANGED <<stage, dir, intent, faults, arrived, crashed>>

\* Rename: the next entry lands; one the stage no longer holds is taken as
\* moved already. The rename does not replace: a name that appeared since
\* the check fails it, and the commit stops with the stage and its intent
\* kept (mutant replace: a rename that replaces).
Rename ==
    /\ pc = "renaming" /\ next <= N
    /\ IF next \notin stage
          THEN UNCHANGED <<stage, dir, pc>> /\ next' = next + 1
       ELSE IF dir[next] = "none" \/ Mutant = "replace"
          THEN /\ stage' = stage \ {next} /\ dir' = [dir EXCEPT ![next] = "ours"]
               /\ next' = next + 1 /\ UNCHANGED pc
       ELSE pc' = "idle" /\ UNCHANGED <<stage, dir, next>>
    /\ UNCHANGED <<intent, faults, arrived, crashed>>

\* Expire: the sweep of another receive finds the stage stale (nobody wrote
\* to it for StageLife) with no intent record of its own: an abandoned
\* offer, removed; received again, it starts over. A stale stage with its
\* intent is finished by that sweep instead (Recover, by another process).
\* Mutant sweep-deletes-intent: the intent's stage removed too.
Expire ==
    /\ pc = "idle" /\ faults < MaxFaults /\ stage # {}
    /\ (intent # "ours" \/ Mutant = "sweep-deletes-intent")
    /\ stage' = {} /\ intent' = "none" /\ faults' = faults + 1
    /\ arrived' = IF intent = "ours" THEN arrived ELSE {}
    /\ UNCHANGED <<dir, pc, next, crashed>>

\* Stop: a rename fails, or the receiver crashes, mid-commit: what landed
\* stays, and so do the stage and its intent (mutant drop-stage: a failed
\* commit removes the stage).
Stop ==
    /\ pc = "renaming" /\ faults < MaxFaults
    /\ stage' = IF Mutant = "drop-stage" THEN {} ELSE stage
    /\ pc' = "idle" /\ faults' = faults + 1 /\ crashed' = TRUE
    /\ UNCHANGED <<dir, next, intent, arrived>>

\* Finish: every entry landed; the directory is synced and the stage, its
\* intent with it, removed.
Finish ==
    /\ pc = "renaming" /\ next = N + 1
    /\ pc' = "done" /\ intent' = "none" /\ stage' = {}
    /\ UNCHANGED <<dir, next, faults, arrived, crashed>>

Served == pc = "done" /\ UNCHANGED vars

\* Refused: a foreign name holds the offer back; the stage stays.
Refused == pc = "idle" /\ (\E t \in Tops : dir[t] = "foreign") /\ UNCHANGED vars

Next == Arrive \/ Foreign \/ Expire \/ Begin \/ Recover \/ Rename \/ Stop \/ Finish \/ Served \/ Refused

Spec == Init /\ [][Next]_vars
        /\ WF_vars(Arrive) /\ WF_vars(Begin) /\ WF_vars(Recover) /\ WF_vars(Rename) /\ WF_vars(Finish)

\* Kept: verified data is never deleted: every entry that arrived is in
\* the stage or in place.
Kept == \A t \in arrived : t \in stage \/ dir[t] = "ours"

\* Committed: a done commit holds every entry in place, the stage gone, no
\* foreign name taken for ours.
Committed == pc = "done" => stage = {} /\ \A t \in Tops : dir[t] = "ours"

\* AllOrNone: with no intent pending, the directory holds none of the
\* offer's entries before its commit (a pending intent's commit is
\* finished by the next receive).
AllOrNone == pc = "idle" /\ intent # "ours" => \A t \in Tops : dir[t] # "ours"

\* NoReplace: another's name is never replaced.
NoReplace == [][\A t \in Tops : dir[t] = "foreign" => dir'[t] = "foreign"]_dir

\* Done: with finitely many faults, the offer is committed, or a foreign
\* name refuses it (the stage kept).
Done == <>(pc = "done" \/ \E t \in Tops : dir[t] = "foreign")

\* Witnesses (each must be violated: the behaviour is reachable).
WitnessPartWay == ~(pc = "renaming" /\ stage # {} /\ \E t \in Tops : dir[t] = "ours")
WitnessRecoveredAfterCrash == ~(pc = "done" /\ crashed)
WitnessForeignRefused == ~(pc = "idle" /\ intent = "ours" /\ \E t \in stage : dir[t] = "foreign")
WitnessRefusedMidCommit == ~(pc = "idle" /\ (\E t \in stage : dir[t] = "foreign") /\ \E u \in Tops : dir[u] = "ours")
WitnessExpired == ~(pc = "idle" /\ stage = {} /\ faults > 0 /\ \A t \in Tops : dir[t] # "ours")
=============================================================================
