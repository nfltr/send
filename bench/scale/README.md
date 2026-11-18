# Scale benchmarks

Scripts that measured nfltr-send at scale between Linux machines in
different regions (results: [`docs/scale.md`](../../docs/scale.md)). They
assume Debian or Ubuntu hosts reachable from each other, with `nfltr-send`
and GNU `time` on both ends, `iperf3`, `ethtool`, `iproute2`, `iptables` and
`strace` on the receiver, and ssh from the sending machine to the receiving
one (a ControlMaster connection keeps each run's ssh hop short). A private
nfltr relay (`nfltr-relay serve`, with nf1 addresses on: `NF1_ENABLED=true`,
`NF1_HOSTS=<relay>:<http port>`) carries the signalling and the relayed
path; the sender holds a machine certificate from it (`nfltr-relay machines
code` on the relay, `nfltr config set-relay ... --code-file -` on the
sender), so the relay carries a transfer whose direct path does not form.

| script | runs on | what it does |
|---|---|---|
| `gen.sh` | sender | the data: one file of AES-CTR keystream (`gen.sh file f10g 10G`), 100,000 x 4 KiB (`small`), 100,000 files log-uniform 1 KiB to 1 MiB, seed 1, 15.2 GB (`mixed`) |
| `baseline.sh <host> <ip>` | sender | the path first: RTT (20 pings), iperf3 TCP 1 and 4 streams, UDP offered at 2 Gbit/s in 1,200-byte datagrams |
| `run.sh <tag> <host> <source>...` | sender | one transfer, one JSON line: sender start to address, receiver start to exit (files committed), total, CPU and peak RSS of each process (GNU time), the receiver's `--json` line; page caches dropped first unless `DROP=0`; `RECV_WRAP` runs the receiver under a command (strace for the syncfs); `NFLTR_SEND` names the binary on both ends (to alternate two builds) |
| `wan.sh on <peer> <rate> <loss>` / `off` / `block <peer>` / `unblock <peer>` | receiver (root) | netem on an ifb behind the ingress, for what arrives from the peer only (GRO and LRO off so netem drops wire packets, queue limit one 200 ms bandwidth-delay product); `block` drops UDP to and from the peer, so only the relayed path works |
| `wanmatrix.sh <name> <host> <file>` | sender | caps x losses x `RUNS` rounds, each round a direct run then a relayed one |
| `resume.sh <host> <file>` | sender | a receiver killed (SIGKILL) at half the file, 4 KiB of its kept part overwritten, a second receive of the same address: what it wrote against the ideal (missing bytes plus one 256 KiB chunk); `NFLTR_SEND` as in `run.sh` |
| `summarize.py <jsonl>...` | anywhere | medians per tag as a markdown table |

Example, from the sending machine:

```console
$ export RELAY=relay:8443 RELAY_HTTP=relay:8444 RELAY_PIN=<sha256> NFLTR_CONFIG=~/nf/config.yaml
$ bash gen.sh file /data/f10g 10G
$ bash baseline.sh peer 203.0.113.7
$ for i in 1 2 3; do OUT=big.jsonl bash run.sh f10g peer /data/f10g; done
$ SELF_IP=198.51.100.4 OUT=wan.jsonl bash wanmatrix.sh peer peer /data/f1g
$ python3 summarize.py big.jsonl wan.jsonl
```

Limits: a run is one sender and one receiver; the wall times include the
sender's and receiver's own start, not the ssh hop that carries the address.
netem adds loss and a rate cap on top of a real path; it models a lossy
bottleneck, not a particular network.
