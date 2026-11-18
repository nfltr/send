# nfltr-send

Send files and directories between two machines, end to end encrypted:
directly between them when their networks allow it, through an
[nfltr](https://nfltr.xyz) relay otherwise. The sender prints an address;
whoever holds it receives.

```console
sender$ nfltr-send send report.pdf photos/ > address
serving 2 files, ...; give the receiver this address (a secret: whoever holds it can receive):
receiver$ nfltr-send receive --to ~/Downloads < address
received 2 files, 1073741829 bytes in 4.1 s (262 MB/s), path direct
```

- **End to end.** The session is TLS 1.3 between the two machines, the
  sender's key pinned by the address; the relay sees neither the address's
  secret nor any byte of the files.
- **Direct first.** Both ends try a direct path (QUIC, hole punched with
  the relay's help) and move the session to it when it works; the relay
  carries the session until then and whenever the direct path dies.
- **Verified.** A file is its BLAKE3 root, taken when the sender started
  serving and pinned by the offer. The sender serves each file as a
  verified stream (Bao, its hash tree sent ahead of the data): the receiver
  verifies every 256 KiB chunk against the root as it arrives and writes
  only one that verifies; a chunk that does not (a file changed since it
  was offered) ends the session at once, the verified chunks kept. Nothing
  lands in the receiving directory until every chunk on the receiver's
  disk verifies against its file's root (checked in parallel; throughput
  notes: [`docs/performance.md`](docs/performance.md)).
  A name that already exists there refuses the transfer.
- **Durable.** Every received file is on stable storage before the
  commit's first rename: on Linux by one `syncfs(2)` of the receiving file
  system (it reports a write error since Linux 5.8), on macOS by an
  `fsync(2)` per file and then one flush of the drive's cache (the
  `F_FULLFSYNC` of the commit's intent record), elsewhere by a sync per
  file. Not one flush per file: that cost milliseconds per small file.
- **Resumed.** A cut transfer keeps what arrived (in a hidden
  `.nfltr-send-*` directory inside the receiving directory, with each
  file's hash tree); the
  receiver resumes by itself, and the next `receive` of the same offer
  resumes from there: each kept part is verified chunk by chunk against the
  root, and only the chunks missing or not verifying are sent again,
  wherever they are in the file. A transfer refused at its commit (a name
  appeared meanwhile, or no atomic rename) keeps its verified stage for a
  later `receive`. A stage nobody wrote to for 7 days is an abandoned
  offer's, and the next `receive` into that directory that opens its own
  stage removes it (one whose intent record does not match is kept), or, when that offer's commit
  had begun (a receiver that died while moving its entries into place),
  finishes it. The next `receive` of the same offer finishes such a
  commit too, and refuses, keeping the stage, when a name the commit had
  moved into place no longer holds what was offered (someone deleted it
  and made another). One `receive` of an offer into a directory at a time: a second
  one, while the first holds the offer's hidden directory (a lock on a
  file in it), is refused (exit 2).
- **Commit.** Each top-level name of the offer is renamed into place on
  its own: one top-level entry (a file, or a directory with everything
  under it) appears at once; an offer of several top-level names can show
  some before the others, and a receiver that dies part way leaves them
  so until the next `receive` of the offer finishes the commit.
- **No name replaced.** A commit never replaces a name that appears
  meanwhile: its renames refuse an existing name atomically, with
  `renameat2(RENAME_NOREPLACE)` on Linux 3.15+, `renamex_np(RENAME_EXCL)`
  on macOS, `MoveFileEx` without `MOVEFILE_REPLACE_EXISTING` on Windows
  (where a failed move refuses the transfer). On a Linux or macOS file
  system that refuses that flag, and on other systems, a file is
  hard-linked into place (which refuses an existing name) and its staged
  name removed, and a directory is not moved: the transfer is refused
  with its files kept staged (receive it on a file system that has the
  flag).

The resume and commit rules are checked TLA+ models: [`spec/`](spec/README.md).

The address is a secret. `receive` reads it from `--address-file` (default
stdin), never from the command line; a file with any group or others
permission is refused. On Windows the file's mode does not reflect its ACL (every
writable file reports 0666), so the mode is not checked there: keep the
file in a directory only you can read, such as your user profile.
Whoever holds the address can open sessions: the sender serves at most 16
at once (a receiver past that is refused, exit 2; in a flood, sessions
past a few refusals under way are closed outright). A session whose peer is
gone ends with its connection (the relay link's and the direct path's
liveness checks).

## Credentials

Receiving needs none. Sending needs none either, while the bytes can go
peer to peer:

- **With this machine's nfltr certificate** the sender registers its
  address under its account, and the relay carries a transfer whose
  direct path does not form. The certificate is the one
  `nfltr config login` saved beside the nfltr config file (`machine.json`
  and `machine.key` next to `$NFLTR_CONFIG`, else `~/.config/nfltr/`), or
  the `machine.json` that `--machine-credential` names (its `machine.key`
  beside it, readable by its owner only). The sender renews it while it
  serves and writes each renewal back to `machine.json`; a certificate
  the relay refused (revoked, expired) ends the sender with advice to run
  `nfltr config login`.
- **With none** the address is served with no account: on direct paths
  only. The relay carries the session's signalling (its TLS handshake and
  the direct path's candidates, inside that TLS) and no byte of the files;
  it learns that the address has no account behind it and the two
  machines' network addresses, never the address's secret. A receiver
  whose direct path does not form exits 12 (`direct_required`): relaying
  needs the sender's account. A direct path lost mid-transfer ends that
  session; the next one resumes, or exits 12 if no path forms again.
  `--direct off` with no certificate is refused. A relay that takes no
  address served without an account (setting `nf1.anon.enabled`, off by
  default) refuses the sender (exit 3).

## What the sender refuses

- A file whose size or modification time changed since it was offered.
- Anything that is not a regular file or a directory (a symlink, a device).
- Two sources with the same base name.

The receiver refuses a peer of another protocol version ("update both
ends"), and the sender a machine certificate issued for another relay.

## Flags and exit codes

`send`: `--once` (exit after the first complete transfer), `--direct
on|off`, `--machine-credential <file>`, `--relay <address>` (default
`grpc.nfltr.xyz:443`, or `$NFLTR_SEND_RELAY`), `--relay-http`,
`--relay-pin <sha256>` (a private relay's certificate).
`receive`: `--to <dir>`, `--address-file <file>` (default stdin),
`--tries <n>` (default 10, each session resuming the last), `--json` (one
JSON line of totals on stderr). `nfltr-send <command> -h` lists them all.

Exit codes: 0 done, 1 usage, 2 refused (a name exists, busy, too many
sessions, a commit refused), 3 failed, 12 `direct_required` (no direct
path and no account to relay).

## Operating notes

On Linux a small `net.core.rmem_max` caps the direct path's QUIC receive
window, and with it the throughput on a path with a long round trip;
quic-go logs `failed to sufficiently increase receive buffer size` then.
Raise it (and `wmem_max`) on the receiving machine:

```console
$ sudo sysctl -w net.core.rmem_max=7500000 net.core.wmem_max=7500000
$ printf 'net.core.rmem_max=7500000\nnet.core.wmem_max=7500000\n' | sudo tee /etc/sysctl.d/90-quic.conf
```

## Install

```console
$ go install github.com/nfltr/send/cmd/nfltr-send@latest
```

Or a release binary (linux, darwin, windows; amd64, arm64) from
[Releases](https://github.com/nfltr/send/releases), checked against its
signed `SHA256SUMS` (`ssh-keygen` from OpenSSH 8.1 or later) and nfltr-send's own release key, Ed25519
`SHA256:TqSS4Ad7mrwaNN6vZmr2liyFAK8Gk5AGDg0KH0evc9I`:

```console
$ v=<version>; f=nfltr-send_linux_amd64
$ for x in $f SHA256SUMS SHA256SUMS.sshsig; do curl -fsSLO https://github.com/nfltr/send/releases/download/$v/$x; done
$ curl -fsSLO https://raw.githubusercontent.com/nfltr/send/main/release/allowed_signers
$ ssh-keygen -Y verify -f allowed_signers -I nfltr-release -n nfltr-release -s SHA256SUMS.sshsig < SHA256SUMS
$ grep " $f\$" SHA256SUMS | sha256sum -c && chmod +x $f && mv $f ~/bin/nfltr-send
```

## Build

```console
$ go build ./cmd/nfltr-send
$ make check   # gofmt, vet, -race tests, a short fuzz run
$ make spec    # the TLA+ models (Resume.tla, Commit.tla), their witnesses and mutants
$ make release-dryrun   # the release files in dist/, built twice and compared, signed with a throwaway key
```

A release is `make release`, `make release-sign SIGNING_KEY=<key>`,
`make release-verify` (against `release/allowed_signers`) on a clean
checkout of its tag, then the files in `dist/` attached to the tag's
GitHub release.

License: Apache-2.0.
