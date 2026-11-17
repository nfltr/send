# spec: nfltr-send's resume and commit (TLA+)

`Resume.tla` models one file of a transfer on the receiving side across
sessions that end at any point, receiver crashes that lose or tear the tail
of what it wrote, a chunk on its disk that is not what was sent (a write
that landed wrong, a bad disk, another program; in a session or between
sessions), and a sender restarted with another file. `make spec` (TLC 1.8.0, fetched by `scripts/fetch-tla.sh`; or `spec/check.sh` with `TLA_JAVA`, `TLA_JAR`) runs the model
with `-coverage 1`, each witness (must be violated) and each mutant (must
be caught). N = 3 chunks, two chunk values, two faults: 608 states.

`Commit.tla` models the step after it, with two receivers of the same
offer into the same directory: each opens the offer's stage by taking its
exclusive lock (held until that receive ends; a receiver that finds it
held refuses, busy), writes and verifies the files, then the commit writes
an intent record (the offer's whole manifest digest) into the stage,
synced, and renames the entries one by one into the receiving directory.
A rename can fail and the receiver can crash at any point; either leaves
things as they are, and the next receive of the same offer finds the
intent and rolls forward: it renames what the stage still holds, and takes
a name the stage no longer holds as moved only when what the directory
holds there has the manifest's content (someone may have deleted it and
made another between the crash and the recovery). Another's name in the
directory refuses the offer, also one that appears during the renames: a
rename never replaces. A stale stage (nobody wrote to it for StageLife)
is removed by another receive's sweep only when its lock is free and it
holds no intent record of its own; one with its intent is finished by
that sweep exactly as a recovery would finish it (the intent record carries
the manifest, so the sweep compares moved entries too). A file whose
digest over what landed is not the manifest's refuses the transfer, and its
stage is removed under the lock (Refuse). `spec/check.sh` checks it the same way. N = 2 entries, P = 2
receivers, two faults: 1250 states.

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
| `Kept` | verified data is never deleted by a receiver: every entry that arrived is in the stage, in place, or deleted there by someone else |
| `Intact` | what a commit puts in place is verified (no other receiver rewrote it in the stage) |
| `Committed` | a done commit holds every entry in place with the manifest's content and no stage; no other's name is taken for the offer's |
| `AllOrNone` | with no intent pending, the directory holds none of the offer's entries |
| `NoReplace` | another's name in the directory is never replaced (an action property) |
| `Done` | with finitely many faults, the offer is committed, or another's name refuses it (the stage kept) |

| witness (violated) | shows |
|---|---|
| `WitnessPartWay` | a commit with some entries in place and some still staged |
| `WitnessRecoveredAfterCrash` | a commit finished by a later receive after one stopped part way |
| `WitnessOtherFinished` | a commit finished by another receiver than the one that stopped |
| `WitnessBusy` | one receiver holds the stage while another waits idle (refused busy) |
| `WitnessForeignRefused` | an intent pending while another's name holds an entry's name |
| `WitnessRefusedMidCommit` | a name that appeared during the renames stops the commit, some entries in place |
| `WitnessReplacedRefused` | an entry in place deleted and made again by someone else while the intent is pending |
| `WitnessExpired` | an abandoned stage removed by the sweep |

| mutant | the failure it plants | caught by |
|---|---|---|
| `drop-stage` | a commit that stops part way removes the stage (review 2026-11-17, finding 1) | `Kept` |
| `no-intent` | roll forward without an intent record: names in the directory taken as moved | `Intact` (and `AllOrNone`) |
| `trust-foreign` | an intent record that is not this offer's (torn, or another offer's) trusted | `Committed` |
| `replace` | a rename that replaces a name that appeared after the check | `NoReplace` |
| `sweep-deletes-intent` | the stale sweep removes a stage holding its intent (review 2026-11-17) | `Kept` |
| `no-lock` | receivers open the stage without its lock: one rewrites what another verified and commits (review 2026-11-17 #2, finding A); deadlock checking off (a refusal under it leaves the other receiver stuck, found first otherwise) | `Kept` (and `Intact`) |
| `trust-moved` | recovery takes any name the stage no longer holds as moved, without comparing it with the manifest (review 2026-11-17 #2, finding B) | `Committed` |
| `sweep-trust-moved` | the sweep (receiver P) finishes a stale stage with its intent without that comparison: the intent held only the digest (lane SENDTRACE) | `Committed` |

## Environment assumptions

| assumption | source |
|---|---|
| the session delivers the sender's bytes in order, unaltered, or ends (it never delivers other bytes) | TLS 1.3 records: AEAD with a per-record sequence nonce (RFC 8446 section 5.3), end to end inside the nf1 session (github.com/nfltr/relay `client/nf1`) |
| a session's end can look clean (EOF) at any record boundary | crypto/tls returns io.EOF on a close without close_notify at a record boundary (Go `crypto/tls` conn.go); RFC 8446 section 6.1 truncation |
| the sender's file does not change while a session sends it; between sessions it may. The manifest carries each file's SHA-256 from when it was offered; the receiver commits only that digest, and the sender ends a session (`ErrChanged`) when the bytes it sent are not it, or the size or mtime changed. So `Finish`'s comparison with `src` is the code's with the offered digest whenever the code completes a file: the model admits more (a changed file committed), its invariants hold for the stricter code | `xfer/send.go` `NewSource`, `sendFile`, `unchanged`; `xfer/receive.go` `verify` |
| a receiver crash keeps some prefix of what it wrote, possibly with a torn last chunk | POSIX write(2): no ordering or atomicity of unsynced writes across a crash |
| SHA-256 is collision resistant (the model compares the bytes themselves) | FIPS 180-4 |
| a rename is atomic across a crash; a synced file and its directory entry survive one | rename(2), fsync(2) |
| an exclusive lock on the stage's lock file excludes every other holder, in this or another process, until it is released or its holder dies (the kernel releases it); it is advisory: only receivers take it | flock(2) LOCK_EX (Linux, macOS, BSDs: per open file description), LockFileEx LOCKFILE_EXCLUSIVE_LOCK (Windows); `xfer/lock_*.go` |
| a rename can refuse an existing destination atomically: renameat2 RENAME_NOREPLACE (Linux 3.15+), renamex_np RENAME_EXCL (macOS 10.12+), MoveFileEx without MOVEFILE_REPLACE_EXISTING (Windows). Where a file system refuses the flag (Linux EINVAL, EOPNOTSUPP or ENOSYS; macOS ENOTSUP or EINVAL), and on other platforms, a file is linked (link(2) fails on an existing name) then unlinked, and a directory commit is refused (`ErrNoAtomicRename`): the model's Rename holds only where a primitive exists | rename(2) of Linux and macOS, link(2), MoveFileExW; `xfer/rename_*.go` |

## Code map

| spec | code |
|---|---|
| `Open` (the receiver's claim, the prefix check, the offset) | `xfer/receive.go` `parts`, `open`; `xfer/send.go` `sendFile` |
| `Send` | `xfer/send.go` `sendFile`; `xfer/receive.go` `run` (frameData) |
| `Finish` (the digest over what is on disk, discard on mismatch) | `xfer/receive.go` `verify`, `commit` |
| `Cut`, `Crash` | `xfer/receive.go` `Receive` (a cut leaves the stage); `cmd/nfltr-send` `receiveCmd` (the next session); `Crash` to an empty part is also the stage's removal: a refused transfer's (`Receive`) and an abandoned one's (`sweep`, `StageLife`) |
| `Change` | `xfer/send.go` `unchanged`; the stage is named by the manifest's digest (`newReceiver`) |
| `Commit.tla` `Open` (the stage's lock, busy; with the intent, the recovery) | `xfer/receive.go` `newReceiver`, `lockStage`, `Receive`; `xfer/lock_*.go` |
| `Commit.tla` `Begin` (the taken check, the intent) | `xfer/receive.go` `commit`, `taken`, `writeIntent` |
| `Commit.tla` `Rename`, `Stop`, `Finish` | `xfer/receive.go` `commit` (the renames, the moved entries compared with the manifest: `placed`, the sync, the stage removed), `xfer/rename_*.go` `renameNoReplace`; `Receive` and `Answer.keep` (the stage kept) |
| `Commit.tla` `Expire` (and `Open` by the sweep) | `xfer/receive.go` `sweep`, `finishStage` |
