# nfltr-send

Send files and directories between two machines, end to end encrypted:
directly between them when their networks allow it, through an
[nfltr](https://nfltr.xyz) relay otherwise. The sender prints an address;
whoever holds it receives.

```console
sender$ nfltr-send send big.bin > address
serving 1 file, 1073741824 bytes; give the receiver this address (a secret: whoever holds it can receive):
receiver$ nfltr-send receive --to ~/Downloads < address
received 1 file, 1073741824 bytes in 4.5 s (237 MB/s), path direct
```

That run, and the numbers below, had both ends on one Mac (Apple M5,
10 cores) over loopback through a local relay: the program's own ceiling,
not a network's rate. The receiver's time runs from its start to the
commit; it leaves out the sender's start (the file's root and outboard,
built before the address is printed). For one 1 GiB file, v0.1.1, medians
of 6 runs interleaved with v0.1.0's on a shared machine (load average
about 35): sender start to address 0.15 s (v0.1.0: 1.47 s), address to
received 4.68 s (5.29 s), sender start to receiver exit 4.82 s (6.79 s).
Measurements per change: [`docs/performance.md`](docs/performance.md).

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
  disk verifies against its file's root (checked in parallel).
  A name that already exists there refuses the transfer.
- **Durable.** Every received file is on stable storage before the
  commit's first rename: on Linux by one `syncfs(2)` of the receiving file
  system (it reports a write error since Linux 5.8), on macOS by an
  `fsync(2)` per file and then one flush of the drive's cache (the
  `F_FULLFSYNC` of the commit's intent record), elsewhere by a sync per
  file. Not one flush of the drive per file.
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
  file in it), is refused (exit 4).
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
at once (a receiver past that is refused, exit 4; in a flood, sessions
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
  default) refuses the sender (exit 2, `auth`).

## What the sender refuses

- A file whose size or modification time changed since it was offered.
- Anything that is not a regular file or a directory (a symlink, a device).
- Two sources with the same base name.

Every 1.x end speaks protocol version 4; the receiver takes any version
from 4 to its own and both speak the lower. The sender refuses a machine
certificate issued for another relay.

## Flags and exit codes

What every 1.x release keeps (commands, flags, environment, exit codes,
the address, compatibility): [docs/v1-contract.md](docs/v1-contract.md).

`send`: `--once` (exit after the first complete transfer), `--direct
on|off`, `--machine-credential <file>`, `--relay <address>`, `--relay-http`,
`--relay-cert-sha256 <sha256>` (a private relay's certificate).
The relay is `--relay`, else `$NFLTR_SEND_RELAY`, else the one `nfltr config
set-relay` saved (with its HTTPS URL and certificate pin; nothing of an
account is read), else `grpc.nfltr.xyz:443`; an error connecting names the
relay and `--relay`.
`receive` (no `--relay`: the address names its relay; `--relay` or
`--relay-http` is a usage error, `$NFLTR_SEND_RELAY` is ignored):
`--to <dir>`, `--address-file <file>` (default stdin), `--direct on|off`,
`--relay-cert-sha256 <sha256>`,
`--tries <n>` (default 10, each session resuming the last; after a
commit whose session was cut before the sender read the answer, the
remaining sessions tell the sender again, so a `--once` sender ends), `--json` (one
JSON line on stderr: the totals and `paths`, each path the transfer used;
on failure instead `exit`, `code` (the exit's name below) and `message`,
the exit status unchanged). `nfltr-send <command> -h` lists them all
(exit 0); `nfltr-send version` prints the release, its commit and the Go
version.

Exit codes, the relay SDK's table, the same as nfltr-pipe's: 0 done, 1
`usage`, 2 `auth`, 3 `not_found`, 4 `refused` (a name exists, the sender
busy, a commit refused), 5 `unreachable`, 6 `reset`, 7
`e2ee_pin_mismatch`, 9 `balance_exhausted`, 10 `e2ee_client_refused`,
12 `direct_required` (no direct path and no account to relay); a sender
stopped by SIGHUP exits 129, by SIGINT 130, by SIGTERM 143 (`--once`
after its transfer: 0). Flags may follow the files; after `--` every word
is a file.

## Tested across NATs

On 2026-11-18 the released binaries (nfltr-send v0.1.1, nfltr v2.1.70)
ran between two machines behind different NATs: a Mac behind a home
router whose NAT maps a new port per destination, and a Linux container
behind Docker's NAT on a cloud machine. Every received file's SHA-256
matched the sent one.

- **No account, IPv4.** No direct path formed between those two NATs, so
  both ends stopped with exit 12 `direct_required` (3 runs each way, the
  receiver after about 56 s): by design nothing is relayed without an
  account.
- **No account, IPv6** (the same machines, the container on the host's
  network): direct both ways, a 64 MiB file and a directory.
- **A machine certificate on both ends** (`nfltr config login`): the relay
  carried an 8 MiB file and a directory (5 files, 8,394,632 bytes) both
  ways, 2 runs each, received in 6.7 to 13.1 s.
- **ssh and rsync over an nf1 address.** On the server:

  ```console
  server$ nfltr cat serve ssh --forward 127.0.0.1:22 --address
  ```

  prints the address once (`--show-address` prints it again); the client
  keeps it in a 0600 file and names it in `~/.ssh/config`:

  ```
  Host box
    ProxyCommand nfltr cat pipe --address-file ~/.ssh/box.addr
  ```

  `rsync -a --delete` of a tree to `box:` and back left 0 differences
  (`--itemize-changes`): direct over IPv6 with no account (298 files,
  30 MiB, 3 runs), relayed with a machine certificate on both ends (98
  files, 5.8 MiB, 2 runs). Over IPv4 with no account ssh failed as the
  transfers did (`nfltr cat pipe` exit 12).

These are one check over one pair of networks, not a benchmark.

## At scale

[`docs/scale.md`](docs/scale.md) measures 10 GiB, 100 GiB and 100,000-file
transfers, resume after a kill, and paths between regions (3 to 159 ms)
with rate caps and loss, on 2026-11-18. In v0.1.2 one transfer moves at
most about 1 MiB per round trip (191 Mbit/s over 36 ms; 41 Mbit/s over
159 ms). The cause is a window in the relay client, which is fixed there.
With the fix, released in v0.1.3, the same transfers ran at 757 and
265 Mbit/s. Resume re-sends only what is missing plus the bad chunk.

## Reliability

[`docs/reliability.md`](docs/reliability.md): a rerunnable matrix (rate,
round trip, loss, direct and relayed, cut and resume, many files, a 4 GiB
file, no account, a full disk) and a soak, each transfer judged by exit
codes and every received byte. On 2026-11-19 (v0.1.3) every one of 275
receives was right and none corrupted; the two defects it found in how a
relayed transfer ends are fixed and listed there. On 2026-11-20 v0.1.9
passed the whole matrix again: 275 of 275, none corrupted.

## Operating notes

On Linux quic-go, which carries the direct path, asks for UDP socket
buffers larger than a default `net.core.rmem_max` allows and logs
`failed to sufficiently increase receive buffer size` when it cannot get
them ([quic-go: UDP buffer sizes](https://github.com/quic-go/quic-go/wiki/UDP-Buffer-Sizes)).
Raise it (and `wmem_max`) on the receiving machine:

```console
$ sudo sysctl -w net.core.rmem_max=7500000 net.core.wmem_max=7500000
$ printf 'net.core.rmem_max=7500000\nnet.core.wmem_max=7500000\n' | sudo tee /etc/sysctl.d/90-quic.conf
```

## Install

Release binaries for Linux, macOS and Windows on amd64 and arm64 are on
[Releases](https://github.com/nfltr/send/releases). Each release
carries `SHA256SUMS`, the SHA-256 of every binary, and
`SHA256SUMS.sshsig`, its signature by nfltr-send's own release key in
[`release/allowed_signers`](release/allowed_signers), Ed25519
`SHA256:TqSS4Ad7mrwaNN6vZmr2liyFAK8Gk5AGDg0KH0evc9I`. Checking both needs `ssh-keygen` from OpenSSH 8.1 or later
(`ssh -V` prints the version); its `Good "nfltr-release" signature` line
names the key's fingerprint, which is the one above. The commands below
take the latest release; `download/<version>` in place of
`latest/download` takes another.

Linux (on arm64, `nfltr-send_linux_arm64`):

```console
$ f=nfltr-send_linux_amd64; u=https://github.com/nfltr/send/releases/latest/download
$ for x in $f SHA256SUMS SHA256SUMS.sshsig; do curl -fsSLO $u/$x; done
$ curl -fsSLO https://raw.githubusercontent.com/nfltr/send/main/release/allowed_signers
$ ssh-keygen -Y verify -f allowed_signers -I nfltr-release -n nfltr-release -s SHA256SUMS.sshsig < SHA256SUMS
$ grep " $f\$" SHA256SUMS | sha256sum -c && mkdir -p ~/.local/bin && install -m 755 $f ~/.local/bin/nfltr-send
```

macOS (on Intel, `nfltr-send_darwin_amd64`), which checks the sum with
`shasum`:

```console
$ f=nfltr-send_darwin_arm64; u=https://github.com/nfltr/send/releases/latest/download
$ for x in $f SHA256SUMS SHA256SUMS.sshsig; do curl -fsSLO $u/$x; done
$ curl -fsSLO https://raw.githubusercontent.com/nfltr/send/main/release/allowed_signers
$ ssh-keygen -Y verify -f allowed_signers -I nfltr-release -n nfltr-release -s SHA256SUMS.sshsig < SHA256SUMS
$ grep " $f\$" SHA256SUMS | shasum -a 256 -c && mkdir -p ~/.local/bin && install -m 755 $f ~/.local/bin/nfltr-send
```

`~/.local/bin` is where nfltr's installer puts `nfltr`; add it to `PATH` if
it is not there yet.

Windows, in PowerShell (on arm64, `nfltr-send_windows_arm64.exe`):

```powershell
PS> $ProgressPreference = "SilentlyContinue"; $f = "nfltr-send_windows_amd64.exe"; $u = "https://github.com/nfltr/send/releases/latest/download"
PS> foreach ($x in $f, "SHA256SUMS", "SHA256SUMS.sshsig") { Invoke-WebRequest "$u/$x" -OutFile $x }
PS> Invoke-WebRequest "https://raw.githubusercontent.com/nfltr/send/main/release/allowed_signers" -OutFile allowed_signers
PS> cmd /c "ssh-keygen -Y verify -f allowed_signers -I nfltr-release -n nfltr-release -s SHA256SUMS.sshsig < SHA256SUMS"; if ($LASTEXITCODE) { throw "SHA256SUMS is not signed by the release key" }
PS> $sum = (Select-String -Path SHA256SUMS -SimpleMatch " $f").Line.Split(" ")[0]
PS> if ((Get-FileHash $f -Algorithm SHA256).Hash -ne $sum) { throw "$f does not match SHA256SUMS" }
PS> New-Item -ItemType Directory -Force "$HOME\bin" | Out-Null; Move-Item -Force $f "$HOME\bin\nfltr-send.exe"
```

`cmd /c` hands `SHA256SUMS` to `ssh-keygen` byte for byte, which a
PowerShell pipe does not; add `$HOME\bin` to `PATH` if it is not there
yet.

With Go, built from source instead of the signed binary:

```console
$ go install github.com/nfltr/send/cmd/nfltr-send@latest
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
