#!/bin/sh
# Copyright 2026 nfltr
# SPDX-License-Identifier: Apache-2.0
#
# release.sh: nfltr-send's release files in dist/ (Makefile "release").
#   build   the binaries for linux, darwin, windows x amd64, arm64 and
#           SHA256SUMS; reproducible: the same commit builds the same bytes
#   sign    SHA256SUMS.sshsig: SHA256SUMS signed with $SIGNING_KEY (an
#           OpenSSH key file, or its .pub with the key in ssh-agent)
#   verify  SHA256SUMS.sshsig against release/allowed_signers, then every
#           file against SHA256SUMS
#   dryrun  build twice and compare, sign with a throwaway key, verify
set -eu
cd "$(dirname "$0")/.."
NS=nfltr-release
PLATFORMS="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64"

sums() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$@"; else shasum -a 256 "$@"; fi; }

build() { # build <dir>
	rm -rf "$1" && mkdir -p "$1"
	for p in $PLATFORMS; do
		os=${p%/*} arch=${p#*/} ext=
		[ "$os" = windows ] && ext=.exe
		CGO_ENABLED=0 GOOS=$os GOARCH=$arch GOFLAGS=-mod=readonly \
			go build -trimpath -ldflags "-s -w -buildid=" -o "$1/nfltr-send_${os}_${arch}$ext" ./cmd/nfltr-send
	done
	(cd "$1" && sums nfltr-send_* >SHA256SUMS)
}

sign() { # sign <dir> <key>
	rm -f "$1/SHA256SUMS.sig" "$1/SHA256SUMS.sshsig"
	ssh-keygen -q -Y sign -f "$2" -n "$NS" "$1/SHA256SUMS"
	mv "$1/SHA256SUMS.sig" "$1/SHA256SUMS.sshsig"
}

verify() { # verify <dir> <allowed_signers>
	ssh-keygen -Y verify -f "$2" -I "$NS" -n "$NS" -s "$1/SHA256SUMS.sshsig" <"$1/SHA256SUMS"
	(cd "$1" && sums -c SHA256SUMS)
}

case ${1:-} in
build) build dist ;;
sign) sign dist "${SIGNING_KEY:?set SIGNING_KEY to the release key file}" ;;
verify) verify dist release/allowed_signers ;;
dryrun)
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT
	build dist
	build "$tmp/again"
	cmp dist/SHA256SUMS "$tmp/again/SHA256SUMS" || { echo "release: two builds of one commit differ" >&2; exit 1; }
	echo "reproducible: two builds, the same SHA256SUMS"
	ssh-keygen -q -t ed25519 -N '' -C dryrun -f "$tmp/key"
	echo "$NS namespaces=\"$NS\" $(cut -d' ' -f1,2 "$tmp/key.pub")" >"$tmp/allowed_signers"
	sign dist "$tmp/key"
	verify dist "$tmp/allowed_signers"
	echo "dry run ok: dist/ signed with a throwaway key (not a release)"
	;;
*) echo "usage: $0 build|sign|verify|dryrun" >&2; exit 1 ;;
esac
