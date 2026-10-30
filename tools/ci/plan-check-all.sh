#!/usr/bin/env bash
# plan-check-all runs `turtle-agent plan-check` on every shipped plan that is live or prepped for a live run.
#
# The rule: a plan is checked when its status starts with "live-checked" or "mapped" (mapped means it was written
# against the world export and the navmesh but has not had its live walk yet). A "draft" is left out: its route has
# not had its mapping walk, so the navmesh check would report the unmapped points as failures. A plan without a status
# is left out too.
#
# Usage:
#   tools/ci/plan-check-all.sh --mmaps DIR [--dir DUNGEONS] [--bin TURTLE-AGENT] [--json-out FILE]
#
# The exit status is plan-check's: 0 when every checked plan passes, 11 when a point fails, 2 on a usage error.
# The JSON-out file (when given) holds plan-check's own report for the checked plans.
set -euo pipefail

dir=dungeons
mmaps=${TURTLE_AGENT_MMAPS:-}
bin=${TURTLE_AGENT_BIN:-turtle-agent}
jsonout=
usage() {
	echo "usage: $0 --mmaps DIR [--dir DUNGEONS] [--bin TURTLE-AGENT] [--json-out FILE]" >&2
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
python3 - "$tmp" <<'PY' || status=1
import json
import sys

path = sys.argv[1]
try:
    with open(path) as f:
        results = json.load(f)
except Exception as exc:  # plan-check itself failed before it wrote a report
    print(f"plan-check-all: no plan-check report: {exc}")
    sys.exit(1)

failed = 0
for r in results:
    name = r.get("plan", "?")
    issues = r.get("issues") or []
    if issues:
        failed += 1
        print(f"FAIL {name}: {len(issues)} of {r.get('points', 0)} points fail (map {r.get('map', '?')})")
        for i in issues:
            detail = i.get("detail") or ""
            print(f"  leg {i.get('leg', '?')} {i.get('leg_name', '')}: {i.get('part', '')}: {i.get('reason', '')}: {detail}")
    else:
        print(f"PASS {name}: {r.get('points', 0)} points on map {r.get('map', '?')} ({len(r.get('warnings') or [])} warnings, {len(r.get('exempt') or [])} exempt)")
if failed:
    print(f"plan-check-all: {failed} of {len(results)} plan(s) failed")
    sys.exit(1)
print(f"plan-check-all: all {len(results)} plan(s) passed")
PY
exit "$status"
