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
\* verifying its part chunk by chunk (with the tree it kept) and asking the
\* sender for the chunks that are missing or do not verify, every other
\* chunk kept (a resume set, not a prefix); at the end it verifies the
\* whole file on its disk and commits it only when every chunk verifies,
\* else keeps it for the next session to verify again.

EXTENDS Naturals, Sequences, FiniteSets

CONSTANTS N,         \* the file's length (in chunks)
          Vals,      \* a chunk's possible contents
          Hole,      \* no chunk there (never written, or lost)
          MaxFaults, \* session ends, crashes, rot and source changes, together
          Mutant     \* "none", or the planted failure (README "Mutants")

VARIABLES offer,     \* the file the offer pinned (its root)
          src,       \* the sender's file now
          part,      \* the receiver's partial file, durable: a chunk or Hole per position
          need,      \* the chunks the open session asks the sender for
          sess,      \* "none" | "open"
          committed, \* the committed file, <<>> before
          faults,    \* faults so far
          resumed,   \* a session resumed with a chunk kept
          refused    \* a chunk was refused

vars == <<offer, src, part, need, sess, committed, faults, resumed, refused>>

Pos   == 1..N
Files == [Pos -> Vals]
Parts == [Pos -> Vals \cup {Hole}]
Empty == [i \in Pos |-> Hole]

\* The root a chunk is verified against: the pinned offer's (mutant
\* "root-unbound": the one of the file the sender has now).
Root == IF Mutant = "root-unbound" THEN src ELSE offer
Verified(i, v) == v # Hole /\ Root[i] = v

\* Good(p): the positions of p whose chunk verifies.
Good(p) == {i \in Pos : Verified(i, p[i])}
Min(S) == CHOOSE i \in S : \A j \in S : i <= j
Max(S) == CHOOSE i \in S : \A j \in S : i >= j

\* Ask(p): what a session opening on p asks for: every chunk missing or not
\* verifying (mutants: "resume-unverified" keeps a chunk that is there but
\* does not verify; "skip-missing" leaves out the last chunk it should ask
\* for; "resend-verified" asks for everything from the first such chunk on,
\* as a prefix resume does).
Ask(p) ==
    LET bad == Pos \ Good(p) IN
    CASE Mutant = "resume-unverified" -> {i \in Pos : p[i] = Hole}
      [] Mutant = "skip-missing" /\ bad # {} -> bad \ {Max(bad)}
      [] Mutant = "resend-verified" /\ bad # {} -> {i \in Pos : i >= Min(bad)}
      [] OTHER -> bad

TypeOK ==
    /\ offer \in Files
    /\ src \in Files
    /\ part \in Parts
    /\ need \subseteq Pos
    /\ sess \in {"none", "open"}
    /\ committed \in {<<>>} \cup Parts
    /\ faults \in 0..MaxFaults
    /\ resumed \in BOOLEAN
    /\ refused \in BOOLEAN

Init ==
    /\ offer \in Files
    /\ src = offer
    /\ part = Empty
    /\ need = {}
    /\ sess = "none"
    /\ committed = <<>>
    /\ faults = 0
    /\ resumed = FALSE
    /\ refused = FALSE

\* Open: the receiver verifies its part and asks for what it lacks, or, with
\* no tree kept, for the whole file.
Open ==
    /\ sess = "none" /\ committed = <<>>
    /\ \E ask \in {Ask(part), Pos} :
          /\ need' = ask
          /\ resumed' = (resumed \/ ask # Pos)
    /\ sess' = "open"
    /\ UNCHANGED <<offer, src, part, committed, faults, refused>>

\* Send: the sender's next asked chunk, verified, written at its position
\* (mutant "accept-unverified": written unverified).
Send ==
    /\ sess = "open" /\ need # {}
    /\ LET i == Min(need) IN
          /\ Verified(i, src[i]) \/ Mutant = "accept-unverified"
          /\ part' = [part EXCEPT ![i] = src[i]]
          /\ need' = need \ {i}
    /\ UNCHANGED <<offer, src, sess, committed, faults, resumed, refused>>

\* Bad: the sender's next asked chunk does not verify: refused at once, not
\* written; the session ends, the part stays.
Bad ==
    /\ sess = "open" /\ need # {} /\ Mutant # "accept-unverified"
    /\ ~Verified(Min(need), src[Min(need)])
    /\ sess' = "none" /\ refused' = TRUE
    /\ UNCHANGED <<offer, src, part, need, committed, faults, resumed>>

\* Finish: every asked chunk arrived; the whole file on the receiver's disk
\* verified, committed when every chunk verifies (mutant "no-final-check":
\* unchecked), else kept for the next session to verify again.
Finish ==
    /\ sess = "open" /\ need = {}
    /\ IF Good(part) = Pos \/ Mutant = "no-final-check"
          THEN committed' = part
          ELSE UNCHANGED committed
    /\ sess' = "none"
    /\ UNCHANGED <<offer, src, part, need, faults, resumed, refused>>

\* CleanEnd (mutant "commit-cut" only): a session that ends cleanly
\* between chunks taken as the transfer's end, committed as it is.
CleanEnd ==
    /\ Mutant = "commit-cut"
    /\ sess = "open" /\ need # {} /\ need # Pos
    /\ committed' = part /\ sess' = "none"
    /\ UNCHANGED <<offer, src, part, need, faults, resumed, refused>>

\* Cut: the session ends (the network, either process): part stays.
Cut ==
    /\ sess = "open" /\ faults < MaxFaults
    /\ sess' = "none" /\ faults' = faults + 1
    /\ UNCHANGED <<offer, src, part, need, committed, resumed, refused>>

\* Crash: the receiver dies; what it wrote but had not synced may be lost
\* (any chunks) or torn (one chunk holds other bytes).
Crash ==
    /\ committed = <<>> /\ faults < MaxFaults
    /\ \E lost \in SUBSET Pos, t \in Pos, v \in Vals \cup {Hole} :
          part' = [i \in Pos |-> IF i = t THEN v ELSE IF i \in lost THEN Hole ELSE part[i]]
    /\ sess' = "none" /\ faults' = faults + 1
    /\ UNCHANGED <<offer, src, need, committed, resumed, refused>>

\* Rot: a chunk on the receiver's disk is not what was written (a write
\* that landed wrong, a bad disk, another program), at any time before the
\* commit.
Rot ==
    /\ committed = <<>> /\ faults < MaxFaults
    /\ \E i \in Pos, v \in Vals : part[i] # Hole /\ v # part[i] /\ part' = [part EXCEPT ![i] = v]
    /\ faults' = faults + 1
    /\ UNCHANGED <<offer, src, need, sess, committed, resumed, refused>>

\* Change: the sender's file is rewritten (between sessions or during
\* one); the offer is not.
Change ==
    /\ committed = <<>> /\ faults < MaxFaults
    /\ src' \in Files \ {src} /\ faults' = faults + 1
    /\ UNCHANGED <<offer, part, need, sess, committed, resumed, refused>>

\* Served: the transfer is committed; nothing more happens to this file.
Served == committed # <<>> /\ UNCHANGED vars

Next == Open \/ Send \/ Bad \/ Finish \/ CleanEnd \/ Cut \/ Crash \/ Rot \/ Change \/ Served

Spec == Init /\ [][Next]_vars /\ WF_vars(Open) /\ WF_vars(Send) /\ WF_vars(Bad) /\ WF_vars(Finish)

\* Safe: only the offered file is ever committed, whole.
Safe == committed # <<>> => committed = offer

\* Kept: a chunk the receiver writes is the offer's (action property).
Kept == [][sess = "open" /\ sess' = "open" /\ need' # need => \A i \in need \ need' : part'[i] = offer[i]]_vars

\* Resumed: a session keeps only chunks that are the offer's: every chunk
\* it does not ask for is there and verifies (action property).
Resumed == [][sess = "none" /\ sess' = "open" => \A i \in Pos \ need' : part'[i] = offer[i]]_vars

\* Minimal: a session resuming a part asks for no chunk that verifies
\* (action property; with no tree kept it asks for the whole file).
Minimal == [][sess = "none" /\ sess' = "open" /\ need' # Pos => need' \cap Good(part) = {}]_vars

\* Done: with finitely many faults, the transfer commits, or the sender's
\* file is no longer the offered one.
Done == <>(committed # <<>>) \/ <>[](src # offer)

\* Witnesses (each must be violated: the behaviour is reachable).
WitnessResumedCommit == ~(committed # <<>> /\ resumed)
WitnessChangedThenCommit == ~(committed # <<>> /\ faults = MaxFaults)
WitnessDiscarded == ~(sess = "none" /\ part = Empty /\ faults > 0 /\ resumed)
WitnessRefusedThenCommit == ~(committed # <<>> /\ refused)
\* WitnessGap: a session asks for a chunk before one it keeps (a resume set
\* that is not a prefix).
WitnessGap == ~(sess = "open" /\ \E i, j \in Pos : i < j /\ i \in need /\ j \notin need)
=============================================================================
