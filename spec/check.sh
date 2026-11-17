#!/bin/sh
# check.sh: TLC on Resume.tla and Commit.tla (spec/README.md): the model with -coverage 1
# (exit 1 on an action no state took), each witness violated, each mutant
# caught. java and tla2tools.jar from $TLA_JAVA and $TLA_JAR, else
# github.com/nfltr/link's scripts/fetch-tla.sh output (the pinned versions).
set -eu
cd "$(dirname "$0")"
java=${TLA_JAVA:-java} jar=${TLA_JAR:?set TLA_JAR to tla2tools.jar (1.8.0)}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
tlc() { mod=$1 cfg=$2; shift 2; "$java" -XX:+UseParallelGC -cp "$jar" tlc2.TLC -workers 2 -cleanup -noGenerateSpecTE -metadir "$tmp/$cfg" "$@" -config "$cfg.cfg" "$mod.tla" 2>&1 | sed 's/^/  /' >"$tmp/$cfg.out" || true; }
fail=0
tlc Resume Resume -coverage 1
# zero coverage outside CleanEnd (enabled only under mutant commit-cut)
zero=$(awk '/^  <[A-Za-z]+ line/ {a=$1} / of module Resume: 0$/ && a != "<CleanEnd" {print}' "$tmp/Resume.out")
if grep -q 'No error has been found' "$tmp/Resume.out" && [ -z "$zero" ]; then echo "ok   Resume: invariants and Done hold, every action taken"
else echo "FAIL Resume"; cat "$tmp/Resume.out"; fail=1; fi
for c in WitnessResumedCommit WitnessChangedThenCommit WitnessDiscarded; do
	tlc Resume "$c"
	if grep -q "Invariant $c is violated" "$tmp/$c.out"; then echo "ok   $c violated (reachable)"; else echo "FAIL $c not violated"; fail=1; fi
done
for m in commit-cut no-checks no-final-check trust-partial; do
	tlc Resume "mutant-$m"
	if grep -E -q 'Invariant Safe is violated|Temporal properties were violated|Temporal property Done was violated' "$tmp/mutant-$m.out"; then echo "ok   mutant $m caught"; else echo "FAIL mutant $m not caught"; fail=1; fi
done
tlc Commit Commit -coverage 1
zero=$(grep ' of module Commit: 0$' "$tmp/Commit.out" || true)
if grep -q 'No error has been found' "$tmp/Commit.out" && [ -z "$zero" ]; then echo "ok   Commit: invariants and Done hold, every action taken"
else echo "FAIL Commit"; cat "$tmp/Commit.out"; fail=1; fi
for c in WitnessPartWay WitnessRecoveredAfterCrash WitnessOtherFinished WitnessBusy WitnessForeignRefused WitnessRefusedMidCommit WitnessReplacedRefused WitnessExpired; do
	tlc Commit "Commit$c"
	if grep -q "Invariant $c is violated" "$tmp/Commit$c.out"; then echo "ok   Commit $c violated (reachable)"; else echo "FAIL Commit $c not violated"; fail=1; fi
done
for m in drop-stage no-intent trust-foreign replace sweep-deletes-intent no-lock trust-moved; do
	tlc Commit "Commit-mutant-$m"
	if v=$(grep -E -o '(Invariant|Action property) [A-Za-z]+ (of the PROPERTIES )?is violated|Temporal propert(y [A-Za-z]+ was|ies were) violated' "$tmp/Commit-mutant-$m.out"); then echo "ok   Commit mutant $m caught ($v)"; else echo "FAIL Commit mutant $m not caught"; fail=1; fi
done
exit $fail
