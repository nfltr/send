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

A receive commits only once every file on its disk is the offer's BLAKE3
root; a name that already exists in `--to` refuses the transfer.

## Flags

| flag | commands | default | meaning |
|---|---|---|---|
| `--relay <address>` | send, receive | `$NFLTR_SEND_RELAY`, else the relay `nfltr config set-relay` saved, else `grpc.nfltr.xyz:443` | the relay's agent-link address the sender registers at (receive ignores it: the address names its relay) |
| `--relay-http <address>` | send, receive | the saved relay's `--proxy-url` host, else `--relay` without its `grpc.` prefix; port 443 dropped | the relay HTTP host the address names (receive ignores it) |
| `--relay-pin <sha256>` | send, receive | `$NFLTR_RELAY_CERT_SHA256`, else the saved relay's pin | accept the relay's certificate by its SHA-256 fingerprint (a private relay's); on receive it verifies the address's relay |
| `--direct on\|off` | send, receive | `on` | `on`: a direct path (QUIC) when the networks allow one, LAN first; `off`: relayed only (refused on send with no machine certificate) |
| `--machine-credential <file>` | send | `machine.json` beside the nfltr config file | this machine's certificate (`machine.key` beside it, readable by its owner only), renewed in place while serving; with none at the default the address is served with no account: direct paths only |
| `--once` | send | off | exit after the first complete transfer |
| `--address-file <file>` | receive | `-` (stdin) | where the address is read from (a file others may read is refused, except on Windows) |
| `--to <dir>` | receive | `.` | the receiving directory |
| `--tries <n>` | receive | 10 | sessions to try before giving up, each resuming the last |
| `--json` | receive | off | end with one JSON line on stderr (below) |

Flags are Go `flag` syntax: `-flag value`, `--flag value`, `--flag=value`.

## Environment

| variable | read by | meaning |
|---|---|---|
| `$NFLTR_SEND_RELAY` | send, receive | `--relay`'s default |
| `$NFLTR_RELAY_CERT_SHA256` | send, receive | `--relay-pin`'s default (the relay SDK's name, shared with nfltr) |
| `$NFLTR_CONFIG` | send, receive | the nfltr config file (default `~/.config/nfltr/nfltr.json`): its saved relay (`server`, `proxy_url`, `relay_cert_sha256`; nothing of an account), and the directory of `machine.json` |

## Exit codes

| exit | code | meaning | when |
|---|---|---|---|
| 0 | | done | receive: every file committed; send: stopped (SIGINT, SIGTERM), or `--once` after its transfer |
| 1 | `usage` | bad flags, address or configuration | before any connection |
| 2 | `refused` | refused by the other side | receive: a name exists in `--to`, the sender is busy or refused the commit |
| 3 | `failed` | failed | receive: `--tries` sessions failed, the relay or the sender was not reached, a protocol mismatch, a certificate refused; send: the relay refused or ended the address |
| 12 | `direct_required` | no direct path to a sender serving with no account | receive: no direct path formed (a session that opened and lost it resumes first) |

`code` is the name receive `--json` gives the exit.

## receive --json

One JSON object on its own line, last on stderr.

| key | line | meaning |
|---|---|---|
| `files` | success | files received |
| `bytes` | success | bytes of the offer |
| `written` | success | file bytes this receive wrote, resumes included |
| `seconds` | success | from the receive's start to the commit |
| `first_byte_s` | success | from the start to the first file byte |
| `sessions` | success | sessions used |
| `paths` | success | each path the transfer used, in order (`direct`, `relayed`, the relayed one's reason after a `/`) |
| `exit` | failure | the exit status |
| `code` | failure | its name (the table above) |
| `error` | failure | the message |

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
codes above, relayed and direct, and with a sender with no account. The
transfer protocol is version 4 (`xfer.Version`, in the manifest) for all
of 1.x: a receiver refuses another version. `make cross-version`
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
  diagnostics variables, and the Go packages (`xfer` is not a 1.x API).

## Before freezing: cut or rename

For the owner's decision; nothing here is changed yet.

1. **The exit table is coarser than the SDK's**: 3 (`failed`) covers
   unreachable, not found, reset, a wrong pin and a refused secret,
   which nfltr-pipe and the relay SDK tell apart (5, 3, 6, 7, 10). Adopt
   the SDK's table before 1.0 (afterwards it is a 2.0), or keep 3.
2. **`receive --relay` and `receive --relay-http`**: accepted and
   ignored (the address names its relay); `$NFLTR_SEND_RELAY` too. Cut
   them from receive.
3. **`--relay-pin` and `$NFLTR_RELAY_CERT_SHA256`**: the flag and its
   variable have different names. The variable is the SDK's and nfltr's;
   rename the flag `--relay-cert-sha256` (nfltr's `config set-relay
   --cert-sha256`), or keep both as they are.
4. **`--direct` takes more than on|off**: the SDK also accepts `true`,
   `false` and the empty string (as off). Accept only `on` and `off`.
5. **The protocol version is an equality**: a receiver refuses any
   version but its own (`update both ends`), so a 1.x protocol change
   breaks every older 1.x end. Before 1.0, make the receiver accept a
   range (each 1.x reads every 1.x manifest), or promise version 4 for
   all of 1.x (as this doc does; the test pins it).
6. **A stopped sender exits 0** (SIGINT, SIGTERM), the same as a
   `--once` sender whose transfer completed. Keep (a server stopped is
   not a failure), or give a stop before any transfer its own code.
7. **`send -h` and `receive -h` exit 1**, `help` and `-h` alone exit 0.
   Make a command's `-h` exit 0.
8. **No `version` command**: neither end can say which release it is.
   Add `nfltr-send version` (possible in 1.x, better in 1.0).
9. **Resuming across releases** (above, not covered): decide whether a
   1.x stage is a 1.x promise.
