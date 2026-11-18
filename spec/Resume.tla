---------------------------- MODULE Resume ----------------------------
\* Copyright 2026 nfltr
\* SPDX-License-Identifier: Apache-2.0
\*
\* One file of an nfltr-send transfer, receiver side, across sessions that
\* end at any point, receiver crashes that lose or tear the tail of what it
\* wrote, chunks on its disk that are not what was sent (Rot), and a
\* sender's file that changes (spec/README.md). The offer pins the file's
\* BLAKE3 root; the root is modelled as an ideal hash tree: it fixes every
\* chunk, so a chunk verifies at position i exactly when it is the offered
\* file's chunk there. The receiver verifies each chunk on arrival against
\* the root and writes only one that verifies; a chunk that does not is
\* refused at once and the session ends. A session opens with the receiver
\* verifying its part chunk by chunk (with the tree it kept) and resuming
\* after a verified prefix of it; at the end it verifies the whole file on
\* its disk and commits it only when every chunk verifies, else keeps the
\* verified prefix for the next session.

EXTENDS Naturals, Sequences

CONSTANTS N,         \* the file's length (in chunks)
          Vals,      \* a chunk's possible contents
          MaxFaults, \* session ends, crashes, rot and source changes, together
          Mutant     \* "none", or the planted failure (README "Mutants")

VARIABLES offer,     \* the file the offer pinned (its root)
          src,       \* the sender's file now
          part,      \* the receiver's partial file, durable
          sess,      \* "none" | "open"
          committed, \* the committed file, <<>> before
          faults,    \* faults so far
          resumed,   \* a session resumed at a non-zero offset
          refused    \* a chunk was refused

vars == <<offer, src, part, sess, committed, faults, resumed, refused>>

Files == [1..N -> Vals]
Seqs  == UNION {[1..k -> Vals] : k \in 0..N}

IsPrefix(p, s) == Len(p) <= Len(s) /\ SubSeq(s, 1, Len(p)) = p

\* The root a chunk is verified against: the pinned offer's (mutant
\* "root-unbound": the one of the file the sender has now).
Root == IF Mutant = "root-unbound" THEN src ELSE offer
Verified(i, v) == Root[i] = v

\* VLen(p, 0): the length of p's longest prefix whose chunks all verify.
RECURSIVE VLen(_, _)
VLen(p, k) == IF k < Len(p) /\ Verified(k + 1, p[k + 1]) THEN VLen(p, k + 1) ELSE k

TypeOK ==
    /\ offer \in Files
    /\ src \in Files
    /\ part \in Seqs
    /\ sess \in {"none", "open"}
    /\ committed \in Seqs
    /\ faults \in 0..MaxFaults
    /\ resumed \in BOOLEAN
    /\ refused \in BOOLEAN

Init ==
    /\ offer \in Files
    /\ src = offer
    /\ part = <<>>
    /\ sess = "none"
    /\ committed = <<>>
    /\ faults = 0
    /\ resumed = FALSE
    /\ refused = FALSE

\* Open: the receiver verifies its part and resumes after a verified prefix
\* of it (the longest, or less: a tree it did not keep); it asks the sender
\* for the rest (mutant "resume-unverified": after the whole part).
Open ==
    /\ sess = "none" /\ committed = <<>>
    /\ \E off \in IF Mutant = "resume-unverified" THEN {Len(part)} ELSE 0..VLen(part, 0) :
          /\ part' = SubSeq(part, 1, off)
          /\ resumed' = (resumed \/ off > 0)
    /\ sess' = "open"
    /\ UNCHANGED <<offer, src, committed, faults, refused>>

\* Send: the sender's next chunk, verified, written at the end of part
\* (mutant "accept-unverified": written unverified).
Send ==
    /\ sess = "open" /\ Len(part) < N
    /\ Verified(Len(part) + 1, src[Len(part) + 1]) \/ Mutant = "accept-unverified"
    /\ part' = Append(part, src[Len(part) + 1])
    /\ UNCHANGED <<offer, src, sess, committed, faults, resumed, refused>>

\* Bad: the sender's next chunk does not verify: refused at once, not
\* written; the session ends, the part stays.
Bad ==
    /\ sess = "open" /\ Len(part) < N /\ Mutant # "accept-unverified"
    /\ ~Verified(Len(part) + 1, src[Len(part) + 1])
    /\ sess' = "none" /\ refused' = TRUE
    /\ UNCHANGED <<offer, src, part, committed, faults, resumed>>

\* Finish: the whole file on the receiver's disk verified; committed when
\* every chunk verifies (mutant "no-final-check": unchecked), else cut to
\* its verified prefix.
Finish ==
    /\ sess = "open" /\ Len(part) = N
    /\ IF VLen(part, 0) = N \/ Mutant = "no-final-check"
          THEN /\ committed' = part /\ UNCHANGED part
          ELSE /\ part' = SubSeq(part, 1, VLen(part, 0))
               /\ UNCHANGED committed
    /\ sess' = "none"
    /\ UNCHANGED <<offer, src, faults, resumed, refused>>

\* CleanEnd (mutant "commit-cut" only): a session that ends cleanly
\* between chunks taken as the transfer's end, committed as it is.
CleanEnd ==
    /\ Mutant = "commit-cut"
    /\ sess = "open" /\ 0 < Len(part) /\ Len(part) < N
    /\ committed' = part /\ sess' = "none"
    /\ UNCHANGED <<offer, src, part, faults, resumed, refused>>

\* Cut: the session ends (the network, either process): part stays.
Cut ==
    /\ sess = "open" /\ faults < MaxFaults
    /\ sess' = "none" /\ faults' = faults + 1
    /\ UNCHANGED <<offer, src, part, committed, resumed, refused>>

\* Crash: the receiver dies; what it wrote last may be lost or torn (any
\* shorter prefix, or a prefix whose last chunk holds other bytes).
Crash ==
    /\ committed = <<>> /\ faults < MaxFaults
    /\ \E k \in 0..Len(part) :
        \/ part' = SubSeq(part, 1, k)
        \/ k > 0 /\ \E v \in Vals : part' = Append(SubSeq(part, 1, k - 1), v)
    /\ sess' = "none" /\ faults' = faults + 1
    /\ UNCHANGED <<offer, src, committed, resumed, refused>>

\* Rot: a chunk on the receiver's disk is not what was written (a write
\* that landed wrong, a bad disk, another program), at any time before the
\* commit.
Rot ==
    /\ committed = <<>> /\ Len(part) > 0 /\ faults < MaxFaults
    /\ \E i \in 1..Len(part), v \in Vals : v # part[i] /\ part' = [part EXCEPT ![i] = v]
    /\ faults' = faults + 1
    /\ UNCHANGED <<offer, src, sess, committed, resumed, refused>>

\* Change: the sender's file is rewritten (between sessions or during
\* one); the offer is not.
Change ==
    /\ committed = <<>> /\ faults < MaxFaults
    /\ src' \in Files \ {src} /\ faults' = faults + 1
    /\ UNCHANGED <<offer, part, sess, committed, resumed, refused>>

\* Served: the transfer is committed; nothing more happens to this file.
Served == committed # <<>> /\ UNCHANGED vars

Next == Open \/ Send \/ Bad \/ Finish \/ CleanEnd \/ Cut \/ Crash \/ Rot \/ Change \/ Served

Spec == Init /\ [][Next]_vars /\ WF_vars(Open) /\ WF_vars(Send) /\ WF_vars(Bad) /\ WF_vars(Finish)

\* Safe: only the offered file is ever committed, whole.
Safe == committed # <<>> => committed = offer

\* Kept: a chunk the receiver writes is the offer's (action property).
Kept == [][Len(part') > Len(part) => part'[Len(part')] = offer[Len(part')]]_vars

\* Resumed: a session resumes only after chunks that are the offer's.
Resumed == [][sess = "none" /\ sess' = "open" => IsPrefix(part', offer)]_vars

\* Done: with finitely many faults, the transfer commits, or the sender's
\* file is no longer the offered one.
Done == <>(committed # <<>>) \/ <>[](src # offer)

\* Witnesses (each must be violated: the behaviour is reachable).
WitnessResumedCommit == ~(committed # <<>> /\ resumed)
WitnessChangedThenCommit == ~(committed # <<>> /\ faults = MaxFaults)
WitnessDiscarded == ~(sess = "none" /\ part = <<>> /\ faults > 0 /\ resumed)
WitnessRefusedThenCommit == ~(committed # <<>> /\ refused)
=============================================================================
