# Changelog

## v0.1.7 (unreleased)

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
