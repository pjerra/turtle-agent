#!/usr/bin/env bash
# Run the remote Uldaman dungeon with one command: the agent binary of origin/main's SHA (built once on the test VM), the login (only
# if the agent is not logged in already), dungeon_run with the prepare block, a poll every 30 s, and trace-score at the end.
#
#   tools/run-uldaman.sh [--mode commander|observe|leader] [--brain PRESET] [--rotation NAME] [--role ROLE]
#                        [--hire [--rehire] [--level N]] [--relogin] [--no-fresh] [--from-leg N [--pull-group M] [--start-here]]
#                        [--pulltest LEG:GROUP[:TRIES[:RESET]]] [--logout] [--print-params]
#
#   --mode M       the run mode (README "Run modes"; or MODE=). commander (the default): the agent leads a bot group from outside it, everything
#                  below. leader: the agent's character is in the group and plays while it leads (the bot brain, the preset of its class unless
#                  --brain names one; the party is the one the character is in: none of the commander's options go with it, only --from-leg).
#                  observe: a TortoiseBots run leader (`.bot run`) clears the dungeon and the agent watches (README "Observe mode"): the commander
#                  options below (--hire, --no-fresh, LEAD/BOTS) prepare the party, but no start option and no TANK/HEALER go with it.
#   --brain P      leader mode: the brain preset that plays the character (frost_mage, arms_warrior, ...; default: by the character's class and --role)
#   --rotation R   leader mode: a rotation file that takes the place of the preset's class rotation (README "Rotations as data"; `turtle-agent
#                  rotation list`); the report names it with its file's digest, so two arms of an A/B test are told apart
#   --role R       leader mode: dps (default), tank or healer
#   --hire         the owner's rule: the party is five HIRED bots at the plan's level, the commander outside the group (prepare.party "hire",
#                  README "Hired party"). The agent hires a warrior tank, a priest healer and three damage dealers (hunter, rogue, warlock),
#                  checks each (level, gear not epic, key spells, talents, supplies, pets), fixes what `.bot autogear` and `.bot maintenance`
#                  can, binds them, and writes the `ready` block of the report. LEAD, BOTS, TANK, HEALER do not apply; --no-fresh is ignored
#                  (new hires have no saved instance; kept ones keep theirs, so --rehire for a clean instance).
#   --rehire       with --hire: end the old binding and dismiss the commander's current hires first (default: keep the ones that match)
#   --level N      with --hire: the hires' level (default: the middle of the plan's level band, 43 for Uldaman)
#   --relogin      log the agent out and in again with this SHA's binary (an agent that is logged in keeps the binary it started with)
#   --no-fresh     do not reset the instance (prepare.fresh false): a run that goes on in the instance the bots are in
#   --from-leg N   start at leg N (default 0). Past leg 0 the commander (a GM) is moved to the leg's start with `.appear <lead bot>` and `.go xyz`, the
#                  lead bot is brought with `.bot summon`, the others with `cmd summon`; the legs before are reported "skipped (from_leg)", never cleared
#                  (README "Start anywhere and the pull test"). The two GM lines count in gm_commands and setup_shortcuts.
#   --pull-group M start inside leg N at pull group M: the party is gathered at a safe spot short of that pack (the leg is --from-leg, default 0)
#   --have-items 7733,7740
#                  item entries the party already carries: a start past the leg that makes an item a later step needs (Uldaman: the Medallion 7740 from
#                  leg 0, the Staff of Prehistoria 7733 from leg 1) is refused without them
#   --start-here   with --from-leg: no move, the run starts where the lead bot stands (the old behaviour)
#   --pulltest LEG:GROUP[:TRIES[:RESET]]
#                  the pull-test scenario instead of the dungeon: pull that one pack TRIES times (default 3; RESET auto, equivalent, respawn or none, default
#                  auto), score each try, print the table and save pulltest.json in OUT; compare two with `turtle-agent pulltest --compare a.json b.json`
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
#   COMPOSITION      with --hire: the party as a comma list of class[:role] (default warrior:tank,priest:healer,hunter,rogue,warlock)
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
MODE=${MODE:-commander} BRAIN= ROTATION= ROLE=
FRESH=true FROM_LEG=0 PULL_GROUP= HAVE_ITEMS= START_HERE=0 PULLTEST= RELOGIN=0 LOGOUT=0 PRINT=0 HIRE=0 REHIRE=0 LEVEL=
COMPOSITION=${COMPOSITION:-}

while [ $# -gt 0 ]; do
  case $1 in
    --mode) shift; MODE=${1:?--mode needs commander, observe or leader} ;;
    --brain) shift; BRAIN=${1:?--brain needs a preset name} ;;
    --rotation) shift; ROTATION=${1:?--rotation needs a rotation name} ;;
    --role) shift; ROLE=${1:?--role needs dps, tank or healer} ;;
    --relogin) RELOGIN=1 ;;
    --no-fresh) FRESH=false ;;
    --hire) HIRE=1 ;;
    --rehire) REHIRE=1 ;;
    --level) shift; LEVEL=${1:?--level needs a number} ;;
    --from-leg) shift; FROM_LEG=${1:?--from-leg needs a number} ;;
    --pull-group) shift; PULL_GROUP=${1:?--pull-group needs a number} ;;
    --start-here) START_HERE=1 ;;
    --have-items) shift; HAVE_ITEMS=${1:?--have-items needs a comma list of item entries} ;;
    --pulltest) shift; PULLTEST=${1:?--pulltest needs LEG:GROUP[:TRIES[:RESET]]} ;;
    --logout) LOGOUT=1 ;;
    --print-params) PRINT=1 ;;
    -h|--help) sed -n '2,/^set -/{/^set -/!p}' "$0"; exit 0 ;;
    *) echo "unknown option $1 (see --help)" >&2; exit 2 ;;
  esac
  shift
done

say() { printf '%s %s\n' "$(date +%H:%M:%S)" "$*"; }
die() { say "ERROR: $*" >&2; exit 1; }

# --- the mode
case $MODE in
  commander|remote) MODE=commander ;;
  leader|group) MODE=leader ;;
  observe) ;;
  *) die "--mode is commander, observe or leader" ;;
esac
name_word() { [[ $1 =~ ^[a-z0-9_]{1,40}$ ]]; }
[ -z "$BRAIN" ] || name_word "$BRAIN" || die "--brain: a preset name (a-z, 0-9, _)"
[ -z "$ROTATION" ] || name_word "$ROTATION" || die "--rotation: a rotation name (a-z, 0-9, _)"
[ -z "$ROLE" ] || case $ROLE in dps|tank|healer) ;; *) die "--role is dps, tank or healer" ;; esac
if [ "$MODE" != leader ]; then
  [ -z "$BRAIN$ROTATION$ROLE" ] || die "--brain, --rotation and --role go with --mode leader (in $MODE mode the bots fight)"
  [ "$MODE" != observe ] || { [ "$FROM_LEG" = 0 ] && [ -z "$PULLTEST" ] && [ -z "$PULL_GROUP" ] && [ "$START_HERE" = 0 ] && [ -z "$HAVE_ITEMS" ]; } ||
    die "observe mode starts at the plan's first step: --from-leg, --pull-group, --start-here, --have-items and --pulltest are commander options"
else
  [ "$HIRE" = 0 ] && [ "$REHIRE" = 0 ] && [ -z "$LEVEL" ] && [ -z "$PULLTEST" ] && [ -z "$PULL_GROUP" ] && [ "$START_HERE" = 0 ] && [ -z "$HAVE_ITEMS" ] && [ "$FRESH" = true ] && [ -z "$COMPOSITION" ] ||
    die "leader mode takes only --from-leg of the start options: --hire, --rehire, --level, --pulltest, --pull-group, --start-here, --have-items, --no-fresh and COMPOSITION are commander options"
fi

# --- the party: names are letters only (the agent checks too); they go into the JSON below as they are
name_ok() { [[ $1 =~ ^[A-Za-z]{2,12}$ ]]; }
for n in "$LEAD" "$TANK" "$HEALER" "$CHARACTER"; do name_ok "$n" || die "not a character name: $n"; done
[[ $FROM_LEG =~ ^[0-9]+$ ]] || die "--from-leg needs a number"
[ -z "$PULL_GROUP" ] || [[ $PULL_GROUP =~ ^[0-9]+$ ]] || die "--pull-group needs a number"
[ -z "$PULL_GROUP" ] || [ "$START_HERE" = 0 ] || die "--pull-group and --start-here exclude each other"
[ "$START_HERE" = 0 ] || [ "$FROM_LEG" != 0 ] || die "--start-here goes with --from-leg N (N above 0)"
[ -z "$HAVE_ITEMS" ] || [[ $HAVE_ITEMS =~ ^[0-9]+(,[0-9]+){0,19}$ ]] || die "--have-items needs up to 20 item entries, comma separated"
pulltest_json=""
if [ -n "$PULLTEST" ]; then
  [ "$FROM_LEG" = 0 ] && [ -z "$PULL_GROUP" ] && [ "$START_HERE" = 0 ] || die "--pulltest names its own start: leave --from-leg, --pull-group and --start-here out"
  IFS=: read -r PT_LEG PT_GROUP PT_TRIES PT_RESET <<<"$PULLTEST"
  [[ $PT_LEG =~ ^[0-9]+$ ]] || die "--pulltest: the leg must be a number (LEG:GROUP[:TRIES[:RESET]])"
  [[ ${PT_GROUP:-} =~ ^[0-9]+$ ]] || die "--pulltest: the pull group must be a number (LEG:GROUP[:TRIES[:RESET]])"
  [ -z "${PT_TRIES:-}" ] || { [[ $PT_TRIES =~ ^[0-9]+$ ]] && [ "$PT_TRIES" -ge 1 ] && [ "$PT_TRIES" -le 10 ]; } || die "--pulltest: tries is 1 to 10"
  case ${PT_RESET:-auto} in auto|equivalent|respawn|none) ;; *) die "--pulltest: reset is auto, equivalent, respawn or none" ;; esac
  pulltest_json="\"pulltest\":{\"leg\":$PT_LEG,\"pull_group\":$PT_GROUP"
  [ -z "${PT_TRIES:-}" ] || pulltest_json+=",\"tries\":$PT_TRIES"
  [ -z "${PT_RESET:-}" ] || pulltest_json+=",\"reset\":\"$PT_RESET\""
  pulltest_json+="},"
fi
[[ $MAX_MIN =~ ^[0-9]+$ ]] || die "MAX_MIN needs a number"
[ "$HIRE" = 1 ] || { [ "$REHIRE" = 0 ] && [ -z "$LEVEL" ]; } || die "--rehire and --level go with --hire"
[ -z "$LEVEL" ] || { [[ $LEVEL =~ ^[0-9]{1,2}$ ]] && [ "$LEVEL" -ge 1 ] && [ "$LEVEL" -le 60 ]; } || die "--level needs a number from 1 to 60"
comp_json=""
if [ -n "$COMPOSITION" ]; then
  IFS=, read -r -a COMP_LIST <<<"$COMPOSITION"
  for c in "${COMP_LIST[@]}"; do
    [[ $c =~ ^[a-z]+(:[a-z]+)?$ ]] || die "COMPOSITION: $c is not class or class:role (lower case)"
    comp_json+="\"$c\","
  done
  comp_json="[${comp_json%,}]"
fi
IFS=, read -r -a BOT_LIST <<<"$BOTS"
bots_json=""
for b in "${BOT_LIST[@]}"; do
  name_ok "$b" || die "not a character name: $b"
  bots_json+="\"$b\","
done
bots_json="[${bots_json%,}]"

# The start of the run: the pull test names its own, else from_leg with the optional pull group and start_here.
start_json="\"from_leg\":$FROM_LEG,"
if [ "$MODE" = observe ]; then
  start_json=""
elif [ -n "$pulltest_json" ]; then
  start_json=$pulltest_json
else
  [ -z "$PULL_GROUP" ] || start_json+="\"from_pull_group\":$PULL_GROUP,"
  [ "$START_HERE" = 0 ] || start_json+="\"start_here\":true,"
  [ -z "$HAVE_ITEMS" ] || start_json+="\"have_items\":[$HAVE_ITEMS],"
fi

sha_label=${SHA8:-unknown}
params() {
  if [ "$MODE" = leader ]; then
    printf '{"name":"uldaman","mode":"leader","from_leg":%s,' "$FROM_LEG"
    [ -z "$BRAIN" ] || printf '"brain":"%s",' "$BRAIN"
    [ -z "$ROTATION" ] || printf '"rotation":"%s",' "$ROTATION"
    [ -z "$ROLE" ] || printf '"role":"%s",' "$ROLE"
    printf '"trace_meta":{"script":"run-uldaman.sh","agent_sha":"%s","mode":"leader"}}' "$sha_label"
    return
  fi
  if [ "$HIRE" = 1 ]; then
    # The hired party names itself: no lead_bot, bots, tank or healer. fresh and online do not go with it.
    local hire="" sep=""
    [ "$REHIRE" = 0 ] || { hire+="${sep}\"rehire\":true"; sep=","; }
    [ -z "$LEVEL" ] || { hire+="${sep}\"level\":$LEVEL"; sep=","; }
    [ -z "$comp_json" ] || { hire+="${sep}\"composition\":$comp_json"; sep=","; }
    printf '{"name":"uldaman","mode":"%s",%s' "$MODE" "$start_json"
    printf '"prepare":{"party":"hire","hire":{%s},"gm_setup":["gm visible off"]},' "$hire"
    printf '"trace_meta":{"script":"run-uldaman.sh","agent_sha":"%s","party":"hire"}}' "$sha_label"
    return
  fi
  if [ "$MODE" = observe ]; then
    printf '{"name":"uldaman","mode":"observe","lead_bot":"%s","bots":%s,' "$LEAD" "$bots_json"
  else
    printf '{"name":"uldaman","mode":"commander","lead_bot":"%s","bots":%s,"tank":"%s","healer":"%s",%s' "$LEAD" "$bots_json" "$TANK" "$HEALER" "$start_json"
  fi
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

while :; do
  sleep "$POLL_S"
  if [ $(( ($(date +%s) - START) / 60 )) -ge "$MAX_MIN" ]; then
    say "the run took $MAX_MIN minutes: dungeon_stop"
    ST=$(rpc dungeon_stop '{}' 60 2>/dev/null) || ST=""
    STATE=$(field result.state <<<"$ST")
    break
  fi
  ST=$(rpc dungeon_status '{}' 20 2>/dev/null) || { say "dungeon_status: no answer"; continue; }
  STATE=$(field result.state <<<"$ST")
  PHASE=$(field result.report.watchdog.waiting <<<"$ST")
  STEP=$(field result.report.current <<<"$ST")
  STALL=$(field result.stalled_for_s <<<"$ST")
  say "state=${STATE:-?} phase=${PHASE:--} step=${STEP:--} stalled_for_s=${STALL:-0}"
  case $STATE in done|failed|stopped) break ;; esac
done

# --- the result
say "result: state=$STATE error=$(field result.report.error <<<"$ST")"
say "setup_s=$(field result.report.setup_s <<<"$ST") prepare=$(field result.report.prepare <<<"$ST")"
if [ "$HIRE" = 1 ]; then
  say "ready: party=$(field result.report.ready.party <<<"$ST") level=$(field result.report.ready.level <<<"$ST") ok=$(field result.report.ready.ready <<<"$ST") hire_s=$(field result.report.ready.seconds <<<"$ST") setup_s=$(field result.report.ready.setup_s <<<"$ST") hired=$(field result.report.ready.hired <<<"$ST") reused=$(field result.report.ready.reused <<<"$ST") note=$(field result.report.ready.level_note <<<"$ST")"
fi
say "preflight_s=$(field result.report.remote.preflight.seconds <<<"$ST") enter_s=$(field result.report.remote.enter.seconds <<<"$ST") run_s=$(field result.report.seconds <<<"$ST")"
if [ "$(field result.report.start.kind <<<"$ST")" != "" ]; then
  say "start: kind=$(field result.report.start.kind <<<"$ST") method=$(field result.report.start.method <<<"$ST") start_s=$(field result.report.start.seconds <<<"$ST") gm_lines=$(field result.report.start.gm_commands <<<"$ST") skipped=$(field result.report.start.skipped <<<"$ST")"
fi
if [ -n "$PULLTEST" ]; then
  # The scenario's result: saved for `turtle-agent pulltest --compare`, printed as a table. A scenario run is not a dungeon run, so no trace-score.
  PT_OUT=${OUT:-/tmp/run-uldaman-$(date +%Y%m%dT%H%M%S)}
  mkdir -p "$PT_OUT"
  field result.report.pulltest <<<"$ST" >"$PT_OUT/pulltest.json"
  if [ -s "$PT_OUT/pulltest.json" ]; then
    "$BIN" pulltest "$PT_OUT/pulltest.json" || true
    say "pulltest result: $PT_OUT/pulltest.json (compare: $BIN pulltest --compare a.json b.json)"
  else
    say "no pulltest result in the report"
  fi
  if [ "$LOGOUT" = 1 ]; then rpc logout '{}' 60 >/dev/null 2>&1 || true; say "agent logged out"; fi
  [ "$STATE" = done ] || exit 1
  exit 0
fi
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
