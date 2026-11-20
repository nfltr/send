#!/usr/bin/env python3
# Copyright 2026 nfltr
# SPDX-License-Identifier: Apache-2.0
"""rel.py matrix|soak: nfltr-send's reliability matrix and soak, as root in
one machine (run.sh starts it in a privileged container). A relay, a sender
and a receiver in their own network namespaces on one bridge; netem on the
bridge ports caps the rate, adds the round trip and drops packets in both
directions, for the direct and the relayed path alike. Every transfer is
judged by exit codes and by the received tree against the sent one (names,
types, SHA-256 of every file): a run passes only when every byte is there,
right, and nothing else is. README.md in this directory lists the cells.

Env: REL_BIN (nfltr-send, nfltr, nfltr-relay; default /bin-in), REL_OUT
(default /out), REL_N (runs per cell, default 5), REL_CELLS (regex: the
cells to run), REL_TIMEOUT (s per receive, default 600), REL_LARGE (the large
file, head -c syntax, default 4G), REL_SMALL (the small files' count, default
10000), REL_SOAK_S (soak seconds, default 86400), REL_SEED (soak, default the
start time), REL_UID (owner of REL_OUT's files at the end), REL_SEND_S and REL_SEND_R (the
sender's and the receiver's nfltr-send in REL_BIN, default nfltr-send: two
versions, cross.sh)."""
import json, os, random, re, signal, statistics, subprocess as sp, sys, time, hashlib

B = os.environ.get("REL_BIN", "/bin-in")
OUT = os.environ.get("REL_OUT", "/out")
N = int(os.environ.get("REL_N", "5"))
TIMEOUT = int(os.environ.get("REL_TIMEOUT", "600"))
SRC = os.path.dirname(os.path.abspath(__file__))
W = "/w"
RELAY = "10.77.0.1"
MiB = 1 << 20
STAGE = ".nfltr-send-"
SEND = {"s": os.environ.get("REL_SEND_S", "nfltr-send"), "r": os.environ.get("REL_SEND_R", "nfltr-send")}


def sh(cmd, check=True):
    return sp.run(cmd, shell=True, check=check, capture_output=True, text=True).stdout.strip()


# ---- the network ----

def net_up():
    sh(f"ip link add br0 type bridge && ip addr add {RELAY}/24 dev br0 && ip link set br0 up")
    for i, n in ((2, "s"), (3, "r")):
        sh(f"ip netns add {n} && ip link add v{n} type veth peer name e0 netns {n} && ip link set v{n} master br0 up"
           f" && ip -n {n} addr add 10.77.0.{i}/24 dev e0 && ip -n {n} link set e0 up && ip -n {n} link set lo up")
        # netem must see wire-sized packets, not offloaded super-packets.
        sh(f"ip netns exec {n} ethtool -K e0 tso off gso off gro off tx-udp-segmentation off", check=False)
        sh(f"ethtool -K v{n} tso off gso off gro off tx-udp-segmentation off", check=False)


def impair(rate=0, rtt=0, loss=0):
    """netem on both bridge ports (what reaches the sender and the receiver):
    half the round trip each way, the rate cap and the loss on each."""
    for d in ("vs", "vr"):
        if not (rate or rtt or loss):
            sh(f"tc qdisc del dev {d} root", check=False)
            continue
        lim = 100000 if not rate else max(1000, int(rate * 1e6 * (rtt / 2000 + 0.2) / 8 / 1500))
        sh(f"tc qdisc replace dev {d} root netem delay {rtt * 500}us"
           + (f" rate {rate}mbit" if rate else "") + f" loss {loss}% limit {lim}")


def block_udp(on):
    op = "-A" if on else "-D"
    sh(f"ip netns exec r iptables {op} INPUT -p udp -s 10.77.0.2 -j DROP", check=on)
    sh(f"ip netns exec r iptables {op} OUTPUT -p udp -d 10.77.0.2 -j DROP", check=on)


# ---- the relay and the sender's certificate ----

PIN = ""
ENV = {}


def relay_up():
    global PIN
    d = f"{W}/relay"
    os.makedirs(d, mode=0o700, exist_ok=True)
    env = dict(os.environ, HOME=d, NF1_ENABLED="true", NF1_HOSTS=f"{RELAY}:8444", NF1_ANON_ENABLED="true",
               RATELIMIT_ENABLE="false", SECURITY_CONN_LIMIT_PER_IP_PACKET_RATE="0")
    # One sender and one receiver address open every session: lift the per-address and per-source caps.
    for k in ("NF1_ADDRESS", "NF1_SOURCE", "NF1_ANON_SOURCE"):
        env.update({k + "_MAX": "1000", k + "_BURST": "100000", k + "_RATE": "100000"})
    env.update(NF1_OPEN_BURST="100000", NF1_OPEN_RATE="100000")
    log = open(f"{OUT}/relay.log", "ab")
    p = sp.Popen([f"{B}/nfltr-relay", "serve", "--data-dir", d, "--tls-self-signed", "--tls-name", RELAY,
                  "--listen", RELAY, "--agent-rate-limit", "0"], env=env, stdout=log, stderr=log, start_new_session=True)
    for _ in range(300):
        if sp.run(f"curl -skf -o /dev/null https://{RELAY}:8444/healthz", shell=True).returncode == 0:
            break
        if p.poll() is not None:
            sys.exit(f"relay exited {p.returncode}: {OUT}/relay.log")
        time.sleep(0.1)
    PIN = sh(f"openssl s_client -connect {RELAY}:8444 </dev/null 2>/dev/null | openssl x509 -outform der | sha256sum | cut -d' ' -f1")
    for who in ("s", "anon"):
        h = f"{W}/{who}"
        os.makedirs(h, exist_ok=True)
        ENV[who] = dict(os.environ, HOME=h, NFLTR_CONFIG=f"{h}/config.yaml", NFLTR_RELAY_CERT_SHA256=PIN)
    with open(f"{d}/admin-enrol-code") as code:
        sp.run(["ip", "netns", "exec", "s", f"{B}/nfltr", "config", "set-relay", f"{RELAY}:8443", "--proxy-url",
                f"https://{RELAY}:8444", "--cert-sha256", PIN, "--code-file", "-"], stdin=code, env=ENV["s"], check=True,
               capture_output=True)
    return p


# ---- data ----

def dataset(kind, size=0):
    """The source path for a cell, generated once (bench/scale/gen.sh)."""
    os.makedirs(f"{W}/data", exist_ok=True)
    name = f"f{size // MiB}m" if kind == "file" else kind
    if kind == "large":
        name = "large"
    p = f"{W}/data/{name}"
    if not os.path.exists(p):
        gen = f"{SRC}/gen.sh"  # bench/scale/gen.sh, copied beside rel.py by run.sh
        if kind == "file":
            sh(f"sh {gen} file {p} {size}")
        elif kind == "large":
            sh(f"sh {gen} file {p} {os.environ.get('REL_LARGE', '4G')}")
        else:
            sh(f"sh {gen} small {p} {os.environ.get('REL_SMALL', '10000')}")
    return p


def tree(root, top=None):
    """{relative path: 'd' | sha256 hex} of everything under root (top: only that entry)."""
    out = {}
    names = [top] if top else os.listdir(root)
    for t in names:
        p = os.path.join(root, t)
        if not os.path.isdir(p) or os.path.islink(p):
            out[t] = digest(p)
            continue
        out[t] = "d"
        for d, dirs, files in os.walk(p):
            for x in dirs:
                out[os.path.relpath(os.path.join(d, x), root)] = "d"
            for x in files:
                out[os.path.relpath(os.path.join(d, x), root)] = digest(os.path.join(d, x))
    return out


def digest(p):
    if os.path.islink(p) or not os.path.isfile(p):
        return "other"
    h = hashlib.sha256()
    with open(p, "rb") as f:
        while b := f.read(4 * MiB):
            h.update(b)
    return h.hexdigest()


EXPECT = {}


def expected(src):
    if src not in EXPECT:
        EXPECT[src] = tree(os.path.dirname(src), os.path.basename(src))
    return EXPECT[src]


def judge(to, src, committed):
    """(None, []) when the visible tree is right: the whole offer when
    committed, else nothing (a refused or failed receive commits nothing;
    its hidden stage is allowed). Else the reason and the differing paths:
    corrupt (a visible file differs from the one offered, or was never
    offered), missing, stage_left (a stage after a commit), or
    committed_on_failure (right files visible after a failed receive)."""
    got = {k: v for k, v in tree(to).items() if not k.split("/")[0].startswith(STAGE)}
    want = expected(src)
    bad = sorted(k for k in got if got[k] != want.get(k))
    if bad:
        return "corrupt", bad[:10]
    if not committed:
        return ("committed_on_failure", sorted(got)[:10]) if got else (None, [])
    if set(want) - set(got):
        return "missing", sorted(set(want) - set(got))[:10]
    left = sorted(x for x in os.listdir(to) if x.startswith(STAGE))
    return ("stage_left", left) if left else (None, [])


def keep_tree(to, logdir):
    """What a failed run left in the receiving directory: every path with
    its SHA-256 (or d), stages included."""
    with open(f"{logdir}/received.json", "w") as f:
        json.dump(tree(to), f, indent=0)


def staged(to):
    n = 0
    for t in os.listdir(to):
        if t.startswith(STAGE):
            for d, _, files in os.walk(os.path.join(to, t)):
                for f in files:
                    try:
                        n += os.stat(os.path.join(d, f)).st_blocks * 512
                    except FileNotFoundError:
                        pass
    return n


# ---- one run ----

class Run:
    def __init__(self, cell, logdir):
        self.c, self.logdir = cell, logdir
        os.makedirs(logdir, exist_ok=True)
        self.n = 0

    def sender(self, src):
        self.n += 1
        self.addr = f"{self.logdir}/addr{self.n}"
        a = open(self.addr, "w")
        os.chmod(self.addr, 0o600)
        log = open(f"{self.logdir}/send{self.n}.log", "wb")
        p = sp.Popen(["ip", "netns", "exec", "s", f"{B}/{SEND['s']}", "send", "--once", "--relay", f"{RELAY}:8443",
                      "--relay-http", f"{RELAY}:8444", src],
                     stdout=a, stderr=log, env=ENV["anon" if self.c.get("anon") else "s"], start_new_session=True)
        for _ in range(600):
            with open(self.addr) as f:
                if f.read().startswith("nf1"):
                    return p, None
            if p.poll() is not None:
                return p, f"sender_exit_{p.returncode}"
            time.sleep(0.05)
        return p, "sender_no_address"

    def receiver(self, to):
        self.n += 1
        self.rlog = f"{self.logdir}/recv{self.n}.log"
        args = ["ip", "netns", "exec", "r", f"{B}/{SEND['r']}", "receive", "--json", "--to", to]
        if self.c["path"] == "relayed":
            args += ["--direct", "off"]
        return sp.Popen(args, stdin=open(self.addr), env=dict(os.environ, NFLTR_RELAY_CERT_SHA256=PIN), stdout=sp.DEVNULL, stderr=open(self.rlog, "wb"), start_new_session=True)

    def rjson(self):
        js = {}
        with open(self.rlog, errors="replace") as f:
            for line in f:
                if line.startswith("{"):
                    try:
                        js = json.loads(line)
                    except ValueError:
                        pass
        return js


def wait(p, t):
    try:
        return p.wait(timeout=t)
    except sp.TimeoutExpired:
        os.killpg(p.pid, signal.SIGKILL)
        p.wait()
        return None


def kill(p):
    if p.poll() is None:
        os.killpg(p.pid, signal.SIGKILL)
    return p.wait()


def run_cell(c, logdir):
    """One transfer of cell c, with its fault if any. Returns the result row."""
    r = Run(c, logdir)
    impair(c.get("rate", 0), c.get("rtt", 0), c.get("loss", 0))
    src = dataset(c["data"], c.get("size", 0))
    size = sum(os.path.getsize(os.path.join(d, f)) for d, _, fs in os.walk(src) for f in fs) if os.path.isdir(src) else os.path.getsize(src)
    to = f"{W}/to"
    sh(f"umount {to} 2>/dev/null; rm -rf {to} && mkdir -p {to}")
    f = c.get("fault") or {}
    if f.get("kind") == "diskfull":
        sh(f"mount -t tmpfs -o size={int(size * 0.75)} tmpfs {to}")
    if c.get("noudp"):
        block_udp(True)
    row = {"cell": c["name"], "bytes": size, "fault": f or None}
    procs = []
    try:
        s, why = r.sender(src)
        procs.append(s)
        if why:
            return dict(row, ok=False, reason=why)
        t0 = time.time()
        rp = r.receiver(to)
        procs.append(rp)
        if f.get("kind") in ("cut", "flap"):
            at = f["at"] / 100 * size
            while rp.poll() is None and staged(to) < at and time.time() - t0 < TIMEOUT:
                time.sleep(0.02)
            row["fault_applied"] = rp.poll() is None
            if row["fault_applied"]:
                row["staged_at_fault"] = staged(to)
                if f["kind"] == "flap":
                    impair(c.get("rate", 0), c.get("rtt", 0), 100)
                    time.sleep(f["s"])
                    impair(c.get("rate", 0), c.get("rtt", 0), c.get("loss", 0))
                elif f["victim"] == "receiver":
                    if kill(rp) != 0:  # 0: it finished before the cut
                        rp = r.receiver(to)
                        procs.append(rp)
                else:
                    kill(s)
                    t1 = time.time()
                    row["first_exit"] = wait(rp, 300)
                    row["first_exit_s"] = round(time.time() - t1, 1)
                    if row["first_exit"] is None:
                        return dict(row, ok=False, reason="receiver_hang_after_sender_kill")
                    if row["first_exit"] != 0:
                        bad, diff = judge(to, src, False)
                        if bad:
                            keep_tree(to, logdir)
                            return dict(row, ok=False, reason="first_" + bad, diff=diff, corrupt=bad == "corrupt")
                    if row["first_exit"] == 0:  # it had every byte before the cut
                        s = None
                    else:
                        s, why = r.sender(src)
                        procs.append(s)
                        if why:
                            return dict(row, ok=False, reason=why)
                        rp = r.receiver(to)
                        procs.append(rp)
        if f.get("kind") == "diskfull":
            row["first_exit"] = wait(rp, TIMEOUT)
            bad, diff = judge(to, src, False)
            if row["first_exit"] in (0, None) or bad:
                keep_tree(to, logdir)
                return dict(row, ok=False, reason="diskfull_" + (bad or f"exit_{row['first_exit']}"), diff=diff,
                            corrupt=bad == "corrupt")
            sh(f"mount -o remount,size={size * 3} {to}")
            rp = r.receiver(to)
            procs.append(rp)
        rc = wait(rp, TIMEOUT)
        secs = time.time() - t0
        js = r.rjson()
        row.update(rc=rc, seconds=round(secs, 3), paths=js.get("paths"), sessions=js.get("sessions"),
                   written=js.get("written"), first_byte_seconds=js.get("first_byte_seconds"), code=js.get("code"))
        want = c.get("expect", 0)
        bad, diff = judge(to, src, want == 0 and rc == 0)
        if bad:  # corrupt or missing: a byte wrong, missing or extra in what the receiver committed
            keep_tree(to, logdir)
            return dict(row, ok=False, reason=bad, diff=diff, corrupt=bad == "corrupt" or bad == "missing")
        if rc is None:
            return dict(row, ok=False, reason="timeout")
        if rc != want:
            return dict(row, ok=False, reason=f"exit_{rc}")
        if want == 0:
            row["mbit"] = round(size * 8 / secs / 1e6, 1)
            sr = 0 if s is None else wait(s, 10)
            if sr != 0:
                return dict(row, ok=False, reason="sender_hang" if sr is None else f"sender_exit_{sr}")
        return dict(row, ok=True, reason=None)
    finally:
        for p in procs:
            kill(p)
        if c.get("noudp"):
            block_udp(False)
        sh(f"umount {to}", check=False)


# ---- cells ----

def cells():
    out = []
    for rate in (10, 100, 1000):
        for rtt in (1, 40, 150):
            for loss in (0, 1):
                for path in ("direct", "relayed"):
                    size = 4 * MiB if loss else {10: 4, 100: 16, 1000: 64}[rate] * MiB
                    out.append(dict(name=f"net-{rate}mbit-{rtt}ms-loss{loss}-{path}", data="file", size=size,
                                    rate=rate, rtt=rtt, loss=loss, path=path))
    for victim in ("receiver", "sender"):
        for at in (25, 50, 75):
            for path in ("direct", "relayed"):
                out.append(dict(name=f"cut-{victim}-{at}pct-{path}", data="file", size=64 * MiB, rate=100, rtt=40,
                                path=path, fault=dict(kind="cut", victim=victim, at=at)))
    for path in ("direct", "relayed"):
        out.append(dict(name=f"small-files-{path}", data="small", path=path))
        out.append(dict(name=f"large-file-{path}", data="large", path=path))
    out.append(dict(name="anon-direct", data="file", size=16 * MiB, path="direct", anon=True))
    out.append(dict(name="anon-no-udp", data="file", size=16 * MiB, path="direct", anon=True, noudp=True, expect=12))
    out.append(dict(name="disk-full", data="file", size=32 * MiB, path="direct", fault=dict(kind="diskfull")))
    return out


def soak_cell(rnd, pool):
    c = dict(rnd.choice(pool))
    if c["name"].startswith("net-") and rnd.random() < 0.5:
        k = rnd.choice(("receiver", "sender", "flap"))
        at = rnd.randint(10, 90)
        c["fault"] = dict(kind="flap", at=at, s=rnd.randint(2, 15)) if k == "flap" else dict(kind="cut", victim=k, at=at)
    return c


# ---- aggregation ----

def med(xs):
    xs = [x for x in xs if x is not None]
    return round(statistics.median(xs), 2) if xs else None


def aggregate(rows):
    by = {}
    for x in rows:
        by.setdefault(x["cell"], []).append(x)
    agg = []
    for name, rs in by.items():
        ok = [x for x in rs if x["ok"]]
        # The receive was right (every byte, exit as expected) though the
        # sender misbehaved after it: the medians count these runs too.
        got = [x for x in rs if x["ok"] or (str(x["reason"]).startswith("sender_") and x.get("rc") is not None)]
        reasons = {}
        for x in rs:
            if not x["ok"]:
                reasons[x["reason"]] = reasons.get(x["reason"], 0) + 1
        paths = {}
        for x in got:
            k = "+".join(x.get("paths") or [])
            paths[k] = paths.get(k, 0) + 1
        agg.append({"cell": name, "runs": len(rs), "pass": len(ok), "pass_rate": round(len(ok) / len(rs), 3),
                    "received": len(got), "median_s": med([x.get("seconds") for x in got]),
                    "median_mbit": med([x.get("mbit") for x in got]),
                    "min_mbit": min([x["mbit"] for x in got if x.get("mbit")], default=None),
                    "max_mbit": max([x["mbit"] for x in got if x.get("mbit")], default=None),
                    "median_written_frac": med([x["written"] / x["bytes"] for x in got if x.get("written") is not None and x.get("fault")]),
                    "paths": paths, "failures": reasons})
    return agg


def machine():
    return {"kernel": sh("uname -r"), "cpus": os.cpu_count(), "load": open("/proc/loadavg").read().split()[:3],
            "rmem_max": open("/proc/sys/net/core/rmem_max").read().strip(),
            **{f"nfltr-send_{n}_sha256": sh(f"sha256sum {B}/{b} | cut -d' ' -f1") for n, b in SEND.items()},
            "nfltr": sh(f"{B}/nfltr version | head -1"), "nfltr-relay": sh(f"{B}/nfltr-relay version 2>&1 | head -1")}


def emit(path, row):
    with open(path, "a") as f:
        f.write(json.dumps(row) + "\n")


def table(agg):
    lines = ["| cell | pass | received | median s | median Mbit/s (min-max) | paths | failures |", "|---|---|---|---|---|---|---|"]
    for a in agg:
        mb = "-" if a["median_mbit"] is None else f"{a['median_mbit']} ({a['min_mbit']}-{a['max_mbit']})"
        lines.append(f"| {a['cell']} | {a['pass']}/{a['runs']} | {a['received']}/{a['runs']} | {a['median_s']} | {mb} | "
                     + ", ".join(f"{k or '-'} {v}" for k, v in a["paths"].items()) + " | "
                     + (", ".join(f"{k} {v}" for k, v in a["failures"].items()) or "-") + " |")
    return "\n".join(lines) + "\n"


def main():
    mode = sys.argv[1] if len(sys.argv) > 1 else "matrix"
    if mode == "summarize":  # rel.py summarize <runs.jsonl> <out dir>: cells.jsonl and the table again
        agg = aggregate([json.loads(x) for x in open(sys.argv[2])])
        with open(f"{sys.argv[3]}/cells.jsonl", "w") as f:
            f.writelines(json.dumps(a) + "\n" for a in agg)
        with open(f"{sys.argv[3]}/summary.md", "w") as f:
            f.write(table(agg))
        return
    os.makedirs(f"{OUT}/logs", exist_ok=True)
    net_up()
    relay = relay_up()
    started = time.time()
    meta = dict(mode=mode, started=time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), n=N, machine=machine())
    rows = []
    sel = re.compile(os.environ.get("REL_CELLS", "."))
    try:
        if mode == "matrix":
            for c in cells():
                if not sel.search(c["name"]):
                    continue
                for i in range(N):
                    if relay.poll() is not None:
                        sys.exit(f"relay exited {relay.returncode}")
                    ld = f"{OUT}/logs/{c['name']}/{i}"
                    row = dict(run_cell(c, ld), rep=i, at=time.strftime("%H:%M:%S"), load=open("/proc/loadavg").read().split()[0])
                    rows.append(row)
                    emit(f"{OUT}/runs.jsonl", row)
                    print(json.dumps(row), flush=True)
                    if row["ok"]:
                        sh(f"rm -rf {ld}")
            agg = aggregate(rows)
            for a in agg:
                emit(f"{OUT}/cells.jsonl", a)
        else:
            seed = int(os.environ.get("REL_SEED", str(int(started))))
            rnd = random.Random(seed)
            pool = [c for c in cells() if sel.search(c["name"]) and c["data"] != "large"]
            meta["seed"] = seed
            end = started + int(os.environ.get("REL_SOAK_S", "86400"))
            i = 0
            while time.time() < end:
                if relay.poll() is not None:
                    sys.exit(f"relay exited {relay.returncode}")
                c = soak_cell(rnd, pool)
                ld = f"{OUT}/logs/soak/{i}"
                row = dict(run_cell(c, ld), rep=i, at=time.strftime("%H:%M:%S"), load=open("/proc/loadavg").read().split()[0])
                rows.append(row)
                emit(f"{OUT}/soak.jsonl", row)
                print(json.dumps(row), flush=True)
                if row["ok"]:
                    sh(f"rm -rf {ld}")
                i += 1
            agg = aggregate(rows)
    finally:
        kill(relay)
        impair()
    fails = {}
    for x in rows:
        if not x["ok"]:
            fails[x["reason"]] = fails.get(x["reason"], 0) + 1
    summary = dict(meta, seconds=round(time.time() - started), transfers=len(rows), passed=sum(x["ok"] for x in rows),
                   corrupted=sum(1 for x in rows if x.get("corrupt")), failures=fails,
                   faults_applied=sum(1 for x in rows if x.get("fault_applied")),
                   pass_rate=round(sum(x["ok"] for x in rows) / max(1, len(rows)), 4),
                   cells_failed=[a["cell"] for a in agg if a["pass"] < a["runs"]])
    summary["ok"] = summary["passed"] == summary["transfers"] > 0
    with open(f"{OUT}/{mode}-summary.json", "w") as f:
        json.dump(summary, f, indent=1)
    with open(f"{OUT}/{mode}-summary.md", "w") as f:
        f.write(table(agg))
    print(json.dumps({k: summary[k] for k in ("ok", "transfers", "passed", "corrupted", "failures", "cells_failed")}))
    if os.environ.get("REL_UID"):
        sh(f"chown -R {os.environ['REL_UID']} {OUT}", check=False)
    sys.exit(0 if summary["ok"] else 1)


if __name__ == "__main__":
    main()
