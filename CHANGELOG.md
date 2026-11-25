# Changelog

## Unreleased

- **Both ends show their progress.** A transfer of minutes printed
  nothing until its end. On a terminal each end now redraws one line each
  second: bytes received (sent) of the total, the share, the rate, the
  time left, the path (direct or relayed) and the sessions open; off a
  terminal it prints that line every 30 s; the receiver with `--json`
  nothing. What a resumed receive already held counts as done at both
  ends: the receiver names it in its want (`held`, read for the progress
  only; an older end sends or reads none, and its sender counts only what
  it sent). The sender's line runs while a receiver is connected; two
  receivers at once share it.
- **A cut session reads as what it is.** The sender logged a session of a
  several-session transfer that was cut (the receiver drops a slow one, or
  a network fails under it) as `WARN transfer ended ... session reset by
  the peer`, which read as a failed transfer while the receiver fetched
  that part again. It now logs `INFO one of the receiver's N sessions
  ended after X bytes; the receiver resumes its part`; `WARN transfer
  ended` is kept for a transfer that ended. The line for each session
  that carried its part ("part of the transfer in") is debug only. The
  sender's `sent ...` line adds the transfer's rate and path, its time
  now from the transfer's first session (was: the last session's). The
  sender's messages go through one text log (`level=INFO msg=...`, as
  the relay SDK's already did) that clears the progress line first.

## v0.1.14

- **A receiver uses less CPU on Linux.** It writes each whole 256 KiB chunk
  straight to the disk (O_DIRECT), not through the page cache, and reads
  each chunk from the session straight into its buffer. Between two 8-vCPU
  cloud machines in one zone, a 100 GiB direct transfer took 11 % less CPU
  per GiB on the receiver (7.06 -> 6.27 CPU-s), and a 10 GiB one ended
  1.7 s sooner (nothing left to flush before the commit). A file system
  that refuses O_DIRECT is written through the page cache as before.
  blake3 v1.5.1 verifies each chunk group in place, not through a copy
  (5 % less time per 256 KiB group), and pathswitch v0.1.5 reads each
  DATA payload in place, not through a frame buffer.
- **BLAKE3 hashes and verifies faster on amd64.** blake3 v1.5.2 hashes a
  256 KiB chunk group's chunks first and then its parents a level at a
  time (every SIMD lane busy), and its amd64 kernels load and transpose
  each block instead of gathering each word, which is slow on Intel CPUs
  with the Gather Data Sampling mitigation (Skylake to Ice Lake). One core
  verifies a chunk group 84 % faster on a GCP n2 (Cascade Lake), 17 % on
  a c3 (Sapphire Rapids), 3 % on arm64; in a 10 GiB transfer the
  receiver's BLAKE3 took 37 % less CPU on n2 and 20 % less on c3, about
  1 % of the receiver's whole CPU (QUIC and the copies are most of it);
  the sender's tree build hashes the same way (3 % less sender CPU).

- **One direct connection on a long path is no longer held at 0.35
  Gbit/s.** A direct session's window now grows from 16 MiB to 64 MiB
  while the receiver keeps up (relay client v0.25.2, quiclink v0.10.0),
  and the QUIC fork lets 65,536 packets be in flight (was 10,000, 14.5 MB)
  with as many gaps in a stream (QUIC fork v0.63.1-nfltr.11). *Measured on
  the candidate*, Helsinki to Singapore (260 ms), 10 GiB direct: 0.36 ->
  0.99 Gbit/s with 1 connection, 1.20 -> 1.71 Gbit/s with 4 (medians).
  A direct session can now hold more memory: up to about 64 MiB in flight
  at the sender and 160 MiB buffered at the receiver; on that path, with
  4 connections, peak RSS rose from 0.3-0.4 GB to 0.9-1.5 GB at the
  sender and from 0.2-0.3 GB to 0.65-0.8 GB at the receiver.
- **Late packets are acknowledged, not sent again.** On a path with more
  than 64 holes open at once, the receiving end's ACK dropped its lowest
  ranges, so a packet arriving late below them was never acknowledged and
  its sender declared it lost and resent it. An ACK now reports first the
  ranges holding packets received since the last ACK. *Measured on the
  candidate*, Helsinki to Singapore, 4 connections: 0.8-3.3 % of the
  packets resent -> about 0.01 %, 6 % less CPU per GiB on the sender and
  3 % less on the receiver, the rate unchanged within the path's spread.
- **No drops in the sender's own queue.** The QUIC fork asked a 7 MiB
  socket send buffer, so one connection queued about 5,000 packets (40-55
  ms) in the host before the network card, and Linux's default qdisc
  (fq_codel) dropped whole batches of 44 packets; it now asks 256 KiB (a
  larger buffer the host gives is kept). *Measured on the candidate*,
  Helsinki to Singapore: with 4 connections 279 qdisc drops per run -> 0
  and 0.11-0.12 % of packets resent -> 0.01-0.02 % at the same rate, but
  7 % more sender CPU per GiB (the send waits on a full socket); with 1
  connection and the larger windows 0.74 -> 1.25 Gbit/s, 0.25 % resent
  -> 0, 16 % less sender CPU per GiB.
- **ACK processing no longer slows with the packets in flight.** The QUIC
  fork's ACK and loss detection walked every packet in flight; they now
  walk only the packets acknowledged or declared lost (the fork's
  benchmark at 50,000 in flight: 2.5 -> 0.3 us per packet). On the real
  path above the CPU per GiB did not change outside the run-to-run spread.
- **A relayed session's window grows with the path** (link v0.14.0): it
  doubles, up to 16 MiB per session and 64 MiB per connection, while its
  sender waits for credit and its reader keeps up; before, a relayed
  session moved one fixed window per round trip. It needs the relay on
  link v0.14.0 or later too; with an older relay the windows stay fixed,
  as before. Link's own benchmark (one session over 20 ms): a 1 MiB
  window 44 -> 278 MB/s; not measured with nfltr-send.
- **A relay dial that fails before any byte is tried again** (relay client
  v0.25.1): a connection to the relay that failed before it sent anything
  (for example "no route to host" just after a network drop) is asked
  again within the open's bound, instead of exit 5 at once.
- Protocol 5, as v0.1.13; link, quiclink and pathswitch change nothing on
  the wire between the two ends.
- Built on link v0.14.0, pathswitch v0.1.5, quiclink v0.10.0 (QUIC fork
  v0.63.1-nfltr.11), blake3 v1.5.2, relay client v0.25.2, relay proto
  v0.22.3 and relay-core v0.19.8.

## v0.1.13

- **A direct transfer uses less CPU.** On a direct path each byte is now
  encrypted once, by the direct QUIC connection's TLS 1.3 (it already
  checks the other end's pinned key and is bound to the session), instead
  of a second time by the session's end-to-end TLS inside it; a relayed
  path keeps the session's TLS (relay client v0.25.0). Between two 8-vCPU
  cloud machines in one zone, a 10 GiB direct transfer took 6 % less CPU
  per GiB on the sender and 6-8 % less on the receiver, with
  `--connections` 4 and 8 (4 runs each, run in turn with the code before).
  Its rate did not change on that pair, whose limit was the sending
  machine's packet path, not CPU; relayed transfers are unchanged. BLAKE3
  equal in every run.
- **Hard cut on the direct path: both ends need v0.1.13 for direct.**
  v0.1.13 and v0.1.12 still speak protocol 5, but their direct paths
  differ, so between them no direct path forms:
  - a sender with an account carries the transfer relayed (every byte,
    as before), both ways;
  - with a sender that has no account (direct only), a v0.1.13 receiver
    exits 12 (`direct_required`) at once, and a v0.1.12 receiver exits 6
    (a protocol error: its reset names the sender's reason, no direct path
    and no account). Nothing is written.
  Against v0.1.11 and earlier the refusals of v0.1.12 hold (exit 4 and 6).
- **Fewer acknowledgements on paths that reorder deeply.** v0.1.12's ACK
  Frequency did not reduce acknowledgements on a real long path (Helsinki
  to Singapore, 260 ms): on a path that reorders packets by more than 10
  places, the receiving end still acknowledged each such late packet at
  once. Now, once the sending end sees that reordering, it asks again for
  no immediate acknowledgement of reordered packets (quiclink v0.9.0, its
  QUIC fork v0.63.1-nfltr.9). This aims to cut acknowledgements and
  their CPU on such paths; it is not measured on a real path yet.
  *Measured 2026-11-22* (docs/scale.md, test 8): from Helsinki to
  Singapore (260 ms) about 0.09 ACKs per data packet at 1 and 4
  connections, reordering forcing 0.04 % of them; 10 GiB at 1.21 Gbit/s
  with 4 connections, 100 GiB at 1.36 Gbit/s; CPU per GiB 8-17 % lower
  at both ends than v0.1.12 on the same pair.
- Built on link v0.13.3, pathswitch v0.1.4, quiclink v0.9.0, relay client
  v0.25.0, relay proto v0.22.2 and relay-core v0.19.6.

## v0.1.12

- **One direct transfer runs over several connections and is more than
  twice as fast.** Between two 8-vCPU machines in one cloud zone (0.2 ms,
  15.6 Gbit/s on one TCP stream), a 10 GiB direct transfer ran at
  5.88 Gbit/s with the default `--connections 4` and 6.22 with
  `--connections 8` (medians of 3; v0.1.11 on the same machines, run in
  turn with it: 2.59), 100 GiB at 6.82 with 8; relayed with 4, 4.79
  (v0.1.11: 3.26). BLAKE3 equal in every run ([docs/scale.md](docs/scale.md)
  test 7). The receiver's CPU is now the limit.
- **Protocol version 5, a receive striped over sessions**: the
  receiver opens up to `--connections` sessions at once (default 4), each
  on a connection of its own, so each one's packets have a core at both
  ends. Each session's want names a set of the chunks still to fetch
  (interleaved 64 MiB runs); the sender sends that set in order, the
  receiver verifies and writes each chunk in place. A session that ends
  early (its connection cut, a chunk that does not verify) puts what it
  did not write back for another session; the slowest session is closed
  and its rest split once the others are done; one commit after the last
  session and the final check. Every session but the last is answered
  `part` (a `--once` sender serves on). A transfer of fewer 64 MiB runs
  than `--connections` takes one session per run (`spec/Stripe.tla`,
  checked before the code; its traces by `spec/TraceStripe.tla`).
- **The receiver reads nothing back before the commit** of a file it
  verified chunk by chunk and wrote whole in this receive (the full re-read
  and re-hash cost about 1 CPU s per GiB and 1.5 s at the end of a 10 GiB
  receive); a file part of which a cut transfer left is verified on disk as
  before, and another program's write to a staged file once it is complete
  still refuses the commit (its mtime). `spec/Stripe.tla` `Trusted`.
- **A striped receive's writers take turns** on one file instead of
  spinning on the kernel's inode lock, into blocks preallocated on Linux:
  4 writers on ext4 1.47 -> 0.58 CPU s per GiB (`BenchmarkStripedWrite`).
- **The direct path asks for fewer acknowledgements** (QUIC's ACK
  Frequency extension, quiclink v0.8.0): the receiver acknowledges every
  10 packets and only a packet more than 10 places late at once. On a
  simulated path that reorders 30 % of its packets, 0.39 -> 0.10 ACK
  packets per data packet at the same rate; the CPU this saves on a real
  reordering path is not measured yet. 
  Measured after the release on a real long path (Helsinki to Singapore,
  260 ms, 10 GiB, 3 runs each): it did not reduce acknowledgements.
  v0.1.11 already sent about one ACK per 7 data packets there; v0.1.12
  on one connection sent about one per 3 in two of three runs, for more
  CPU. With the default 4 connections ACKs were near v0.1.11's and the
  transfer ran 3.1 times as fast (1.09 against 0.35 Gbit/s). The cause
  is being worked on.
- **Hard cut: both ends need v0.1.12.** v0.1.11 and earlier and this
  release refuse each other, neither retries, nothing is written:
  - a v0.1.12 receiver of an older sender exits 4 (`refused`):
    `refused: the sender speaks protocol version 4, this receiver 5 to 5:
    update the older end`;
  - an older receiver of a v0.1.12 sender exits 6, a protocol error (a
    frame it does not expect: the sender's answer naming both versions);
  - with a sender that has no account (direct only), the direct path does
    not form between the two (the QUIC peer without ACK Frequency is
    refused), so the receiver exits 12 (`direct_required`) once its
    direct attempts fail (2 s to about a minute on one machine); the
    other two refuse at the first frame.
- **A sender whose move to another relay replica failed moves when the
  relay asks again**; before, it ignored the second ask and stayed where
  the relay was rebalancing away from (relay client v0.24.5).
- `--connections <n>` (receive, 1 to 16, default 4). 8 was faster than 4
  on the machines above; the default stays 4 until 8 is measured on
  slower receivers and long paths.
- Built on link v0.13.3, pathswitch v0.1.4, quiclink v0.8.0 (QUIC fork
  v0.63.1-nfltr.7), relay client v0.24.5, relay proto v0.22.1 and
  relay-core v0.19.5.

## v0.1.11

- **Direct transfers are faster on fast links, for less CPU.** Between
  two 8-vCPU machines in one cloud zone (0.2 ms, 15.6 Gbit/s on one TCP
  stream), a 10 GiB direct transfer ran at 2.71 Gbit/s (median of 3;
  v0.1.10 on the same machines, run in turn with it: 1.54) and 100 GiB
  at 2.55, BLAKE3 equal. The sender spent 6.8 CPU s per GiB (v0.1.10:
  10.3) and the receiver 8.4 (10.1). The relayed path is unchanged:
  3.26 Gbit/s on both versions ([docs/scale.md](docs/scale.md) test 7).
  From the path switch, whose send and receive buffers are now rings: an
  acknowledgement no longer moves every unacknowledged byte to the front
  of its buffer (pathswitch v0.1.4); and from the direct path's QUIC
  library: on Linux the receiver reads datagrams in batches (UDP GRO), a
  send batch holds up to 44 packets (was 14), and a connection queues up
  to 4096 received packets (quiclink v0.7.4).
- **A closed session no longer leaves a write hanging**: a direct
  session's write blocked on the peer's window returns an error once the
  session is closed; before, it waited (quiclink v0.7.4).
- **CPU benchmarks**: `make bench-line` measures one direct connection
  on loopback (2 GiB, 6 runs) with a CPU profile; `make bench-ab` runs
  the direct and relayed CPU benchmarks (`BenchmarkDirectCPU`,
  `BenchmarkRelayedCPU`) of builds that differ only in their
  dependencies, in turn.
- The protocol and the wire are unchanged from v0.1.10.
- Built on link v0.13.3, pathswitch v0.1.4, quiclink v0.7.4, relay client
  v0.24.4, relay proto v0.22.1 and relay-core v0.19.4.

## v0.1.10

- **A sender whose machine was off past its certificate's life serves
  again**: the expired certificate is renewed before the first connection
  presents it (before: the relay refused it for good and `send` exited 6).
  A refused machine's error is one line: no certificate dump, no relay
  answer, `nfltr config login` named once; it no longer says `--relay
  names another`.
- **A machine certificate renews through a pinned private relay**: the
  renewal trusts the relay certificate that `--cert-sha256` (else
  `$NFLTR_RELAY_CERT_SHA256`) pins, as the sessions do; before, it used
  the system roots only, a self-signed relay refused every renewal and
  the certificate lapsed.
- **Direct transfers on long, lossy paths**: the direct path's QUIC uses
  BBR instead of NewReno (link quiclink v0.7.2). A 10 GiB direct transfer
  Helsinki to Singapore (260 ms, 0.03-0.06 % loss, 29 % of packets
  reordered) ran at a steady 352-359 Mbit/s; before, 36-166 Mbit/s,
  falling to 15-59. The wire is unchanged.
- **Hole punching**: the dialling end sends its first QUIC packet half a
  relay round trip after the sync, once the other end's first probe has
  opened its NAT (before: at the sync, and the first packet met a closed
  NAT); an attempt that no candidate answered is followed by another, at
  most three per round, before the transfer stays relayed; a round for a
  session that has ended stops (relay client v0.24.1).
- Built on link v0.13.3, relay client v0.24.1, relay proto v0.22.1 and
  relay-core v0.19.1.

From the v1.0 surface review ([docs/v1-surface-review.md](docs/v1-surface-review.md)).
These change what scripts see; v1.0 keeps them.

- **receive `--json`: `error` is now `message`, `first_byte_s` is now
  `first_byte_seconds`**, the names nfltr's `cat` events and metrics use.
  A script reading either key reads the new one.
- **SIGHUP stops the sender cleanly, exit 129**: its sessions close at
  once (before, the process died and its receivers waited for the link's
  liveness). The shell reported 129 for that death before too. A
  receive stopped by SIGHUP exits 6 and resumes, as at SIGINT.
- **Flags may follow the files**: `nfltr-send send report.pdf --once`
  now sends `report.pdf` once (before: a file named `--once`, exit 1).
  After `--` every word is a file.
- **The module has no Go API**: `xfer` is now `internal/xfer`. Nothing
  outside this repository imported it.
- The built binary `cmd/nfltr-send/nfltr-send` committed in v0.1.8 is
  removed, and a test keeps binaries out.
- **`--relay-cert-sha256` is now `--cert-sha256`**, the name `nfltr config
  set-relay` and nfltr's `cat` commands use (one name in all three tools);
  `--relay-cert-sha256` is refused. `$NFLTR_RELAY_CERT_SHA256` is
  unchanged.
- **Large files: the address and the commit come sooner.** The sender's
  BLAKE3 tree and the receiver's check of the file on its disk read
  4 MiB at a time instead of 256 KiB; from a cold page cache that read
  the disk 1.6 times as fast (local, docs/performance.md).
- **A file changed while a later one was sent is no longer committed as
  offered.** The sender checks every file's size and modification time
  again before it ends the transfer (an earlier one changed: refused,
  "changed since it was offered"), and the receiver checks every staged
  file again before it commits (one another program wrote after it was
  verified: the commit is refused, the stage kept, and the next session
  sends what no longer verifies). Model `spec/Offer.tla` (lane SENDMULTI).

## v0.1.9

- **The machine certificate renews itself after an outage.** A sender
  that keeps serving renews its certificate at half its lifetime, up to
  10 % later (before: at two thirds), and while the relay cannot be
  reached it keeps retrying, also past the certificate's expiry: a relay
  renews an expired certificate within its grace (90 days), so the sender
  needs no new `nfltr config login` once its relay answers again. Before,
  renewal stopped at the expiry. Only a final refusal (the machine
  revoked, or expired past the grace) stops it, and the sender logs that
  (relay client v0.23.11).
- **Built on link v0.13.0, the API link 1.0 keeps.** The wire is
  unchanged: a v0.1.9 end works with a v0.1.8 end, both ways, relayed and
  direct (`make cross-version` now checks it against v0.1.8).
  A relay built on it also ends two joined sessions whose ends cross
  cleanly; before, both ends could be reset 30 s after reading every byte.
- **Sessions ride the relay's agent link, or its gRPC streams**: the
  relay client no longer opens separate data connections to a relay that
  offers them (relay client v0.23.12).
- Built on link v0.13.0, relay client v0.23.12, relay proto v0.21.5 and
  relay-core v0.17.0.

## v0.1.8

These change what scripts see; v1.0 keeps them.

- **Exit codes are the relay SDK's, the same as nfltr-pipe's.** Exit 3
  (`failed`) is split: 3 `not_found` (the sender is gone), 5
  `unreachable` (the relay or the sender not reached), 6 `reset` (the
  session cut), 7 `e2ee_pin_mismatch`, 10 `e2ee_client_refused`; a
  refusal (a name exists, the sender busy, a commit refused) is 4
  `refused`, no longer 2; 2 is now `auth` (the relay refused the sender's
  certificate, or serves no address without an account) and 9
  `balance_exhausted`. receive `--json`'s `code` names them so.
- **A stopped sender exits 130 (SIGINT) or 143 (SIGTERM)**, no longer 0,
  so a script tells it from a `--once` sender whose transfer completed
  (0).
- **`receive` takes no relay flag.** `--relay` and `--relay-http` were
  ignored by receive (the address names its relay); now each is a usage
  error (exit 1) that says so. `$NFLTR_SEND_RELAY` stays send's only:
  receive ignores it.
- **`--relay-pin` is now `--relay-cert-sha256`**, the name of its
  variable `$NFLTR_RELAY_CERT_SHA256` and of nfltr's `config set-relay
  --cert-sha256`.
- **`--direct` takes `on` or `off` only**; `true`, `false` and an empty
  value are usage errors.
- **`send -h` and `receive -h` exit 0**, as `help` does.
- **`nfltr-send version`** (and `--version`) prints the release, its
  commit and the Go version.
- **Protocol versions are negotiated.** Every 1.x end speaks version 4;
  a receiver takes any version from 4 to its own and both speak the
  lower, so a later 1.x sender works with an earlier 1.x receiver.

- **A relayed close waits for the other end.** A relayed session's close
  returned once the relay had the last bytes; it now returns once the
  other end acknowledged them through the relay, as a direct close does,
  and gives up when that end stays silent past its bound (link v0.12.20,
  relay client v0.23.10, relay-core v0.16.9).
- **A reset before the sender's answer is a reset, not a refused secret.**
  A receive whose session was reset before the sender answered could exit
  10 (`e2ee_client_refused`); it now exits 6 (`reset`): only a sender that
  refused the address's secret is exit 10 (relay client v0.23.10).
- **The contract of a 1.x release:** [docs/v1-contract.md](docs/v1-contract.md)
  lists the commands, flags, environment variables, exit codes, the
  `--json` keys, the address format, the protocol version and the
  compatibility rule; a test fails when the code and it differ.
- **`make cross-version`:** the previous release, downloaded from GitHub
  and verified, against this checkout, both ways (a sender of one, a
  receiver of the other), relayed and direct, on a private relay; in
  the check workflow.
- Built on link v0.12.20, relay client v0.23.10, relay proto v0.21.4 and
  relay-core v0.16.9.

## v0.1.7

- **A stopped receive tells the sender at once.** A receive you stop
  (Ctrl-C) on the direct path now resets the session before it exits, so
  the sender sees that session end within about a second; before, it took
  the stop for a lost path and waited about 30 s (relay client v0.23.8).
- **A slow, lossy direct transfer is no longer cut at its close.** Closing
  a direct-path session waited at most 30 s for the peer to acknowledge
  the last bytes, then reset it; on a slow, lossy path the tail could take
  longer. Now it waits as long as the peer keeps answering, and gives up
  5 s after the peer goes silent (link v0.12.18, relay client v0.23.8).
- **Bytes already received survive a reset.** On the direct path, bytes
  this end had taken before the session was reset stay readable, instead
  of being dropped with the reset (link v0.12.18).
- Built on link v0.12.18, relay client v0.23.8, relay proto v0.21.2 and
  relay-core v0.16.7.

## v0.1.6

- **The relay you chose is the one used.** nfltr-send takes its relay from
  `--relay`, else `$NFLTR_SEND_RELAY`, else the relay `nfltr config
  set-relay` saved, else `grpc.nfltr.xyz:443`. Reading the saved relay
  reads only its address and pin, never an account or key. Before, it
  always dialled nfltr.xyz.
- **Connect errors name the relay.** A send that cannot reach its relay
  says which relay it tried and that `--relay` names another (`relay
  <address> (--relay names another): ...`); a machine certificate made for
  another relay says so and names `--relay`.
- **A `--once` sender always learns that the transfer finished.** When the
  network dropped right after the receiver saved every file, the receive
  exited 0 but the sender could keep serving. The receiver now waits for
  the sender to confirm and, if the session was cut first, tells it again.
- **A sender whose direct-path receiver was killed exits promptly.** After
  a later receive completed, such a `--once` sender took over 20 s to
  exit; now about 5 s. The reliability matrix's `cut-receiver-*-direct`
  cells failed on this (relay client v0.23.7).
- **A cut stream is never taken for a whole one** (link v0.12.17): a
  session this side resets ends its reads with an error, not a clean end.
- The install snippet installs into `~/.local/bin` and creates it first.
- `make reliability` runs nfltr-send built from the checkout (released
  binaries with `SEND_VERSION=`), so it can prove a commit before release.
- Built on link v0.12.17, relay client v0.23.7, relay proto v0.21.1 and
  relay-core v0.16.6.
