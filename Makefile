# Copyright 2026 nfltr
# SPDX-License-Identifier: Apache-2.0

# make check: gofmt, vet, -race tests; make spec: the TLA+ model's
# invariants, witnesses and mutants (spec/README.md). Both run in CI.
.PHONY: check spec
check:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	go vet ./...
	go test -race ./...

spec:
	@set -e; t=$$(sh scripts/fetch-tla.sh); \
	TLA_JAVA=$$(echo "$$t" | sed -n 1p) TLA_JAR=$$(echo "$$t" | sed -n 2p) sh spec/check.sh
