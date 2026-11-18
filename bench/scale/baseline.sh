#!/bin/bash
# Copyright 2026 nfltr
# SPDX-License-Identifier: Apache-2.0
#
# baseline.sh <receiver ssh host> <receiver ip>, run on the sending machine: one
# JSON line with the path's RTT (20 pings) and raw throughput (iperf3, 10 s
# each: TCP one stream, TCP 4 streams, UDP offered at 2 Gbit/s in 1,200-byte
# datagrams, the size a QUIC packet uses).
set -euo pipefail
rh=$1 ip=$2
ping=$(ping -q -c 20 -i 0.2 "$ip" | awk -F/ '/rtt/ {print $5}')
ip3() { ssh "$rh" 'iperf3 -s -1 -D >/dev/null 2>&1'; sleep 1; iperf3 -J -c "$ip" -t 10 "$@"; }
tcp1=$(ip3 | jq '.end.sum_received.bits_per_second / 1e6 | floor')
tcp4=$(ip3 -P 4 | jq '.end.sum_received.bits_per_second / 1e6 | floor')
udp=$(ip3 -u -b 2G -l 1200 | jq -c '{mbit: (.end.sum.bits_per_second / 1e6 * (100 - .end.sum.lost_percent) / 100 | floor), lost_percent: .end.sum.lost_percent}')
echo "{\"to\":\"$rh\",\"rtt_ms\":$ping,\"tcp1_mbit\":$tcp1,\"tcp4_mbit\":$tcp4,\"udp\":$udp}"
