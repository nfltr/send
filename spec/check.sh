#!/bin/sh
# check.sh: TLC on Resume.tla (spec/README.md): the model with -coverage 1
# (exit 1 on an action no state took), each witness violated, each mutant
# caught. java and tla2tools.jar from $TLA_JAVA and $TLA_JAR, else
# github.com/nfltr/link's scripts/fetch-tla.sh output (the pinned versions).
set -eu
cd "$(dirname "$0")"
java=${TLA_JAVA:-java} jar=${TLA_JAR:?set TLA_JAR to tla2tools.jar (1.8.0)}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
tlc() { c=$1; shift; "$java" -XX:+UseParallelGC -cp "$jar" tlc2.TLC -workers 2 -cleanup -noGenerateSpecTE -metadir "$tmp/$c" "$@" -config "$c.cfg" Resume.tla 2>&1 | sed 's/^/  /' >"$tmp/$c.out" || true; }
fail=0
tlc Resume -coverage 1
# zero coverage outside CleanEnd (enabled only under mutant commit-cut)
zero=$(awk '/^  <[A-Za-z]+ line/ {a=$1} / of module Resume: 0$/ && a != "<CleanEnd" {print}' "$tmp/Resume.out")
if grep -q 'No error has been found' "$tmp/Resume.out" && [ -z "$zero" ]; then echo "ok   Resume: invariants and Done hold, every action taken"
else echo "FAIL Resume"; cat "$tmp/Resume.out"; fail=1; fi
for c in WitnessResumedCommit WitnessChangedThenCommit WitnessDiscarded; do
	tlc "$c"
	if grep -q "Invariant $c is violated" "$tmp/$c.out"; then echo "ok   $c violated (reachable)"; else echo "FAIL $c not violated"; fail=1; fi
done
for m in commit-cut no-checks no-final-check trust-partial; do
	tlc "mutant-$m"
	if grep -E -q 'Invariant Safe is violated|Temporal properties were violated|Temporal property Done was violated' "$tmp/mutant-$m.out"; then echo "ok   mutant $m caught"; else echo "FAIL mutant $m not caught"; fail=1; fi
done
exit $fail
