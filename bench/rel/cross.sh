#!/bin/sh
# Copyright 2026 nfltr
# SPDX-License-Identifier: Apache-2.0
#
# cross.sh: the released nfltr-send $SEND_PREV (GitHub release, signature
# and sum verified: fetch.sh) against nfltr-send built from this checkout,
# both ways, relayed and direct, on rel.py's private relay: the cells
# CROSS_CELLS (default below), REL_N runs each (default 1). Protocol 5 is
# a hard cut (docs/v1-contract.md "Compatibility"): each way must refuse
# at once, nothing written: an old sender and this receiver exit 4
# (refused, naming both versions), this sender and an old receiver exit 6
# (the old receiver's protocol error); with a sender with no account the
# direct path does not form (quiclink v0.8.0 refuses a QUIC peer without
# ACK Frequency), so the receiver exits 12 (direct_required) both ways;
# against v0.1.12 or earlier (direct paths sealed twice, hint d1; nfltr
# docs/decisions/direct-once-2026-11-22.md) this receiver exits 12 at once
# and an older receiver 6 (its reset, naming the sender's reason: no
# direct path formed and no account).
# Results in REL_OUT/<sender>-<receiver>.
# Exit 0 only when every transfer of both ways ended so.
set -eu
here=$(cd "$(dirname "$0")/../.." && pwd)
: "${SEND_PREV:?the previous release, e.g. v0.1.7}" "${NFLTR_VERSION:?}"
out=${REL_OUT:-${TMPDIR:-/tmp}/nfltr-send-cross-$(date -u +%Y%m%dT%H%M%SZ)}
bin=$out/bin
mkdir -p "$bin"
(cd "$here" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$bin/nfltr-send" ./cmd/nfltr-send)
docker build -q -t nfltr-send-rel "$here/bench/rel" >/dev/null
cp "$here/bench/rel/fetch.sh" "$here/release/allowed_signers" "$out/"
docker run --rm -v "$out:/out" -e SEND_VERSION="$SEND_PREV" -e SEND_AS=nfltr-send-prev -e NFLTR_VERSION="$NFLTR_VERSION" \
	nfltr-send-rel sh /out/fetch.sh /out/bin
echo "cross-version: nfltr-send $SEND_PREV and $(cd "$here" && git describe --tags --always --dirty 2>/dev/null || echo this checkout)"
# The refusal is at the first frame: a file relayed and direct, and a
# sender with no account.
cells=${CROSS_CELLS:-'^net-100mbit-1ms-loss0-(direct|relayed)$'}
anon=${CROSS_ANON_CELLS:-'^anon-direct$'}
rc=0
# Against v0.1.12 (protocol 5 too) a sender with an account is carried
# relayed: the file cells must succeed (exit 0) both ways.
case $SEND_PREV in v0.1.1[01]|v0.1.[0-9]) codes="4 12|6 6" ;; *) codes="0 12|0 6" ;; esac
old_ifs=$IFS IFS='|'
set -- $codes
IFS=$old_ifs
for pair in "nfltr-send-prev nfltr-send $1" "nfltr-send nfltr-send-prev $2"; do
	set -- $pair
	for run in "$cells $3 $out/$1-$2" "$anon $4 $out/$1-$2-anon"; do
		set -- "$1" "$2" $run
		echo "== sender $1, receiver $2, cells $3, exit $4"
		REL_EXPECT=$4 REL_BIN=$bin REL_OUT=$5 REL_SEND_S=$1 REL_SEND_R=$2 REL_N=${REL_N:-1} REL_SMALL=${REL_SMALL:-500} \
			REL_CELLS=$3 sh "$here/bench/rel/run.sh" matrix || rc=1
	done
done
exit $rc
