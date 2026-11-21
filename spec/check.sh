#!/bin/sh
# check.sh: TLC on Resume.tla, End.tla, Commit.tla, Offer.tla and Stripe.tla (spec/README.md): the model with -coverage 1
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
# zero coverage outside CleanEnd (enabled only under mutant commit-cut) and
# the action properties' subscripts
zero=$(awk '/^  <[A-Za-z]+ line/ {a=$1} / of module Resume: 0$/ && a != "<CleanEnd" && a != "<Action" {print}' "$tmp/Resume.out")
if grep -q 'No error has been found' "$tmp/Resume.out" && [ -z "$zero" ]; then echo "ok   Resume: invariants and Done hold, every action taken"
else echo "FAIL Resume"; cat "$tmp/Resume.out"; fail=1; fi
for c in WitnessResumedCommit WitnessChangedThenCommit WitnessDiscarded WitnessRefusedThenCommit WitnessGap; do
	tlc Resume "$c"
	if grep -q "Invariant $c is violated" "$tmp/$c.out"; then echo "ok   $c violated (reachable)"; else echo "FAIL $c not violated"; fail=1; fi
done
for m in accept-unverified resume-unverified skip-missing resend-verified root-unbound commit-cut no-final-check; do
	tlc Resume "mutant-$m"
	if v=$(grep -E -o '(Invariant|Action property) [A-Za-z]+ (of the PROPERTIES )?is violated' "$tmp/mutant-$m.out"); then echo "ok   mutant $m caught ($v)"; else echo "FAIL mutant $m not caught"; fail=1; fi
done
tlc End End -coverage 1
# zero coverage only in Close's mutant branch (close-cuts) and Over's subscript
zero=$(awk '/^  <[A-Za-z]+ line/ {a=$1} / of module End: 0$/ && a != "<Close" {print}' "$tmp/End.out")
if grep -q 'No error has been found' "$tmp/End.out" && [ -z "$zero" ]; then echo "ok   End: invariants and Done hold, every action taken"
else echo "FAIL End"; cat "$tmp/End.out"; fail=1; fi
for c in WitnessCommittedCut WitnessTold WitnessRetold; do
	tlc End "End$c"
	if grep -q "Invariant $c is violated" "$tmp/End$c.out"; then echo "ok   End $c violated (reachable)"; else echo "FAIL End $c not violated"; fail=1; fi
done
for m in close-cuts exit-on-answer no-retell close-confirms sender-gives-up; do
	tlc End "End-mutant-$m"
	if v=$(grep -E -o 'Invariant [A-Za-z]+ is violated' "$tmp/End-mutant-$m.out"); then echo "ok   End mutant $m caught ($v)"; else echo "FAIL End mutant $m not caught"; fail=1; fi
done
tlc Commit Commit -coverage 1
zero=$(grep ' of module Commit: 0$' "$tmp/Commit.out" || true)
if grep -q 'No error has been found' "$tmp/Commit.out" && [ -z "$zero" ]; then echo "ok   Commit: invariants and Done hold, every action taken"
else echo "FAIL Commit"; cat "$tmp/Commit.out"; fail=1; fi
for c in WitnessPartWay WitnessRecoveredAfterCrash WitnessOtherFinished WitnessBusy WitnessForeignRefused WitnessRefusedMidCommit WitnessReplacedRefused WitnessExpired; do
	tlc Commit "Commit$c"
	if grep -q "Invariant $c is violated" "$tmp/Commit$c.out"; then echo "ok   Commit $c violated (reachable)"; else echo "FAIL Commit $c not violated"; fail=1; fi
done
for m in drop-stage no-intent trust-foreign replace sweep-deletes-intent no-lock trust-moved sweep-trust-moved; do
	tlc Commit "Commit-mutant-$m"
	if v=$(grep -E -o '(Invariant|Action property) [A-Za-z]+ (of the PROPERTIES )?is violated|Temporal propert(y [A-Za-z]+ was|ies were) violated' "$tmp/Commit-mutant-$m.out"); then echo "ok   Commit mutant $m caught ($v)"; else echo "FAIL Commit mutant $m not caught"; fail=1; fi
done
tlc Offer Offer -coverage 1
zero=$(grep ' of module Offer: 0$' "$tmp/Offer.out" || true)
if grep -q 'No error has been found' "$tmp/Offer.out" && [ -z "$zero" ]; then echo "ok   Offer: invariants and Ended hold, every action taken"
else echo "FAIL Offer"; cat "$tmp/Offer.out"; fail=1; fi
for c in WitnessEarlierChanged WitnessEarlierTouched WitnessCutThenCommit WitnessChangedAfterDone; do
	tlc Offer "Offer$c"
	if grep -q "Invariant $c is violated" "$tmp/Offer$c.out"; then echo "ok   Offer $c violated (reachable)"; else echo "FAIL Offer $c not violated"; fail=1; fi
done
for m in no-sender-recheck no-receiver-recheck; do
	tlc Offer "Offer-mutant-$m"
	if v=$(grep -E -o 'Invariant [A-Za-z]+ is violated' "$tmp/Offer-mutant-$m.out"); then echo "ok   Offer mutant $m caught ($v)"; else echo "FAIL Offer mutant $m not caught"; fail=1; fi
done
tlc Stripe Stripe -coverage 1
# zero coverage only in Close's mutant branch (steal-live)
zero=$(awk '/^  <[A-Za-z]+ line/ {a=$1} / of module Stripe: 0$/ && a != "<Close" {print}' "$tmp/Stripe.out")
if grep -q 'No error has been found' "$tmp/Stripe.out" && [ -z "$zero" ]; then echo "ok   Stripe: invariants and Done hold, every action taken"
else echo "FAIL Stripe"; cat "$tmp/Stripe.out"; fail=1; fi
for c in WitnessParallel WitnessDiedThenCommit WitnessClosedThenCommit WitnessRefusedThenCommit WitnessResumedCommit WitnessTrustedCommit WitnessRotThenCommit WitnessTouchedThenCommit; do
	tlc Stripe "Stripe$c"
	if grep -q "Invariant $c is violated" "$tmp/Stripe$c.out"; then echo "ok   Stripe $c violated (reachable)"; else echo "FAIL Stripe $c not violated"; fail=1; fi
done
for m in lost-on-death steal-live resume-trust-log no-window accept-unverified commit-early trust-resumed no-recheck fresh-across-runs; do
	tlc Stripe "Stripe-mutant-$m"
	if v=$(grep -E -o 'Invariant [A-Za-z]+ is violated' "$tmp/Stripe-mutant-$m.out"); then echo "ok   Stripe mutant $m caught ($v)"; else echo "FAIL Stripe mutant $m not caught"; fail=1; fi
done
exit $fail
