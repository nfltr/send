------------------------------ MODULE End ------------------------------
\* Copyright 2026 nfltr
\* SPDX-License-Identifier: Apache-2.0
\*
\* The end of one nfltr-send transfer, both processes and the sessions
\* between them (spec/README.md "End"). The receiver commits (Resume.tla
\* and Commit.tla say what), writes its answer, closes the session and
\* exits; the sender reads the answer and counts the transfer done (a
\* --once sender exits). The sessions carry the receiver's frames to the
\* sender in order (one queue: a later session's frames after an earlier
\* one's); a close delivers what was written before it, then a clean end,
\* and returns once the sender read it all; a cut (the network, the
\* sender's death, the relay) delivers some prefix of what was written,
\* then a reset, and fails the receiver's later writes and its close. A
\* receiver that committed and whose close did not return clean (its
\* answer may not have reached the sender) dials the address again and
\* answers the manifest at once (Retell, lane SENDONCE), up to MaxTries
\* dials; a dial while the sender no longer serves fails. The sender's
\* own frames before the answer are Resume's.

EXTENDS Naturals, Sequences

CONSTANTS MaxFaults, \* cuts
          MaxTries,  \* the receiver's dials to tell the sender again (receive --tries)
          Mutant     \* "none", or the planted failure (README "Mutants")

VARIABLES committed, \* the receiver committed the offer
          wire,      \* the receiver's frames in flight to the sender, oldest first
          rx,        \* the receiver: "run" | "answered" | "closed" | "retell" | "ok" | "failed" (its exit)
          sx,        \* the sender: "serve" | "done" (read an answer: a --once sender exits)
          cut,       \* the receiver's current session was cut
          cuts,      \* cuts so far
          tries,     \* dials made to tell the sender again
          lost       \* ghost: a cut lost a committed receiver's answer (the sender had not read one)

vars == <<committed, wire, rx, sx, cut, cuts, tries, lost>>

Frames == {"answer", "end", "reset"}
Exited == {"ok", "failed"}
Prefixes(s) == {SubSeq(s, 1, k) : k \in 0..Len(s)}
Has(s, f) == \E i \in 1..Len(s) : s[i] = f

TypeOK ==
    /\ committed \in BOOLEAN
    /\ wire \in Seq(Frames)
    /\ rx \in {"run", "answered", "closed", "retell"} \cup Exited
    /\ sx \in {"serve", "done"}
    /\ cut \in BOOLEAN
    /\ cuts \in 0..MaxFaults
    /\ tries \in 0..MaxTries
    /\ lost \in BOOLEAN

Init ==
    /\ committed = FALSE /\ wire = <<>> /\ rx = "run" /\ sx = "serve"
    /\ cut = FALSE /\ cuts = 0 /\ tries = 0 /\ lost = FALSE

\* Commit: every file arrived and verified (before or as the session was
\* cut: what the sender wrote before its death still arrives), committed.
Commit ==
    /\ rx = "run" /\ ~committed
    /\ committed' = TRUE
    /\ UNCHANGED <<wire, rx, sx, cut, cuts, tries, lost>>

\* Answer: the receiver writes its answer on the session it still has
\* (after a Retell dial: at once, in place of its want).
Answer ==
    /\ rx = "run" /\ committed /\ ~cut
    /\ wire' = Append(wire, "answer") /\ rx' = "answered"
    /\ UNCHANGED <<committed, sx, cut, cuts, tries, lost>>

\* Close: the receiver closes the session: what it wrote, then the end
\* (relay client nfnet: the nf1 data connection drained, Mux.Drain).
\* Mutant "close-cuts" (send docs/reliability.md defect 1): the data
\* connection closed at once, its unwritten frames lost, and the relay
\* reset the session.
Close ==
    /\ rx = "answered"
    /\ IF Mutant = "close-cuts"
          THEN \E p \in Prefixes(wire) : wire' = Append(p, "reset")
          ELSE wire' = Append(wire, "end")
    /\ rx' = "closed"
    /\ UNCHANGED <<committed, sx, cut, cuts, tries, lost>>

\* Exit: the close returned clean, the sender read every frame: ok, it
\* committed.
Exit ==
    /\ rx = "closed" /\ ~cut /\ wire = <<>>
    /\ rx' = "ok"
    /\ UNCHANGED <<committed, wire, sx, cut, cuts, tries, lost>>

\* Cut: the session ends as failed before the receiver's close returned:
\* some prefix of what it wrote arrives, then the reset.
Cut ==
    /\ ~cut /\ cuts < MaxFaults /\ rx \in {"run", "answered", "closed"}
    /\ \E p \in Prefixes(wire) :
          /\ wire' = Append(p, "reset")
          /\ lost' = (lost \/ (committed /\ sx = "serve" /\ ~Has(p, "answer")))
    /\ cut' = TRUE /\ cuts' = cuts + 1
    /\ UNCHANGED <<committed, rx, sx, tries>>

\* Fail: a receiver whose session was cut: one that committed tells the
\* sender again (Retell; its files are in place); one that did not exits
\* failed (the next receive resumes). Mutant "exit-on-answer" (defect 2):
\* a committed one exits failed because its answer did not go. Mutant
\* "no-retell" (lane SENDONCE: 4 of 20 relayed link drops longer than the
\* session's liveness): it exits ok at once, and a sender that had not read
\* its answer serves on.
Fail ==
    /\ cut /\ rx \in {"run", "answered", "closed"}
    /\ rx' = CASE ~committed \/ Mutant = "exit-on-answer" -> "failed"
               [] Mutant = "no-retell"                  -> "ok"
               [] OTHER                                 -> "retell"
    /\ UNCHANGED <<committed, wire, sx, cut, cuts, tries, lost>>

\* Retell: the committed receiver dials the address again; the sender
\* still serves it, a new session.
Retell ==
    /\ rx = "retell" /\ tries < MaxTries /\ sx = "serve"
    /\ rx' = "run" /\ cut' = FALSE /\ tries' = tries + 1
    /\ UNCHANGED <<committed, wire, sx, cuts, lost>>

\* Unserved: the dial finds the address no longer served (a --once sender
\* that read the answer exited); the receiver counts the try.
Unserved ==
    /\ rx = "retell" /\ tries < MaxTries /\ sx = "done"
    /\ tries' = tries + 1
    /\ UNCHANGED <<committed, wire, rx, sx, cut, cuts, lost>>

\* GiveUp: every dial made; the receiver exits ok (it committed) and says
\* the sender may not know.
GiveUp ==
    /\ rx = "retell" /\ tries = MaxTries
    /\ rx' = "ok"
    /\ UNCHANGED <<committed, wire, sx, cut, cuts, tries, lost>>

\* Read: the sender reads the next frame: an answer counts the transfer
\* done; an end or a reset ends that session (it serves on). Mutant
\* "sender-gives-up" (the rejected design: a --once sender that read a
\* reset after its last byte exits as if delivered).
Read ==
    /\ wire # <<>>
    /\ sx' = CASE Head(wire) = "answer"                               -> "done"
               [] Head(wire) = "reset" /\ Mutant = "sender-gives-up" -> "done"
               [] OTHER                                             -> sx
    /\ wire' = Tail(wire)
    /\ UNCHANGED <<committed, rx, cut, cuts, tries, lost>>

\* Over: both ends are finished (a sender not done serves on, for the next receive).
Over == rx \in Exited /\ wire = <<>> /\ UNCHANGED vars

Next == Commit \/ Answer \/ Close \/ Exit \/ Cut \/ Fail \/ Retell \/ Unserved
        \/ GiveUp \/ Read \/ Over

Spec == Init /\ [][Next]_vars /\ WF_vars(Commit) /\ WF_vars(Answer) /\ WF_vars(Close)
        /\ WF_vars(Exit) /\ WF_vars(Fail) /\ WF_vars(Retell) /\ WF_vars(Unserved)
        /\ WF_vars(GiveUp) /\ WF_vars(Read)

\* ExitRight: a receiver exits ok exactly when it committed.
ExitRight == rx \in Exited => (rx = "ok") = committed

\* Told: a reset reaches the sender only from a cut.
Told == \A i \in 1..Len(wire) : wire[i] = "reset" => cuts > 0

\* Confirmed: a sender ends only on an answer, so only once the receiver
\* committed (no "delivered" without the receiver's word).
Confirmed == sx = "done" => committed

\* Retold: with more dials than cuts, a receiver exits ok only once the
\* sender read an answer (lane SENDONCE: the sender serving on after a
\* cut that lost the answer).
Retold == rx = "ok" => sx = "done"

\* Done: both ends finish; once the receiver committed, the sender ends.
Done == <>(rx \in Exited) /\ (committed ~> sx = "done")

\* Witnesses (each must be violated: the behaviour is reachable).
\* WitnessCommittedCut: a receiver committed, its session was cut before
\* its answer reached the sender, and it exits ok (defect 2's case).
WitnessCommittedCut == ~(rx = "ok" /\ lost)
\* WitnessTold: the sender read the answer.
WitnessTold == ~(sx = "done")
\* WitnessRetold: a cut lost the committed receiver's answer, and the
\* sender ended all the same (told again on a later session).
WitnessRetold == ~(lost /\ sx = "done")
=============================================================================
