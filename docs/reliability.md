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
  "Open defects"). All 145 direct-path, no-account and disk-full transfers
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

## Open defects

1. **A `--once` sender keeps serving after a relayed transfer completed.**
   The receiver has committed every file and exited 0; the sender logs
   `transfer ended ... err="session reset: link: session reset by the
   peer"` instead of `transfer done`, so `--once` never exits. 74 of 130
   relayed transfers in the matrix, never on the direct path. The
   receiver exits right after writing its answer, and the session's reset
   appears to reach the sender before the answer does.
2. **A relayed receive can commit every file and still exit 3.** When the
   sender dies mid-transfer on the relayed path, the receiver can still
   get every byte (what the sender had already written), verify and
   commit it, then lose the session before the final answer, retry the
   dead address and exit 3 with "receive the same address again to
   resume". A receive of a new sender's address then refuses with exit 2
   (the name exists). The committed files were right in all 4 soak cases;
   a script that trusts the exit status sees a failure for a transfer
   that succeeded.

## Rerun

```console
$ make reliability                             # the matrix, about 80 minutes
$ make reliability-soak REL_SOAK_S=1800        # 30 minutes of random cells with faults
$ make reliability SEND_VERSION=v0.1.4 NFLTR_VERSION=v2.1.74
```

Each run writes one JSON line per transfer, one per cell and a summary
([`bench/rel/README.md`](../bench/rel/README.md) "Output").
