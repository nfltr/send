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
  missing is sent again.

Serving an address needs an nfltr account's API key (`--api-key-file` or
`$NFLTR_API_KEY`); receiving needs none.

The resume rules are a checked TLA+ model: [`spec/`](spec/README.md).

## Build

```console
$ go build ./cmd/nfltr-send
$ go test ./...
```

License: Apache-2.0.
