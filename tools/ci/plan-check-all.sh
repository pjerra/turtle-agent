#!/usr/bin/env bash
# plan-check-all runs `turtle-agent plan-check` on every shipped plan that is live or prepped for a live run.
#
# The rule: a plan is checked when its status starts with "live-checked" or "mapped" (mapped means it was written
# against the world export and the navmesh but has not had its live walk yet). A "draft" is left out: its route has
# not had its mapping walk, so the navmesh check would report the unmapped points as failures. A plan without a status
# is left out too.
#
# Usage:
#   tools/ci/plan-check-all.sh --mmaps DIR [--dir DUNGEONS] [--bin TURTLE-AGENT] [--json-out FILE] [--strict-lints]
#
# The gate's verdict (exit status):
#   0   every checked plan passes (WARN placement lints do not count),
#   11  a point fails, or an error-severity placement lint that is not a quality lint, or (with --strict-lints) any
#       error-severity placement lint,
#   2   a usage error, or plan-check itself failed with a status other than 0 or 11.
# Quality lints (water, off-floor, edge, ledge, short hop, aggro, patrol, joiners) are reported as WARN with a count per
# plan and do not fail the gate, by the lead's decision of 2026-11-03; --strict-lints makes them fail. Every other
# error-severity lint (reach, arrival, hop_len, climb_anchor) and every point issue still fails the gate.
# A plan that fails is printed as FAIL with its points or lint findings. The JSON-out file (when given) holds plan-check's
# own report for the checked plans.
set -euo pipefail

dir=dungeons
mmaps=${TURTLE_AGENT_MMAPS:-}
bin=${TURTLE_AGENT_BIN:-turtle-agent}
jsonout=
strictlints=0
usage() {
	echo "usage: $0 --mmaps DIR [--dir DUNGEONS] [--bin TURTLE-AGENT] [--json-out FILE] [--strict-lints]" >&2
	exit 2
}
while (($# > 0)); do
	case "$1" in
	--dir)
		(($# >= 2)) || usage
		dir=$2
		shift 2
		;;
	--mmaps)
		(($# >= 2)) || usage
		mmaps=$2
		shift 2
		;;
	--bin)
		(($# >= 2)) || usage
		bin=$2
		shift 2
		;;
	--json-out)
		(($# >= 2)) || usage
		jsonout=$2
		shift 2
		;;
	--strict-lints)
		strictlints=1
		shift
		;;
	-h | --help)
		usage
		;;
	*)
		echo "plan-check-all: unknown argument $1" >&2
		usage
		;;
	esac
done
[[ -d $dir ]] || {
	echo "plan-check-all: no plan folder $dir" >&2
	exit 2
}
[[ -n $mmaps ]] || {
	echo "plan-check-all: --mmaps (or TURTLE_AGENT_MMAPS) is required" >&2
	exit 2
}
if [[ $bin == */* ]]; then
	[[ -x $bin ]] || {
		echo "plan-check-all: $bin is not executable" >&2
		exit 2
	}
else
	bin=$(command -v "$bin" || true)
	[[ -n $bin ]] || {
		echo "plan-check-all: turtle-agent binary not found; pass --bin" >&2
		exit 2
	}
fi

plans=()
for f in "$dir"/*.json; do
	[[ -e $f ]] || continue
	name=$(basename "$f" .json)
	case "$name" in
	*.tune) continue ;; # a tunables file, not a plan
	esac
	status=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("status", ""))' "$f")
	case "$status" in
	live-checked* | mapped*) plans+=("$name") ;;
	esac
done
if ((${#plans[@]} == 0)); then
	echo "plan-check-all: no live or mapped plan in $dir"
	exit 0
fi

tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
echo "plan-check-all: checking ${#plans[@]} live or mapped plan(s): ${plans[*]}"
status=0
"$bin" plan-check --dir "$dir" --mmaps "$mmaps" --json "${plans[@]}" >"$tmp" || status=$?
if [[ -n $jsonout ]]; then
	cp "$tmp" "$jsonout"
fi
# plan-check exits 11 for any error-severity finding, including the quality lints the gate only warns on, so the
# verdict comes from the report. Any other failing status is plan-check's own failure and is passed through.
if ((status != 0 && status != 11)); then
	echo "plan-check-all: plan-check exited $status" >&2
	exit "$status"
fi
verdict=0
python3 - "$tmp" "$strictlints" <<'PY' || verdict=$?
import collections
import json
import sys

path = sys.argv[1]
strict = sys.argv[2] == "1"
# The lead's decision of 2026-11-03: these placement lints are quality findings. The gate reports them as WARN with a
# count per plan; --strict-lints makes them fail. Other error-severity lints are navmesh route defects and always fail.
QUALITY = {"water", "off-floor", "edge", "ledge", "short hop", "aggro", "patrol", "joiners"}
try:
    with open(path) as f:
        results = json.load(f)
except Exception as exc:  # plan-check itself failed before it wrote a report
    print(f"plan-check-all: no plan-check report: {exc}")
    sys.exit(1)

blocked = 0
warned = 0
for r in results:
    name = r.get("plan", "?")
    mapname = r.get("map", "?")
    issues = r.get("issues") or []
    errors = [f for f in (r.get("lints") or []) if f.get("severity") == "error"]
    soft = [f for f in errors if f.get("lint") in QUALITY and not strict]
    hard = [f for f in errors if not (f.get("lint") in QUALITY and not strict)]
    if issues or hard:
        blocked += 1
    if issues:
        print(f"FAIL {name}: {len(issues)} of {r.get('points', 0)} points fail (map {mapname})")
        for i in issues:
            detail = i.get("detail") or ""
            print(f"  leg {i.get('leg', '?')} {i.get('leg_name', '')}: {i.get('part', '')}: {i.get('reason', '')}: {detail}")
    if hard:
        print(f"FAIL {name}: {len(hard)} placement lint error(s) (map {mapname})")
        for f in hard:
            print(f"  step {f.get('step', '?')} leg {f.get('leg', '?')} {f.get('lint', '')}: {f.get('point', '')}: {f.get('detail', '')}")
    if soft:
        warned += 1
        counts = collections.Counter(f.get("lint", "?") for f in soft)
        kinds = ", ".join(f"{k} {counts[k]}" for k in sorted(counts))
        print(f"WARN {name}: {len(soft)} placement lint finding(s) do not fail the gate (map {mapname}): {kinds}")
    if not issues and not hard and not soft:
        print(f"PASS {name}: {r.get('points', 0)} points on map {mapname} ({len(r.get('warnings') or [])} warnings, {len(r.get('exempt') or [])} exempt)")
if blocked:
    print(f"plan-check-all: {blocked} of {len(results)} plan(s) failed")
    sys.exit(11)
if warned:
    print(f"plan-check-all: {warned} of {len(results)} plan(s) have WARN placement lints (not blocking; --strict-lints makes them fail)")
print(f"plan-check-all: all {len(results)} plan(s) passed")
PY
exit "$verdict"
