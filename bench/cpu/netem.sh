#!/bin/bash
# Copyright 2026 nfltr
# SPDX-License-Identifier: Apache-2.0
#
# netem.sh <old.test> <new.test>: both ends' CPU per GiB of two builds,
# interleaved, on one long lossy path (lane SENDCPU). Each argument is
# cmd/nfltr-send's test binary for linux at the build's commit:
#   GOOS=linux CGO_ENABLED=0 go test -c -o new.test ./cmd/nfltr-send
# (a commit before the benchmark: copy cpu_bench_test.go and anon_test.go in first).
# In one privileged Debian container (docker, the host's architecture),
# netem on lo delays each packet 130 ms (a 260 ms RTT), with JITTER
# (default 300us: reorders) and LOSS % (default 0.025, each way); then
# BenchmarkDirectCPU (-transfer-mb MB, default 2048) RUNS times (default 3)
# per binary, alternating. Prints benchstat-readable lines tagged old/new:
#   bash netem.sh old.test new.test | tee out.txt
#   sed -n 's/^old //p' out.txt >old.txt; sed -n 's/^new //p' out.txt >new.txt; benchstat old.txt new.txt
# PROF=<dir> writes a CPU profile per run there. Both ends run in one
# process, so the ratio between builds is what this shows, not a cloud
# machine's absolute figure.
set -euo pipefail
old=$(cd "$(dirname "$1")" && pwd)/$(basename "$1") new=$(cd "$(dirname "$2")" && pwd)/$(basename "$2")
prof=${PROF:+$(mkdir -p "$PROF" && cd "$PROF" && pwd)}
docker run --rm --privileged --user root -v "$old:/b/old.test:ro" -v "$new:/b/new.test:ro" ${prof:+-v "$prof:/prof"} \
	-e RUNS="${RUNS:-3}" -e MB="${MB:-2048}" -e PROF="${prof:+1}" debian:trixie-slim bash -c '
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive; apt-get update -qq >/dev/null && apt-get install -y -qq --no-install-recommends iproute2 >/dev/null 2>&1
tc qdisc replace dev lo root netem delay 130ms '"${JITTER:-300us}"' loss '"${LOSS:-0.025}"'% limit 200000
cd /tmp
for i in $(seq "$RUNS"); do for b in old new; do
	p=(); [ -n "$PROF" ] && p=(-test.cpuprofile "/prof/$b.$i.cpu")
	/b/$b.test -test.run "^\$" -test.bench BenchmarkDirectCPU -test.benchtime 1x -transfer-mb "$MB" "${p[@]}" 2>/tmp/err | sed -n "s/^Benchmark/$b Benchmark/p" || { cat /tmp/err >&2; exit 1; }
done; done'
