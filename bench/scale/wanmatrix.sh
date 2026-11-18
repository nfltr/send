#!/bin/bash
# Copyright 2026 nfltr
# SPDX-License-Identifier: Apache-2.0
#
# wanmatrix.sh <name> <receiver ssh host> <file>, run on the sending machine:
# for each cap in $CAPS and loss in $LOSSES, $RUNS rounds, each round a
# direct run then a relayed one (UDP between the two machines dropped at the
# receiver: wan.sh block), the impairment set at the receiver (wan.sh on).
# Loss cells (loss > 0) stop the receiver after $LOSS_SECONDS (default 60):
# their goodput is what arrived in that window, slow start included.
# SELF_IP: this machine's address as the receiver sees it (also the relay's,
# when the relay runs here). run.sh's env applies. Tags: <name>-<cap>-<loss>-<path>.
set -uo pipefail
name=$1 rh=$2 file=$3
: "${SELF_IP:?}" "${CAPS:=100mbit 1gbit}" "${LOSSES:=0 0.5 1}" "${RUNS:=3}"
d=$(dirname "$0")
for cap in $CAPS; do
	for loss in $LOSSES; do
		ssh "$rh" "sudo /tmp/wan.sh off; sudo /tmp/wan.sh on $SELF_IP $cap $loss"
		t=${TIMEOUT:-3600}; [ "$loss" != 0 ] && t=${LOSS_SECONDS:-60}
		for i in $(seq "$RUNS"); do
			for path in direct relayed; do
				[ $path = relayed ] && ssh "$rh" "sudo /tmp/wan.sh block $SELF_IP"
				TIMEOUT=$t bash "$d/run.sh" "$name-$cap-$loss-$path" "$rh" "$file"
				[ $path = relayed ] && ssh "$rh" "sudo /tmp/wan.sh unblock $SELF_IP"
			done
		done
	done
done
ssh "$rh" "sudo /tmp/wan.sh off"
