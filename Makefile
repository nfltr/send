# Copyright 2026 nfltr
# SPDX-License-Identifier: Apache-2.0

# make check: gofmt, vet (here and for windows/amd64), the windows/amd64
# build, -race tests, 10 s of xfer's fuzz target; make spec: the TLA+ model's invariants, witnesses and
# mutants (spec/README.md); make spec-traces: the xfer tests with
# -tags sendtrace, each trace they write checked against its model with TLC,
# and the mutant traces rejected (spec/README.md "Traces"). All run in CI,
# and the tests on Windows too.
.PHONY: ci check check-gosec check-semgrep check-hadolint spec spec-traces bench-line bench-write bench-ab release release-sign release-verify release-dryrun reliability reliability-soak reliability-soak-relayed cross-version
# make ci: the check workflow's Linux jobs but cross-version, for a CI runner
# of our own.
ci: check check-gosec check-semgrep check-hadolint spec spec-traces release-dryrun

# The static security gates nfltr's repositories run in make ci (lane
# SHIFTLEFT): gosec and semgrep (pinned, each finding fixed, suppressed with
# its reason or in scripts/*-baseline.txt; a stale entry fails) and hadolint
# (pinned image, docker) on the tracked Dockerfiles.
check-gosec:
	./scripts/check-gosec.sh

check-semgrep:
	./scripts/check-semgrep.sh

check-hadolint:
	./scripts/check-hadolint.sh

check:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	go vet ./...
	GOOS=windows GOARCH=amd64 go vet ./...
	GOOS=windows GOARCH=amd64 go build -o /dev/null ./cmd/nfltr-send
	go test -race ./...
	go test -run '^$$' -fuzz FuzzReceive -fuzztime 10s ./internal/xfer/

spec:
	@set -e; t=$$(sh scripts/fetch-tla.sh); \
	TLA_JAVA=$$(echo "$$t" | sed -n 1p) TLA_JAR=$$(echo "$$t" | sed -n 2p) sh spec/check.sh

spec-traces:
	@set -e; t=$$(sh scripts/fetch-tla.sh); \
	TLA_JAVA=$$(echo "$$t" | sed -n 1p) TLA_JAR=$$(echo "$$t" | sed -n 2p) sh spec/traces.sh

# make bench-line: BenchmarkDirectCPU on loopback, no netem (2 GiB, 6 runs):
# the direct path's ceiling at each BENCH_K on this machine, both ends in one process,
# CPU s per GiB and Mbit/s; the benchmark text and a CPU profile (with the
# test binary, for pprof) in BENCH_OUT (default $TMPDIR/bench-line: a box
# TARGET run's out). The local gate of lane SPEED's steps (nfltr
# docs/decisions/send-line-rate-2026-11-21.md): old and new interleaved.
BENCH_OUT ?= $${TMPDIR:-/tmp}/bench-line
# BENCH_K: the receive's --connections, one sub-benchmark (k=<n>) each,
# in turn in each of the 6 runs (interleaved; a CPU profile per run).
BENCH_K ?= 1,2,4
bench-line:
	mkdir -p $(BENCH_OUT)
	go test -c -o $(BENCH_OUT)/send.test ./cmd/nfltr-send
	cd $(BENCH_OUT) && : >bench.txt && for i in 1 2 3 4 5 6; do \
		./send.test -test.run '^$$' -test.bench BenchmarkDirectCPU -test.benchtime 1x -test.count 1 \
		-test.cpuprofile cpu-$$i.prof -transfer-mb 2048 -bench-k $(BENCH_K) >>bench.txt || { cat bench.txt; exit 1; }; done; cat bench.txt

# make bench-write: BenchmarkStripedWrite (4 writers into one 1 GiB file,
# as a striped receive writes: each WriteAt at once against the receive's
# writeChunk and preallocate), 6 runs; CPU s per GiB and MiB/s on this
# machine's temp file system (lane SPEED-RECV; nfltr
# docs/decisions/send-line-rate-2026-11-21.md "Step 6").
bench-write:
	go test -run '^$$' -bench BenchmarkStripedWrite -benchtime 1x -count 6 ./internal/xfer/

# make bench-ab AB='base= ps=github.com/nfltr/link/pathswitch@v0.1.4': the
# CPU benchmarks (direct and relayed) of builds differing only in their
# dependencies, interleaved (bench/cpu/ab.sh; BENCH, RUNS, MB; out in
# BENCH_OUT). Which dependency bump moved a figure (lane RELAYEDREG: the
# SPEED-IO bisect ran AB='base= ps=github.com/nfltr/link/pathswitch@v0.1.4
# fork=github.com/nfltr/quic-go@v0.63.1-nfltr.5
# both=github.com/nfltr/link/peerlink/quiclink@v0.7.3'). A box TARGET run
# takes no AB: set it in the requested commit.
AB ?= base=
bench-ab:
	OUT=$(BENCH_OUT) bash bench/cpu/ab.sh $(AB)

# Release files in dist/ (scripts/release.sh): make release builds the six
# binaries and SHA256SUMS, reproducibly; make release-sign SIGNING_KEY=<key>
# signs SHA256SUMS (SHA256SUMS.sshsig); make release-verify checks both
# against release/allowed_signers; make release-dryrun builds twice,
# compares, and signs and verifies with a throwaway key. Tags and GitHub
# releases are made by hand from a verified dist/.
release:
	sh scripts/release.sh build
release-sign:
	sh scripts/release.sh sign
release-verify:
	sh scripts/release.sh verify
release-dryrun:
	sh scripts/release.sh dryrun

# Reliability (bench/rel/README.md), on a Linux machine with docker: make
# reliability runs the matrix (every cell REL_N times, default 5), make
# reliability-soak random cells with faults for REL_SOAK_S seconds (default
# 86400). nfltr-send built from this checkout (or the released SEND_VERSION
# when set), nfltr and nfltr-relay NFLTR_VERSION from the installer, or
# REL_BIN=<dir> with all three. Exit 0 only when every transfer passed.
SEND_VERSION ?=
NFLTR_VERSION ?= v2.1.78
export SEND_VERSION NFLTR_VERSION
reliability:
	sh bench/rel/run.sh matrix
reliability-soak:
	sh bench/rel/run.sh soak
# The relayed soak of docs/reliability.md (defect 3), 25 minutes, by name.
reliability-soak-relayed:
	REL_SEED=$${REL_SEED:-17} REL_SOAK_S=1500 REL_CELLS='relayed$$' sh bench/rel/run.sh soak

# make cross-version: the previous release SEND_PREV (downloaded from its
# GitHub release, signature and sums verified) against nfltr-send built
# from this checkout, both ways, relayed and direct, on a private relay
# (bench/rel/cross.sh; docs/v1-contract.md "Compatibility"); Linux with
# docker. A check workflow job; not in make ci. A release
# moves SEND_PREV to itself.
SEND_PREV ?= v0.1.12
export SEND_PREV
cross-version:
	sh bench/rel/cross.sh
