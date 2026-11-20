# nfltr-send v1.0 surface review

Lane SPAPI, 2026-11-20, at send v0.1.9 (`7b6c86a`), beside nfltr-pipe
v0.1.4 (`e39832e`) and nfltr's `cat` commands (nfltr `57786a6`), as
link's review (link `docs/v1-api-review.md`) did for link. Every item a
1.x release keeps (docs/v1-contract.md) was asked: is it needed, named
well, documented with its failure modes, and the same as nfltr-pipe's
and nfltr cat's for the same idea? A change after 1.0 needs a 2.0, so
every "change" below is applied in this lane (CHANGELOG.md
"Unreleased"); `TestContractDoc` keeps the doc and the code equal.

## Findings and decisions

| # | kind | finding | decision |
|---|---|---|---|
| 1 | repository | `cmd/nfltr-send/nfltr-send`, a 26 MB darwin binary, was committed with V1CUT (`eb8ce4b`): `.gitignore`'s `/nfltr-send` covers only a build at the root | removed; `.gitignore` names the cmd build; `TestNoTrackedBinaries` fails on any tracked ELF, Mach-O or PE file. The blob stays in history (a public main is not rewritten here: the owner decides) |
| 2 | Go API | `xfer` and `xfer/verified` were importable: a v1.0.0 tag makes every exported name of the module a Go semver promise, whatever the contract said ("`xfer` is not a 1.x API"); nothing outside this module imports them (GitHub code search of the nfltr organisation, nfltr's tree) | moved to `internal/xfer`, `internal/xfer/verified`: the module has no Go API. A library later is a 1.x minor (a new package) |
| 3 | JSON | receive `--json`'s failure line named the message `error`; nfltr's `cat` events name it `message` (`{"event":"error","code":...,"message":...}`) | `message` |
| 4 | JSON | `first_byte_s` beside `seconds`: two spellings of the unit; nfltr's metrics say `..._first_byte_seconds` | `first_byte_seconds` |
| 5 | exit codes | SIGHUP (a closed terminal, ssh ending its session) killed the sender untyped: its receivers waited for the link's liveness to see the end. nfltr-pipe and `nfltr cat pipe` treat SIGHUP as a stop | SIGHUP is a stop: the sender closes its sessions and exits 129 (the number a shell already reported for death by SIGHUP, so no script sees another number); a receive stops with 6 and resumes, as at SIGINT |
| 6 | CLI | Go `flag` stops at the first argument: `nfltr-send send report.pdf --once` sent a file named `--once` (exit 1, "no such file"). nfltr's commands take flags after their arguments (`nfltr cat forward home/db --listen ...`) | flags before, between or after the files; every word after the first `--` is a file |
| 7 | CLI | receive takes the address only from `--address-file` (default stdin); nfltr-pipe's dial also takes it as an argument | kept: different on purpose. A send address serves until the sender stops (one holder can receive many times), so it never goes on a command line other users see; a pipe address is spent by its one session and the listener exits. `nfltr cat` refuses it on the command line too |
| 8 | CLI | the address is printed on stdout; nfltr-pipe prints it on stderr | kept: nfltr-pipe's stdout is the session's bytes |
| 9 | exit codes | 130/143 for a stopped sender while nfltr-pipe exits 6 when stopped | kept: send serves until it is stopped, so a stop is its normal end and must differ from a failure (6) and from a `--once` sender's completed transfer (0); a pipe end stopped mid-session cut the session (6) |
| 10 | naming | `--relay` / `$NFLTR_SEND_RELAY` and `--relay-cert-sha256` here and in nfltr-pipe; nfltr's cat commands say `--server` / `$NFLTR_SERVER` and `--pin-cert` | kept here: the two products agree with each other and with `nfltr config set-relay --cert-sha256`; nfltr's cat flag names are nfltr's own (named for its follow-up below) |
| 11 | naming | `--machine-credential <file>` while every message says "machine certificate" | kept: the file (`machine.json`) holds the certificate and names its key file; nfltr-pipe uses the same flag. Renaming one of two products alone would split them |
| 12 | code | `machine.go` and `saved_relay.go` are the same file in send and pipe | no surface change (both are `package main`); a later move into the relay SDK changes neither CLI |

Checked and kept as they are: the commands (`send`, `receive`, `help`,
`version`), every flag's default and precedence, `$NFLTR_SEND_RELAY`,
`$NFLTR_RELAY_CERT_SHA256`, `$NFLTR_CONFIG`, the exit table (the relay
SDK's, one table with nfltr-pipe and `nfltr cat pipe`; 8 left to
`nfltr cat`'s parent_gone), the remaining `--json` keys (`files`,
`bytes`, `written`, `seconds`, `sessions`, `paths`, `exit`, `code`; the
same `files`/`bytes` as nfltr cat cp's answer), the nf1 address format,
the transfer protocol's version rule, and the release file names. Files
send writes: the received files, and the stage directory
(`.nfltr-send-*`, not covered), and the renewed `machine.json` (nfltr's
format, written in place).

## Follow-up (not in this repository)

- nfltr: `nfltr cat`'s `--pin-cert` is `config set-relay --cert-sha256`'s
  and nfltr-send's `--relay-cert-sha256`; one name across nfltr is
  nfltr's decision (nfltr has no 1.0 freeze yet).
- The 26 MB blob in send's history (finding 1): a history rewrite of a
  public repository is the owner's call.
