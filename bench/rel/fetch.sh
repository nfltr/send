#!/bin/sh
# Copyright 2026 nfltr
# SPDX-License-Identifier: Apache-2.0
#
# fetch.sh <dir>: the released binaries the reliability harness runs, each
# verified: nfltr-send $SEND_VERSION (GitHub release, SHA256SUMS signed by
# release/allowed_signers) and nfltr + nfltr-relay $NFLTR_VERSION (the nfltr
# installer, which verifies its own signatures), for linux/amd64.
set -eu
d=$1 here=$(cd "$(dirname "$0")/../.." && pwd)
: "${SEND_VERSION:?}" "${NFLTR_VERSION:?}"
mkdir -p "$d" && cd "$d"
f=nfltr-send_linux_amd64 u=https://github.com/nfltr/send/releases/download/$SEND_VERSION
for x in $f SHA256SUMS SHA256SUMS.sshsig; do curl -fsSLO "$u/$x"; done
ssh-keygen -Y verify -f "$here/release/allowed_signers" -I nfltr-release -n nfltr-release -s SHA256SUMS.sshsig <SHA256SUMS >/dev/null
grep " $f\$" SHA256SUMS | sha256sum -c --quiet
mv $f nfltr-send && chmod +x nfltr-send && rm SHA256SUMS SHA256SUMS.sshsig
curl -fsSL https://nfltr.xyz/install.sh -o install.sh
NFLTR_VERSION=$NFLTR_VERSION NFLTR_INSTALL_DIR=$d sh install.sh --with relay >install.log 2>&1 || { tail -20 install.log >&2; exit 1; }
rm install.sh
for b in nfltr-send nfltr nfltr-relay; do test -x "$d/$b"; done
