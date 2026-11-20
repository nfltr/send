# nfltr-send v1 contract

What every 1.x release keeps. A change to anything below is a 2.0;
adding a command, a flag, a variable, an exit code or a `--json` key is
a 1.x minor release, and a 1.x end never gives an existing one another
meaning. `TestContractDoc` (`cmd/nfltr-send/contract_test.go`, in `make
check`) fails when the flags, the environment variables, the exit codes,
the `--json` keys or the transfer protocol's version differ from the
code's.

## Commands

| command | what |
|---|---|
| `nfltr-send send [flags] <file\|dir>...` | serves the files and directories under a fresh address, printed on stdout (a line of its own), until it is stopped, or with `--once` until the first complete transfer |
| `nfltr-send receive [flags]` | reads the address from `--address-file` (default stdin; never the command line) and writes the offer into `--to`; resumes a cut transfer, by itself and at the next `receive` of the same address |
| `nfltr-send help`, `-h`, `--help` | the usage text on stdout, exit 0 |
| `nfltr-send send -h`, `nfltr-send receive -h` | the usage text and the command's flags on stderr, exit 0 |
| `nfltr-send version`, `--version` | one line on stdout, exit 0: `nfltr-send <version> <commit> <go>`, the module version the go command stamped from the release's tag (a pseudo-version for a build from a checkout), the commit (`unknown` when built from the module proxy) and the Go version that built it |

A receive commits only once every file on its disk is the offer's BLAKE3
root; a name that already exists in `--to` refuses the transfer.

## Flags

| flag | commands | default | meaning |
|---|---|---|---|
| `--relay <address>` | send | `$NFLTR_SEND_RELAY`, else the relay `nfltr config set-relay` saved, else `grpc.nfltr.xyz:443` | the relay's agent-link address the sender registers at |
| `--relay-http <address>` | send | the saved relay's `--proxy-url` host, else `--relay` without its `grpc.` prefix; port 443 dropped | the relay HTTP host the address names |
| `--cert-sha256 <sha256>` | send, receive | `$NFLTR_RELAY_CERT_SHA256`, else the saved relay's pin | accept the relay's certificate by its SHA-256 fingerprint (a private relay's); on receive it verifies the address's relay |
| `--direct on\|off` | send, receive | `on` | `on`: a direct path (QUIC) when the networks allow one, LAN first; `off`: relayed only (refused on send with no machine certificate); any other value is a usage error |
| `--machine-credential <file>` | send | `machine.json` beside the nfltr config file | this machine's certificate (`machine.key` beside it, readable by its owner only), renewed in place while serving; with none at the default the address is served with no account: direct paths only |
| `--once` | send | off | exit after the first complete transfer |
| `--address-file <file>` | receive | `-` (stdin) | where the address is read from (a file others may read is refused, except on Windows) |
| `--to <dir>` | receive | `.` | the receiving directory |
| `--tries <n>` | receive | 10 | sessions to try before giving up, each resuming the last |
| `--json` | receive | off | end with one JSON line on stderr (below) |

Flags are Go `flag` syntax: `-flag value`, `--flag value`, `--flag=value`,
before, between or after the arguments (as nfltr's commands take them);
every word after the first `--` is an argument (a file named `-x`).
receive takes no relay flag: `--relay` or `--relay-http` is a usage error
(exit 1), since the address names its relay. It ignores
`$NFLTR_SEND_RELAY`: the address names its relay; the variable applies to
send only.

## Environment

| variable | read by | meaning |
|---|---|---|
| `$NFLTR_SEND_RELAY` | send | `--relay`'s default; receive ignores it (the address names its relay; the variable applies to send only) |
| `$NFLTR_RELAY_CERT_SHA256` | send, receive | `--cert-sha256`'s default (the relay SDK's name, shared with nfltr) |
| `$NFLTR_CONFIG` | send, receive | the nfltr config file (default `~/.config/nfltr/nfltr.json`): its saved relay (`server`, `proxy_url`, `relay_cert_sha256`; nothing of an account), and the directory of `machine.json` |

## Exit codes

One table for nfltr-send and nfltr-pipe: the relay SDK's (`nfnet.Code`),
named as the SDK names them, so a number means the same in both and in
`nfltr cat pipe`.

| exit | code | meaning |
|---|---|---|
| 0 | | done |
| 1 | `usage` | bad flags, address or configuration, before any connection |
| 2 | `auth` | the relay refused this machine's certificate, or serves no address without an account (a relay account state) |
| 3 | `not_found` | nothing serves at the address |
| 4 | `refused` | the other end or the relay refused |
| 5 | `unreachable` | the relay or the other end was not reached in time |
| 6 | `reset` | the session was cut |
| 7 | `e2ee_pin_mismatch` | the other end is not the one the address names |
| 9 | `balance_exhausted` | the serving account's balance is exhausted (a relay account state) |
| 10 | `e2ee_client_refused` | the other end refused this end's proof: the address's secret is not its |
| 12 | `direct_required` | no direct path to an end serving with no account, or that path was lost |

8 and 11 are not used (the relay SDK leaves them to other programs).
nfltr-send's sender adds the shell's exits for a stop:

| exit | code | meaning |
|---|---|---|
| 129 | | send stopped by SIGHUP (its terminal closed, ssh ended its session) |
| 130 | | send stopped by SIGINT |
| 143 | | send stopped by SIGTERM |

When nfltr-send exits with each:

- 0: receive: every file committed; send: `--once` after its transfer.
- 1: a flag (receive given a relay, `--direct` neither on nor off), the
  address, the address file, the machine certificate, send's `--direct
  off` with no certificate.
- 2: send: the relay refused the machine certificate at registration, or
  serves no address without an account.
- 3: receive: the sender stopped, never ran, or its `--once` transfer
  ended.
- 4: receive: a name exists in `--to`, the sender is busy (16 sessions)
  or refused the commit, the sender speaks no protocol version from 4 to
  the receiver's own; the relay's limits.
- 5: the relay or the sender not reached within the open bound; the
  relay's certificate refused (`--cert-sha256`).
- 6: receive: the last of `--tries` sessions cut, a protocol error, the
  receive stopped (SIGINT, SIGTERM, SIGHUP; receive the address again
  to resume); send: the relay ended the address.
- 7, 10: receive: the sender's key is not the address's; the sender
  refused this receive's proof.
- 9: either, on a relay that bills.
- 12: receive: no direct path formed (a session that opened and lost it
  resumes first).
- 129, 130, 143: send stopped by a signal, before or during a transfer.

receive tries another session after 3, 5 and 6 (up to `--tries`); 1, 2,
4, 7, 9, 10, a protocol error and a refused relay certificate end it at
once. `code` is the name receive `--json` gives the exit.

## receive --json

One JSON object on its own line, last on stderr.

| key | line | meaning |
|---|---|---|
| `files` | success | files received |
| `bytes` | success | bytes of the offer |
| `written` | success | file bytes this receive wrote, resumes included |
| `seconds` | success | from the receive's start to the commit |
| `first_byte_seconds` | success | from the start to the first file byte |
| `sessions` | success | sessions used |
| `paths` | success | each path the transfer used, in order (`direct`, `relayed`, the relayed one's reason after a `/`) |
| `exit` | failure | the exit status |
| `code` | failure | its name (the table above) |
| `message` | failure | why (the key nfltr's `cat` events name it by) |

The values' types are covered; the reason after `relayed/` is not.

## Address

`nf1` followed by base32 (RFC 4648 alphabet, lowercase, no padding) of:
version (1) | length of R | R | rid (16 bytes) | pin (32) | secret (32) |
CRC-32C of the bytes before it. R is the relay's HTTP host (no port when
443); rid the rendezvous id bound to the sender's key; pin the sender's
TLS key keyed by the secret; the secret 32 random bytes
(`github.com/nfltr/relay/client/capaddr`). It contains no `/` or `@`.
send prints it on stdout, alone on its line; the address is a secret:
whoever holds it can receive. A 1.x receive reads every address a 1.x
sender prints.

## Compatibility

A 1.x end works with any 1.x end, both ways: a sender of one 1.x release
and a receiver of another complete a transfer of a file and of many
small files, resume a transfer cut at either end, and end with the exit
codes above, relayed and direct, and with a sender with no account.
Every 1.x end speaks version 4 (`xfer.MinVersion`) of the transfer
protocol. The sender names its highest version in the manifest (a frame
that stays version 4's in all of 1.x); the receiver takes any from 4 to
its own (`xfer.Version`), names the lower of the two in its answer (the
want), and both speak that one; a receiver refuses a manifest below 4
(exit 4), a sender a want outside 4 to its own. A v0.1 receiver names
none, which is 4. `make cross-version`
(`bench/rel/cross.sh`, a check workflow job; on the nfltr CI runners
`make ci-request REPO=send REF=<sha> TARGET=cross-version`) runs the
previous release (`SEND_PREV`, downloaded from its GitHub release, its
signature and sum verified) against this checkout both ways on a private
relay. Both ends need a relay that serves nf1 addresses (nfltr-relay
`NF1_ENABLED`; `NF1_ANON_ENABLED` for senders with no account).

Release files: `nfltr-send_<os>_<arch>` (`.exe` on Windows) for linux,
darwin and windows on amd64 and arm64, `SHA256SUMS`, and
`SHA256SUMS.sshsig` (namespace `nfltr-release`, keys in
`release/allowed_signers`).

## Not covered

- Text on stderr other than the `--json` line: messages, progress,
  their wording and order, and the usage text's layout.
- Timings: the open bounds, the pause between sessions, liveness bounds,
  the 7-day bound on an abandoned stage.
- The stage directory (`.nfltr-send-*` in `--to`): its layout, and a
  receive of one release resuming a stage another release left (each
  release resumes its own; across releases a receive may start over).
- The sender's session limits (16 at once), which path a transfer takes,
  throughput, memory.
- Builds with `-tags sendtrace` (`$SENDTRACE`), the relay SDK's own
  diagnostics variables. The module has no Go API: its packages are
  under `internal/`.
