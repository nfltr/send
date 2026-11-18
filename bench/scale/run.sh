#!/bin/bash
# Copyright 2026 nfltr
# SPDX-License-Identifier: Apache-2.0
#
# run.sh <tag> <receiver ssh host> <source>...: one transfer, run on the
# sending machine; appends one JSON line to $OUT.
#   start_to_address_s  sender start (cold page cache) to the address printed
#                       (its root and outboard built over the sources)
#   recv.wall_s         receiver process start to exit (files committed in
#                       place), measured on the receiver by GNU time
#   total_s             start_to_address_s + recv.wall_s (leaves out the
#                       ssh hop that carries the address, a few RTTs)
#   send/recv cpu (user+sys s), peak RSS (KiB): GNU time of each process
#   recv.json           the receiver's --json line (bytes, written, seconds,
#                       first_byte_s, sessions, paths)
# Env: RELAY RELAY_HTTP RELAY_PIN (the relay), NFLTR_CONFIG (the sender's
# machine certificate beside it), RECV_DIR (default /data/in, emptied first),
# SEND_FLAGS RECV_FLAGS (extra flags), RECV_WRAP (a command the receiver
# runs under GNU time, e.g. strace), DROP (1: drop both page caches first),
# TIMEOUT (s, default 3600: the receiver is stopped then; arrived_bytes
# counts its stage too, so goodput_mbit is what arrived per second either way), OUT (default results.jsonl).
set -uo pipefail
tag=$1 rh=$2; shift 2
: "${RELAY:?}" "${RELAY_HTTP:?}" "${RELAY_PIN:?}"
RECV_DIR=${RECV_DIR:-/data/in} OUT=${OUT:-results.jsonl} TIMEOUT=${TIMEOUT:-3600}
rargs="--relay $RELAY --relay-http $RELAY_HTTP --relay-pin $RELAY_PIN"
w=$(mktemp -d)
stop() { pkill -P $spid 2>/dev/null; kill $spid 2>/dev/null; }  # GNU time's child, then time
trap 'stop; rm -rf "$w"' EXIT
ssh "$rh" "rm -rf $RECV_DIR && mkdir -p $RECV_DIR"
if [ "${DROP:-1}" = 1 ]; then
	sync; echo 3 | sudo tee /proc/sys/vm/drop_caches >/dev/null
	ssh "$rh" 'sync; echo 3 | sudo tee /proc/sys/vm/drop_caches >/dev/null'
fi
src_bytes=$(du -sb "$@" | awk '{s+=$1} END {print s}')
t0=$(date +%s.%N)
/usr/bin/time -f '%e %U %S %M' -o "$w/send.time" nfltr-send send --once $rargs ${SEND_FLAGS:-} "$@" >"$w/addr" 2>"$w/send.log" &
spid=$!
until grep -q '^nf1' "$w/addr" 2>/dev/null; do
	kill -0 $spid 2>/dev/null || { echo "sender exited before its address:" >&2; cat "$w/send.log" >&2; exit 1; }
	sleep 0.02
done
t1=$(date +%s.%N)
# The receiver: GNU time's line, then its exit status, then its --json line.
out=$(ssh "$rh" "cd /tmp && /usr/bin/time -f '%e %U %S %M' -o /tmp/recv.time timeout $TIMEOUT ${RECV_WRAP:-} nfltr-send receive --json $rargs ${RECV_FLAGS:-} --to $RECV_DIR 2>/tmp/recv.err; echo \"rc \$?\"; cat /tmp/recv.time; grep '^{' /tmp/recv.err | tail -1" <"$w/addr")
t2=$(date +%s.%N)
for _ in $(seq 300); do kill -0 $spid 2>/dev/null || break; sleep 0.1; done
stop; wait $spid; src=$?
got=$(ssh "$rh" "du -sb --exclude='.nfltr-send-*' $RECV_DIR | cut -f1; du -sb $RECV_DIR | cut -f1")
arrived=${got#*$'\n'} got=${got%%$'\n'*}
python3 -I - "$tag" "$src_bytes" "$t0" "$t1" "$t2" "$src" "$got" "$(cat "$w/send.time" 2>/dev/null)" "$out" "$(uptime)" "$arrived" <<'PY' >>"$OUT"
import json, sys
tag, size, t0, t1, t2, src, got, stime, out, up, arrived = sys.argv[1:]
lines = out.splitlines()
rc = next((int(l.split()[1]) for l in lines if l.startswith("rc ")), None)
rt = next((l.split() for l in lines if l and l[0].isdigit()), None)
js = next((json.loads(l) for l in lines if l.startswith("{")), {})
st = stime.split()
def proc(t):
    return {"wall_s": float(t[0]), "cpu_s": round(float(t[1]) + float(t[2]), 2), "rss_kib": int(t[3])} if t and len(t) == 4 else None
r = {"tag": tag, "bytes": int(size), "start_to_address_s": round(float(t1) - float(t0), 3),
     "send": dict(proc(st) or {}, exit=int(src)), "recv": dict(proc(rt) or {}, exit=rc, json=js),
     "received_bytes": int(got or 0), "arrived_bytes": int(arrived or 0), "load": up.split("load average:")[-1].strip()}
r["total_s"] = round(r["start_to_address_s"] + r["recv"].get("wall_s", 0), 3)
if r["recv"].get("wall_s"):
    r["goodput_mbit"] = round(r["arrived_bytes"] * 8 / r["recv"]["wall_s"] / 1e6, 1)
r["ok"] = rc == 0 and int(got or 0) >= int(size) - 4096 * 200
print(json.dumps(r))
sys.stderr.write("%s ok=%s addr=%.2fs recv=%ss total=%.2fs paths=%s\n" % (tag, r["ok"], r["start_to_address_s"], r["recv"].get("wall_s"), r["total_s"], js.get("paths")))
PY
