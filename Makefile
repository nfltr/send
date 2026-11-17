# Copyright 2026 nfltr
# SPDX-License-Identifier: Apache-2.0

# make check: gofmt, vet (here and for windows/amd64), the windows/amd64
# build, -race tests, 10 s of xfer's fuzz target; make spec: the TLA+ model's invariants, witnesses and
# mutants (spec/README.md). Both run in CI, and the tests on Windows too.
.PHONY: check spec release release-sign release-verify release-dryrun
check:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	go vet ./...
	GOOS=windows GOARCH=amd64 go vet ./...
	GOOS=windows GOARCH=amd64 go build -o /dev/null ./cmd/nfltr-send
	go test -race ./...
	go test -run '^$$' -fuzz FuzzReceive -fuzztime 10s ./xfer/

spec:
	@set -e; t=$$(sh scripts/fetch-tla.sh); \
	TLA_JAVA=$$(echo "$$t" | sed -n 1p) TLA_JAR=$$(echo "$$t" | sed -n 2p) sh spec/check.sh

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
