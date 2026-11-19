# Copyright 2026 nfltr
# SPDX-License-Identifier: Apache-2.0
# The reliability harness's machine: network namespaces, netem, iptables,
# Python for the harness, and what fetch.sh needs.
FROM debian:trixie-slim
RUN apt-get update -qq && apt-get install -y -qq --no-install-recommends \
	iproute2 iptables ethtool python3 openssl openssh-client curl ca-certificates procps \
	&& rm -rf /var/lib/apt/lists/*
