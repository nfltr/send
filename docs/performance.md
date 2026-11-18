# Performance notes

One line per change that moved nfltr-send's throughput, newest first:
what changed, the measurement, and what keeps it. Measure old and new
interleaved on the same machine and report medians.

| change | measured | kept by |
|---|---|---|
| Verified streaming (BLAKE3 root, Bao outboard). The receiver verifies up to 8 arriving chunks at once while it reads the next frames, each read straight into its own buffer (`xfer/receive.go` `run`, `inFlight`); the from-disk check verifies the file's 256 KiB chunks in parallel across GOMAXPROCS (`xfer/verified` `Verify`); the sender no longer hashes what it sends and writes each data frame, header and chunk read in place, in one write (`xfer/send.go` `sendFile`, `writeFrame`) | `BenchmarkTransfer` (64 MiB over a pipe), darwin/arm64, 10 cores, load average 8-9, 16 runs each interleaved: SHA-256 version median 754 MB/s, this one 884 MB/s; under background QoS (`taskpolicy -b`, efficiency cores) 371 and 382 MB/s. Verifying each chunk in line and hashing the file on one core afterwards measured half the SHA-256 version: the library has no arm64 assembly, so one core hashes a chunk at about 435 MB/s. 2 or 4 workers for the from-disk check measured below GOMAXPROCS | `BenchmarkTransfer`; `TestVerify` (parallel check, first bad chunk) |
