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
- **Verified.** Nothing lands in the receiving directory until every
  file's SHA-256 over what is on the receiver's disk equals the sender's.
  A name that already exists there refuses the transfer.
- **Resumed.** A cut transfer keeps what arrived (in a hidden
  `.nfltr-send-*` directory beside it); the receiver resumes by itself, and
  the next `receive` of the same offer resumes from there. Only what is
  missing is sent again. A refused transfer keeps nothing; a stage
  nobody wrote to for 7 days is an abandoned offer's, and the next
  `receive` into that directory removes it.

The resume rules are a checked TLA+ model: [`spec/`](spec/README.md).

## Credentials

Receiving needs none. Serving an address registers it with the relay, so
the sending machine presents its nfltr machine certificate: by default the
one `nfltr config login` saved beside the nfltr config file
(`machine.json` and `machine.key` next to `$NFLTR_CONFIG`, else
`~/.config/nfltr/`), or the `machine.json` that `--machine-credential`
names (its `machine.key` beside it, readable by its owner only). The
sender renews the certificate while it serves and writes each renewal back
to `machine.json`. A machine without one is told to run
`nfltr config login`; a certificate the relay refused (revoked, expired)
ends the sender with the same advice.

## Install

```console
$ go install github.com/nfltr/send/cmd/nfltr-send@latest
```

Or a release binary (linux, darwin, windows; amd64, arm64) from
[Releases](https://github.com/nfltr/send/releases), checked against its
signed `SHA256SUMS` (`ssh-keygen` from OpenSSH 8.1 or later):

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
$ make check   # gofmt, vet, -race tests
$ make spec    # the TLA+ model
$ make release-dryrun   # the release files in dist/, built twice and compared, signed with a throwaway key
```

A release is `make release`, `make release-sign SIGNING_KEY=<key>`,
`make release-verify` (against `release/allowed_signers`) on a clean
checkout of its tag, then the files in `dist/` attached to the tag's
GitHub release.

License: Apache-2.0.
