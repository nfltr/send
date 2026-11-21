----------------------------- MODULE Offer -----------------------------
\* Copyright 2026 nfltr
\* SPDX-License-Identifier: Apache-2.0
\*
\* An offer of F files, one session after another, to its one commit
\* (spec/README.md "Offer.tla"; lane SENDMULTI). Resume.tla is one file: its
\* Finish commits the file at its final check. An offer commits once, after
\* its last file: an earlier file can change after its check and before
\* that commit, on the sender's disk (Change) or in the receiver's stage
\* (Touch, another program). Here a file is a version: the offered one is
\* 0, each write makes the next (a write changes the file's size or mtime,
\* the version checks compare: README "Environment assumptions"). Sending a
\* file is one step, Resume.tla's whole session of it: the sender checks
\* its file at its end, the receiver verifies what landed (so the stage
\* holds the offered version, or the session ends: a Cut here). After the
\* last file the sender checks every file again before its done frame
\* (Done), and the receiver checks every staged file again before it
\* commits (Commit). Mutants: "no-sender-recheck" (the done frame after
\* the per-file checks only, the design before SENDMULTI) and
\* "no-receiver-recheck" (the commit after the per-file verifies only).

EXTENDS Integers

CONSTANTS F,         \* the offer's files
          MaxFaults, \* changes, touches and cuts, together
          Mutant     \* "none", "no-sender-recheck", "no-receiver-recheck"

VARIABLES src,       \* the sender's files now: a version each
          stage,     \* the receiver's staged files: a version each, -1 none
          next,      \* the file the session sends next; F + 1 after the last
          state,     \* "sending" | "done" | "committed" | "changed" | "refused"
          sent,      \* the sender's files at its done frame, <<>> before
          committed, \* the committed files, <<>> before
          faults

vars == <<src, stage, next, state, sent, committed, faults>>

Files == 1..F
Offered == [f \in Files |-> 0]
Ends == {"committed", "changed", "refused"}

TypeOK ==
    /\ src \in [Files -> 0..MaxFaults]
    /\ stage \in [Files -> -1..MaxFaults]
    /\ next \in 1..F + 1
    /\ state \in {"sending", "done"} \cup Ends
    /\ sent \in {<<>>} \cup [Files -> 0..MaxFaults]
    /\ committed \in {<<>>} \cup [Files -> -1..MaxFaults]
    /\ faults \in 0..MaxFaults

Init ==
    /\ src = Offered
    /\ stage = [f \in Files |-> -1]
    /\ next = 1
    /\ state = "sending"
    /\ sent = <<>>
    /\ committed = <<>>
    /\ faults = 0

\* SendFile: the session's next file; the sender's check at its end
\* (ErrChanged), the receiver's verify of what landed (Resume.tla Finish).
SendFile ==
    /\ state = "sending" /\ next <= F
    /\ IF src[next] = Offered[next]
          THEN /\ stage' = [stage EXCEPT ![next] = Offered[next]]
               /\ next' = next + 1
               /\ UNCHANGED state
          ELSE /\ state' = "changed"
               /\ UNCHANGED <<stage, next>>
    /\ UNCHANGED <<src, sent, committed, faults>>

\* Done: after the last file the sender checks every file again, then
\* writes its done frame (mutant "no-sender-recheck": none checked).
Done ==
    /\ state = "sending" /\ next = F + 1
    /\ IF src = Offered \/ Mutant = "no-sender-recheck"
          THEN state' = "done" /\ sent' = src
          ELSE state' = "changed" /\ UNCHANGED sent
    /\ UNCHANGED <<src, stage, next, committed, faults>>

\* Commit: the receiver checks every staged file again, then commits
\* (Commit.tla's commit, one step here); a file that changed in the stage
\* refuses the commit, the stage kept (mutant "no-receiver-recheck": none
\* checked).
Commit ==
    /\ state = "done"
    /\ IF stage = Offered \/ Mutant = "no-receiver-recheck"
          THEN state' = "committed" /\ committed' = stage
          ELSE state' = "refused" /\ UNCHANGED committed
    /\ UNCHANGED <<src, stage, next, sent, faults>>

\* Change: a sender's file is written, at any time before the commit.
Change ==
    /\ state \in {"sending", "done"} /\ faults < MaxFaults
    /\ \E f \in Files : src' = [src EXCEPT ![f] = src[f] + 1]
    /\ faults' = faults + 1
    /\ UNCHANGED <<stage, next, state, sent, committed>>

\* Touch: a staged file the receiver verified is written by another
\* program, at any time before the commit.
Touch ==
    /\ state \in {"sending", "done"} /\ faults < MaxFaults
    /\ \E f \in Files : stage[f] >= 0 /\ stage' = [stage EXCEPT ![f] = stage[f] + 1]
    /\ faults' = faults + 1
    /\ UNCHANGED <<src, next, state, sent, committed>>

\* Cut: the session ends before its done frame; the next one sends every
\* file again from the first (each kept part verified, Resume.tla Open).
Cut ==
    /\ state = "sending" /\ faults < MaxFaults
    /\ next' = 1 /\ faults' = faults + 1
    /\ UNCHANGED <<src, stage, state, sent, committed>>

Over == state \in Ends /\ UNCHANGED vars

Next == SendFile \/ Done \/ Commit \/ Change \/ Touch \/ Cut \/ Over

Spec == Init /\ [][Next]_vars /\ WF_vars(SendFile) /\ WF_vars(Done) /\ WF_vars(Commit)

\* Safe: every committed file is the offered one (what was sent).
Safe == state = "committed" => committed = Offered

\* Current: at its done frame, the sender's last word before the commit,
\* every one of its files is still the offered one. (A write between that
\* frame and the commit, one frame's flight, is after the transfer:
\* WitnessChangedAfterDone.)
Current == state \in {"done", "committed"} => sent = Offered

\* Ended: the offer commits, or fails typed (the sender's ErrChanged, the
\* receiver's refused commit).
Ended == <>(state \in Ends)

\* Witnesses (each must be violated: the behaviour is reachable).
\* WitnessEarlierChanged: an earlier file changed after its check, caught
\* by the recheck before the done frame.
WitnessEarlierChanged == ~(state = "changed" /\ next = F + 1)
\* WitnessEarlierTouched: a staged file changed after its verify, caught
\* by the recheck before the commit.
WitnessEarlierTouched == ~(state = "refused")
WitnessCutThenCommit == ~(state = "committed" /\ faults > 0)
WitnessChangedAfterDone == ~(state = "committed" /\ src # Offered)
=============================================================================
