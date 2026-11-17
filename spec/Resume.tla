---------------------------- MODULE Resume ----------------------------
\* Copyright 2026 nfltr
\* SPDX-License-Identifier: Apache-2.0
\*
\* One file of an nfltr-send transfer, receiver side, across sessions that
\* end at any point and receiver crashes that lose or tear the tail of what
\* it wrote (spec/README.md). A session opens with the receiver's partial
\* file as its claim; the sender resumes at its length only when that
\* partial is a prefix of its file (it hashes its own prefix and compares:
\* the prefix digest is modelled as the bytes themselves, an ideal hash),
\* else at 0. Each session appends the sender's bytes; at the end the
\* sender's digest of the whole file is compared with the receiver's, and
\* only an equal file is committed (renamed into place); an unequal one is
\* discarded. The sender's file may change while no session is open (a
\* restarted sender), never during one (README, assumptions).

EXTENDS Naturals, Sequences

CONSTANTS N,        \* the file's length (in chunks)
          Vals,     \* a chunk's possible contents
          MaxFaults, \* session ends, crashes and source changes, together
          Mutant     \* "none", or the planted failure (README "Mutants")

VARIABLES src,       \* the sender's file
          part,      \* the receiver's partial file, durable
          sess,      \* "none" | "open"
          committed, \* the committed file, <<>> before
          faults,    \* faults so far
          resumed    \* a session resumed at a non-zero offset

vars == <<src, part, sess, committed, faults, resumed>>

Files == [1..N -> Vals]
Seqs  == UNION {[1..k -> Vals] : k \in 0..N}

IsPrefix(p, s) == Len(p) <= Len(s) /\ SubSeq(s, 1, Len(p)) = p

TypeOK ==
    /\ src \in Files
    /\ part \in Seqs
    /\ sess \in {"none", "open"}
    /\ committed \in Seqs
    /\ faults \in 0..MaxFaults
    /\ resumed \in BOOLEAN

Init ==
    /\ src \in Files
    /\ part = <<>>
    /\ sess = "none"
    /\ committed = <<>>
    /\ faults = 0
    /\ resumed = FALSE

\* Open: the receiver's claim is its partial file; the sender answers the
\* offset it resumes at and the receiver truncates to it.
Open ==
    /\ sess = "none" /\ committed = <<>>
    /\ LET off == IF IsPrefix(part, src) \/ Mutant \in {"trust-partial", "no-checks"} THEN Len(part) ELSE 0
       IN /\ part' = SubSeq(part, 1, off)
          /\ resumed' = (resumed \/ off > 0)
    /\ sess' = "open"
    /\ UNCHANGED <<src, committed, faults>>

\* Send: the next chunk of the sender's file, written at the end of part.
Send ==
    /\ sess = "open" /\ Len(part) < N
    /\ part' = Append(part, src[Len(part) + 1])
    /\ UNCHANGED <<src, sess, committed, faults, resumed>>

\* Finish: the sender's digest of the file it read against the receiver's
\* digest of the file it wrote, read back from its disk.
Finish ==
    /\ sess = "open" /\ Len(part) = N
    /\ IF part = src \/ Mutant \in {"no-checks", "no-final-check"}
          THEN /\ committed' = part /\ UNCHANGED part
          ELSE /\ part' = IF Mutant = "trust-partial" THEN part ELSE <<>>
               /\ UNCHANGED committed
    /\ sess' = "none"
    /\ UNCHANGED <<src, faults, resumed>>

\* CleanEnd (mutant "commit-cut" only): a session that ends cleanly
\* between chunks taken as the transfer's end, committed as it is.
CleanEnd ==
    /\ Mutant = "commit-cut"
    /\ sess = "open" /\ 0 < Len(part) /\ Len(part) < N
    /\ committed' = part /\ sess' = "none"
    /\ UNCHANGED <<src, part, faults, resumed>>

\* Cut: the session ends (the network, either process): part stays.
Cut ==
    /\ sess = "open" /\ faults < MaxFaults
    /\ sess' = "none" /\ faults' = faults + 1
    /\ UNCHANGED <<src, part, committed, resumed>>

\* Crash: the receiver dies; what it wrote last may be lost or torn (any
\* shorter prefix, or a prefix whose last chunk holds other bytes).
Crash ==
    /\ committed = <<>> /\ faults < MaxFaults
    /\ \E k \in 0..Len(part) :
        \/ part' = SubSeq(part, 1, k)
        \/ k > 0 /\ \E v \in Vals : part' = Append(SubSeq(part, 1, k - 1), v)
    /\ sess' = "none" /\ faults' = faults + 1
    /\ UNCHANGED <<src, committed, resumed>>

\* Rot: a chunk on the receiver's disk is not what it was sent (a write
\* at a wrong offset, a torn write, a bad disk, another program), at any
\* time before the commit: in a session only the final digest, over what
\* is on disk, sees it; between sessions the next one's prefix check.
Rot ==
    /\ committed = <<>> /\ Len(part) > 0 /\ faults < MaxFaults
    /\ \E i \in 1..Len(part), v \in Vals : v # part[i] /\ part' = [part EXCEPT ![i] = v]
    /\ faults' = faults + 1
    /\ UNCHANGED <<src, sess, committed, resumed>>

\* Change: the sender restarted with another file, between sessions.
Change ==
    /\ sess = "none" /\ committed = <<>> /\ faults < MaxFaults
    /\ src' \in Files /\ faults' = faults + 1
    /\ UNCHANGED <<part, sess, committed, resumed>>

\* Served: the transfer is committed; nothing more happens to this file.
Served == committed # <<>> /\ UNCHANGED vars

Next == Open \/ Send \/ Finish \/ CleanEnd \/ Cut \/ Crash \/ Rot \/ Change \/ Served

Spec == Init /\ [][Next]_vars /\ WF_vars(Open) /\ WF_vars(Send) /\ WF_vars(Finish)

\* Safe: only the sender's file is ever committed, whole.
Safe == committed # <<>> => committed = src

\* Done: with finitely many faults, every transfer commits.
Done == <>(committed # <<>>)

\* Witnesses (each must be violated: the behaviour is reachable).
WitnessResumedCommit == ~(committed # <<>> /\ resumed)
WitnessChangedThenCommit == ~(committed # <<>> /\ faults = MaxFaults)
WitnessDiscarded == ~(sess = "none" /\ part = <<>> /\ faults > 0 /\ resumed)
=============================================================================
