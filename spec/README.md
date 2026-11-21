# spec: nfltr-send's resume, offer, commit and end (TLA+)

`Resume.tla` models one file of a transfer on the receiving side across
sessions that end at any point, receiver crashes that lose or tear the tail
of what it wrote, a chunk on its disk that is not what was written (a write
that landed wrong, a bad disk, another program; in a session or between
sessions), and a sender's file rewritten (between sessions or during one).
The offer pins the file's BLAKE3 root (an ideal hash tree: the root fixes
every chunk). The receiver verifies each chunk on arrival against that root
(Bao verified streaming, the tree's outboard sent ahead of the data) and
writes only a chunk that verifies; one that does not is refused at once
and ends the session (`Bad`). A session opens with the receiver verifying
its part chunk by chunk with the tree it kept and asking the sender for
every chunk that is missing or does not verify, keeping every other: a
resume set, not a prefix (`Open`; the sender sends the asked chunks in
order, each written at its place). At the end it verifies the whole file on
its disk and commits it only when every chunk verifies, else keeps it for
the next session to verify again (`Finish`). A crash loses any chunk
written since the last sync, or tears one (`Crash`). The code reads ahead:
up to 8 chunks are verified at once while the next frames are read, but
each is written (the model's `Send`) only after it verified and every
asked chunk before it was written; a chunk read but not yet written when
the session ends is not kept (the model's `Cut` then), and the first that
does not verify ends the session with nothing after it written (`Bad`).
The from-disk checks verify the chunks in parallel. `make spec` (TLC 1.8.0, fetched by `scripts/fetch-tla.sh`; or `spec/check.sh` with `TLA_JAVA`, `TLA_JAR`) runs the model
with `-coverage 1`, each witness (must be violated) and each mutant (must
be caught). N = 3 chunks, two chunk values, two faults: 19096 states.

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
the manifest, so the sweep compares moved entries too). A file that does
not verify keeps its verified part (Resume.tla), so for this model its
session is a Cut. `spec/check.sh` checks it the same way. N = 2 entries, P = 2
receivers, two faults: 1250 states.

| invariant / property | meaning |
|---|---|
| `Safe` | only the offered file (the pinned root's) is ever committed, whole |
| `Kept` | a chunk the receiver writes verifies against the pinned root (an action property) |
| `Resumed` | a session keeps only chunks that verify: every chunk it does not ask for is the offer's (an action property) |
| `Minimal` | a session resuming a part asks for no chunk that verifies (an action property) |
| `Done` | with finitely many faults, the transfer commits, or the sender's file is no longer the offered one |

| witness (violated) | shows |
|---|---|
| `WitnessResumedCommit` | a commit after a resume at a non-zero offset |
| `WitnessChangedThenCommit` | a commit after the maximum of faults |
| `WitnessDiscarded` | a resumed part discarded |
| `WitnessRefusedThenCommit` | a commit after a chunk was refused |
| `WitnessGap` | a session that asks for a chunk before one it keeps (not a prefix) |

| mutant | the failure it plants | caught by |
|---|---|---|
| `accept-unverified` | a chunk written without verifying it | `Kept` |
| `resume-unverified` | keep a chunk that is on disk but does not verify | `Resumed` |
| `skip-missing` | leave out of the ask a chunk that is missing | `Resumed` |
| `resend-verified` | ask for everything from the first missing or bad chunk on (the prefix resume this replaced: a 1 GiB file with 4 KiB overwritten at 100 MiB was sent again from there) | `Minimal` |
| `root-unbound` | chunks verified against the root of the file the sender has now, not the pinned offer's | `Kept` |
| `commit-cut` | a session that ends cleanly between chunks is taken as the end (a tar stream's clean EOF between entries) | `Safe` |
| `no-final-check` | commit without verifying the file on disk (a write that landed wrong in this session) | `Safe` |

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

### End.tla

`End.tla` models the end of a transfer, both processes and the sessions
between them (lanes SENDFIX and SENDONCE, docs/reliability.md "Defects
found and fixed"): the receiver commits, writes its answer, closes the
session and exits; the sender reads the answer and counts the transfer
done (a `--once` sender exits). A close delivers what was written before
it, then a clean end; a cut (the network, the sender's death, the relay)
delivers some prefix of what was written, then a reset, at any time, also
after the receiver's close returned (it returns once the relay has the
frames, not the sender). The sender ends a session cleanly once it read
the answer, and the receiver takes that end (end to end, under TLS after
its close_notify) as the sender's word. A receiver that committed and whose
session was cut before that end dials the address again and answers the
manifest at once (`Retell`), up to
`MaxTries` dials (receive's `--tries`); a dial while the sender no longer
serves fails (`Unserved`), and after the last the receiver exits ok all the
same (`GiveUp`). Two cuts, three dials: 712 states. Not traced (two
processes; see "Traces").

| invariant / property | meaning |
|---|---|
| `ExitRight` | a receiver exits ok exactly when it committed |
| `Told` | a reset reaches the sender only from a cut |
| `Confirmed` | a sender ends only on an answer, so only once the receiver committed |
| `Retold` | with more dials than cuts, a receiver exits ok only once the sender read an answer |
| `Done` | the receiver exits; once it committed, the sender ends |

| witness (violated) | shows |
|---|---|
| `WitnessCommittedCut` | a receiver that committed, whose session was cut before its answer reached the sender, exits ok |
| `WitnessTold` | the sender reads the answer |
| `WitnessRetold` | a cut lost the committed receiver's answer, and the sender ended all the same (told on a later session) |

| mutant | the failure it plants | caught by |
|---|---|---|
| `close-cuts` | the receiver's close cuts the nf1 data connection at once, its unwritten frames lost, and the relay resets the session (defect 1: a `--once` sender read "session reset by the peer" and served on in 74 of 130 relayed runs) | `Told` |
| `exit-on-answer` | a receiver that committed exits failed because its answer did not go (defect 2: exit 3, and the next receive of a new address refused, the names taken) | `ExitRight` |
| `no-retell` | a receiver that committed exits ok when its session was cut before its answer arrived, telling no one (defect 3: a link drop longer than the session's liveness left the `--once` sender serving, 4 of 20 relayed drops) | `Retold` |
| `close-confirms` | the receiver takes its own close's clean return as the sender's word (SENDONCE's first fix: the soak's link drop still left the sender serving, the relay having taken the answer) | `Retold` |
| `sender-gives-up` | the rejected design: a `--once` sender that reads a reset after its last byte exits as if delivered | `Confirmed` |

### Offer.tla

`Offer.tla` models an offer of F files to its one commit (lane SENDMULTI).
Resume.tla is one file, whose `Finish` commits it at its final check; an
offer commits once, after its last file, so an earlier file can change
after its check and before that commit: on the sender's disk (`Change`) or,
written by another program, in the receiver's stage (`Touch`). A file is a
version (the offered one 0; a write makes the next), sending it is one step
(`SendFile`: Resume.tla's whole session of it, the sender's check at its
end, the receiver's verify of what landed). After the last file the sender
checks every file again before its done frame (`Done`: size and mtime, as
`unchanged`), and the receiver checks every staged file again before it
commits (`Commit`: size and mtime against those it saw when it verified
the file); either refuses typed: the sender's `ErrChanged`, the receiver's
`ErrUnverified` with the stage kept (the next session verifies it again,
Resume.tla `Open`). A write between the done frame and the commit, one
frame's flight, is after the transfer (`WitnessChangedAfterDone`). F = 2
files, two faults: 128 states. Not traced (both processes; see "Traces").

| invariant / property | meaning |
|---|---|
| `Safe` | every committed file is the offered one (what was sent) |
| `Current` | at its done frame, the sender's last word before the commit, every one of its files is the offered one |
| `Ended` | the offer commits, or fails typed (the sender's `ErrChanged`, the receiver's refused commit) |

| witness (violated) | shows |
|---|---|
| `WitnessEarlierChanged` | an earlier file changed on the sender's disk after its check, refused at the done frame |
| `WitnessEarlierTouched` | a staged file changed after its verify, the commit refused |
| `WitnessCutThenCommit` | a commit after a cut session |
| `WitnessChangedAfterDone` | a sender's file written after the done frame, the offer committed (the window the design leaves) |

| mutant | the failure it plants | caught by |
|---|---|---|
| `no-sender-recheck` | the done frame after the per-file checks only (lane SENDTAIL's finding: an earlier file rewritten while a later one was sent was committed as offered) | `Current` |
| `no-receiver-recheck` | the commit after the per-file verifies only (a staged file written after its verify, while later files were received) | `Safe` |

## Environment assumptions

| assumption | source |
|---|---|
| a session's Close writes every byte written before it, then its end, before its nf1 data connection ends; the relay passes a session's bytes and its end on in order (End.tla `Close`) | github.com/nfltr/relay `client/nfnet` `dropMux` since client v0.23.6 (link `Mux.Drain`: the connection closes with every frame written), `TestNF1CloseKeepsWhatWasWritten`; `cmd/nfltr-send` `TestOnceRelayedSenderReadsTheAnswer` through relay-core |
| the session delivers the sender's bytes in order, unaltered, or ends (it never delivers other bytes) | TLS 1.3 records: AEAD with a per-record sequence nonce (RFC 8446 section 5.3), end to end inside the nf1 session (github.com/nfltr/relay `client/nf1`) |
| a session's end can look clean (EOF) at any record boundary | crypto/tls returns io.EOF on a close without close_notify at a record boundary (Go `crypto/tls` conn.go); RFC 8446 section 6.1 truncation |
| the sender's file may change at any time (`Change`); the offer does not: the manifest carries each file's BLAKE3 root from when it was offered and the receiver verifies against that root only (the tree the session carries is checked against it, never trusted on its own). The sender also ends a session (`ErrChanged`) when the size or mtime changed | `internal/xfer/send.go` `NewSource`, `sendFile`, `unchanged`; `internal/xfer/receive.go` `run`, `verify` |
| a receiver crash keeps some prefix of what it wrote, possibly with a torn last chunk | POSIX write(2): no ordering or atomicity of unsynced writes across a crash |
| BLAKE3 is collision resistant and its tree binds every chunk and its position to the root, so a chunk that verifies at an offset is the offered file's chunk there (the model compares the chunks themselves); the tree's length is the manifest's size | the BLAKE3 paper (2020), section 7 (security); the Bao spec (verified streaming, outboard encoding); `internal/xfer/verified` over `github.com/nfltr/blake3/bao` (lukechampine.com/blake3 with arm64 NEON) |
| SHA-256 is collision resistant (the manifest's digest names the stage and the intent record) | FIPS 180-4 |
| a rename is atomic across a crash; a synced file and its directory entry survive one. The received files are synced before the intent record: one syncfs(2) of the file system (Linux), an fsync(2) per file then the intent's F_FULLFSYNC, which flushes the drive's whole cache (macOS), FlushFileBuffers or fsync per file (elsewhere) | rename(2), fsync(2), syncfs(2), fcntl(2) F_FULLFSYNC; `internal/xfer/sync_*.go` |
| an exclusive lock on the stage's lock file excludes every other holder, in this or another process, until it is released or its holder dies (the kernel releases it); it is advisory: only receivers take it | flock(2) LOCK_EX (Linux, macOS, BSDs: per open file description), LockFileEx LOCKFILE_EXCLUSIVE_LOCK (Windows); `internal/xfer/lock_*.go` |
| a rename can refuse an existing destination atomically: renameat2 RENAME_NOREPLACE (Linux 3.15+), renamex_np RENAME_EXCL (macOS 10.12+), MoveFileEx without MOVEFILE_REPLACE_EXISTING (Windows). Where a file system refuses the flag (Linux EINVAL, EOPNOTSUPP or ENOSYS; macOS ENOTSUP or EINVAL), and on other platforms, a file is linked (link(2) fails on an existing name) then unlinked, and a directory commit is refused (`ErrNoAtomicRename`): the model's Rename holds only where a primitive exists | rename(2) of Linux and macOS, link(2), MoveFileExW; `internal/xfer/rename_*.go` |
| a write to a file changes its size or its mtime (both checks of a file, the per-file ones and the rechecks before the done frame and the commit, compare size and mtime, not the bytes); a write that restores both is caught only while the file is sent (the receiver verifies each chunk) | stat(2) st_mtim (POSIX: a write marks it for update); `internal/xfer/send.go` `unchanged`, `recheck`; `internal/xfer/receive.go` `verify`, `recheck` |

## Code map

| spec | code |
|---|---|
| `Open` (the part verified with its kept tree, the chunks asked for) | `internal/xfer/receive.go` `parts`, `runs`, `keptTree`, `open`; `internal/xfer/xfer.go` `need`, `validRuns`; `internal/xfer/send.go` `sendFile`; `internal/xfer/verified` `Good` |
| `Send`, `Bad` (a chunk verified on arrival, written in order, or refused) | `internal/xfer/send.go` `sendFile`; `internal/xfer/receive.go` `run` (frameTree, frameData, `keep`, `inFlight`, `ErrUnverified`); `internal/xfer/verified` `Chunk` |
| `Finish` (every chunk on disk verified, kept on mismatch) | `internal/xfer/receive.go` `verify`, `commit`; `internal/xfer/verified` `Verify` |
| `Cut`, `Crash` | `internal/xfer/receive.go` `Receive` (a cut leaves the stage); `cmd/nfltr-send` `receiveCmd` (the next session); `Crash` to an empty part is also the stage's removal: an abandoned one's (`sweep`, `StageLife`) |
| `Rot` | the disk; `internal/xfer/receive.go` `verify` (in a session), `parts` (between sessions) |
| `Change` | the sender's disk; `internal/xfer/send.go` `unchanged`; the stage is named by the manifest's digest (`newReceiver`) |
| `Commit.tla` `Open` (the stage's lock, busy; with the intent, the recovery) | `internal/xfer/receive.go` `newReceiver`, `lockStage`, `Receive`; `internal/xfer/lock_*.go` |
| `Commit.tla` `Begin` (the taken check, the intent with the manifest) | `internal/xfer/receive.go` `commit`, `taken`, `writeIntent`; `internal/xfer/sync_*.go` (the files synced before it) |
| `Commit.tla` `Rename`, `Stop`, `Finish` | `internal/xfer/receive.go` `commit` (one pass in manifest order: a moved entry compared with the manifest, `placed`; a staged one renamed, `move`; the sync, the stage removed), `internal/xfer/rename_*.go` `renameNoReplace`; `Receive`, `end` and `Answer.keep` (the stage kept) |
| `Commit.tla` `Expire` (and `Open` by the sweep) | `internal/xfer/receive.go` `sweep`, `finishStage` (with its intent: `commit`, as a recovery) |
| `End.tla` `Answer`, `Close`, `Exit`, `Fail`, `Retell`, `Unserved`, `GiveUp` | `internal/xfer/receive.go` `Receive` (the answer: a commit stands when it does not go, `answer`), `Retell`; `cmd/nfltr-send` `receiveTo`, `receiveOnce`, `retellOnce` (the close, the dials again, the exit status); `Read`: `internal/xfer/send.go` `Serve` (an answer in place of the want); github.com/nfltr/relay `client/nfnet` `Conn.Close`, `dropMux` |
| `End.tla` `Read` | `internal/xfer/send.go` `Serve` (the answer read); `cmd/nfltr-send` `serve` (`--once` ends on it) |
| `Offer.tla` `SendFile`, `Done` (the sender's checks), `Commit` (the receiver's recheck) | `internal/xfer/send.go` `Serve`, `sendFile`, `unchanged`, `recheck`; `internal/xfer/receive.go` `run`, `verify`, `recheck`, `commit` |
| `TraceCommit.tla`, `TraceResume.tla`, `TraceLog.tla`, `traces.sh`, `traces/` | `internal/xfer/trace_on.go` (the recorder), `internal/xfer/trace_off.go`; the call sites in `internal/xfer/receive.go` (behind `traceOn`) |

## Traces

`make spec-traces` (`spec/traces.sh`) runs the xfer tests with
`-tags sendtrace`: `internal/xfer/trace_on.go` writes one NDJSON line per model step
a receiver takes, per generation of a stage (from its creation to its
removal), one file for Commit.tla and one per non-empty file for
Resume.tla. Each is checked with TLC (`TraceCommit.tla`, `TraceResume.tla`,
`-coverage 1`): accepted when a behaviour of the model takes every line
(`TraceNotDone` violated); a line that maps to no action rejects it. The
actions no accepted behaviour takes are listed as test gaps (none now; the
tests `TestPartWhoseTailWasLostResumes`,
`TestNameTakenDuringTheTransferRefusesTheCommit` and
`TestStaleStageOfACutTransferIsRemoved` closed Crash, Begin's refusal and
Expire; `TestUnverifiedChunkIsRefused` and
`TestSenderRefusesAFileRewrittenWhileSent` take Bad and Change). Without
the tag the recorder is `internal/xfer/trace_off.go`, every call site behind the
constant `traceOn`: no code in a release binary. 99 traces, about 10 s.

| line (code) | action |
|---|---|
| Commit `Init` {n} (`newReceiver` makes the stage) | the model's constants: N tops; P the receivers named |
| `Open` {p, rec} / `Busy` / `Taken` (`newReceiver`, the lock taken or not, under the recorder's mutex) | `Open(p)`; Busy and Taken a stutter step whose model reason holds (the lock held or the stage just finished; a name taken) |
| `Write` {t}, `Verify` {t} (`run`: a top's first file started; its last file verified; at `commit`, a top holding no file) | `Write`, `Verify` |
| `Begin`, `BeginRefused` (`commit`: the intent synced; a name taken) | `Begin` |
| `Rename` {t}, `RenameRefused` {t}, `Stop` (`commit`, per top in order; a receiver that died in its commit: `end`) | `Rename`, `Stop` |
| `Finish` (before the stage's removal under its lock), `Cut` (`end`: the lock released before the commit) | `Finish`, `Cut` |
| `Expire` (`finishStage`: a stale stage without its intent) | `Expire`; of a stage holding nothing, a stutter |
| Resume `Init` {n} | N: the file's length in chunks of `maxData` bytes |
| `Open` {part, off} (`run`, a start frame: the part on disk before the cut to the offset, as chunk digests; the offset in chunks) | `Open`; once verified, `Served` |
| `Send` {i, v} (a chunk verified and complete on disk), `Bad` {i, v} (a chunk that did not verify), `Finish` {ok} (`verify` when it fails; when it verifies, at the commit's `recheck`, failed for the file it refuses: Resume's Finish commits, the offer only after its recheck, Offer.tla), `Cut` (the session ended inside the file, or after a file it verified and before the recheck) | `Send`, `Bad`, `Finish` (a refused recheck after a hidden `Rot`), `Cut` |
| hidden | Commit `Foreign`, `Replace` (someone else's names); Resume `Rot`, `Crash`, `Change` (taken only towards the next line); the offer one whose chunk at each position was taken there, the sender's file `src` one whose chunk at each position was taken or refused there |

Abstractions and limits: the tests run receivers and senders as goroutines
of one process, and receivers meet only through the stage's lock, whose
acquire and release the recorder writes atomically with their lines, so one
file in write order is a linearization (no vector clock); receivers in
separate processes are not traced. Senders are not recorded. A chunk is
`maxData` bytes; a part not cut at a chunk boundary (a sender that frames
otherwise, then a resume) is logged with offset -1 and rejected. Empty
files are not in Resume (its committed `<<>>` is "not committed").

| mutant trace (`spec/traces/`) | the failure | invariant it protects |
|---|---|---|
| `mutant-commit-cut-resume` | a file finished after a clean end mid-file | Resume `Safe` |
| `mutant-resume-unverified-resume` | a resume after a chunk that does not verify | Resume `Resumed` |
| `mutant-accept-unverified-resume` | a chunk refused, then the same chunk written | Resume `Kept` |
| `mutant-sweep-deletes-intent-commit` | the sweep removes a stage holding its intent | `Kept` |
| `mutant-no-lock-commit` | a second receiver opens a held stage | `Intact` |
| `mutant-finish-part-way-commit` | a commit finished with a top not in place | `Committed` |
| `mutant-no-intent-commit` | a recovery without an intent record | `AllOrNone` |
| `mutant-replace-commit` | a rename onto a name someone else took | `NoReplace` |
| `mutant-unknown-step-commit` | a step that is no action of the model | (rule: every line maps to an action) |

`Done` (both models) is a liveness property: no finite trace violates it,
so it has no mutant trace.
