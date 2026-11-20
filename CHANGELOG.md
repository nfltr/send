# Changelog

## Unreleased

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
