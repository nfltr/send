#!/bin/sh
# Copyright 2026 nfltr
# SPDX-License-Identifier: Apache-2.0
#
# run.sh matrix|soak: rel.py in a privileged container (network namespaces,
# netem, iptables, tmpfs), on a Linux machine with docker. Binaries: the
# directory REL_BIN (nfltr-send, nfltr, nfltr-relay), else nfltr-send built
# from this checkout (or the released SEND_VERSION when set) and nfltr and
# nfltr-relay NFLTR_VERSION (fetch.sh, verified). Results in REL_OUT
# (default $TMPDIR/nfltr-send-rel-<mode>-<UTC time>); rel.py's REL_* settings
# pass through. Exit 0 only when every transfer passed.
# Containers mount only REL_OUT and REL_BIN: on a CI runner whose docker is
# the host's, only paths under TMPDIR are the same on both.
set -eu
mode=${1:?usage: run.sh matrix|soak}
here=$(cd "$(dirname "$0")/../.." && pwd)
out=${REL_OUT:-${TMPDIR:-/tmp}/nfltr-send-rel-$mode-$(date -u +%Y%m%dT%H%M%SZ)}
mkdir -p "$out"
img=nfltr-send-rel
docker build -q -t $img "$here/bench/rel" >/dev/null
cp "$here/bench/rel/rel.py" "$here/bench/rel/fetch.sh" "$here/bench/scale/gen.sh" "$here/release/allowed_signers" "$out/"
if [ -z "${REL_BIN:-}" ]; then
	REL_BIN=$out/bin
	mkdir -p "$REL_BIN"
	[ -n "${SEND_VERSION:-}" ] ||
		(cd "$here" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$REL_BIN/nfltr-send" ./cmd/nfltr-send)
	docker run --rm -v "$out:/out" -e SEND_VERSION="${SEND_VERSION:-}" -e NFLTR_VERSION="${NFLTR_VERSION:?}" \
		$img sh /out/fetch.sh /out/bin
fi
echo "results: $out"
set -- -e REL_UID="$(id -u):$(id -g)"
for v in REL_N REL_CELLS REL_TIMEOUT REL_LARGE REL_SMALL REL_SOAK_S REL_SEED; do
	eval "x=\${$v:-}"
	[ -z "$x" ] || set -- "$@" -e "$v=$x"
done
exec docker run --rm --privileged --name "nfltr-send-rel-$$" "$@" \
	-v "$(cd "$REL_BIN" && pwd):/bin-in:ro" -v "$out:/out" $img python3 -u /out/rel.py "$mode"
