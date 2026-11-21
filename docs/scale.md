# Scale measurements

nfltr-send between Linux cloud machines in different regions, on
2026-11-18. **before** is the v0.1.2 release binary (its signature and
sum checked as the README says). **after** is v0.1.2 built against the
relay client with the direct-path window fix (relay branch
`land/SENDWINDOW`, see "What limited a transfer"). Where the two are
compared they ran alternately on the same machines, so load fell on both.
The relay is a private one (`nfltr-relay` from nfltr v2.1.70) on one of
the machines, and the sender holds a machine certificate from it, so the
relay can carry a transfer. The scripts and how to rerun them are in
[`bench/scale/`](../bench/scale/README.md). Each number is a median of the
runs named. "address to received" is the receiver's own run, from its
start to its exit with the files committed. Test 5, on 2026-11-20, is
the v0.1.9 release binary on the longest path between the provider's
regions; it adds to the history above and replaces none of it. Test 6,
on 2026-11-21, is the same release between that provider's machines and
Google Cloud machines (["Between providers"](#6-between-providers-v019-and-v0110));
its 10 GiB and 100 GiB runs from Helsinki to Singapore were repeated
later that day on the v0.1.10 release.

## What limited a transfer

One transfer moved at most about 1 MiB per round trip, on the direct
path and the relayed path alike. A session that may go direct wraps its
streams in the relay client's path switch. Two windows were 1 MiB: the
switch's window (the bytes a sender keeps until the peer acknowledges
them) and the direct QUIC stream's receive window. The relay link itself
gives a session 16 MiB.

The measurements show it. Before the fix, the 10 GiB file arrived at
24 MB/s over 36 ms, about 1 MiB per round trip, on a path that carries
748 Mbit/s on one TCP stream. On 95 ms it was 9 MB/s, and on 159 ms 5 MB/s.
Neither machine was busy: the receiver's disk writes 1.1 GB/s and its CPU
was mostly idle. The fix gives both windows the relay link's 16 MiB. A
test in the relay client fails without it.

| | before | after |
|---|---|---|
| 10 GiB, Falkenstein to Helsinki (36 ms) | 191 Mbit/s (449 s) | 757 Mbit/s (114 s) |
| 15 GB in 100,000 mixed files, same path | 192 Mbit/s (632 s) | 676 Mbit/s (180 s) |
| 1 GiB, Nuremberg to Ashburn (95 ms), direct | 73 Mbit/s (118 s) | 292 Mbit/s (29 s) |
| 1 GiB, Nuremberg to Ashburn, relayed | 87 Mbit/s (98 s) | 233 Mbit/s (37 s) |
| 1 GiB, Nuremberg to Singapore (159 ms), direct | 41 Mbit/s (211 s) | 265 Mbit/s (32 s) |
| 1 GiB, Nuremberg to Singapore, relayed | 48 Mbit/s (180 s) | 136 Mbit/s (63 s) |

Both ends need the fix. A sender bounds what it keeps by its own window,
and a receiver's QUIC window is its own. Ends of different versions still
work together, at the old rate.

**After the fix.** The 10 GiB transfer moves at about the rate of one TCP
stream on the same path. Neither end is short of CPU: under `perf` the
sender used 1.35 CPUs of 8 and the receiver 1.15 of 4. The profile is flat
(raw profiles are kept with the evidence):

- 56 % of the receiver's samples are in the kernel's UDP receive path.
- AES-GCM runs twice per byte, once for QUIC and once for the end-to-end
  TLS: 14 % of the sender, 6.5 % of the receiver.
- Copies take 15 % of the sender.
- BLAKE3 verification takes 2.5 % of the receiver.

What is left is one flow's congestion control on a path with some loss.
The same path carries 2.9 Gbit/s on four TCP streams, so going further
would need parallel connections, not larger windows.

**Rate-capped paths.** Under a netem cap of 100 Mbit/s at 95 and 159 ms,
the direct path took 68 % and 43 % of the cap in these runs, while the
relayed path (TCP) took 92-93 %. That the before and after runs match does
not show that the window is not the cause: on a lab path with the same cap
and v0.1.3 at both ends, the direct path took 93.1 Mbit/s at 95 ms (one TCP
stream: 94.5) and 89.2 at 159 ms (TCP: 93.6) in 7 of 8 runs, 55.0 in the
other, while a v0.1.2 receiver gave 38.6 at 159 ms, and a 1 MiB window
predicts 66 % and 42 %. Why these capped runs look like a 1 MiB window is
not yet known; they are to be rerun with each end's binary recorded.

## Machines and paths

| machine | role | vCPU | RAM | disk | region |
|---|---|---|---|---|---|
| A | sender (tests 1, 2, 4) | 8 dedicated (AMD EPYC Milan) | 32 GB | 240 GB NVMe | Falkenstein, DE |
| B | receiver (tests 1, 2, 4) | 4 shared (AMD EPYC Genoa) | 8 GB | 160 GB NVMe | Helsinki, FI |
| C | sender and relay (test 3) | 2 dedicated | 8 GB | 80 GB | Nuremberg, DE |
| D | receiver (test 3); the third-region relay | 2 dedicated | 8 GB | 80 GB | Ashburn, US |
| E | receiver (test 3) | 2 dedicated | 8 GB | 80 GB | Singapore |
| F | sender and relay (test 5) | 8 dedicated (AMD EPYC Milan) | 32 GB | 240 GB NVMe | Ashburn, US |
| G | receiver (test 5) | 4 dedicated (AMD EPYC Milan) | 16 GB | 160 GB NVMe | Singapore |

Debian 13, Linux 6.12, `net.core.rmem_max` and `wmem_max` 7,500,000 (the
README's setting) unless a row says otherwise. IPv4 only: the machines'
IPv6 was turned off for the measured runs, so the impairments and the UDP
block below apply to every path. In a first run the direct path chose
IPv6, and that run is left out.

Each path was measured first, unimpaired (`bench/scale/baseline.sh`: 20
pings, iperf3 for 10 s each):

| path | RTT ms | TCP 1 stream Mbit/s | TCP 4 streams | UDP Mbit/s received (loss at 2 Gbit/s offered) |
|---|---|---|---|---|
| A to B (Falkenstein to Helsinki) | 36.1 | 748 | 2944 | 1856 (2.2 %) |
| C to B (Nuremberg to Helsinki) | 23.8 | 1096 | 4369 | 1658 (1.9 %) |
| C to D (Nuremberg to Ashburn) | 95.1 | 247 | 1013 | 1620 (0.4 %) |
| C to E (Nuremberg to Singapore) | 158.7 | 69 | 376 | 1645 (4.5 %) |
| C to A (Nuremberg to Falkenstein) | 2.8 | 6074 | 14057 | 1649 (0.8 %) |
| F to G (Ashburn to Singapore, test 5) | 221.2 | 76 | 330 | 1146 (1.5 %) |

## 1 and 2. Large files and many small files (A to B, Falkenstein to Helsinki)

Page caches were dropped on both machines before each run. B's disk
writes 1.1 GB/s (`dd conv=fdatasync`, 10 GiB and 100 GiB). A reads the
100 GiB file at 13.7 GB/s (`dd iflag=direct`).

| case | ok | to address s | address to received s | total s | MB/s | files/s | send CPU s | send RSS MiB | recv CPU s | recv RSS MiB | paths |
|---|---|---|---|---|---|---|---|---|---|---|---|
| 10 GiB, before | 3/3 | 2.24 | 448.9 | 451.2 | 23.9 | - | 90.0 | 45 | 125.3 | 43 | relayed+direct |
| 10 GiB, after | 3/3 | 2.22 | 113.5 | 115.8 | 94.6 | - | 81.9 | 104 | 111.7 | 83 | relayed+direct |
| mixed 15 GB, 100,000 files, before | 3/3 | 23.98 | 632.1 | 656.0 | 24.0 | 158 | 160.6 | 159 | 216.2 | 127 | relayed+direct |
| mixed 15 GB, 100,000 files, after | 3/3 | 22.54 | 179.8 | 202.6 | 84.5 | 556 | 131.0 | 194 | 188.2 | 150 | relayed+direct |
| 100,000 x 4 KiB, before | 3/3 | 10.92 | 19.7 | 30.5 | 20.8 | 5086 | 20.3 | 139 | 24.7 | 124 | relayed+direct |
| 100,000 x 4 KiB, after | 3/3 | 10.54 | 18.4 | 28.8 | 22.3 | 5447 | 18.5 | 184 | 23.8 | 185 | relayed+direct |

- **100 GiB, sender start to address**: 30.1 s (3 runs, cold cache).
  Computing its BLAKE3 root and outboard on 8 cores took 135 s
  of CPU and 62 MiB peak RSS.
- **100 GiB, address to received, before**: 4717 s (22.8 MB/s). This was one run,
  alongside some of the 10 GiB and mixed runs. Each session was bound by
  its own window, not by the path.
- **100 GiB, after**: no uninterrupted run on this path. (On v0.1.9 an
  uninterrupted 100 GiB ran on a longer path, Ashburn to Singapore: test
  5.) The resume run's two
  receives moved 53.7 GB in 602 s (89 MB/s) and 53.7 GB in 704 s (76 MB/s);
  see test 4. Those are halves of a run whose receiver was killed and
  restarted, not a sustained rate for 100 GiB, and why the second half was
  slower is not measured.
- **Commit**: the receiver's one `syncfs(2)` took 0.09 s for the
  100,000 x 4 KiB files. This is from one run under
  `strace -e trace=syncfs -T`. strace slowed that run eightfold, so more of
  the data had already been written back: treat it as a lower bound.
- Small files are bound by per-file work, not the window: 18 s for 100,000
  files before and after.

## 3. Wide area: C to D, E and A, with a rate cap and loss added at the receiver

C sends a 1 GiB file to a receiver in Ashburn (95 ms), Singapore (159 ms)
and Falkenstein (3 ms), through the relay on C. `wan.sh` caps what arrives
from C at 100 Mbit/s or 1 Gbit/s, using netem on an ifb behind the
receiver's ingress, with a queue of one 200 ms bandwidth-delay product.
It also drops 0, 0.5 or 1 % of packets at random. A "relayed" run drops
UDP between C and the receiver, so only the relay's TCP path works.

**Without loss**, before and after, alternated, 3 runs each:

| case | runs | runs ended | goodput Mbit/s (median) | min-max | % of cap | paths |
|---|---|---|---|---|---|---|
| D (95 ms) after 100 Mbit/s cap, direct | 3 | all complete | 68.2 | 68.2-69.2 | 68 | relayed+direct |
| D (95 ms) after 100 Mbit/s cap, relayed | 3 | all complete | 93.2 | 93.2-93.3 | 93 | relayed/no_answer |
| D (95 ms) after 1 Gbit/s cap, direct | 3 | all complete | 292.0 | 259.4-319.7 | 29 | relayed+direct |
| D (95 ms) after 1 Gbit/s cap, relayed | 3 | all complete | 232.7 | 215.8-238.1 | 23 | relayed/no_answer |
| D (95 ms) before 100 Mbit/s cap, direct | 3 | all complete | 67.3 | 65.6-67.7 | 67 | relayed+direct |
| D (95 ms) before 100 Mbit/s cap, relayed | 3 | all complete | 88.0 | 88.0-88.4 | 88 | relayed/no_answer |
| D (95 ms) before 1 Gbit/s cap, direct | 3 | all complete | 72.7 | 71.8-74.4 | 7 | relayed+direct |
| D (95 ms) before 1 Gbit/s cap, relayed | 3 | all complete | 87.4 | 87.0-89.4 | 9 | relayed/no_answer |
| E (159 ms) after 100 Mbit/s cap, direct | 3 | all complete | 43.0 | 42.9-90.7 | 43 | relayed+direct |
| E (159 ms) after 100 Mbit/s cap, relayed | 3 | all complete | 91.9 | 91.7-92.1 | 92 | relayed/no_answer |
| E (159 ms) after 1 Gbit/s cap, direct | 3 | all complete | 265.0 | 206.9-279.4 | 26 | relayed+direct |
| E (159 ms) after 1 Gbit/s cap, relayed | 3 | all complete | 136.2 | 130.7-137.9 | 14 | relayed/no_answer |
| E (159 ms) before 100 Mbit/s cap, direct | 3 | all complete | 38.8 | 38.5-38.9 | 39 | relayed+direct |
| E (159 ms) before 100 Mbit/s cap, relayed | 3 | all complete | 49.4 | 47.6-50.6 | 49 | relayed/no_answer |
| E (159 ms) before 1 Gbit/s cap, direct | 3 | all complete | 40.8 | 39.7-40.8 | 4 | relayed+direct |
| E (159 ms) before 1 Gbit/s cap, relayed | 3 | all complete | 47.8 | 47.6-51.2 | 5 | relayed/no_answer |

**With loss** (before only; loss is not a window matter). These runs were
stopped after 60 s, so their goodput is what arrived in those 60 s, slow
start included. The stop is the harness's bound, not a failure. Run to
completion, a 16 MiB file at 1 % loss over 95 ms arrives intact on both
paths, at about the rate one TCP stream gets on the same impaired path
(iperf3: 2.37 Mbit/s):

| case | runs | runs ended | goodput Mbit/s (median) | min-max | % of cap | paths |
|---|---|---|---|---|---|---|
| 16 MiB, D, 1 % loss, direct, before | 1 | all complete | 2.1 | 2.1-2.1 | - | relayed+direct |
| 16 MiB, D, 1 % loss, relayed, before | 1 | all complete | 2.5 | 2.5-2.5 | - | relayed/no_answer |
| 16 MiB, D, 1 % loss, direct, after | 1 | all complete | 2.2 | 2.2-2.2 | - | relayed+direct |
| 16 MiB, D, 1 % loss, relayed, after | 1 | all complete | 2.5 | 2.5-2.5 | - | relayed/no_answer |


All before-fix cells, including the unimpaired 3 ms path (case = receiver-cap-loss-path):

| case | runs | runs ended | goodput Mbit/s (median) | min-max | % of cap | paths |
|---|---|---|---|---|---|---|
| D-100mbit-1-direct | 3 | at the window | 1.9 | 1.7-2.1 | 2 | (none) |
| D-100mbit-1-relayed | 3 | at the window | 2.3 | 1.8-2.4 | 2 | (none) |
| D-100mbit-0.5-direct | 3 | at the window | 2.8 | 2.6-2.9 | 3 | (none) |
| D-100mbit-0.5-relayed | 3 | at the window | 3.4 | 3.1-3.5 | 3 | (none) |
| D-1gbit-1-direct | 1 | at the window | 2.0 | 2.0-2.0 | 0 | (none) |
| D-1gbit-1-relayed | 1 | at the window | 2.0 | 2.0-2.0 | 0 | (none) |
| E-1gbit-1-direct | 3 | at the window | 1.2 | 1.0-1.3 | 0 | (none) |
| E-1gbit-1-relayed | 3 | at the window | 1.4 | 1.3-2.9 | 0 | (none) |
| E-1gbit-0.5-direct | 3 | at the window | 1.7 | 1.4-1.8 | 0 | (none) |
| E-1gbit-0.5-relayed | 3 | at the window | 2.6 | 2.0-3.6 | 0 | (none) |
| A-1gbit-0-direct | 3 | all complete | 892.9 | 886.5-894.8 | 89 | relayed+direct |
| A-1gbit-0-relayed | 3 | all complete | 898.5 | 883.7-899.5 | 90 | relayed/no_answer |
| A-1gbit-1-direct | 3 | at the window | 60.3 | 59.9-62.2 | 6 | (none) |
| A-1gbit-1-relayed | 3 | at the window | 90.4 | 87.6-94.5 | 9 | (none) |
| A-1gbit-0.5-direct | 3 | at the window | 88.9 | 88.0-90.2 | 9 | (none) |
| A-1gbit-0.5-relayed | 3 | all complete | 165.3 | 162.6-167.3 | 17 | relayed/no_answer |
| A-100mbit-0-direct | 3 | all complete | 94.5 | 94.5-94.5 | 94 | relayed+direct |
| A-100mbit-0-relayed | 3 | all complete | 94.4 | 94.3-94.5 | 94 | relayed/no_answer |
| A-100mbit-1-direct | 3 | at the window | 49.6 | 49.6-49.6 | 50 | (none) |
| A-100mbit-1-relayed | 3 | at the window | 59.5 | 57.9-61.0 | 60 | (none) |
| A-100mbit-0.5-direct | 3 | at the window | 69.7 | 68.5-69.9 | 70 | (none) |
| A-100mbit-0.5-relayed | 3 | at the window | 83.9 | 82.9-85.2 | 84 | (none) |

**rmem.** The receiver's `net.core.rmem_max` at Debian's default (212992)
and at the README's 7,500,000, unimpaired, direct, alternated, 3 runs each:

| case | ok | address to received s | MB/s | recv CPU s | paths |
|---|---|---|---|---|---|
| C to A (3 ms), 212992, before | 3/3 | 6.5 | 164.4 | 9.3 | relayed+direct |
| C to A (3 ms), 7,500,000, before | 3/3 | 4.8 | 223.2 | 8.5 | relayed+direct |
| C to D (95 ms), 212992, after | 3/3 | 28.9 | 37.1 | 19.0 | relayed+direct |
| C to D (95 ms), 7,500,000, after | 3/3 | 18.1 | 59.4 | 14.9 | relayed+direct |

At 3 ms the raised buffer gave 36 % more. At 95 ms, after the fix, it gave
60 % more, because a larger window needs more socket buffer behind it.

**Relay in a third region.** C to A (both in Germany, 3 ms apart), with
UDP between them dropped, once through the relay on C and once through
one in Ashburn, alternated, 3 runs each:

| case | ok | address to received s | MB/s | paths |
|---|---|---|---|---|
| relay on C, before | 3/3 | 4.2 | 256.3 | relayed |
| relay in Ashburn, before | 3/3 | 198.2 | 5.4 | relayed/no_answer |
| relay on C, after | 3/3 | 2.2 | 488.1 | relayed |
| relay in Ashburn, after | 3/3 | 48.8 | 22.0 | relayed/no_answer |

## 4. Resume

The receiver is killed (SIGKILL) once its stage holds half the file. Then
4 KiB of the kept part is overwritten, so one chunk no longer verifies, and
a second receive of the same address runs. "Ideal" is what a perfect
resume writes: the missing bytes plus the one bad 256 KiB chunk. The 10 GiB
runs are C to A (3 ms), before and after alternated. The 100 GiB run is A
to B, after.

| file, build | kept at the kill | first receive s | second receive s | second wrote | ideal (missing + one 256 KiB chunk) | beyond the ideal |
|---|---|---|---|---|---|---|
| 10 GiB, C to A, before | 5.4 GB | 21.3 | 22.7 | 5.3 GB | 5.3 GB | 4096 bytes |
| 10 GiB, C to A, after | 5.4 GB | 19.3 | 21.1 | 5.3 GB | 5.3 GB | 4096 bytes |
| 10 GiB, C to A, before | 5.5 GB | 21.3 | 22.4 | 5.3 GB | 5.3 GB | 4096 bytes |
| 10 GiB, C to A, after | 5.5 GB | 19.2 | 20.5 | 5.3 GB | 5.3 GB | 4096 bytes |
| 10 GiB, C to A, before | 5.4 GB | 21.3 | 22.7 | 5.3 GB | 5.3 GB | 4096 bytes |
| 10 GiB, C to A, after | 5.5 GB | 20.3 | 19.3 | 5.3 GB | 5.3 GB | 4096 bytes |
| 100 GiB, A to B, after | 53.7 GB | 601.8 | 704.3 | 53.7 GB | 53.7 GB | 8192 bytes |

Every second receive re-sent the bad chunk and nothing else that had
verified. It came within 8 KiB of the ideal; the kept bytes are read as
allocated disk blocks, so the ideal is only exact to a block. In the first 100 GiB attempt the
harness failed to stop the receiver (`resume.sh` read back the pid of a
shell, not the receiver), so that run went through uninterrupted. It is
reported above as the 100 GiB "before" time. The harness was fixed before
the runs in this table.

## 5. 100 GiB uninterrupted on the longest path (F to G, Ashburn to Singapore, v0.1.9)

On 2026-11-20, with the v0.1.9 release binary at both ends (downloaded
from the GitHub release on each machine, `SHA256SUMS` and its signature
checked as the README says). The path is the one with the longest round
trip between the provider's six regions, measured first with one small
machine in each (20 pings each way, median; both directions within 1 ms,
the table gives the row's side):

| ms | Nuremberg | Helsinki | Ashburn | Hillsboro | Singapore |
|---|---|---|---|---|---|
| Falkenstein | 2.7 | 35.7 | 101 | 166 | 169 |
| Nuremberg | | 23.7 | 91.9 | 163 | 167 |
| Helsinki | | | 107 | 172 | 188 |
| Ashburn | | | | 93.5 | **221** |
| Hillsboro | | | | | 159 |

F (8 dedicated vCPUs) sends; G (4 dedicated vCPUs, 153 GB free before the
run, for the 107.4 GB file and its stage) receives. A private relay
(`nfltr-relay` from nfltr v2.1.77) runs on F, as on C in test 3, and the
sender holds a machine certificate from it. Debian 13, Linux 6.12,
`rmem_max` and `wmem_max` 7,500,000, IPv4 only, page caches dropped
before each run. F reads the file at 3.5 GB/s (`dd iflag=direct`); G
writes 1.7 GB/s (`dd conv=fdatasync`, 10 GiB). The file is `gen.sh file`
of 100 GiB; each run is `run.sh` with `TIMEOUT=14400`.

| run | ok | to address s | address to received s | total s | Mbit/s | first byte s | send CPU s | send RSS MiB | recv CPU s | recv RSS MiB | paths | BLAKE3 received = sent |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 1 | yes | 49.89 | 2395.7 | 2445.6 | 358.6 | 6.1 | 2260 | 153 | 2934 | 143 | relayed+direct | yes |
| 2 | yes | 49.92 | 2187.5 | 2237.5 | 392.7 | 6.2 | 2178 | 151 | 2729 | 155 | relayed+direct | yes |
| median of the 2 | 2/2 | 49.9 | 2291.6 | 2341.5 | 374.8 | 6.2 | 2219 | 152 | 2831 | 149 | relayed+direct | 2/2 |

- **Path**: each transfer started relayed and went direct within its
  first 10 s; the direct path (QUIC over UDP) carried the rest. Of the
  receiver's packets during the run, 99.99 % were UDP (76.7 million
  datagrams against 3,874 and 3,285 TCP segments). Both receivers printed
  `path direct`. Neither moved back to the relay.
- **Rate over the run**: in 10 s intervals while data arrived, run 1
  received a median of 397 Mbit/s (10th to 90th percentile 309 to 472),
  run 2 443 Mbit/s (373 to 479). One 16 MiB window per round trip at
  221 ms allows about 607 Mbit/s. One TCP stream on this path carries
  76 Mbit/s.
- **CPU**: the sender used 0.9 of its 8 vCPUs on average (3.3 while it
  built the address), the receiver 1.2 of its 4 (GNU time: CPU seconds over
  wall seconds). Neither was short of CPU.
- **After the last byte**: the receiver ran on for about 90 s in each run
  with no network traffic, 1.7 vCPUs busy and 1.8 in I/O wait, and then
  exited with the file committed. Its disk read about the whole file once
  more per run (425.9 GB read over the two runs, 214.7 GB of it the two
  BLAKE3 checks below). That time is in "address to received". The read
  is the receiver's check of the whole file on its disk against the
  offered root before it commits (`internal/xfer/receive.go` `verify`;
  `spec/Resume.tla` `Finish`). The per-chunk checks on arrival do not
  replace it: they cannot see a chunk that changes on the disk after it
  was written (`Rot`), and the model's `no-final-check` mutant, which
  commits without it, breaks `Safe`. It read the disk at 1.2 GB/s in
  256 KiB reads. After v0.1.9 it reads 4 MiB at a time; locally (not on
  these machines) that took the last byte to commit of a 20 GiB file from
  44.7 to 33.9 s (`docs/performance.md`).
- **Disk**: the received file took 107.4 GB of G's 153 GB free; 46 GB
  were left after the commit.
- **Integrity**: both runs exited 0 at both ends, and `b3sum` of the
  received file equals the sent one's
  (`e686123bfcba5a5a58ee02edd9faff9e4060d16c3164fa8547c823b18d25cfb7`).
- **To address**: 49.9 s here against 30.1 s on A for the same 100 GiB
  of the same kind of machine. Nearly all of it is the sender reading the
  file to build its BLAKE3 root and outboard: the relay logged the
  sender's registration 51 s after run 1's start, and the address
  follows it at once. That read ran at 2.1 GB/s with 1.4 vCPUs hashing,
  1.8 in the kernel and 3.8 in I/O wait: F's disk (3.5 GB/s with
  `iflag=direct`) against A's (13.7 GB/s), in 256 KiB reads. The reads
  are 4 MiB after v0.1.9; locally (not on F) a 20 GiB file's sender
  start to manifest went from 43.6 to 38.2 s (`docs/performance.md`).

## 6. Between providers (v0.1.9 and v0.1.10)

On 2026-11-21, the v0.1.9 release binary (from the GitHub release on each
machine, `SHA256SUMS` and its signature checked) between machines at the
provider of tests 1 to 5 and Google Cloud machines. Each pair's private
relay (`nfltr-relay` from nfltr v2.1.78) runs on its first machine, which
sends, as in tests 3 and 5; for J to H, J holds a machine certificate
from H's relay.

| machine | vCPU | RAM | disk | where | interface MTU |
|---|---|---|---|---|---|
| H | 4 dedicated | 16 GB | 160 GB NVMe | Falkenstein, DE | 1500 |
| I | 4 dedicated | 16 GB | 160 GB NVMe | Helsinki, FI | 1500 |
| J | 4 (c3-standard-4-lssd) | 16 GB | 375 GB local NVMe SSD | Google Cloud europe-west3 (Frankfurt, DE) | 1460 |
| K | 4 (c3-standard-4-lssd) | 16 GB | 375 GB local NVMe SSD | Google Cloud asia-southeast1 (Singapore) | 1460 |

Debian 13, Linux 6.12, `rmem_max` and `wmem_max` 7,500,000, IPv4 only,
page caches dropped before each run. J and K keep the default network's
firewall: nothing comes in but ssh and ICMP, and no rule was added. The
paths (`baseline.sh`, 5 s per test, the Google Cloud end as the iperf3
client since its firewall closes inbound ports):

| path | RTT ms | TCP 1 stream Mbit/s | TCP 4 streams | UDP Mbit/s received (loss at 2 Gbit/s offered) |
|---|---|---|---|---|
| H to J (Falkenstein to Frankfurt) | 6.3 | 3067 | 12435 | 1682 (0.0 %) |
| J to H | 6.1 | 1660 | 5384 | 1945 (2.7 %) |
| I to K (Helsinki to Singapore) | 260.2 | 59 | 181 | 1614 (0.0 %) |

Each row is `run.sh` (or `resume.sh`) with medians of the runs named; a
"relayed" run drops UDP between the two at the receiver (`wan.sh block`).
"direct at" is from the receiver's first packet to the relay to the first
0.1 s in which 100 datagrams arrived from the sender (packet captures of
both ends).

| run | runs | ok | RTT ms | address to received s | Mbit/s | range | paths | direct at s | BLAKE3 received = sent |
|---|---|---|---|---|---|---|---|---|---|
| 1 GiB, H to J, direct, v0.1.9 | 3 | 3/3 | 6.3 | 6.9 | 1248 | 446-1286 | relayed+direct | 0.23-0.24 | 2/2 (one not checked) |
| 1 GiB, J to H, direct, v0.1.9 | 3 | 3/3 | 6.1 | 6.0 | 1429 | 1368-1461 | relayed+direct | 0.29-0.33 | 3/3 |
| 1 GiB, H to J, relayed, v0.1.9 | 3 | 3/3 | 6.1 | 5.3 | 1630 | 1624-1665 | relayed/no_answer (2), relayed | - | 3/3 |
| 1 GiB, J to H, relayed, v0.1.9 | 3 | 3/3 | 5.8 | 4.7 | 1812 | 1711-2440 | relayed | - | 3/3 |
| 10 GiB, H to J, direct, v0.1.9 | 2 | 2/2 | 5.9 | 51.1 | 1681 | 1668-1693 | relayed+direct | 0.24 | 2/2 |
| 1 GiB, I to K, direct, v0.1.9 | 3 | 3/3 | 260.8 | 36.1 | 238 | 192-262 | relayed+direct | 5.9 | 3/3 |
| 1 GiB, I to K, relayed, v0.1.9 | 3 | 3/3 | 259.5 | 100.2 | 86 | 81-86 | relayed/no_answer (2), relayed/withheld | - | 3/3 |
| 10 GiB, I to K, direct, v0.1.9 | 2 | 0/2 | 260.4 | stopped at 589 and 283, 0.43 and 1.59 GB in | 5.8 and 44.8 | - | (stopped) | 5.9 | - |
| 10 GiB, I to K, direct, v0.1.10 | 2 | 2/2 | 260.0 | 247.2 | 348 | 341-354 | relayed+direct | - | 2/2 |
| 100 GiB, I to K, direct, v0.1.10 | 1 | 1/1 | 261.1 | 2486.3 | 346 | - | relayed+direct | - | 1/1 |

A receiver killed (SIGKILL) at half a 1 GiB file and started again
(`resume.sh`), once per pair: H to J kept 650.6 MB and the second receive
wrote 423.4 MB, the ideal (missing bytes plus one 256 KiB chunk); I to K
kept 599.8 MB and wrote 474.2 MB, the ideal plus 4,096 bytes. Both
resumed files' BLAKE3 equals the sent file's.

- **Through the default firewall**: every direct run went direct with no
  rule added, in both directions on H and J, so no run with a UDP rule
  was needed. The firewall keeps state: the Google Cloud end's datagram
  to the peer opens the way back. In every capture, the machine at the
  other provider sent no datagram to the Google Cloud end before the
  first one from it arrived (the Google Cloud end sent 1 to 4 first). At 6 ms the transfer went direct 0.23-0.33 s
  after the receiver reached the relay; at 260 ms after 5.9 s, the first
  datagram to the peer at 3.9 s. The relay carried the bytes before
  that (first byte 0.1 s and 3.9 s).
- **MTU**: no IP fragments and no ICMP "fragmentation needed" in any
  capture; the large datagrams carry DF. Toward J and K the direct path
  grew to 1,441-byte UDP payloads (1,469-byte IP packets, above the
  receiving interface's 1460), and they arrived. The only ICMP was "port
  unreachable": for datagrams the receiver sends to the relay's HTTPS
  port, which has no UDP listener (2 to 6 per run), and at the end of a
  run for a closed socket.
- **10 GiB, I to K: the direct path slowed down and did not recover**
  on v0.1.9 (closed in v0.1.10, below). Run 1 received 100 and then
  50 Mbit/s in its first 20 s direct, then 3 to 5 Mbit/s for 8 minutes;
  run 2 received 272, 157, 97, 57, 45 and then 21 to 35 Mbit/s over
  4 minutes. Both were stopped by hand (run 1 by SIGQUIT for its goroutine
  dumps, run 2 by SIGTERM), so their exit statuses are not the program's
  verdict. Neither end was busy, the RTT
  stayed at 260 ms under the load, and the wire lost 0.04-0.06 % of the
  datagrams. The sender's path switch waited on its window while the
  receiver waited for data (goroutine dumps of both ends). This path
  reorders: in run 2, 29 % of the datagrams arrived after a later-sent
  one, up to 10 places later (matched between the two ends' captures);
  iperf3 at 200 Mbit/s counted 43,960 of 93,990 datagrams out of order
  from I to K and none from K to I; the captures of H to J show none. QUIC declares a
  packet lost once one sent 3 later is acknowledged (RFC 9002, section
  6.1.1), so reordering past 3 reads as loss, and each loss narrows the
  congestion window; that is the likely cause, not yet shown. TCP on the
  same path (the relayed runs) held 81-86 Mbit/s. The 1 GiB direct runs
  finished at 192-262 Mbit/s before the rate fell far.
- **v0.1.10 on I to K** (the release binary, checked as above, on new
  machines of the same types and places; the relay, its placement and
  J's and K's firewall as before, so the direct path again formed with no
  rule added). The direct path's QUIC uses BBR in v0.1.10 instead of
  NewReno. Both 10 GiB runs finished, at 354 and 341 Mbit/s (receives of
  242.9 s and 251.6 s), and the uninterrupted 100 GiB (107.4 GB) in
  2,486.3 s, 41.4 minutes, at 346 Mbit/s, with 45.3 s more before it to
  print the address (hashing the file). Measured each 10 s at the
  receiver's interface, the rate held 287-412 Mbit/s once the transfer
  was direct (100 GiB: 337-412, median 390), with no 30 s under
  50 Mbit/s; each was one session, with no resume, and each received
  file's BLAKE3 equals the sent one's. K counted no UDP receive-buffer
  drops in any of the three. Peak resident memory: the sender 110-114 MB
  for 10 GiB and 163 MB for 100 GiB, the receiver 124-170 MB and 232 MB.
  CPU: the sender used 277-282 CPU s per 10 GiB and 2,631 for 100 GiB
  (about one of its 4 vCPUs), the receiver 160-163 and 1,485. A
  pre-release build of the same code used 96-108 and 121-123 on the
  earlier machines (lane REORDER): interleaved behind netem the two use the
  same CPU per GiB (`bench/cpu/netem.sh`, lane SENDCPU). Here the
  receiver sent 1.8 times the ACKs (3.3-3.5 M per 10 GiB: a packet the
  last ACK reported missing is acknowledged at once, so the count follows
  the path's reordering), which accounts for the receiver's difference,
  and the sender machine cost more per byte (it also hashed the address
  1.5 times slower).
- **Relayed above direct at 6 ms**: on H and J, the relayed path (TCP to
  the relay on the sender) moved 1,624 to 2,440 Mbit/s and the direct path
  (QUIC) 1,248 to 1,461 Mbit/s. The relayed runs also took about half
  the CPU: 3.3-4.4 CPU s at each end per GiB, against 6.1-10.3 direct. One H to J direct run took
  19.3 s against 6.7-6.9 for the others; why is not known.
- **Receive buffers**: with `rmem_max` at the README's 7,500,000, the
  receivers counted UDP receive-buffer drops (`UdpRcvbufErrors`): 28,582 on
  H, 1,429 on J and 53,409 on K over the session.
- **CPU, 10 GiB H to J**: the sender used 66 CPU s in 51 s, the receiver
  84 (1.3 and 1.6 of 4 vCPUs).

## Limits

- Each configuration ran one at a time, on cloud machines; B's vCPUs are
  shared, and the receivers in test 3 have 2 vCPUs.
- netem's loss is uniform at random, which is harsher on congestion control
  than most real loss. Its cap models a bottleneck link, not any
  particular network.
- IPv4 only.
- Tests 1 to 5 are from one day's set of paths, on one provider's backbone
  between its regions; test 6 crosses between two providers' networks;
  none is the public internet's last miles.
- The "after" binaries are a local build of v0.1.2 with the relay client
  patched, not a release (tests 1 to 4). Test 5 is the v0.1.9 release:
  2 runs on one pair of machines on one evening.
- The v0.1.10 rows of test 6 are 2 runs of 10 GiB and one of 100 GiB, on
  one pair of machines in one afternoon.
