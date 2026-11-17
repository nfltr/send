---------------------------- MODULE Commit ----------------------------
\* Copyright 2026 nfltr
\* SPDX-License-Identifier: Apache-2.0
\*
\* The commit of one offer of nfltr-send, receiver side (spec/README.md).
\* P receivers of the same offer into the same directory share its stage.
\* A receiver opens the stage by taking its exclusive lock (held until it
\* ends; a receiver that finds it held refuses: busy). It then writes
\* files into the stage and verifies each (Resume.tla). Once all are
\* verified, the commit writes an intent record into the stage (the offer's
\* digest, written and synced before the first rename: a write-ahead
\* record, as a journaling file system's), then renames the top-level
\* entries one by one into the receiving directory; any rename can fail,
\* and the receiver can crash at any point: either leaves things as they
\* are. A later receive of the same offer that finds its intent rolls
\* forward: it renames what the stage still holds, refusing a name the
\* directory holds that the stage holds too (not ours), and takes a name
\* the stage no longer holds as already moved only when what is there has
\* the manifest's content. Only a commit whose every rename landed removes
\* the stage.

EXTENDS Naturals

CONSTANTS N,         \* the offer's top-level entries
          P,         \* receivers of the offer
          MaxFaults, \* rename failures, crashes, cut sessions, foreign names together
          Mutant     \* "none", or the planted failure (README "Mutants")

VARIABLES stage,   \* each entry in the stage: "none" | "part" (being written) | "ok" (verified)
          dir,     \* each name in the receiving directory: "none" | "ours" | "bad" (ours, unverified) | "foreign"
          pc,      \* each receiver: "idle" | "recv" | "renaming" | "done"
          next,    \* each receiver: the entry its commit renames next
          seen,    \* each receiver: the entries verified when it opened the stage (its resume offsets)
          holds,   \* the receivers holding the stage's lock
          intent,  \* the intent record the stage holds: "none" | "ours" | "foreign"
          faults,  \* faults so far
          arrived, \* ghost: the entries that arrived verified
          lost,    \* ghost: entries in place that someone else deleted and recreated
          crashed  \* ghost: the receivers whose commit stopped part way

vars == <<stage, dir, pc, next, seen, holds, intent, faults, arrived, lost, crashed>>

Tops == 1..N
Procs == 1..P

TypeOK ==
    /\ stage \in [Tops -> {"none", "part", "ok"}]
    /\ dir \in [Tops -> {"none", "ours", "bad", "foreign"}]
    /\ pc \in [Procs -> {"idle", "recv", "renaming", "done"}]
    /\ next \in [Procs -> 1..N+1]
    /\ seen \in [Procs -> SUBSET Tops]
    /\ holds \subseteq Procs
    /\ intent \in {"none", "ours", "foreign"}
    /\ faults \in 0..MaxFaults
    /\ arrived \subseteq Tops /\ lost \subseteq Tops /\ crashed \subseteq Procs

\* A record "foreign" stands for one that is not this offer's: a torn
\* write, or another offer's (the code compares the whole digest).
Init ==
    /\ stage = [t \in Tops |-> "none"] /\ dir = [t \in Tops |-> "none"]
    /\ pc = [p \in Procs |-> "idle"] /\ next = [p \in Procs |-> 1]
    /\ seen = [p \in Procs |-> {}] /\ holds = {}
    /\ intent \in {"none", "foreign"}
    /\ faults = 0 /\ arrived = {} /\ lost = {} /\ crashed = {}

\* Whether the receiver takes the stage's intent record as this offer's.
Ours(i) == i = "ours" \/ (Mutant = "trust-foreign" /\ i = "foreign")

\* The stage's lock is free (mutant no-lock: no lock, as before the
\* review of 2026-11-17, finding A).
Free == holds = {} \/ Mutant = "no-lock"

Release(p) == holds' = holds \ {p}

\* Open: a receiver takes the stage's lock. With the offer's intent it
\* finishes the commit (Recover); else, while the directory holds none of
\* the names, it receives, asking the sender only for what the stage does
\* not hold verified (mutant no-intent: rolls forward without an intent).
Open(p) ==
    /\ pc[p] = "idle" /\ Free
    /\ \/ /\ Ours(intent) \/ Mutant = "no-intent"
          /\ pc' = [pc EXCEPT ![p] = "renaming"] /\ next' = [next EXCEPT ![p] = 1]
          /\ UNCHANGED seen
       \/ /\ ~Ours(intent) /\ \A t \in Tops : dir[t] = "none"
          /\ pc' = [pc EXCEPT ![p] = "recv"] /\ UNCHANGED next
          /\ seen' = [seen EXCEPT ![p] = {t \in Tops : stage[t] = "ok"}]
    /\ holds' = holds \cup {p}
    /\ UNCHANGED <<stage, dir, intent, faults, arrived, lost, crashed>>

\* Write: the receiver truncates an entry to its resume offset and writes
\* it: unverified until Verify. It writes only what was not verified when
\* it opened the stage.
Write(p, t) ==
    /\ pc[p] = "recv" /\ t \notin seen[p] /\ stage[t] # "part"
    /\ stage' = [stage EXCEPT ![t] = "part"]
    /\ UNCHANGED <<dir, pc, next, seen, holds, intent, faults, arrived, lost, crashed>>

\* Verify: the entry's SHA-256 over what is on disk is the manifest's.
Verify(p, t) ==
    /\ pc[p] = "recv" /\ stage[t] = "part"
    /\ stage' = [stage EXCEPT ![t] = "ok"] /\ seen' = [seen EXCEPT ![p] = @ \cup {t}]
    /\ arrived' = arrived \cup {t}
    /\ UNCHANGED <<dir, pc, next, holds, intent, faults, lost, crashed>>

\* Cut: the session ends before the commit; the stage stays, the lock is
\* released.
Cut(p) ==
    /\ pc[p] = "recv" /\ faults < MaxFaults
    /\ pc' = [pc EXCEPT ![p] = "idle"] /\ Release(p) /\ faults' = faults + 1
    /\ UNCHANGED <<stage, dir, next, seen, intent, arrived, lost, crashed>>

\* Refuse: a file's digest over what landed is not the manifest's (a write
\* that landed wrong, Resume.tla Rot, or a sender whose bytes are not its
\* manifest's): the transfer is refused and not resumed, so its stage is
\* removed under the lock; received again, it starts over.
Refuse(p) ==
    /\ pc[p] = "recv" /\ faults < MaxFaults
    /\ stage' = [t \in Tops |-> "none"] /\ intent' = "none" /\ arrived' = {}
    /\ pc' = [pc EXCEPT ![p] = "idle"] /\ Release(p) /\ faults' = faults + 1
    /\ UNCHANGED <<dir, next, seen, lost, crashed>>

\* Foreign: a name of the offer's appears in the directory, someone else's,
\* at any time before the offer is committed, a commit's renames included.
Foreign ==
    /\ \A p \in Procs : pc[p] # "done"
    /\ faults < MaxFaults
    /\ \E t \in Tops : dir[t] = "none" /\ dir' = [dir EXCEPT ![t] = "foreign"]
    /\ faults' = faults + 1
    /\ UNCHANGED <<stage, pc, next, seen, holds, intent, arrived, lost, crashed>>

\* Replace: between a stopped commit and its recovery, someone deletes an
\* entry the commit had moved into place and creates another under its
\* name (review 2026-11-17, finding B).
Replace ==
    /\ \A p \in Procs : pc[p] \notin {"renaming", "done"}
    /\ faults < MaxFaults
    /\ \E t \in Tops : dir[t] = "ours" /\ dir' = [dir EXCEPT ![t] = "foreign"] /\ lost' = lost \cup {t}
    /\ faults' = faults + 1
    /\ UNCHANGED <<stage, pc, next, seen, holds, intent, arrived, crashed>>

\* Begin: every entry verified and the directory holds none of the names:
\* the intent is written and synced, then the renames start. A name the
\* directory holds refuses the commit: the stage stays, the lock is released.
Begin(p) ==
    /\ pc[p] = "recv" /\ \A t \in Tops : stage[t] = "ok"
    /\ IF \A t \in Tops : dir[t] = "none"
          THEN intent' = "ours" /\ pc' = [pc EXCEPT ![p] = "renaming"] /\ next' = [next EXCEPT ![p] = 1]
               /\ UNCHANGED holds
       ELSE pc' = [pc EXCEPT ![p] = "idle"] /\ Release(p) /\ UNCHANGED <<intent, next>>
    /\ UNCHANGED <<stage, dir, seen, faults, arrived, lost, crashed>>

\* Rename: the next entry lands, as the stage holds it. One the stage no
\* longer holds is taken as moved already only when the directory's has
\* the manifest's content (mutant trust-moved: any name taken as moved;
\* sweep-trust-moved: so does the sweep, here receiver P, that finishes a
\* stale stage holding its intent, as before 2026-11-17's lane SENDTRACE).
\* The rename does not replace: a name that appeared since the check
\* fails it, and the commit stops with the stage and its intent kept
\* (mutant replace: a rename that replaces).
Rename(p) ==
    /\ pc[p] = "renaming" /\ next[p] <= N
    /\ LET t == next[p] IN
       IF stage[t] = "none"
          THEN IF dir[t] = "ours" \/ (Mutant = "trust-moved" /\ dir[t] # "none")
                       \/ (Mutant = "sweep-trust-moved" /\ p = P /\ dir[t] # "none")
                  THEN next' = [next EXCEPT ![p] = t + 1] /\ UNCHANGED <<stage, dir, pc, holds>>
               ELSE pc' = [pc EXCEPT ![p] = "idle"] /\ Release(p) /\ UNCHANGED <<stage, dir, next>>
       ELSE IF dir[t] = "none" \/ Mutant = "replace"
          THEN /\ stage' = [stage EXCEPT ![t] = "none"]
               /\ dir' = [dir EXCEPT ![t] = IF stage[t] = "ok" THEN "ours" ELSE "bad"]
               /\ next' = [next EXCEPT ![p] = t + 1] /\ UNCHANGED <<pc, holds>>
       ELSE pc' = [pc EXCEPT ![p] = "idle"] /\ Release(p) /\ UNCHANGED <<stage, dir, next>>
    /\ UNCHANGED <<seen, intent, faults, arrived, lost, crashed>>

\* Expire: the sweep of another receive finds the stage stale (nobody wrote
\* to it for StageLife), takes its lock (a held one: skipped), and finds no
\* intent record of its own: an abandoned offer, removed; received again,
\* it starts over. A stale stage with its intent is finished by that sweep
\* instead (Open, by another process). Mutant sweep-deletes-intent: the
\* intent's stage removed too.
Expire ==
    /\ holds = {} /\ faults < MaxFaults /\ \E t \in Tops : stage[t] # "none"
    /\ (intent # "ours" \/ Mutant = "sweep-deletes-intent")
    /\ stage' = [t \in Tops |-> "none"] /\ intent' = "none" /\ faults' = faults + 1
    /\ arrived' = IF intent = "ours" THEN arrived ELSE {}
    /\ UNCHANGED <<dir, pc, next, seen, holds, lost, crashed>>

\* Stop: a rename fails, or the receiver crashes, mid-commit: what landed
\* stays, and so do the stage and its intent; the lock is released (mutant
\* drop-stage: a failed commit removes the stage).
Stop(p) ==
    /\ pc[p] = "renaming" /\ faults < MaxFaults
    /\ stage' = IF Mutant = "drop-stage" THEN [t \in Tops |-> "none"] ELSE stage
    /\ pc' = [pc EXCEPT ![p] = "idle"] /\ Release(p) /\ faults' = faults + 1
    /\ crashed' = crashed \cup {p}
    /\ UNCHANGED <<dir, next, seen, intent, arrived, lost>>

\* Finish: every entry landed; the directory is synced and the stage, its
\* intent with it, removed under the lock; then the lock is released.
Finish(p) ==
    /\ pc[p] = "renaming" /\ next[p] = N + 1
    /\ pc' = [pc EXCEPT ![p] = "done"] /\ intent' = "none" /\ stage' = [t \in Tops |-> "none"]
    /\ Release(p)
    /\ UNCHANGED <<dir, next, seen, faults, arrived, lost, crashed>>

Served == (\E p \in Procs : pc[p] = "done") /\ UNCHANGED vars

\* Refused: a foreign name holds the offer back; the stage stays.
Refused == (\A p \in Procs : pc[p] = "idle") /\ (\E t \in Tops : dir[t] = "foreign") /\ UNCHANGED vars

Proc(p) == Open(p) \/ (\E t \in Tops : Write(p, t) \/ Verify(p, t)) \/ Cut(p) \/ Refuse(p)
           \/ Begin(p) \/ Rename(p) \/ Stop(p) \/ Finish(p)

Next == (\E p \in Procs : Proc(p)) \/ Foreign \/ Replace \/ Expire \/ Served \/ Refused

Spec == Init /\ [][Next]_vars
        /\ \A p \in Procs : /\ WF_vars(Open(p)) /\ WF_vars(\E t \in Tops : Write(p, t))
                            /\ WF_vars(\E t \in Tops : Verify(p, t)) /\ WF_vars(Begin(p))
                            /\ WF_vars(Rename(p)) /\ WF_vars(Finish(p))

\* Kept: verified data is never deleted by a receiver: every entry that
\* arrived is in the stage, or in place, or was deleted there by someone else.
Kept == \A t \in arrived : stage[t] # "none" \/ dir[t] = "ours" \/ t \in lost

\* Intact: what a commit puts in place is verified.
Intact == \A t \in Tops : dir[t] # "bad"

\* Committed: a done commit holds every entry in place with the manifest's
\* content, the stage gone, no other's name taken for the offer's.
Committed == \A p \in Procs : pc[p] = "done" =>
    /\ \A t \in Tops : stage[t] = "none" /\ dir[t] = "ours"

\* AllOrNone: with no intent pending and no commit running, the directory
\* holds none of the offer's entries.
AllOrNone == (\A p \in Procs : pc[p] \in {"idle", "recv"}) /\ intent # "ours" => \A t \in Tops : dir[t] # "ours"

\* NoReplace: another's name is never replaced.
NoReplace == [][\A t \in Tops : dir[t] = "foreign" => dir'[t] = "foreign"]_dir

\* Done: with finitely many faults, the offer is committed, or a foreign
\* name refuses it (the stage kept).
Done == <>((\E p \in Procs : pc[p] = "done") \/ \E t \in Tops : dir[t] = "foreign")

\* Witnesses (each must be violated: the behaviour is reachable).
WitnessPartWay == ~(\E p \in Procs : pc[p] = "renaming" /\ (\E t \in Tops : stage[t] = "ok") /\ \E t \in Tops : dir[t] = "ours")
WitnessRecoveredAfterCrash == ~(\E p \in Procs : pc[p] = "done" /\ crashed # {})
WitnessOtherFinished == ~(\E p \in Procs : pc[p] = "done" /\ crashed \ {p} # {})
WitnessBusy == ~(\E p, q \in Procs : p # q /\ pc[p] = "recv" /\ pc[q] = "idle" /\ holds = {p})
WitnessForeignRefused == ~((\A p \in Procs : pc[p] = "idle") /\ intent = "ours" /\ \E t \in Tops : stage[t] = "ok" /\ dir[t] = "foreign")
WitnessRefusedMidCommit == ~((\A p \in Procs : pc[p] = "idle") /\ (\E t \in Tops : stage[t] = "ok" /\ dir[t] = "foreign") /\ \E u \in Tops : dir[u] = "ours")
WitnessReplacedRefused == ~(lost # {} /\ intent = "ours" /\ crashed # {} /\ \A p \in Procs : pc[p] = "idle")
WitnessExpired == ~((\A p \in Procs : pc[p] = "idle") /\ (\A t \in Tops : stage[t] = "none" /\ dir[t] # "ours") /\ faults > 0 /\ arrived = {} /\ intent = "none")
=============================================================================
