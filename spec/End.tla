------------------------------ MODULE End ------------------------------
\* Copyright 2026 nfltr
\* SPDX-License-Identifier: Apache-2.0
\*
\* The end of one nfltr-send transfer, both processes and the session
\* between them (spec/README.md "End"). The receiver commits (Resume.tla
\* and Commit.tla say what), writes its answer, closes the session and
\* exits; the sender reads the answer and counts the transfer done (a
\* --once sender exits). The session carries the receiver's frames to the
\* sender in order; a close delivers what was written before it, then a
\* clean end; a cut (the network, the sender's death, the relay) delivers
\* some prefix of what was written, then a reset, and fails the receiver's
\* later writes. The sender's own frames before the answer are Resume's.

EXTENDS Naturals, Sequences

CONSTANTS MaxFaults, \* cuts, 0 or 1
          Mutant     \* "none", or the planted failure (README "Mutants")

VARIABLES committed, \* the receiver committed the offer
          wire,      \* the receiver's frames in flight to the sender, oldest first
          rx,        \* the receiver: "run" | "answered" | "closed" | "ok" | "failed" (its exit)
          sx,        \* the sender: "wait" | "done" (read the answer) | "ended" (read an end or a reset first)
          cut        \* the session was cut

vars == <<committed, wire, rx, sx, cut>>

Frames == {"answer", "end", "reset"}
Exited == {"ok", "failed"}
Prefixes(s) == {SubSeq(s, 1, k) : k \in 0..Len(s)}

TypeOK ==
    /\ committed \in BOOLEAN
    /\ wire \in Seq(Frames)
    /\ rx \in {"run", "answered", "closed"} \cup Exited
    /\ sx \in {"wait", "done", "ended"}
    /\ cut \in BOOLEAN

Init ==
    /\ committed = FALSE /\ wire = <<>> /\ rx = "run" /\ sx = "wait" /\ cut = FALSE

\* Commit: every file arrived and verified (before or as the session was
\* cut: what the sender wrote before its death still arrives), committed.
Commit ==
    /\ rx = "run" /\ ~committed
    /\ committed' = TRUE
    /\ UNCHANGED <<wire, rx, sx, cut>>

\* Answer: the receiver writes its answer on the session it still has.
Answer ==
    /\ rx = "run" /\ committed /\ ~cut
    /\ wire' = Append(wire, "answer") /\ rx' = "answered"
    /\ UNCHANGED <<committed, sx, cut>>

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
    /\ UNCHANGED <<committed, sx, cut>>

\* Exit: the receiver exits once it closed: ok, it committed.
Exit ==
    /\ rx = "closed"
    /\ rx' = "ok"
    /\ UNCHANGED <<committed, wire, sx, cut>>

\* Cut: the session ends as failed at any point before the receiver
\* closed it: some prefix of what it wrote arrives, then the reset.
Cut ==
    /\ ~cut /\ MaxFaults > 0 /\ rx \in {"run", "answered"}
    /\ \E p \in Prefixes(wire) : wire' = Append(p, "reset")
    /\ cut' = TRUE
    /\ UNCHANGED <<committed, rx, sx>>

\* Fail: a receiver whose session was cut exits: ok when it had committed
\* (its answer did not go, the files are in place), else failed (the next
\* receive resumes). Mutant "exit-on-answer" (defect 2): it exits failed
\* because its answer did not go.
Fail ==
    /\ cut /\ rx \in {"run", "answered"}
    /\ rx' = IF committed /\ Mutant # "exit-on-answer" THEN "ok" ELSE "failed"
    /\ UNCHANGED <<committed, wire, sx, cut>>

\* Read: the sender reads the next frame: the answer counts the transfer
\* done; an end or a reset before it ends the session (a --once sender
\* serves on).
Read ==
    /\ sx = "wait" /\ wire # <<>>
    /\ sx' = IF Head(wire) = "answer" THEN "done" ELSE "ended"
    /\ wire' = Tail(wire)
    /\ UNCHANGED <<committed, rx, cut>>

\* Over: both ends are finished.
Over == rx \in Exited /\ sx # "wait" /\ UNCHANGED vars

Next == Commit \/ Answer \/ Close \/ Exit \/ Cut \/ Fail \/ Read \/ Over

Spec == Init /\ [][Next]_vars /\ WF_vars(Commit) /\ WF_vars(Answer) /\ WF_vars(Close)
        /\ WF_vars(Exit) /\ WF_vars(Fail) /\ WF_vars(Read)

\* ExitRight: a receiver exits ok exactly when it committed.
ExitRight == rx \in Exited => (rx = "ok") = committed

\* Told: a sender whose session was not cut ends only on the answer.
Told == sx = "ended" => cut

\* Done: both ends finish; with no cut the sender read the answer.
Done == <>(rx \in Exited /\ sx # "wait") /\ <>[](~cut => sx = "done")

\* Witnesses (each must be violated: the behaviour is reachable).
\* WitnessCommittedCut: a receiver committed, its session was cut before
\* its answer went, and it exits ok (defect 2's case).
WitnessCommittedCut == ~(rx = "ok" /\ cut /\ sx # "done")
\* WitnessTold: the sender read the answer.
WitnessTold == ~(sx = "done")
=============================================================================
