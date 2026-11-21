------------------------------ MODULE TraceResume ------------------------------
\* Copyright 2026 nfltr
\* SPDX-License-Identifier: Apache-2.0
\*
\* Trace validation of Resume.tla: the steps one file's receiver recorded
\* (xfer -tags sendtrace, <stage>-f<entry>.ndjson) must be a behaviour of
\* Resume. A chunk is maxData bytes of the file (a verified chunk), its value the digest of
\* its bytes, "h" where the part has no byte. The receiver records Open
\* (its part on disk, a value per chunk, and the chunks the session asks
\* for), Send (a chunk verified and written at its place), Bad (a chunk
\* that did not verify, refused), Finish (the file's BLAKE3 over what is on
\* its disk compared with the root) and Cut; once the file is verified (Resume's committed: the offer's
\* commit is Commit.tla's), a later session's Open, Finish and Cut of it are
\* Served. The sender is not recorded: the offered file is one whose chunk
\* at each position is a chunk the receiver took there (Offers), the
\* sender's file one whose chunk is that or a refused one (Srcs), and Rot,
\* Crash and Change are hidden steps, each taken only towards the next
\* line. Accepted when a
\* behaviour takes every line (TraceNotDone violated).
EXTENDS TraceLog, Resume, TLC

TraceN == Log[1].n
At(ev, j) == {Lines[k].v : k \in {k \in DOMAIN Lines : Lines[k].ev = ev /\ Lines[k].i = j}}
OpenVals == UNION {{Lines[k].part[j] : j \in DOMAIN Lines[k].part} : k \in {k \in DOMAIN Lines : Lines[k].ev = "Open"}} \ {Hole}
TraceVals == {"x"} \cup OpenVals \cup UNION {At("Send", j) \cup At("Bad", j) : j \in 1..TraceN}
\* A chunk never sent in this trace: the offer's is the value its first Open
\* found staged (an earlier receive fetched it: a striped one writes no
\* Resume lines), else unknown ("x").
Staged(j) == {Lines[k].part[j] : k \in {k \in DOMAIN Lines : Lines[k].ev = "Open" /\ \A m \in 1..k - 1 : Lines[m].ev # "Open"}} \ {Hole}
C(j) == IF At("Send", j) # {} THEN At("Send", j) ELSE IF Staged(j) # {} THEN Staged(j) ELSE {"x"}
RECURSIVE Prod(_, _)
Prod(G, j) == IF j = 0 THEN {<<>>} ELSE {Append(s, v) : s \in Prod(G, j - 1), v \in G[j]}
Offers == Prod([j \in 1..TraceN |-> C(j)], TraceN)
Srcs == Prod([j \in 1..TraceN |-> C(j) \cup At("Bad", j)], TraceN)
Set(s) == {s[k] : k \in DOMAIN s}

VARIABLE l
traceVars == <<vars, l>>

E == Lines[l]
\* A recorded step takes the next line; a hidden one takes none.
Adv == l <= Len(Lines) /\ l' = l + 1
Hid == l <= Len(Lines) /\ UNCHANGED l
NE == IF l <= Len(Lines) THEN Lines[l] ELSE [ev |-> "none"]

TOpen == Adv /\ E.ev = "Open" /\ committed = <<>> /\ part = E.part /\ Open /\ need' = Set(E.need)
TSend == Adv /\ E.ev = "Send" /\ need # {} /\ Min(need) = E.i /\ Send /\ part'[E.i] = E.v
TBad == Adv /\ E.ev = "Bad" /\ need # {} /\ Min(need) = E.i /\ src[E.i] = E.v /\ Bad
TFinish == Adv /\ E.ev = "Finish" /\ committed = <<>> /\ Finish /\ (committed' # <<>>) = E.ok
TCut == Adv /\ E.ev = "Cut" /\ committed = <<>> /\ Cut
TServed == Adv /\ E.ev \in {"Open", "Finish", "Cut"} /\ committed # <<>> /\ Served
           /\ (E.ev = "Open" => E.part = committed /\ E.need = <<>>) /\ (E.ev = "Finish" => E.ok)

HRot == Hid /\ Rot
        /\ \/ /\ NE.ev = "Open"
              /\ \E i \in Pos : part[i] # NE.part[i] /\ part'[i] = NE.part[i]
           \/ /\ NE.ev = "Finish" /\ ~NE.ok /\ part = offer
              /\ \E i \in Pos : part'[i] = "x"
HCrash == Hid /\ l > 1 /\ NE.ev = "Open" /\ part # NE.part /\ part' = NE.part /\ Crash
\* HPrior: the trace's first Open finds chunks an earlier receive staged that
\* this trace does not show (a striped receive writes no Resume lines);
\* before the first line it is the only change of part (HCrash would
\* enumerate every subset of Pos to reach it).
HPrior == Hid /\ l = 1 /\ part = Empty /\ NE.ev = "Open" /\ part # NE.part /\ part' = NE.part
          /\ UNCHANGED <<offer, src, need, sess, committed, faults, resumed, refused>>
HChange == Hid /\ NE.ev \in {"Open", "Bad"} /\ src' \in Srcs \ {src} /\ Change

TraceInit == offer \in Offers /\ Init /\ l = 1
TraceNext == TOpen \/ TSend \/ TBad \/ TFinish \/ TCut \/ TServed \/ HRot \/ HCrash \/ HPrior \/ HChange
TraceNotDone == l <= Len(Lines)
=============================================================================
