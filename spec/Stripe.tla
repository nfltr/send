------------------------------ MODULE Stripe ------------------------------
\* Copyright 2026 nfltr
\* SPDX-License-Identifier: Apache-2.0
\*
\* One file of a transfer striped across up to K sessions at once, each on
\* a QUIC connection of its own (lane SPEED, nfltr
\* docs/decisions/send-line-rate-2026-11-21.md; spec/README.md "Stripe").
\* The receiver holds the chunks still to fetch (pool). A session opens
\* with a want naming a set of chunks taken out of the pool (the receiver's
\* "have" is the complement, as a v4 want already says); the sender sends
\* that set's chunks in index order, at most W unread on a session (QUIC's
\* stream window); the receiver reads them in order, verifies each against
\* the file's tree at the index its want says comes next (the data frame
\* carries no offset) and writes it there, so no session waits for another
\* and nothing is reassembled in memory. A session ends cleanly once its
\* set is read. A session dies (its connection: the network, a peer, a
\* refusal) at any time, its unread chunks lost; the receiver notices and
\* puts every chunk of the set it did not write back into the pool. The
\* receiver may end a slow session itself to rebalance (Close): the same
\* as a death it caused. A chunk that does not verify ends its session
\* (Resume.tla Bad). The receiver crashes at any time: every session ends,
\* its memory is gone, its disk loses any chunk written since the last
\* sync; on restart it verifies the disk (Resume.tla Open) and the pool is
\* every chunk that does not verify. Once the pool is empty and no session
\* is open, the final check commits (lane SPEED-RECV, nfltr
\* docs/decisions/send-line-rate-2026-11-21.md "Step 6"): when this run
\* verified on arrival and wrote every chunk itself (fresh) it trusts them,
\* reading nothing back; else it verifies the file on disk (Resume.tla
\* Finish). What else writes the disk: Rot, a chunk this run did not write
\* (one kept from before a crash) changed (a bad disk, another program),
\* and Touch, another program's write once the file is complete, which
\* changes its mtime (touched): the commit's recheck refuses it (Offer.tla
\* Commit). A refused commit keeps the stage; the next run verifies it
\* (Restart). Between a file's first write and its last in one run nothing
\* but the run writes it (README "Environment assumptions"). Chunk content is
\* the chunk's index (Resume.tla verifies values; here a bad chunk is a
\* negative index), so a chunk at the wrong offset does not verify.

EXTENDS Naturals, Integers, Sequences, FiniteSets

CONSTANTS C,         \* chunks of the file
          K,         \* sessions at once (connections)
          W,         \* unread chunks one session holds (its stream window)
          MaxFaults, \* deaths, closes, refused chunks and crashes in all
          Mutant     \* "none", or the planted failure (README "Stripe")

Chunks == 1..C
Conns == 1..K

VARIABLES disk,     \* disk[i]: the chunk written at offset i (0 none, negative a bad one)
          pool,     \* chunks to fetch that no session holds
          want,     \* want[k]: the chunks of session k's set the receiver has not read
          unsent,   \* unsent[k]: the chunks of session k's set its sender has not sent
          wire,     \* wire[k]: chunks sent on session k, not read, oldest first
          st,       \* st[k]: "idle" (no session) | "open" | "dead" (cut, not yet noticed)
          tree,     \* the receiver holds the file's tree (verified against the offer's root)
          phase,    \* the receiver: "run" | "down" (crashed) | "done" (committed)
          writes,   \* writes[i]: writes at offset i since the receiver last started
          logged,   \* chunks ever written (ghost; Mutant "resume-trust-log" restarts from it)
          fresh,    \* chunks this run verified on arrival and wrote (the receiver's memory)
          touched,  \* a write by another program changed the complete file's mtime
          ev        \* counts: deaths, closes, refusals, crashes, rot, touches

vars == <<disk, pool, want, unsent, wire, st, tree, phase, writes, logged, fresh, touched, ev>>

Events == {"die", "close", "refuse", "crash", "rot", "touch"}
Faults == ev.die + ev.close + ev.refuse + ev.crash + ev.rot + ev.touch
Bump(f) == [ev EXCEPT ![f] = @ + 1]
Min(S) == CHOOSE x \in S : \A y \in S : x <= y
Held(k) == st[k] \in {"open", "dead"}

TypeOK ==
    /\ disk \in [Chunks -> -C..C]
    /\ pool \subseteq Chunks
    /\ want \in [Conns -> SUBSET Chunks]
    /\ unsent \in [Conns -> SUBSET Chunks]
    /\ wire \in [Conns -> Seq(-C..C)]
    /\ st \in [Conns -> {"idle", "open", "dead"}]
    /\ tree \in BOOLEAN
    /\ phase \in {"run", "down", "done"}
    /\ writes \in [Chunks -> 0..3]
    /\ logged \subseteq Chunks
    /\ fresh \subseteq Chunks
    /\ touched \in BOOLEAN
    /\ ev \in [Events -> 0..MaxFaults]

Init ==
    /\ disk = [i \in Chunks |-> 0]
    /\ pool = Chunks
    /\ want = [k \in Conns |-> {}]
    /\ unsent = [k \in Conns |-> {}]
    /\ wire = [k \in Conns |-> <<>>]
    /\ st = [k \in Conns |-> "idle"]
    /\ tree = FALSE
    /\ phase = "run"
    /\ writes = [i \in Chunks |-> 0]
    /\ logged = {}
    /\ fresh = {}
    /\ touched = FALSE
    /\ ev = [f \in Events |-> 0]

\* Open: a session with a want for S, taken out of the pool at once (no
\* other session can be given a chunk of S while k holds it). The first
\* session's tree frames give the receiver the tree (Tree).
Open(k, S) ==
    /\ phase = "run" /\ st[k] = "idle"
    /\ S /= {} /\ S \subseteq pool
    /\ st' = [st EXCEPT ![k] = "open"]
    /\ want' = [want EXCEPT ![k] = S]
    /\ unsent' = [unsent EXCEPT ![k] = S]
    /\ pool' = pool \ S
    /\ UNCHANGED <<disk, wire, tree, phase, writes, logged, fresh, touched, ev>>

Tree ==
    /\ phase = "run" /\ ~tree /\ \E k \in Conns : st[k] = "open"
    /\ tree' = TRUE
    /\ UNCHANGED <<disk, pool, want, unsent, wire, st, phase, writes, logged, fresh, touched, ev>>

\* Send: the sender's next chunk of k's set, in index order, while the
\* stream window has room. Mutant "no-window": the window ignored.
Send(k) ==
    /\ st[k] = "open" /\ unsent[k] /= {}
    /\ Len(wire[k]) < W \/ Mutant = "no-window"
    /\ LET c == Min(unsent[k]) IN
         /\ wire' = [wire EXCEPT ![k] = Append(@, c)]
         /\ unsent' = [unsent EXCEPT ![k] = @ \ {c}]
    /\ UNCHANGED <<disk, pool, want, st, tree, phase, writes, logged, fresh, touched, ev>>

\* Corrupt: a chunk on the wire is not the offer's (a sender's file
\* changed, a bad disk there: Resume.tla Change, Rot).
Corrupt(k) ==
    /\ Faults < MaxFaults /\ wire[k] /= <<>> /\ Head(wire[k]) > 0
    /\ wire' = [wire EXCEPT ![k] = <<-Head(@)>> \o Tail(@)]
    /\ ev' = Bump("refuse")
    /\ UNCHANGED <<disk, pool, want, unsent, st, tree, phase, writes, logged, fresh, touched>>

\* End k's session from the receiver's side: every chunk of its set it
\* did not write goes back into the pool. Mutant "lost-on-death": only
\* those its sender had not sent (the sent ones taken as delivered).
Requeue(k) ==
    IF Mutant = "lost-on-death" THEN pool \cup unsent[k] ELSE pool \cup want[k]
EndSession(k) ==
    /\ st' = [st EXCEPT ![k] = "idle"]
    /\ pool' = Requeue(k)
    /\ want' = [want EXCEPT ![k] = {}]
    /\ unsent' = [unsent EXCEPT ![k] = {}]
    /\ wire' = [wire EXCEPT ![k] = <<>>]

\* Read: the next chunk on k's wire, verified at the offset k's want says
\* comes next (the smallest of the set not yet read) and written there.
\* One that does not verify ends the session (Resume.tla Bad). Mutant
\* "accept-unverified": written without verifying. The session ends
\* cleanly once its set is read.
Read(k) ==
    /\ phase = "run" /\ st[k] = "open" /\ tree /\ wire[k] /= <<>>
    /\ LET c == Head(wire[k])  at == Min(want[k]) IN
       IF c = at \/ Mutant = "accept-unverified"
       THEN /\ disk' = [disk EXCEPT ![at] = c]
            /\ writes' = [writes EXCEPT ![at] = @ + 1]
            /\ logged' = logged \cup {at}
            /\ fresh' = fresh \cup {at}
            /\ IF want[k] = {at}
               THEN /\ st' = [st EXCEPT ![k] = "idle"]
                    /\ want' = [want EXCEPT ![k] = {}]
               ELSE /\ want' = [want EXCEPT ![k] = @ \ {at}]
                    /\ UNCHANGED st
            /\ wire' = [wire EXCEPT ![k] = Tail(@)]
            /\ UNCHANGED <<pool, unsent>>
       ELSE /\ EndSession(k)
            /\ UNCHANGED <<disk, writes, logged, fresh>>
    /\ UNCHANGED <<tree, phase, touched, ev>>

\* Die: k's connection is cut; what was on its wire is lost, its sender
\* sends nothing more. Notice: the receiver's read fails; it ends k.
Die(k) ==
    /\ Faults < MaxFaults /\ phase = "run" /\ st[k] = "open"
    /\ st' = [st EXCEPT ![k] = "dead"]
    /\ wire' = [wire EXCEPT ![k] = <<>>]
    /\ ev' = Bump("die")
    /\ UNCHANGED <<disk, pool, want, unsent, tree, phase, writes, logged, fresh, touched>>

Notice(k) ==
    /\ phase = "run" /\ st[k] = "dead"
    /\ EndSession(k)
    /\ UNCHANGED <<disk, tree, phase, writes, logged, fresh, touched, ev>>

\* Close: the receiver ends a session that is still sending (to give its
\* set to sessions that finished: the slow tail) and stops reading it.
\* Mutant "steal-live": its set put back into the pool while the session
\* still sends it.
Close(k) ==
    /\ Faults < MaxFaults /\ phase = "run" /\ st[k] = "open" /\ want[k] /= {}
    /\ ev' = Bump("close")
    /\ IF Mutant = "steal-live"
       THEN /\ pool' = pool \cup want[k]
            /\ UNCHANGED <<want, unsent, wire, st>>
       ELSE EndSession(k)
    /\ UNCHANGED <<disk, tree, phase, writes, logged, fresh, touched>>

\* Crash: the receiver dies. Its sessions end (their connections with
\* it), its memory goes, its disk loses any chunk written since the last
\* sync (Resume.tla Crash; a torn chunk is one that does not verify, here
\* one lost) and possibly the kept tree.
Crash ==
    /\ Faults < MaxFaults /\ phase = "run"
    /\ \E lost \in SUBSET {i \in Chunks : disk[i] /= 0}, keep \in BOOLEAN :
         /\ disk' = [i \in Chunks |-> IF i \in lost THEN 0 ELSE disk[i]]
         /\ tree' = (tree /\ keep)
    /\ phase' = "down"
    /\ pool' = {}
    /\ want' = [k \in Conns |-> {}]
    /\ unsent' = [k \in Conns |-> {}]
    /\ wire' = [k \in Conns |-> <<>>]
    /\ st' = [k \in Conns |-> "idle"]
    /\ fresh' = IF Mutant = "fresh-across-runs" THEN fresh ELSE {}
    /\ ev' = Bump("crash")
    /\ UNCHANGED <<writes, logged, touched>>

\* Restart: the next receive verifies the stage on disk (Resume.tla Open):
\* the pool is every chunk that does not verify. Mutant
\* "resume-trust-log": the pool is the chunks never written by an
\* earlier run (a progress record trusted, the crash's losses skipped);
\* "fresh-across-runs": the fresh chunks kept over a crash and not fetched
\* again (the crash's losses missing, then trusted at Finish).
Restart ==
    /\ phase = "down"
    /\ phase' = "run"
    /\ pool' = CASE Mutant = "resume-trust-log" -> Chunks \ logged
                  [] Mutant = "fresh-across-runs" -> {i \in Chunks : disk[i] /= i} \ fresh
                  [] OTHER -> {i \in Chunks : disk[i] /= i}
    /\ writes' = [i \in Chunks |-> 0]
    /\ fresh' = IF Mutant = "fresh-across-runs" THEN fresh ELSE {}
    /\ touched' = FALSE
    /\ UNCHANGED <<disk, want, unsent, wire, st, tree, logged, ev>>

\* Finish: nothing left to fetch and no session open: the final check.
\* Every chunk fresh: trusted, nothing read back; else the file on disk
\* verified (Resume.tla Finish). Then the recheck: a touched file refuses.
\* A refusal keeps the stage (Resume.tla Finish, Offer.tla Commit): the
\* receive ends, the next verifies the disk (Restart). Mutants:
\* "commit-early" (commit once the pool is empty, a session still open,
\* nothing checked), "trust-resumed" (a chunk kept from an earlier run
\* trusted as if this run had verified it: the check skipped when every
\* chunk is on disk), "no-recheck" (a touched file committed).
Trusted == IF Mutant = "trust-resumed" THEN \A i \in Chunks : i \in fresh \/ disk[i] /= 0
           ELSE fresh = Chunks
Finish ==
    /\ phase = "run" /\ pool = {}
    /\ (\A k \in Conns : st[k] = "idle") \/ Mutant = "commit-early"
    /\ IF \/ Mutant = "commit-early"
          \/ /\ Trusted \/ \A i \in Chunks : disk[i] = i
             /\ ~touched \/ Mutant = "no-recheck"
       THEN phase' = "done"
       ELSE phase' = "down"
    /\ UNCHANGED <<disk, pool, want, unsent, wire, st, tree, writes, logged, fresh, touched, ev>>

\* Rot: a chunk on disk that this run did not write is not the offer's (a
\* bad disk, another program; one kept from an earlier run).
Rot ==
    /\ Faults < MaxFaults /\ phase = "run"
    /\ \E i \in Chunks \ fresh : disk[i] /= 0 /\ disk' = [disk EXCEPT ![i] = -i]
    /\ ev' = Bump("rot")
    /\ UNCHANGED <<pool, want, unsent, wire, st, tree, phase, writes, logged, fresh, touched>>

\* Touch: another program writes the file once it is complete (no chunk
\* left to fetch or being fetched): its mtime changes (README
\* "Environment assumptions").
Touch ==
    /\ Faults < MaxFaults /\ phase = "run" /\ pool = {} /\ \A k \in Conns : want[k] = {}
    /\ \E i \in Chunks : disk' = [disk EXCEPT ![i] = -i]
    /\ touched' = TRUE
    /\ ev' = Bump("touch")
    /\ UNCHANGED <<pool, want, unsent, wire, st, tree, phase, writes, logged, fresh>>

\* Over: the transfer committed; nothing more happens.
Over == phase = "done" /\ UNCHANGED vars

Next ==
    \/ \E k \in Conns, S \in SUBSET Chunks : Open(k, S)
    \/ Tree
    \/ \E k \in Conns : Send(k) \/ Corrupt(k) \/ Read(k) \/ Die(k) \/ Notice(k) \/ Close(k)
    \/ Crash \/ Restart \/ Finish \/ Rot \/ Touch \/ Over

Spec == Init /\ [][Next]_vars
    /\ WF_vars(\E k \in Conns, S \in SUBSET Chunks : Open(k, S))
    /\ WF_vars(Tree)
    /\ \A k \in Conns : WF_vars(Send(k)) /\ WF_vars(Read(k)) /\ WF_vars(Notice(k))
    /\ WF_vars(Restart) /\ WF_vars(Finish)

\* --- invariants ---

\* Right: every chunk this run verified and wrote is the offer's chunk for
\* its offset, unless another program touched the file since.
Right == touched \/ \A i \in fresh : disk[i] = i

\* Once: no offset is written twice in one run (no chunk fetched twice).
Once == \A i \in Chunks : writes[i] <= 1

\* Accounted: while running, every chunk is on disk, in the pool, or in
\* the set of a session the receiver holds; the pool and the sets are
\* disjoint (one holder per chunk).
Accounted ==
    phase = "run" =>
      /\ \A i \in Chunks : disk[i] /= 0 \/ i \in pool \/ \E k \in Conns : Held(k) /\ i \in want[k]
      /\ \A k \in Conns : want[k] \cap pool = {}
      /\ \A j, k \in Conns : j /= k => want[j] \cap want[k] = {}

\* Bounded: a session holds at most W unread chunks; the receiver at most
\* K sessions' (no reassembly buffer).
Bounded == \A k \in Conns : Len(wire[k]) <= W

\* Safe: a commit holds the offer's whole file.
Safe == phase = "done" => \A i \in Chunks : disk[i] = i

\* Done: with finitely many faults, the file commits.
Done == <>(phase = "done")

\* --- witnesses (each must be violated: the behaviour is reachable) ---

WitnessParallel == ~(\A k \in Conns : wire[k] /= <<>>)
WitnessDiedThenCommit == ~(phase = "done" /\ ev.die >= 1)
WitnessClosedThenCommit == ~(phase = "done" /\ ev.close >= 1)
WitnessRefusedThenCommit == ~(phase = "done" /\ ev.refuse >= 1)
WitnessResumedCommit == ~(phase = "done" /\ ev.crash >= 1 /\ \E i \in Chunks : writes[i] = 0)
\* a commit that read nothing back (every chunk fresh)
WitnessTrustedCommit == ~(phase = "done" /\ fresh = Chunks)
\* a rotted kept chunk caught by the check, then committed
WitnessRotThenCommit == ~(phase = "done" /\ ev.rot >= 1)
\* a touched file refused by the recheck, then committed
WitnessTouchedThenCommit == ~(phase = "done" /\ ev.touch >= 1)
=============================================================================
