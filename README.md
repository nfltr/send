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

## Build

```console
$ go build ./cmd/nfltr-send
$ make check   # gofmt, vet, -race tests
$ make spec    # the TLA+ model
```

License: Apache-2.0.
