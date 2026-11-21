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
# (the old receiver's protocol error). Results in REL_OUT/<sender>-<receiver>.
# Exit 0 only when every transfer of both ways refused so.
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
cells=${CROSS_CELLS:-'^(net-100mbit-1ms-loss0-(direct|relayed)|anon-direct)$'}
rc=0
for pair in "nfltr-send-prev nfltr-send 4" "nfltr-send nfltr-send-prev 6"; do
	set -- $pair
	echo "== sender $1, receiver $2"
	REL_EXPECT=$3 REL_BIN=$bin REL_OUT=$out/$1-$2 REL_SEND_S=$1 REL_SEND_R=$2 REL_N=${REL_N:-1} REL_SMALL=${REL_SMALL:-500} \
		REL_CELLS=$cells sh "$here/bench/rel/run.sh" matrix || rc=1
done
exit $rc
