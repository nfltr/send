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
start to its exit with the files committed.

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
the direct path takes 68 % and 43 % of the cap, while the relayed path
(TCP) takes 92-93 %. This is the same before and after the fix, so it is
not the window. The direct receiver took in 758,915 UDP datagrams for the
1 GiB file, about one per packet of data, so the shortfall is not
retransmission. Without the cap, on the same paths, the direct path is the
faster of the two. Why QUIC's sender stays below a rate-limited bottleneck
is left for the QUIC transport (quic-go's congestion control and pacing),
and is noted here so a capped link is not mistaken for this window.

## Machines and paths

| machine | role | vCPU | RAM | disk | region |
|---|---|---|---|---|---|
| A | sender (tests 1, 2, 4) | 8 dedicated (AMD EPYC Milan) | 32 GB | 240 GB NVMe | Falkenstein, DE |
| B | receiver (tests 1, 2, 4) | 4 shared (AMD EPYC Genoa) | 8 GB | 160 GB NVMe | Helsinki, FI |
| C | sender and relay (test 3) | 2 dedicated | 8 GB | 80 GB | Nuremberg, DE |
| D | receiver (test 3); the third-region relay | 2 dedicated | 8 GB | 80 GB | Ashburn, US |
| E | receiver (test 3) | 2 dedicated | 8 GB | 80 GB | Singapore |

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
- **100 GiB, after**: the resume run's two receives moved 53.7 GB in 602 s
  (89 MB/s) and 53.7 GB in 704 s (76 MB/s); see test 4.
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

## Limits

- Each configuration ran one at a time, on cloud machines; B's vCPUs are
  shared, and the receivers in test 3 have 2 vCPUs.
- netem's loss is uniform at random, which is harsher on congestion control
  than most real loss. Its cap models a bottleneck link, not any
  particular network.
- IPv4 only.
- All runs are from one day's set of paths, on one provider's backbone
  between its regions, not the public internet's last miles.
- The "after" binaries are a local build of v0.1.2 with the relay client
  patched, not a release.
