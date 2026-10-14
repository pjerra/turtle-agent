#!/usr/bin/env bash
# Run the remote Uldaman dungeon with one command: the agent binary of origin/main's SHA (built once on the test VM), the login (only
# if the agent is not logged in already), dungeon_run with the prepare block, a poll every 30 s, and trace-score at the end.
#
#   tools/run-uldaman.sh [--relogin] [--no-fresh] [--from-leg N] [--logout] [--print-params]
#
#   --relogin      log the agent out and in again with this SHA's binary (an agent that is logged in keeps the binary it started with)
#   --no-fresh     do not reset the instance (prepare.fresh false): a run that goes on in the instance the bots are in
#   --from-leg N   start at leg N (default 0)
#   --logout       log the agent out after the run (default: it stays online, so the next run needs no login)
#   --print-params print the dungeon_run params and exit (nothing is sent, no login, no build)
#
# Environment (all optional):
#   TURTLE_AGENT_DIR  the checkout (default ~/turtle-agent); the binary goes to its bin/turtle-agent-<sha8>
#   SETTINGS          the agent's settings file (default ~/.config/turtle-agent/settings-2.json); only its "rpc" and (if set) "rpc_token" keys
#                     are read, and neither is ever printed. The token file rpc-<port>.token next to it is read when there is no rpc_token.
#   CHARACTER         the agent's character (default Glacienne, a GM)
#   LEAD, BOTS, TANK, HEALER   the party (default Lcwarra; Lcpriest,Lchunter,Lcrogue,Lcwarlock; Lcwarra; Lcpriest)
#   MAX_MIN           give up and dungeon_stop after this many minutes (default 90); POLL_S the seconds between two dungeon_status polls (default 30)
#   NO_FETCH=1        do not `git fetch` before reading origin/main
#   OUT               a folder for the agent's log and capture (default /tmp/run-uldaman-<time>)
#   VMTEST            the vmtest.sh that builds on the VM (default ~/TortoiseBots-audit/tools/vmtest.sh); VMTEST_HOST is its host
#
# The setup this replaces (about 30 calls, ~11 min) is the `prepare` block of dungeon_run: `.bot add` for the bots that are offline, a fresh
# instance (teleport out, leave the old group, `.instance unbind all` per bot by name and for the agent), `.gm visible off`; then the
# preflight and the enter phase. See docs/LIVE-CHECKS.md "Remote Uldaman in one command".
set -euo pipefail

ROOT=${TURTLE_AGENT_DIR:-$HOME/turtle-agent}
SETTINGS=${SETTINGS:-$HOME/.config/turtle-agent/settings-2.json}
CHARACTER=${CHARACTER:-Glacienne}
LEAD=${LEAD:-Lcwarra}
BOTS=${BOTS:-Lcpriest,Lchunter,Lcrogue,Lcwarlock}
TANK=${TANK:-$LEAD}
HEALER=${HEALER:-Lcpriest}
MAX_MIN=${MAX_MIN:-90}
POLL_S=${POLL_S:-30}
VMTEST=${VMTEST:-$HOME/TortoiseBots-audit/tools/vmtest.sh}
VM=${VMTEST_HOST:-yulon-ubuntu2}
FRESH=true FROM_LEG=0 RELOGIN=0 LOGOUT=0 PRINT=0

while [ $# -gt 0 ]; do
  case $1 in
    --relogin) RELOGIN=1 ;;
    --no-fresh) FRESH=false ;;
    --from-leg) shift; FROM_LEG=${1:?--from-leg needs a number} ;;
    --logout) LOGOUT=1 ;;
    --print-params) PRINT=1 ;;
    -h|--help) sed -n '2,25p' "$0"; exit 0 ;;
    *) echo "unknown option $1 (see --help)" >&2; exit 2 ;;
  esac
  shift
done

say() { printf '%s %s\n' "$(date +%H:%M:%S)" "$*"; }
die() { say "ERROR: $*" >&2; exit 1; }

# --- the party: names are letters only (the agent checks too); they go into the JSON below as they are
name_ok() { [[ $1 =~ ^[A-Za-z]{2,12}$ ]]; }
for n in "$LEAD" "$TANK" "$HEALER" "$CHARACTER"; do name_ok "$n" || die "not a character name: $n"; done
[[ $FROM_LEG =~ ^[0-9]+$ ]] || die "--from-leg needs a number"
[[ $MAX_MIN =~ ^[0-9]+$ ]] || die "MAX_MIN needs a number"
IFS=, read -r -a BOT_LIST <<<"$BOTS"
bots_json=""
for b in "${BOT_LIST[@]}"; do
  name_ok "$b" || die "not a character name: $b"
  bots_json+="\"$b\","
done
bots_json="[${bots_json%,}]"

sha_label=${SHA8:-unknown}
params() {
  printf '{"name":"uldaman","mode":"remote","lead_bot":"%s","bots":%s,"tank":"%s","healer":"%s","from_leg":%s,' "$LEAD" "$bots_json" "$TANK" "$HEALER" "$FROM_LEG"
  printf '"prepare":{"online":true,"fresh":%s,"gm_setup":["gm visible off"]},' "$FRESH"
  printf '"trace_meta":{"script":"run-uldaman.sh","agent_sha":"%s"}}' "$sha_label"
}
if [ "$PRINT" = 1 ]; then params; echo; exit 0; fi

# --- JSON helpers: jq when there is one, else python3. field reads a dotted path of keys from stdin; a list or object comes out as JSON.
if command -v jq >/dev/null 2>&1; then
  field() { jq -r --arg p "$1" 'getpath($p | split(".")) as $v | if $v == null then "" elif ($v|type) == "string" or ($v|type) == "number" or ($v|type) == "boolean" then $v|tostring else $v|tojson end' 2>/dev/null || true; }
else
  command -v python3 >/dev/null 2>&1 || die "needs jq or python3"
  field() { python3 -c '
import json, sys
try:
    v = json.load(sys.stdin)
    for k in sys.argv[1].split("."):
        v = v[k]
except Exception:
    v = None
if v is None: print("")
elif isinstance(v, (dict, list)): print(json.dumps(v, separators=(",", ":")))
else: print(str(v).lower() if isinstance(v, bool) else v)' "$1" 2>/dev/null || true; }
fi
settings_field() { field "$1" <"$SETTINGS"; }

[ -f "$SETTINGS" ] || die "no settings file $SETTINGS (set SETTINGS)"
RPC=$(settings_field rpc)
[ -n "$RPC" ] || die "the settings file has no \"rpc\" address"
case $RPC in 127.0.0.1:*|localhost:*|\[::1\]:*) ;; *) die "rpc must be a loopback address" ;; esac
PORT=${RPC##*:}
TOKEN_FILE="$(dirname "$SETTINGS")/rpc-$PORT.token"

# rpc METHOD [PARAMS] [SECONDS]: one JSON-RPC call, the response on stdout. The token goes to curl on stdin, never on its command line.
rpc() {
  local params=${2-} tok
  [ -n "$params" ] || params='{}'
  tok=$(settings_field rpc_token)
  if [ -z "$tok" ] && [ -r "$TOKEN_FILE" ]; then tok=$(<"$TOKEN_FILE"); fi
  printf 'header = "Authorization: Bearer %s"\n' "$tok" |
    curl -sS --max-time "${3:-30}" -K - -H 'Content-Type: application/json' \
      -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$1\",\"params\":$params}" "http://$RPC/rpc"
}

# --- the binary: origin/main's short SHA
[ -d "$ROOT/.git" ] || [ -f "$ROOT/.git" ] || die "$ROOT is not a git checkout (set TURTLE_AGENT_DIR)"
if [ "${NO_FETCH:-0}" != 1 ]; then git -C "$ROOT" fetch -q origin; fi
SHA8=$(git -C "$ROOT" rev-parse --short=8 origin/main)
sha_label=$SHA8
BIN="$ROOT/bin/turtle-agent-$SHA8"
if [ -x "$BIN" ]; then
  say "binary: $BIN (already built)"
else
  say "binary: $BIN is missing, building origin/main $SHA8 on the VM"
  [ -x "$VMTEST" ] || die "no vmtest.sh at $VMTEST (set VMTEST)"
  wt=$(mktemp -d "$HOME/wt-ta-run-XXXXXX")
  trap 'git -C "$ROOT" worktree remove --force "$wt" >/dev/null 2>&1 || true' EXIT
  git -C "$ROOT" worktree add -q --detach "$wt" "$SHA8"
  "$VMTEST" "$wt" 'make build' >&2
  mkdir -p "$ROOT/bin"
  scp -q -o LogLevel=ERROR "$VM:tb-ci/work/$(basename "$wt")/bin/turtle-agent" "$BIN.part"
  chmod 755 "$BIN.part"
  mv "$BIN.part" "$BIN"
  git -C "$ROOT" worktree remove --force "$wt" >/dev/null 2>&1 || true
  trap - EXIT
  say "binary: built and copied to $BIN"
fi

# --- the login, only when the agent is not in the world already
agent_state() { rpc status '{}' 10 2>/dev/null | field result.state || true; }
STATE=$(agent_state)
if [ "$STATE" = in_world ] && [ "$RELOGIN" = 1 ]; then
  say "relogin: logging $CHARACTER out"
  rpc logout '{}' 60 >/dev/null 2>&1 || true
  for _ in $(seq 1 30); do [ -n "$(agent_state)" ] || break; sleep 2; done
  STATE=$(agent_state)
fi
if [ "$STATE" = in_world ]; then
  say "agent: already logged in at $RPC (it runs the binary it started with; --relogin starts $SHA8)"
else
  OUT=${OUT:-/tmp/run-uldaman-$(date +%Y%m%dT%H%M%S)}
  mkdir -p "$OUT"
  say "agent: logging $CHARACTER in at $RPC (log and capture in $OUT)"
  (cd "$ROOT" && nohup "$BIN" login --config "$SETTINGS" --rpc "$RPC" --capture "$OUT/agent.cap" --stay 3h "$CHARACTER" >"$OUT/agent.log" 2>&1 &)
  for _ in $(seq 1 60); do
    STATE=$(agent_state)
    [ "$STATE" = in_world ] && break
    sleep 2
  done
  [ "$STATE" = in_world ] || die "the agent is not in the world after 120 s (see $OUT/agent.log)"
  say "agent: in the world"
fi

# --- the run
START=$(date +%s)
RESP=$(rpc dungeon_run "$(params)" 60) || die "dungeon_run: no answer from $RPC"
ERR=$(field error.message <<<"$RESP")
[ -z "$ERR" ] || die "dungeon_run refused: $ERR"
RUN_ID=$(field result.run_id <<<"$RESP")
TRACE=$(field result.trace_file <<<"$RESP")
say "run $RUN_ID started (trace $TRACE)"

LAST=""
while :; do
  sleep "$POLL_S"
  ST=$(rpc dungeon_status '{}' 20 2>/dev/null) || { say "dungeon_status: no answer"; continue; }
  STATE=$(field result.state <<<"$ST")
  PHASE=$(field result.report.watchdog.waiting <<<"$ST")
  STEP=$(field result.report.current <<<"$ST")
  STALL=$(field result.stalled_for_s <<<"$ST")
  say "state=${STATE:-?} phase=${PHASE:--} step=${STEP:--} stalled_for_s=${STALL:-0}"
  case $STATE in done|failed|stopped) break ;; esac
  if [ $(( ($(date +%s) - START) / 60 )) -ge "$MAX_MIN" ]; then
    say "the run took $MAX_MIN minutes: dungeon_stop"
    ST=$(rpc dungeon_stop '{}' 60) || true
    STATE=$(field result.state <<<"$ST")
    break
  fi
done

# --- the result
say "result: state=$STATE error=$(field result.report.error <<<"$ST")"
say "setup_s=$(field result.report.setup_s <<<"$ST") prepare=$(field result.report.prepare <<<"$ST")"
say "preflight_s=$(field result.report.remote.preflight.seconds <<<"$ST") enter_s=$(field result.report.remote.enter.seconds <<<"$ST") run_s=$(field result.report.seconds <<<"$ST")"
TRACE=${TRACE:-$(field result.report.trace_file <<<"$ST")}
SCORE=0
if [ -n "$TRACE" ]; then
  case $TRACE in /*) T=$TRACE ;; *) T="$ROOT/$TRACE" ;; esac
  say "trace-score $T"
  (cd "$ROOT" && "$BIN" trace-score "$T") || SCORE=$?
fi
if [ "$LOGOUT" = 1 ]; then rpc logout '{}' 60 >/dev/null 2>&1 || true; say "agent logged out"; fi
[ "$STATE" = done ] || exit 1
[ "$SCORE" = 0 ] || exit "$SCORE"
