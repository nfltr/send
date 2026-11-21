# Reliability matrix and soak

`make reliability` and `make reliability-soak` run nfltr-send through a
fixed set of network conditions and faults on one Linux machine with
docker, and judge every transfer by exit codes and by what arrived: the
received tree must equal the sent one (every name, type and SHA-256), with
nothing missing and nothing extra. Results: [`docs/reliability.md`](../../docs/reliability.md).

`run.sh` starts `rel.py` in a privileged container (it needs network
namespaces, netem, iptables and tmpfs mounts). Inside it a private nfltr
relay (`nfltr-relay serve`, self-signed, nf1 addresses on, addresses
without an account allowed) listens on a bridge; the sender and the
receiver each have a network namespace on that bridge. netem on the two
bridge ports adds half the round trip in each direction and applies the
rate cap and the loss to what reaches each end, so the direct path and the
relayed path cross the same impairment. Offloads are off on every port, so
netem sees wire-sized packets. The sender holds a machine certificate from
the relay (enrolled with `nfltr config set-relay`), so the relay can carry
its transfers.

## Cells

| cells | what |
|---|---|
| `net-<rate>-<rtt>-loss<l>-<path>` | rates 10, 100, 1000 Mbit/s x round trip 1, 40, 150 ms x loss 0, 1 % x direct, relayed (the receiver's `--direct off`). One file: 4, 16, 64 MiB by rate without loss, 4 MiB with loss |
| `cut-<victim>-<at>pct-<path>` | 64 MiB at 100 Mbit/s and 40 ms. Once the receiver's stage holds 25, 50 or 75 % of the file, SIGKILL the receiver (then receive the same address again) or the sender (the first receiver must end on its own; then a new sender of the same file and a receive of its address into the same directory, which resumes) |
| `small-files-<path>` | 10,000 files of 4 KiB in 10 directories (`REL_SMALL`) |
| `large-file-<path>` | one 4 GiB file (`REL_LARGE`), no impairment |
| `anon-direct` | a sender without a certificate (no account): direct only |
| `anon-no-udp` | the same with UDP between the two ends dropped: the receiver must exit 12 (`direct_required`) with nothing in the receiving directory |
| `disk-full` | 32 MiB into a tmpfs of 24 MiB: the receive must fail with nothing visible in the directory; then the tmpfs grows and a receive of the same address completes |

A run passes when the receiver exits as expected (0, or 12 for
`anon-no-udp`), the received tree equals the sent one (or is empty when
nothing may be committed), no stage is left after a commit, and a
`--once` sender exits 0 within 10 s of a completed transfer. Failure
reasons: `exit_<n>`, `timeout`, `sender_hang` (a `--once` sender still
serving 10 s after the receiver committed), `sender_exit_<n>`,
`committed_on_failure` (a receive that exited non-zero left the offer's
files, right ones, in place; `first_` when it was the receive cut by the
sender's death), `missing`, `stage_left`, and `corrupt`: a visible file
that differs from the one sent in any byte, or a name never sent. A run
whose receiver exited 0 with a file missing or any corrupt run counts as
`corrupted`, which must be 0. Each cell runs `REL_N` times (default 5).

The soak (`make reliability-soak`, `REL_SOAK_S`, default 24 h) runs random
cells (all but the large file; `REL_SEED`), half of the `net-` ones with a
fault at a random point between 10 and 90 %: the receiver or the sender
killed, or the link dropped (100 % loss) for 2 to 15 s.

## Output

In `REL_OUT` (default `$TMPDIR/nfltr-send-rel-<mode>-<time>`):

- `runs.jsonl` (matrix) or `soak.jsonl`: one line per transfer: cell,
  bytes, fault, exit status, seconds (receiver start to its exit, faults
  included), Mbit/s, the receiver's `--json` fields (`paths`, `sessions`,
  `written`, `first_byte_seconds`), ok and the failure reason.
- `cells.jsonl`: one line per cell: runs, passes, pass rate, median
  seconds and Mbit/s of the passing runs, the paths they took, failures by
  reason.
- `matrix-summary.json` / `soak-summary.json`: totals, failures by reason,
  `corrupted`, the failed cells, the machine (kernel, CPUs, load,
  `net.core.rmem_max`) and the binaries' versions; `*-summary.md`, the
  per-cell table.
- `logs/<cell>/<run>/`: the sender's and receiver's output of every failed
  run, and `received.json`, what it left in the receiving directory (each
  path's SHA-256); a passing run's logs are removed. `relay.log`.

## Running it

```console
$ make reliability                                  # nfltr-send from this checkout, nfltr v2.1.78
$ make reliability SEND_VERSION=v0.1.5 NFLTR_VERSION=v2.1.74   # released binaries
$ make reliability REL_BIN=/path/to/bins            # nfltr-send, nfltr, nfltr-relay
$ make reliability REL_N=1 REL_CELLS='^cut-'        # a subset
$ make reliability-soak REL_SOAK_S=1800
$ make reliability-soak-relayed                     # 25 minutes, relayed cells, REL_SEED 17 unless set
```

nfltr-send is built from the checkout unless `SEND_VERSION` names a
release; released binaries are fetched and verified by `fetch.sh`
(nfltr-send's signed `SHA256SUMS`, nfltr's installer). It exits 0 only when every
transfer passed.

## Cross-version

`make cross-version` (`cross.sh`, a check workflow job; on the nfltr CI
runners `make ci-request REPO=send REF=<sha> TARGET=cross-version`)
runs the previous release, `SEND_PREV` in the Makefile (downloaded from its
GitHub release by `fetch.sh`, `SHA256SUMS.sshsig` verified against
`release/allowed_signers` and the binary against `SHA256SUMS`), against
nfltr-send built from the checkout, on this harness's private relay, twice:
the old one sends and the new one receives (`REL_SEND_S`, `REL_SEND_R`), then the reverse. Cells (`CROSS_CELLS`): a file at 100 Mbit/s relayed and direct, and `anon-direct`; `REL_N` runs each (default 1). Protocol 5 is a hard cut, so each run must refuse at once with nothing written (`REL_EXPECT`): the new receiver exits 4, the old receiver 6.
Results in `REL_OUT/<first>-<second>/`. It exits 0 only when every run of
both ways refused so. A release moves `SEND_PREV` to itself.

```console
$ make cross-version
$ make cross-version SEND_PREV=v0.1.0 REL_N=3
```

Limits: one machine, so the round trip and the loss are netem's, not a
real path's, and every cell shares the machine's CPU with whatever else
runs there (each run records the load average). The host's
`net.core.rmem_max` (not namespaced) applies to the direct path's UDP
buffers; the summary records it. netem drops packets at random, not in
bursts.
