#!/bin/bash
# Copyright 2026 nfltr
# SPDX-License-Identifier: Apache-2.0
#
# resume.sh <receiver ssh host> <file>, run on the sending machine: one
# sender (--once); a receiver killed (SIGKILL) once its stage holds half the
# file; 4 KiB of the kept part overwritten at 1/10 of the file (one chunk
# that no longer verifies); a second receive of the same address. One JSON
# line to $OUT: bytes kept at the kill, what the second receive wrote
# (--json written), the ideal (missing bytes + one 256 KiB chunk), and the
# times. run.sh's env applies (RELAY RELAY_HTTP RELAY_PIN NFLTR_CONFIG RECV_DIR OUT).
set -uo pipefail
rh=$1 file=$2
RECV_DIR=${RECV_DIR:-/data/in} OUT=${OUT:-results.jsonl}
rargs="--relay $RELAY --relay-http $RELAY_HTTP --relay-pin $RELAY_PIN"
size=$(stat -c %s "$file") half=$(($(stat -c %s "$file") / 2))
w=$(mktemp -d)
ssh "$rh" "rm -rf $RECV_DIR && mkdir -p $RECV_DIR"
sync; echo 3 | sudo tee /proc/sys/vm/drop_caches >/dev/null
t0=$(date +%s.%N)
/usr/bin/time -f '%e %U %S %M' -o "$w/send.time" nfltr-send send --once $rargs "$file" >"$w/addr" 2>"$w/send.log" &
spid=$!
trap 'pkill -P $spid 2>/dev/null; kill $spid 2>/dev/null; rm -rf "$w"' EXIT
until grep -q '^nf1' "$w/addr"; do sleep 0.05; done
t1=$(date +%s.%N)
ssh "$rh" 'umask 077 && cat >/tmp/resume.addr' <"$w/addr"
rpid=$(ssh "$rh" "cd /tmp && nohup nfltr-send receive --json $rargs --address-file /tmp/resume.addr --to $RECV_DIR >/dev/null 2>/tmp/resume1.err </dev/null & echo \$!")
kept=0
until [ "$kept" -ge "$half" ]; do
	sleep 5
	kept=$(ssh "$rh" "find $RECV_DIR -path '*/.nfltr-send-*' -type f -size +1M -printf '%b\n' | sort -n | tail -1")
	kept=$((${kept:-0} * 512)) # allocated bytes: the part may be sized ahead of what arrived
done
ssh "$rh" "kill -9 $rpid"
t2=$(date +%s.%N)
part=$(ssh "$rh" "find $RECV_DIR -path '*/.nfltr-send-*' -type f -size +1M -printf '%b %p\n' | sort -n | tail -1")
kept=$((${part%% *} * 512)) path=${part#* }
# The kept part may be sparse past what arrived; overwrite 4 KiB well inside what arrived.
ssh "$rh" "head -c 4096 /dev/urandom | dd of='$path' bs=4096 seek=$((size / 10 / 4096)) conv=notrunc status=none"
out=$(ssh "$rh" "cd /tmp && /usr/bin/time -f '%e %U %S %M' -o /tmp/resume2.time nfltr-send receive --json $rargs --address-file /tmp/resume.addr --to $RECV_DIR 2>/tmp/resume2.err; echo \"rc \$?\"; cat /tmp/resume2.time; grep '^{' /tmp/resume2.err | tail -1")
t3=$(date +%s.%N)
wait $spid
python3 -I - "$size" "$kept" "$t0" "$t1" "$t2" "$t3" "$out" "$(cat "$w/send.time")" <<'PY' >>"$OUT"
import json, sys
size, kept = int(sys.argv[1]), int(sys.argv[2])
t0, t1, t2, t3 = map(float, sys.argv[3:7])
lines = sys.argv[7].splitlines()
rc = next(int(l.split()[1]) for l in lines if l.startswith("rc "))
rt = next(l.split() for l in lines if l and l[0].isdigit())
js = next((json.loads(l) for l in lines if l.startswith("{")), {})
ideal = size - kept + 256 * 1024
print(json.dumps({"tag": "resume", "bytes": size, "start_to_address_s": round(t1 - t0, 3),
    "first_receive_s": round(t2 - t1, 1), "kept_bytes": kept, "second_receive": {"exit": rc, "wall_s": float(rt[0]),
    "cpu_s": round(float(rt[1]) + float(rt[2]), 1), "rss_kib": int(rt[3]), "json": js},
    "written": js.get("written"), "ideal_written": ideal,
    "resent_beyond_ideal": (js.get("written") or 0) - ideal, "sender": sys.argv[8]}))
PY
tail -1 "$OUT"
