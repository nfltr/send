#!/bin/sh
# Copyright 2026 nfltr
# SPDX-License-Identifier: Apache-2.0
#
# gen.sh <kind> <path> [size]: the test data, incompressible.
#   file <path> <size>  one file of <size> (head -c syntax: 1G, 100G) of AES-CTR keystream
#   small <dir> [n]     n (default 100,000) files of 4 KiB, 1,000 per directory
#   mixed <dir> [n]     n (default 100,000) files, sizes log-uniform 1 KiB-1 MiB (seed 1), 1,000 per directory
set -eu
case "$1" in
file)
	openssl enc -aes-128-ctr -nosalt -pass pass:"$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')" </dev/zero 2>/dev/null |
		head -c "$3" >"$2" ;;
small | mixed)
	python3 -I - "$1" "$2" "${3:-100000}" <<'PY'
import os, random, sys
kind, root, count = sys.argv[1], sys.argv[2], int(sys.argv[3])
rnd = random.Random(1)
total = 0
for i in range(count):
    d = os.path.join(root, "d%03d" % (i // 1000))
    if i % 1000 == 0:
        os.makedirs(d, exist_ok=True)
    n = 4096 if kind == "small" else int(1024 * 1024 ** rnd.random())
    with open(os.path.join(d, "f%06d" % i), "wb") as f:
        f.write(os.urandom(n))
    total += n
print("%s: %d files, %d bytes" % (root, count, total))
PY
	;;
*) echo "usage: gen.sh file <path> <size> | small <dir> [n] | mixed <dir> [n]" >&2; exit 1 ;;
esac
