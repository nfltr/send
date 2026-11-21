#!/bin/bash
# Copyright 2026 nfltr
# SPDX-License-Identifier: Apache-2.0
#
# ab.sh <name>=[module@version,...] ...: cmd/nfltr-send's CPU benchmarks for
# builds that differ only in their dependencies, interleaved on this machine
# (lane RELAYEDREG: which dependency bump moved a figure). Each variant is
# this checkout with `go get` of its modules (none: as checked in):
#   bash bench/cpu/ab.sh base= ps=github.com/nfltr/link/pathswitch@v0.1.4
# BENCH (default 'BenchmarkDirectCPU|BenchmarkRelayedCPU'), RUNS (default 6)
# rounds, MB per transfer (default 2048). Prints benchstat-readable lines
# tagged with the variant's name ("base BenchmarkRelayedCPU-8 ..."), and
# writes them and a CPU profile per variant and round to OUT (default
# $TMPDIR/bench-ab):
#   sed -n 's/^base //p' $OUT/bench.txt >base.txt; ...; benchstat base.txt ps.txt
set -euo pipefail
src=$(cd "$(dirname "$0")/../.." && pwd)
out=${OUT:-${TMPDIR:-/tmp}/bench-ab}
mkdir -p "$out"
out=$(cd "$out" && pwd)
names=()
for v in "$@"; do
	n=${v%%=*} mods=${v#*=}
	w=$(mktemp -d)
	cp -R "$src/." "$w"
	if [ -n "$mods" ]; then (cd "$w" && IFS=, && go get $mods); fi
	(cd "$w" && go list -m github.com/nfltr/link/pathswitch github.com/nfltr/link/peerlink/quiclink github.com/nfltr/quic-go | sed "s/^/$n dep /")
	(cd "$w" && go test -c -o "$out/$n.test" ./cmd/nfltr-send)
	rm -rf "$w"
	names+=("$n")
done
: >"$out/bench.txt"
cd "$out"
for i in $(seq "${RUNS:-6}"); do for n in "${names[@]}"; do
	"./$n.test" -test.run '^$' -test.bench "${BENCH:-BenchmarkDirectCPU|BenchmarkRelayedCPU}" -test.benchtime 1x \
		-transfer-mb "${MB:-2048}" -test.cpuprofile "$n.$i.cpu" 2>"$out/err" | sed -n "s/^Benchmark/$n Benchmark/p" | tee -a "$out/bench.txt" ||
		{ cat "$out/err" >&2; exit 1; }
done; done
