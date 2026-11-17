#!/bin/sh
# traces.sh: the xfer tests under -tags sendtrace write their receivers'
# steps (xfer/trace_on.go); each trace is checked against its model with
# TLC (TraceCommit.tla, TraceResume.tla: accepted when a behaviour takes
# every line), the actions no accepted trace takes are listed (test gaps),
# and every fixture in spec/traces/ named mutant-* must be rejected
# (spec/README.md "Traces"). java and tla2tools.jar from $TLA_JAVA and
# $TLA_JAR (make spec-traces fetches them).
set -eu
cd "$(dirname "$0")"
java=${TLA_JAVA:-java} jar=${TLA_JAR:?set TLA_JAR to tla2tools.jar (1.8.0)}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir "$tmp/t"
(cd .. && SENDTRACE=$tmp/t go test -tags sendtrace -count=1 ./xfer/) >"$tmp/test.out" 2>&1 || { cat "$tmp/test.out"; exit 1; }

# check FILE: TLC on FILE with the model its name says; FILE.out.
check() {
	case $1 in *-commit.ndjson) m=TraceCommit ;; *) m=TraceResume ;; esac
	d=$tmp/w/$(basename "$1")
	mkdir -p "$d"
	sed "1s|^|CONSTANT TraceFile = \"$1\"\n|" "$m.cfg" >"$d/t.cfg"
	"$java" -XX:+UseSerialGC -Xss4m -cp "$jar" tlc2.TLC -workers 1 -cleanup -noGenerateSpecTE -metadir "$d" -coverage 1 -config "$d/t.cfg" "$m.tla" >"$1.out" 2>&1 || true
	grep -q 'Invariant TraceNotDone is violated' "$1.out"
}

fail=0 n=0
for f in "$tmp"/t/*.ndjson; do
	n=$((n + 1))
	(check "$f" && touch "$f.ok") &
	[ $((n % ${TRACE_JOBS:-4})) -ne 0 ] || wait
done
wait
for f in "$tmp"/t/*.ndjson; do
	if [ -f "$f.ok" ]; then cat "$f.out" >>"$tmp/cov"; else
		echo "REJECTED $(basename "$f")"; grep -E '^Error|^/\\ l = ' "$f.out" | tail -3
		[ -z "${TRACE_KEEP:-}" ] || cp "$f" "$f.out" "$TRACE_KEEP/"
		fail=1
	fi
done
echo "$n traces checked"
# Spec actions no accepted trace takes: the behaviour TLC prints for an
# accepted trace (TraceNotDone violated) is the one that took every line. Resume's Change is unreachable in
# the code: a changed file is another offer (the stage is named by the
# manifest's digest), so it is not listed.
for a in TOpen TSend TFinish TCut TServed HRot HCrash TraceCommit:TOpen TBusy TTaken TWrite TVerify \
	TraceCommit:TCut TRefuse TBegin TBeginRefused TRename TRenameRefused TStop TraceCommit:TFinish TExpire HForeign HReplace; do
	case $a in TraceCommit:*) mod=TraceCommit a=${a#*:} ;; TBusy | TTaken | TWrite | TVerify | TRefuse | TBegin* | TRename* | TStop | TExpire | HForeign | HReplace) mod=TraceCommit ;; *) mod=TraceResume ;; esac

	grep -qE "^State [0-9]+: <$a line .* of module $mod>" "$tmp/cov" || echo "test gap: no trace takes $mod $a"
	[ -z "${TRACE_WHO:-}" ] || grep -lE "^State [0-9]+: <$a line .* of module $mod>" "$tmp"/t/*.out | sed "s|^|  $mod $a: |"
done
# A fixture is rejected when TLC finished without a behaviour taking every
# line (or one broke an invariant), never by an error of TLC's own.
for f in traces/mutant-*.ndjson; do
	(check "$PWD/$f" || touch "$tmp/$(basename "$f").no") &
done
wait
for f in traces/mutant-*.ndjson; do
	if [ -f "$tmp/$(basename "$f").no" ] && grep -E -q 'No error has been found|Invariant [A-Za-z]+ is violated' "$f.out"; then
		echo "ok   $f rejected ($(grep -E -o 'No error has been found|Invariant [A-Za-z]+ is violated' "$f.out" | head -1))"
	else echo "FAIL $f accepted or not checked"; cat "$f.out"; fail=1; fi
	rm -f "$f.out"
done
exit $fail
