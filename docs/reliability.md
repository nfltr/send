# Reliability

Every number here comes from `make reliability` and `make
reliability-soak` ([`bench/rel/`](../bench/rel/README.md)), which anyone can
rerun on a Linux machine with docker. A private relay, a sender and a
receiver run in their own network namespaces on one machine; netem sets the
rate, the round trip and the loss for the direct and the relayed path
alike. Each transfer is judged by exit codes and by the received tree
against the sent one: every name, type and SHA-256, nothing missing, nothing
extra. Any run with a wrong, missing or extra byte fails the whole run.

## Baseline, 2026-11-19

nfltr-send v0.1.3 with nfltr and nfltr-relay v2.1.73, the released
binaries (signatures verified). One Linux machine (16 CPUs, Linux 6.12)
shared with other jobs: load average 0.2 to 60 during the run, median
3.7. `net.core.rmem_max` was the kernel's default (212,992 bytes), which
limits the direct path's UDP buffers. 55 cells, 5 runs each, 275
transfers, 103 minutes.

- **Every receive was right**: in all 275 transfers the receiver exited as
  expected and the received tree equalled the sent one byte for byte.
  Corrupted: 0. Missing or extra bytes: 0.
- **The run fails**, on one defect: in 74 of the 130 relayed transfers
  the `--once` sender kept serving after the receiver had committed every
  file. It logged `session reset by the peer` where it should have read
  the receiver's answer, so it never counted the transfer as done (see
  "Defects found and fixed"). All 145 direct-path, no-account and disk-full transfers
  passed.
- **Soak, 30 minutes** (random cells, 37 of 60 transfers with a fault: the
  sender or the receiver killed at a random point, or the link dropped for
  2 to 15 s): 60 transfers, corrupted 0; 42 passed, 14 relayed ones left
  the sender serving (the defect above), and 4 hit the second defect below.

### Rate, round trip and loss

One file per transfer: 4, 16 or 64 MiB at 10, 100 or 1000 Mbit/s without
loss, 4 MiB with 1 % loss in each direction. Each entry is the receives
that were right, then the median Mbit/s (file bytes over the receiver's
run, from its start to its exit, so the session's opening counts); "hung"
counts the runs whose `--once` sender did not exit.

| rate | RTT | direct | relayed | direct, 1 % loss | relayed, 1 % loss |
|---|---|---|---|---|---|
| 10 Mbit/s | 1 ms | 5/5, 8.2 | 5/5, 9.3 | 5/5, 7.7 | 5/5, 9.3 |
| 10 Mbit/s | 40 ms | 5/5, 7.5 | 5/5, 8.8 | 5/5, 4 | 5/5, 6.6 (3 hung) |
| 10 Mbit/s | 150 ms | 5/5, 3.6 | 5/5, 7.4 (1 hung) | 5/5, 2.4 | 5/5, 3.2 |
| 100 Mbit/s | 1 ms | 5/5, 88.5 | 5/5, 91.5 (2 hung) | 5/5, 72.2 | 5/5, 80.9 (2 hung) |
| 100 Mbit/s | 40 ms | 5/5, 64.9 | 5/5, 78.1 (4 hung) | 5/5, 8.3 | 5/5, 10.3 (2 hung) |
| 100 Mbit/s | 150 ms | 5/5, 10.2 | 5/5, 51.2 (4 hung) | 5/5, 1.4 | 5/5, 2.1 (4 hung) |
| 1000 Mbit/s | 1 ms | 5/5, 806.9 | 5/5, 807 (2 hung) | 5/5, 126.8 | 5/5, 523.9 (3 hung) |
| 1000 Mbit/s | 40 ms | 5/5, 392.7 | 5/5, 555.7 (5 hung) | 5/5, 4.5 | 5/5, 32.6 (5 hung) |
| 1000 Mbit/s | 150 ms | 5/5, 123.9 | 5/5, 173.7 (2 hung) | 5/5, 1.7 | 5/5, 3.4 (3 hung) |

These are small files on a netem path, so slow start and loss recovery
dominate the long round trips; the numbers are measurements, not a
target. With loss, and at 150 ms, the direct path (QUIC) is slower than
the relayed one (TCP to the relay), for example 10.2 against 51.2 Mbit/s
at 100 Mbit/s and 150 ms.

### Cut and resume

A 64 MiB file at 100 Mbit/s and 40 ms. Once the receiver's stage held 25,
50 or 75 % of the file, the receiver or the sender was killed (SIGKILL).
A killed receiver was started again on the same address; after a killed
sender the first receiver had to end on its own (it did, with exit 3),
then a new sender of the same file served a new address and a receive of
it into the same directory resumed. Each entry is the receives that were
right, then the median share of the file the resuming receive wrote (the
ideal is the part still missing: 0.75 after a cut at 25 %).

| fault (64 MiB, 100 Mbit/s, 40 ms) | direct | relayed |
|---|---|---|
| receiver killed at 25 % | 5/5, 0.75 | 5/5, 0.75 (5 hung) |
| receiver killed at 50 % | 5/5, 0.5 | 5/5, 0.5 (5 hung) |
| receiver killed at 75 % | 5/5, 0.25 | 5/5, 0.25 (5 hung) |
| sender killed at 25 % | 5/5, 0.7 | 5/5, 0.61 (4 hung) |
| sender killed at 50 % | 5/5, 0.46 | 5/5, 0.38 (4 hung) |
| sender killed at 75 % | 5/5, 0.21 | 5/5, 0.12 (5 hung) |

### Many files, a large file, no account, a full disk

| data (no impairment) | direct | relayed |
|---|---|---|
| 10,000 files of 4 KiB | 5/5, 104.1 | 5/5, 108.2 (2 hung) |
| one 4 GiB file | 5/5, 1872.8 | 5/5, 1999.4 (2 hung) |

- **No account, direct**: a sender without a machine certificate, 16 MiB,
  5/5 right, every one on the direct path, 0.16 s median.
- **No account, no UDP** between the two ends: the receiver must exit 12
  (`direct_required`) with nothing in the receiving directory: 5/5, after
  a median 55 s.
- **Disk full**: 32 MiB into a 24 MiB file system. The first receive must
  fail with nothing visible in the directory, and a receive of the same
  address after the file system grew must complete: 5/5 (median 35.7 s
  for both).

## Defects found and fixed

All three were in how a relayed transfer ends (lanes SENDFIX and SENDONCE;
model [`spec/End.tla`](../spec/README.md), a mutant for each).

1. **A `--once` sender kept serving after a relayed transfer completed**
   (74 of 130 relayed transfers in the matrix, never on the direct path).
   The sender logged `transfer ended ... err="session reset: link:
   session reset by the peer"` instead of `transfer done`. Cause: the
   receiver's close of its nf1 session cut the session's data connection
   to the relay at once (relay client `nfnet`), its last frames and the
   session's end still unwritten, so the relay reset the session under
   the answer. Fixed in the relay client: the data connection drains
   (link `Mux.Drain`) and closes with every frame written. Tests:
   relay client v0.23.6 `TestNF1CloseKeepsWhatWasWritten`, here
   `TestOnceRelayedSenderReadsTheAnswer` (20 relayed `--once` transfers
   through relay-core; the first failed before the fix). Model: `Told`,
   mutant `close-cuts`.
2. **A relayed receive could commit every file and still exit 3** (4 soak
   runs). When the sender died after its last frame, the receiver
   committed, then its answer's write failed and the receive retried the
   dead address and exited 3; a receive of a new address then refused
   (the names taken). A committed receive is now done whether or not its
   answer reached the sender: exit 0. Test
   `TestCommitStandsWhenTheAnswerDoesNotGo`; model `ExitRight`, mutant
   `exit-on-answer`.
3. **A `--once` sender served on after a link drop at the commit** (4 of
   20 relayed link drops in the soak below, lane SENDONCE). A drop longer
   than the session's liveness, right after the receiver committed, made
   the relay reset the session before the receiver's answer reached the
   sender; the receiver exited 0 with every file, and the sender, never
   told, served on. Fix: the sender ends a session cleanly once it read
   the answer, and the receiver, after its answer, waits for that end
   (end to end: under TLS, the sender's close_notify) as the sender's
   word. Its own close is no such word: it returns once the relay has the
   bytes (a first fix that trusted it still left a sender serving in the
   soak; the model's mutant `close-confirms`). A committed receive whose
   session was cut before the sender's end dials the address again,
   within its `--tries` (the
   pauses of a resume: 1, 2, ... 5 s), and answers the manifest at once
   with the commit's answer (`xfer.Retell`; the sender already takes an
   answer in place of the want), so the sender reads it and ends. The
   receive exits 0 either way (its files are in place); after the last
   try it says the sender did not confirm. Test
   `TestOnceSenderToldAfterACutAnswer` (under the receiver's answer,
   `receiver`: its relay connections closed; `sender`: the sender's agent
   link silenced, the soak's drop, the relay resetting the sender's
   session when its liveness ran out; before the fix the sender logged
   `session reset by the peer` and served on in both); model `Retold`,
   mutants `no-retell` and `close-confirms`.

   Rejected: a `--once` sender that, after a reset following its last
   byte, waits a while for a new session and then exits with a
   "delivered, unconfirmed" code. The sender cannot tell a receiver that
   committed from one that died before its commit with every byte
   staged; exiting would strand the second (its next receive resumes from
   a sender that is gone), and the exit would claim a delivery nobody
   confirmed. The model's mutant `sender-gives-up` is that design, caught
   by `Confirmed` (a sender ends only on an answer). The sender still
   exits 0 only on the receiver's answer. Prior art: the receiver's
   repeated answer is TCP's final acknowledgement repeated from
   TIME-WAIT when the peer's FIN comes again (RFC 9293, TIME-WAIT),
   and an idempotent retry of the last message of an exchange; no end of
   a two-party exchange over a lossy link can be certain (the two
   generals), so the receiver's tries bound it: a drop that outlasts them
   all leaves the sender serving, as one that outlasts a receiver killed
   after its commit does.

Rerun with the fixes (nfltr-send at land/SENDFIX, nfltr and nfltr-relay
v2.1.73, same machine, 2026-11-19): every relayed cell of the matrix
(`REL_CELLS='relayed$'`) 20 times, 26 cells, 520 transfers: 520 right,
0 failed, no sender left serving. A 25-minute soak of relayed cells
(`REL_SEED=17`, 137 transfers, 65 with a process killed, 20 with the link
dropped for 2 to 15 s): every receive right, no committed receive exiting
non-zero, no sender left serving after a transfer without a fault or with
a killed process; 4 of the 20 link drops left the sender serving (defect
3 above).

Rerun with defect 3 fixed (nfltr-send at land/SENDONCE aa78326, nfltr and
nfltr-relay v2.1.73, same machine, 2026-11-19): two 25-minute soaks of
relayed cells (`REL_SEED=17` and `29`), 217 transfers, 120 with a fault,
33 of them a link drop: all 217 right, none left a sender serving. The
whole matrix (`make reliability`, 55 cells, 275 transfers): 260 right,
0 corrupted; the 15 failures are all `cut-receiver-*-direct`
(`sender_hang`), and the same cells fail at main (d3a2174) too, 9 of 9:
after a transfer completed, a `--once` sender whose earlier direct-path
receiver was killed takes more than 10 s to exit, its listener's close
waiting for that dead receiver's direct session (relay client
`nfnet.Listener.Close`, `agent.directServer.stop`).

Lane SENDCUT found the cause (not a v0.23.6 regression: send v0.1.4 with
client v0.23.5 fails the same cells 9 of 9; the matrix's sender bound went
from 30 s to 10 s in `05d28f5`, the baseline ran with 30 s). The sender
exited 21 s after its transfer: the listener's close reset the dead
session's Conn, but `peerlink.Join`'s copy toward the dead peer waited on
its window and never read that reset, so the session held the agent's
drain to its bound (5 s); then the agent's three stops each waited their
own 5 s grace for the ABORT's answer (5 + 5 + 1 + 5 s). Fixed in the relay
client (land/SENDCUT): a target's reset resets its direct session at once,
the drain skips a session already reset, and the grace runs once per
session (tests `TestDirectTargetResetResetsTheSession`,
`TestDirectStopsWaitOneGrace`). With it (nfltr-send at d16a050, the relay
client replaced locally): the `cut-receiver-*-direct` sender exits 4.8 s
after the receiver, 15 of 15; the whole matrix, 275 of 275 right, 0 corrupted.

nfltr-send v0.1.5 (the release, client v0.23.6; nfltr and nfltr-relay
v2.1.73, same machine, 2026-11-19), the whole matrix: 258 of 275 right, 0 corrupted; the 17 failures all
`sender_hang`: the 15 `cut-receiver-*-direct` and two direct transfers
without a fault (`net-100mbit-150ms-loss1-direct`, `large-file-direct`),
the same stop waiting on a slow direct session.

## Rerun

```console
$ make reliability                             # the matrix (nfltr-send from this checkout), about 80 minutes
$ make reliability-soak REL_SOAK_S=1800        # 30 minutes of random cells with faults
$ make reliability SEND_VERSION=v0.1.5          # a released nfltr-send
```

Each run writes one JSON line per transfer, one per cell and a summary
([`bench/rel/README.md`](../bench/rel/README.md) "Output").
