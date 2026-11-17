# spec: nfltr-send's resume and commit (TLA+)

`Resume.tla` models one file of a transfer on the receiving side across
sessions that end at any point, receiver crashes that lose or tear the tail
of what it wrote, writes that land wrong, and a sender restarted with
another file. `make spec` (TLC 1.8.0, fetched by `scripts/fetch-tla.sh`; or `spec/check.sh` with `TLA_JAVA`, `TLA_JAR`) runs the model
with `-coverage 1`, each witness (must be violated) and each mutant (must
be caught). N = 3 chunks, two chunk values, two faults: 608 states.

`Commit.tla` models the step after it: an offer's top-level entries
arrive verified in the stage; the commit writes an intent record (the
offer's whole manifest digest) into the stage, synced, then renames the
entries one by one into the receiving directory. A rename can fail and
the receiver can crash at any point; either leaves things as they are,
and the next receive of the same offer finds the intent and rolls forward
(renames what the stage still holds; a name the stage no longer holds was
moved). Another's name in the directory refuses the offer. `spec/check.sh`
checks it the same way. N = 2 entries, two faults: 62 states.

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

### Commit.tla

| invariant / property | meaning |
|---|---|
| `Kept` | verified data is never deleted: every entry that arrived is in the stage or in place |
| `Committed` | a done commit holds every entry in place and no stage; no other's name is taken for the offer's |
| `AllOrNone` | with no intent pending, the directory holds none of the offer's entries |
| `Done` | with finitely many faults, the offer is committed, or another's name refuses it (the stage kept) |

| witness (violated) | shows |
|---|---|
| `WitnessPartWay` | a commit with some entries in place and some still staged |
| `WitnessRecoveredAfterCrash` | a commit finished by a later receive after one stopped part way |
| `WitnessForeignRefused` | an intent pending while another's name holds an entry's name |

| mutant | the failure it plants | caught by |
|---|---|---|
| `drop-stage` | a commit that stops part way removes the stage (review 2026-11-17, finding 1) | `Kept` |
| `no-intent` | roll forward without an intent record: names in the directory taken as moved | `AllOrNone` |
| `trust-foreign` | an intent record that is not this offer's (torn, or another offer's) trusted | `Committed` |

## Environment assumptions

| assumption | source |
|---|---|
| the session delivers the sender's bytes in order, unaltered, or ends (it never delivers other bytes) | TLS 1.3 records: AEAD with a per-record sequence nonce (RFC 8446 section 5.3), end to end inside the nf1 session (github.com/nfltr/relay `client/nf1`) |
| a session's end can look clean (EOF) at any record boundary | crypto/tls returns io.EOF on a close without close_notify at a record boundary (Go `crypto/tls` conn.go); RFC 8446 section 6.1 truncation |
| the sender's file does not change while a session sends it; between sessions it may. The manifest carries each file's SHA-256 from when it was offered; the receiver commits only that digest, and the sender ends a session (`ErrChanged`) when the bytes it sent are not it, or the size or mtime changed. So `Finish`'s comparison with `src` is the code's with the offered digest whenever the code completes a file: the model admits more (a changed file committed), its invariants hold for the stricter code | `xfer/send.go` `NewSource`, `sendFile`, `unchanged`; `xfer/receive.go` `verify` |
| a receiver crash keeps some prefix of what it wrote, possibly with a torn last chunk | POSIX write(2): no ordering or atomicity of unsynced writes across a crash |
| SHA-256 is collision resistant (the model compares the bytes themselves) | FIPS 180-4 |
| a rename is atomic across a crash; a synced file and its directory entry survive one | rename(2), fsync(2) |
| no one else creates an offer's top-level name while its commit runs (between the check and the renames) | `xfer/receive.go` `commit` |

## Code map

| spec | code |
|---|---|
| `Open` (the receiver's claim, the prefix check, the offset) | `xfer/receive.go` `parts`, `open`; `xfer/send.go` `sendFile` |
| `Send` | `xfer/send.go` `sendFile`; `xfer/receive.go` `run` (frameData) |
| `Finish` (the digest over what is on disk, discard on mismatch) | `xfer/receive.go` `verify`, `commit` |
| `Cut`, `Crash` | `xfer/receive.go` `Receive` (a cut leaves the stage); `cmd/nfltr-send` `receiveCmd` (the next session); `Crash` to an empty part is also the stage's removal: a refused transfer's (`Receive`) and an abandoned one's (`sweep`, `StageLife`) |
| `Change` | `xfer/send.go` `unchanged`; the stage is named by the manifest's digest (`newReceiver`) |
| `Commit.tla` `Begin` (the taken check, the intent) | `xfer/receive.go` `commit`, `taken`, `writeIntent` |
| `Commit.tla` `Recover` | `xfer/receive.go` `newReceiver`, `readIntent`, `Receive` |
| `Commit.tla` `Rename`, `Stop`, `Finish` | `xfer/receive.go` `commit` (the renames, the sync, the stage removed); `Receive` and `Answer.keep` (the stage kept) |
