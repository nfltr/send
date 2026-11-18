#!/bin/sh
# Copyright 2026 nfltr
# SPDX-License-Identifier: Apache-2.0
#
# gen.sh <kind> <path> [size]: the test data, incompressible.
#   file <path> <size>  one file of <size> (head -c syntax: 1G, 100G) of AES-CTR keystream
#   small <dir>         100,000 files of 4 KiB in 100 directories
#   mixed <dir>         100,000 files, sizes log-uniform 1 KiB-1 MiB (seed 1), in 100 directories
set -eu
case "$1" in
file)
	openssl enc -aes-128-ctr -nosalt -pass pass:"$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')" </dev/zero 2>/dev/null |
		head -c "$3" >"$2" ;;
small | mixed)
	python3 -I - "$1" "$2" <<'PY'
import os, random, sys
kind, root = sys.argv[1], sys.argv[2]
rnd = random.Random(1)
total = 0
for i in range(100000):
    d = os.path.join(root, "d%03d" % (i // 1000))
    if i % 1000 == 0:
        os.makedirs(d, exist_ok=True)
    n = 4096 if kind == "small" else int(1024 * 1024 ** rnd.random())
    with open(os.path.join(d, "f%06d" % i), "wb") as f:
        f.write(os.urandom(n))
    total += n
print("%s: 100000 files, %d bytes" % (root, total))
PY
	;;
*) echo "usage: gen.sh file <path> <size> | small <dir> | mixed <dir>" >&2; exit 1 ;;
esac
