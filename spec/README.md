# spec: nfltr-send's resume (TLA+)

`Resume.tla` models one file of a transfer on the receiving side across
sessions that end at any point, receiver crashes that lose or tear the tail
of what it wrote, writes that land wrong, and a sender restarted with
another file. `make spec` (TLC 1.8.0, fetched by `scripts/fetch-tla.sh`; or `spec/check.sh` with `TLA_JAVA`, `TLA_JAR`) runs the model
with `-coverage 1`, each witness (must be violated) and each mutant (must
be caught). N = 3 chunks, two chunk values, two faults: 608 states.

| invariant / property | meaning |
|---|---|
| `Safe` | only the sender's file is ever committed, whole |
| `Done` | with finitely many faults, every transfer commits |

| witness (violated) | shows |
|---|---|
| `WitnessResumedCommit` | a commit after a resume at a non-zero offset |
| `WitnessChangedThenCommit` | a commit after the maximum of faults |
| `WitnessDiscarded` | a resumed part discarded |

| mutant | the failure it plants | caught by |
|---|---|---|
| `commit-cut` | a session that ends cleanly between chunks is taken as the end (a tar stream's clean EOF between entries) | `Safe` |
| `no-checks` | resume at the part's length without the prefix check, commit without the final digest | `Safe` |
| `no-final-check` | commit without the final digest (a write that landed wrong in this session) | `Safe` |
| `trust-partial` | resume without the prefix check and keep a part whose digest differed | `Done` |

## Environment assumptions

| assumption | source |
|---|---|
| the session delivers the sender's bytes in order, unaltered, or ends (it never delivers other bytes) | TLS 1.3 records: AEAD with a per-record sequence nonce (RFC 8446 section 5.3), end to end inside the nf1 session (github.com/nfltr/relay `client/nf1`) |
| a session's end can look clean (EOF) at any record boundary | crypto/tls returns io.EOF on a close without close_notify at a record boundary (Go `crypto/tls` conn.go); RFC 8446 section 6.1 truncation |
| the sender's file does not change while a session sends it; between sessions it may (a check of size and mtime before and after each file refuses a change it sees) | `xfer/send.go` `unchanged` |
| a receiver crash keeps some prefix of what it wrote, possibly with a torn last chunk | POSIX write(2): no ordering or atomicity of unsynced writes across a crash |
| SHA-256 is collision resistant (the model compares the bytes themselves) | FIPS 180-4 |

## Code map

| spec | code |
|---|---|
| `Open` (the receiver's claim, the prefix check, the offset) | `xfer/receive.go` `parts`, `open`; `xfer/send.go` `sendFile` |
| `Send` | `xfer/send.go` `sendFile`; `xfer/receive.go` `run` (frameData) |
| `Finish` (the digest over what is on disk, discard on mismatch) | `xfer/receive.go` `verify`, `commit` |
| `Cut`, `Crash` | `xfer/receive.go` `Receive` (a cut leaves the stage); `cmd/nfltr-send` `receiveCmd` (the next session); `Crash` to an empty part is also the stage's removal: a refused transfer's (`Receive`) and an abandoned one's (`sweep`, `StageLife`) |
| `Change` | `xfer/send.go` `unchanged`; the stage is named by the manifest's digest (`newReceiver`) |
