#!/usr/bin/env bash
# plan-check-all runs `turtle-agent plan-check` on every shipped plan that is live or prepped for a live run.
#
# The rule: a plan is checked when its status starts with "live-checked" or "mapped" (mapped means it was written
# against the world export and the navmesh but has not had its live walk yet). A "draft" is left out: its route has
# not had its mapping walk, so the navmesh check would report the unmapped points as failures. A plan without a status
# is left out too.
#
#   tools/ci/plan-check-all.sh --mmaps DIR [--dir DUNGEONS] [--bin TURTLE-AGENT] [--json-out FILE] [--strict-lints]
#     [--baseline FILE] [--refresh-baseline]
#
# The gate's verdict (exit status):
#   0   every checked plan passes (WARN placement lints do not count),
#   11  a point fails (plan-check's navmesh FAIL), or (with --strict-lints) any error-severity placement lint,
#       or a NEW error-severity route lint (path_ratio, no_path, zjump: the route-baseline file lists the ones the
#       gate already knows, so only findings it does not list fail),
#   2   a usage error, or plan-check itself failed with a status other than 0 or 11.
# Every error-severity placement lint (water, off-floor, edge, ledge, short hop, aggro, boss_aggro, patrol, joiners, reach, arrival,
# hop_len, climb_anchor) is reported as WARN with a count per plan and does not fail the gate, by the lead's decisions of
# 2026-11-03 (the second one extends the first to the plan-scout lints); --strict-lints makes them all fail, and makes every
# route finding fail whether the baseline lists it or not. The route WARN lints (backtrack, long_hop, a path_ratio warning)
# never fail; they are reported with the counts.
# Only a point failure, the same failure the gate had before the placement lints existed, blocks by default.
# A plan that fails is printed as FAIL with its points or lint findings. The JSON-out file (when given) holds plan-check's
# own report for the checked plans.
# --refresh-baseline rewrites the baseline file from this run's error-severity route findings and passes: run it after a
# plan's route fix round, and commit the file with the plan.
set -euo pipefail

dir=dungeons
mmaps=${TURTLE_AGENT_MMAPS:-}
bin=${TURTLE_AGENT_BIN:-turtle-agent}
jsonout=
strictlints=0
baseline=tools/ci/route-baseline.txt
refresh=0
usage() {
	echo "usage: $0 --mmaps DIR [--dir DUNGEONS] [--bin TURTLE-AGENT] [--json-out FILE] [--strict-lints] [--baseline FILE] [--refresh-baseline]" >&2
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
	--baseline)
		(($# >= 2)) || usage
		baseline=$2
		shift 2
		;;
	--refresh-baseline)
		refresh=1
		shift
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
# plan-check exits 11 for any error-severity finding, including the placement lints the gate only warns on, so the
# verdict comes from the report. Any other failing status is plan-check's own failure and is passed through.
if ((status != 0 && status != 11)); then
	echo "plan-check-all: plan-check exited $status" >&2
	exit "$status"
fi
verdict=0
python3 - "$tmp" "$strictlints" "$baseline" "$refresh" <<'PY' || verdict=$?
import collections
import json
import sys

path = sys.argv[1]
strict = sys.argv[2] == "1"
basepath = sys.argv[3]
refresh = sys.argv[4] == "1"

# The route rule family: error-severity findings fail the gate unless the
# baseline lists them; warnings report with the counts and never fail.
ROUTE = {"path_ratio", "no_path", "zjump", "backtrack", "long_hop"}


def key(name, f):
    return f"{name} {f.get('step', '?')} {f.get('leg', '?')} {f.get('lint', '')}"


try:
    with open(path) as f:
        results = json.load(f)
except Exception as exc:  # plan-check itself failed before it wrote a report
    print(f"plan-check-all: no plan-check report: {exc}")
    sys.exit(1)

known = set()
if not refresh:
    try:
        with open(basepath) as f:
            for line in f:
                line = line.strip()
                if line and not line.startswith("#"):
                    known.add(line)
    except FileNotFoundError:
        print(f"plan-check-all: no baseline file {basepath} (pass --refresh-baseline to write it)") 
        sys.exit(2)

if refresh:
    fresh = sorted({key(r.get("plan", "?"), f) for r in results for f in (r.get("lints") or [])
                    if f.get("lint") in ROUTE and f.get("severity") == "error"})
    with open(basepath, "w") as f:
        f.write("# route-baseline.txt: error-severity route lint findings the gate already knows.\n")
        f.write("# One per line: <plan> <step> <leg> <lint>. Only findings NOT listed here fail the gate.\n")
        f.write("# Rewrites with: tools/ci/plan-check-all.sh --mmaps DIR --refresh-baseline (commit the file with the plan).\n")
        for k in fresh:
            f.write(k + "\n")
    print(f"plan-check-all: wrote {len(fresh)} baseline finding(s) to {basepath}")
    sys.exit(0)

blocked = 0
warned = 0
for r in results:
    name = r.get("plan", "?")
    mapname = r.get("map", "?")
    issues = r.get("issues") or []
    errors = [f for f in (r.get("lints") or []) if f.get("severity") == "error"]
    routes = [f for f in errors if f.get("lint") in ROUTE]
    place = [f for f in errors if f.get("lint") not in ROUTE]
    warnroutes = [f for f in (r.get("lints") or []) if f.get("lint") in ROUTE and f.get("severity") != "error"]
    # The lead's decisions of 2026-11-03: every error-severity placement lint warns; --strict-lints makes them all block.
    soft = [] if strict else place
    hard = place if strict else []
    # Route errors fail only when new (not in the baseline); --strict-lints fails them all, baseline or not.
    newroutes = [f for f in routes if strict or key(name, f) not in known]
    if issues or hard or newroutes:
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
    if newroutes:
        print(f"FAIL {name}: {len(newroutes)} new route lint error(s) (map {mapname}, not in {basepath})")
        for f in newroutes:
            print(f"  step {f.get('step', '?')} leg {f.get('leg', '?')} {f.get('lint', '')}: {f.get('point', '')}: {f.get('detail', '')}")
    if soft or warnroutes:
        warned += 1
        counts = collections.Counter(f.get("lint", "?") for f in soft + warnroutes)
        kinds = ", ".join(f"{k} {counts[k]}" for k in sorted(counts))
        print(f"WARN {name}: {len(soft + warnroutes)} placement/route lint finding(s) do not fail the gate (map {mapname}): {kinds}")
    if not issues and not hard and not soft and not newroutes and not warnroutes:
        print(f"PASS {name}: {r.get('points', 0)} points on map {mapname} ({len(r.get('warnings') or [])} warnings, {len(r.get('exempt') or [])} exempt)")
if blocked:
    print(f"plan-check-all: {blocked} of {len(results)} plan(s) failed")
    sys.exit(11)
if warned:
    print(f"plan-check-all: {warned} of {len(results)} plan(s) have WARN placement/route lints (not blocking; --strict-lints makes them fail)")
print(f"plan-check-all: all {len(results)} plan(s) passed")
PY
exit "$verdict"
