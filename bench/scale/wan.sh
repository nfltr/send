#!/bin/bash
# Copyright 2026 nfltr
# SPDX-License-Identifier: Apache-2.0
#
# wan.sh, run on the receiving machine (root):
#   on <peer ip> <rate|none> <loss %>  impair what arrives from <peer>: netem on an ifb
#                                      behind the ingress (GRO/LRO off, so netem sees
#                                      wire packets, not coalesced ones); queue limit
#                                      one 200 ms bandwidth-delay product
#   off                                remove it, GRO back on
#   block <peer ip> | unblock <peer ip> drop UDP to and from <peer> (no direct path)
set -euo pipefail
dev=$(ip route get 1.1.1.1 | awk '{for (i = 1; i < NF; i++) if ($i == "dev") print $(i + 1)}')
case "$1" in
on)
	peer=$2 rate=$3 loss=$4
	modprobe ifb numifbs=1 && ip link set ifb0 up
	ethtool -K "$dev" gro off lro off 2>/dev/null || ethtool -K "$dev" gro off
	tc qdisc replace dev "$dev" handle ffff: ingress
	tc filter replace dev "$dev" parent ffff: protocol ip prio 1 u32 match ip src "$peer"/32 action mirred egress redirect dev ifb0
	if [ "$rate" = none ]; then
		tc qdisc replace dev ifb0 root netem loss "$loss"% limit 100000
	else
		bps=$(echo "$rate" | awk '/gbit$/ {print $1 * 1e9; next} /mbit$/ {print $1 * 1e6}')
		limit=$(awk -v b="$bps" 'BEGIN {l = int(b * 0.2 / 8 / 1500); print (l < 1000 ? 1000 : l)}')
		tc qdisc replace dev ifb0 root netem rate "$rate" loss "$loss"% limit "$limit"
	fi ;;
off)
	tc qdisc del dev "$dev" ingress 2>/dev/null || true
	tc qdisc del dev ifb0 root 2>/dev/null || true
	ethtool -K "$dev" gro on 2>/dev/null || true ;;
block)
	iptables -I INPUT -p udp -s "$2" -j DROP
	iptables -I OUTPUT -p udp -d "$2" -j DROP ;;
unblock)
	iptables -D INPUT -p udp -s "$2" -j DROP 2>/dev/null || true
	iptables -D OUTPUT -p udp -d "$2" -j DROP 2>/dev/null || true ;;
*) echo "usage: wan.sh on <peer> <rate|none> <loss> | off | block <peer> | unblock <peer>" >&2; exit 1 ;;
esac
