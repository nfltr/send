#!/usr/bin/env python3
# Copyright 2026 nfltr
# SPDX-License-Identifier: Apache-2.0
#
# summarize.py [--wan] <results.jsonl>...: a markdown table, one row per tag,
# medians over its runs. Default: the completed runs' timings, CPU, RSS and
# files/s. --wan: goodput (what arrived per second of the receiver's run,
# so a run stopped at its window counts too) and its share of the cap the
# tag names (<name>-<cap>-<loss>-<path>).
import json, re, statistics, sys

wan = sys.argv[1:2] == ["--wan"]
rows = {}
for p in sys.argv[1 + wan:]:
    for line in open(p):
        if line.startswith("{"):
            r = json.loads(line)
            rows.setdefault(r["tag"], []).append(r)

def med(rs, f):
    v = [x for x in map(f, rs) if x is not None]
    return statistics.median(v) if v else None

def fmt(x, d=1):
    return "-" if x is None else "%.*f" % (d, x)

def paths(rs):
    return ", ".join(sorted({"+".join(r["recv"]["json"].get("paths") or []) or "(none)" for r in rs}))

if wan:
    print("| case | runs | runs ended | goodput Mbit/s (median) | min-max | % of cap | paths |")
    print("|---|---|---|---|---|---|---|")
    for tag, rs in rows.items():
        g = [r.get("goodput_mbit") for r in rs if r.get("goodput_mbit") is not None]
        m = re.search(r"-(\d+)(mbit|gbit)-", tag)
        cap = int(m.group(1)) * (1000 if m.group(2) == "gbit" else 1) if m else None
        gm = statistics.median(g) if g else None
        print("| %s | %d | %s | %s | %s-%s | %s | %s |" % (tag, len(rs), "all complete" if all(r["ok"] for r in rs) else ("at the window" if not any(r["ok"] for r in rs) else "mixed"),
              fmt(gm), fmt(min(g) if g else None), fmt(max(g) if g else None),
              fmt(gm * 100 / cap if gm and cap else None, 0), paths(rs)))
    sys.exit(0)

print("| case | ok | to address s | address to received s | total s | MB/s | files/s | send CPU s | send RSS MiB | recv CPU s | recv RSS MiB | paths |")
print("|---|---|---|---|---|---|---|---|---|---|---|---|")
for tag, rs in rows.items():
    ok = [r for r in rs if r["ok"]]
    files = med(ok, lambda r: r["recv"]["json"].get("files", 0) / r["recv"]["wall_s"])
    print("| %s | %d/%d | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |" % (
        tag, len(ok), len(rs),
        fmt(med(ok, lambda r: r["start_to_address_s"]), 2),
        fmt(med(ok, lambda r: r["recv"]["wall_s"])),
        fmt(med(ok, lambda r: r["total_s"])),
        fmt(med(ok, lambda r: r["bytes"] / r["recv"]["wall_s"] / 1e6)),
        fmt(files, 0) if files and files > 10 else "-",
        fmt(med(ok, lambda r: r["send"].get("cpu_s"))),
        fmt(med(ok, lambda r: r["send"].get("rss_kib", 0) / 1024), 0),
        fmt(med(ok, lambda r: r["recv"].get("cpu_s"))),
        fmt(med(ok, lambda r: r["recv"].get("rss_kib", 0) / 1024), 0),
        paths(ok)))
