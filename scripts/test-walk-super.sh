#!/usr/bin/env bash
#
# test-walk-super.sh -- cases for tools/walk-super.sh, driven by a fake turtle-agent.
#
# The fake stands in for the real binary path in the cmd file: it writes a
# run.log with a chosen last TBM:RUN line into its --out dir and exits with a
# chosen code (FAKE_EXIT, or popped per-run from FAKE_SEQ_FILE). FAKE_SUMMARY
# writes a summary.json verbatim (its `wipes` and `end_reason` are what the
# no-progress stop reads; unset: no summary.json). Every
# invocation appends its %q-quoted argv to FAKE_ARGS_LOG (one line per run).
# FAKE_SLEEP makes the fake sleep that long before exiting (default 0): the
# signal cases use it to hold the supervisor inside a run. FAKE_IGNORE_INT
# makes the fake ignore SIGINT like a backgrounded walk child does (job
# control off resets nothing: INT arrives ignored, so the supervisor's INT
# handler must escalate to TERM or the walk survives it).
#
# Usage: test-walk-super.sh [case-number ...]   (default: all cases + mutation check)
# Env: WALK_SUPER_BIN (default: walk-super.sh next to this file),
#      WALK_SUPER_WAIT (default 1: seconds between test restarts; prod default is 20).
set -u

HERE="$(cd "$(dirname "$0")" && pwd)"
SUPER_BIN="${WALK_SUPER_BIN:-$HERE/walk-super.sh}"
export WALK_SUPER_WAIT="${WALK_SUPER_WAIT:-1}"

PASS=0
FAIL=0
ok() { printf 'PASS %s\n' "$1"; PASS=$((PASS + 1)); }
bad() { printf 'FAIL %s\n' "$1"; FAIL=$((FAIL + 1)); }

# WIPE_SUMMARY is a run that really wiped (summary.json's `wipes` non-empty): the walk-25 shape the no-progress stop
# needs. NO_WIPE_SUMMARY is the same run without one (review round 1, L4).
WIPE_SUMMARY='{"wipes": [204], "end_reason": "not revived: the revive point never went quiet", "exit_code": 19}'
NO_WIPE_SUMMARY='{"wipes": [], "end_reason": "interrupted", "exit_code": 19}'

# PLACE_SUMMARY is a run that died in the place phase (exit 18, no TBM:RUN leg): summary.json's `place.start_step`
# and `end_reason` are what the empty-leg no-progress guard compares (walk 26d, #373 fix C).
PLACE_SUMMARY='{"wipes": [], "end_reason": "place: fighting after the place", "exit_code": 18, "place": {"start_step": 201, "leg": "high_priestess_marli", "point": [-12242.112, -1685.4194, 129.40071], "until_step": 216, "instance": 102, "circles": [], "not_cleared": [], "quiet": true, "placed": 0, "hires": 20}}'

T="$(mktemp -d /tmp/walk-super-test.XXXXXX)"
trap 'rm -rf "$T"' EXIT

write_fake() {
  cat >"$T/fake-walk" <<'FAKE_EOF'
#!/usr/bin/env bash
set -u
argv_str=""
if (( $# > 0 )); then
  argv_str="$(printf '%q ' "$@")"
fi
printf '%s\n' "$argv_str" >>"${FAKE_ARGS_LOG:?need FAKE_ARGS_LOG}"
out=""
while (( $# > 0 )); do
  case "$1" in
    --out)
      if (( $# >= 2 )); then out="$2"; shift 2; else out=""; shift; fi
      ;;
    --out=*)
      out="${1#--out=}"
      shift
      ;;
    *)
      shift
      ;;
  esac
done
[[ -n "$out" ]] || { echo "fake-walk: no --out" >&2; exit 2; }
mkdir -p -- "$out"
if [[ -n "${FAKE_NO_TBM:-}" ]]; then
  printf 'walk starting\nno status lines yet\n' >"$out/run.log"
else
  leg="${FAKE_LEG:-high_priest_venoxis}"
  if [[ -n "${FAKE_LEG_SEQ_FILE:-}" && -s "$FAKE_LEG_SEQ_FILE" ]]; then
    leg="$(head -n 1 -- "$FAKE_LEG_SEQ_FILE")"
    tail -n +2 -- "$FAKE_LEG_SEQ_FILE" >"$FAKE_LEG_SEQ_FILE.tmp" && mv -- "$FAKE_LEG_SEQ_FILE.tmp" "$FAKE_LEG_SEQ_FILE"
  fi
  step="${FAKE_STEP:-41}"
  if [[ -n "${FAKE_STEP_SEQ_FILE:-}" && -s "$FAKE_STEP_SEQ_FILE" ]]; then
    step="$(head -n 1 -- "$FAKE_STEP_SEQ_FILE")"
    tail -n +2 -- "$FAKE_STEP_SEQ_FILE" >"$FAKE_STEP_SEQ_FILE.tmp" && mv -- "$FAKE_STEP_SEQ_FILE.tmp" "$FAKE_STEP_SEQ_FILE"
  fi
  {
    printf 'TBM:RUN|zul_gurub|running|%s/1103|%s|move|walk|fighting\n' "$((step - 1))" "$leg"
    printf 'TBM:RUN|zul_gurub|paused|%s/1103|%s|boss|walk|16 dead: the run stops here\n' "$step" "$leg"
  } >"$out/run.log"
fi
# FAKE_SUMMARY writes a summary.json verbatim: the no-progress stop reads its `wipes` and `end_reason` (review round
# 1, L4). Unset: no summary at all.
if [[ -n "${FAKE_SUMMARY_SEQ_FILE:-}" && -s "$FAKE_SUMMARY_SEQ_FILE" ]]; then
  sum="$(head -n 1 -- "$FAKE_SUMMARY_SEQ_FILE")"
  tail -n +2 -- "$FAKE_SUMMARY_SEQ_FILE" >"$FAKE_SUMMARY_SEQ_FILE.tmp" && mv -- "$FAKE_SUMMARY_SEQ_FILE.tmp" "$FAKE_SUMMARY_SEQ_FILE"
  printf '%s\n' "$sum" >"$out/summary.json"
elif [[ -n "${FAKE_SUMMARY:-}" ]]; then
  printf '%s\n' "$FAKE_SUMMARY" >"$out/summary.json"
fi
if [[ -n "${FAKE_STDERR_LINE:-}" ]]; then
  printf '%s\n' "$FAKE_STDERR_LINE" >&2
fi
# FAKE_IGNORE_INT makes the fake ignore SIGINT, like a backgrounded walk child
# with job control off: the supervisor's INT handler must escalate to TERM or
# the walk survives it. FAKE_SLEEP holds the fake inside the run (seconds)
# so the signal cases can strike mid-run or mid-wait.
if [[ -n "${FAKE_IGNORE_INT:-}" ]]; then
  trap '' INT
fi
if [[ -n "${FAKE_SLEEP:-}" ]]; then
  sleep "$FAKE_SLEEP"
fi
if [[ -n "${FAKE_SEQ_FILE:-}" && -s "$FAKE_SEQ_FILE" ]]; then
  code="$(head -n 1 -- "$FAKE_SEQ_FILE")"
  tail -n +2 -- "$FAKE_SEQ_FILE" >"$FAKE_SEQ_FILE.tmp" && mv -- "$FAKE_SEQ_FILE.tmp" "$FAKE_SEQ_FILE"
else
  code="${FAKE_EXIT:-0}"
fi
exit "$code"
FAKE_EOF
  chmod +x "$T/fake-walk"
}

make_cmd() {
  local p="$1"
  shift
  {
    printf '%s' "$T/fake-walk"
    printf ' %q' "$@"
    printf '\n'
  } >"$p"
}

runs_done() {
  if [[ -f "$1" ]]; then
    wc -l <"$1" 2>/dev/null | tr -d ' '
  else
    printf '0'
  fi
}

case1() {
  local d="$T/c1" rc runs l2
  mkdir -p "$d"
  make_cmd "$d/cmd.txt" raid-walk zul_gurub --server s --slot 2 --holder H1 \
    --sql /tmp/w.sql --until boss --out "$d/run0"
  printf '19\n0\n' >"$d/seq"
  FAKE_SEQ_FILE="$d/seq" FAKE_ARGS_LOG="$d/args.log" FAKE_LEG="high_priest_venoxis" \
    "$SUPER_BIN" "$d/cmd.txt" 5 "$d/restart.log" >"$d/stdout.log" 2>"$d/stderr.log"
  rc=$?
  runs="$(runs_done "$d/args.log")"
  (( rc == 0 )) || { bad "case1 exit=$rc want 0"; return; }
  (( runs == 2 )) || { bad "case1 runs=$runs want 2"; return; }
  l2="$(sed -n '2p' "$d/args.log")"
  printf '%s\n' "$l2" | grep -qF -e '--from leg\ high_priest_venoxis' || { bad "case1 run2 missing --from leg venoxis"; return; }
  printf '%s\n' "$l2" | grep -qF -e '--place' || { bad "case1 run2 missing --place"; return; }
  printf '%s\n' "$l2" | grep -qF -e "--out $d/run0-r1" || { bad "case1 run2 missing --out DIR-r1"; return; }
  printf '%s\n' "$l2" | grep -qF -e '--holder H1' || { bad "case1 run2 holder changed"; return; }
  printf '%s\n' "$l2" | grep -qF -e '--until boss' || { bad "case1 run2 lost --until"; return; }
  printf '%s\n' "$l2" | grep -qF -e '--sql /tmp/w.sql' || { bad "case1 run2 lost --sql"; return; }
  (( $(printf '%s' "$l2" | grep -o -F -e '--from' | wc -l) == 1 )) || { bad "case1 run2 --from count != 1"; return; }
  grep -q 'run 1 exit 19' "$d/restart.log" || { bad "case1 restart.log missing run 1 exit 19"; return; }
  grep -q -- "$d/run0-r1" "$d/restart.log" || { bad "case1 restart.log missing new --out"; return; }
  cmp -s "$d/restart.log" "$d/stdout.log" || { bad "case1 stdout differs from restart.log"; return; }
  ok "case1 exit-19 then 0: 2 runs, run2 --from leg venoxis --place --out DIR-r1 holder kept"
}

case2() {
  local d="$T/c2" rc runs l2 nfrom nplace
  mkdir -p "$d"
  make_cmd "$d/cmd.txt" raid-walk zul_gurub --server s --slot 2 --holder H2 \
    --sql /tmp/w.sql --from "leg x" --place --until boss --out "$d/run0"
  printf '19\n0\n' >"$d/seq"
  FAKE_SEQ_FILE="$d/seq" FAKE_ARGS_LOG="$d/args.log" FAKE_LEG="high_priest_venoxis" \
    "$SUPER_BIN" "$d/cmd.txt" 5 "$d/restart.log" >"$d/stdout.log" 2>"$d/stderr.log"
  rc=$?
  runs="$(runs_done "$d/args.log")"
  (( rc == 0 )) || { bad "case2 exit=$rc want 0"; return; }
  (( runs == 2 )) || { bad "case2 runs=$runs want 2"; return; }
  l2="$(sed -n '2p' "$d/args.log")"
  nfrom="$(printf '%s' "$l2" | grep -o -F -e '--from' | wc -l)"
  (( nfrom == 1 )) || { bad "case2 run2 --from count=$nfrom want 1"; return; }
  nplace="$(printf '%s' "$l2" | grep -o -F -e '--place' | wc -l)"
  (( nplace == 1 )) || { bad "case2 run2 --place count=$nplace want 1"; return; }
  printf '%s\n' "$l2" | grep -qF -e '--from leg\ high_priest_venoxis' || { bad "case2 run2 missing new --from leg venoxis"; return; }
  printf '%s\n' "$l2" | grep -qF -e 'leg\ x' && { bad "case2 run2 still carries old leg x"; return; }
  ok "case2 pre-existing --from/--place: run2 has exactly one --from (new leg) and one --place"
}

case3() {
  local d="$T/c3" rc runs
  mkdir -p "$d"
  make_cmd "$d/cmd.txt" raid-walk zul_gurub --server s --slot 2 --holder H3 \
    --until boss --out "$d/run0"
  FAKE_EXIT=19 FAKE_ARGS_LOG="$d/args.log" \
    "$SUPER_BIN" "$d/cmd.txt" 5 "$d/restart.log" >"$d/stdout.log" 2>"$d/stderr.log"
  rc=$?
  runs="$(runs_done "$d/args.log")"
  (( rc == 19 )) || { bad "case3 exit=$rc want 19"; return; }
  (( runs == 1 )) || { bad "case3 runs=$runs want 1"; return; }
  grep -qF -e 'no --sql, cannot place' "$d/restart.log" || { bad "case3 log missing no-sql reason"; return; }
  ok "case3 no --sql: no restart, exit 19, logs no --sql cannot place"
}

case4() {
  local d="$T/c4" rc runs
  mkdir -p "$d"
  make_cmd "$d/cmd.txt" raid-walk zul_gurub --server s --slot 2 --holder H4 \
    --sql /tmp/w.sql --out "$d/run0"
  printf '19\n19\n19\n' >"$d/seq"
  # Every run dies further on: without the progress the no-progress stop (case7) would end this before the cap.
  printf '100\n110\n120\n' >"$d/steps"
  FAKE_SEQ_FILE="$d/seq" FAKE_STEP_SEQ_FILE="$d/steps" FAKE_ARGS_LOG="$d/args.log" \
    "$SUPER_BIN" "$d/cmd.txt" 2 "$d/restart.log" >"$d/stdout.log" 2>"$d/stderr.log"
  rc=$?
  runs="$(runs_done "$d/args.log")"
  (( rc == 19 )) || { bad "case4 exit=$rc want 19"; return; }
  (( runs == 3 )) || { bad "case4 runs=$runs want 3"; return; }
  grep -qF -e 'restart count reached' "$d/restart.log" || { bad "case4 log missing restart-count reason"; return; }
  ok "case4 exit-19 x3 max-restarts 2: 3 runs, exit 19"
}

case5() {
  local d="$T/c5" rc runs
  mkdir -p "$d"
  make_cmd "$d/cmd.txt" raid-walk zul_gurub --server s --slot 2 --holder H5 \
    --sql /tmp/w.sql --out "$d/run0"
  FAKE_EXIT=1 FAKE_ARGS_LOG="$d/args.log" \
    "$SUPER_BIN" "$d/cmd.txt" 5 "$d/restart.log" >"$d/stdout.log" 2>"$d/stderr.log"
  rc=$?
  runs="$(runs_done "$d/args.log")"
  (( rc == 1 )) || { bad "case5 exit=$rc want 1"; return; }
  (( runs == 1 )) || { bad "case5 runs=$runs want 1"; return; }
  grep -qF -e 'is not 18/19' "$d/restart.log" || { bad "case5 log missing not-18/19 reason"; return; }
  ok "case5 exit 1: no restart, exit 1"
}

case6a() {
  # Walk 26d, #373 fix C: an exit-18 --place run with no TBM:RUN leg died in the place phase, so the supervisor
  # retries the SAME command with only --out changed to DIR-r1. The stop then comes from the empty-leg
  # no-progress guard: two consecutive place failures with the same summary.json place.start_step and end_reason.
  local d="$T/c6a" rc runs l2
  mkdir -p "$d"
  make_cmd "$d/cmd.txt" raid-walk zul_gurub --server s --slot 2 --holder H6 \
    --from "leg high_priestess_marli" --place --sql /tmp/w.sql --until boss --out "$d/run0"
  printf '18\n18\n' >"$d/seq"
  printf '%s\n%s\n' "$PLACE_SUMMARY" "$PLACE_SUMMARY" >"$d/sums"
  FAKE_NO_TBM=1 FAKE_SEQ_FILE="$d/seq" FAKE_SUMMARY_SEQ_FILE="$d/sums" FAKE_ARGS_LOG="$d/args.log" \
    "$SUPER_BIN" "$d/cmd.txt" 5 "$d/restart.log" >"$d/stdout.log" 2>"$d/stderr.log"
  rc=$?
  runs="$(runs_done "$d/args.log")"
  (( rc == 18 )) || { bad "case6a exit=$rc want 18"; return; }
  (( runs == 2 )) || { bad "case6a runs=$runs want 2 (one same-command restart, then the no-progress stop)"; return; }
  l2="$(sed -n '2p' "$d/args.log")"
  printf '%s\n' "$l2" | grep -qF -e "--out $d/run0-r1" || { bad "case6a run2 missing --out DIR-r1"; return; }
  printf '%s\n' "$l2" | grep -qF -e '--from leg\ high_priestess_marli' || { bad "case6a run2 lost its --from leg"; return; }
  printf '%s\n' "$l2" | grep -qF -e '--place' || { bad "case6a run2 lost --place"; return; }
  printf '%s\n' "$l2" | grep -qF -e '--sql /tmp/w.sql' || { bad "case6a run2 lost --sql"; return; }
  (( $(printf '%s' "$l2" | grep -o -F -e '--out' | wc -l) == 1 )) || { bad "case6a run2 --out count != 1"; return; }
  grep -qF -e 'retrying the same command' "$d/restart.log" || { bad "case6a log missing the same-command restart"; return; }
  grep -qF -e 'no progress (place failed before the run started at start step 201 twice' "$d/restart.log" ||
    { bad "case6a log missing the empty-leg no-progress stop"; return; }
  ok "case6a exit-18 --place with no TBM:RUN: one same-command restart --out DIR-r1, then the no-progress stop"
}

case6b() {
  local d="$T/c6b" rc runs
  mkdir -p "$d"
  make_cmd "$d/cmd.txt" raid-walk zul_gurub --server s --slot 2 --holder H6 \
    --sql /tmp/w.sql --out "$d/run0"
  FAKE_NO_TBM=1 FAKE_EXIT=19 FAKE_ARGS_LOG="$d/args.log" \
    "$SUPER_BIN" "$d/cmd.txt" 5 "$d/restart.log" >"$d/stdout.log" 2>"$d/stderr.log"
  rc=$?
  runs="$(runs_done "$d/args.log")"
  (( rc == 19 )) || { bad "case6b exit=$rc want 19"; return; }
  (( runs == 1 )) || { bad "case6b runs=$runs want 1"; return; }
  grep -qF -e 'no TBM:RUN leg' "$d/restart.log" || { bad "case6b log missing no-leg reason"; return; }
  ok "case6b exit-19 run.log without TBM:RUN: no restart, exit 19"
}

# case11 (walk 26c, #370): the placed restart is refused for no quiet move row (exit 2 with a
# `pass --from "step N"` line). The supervisor still stops, but the restarts log names the reason instead of only
# saying "exit 2 is not 18/19".
case11() {
  local d="$T/c11" rc runs
  mkdir -p "$d"
  make_cmd "$d/cmd.txt" raid-walk zul_gurub --server s --slot 2 --holder H11 \
    --sql /tmp/w.sql --out "$d/run0"
  FAKE_EXIT=2 FAKE_ARGS_LOG="$d/args.log" \
    FAKE_STDERR_LINE='raid-walk: --place --from leg high_priestess_jeklik: leg high_priestess_jeklik has no quiet move row before its first pull: pass --from "step N"' \
    "$SUPER_BIN" "$d/cmd.txt" 5 "$d/restart.log" >"$d/stdout.log" 2>"$d/stderr.log"
  rc=$?
  runs="$(runs_done "$d/args.log")"
  (( rc == 2 )) || { bad "case11 exit=$rc want 2"; return; }
  (( runs == 1 )) || { bad "case11 runs=$runs want 1"; return; }
  grep -qF -e 'is not 18/19' "$d/restart.log" || { bad "case11 log missing not-18/19 reason"; return; }
  grep -qF -e 'pass --from "step N"' "$d/restart.log" || { bad "case11 log missing the pass---from reason"; return; }
  grep -qF -e 'pass --from "step N"' "$d/stdout.log" || { bad "case11 stdout missing the refusal"; return; }
  ok "case11 exit 2 with pass --from step N: no restart, exit 2, the reason is logged"
}

# case12 (#370): INT during the step-6 wait stops the supervisor at once: exit
# 130, no second run. The wait is long (30 s) so a pass proves the trap fired
# instead of the sleep running out. FAKE_SEQ: run 1 exits 19 at once, then the
# supervisor sits in the wait when the INT lands.
# Launched with job control on (set -m): a plain `&` job in a non-interactive
# shell inherits SIGINT as ignored, and bash cannot trap an ignored-on-entry
# signal (an `exec bash` wrapper inherits it too). An interactive Ctrl-C reaches
# the foreground supervisor with its trap, so this is a harness detail only.
case12() {
  local d="$T/c12" rc runs start elapsed sup super_path
  mkdir -p "$d"
  make_cmd "$d/cmd.txt" raid-walk zul_gurub --server s --slot 2 --holder H12 \
    --sql /tmp/w.sql --out "$d/run0"
  printf '19\n0\n' >"$d/seq"
  # "$SUPER_BIN" may be a bare filename (mutation copies); resolve it so the
  # launch finds it regardless of cwd.
  case "$SUPER_BIN" in
    */*) super_path="$SUPER_BIN" ;;
    *) super_path="$HERE/$SUPER_BIN" ;;
  esac
  set -m
  FAKE_SEQ_FILE="$d/seq" FAKE_ARGS_LOG="$d/args.log" FAKE_LEG="high_priest_venoxis" \
    WALK_SUPER_WAIT=30 bash "$super_path" "$d/cmd.txt" 5 "$d/restart.log" >"$d/stdout.log" 2>"$d/stderr.log" &
  sup=$!
  set +m
  sleep 2
  kill -INT "$sup"
  start="$SECONDS"
  wait "$sup"
  rc=$?
  elapsed=$((SECONDS - start))
  (( rc == 130 )) || { bad "case12 exit=$rc want 130"; return; }
  (( elapsed < 25 )) || { bad "case12 took ${elapsed}s: the INT trap did not interrupt the 30s wait"; return; }
  runs="$(runs_done "$d/args.log")"
  (( runs == 1 )) || { bad "case12 runs=$runs want 1 (no restart after INT)"; return; }
  ok "case12 INT during the restart wait: exit 130 in ${elapsed}s, no second run"
}

# case13 (#370): TERM during the step-6 wait stops the supervisor at once:
# exit 143, no second run. Same shape as case12, other signal (TERM is not
# ignored on entry, so no job-control launch is needed).
case13() {
  local d="$T/c13" rc runs start elapsed sup
  mkdir -p "$d"
  make_cmd "$d/cmd.txt" raid-walk zul_gurub --server s --slot 2 --holder H13 \
    --sql /tmp/w.sql --out "$d/run0"
  printf '19\n0\n' >"$d/seq"
  FAKE_SEQ_FILE="$d/seq" FAKE_ARGS_LOG="$d/args.log" FAKE_LEG="high_priest_venoxis" \
    WALK_SUPER_WAIT=30 "$SUPER_BIN" "$d/cmd.txt" 5 "$d/restart.log" >"$d/stdout.log" 2>"$d/stderr.log" &
  sup=$!
  sleep 2
  kill -TERM "$sup"
  start="$SECONDS"
  wait "$sup"
  rc=$?
  elapsed=$((SECONDS - start))
  (( rc == 143 )) || { bad "case13 exit=$rc want 143"; return; }
  (( elapsed < 25 )) || { bad "case13 took ${elapsed}s: the TERM trap did not interrupt the 30s wait"; return; }
  runs="$(runs_done "$d/args.log")"
  (( runs == 1 )) || { bad "case13 runs=$runs want 1 (no restart after TERM)"; return; }
  ok "case13 TERM during the restart wait: exit 143 in ${elapsed}s, no second run"
}

# case14 (#370): INT during a run stops the supervisor at once: exit 130, the
# INT-ignoring walk child is gone (the handler escalates to TERM), one run
# only. FAKE_SLEEP holds the fake inside run 1 when the INT lands. Job control
# on for the launch, as in case12.
case14() {
  local d="$T/c14" rc runs sup walk super_path
  mkdir -p "$d"
  make_cmd "$d/cmd.txt" raid-walk zul_gurub --server s --slot 2 --holder H14 \
    --sql /tmp/w.sql --out "$d/run0"
  case "$SUPER_BIN" in
    */*) super_path="$SUPER_BIN" ;;
    *) super_path="$HERE/$SUPER_BIN" ;;
  esac
  set -m
  FAKE_EXIT=19 FAKE_SLEEP=30 FAKE_IGNORE_INT=1 FAKE_ARGS_LOG="$d/args.log" FAKE_LEG="high_priest_venoxis" \
    WALK_SUPER_WAIT=1 bash "$super_path" "$d/cmd.txt" 5 "$d/restart.log" >"$d/stdout.log" 2>"$d/stderr.log" &
  sup=$!
  set +m
  sleep 2
  walk="$(pgrep -f "fake-walk raid-walk.*H14" | head -n 1)"
  [[ -n "$walk" ]] || { bad "case14 setup: no running fake walk found"; kill -9 "$sup" 2>/dev/null; wait "$sup" 2>/dev/null; return; }
  kill -INT "$sup"
  wait "$sup"
  rc=$?
  (( rc == 130 )) || { bad "case14 exit=$rc want 130"; kill -9 "$walk" 2>/dev/null; return; }
  sleep 2
  if kill -0 "$walk" 2>/dev/null; then
    bad "case14 walk $walk still alive after INT (handler must forward and escalate)"
    kill -9 "$walk" 2>/dev/null
    return
  fi
  runs="$(runs_done "$d/args.log")"
  (( runs == 1 )) || { bad "case14 runs=$runs want 1 (no restart after INT)"; return; }
  ok "case14 INT during a run: exit 130, INT-ignoring walk dead, no second run"
}

# case15 (#370): TERM during a run stops the supervisor at once: exit 143, one
# run only. Same shape as case14 with FAKE_IGNORE_INT unset (TERM needs no
# escalation), other signal. $! is the supervisor itself (no wrapper).
case15() {
  local d="$T/c15" rc runs sup walk
  mkdir -p "$d"
  make_cmd "$d/cmd.txt" raid-walk zul_gurub --server s --slot 2 --holder H15 \
    --sql /tmp/w.sql --out "$d/run0"
  set -m
  FAKE_EXIT=19 FAKE_SLEEP=30 FAKE_ARGS_LOG="$d/args.log" FAKE_LEG="high_priest_venoxis" \
    WALK_SUPER_WAIT=1 bash "$SUPER_BIN" "$d/cmd.txt" 5 "$d/restart.log" >"$d/stdout.log" 2>"$d/stderr.log" &
  sup=$!
  set +m
  sleep 2
  walk="$(pgrep -f "fake-walk raid-walk.*H15" | head -n 1)"
  [[ -n "$walk" ]] || { bad "case15 setup: no running fake walk found"; kill -9 "$sup" 2>/dev/null; wait "$sup" 2>/dev/null; return; }
  kill -TERM "$sup"
  wait "$sup"
  rc=$?
  (( rc == 143 )) || { bad "case15 exit=$rc want 143"; kill -9 "$walk" 2>/dev/null; return; }
  sleep 2
  if kill -0 "$walk" 2>/dev/null; then
    bad "case15 walk $walk still alive after TERM (handler must forward)"
    kill -9 "$walk" 2>/dev/null
    return
  fi
  runs="$(runs_done "$d/args.log")"
  (( runs == 1 )) || { bad "case15 runs=$runs want 1 (no restart after TERM)"; return; }
  ok "case15 TERM during a run: exit 143, walk dead, no second run"
}

case7() {
  local d="$T/c7" rc runs d2="$T/c7b" rc2 runs2
  mkdir -p "$d"
  # Walk 25's sequence: run 1 dies at 204, the restart at 202, the next at 202 again. The stop comes at run 2 (202 is
  # behind run 1's 204), not after a third run.
  make_cmd "$d/cmd.txt" raid-walk zul_gurub --server s --slot 2 --holder H7 \
    --sql /tmp/w.sql --out "$d/run0"
  printf '19\n19\n19\n' >"$d/seq"
  printf '204\n202\n202\n' >"$d/steps"
  FAKE_SEQ_FILE="$d/seq" FAKE_STEP_SEQ_FILE="$d/steps" FAKE_ARGS_LOG="$d/args.log" FAKE_LEG="high_priest_venoxis" \
    FAKE_SUMMARY="$WIPE_SUMMARY" \
    "$SUPER_BIN" "$d/cmd.txt" 5 "$d/restart.log" >"$d/stdout.log" 2>"$d/stderr.log"
  rc=$?
  runs="$(runs_done "$d/args.log")"
  (( rc == 19 )) || { bad "case7 exit=$rc want 19"; return; }
  (( runs == 2 )) || { bad "case7 runs=$runs want 2 (a restart that dies no further on is pointless)"; return; }
  grep -qF -e 'no progress' "$d/restart.log" || { bad "case7 log missing the no-progress reason"; return; }
  grep -qF -e 'run 2 died at step 202, run 1 at step 204, same leg high_priest_venoxis' "$d/restart.log" ||
    { bad "case7 log missing the two steps and the leg"; return; }
  # The "at or before" boundary: a restart that dies at exactly the step again. This half is what tells the rule's
  # `-le` from mutation2's `-lt` (a die-earlier pair stops under either).
  mkdir -p "$d2"
  make_cmd "$d2/cmd.txt" raid-walk zul_gurub --server s --slot 2 --holder H7 --sql /tmp/w.sql --out "$d2/run0"
  printf '19\n19\n19\n' >"$d2/seq"
  printf '204\n204\n202\n' >"$d2/steps"
  FAKE_SEQ_FILE="$d2/seq" FAKE_STEP_SEQ_FILE="$d2/steps" FAKE_ARGS_LOG="$d2/args.log" FAKE_LEG="high_priest_venoxis" \
    FAKE_SUMMARY="$WIPE_SUMMARY" \
    "$SUPER_BIN" "$d2/cmd.txt" 5 "$d2/restart.log" >"$d2/stdout.log" 2>"$d2/stderr.log"
  rc2=$?
  runs2="$(runs_done "$d2/args.log")"
  (( rc2 == 19 )) || { bad "case7 same-step exit=$rc2 want 19"; return; }
  (( runs2 == 2 )) || { bad "case7 same-step runs=$runs2 want 2 (dying at the step again is no progress)"; return; }
  grep -qF -e 'run 2 died at step 204, run 1 at step 204, same leg high_priest_venoxis' "$d2/restart.log" ||
    { bad "case7 same-step log missing the equal steps and the leg"; return; }
  ok "case7 exit-19 x3 at 204/202/202 and at 204/204/202, same leg: 2 runs, exit 19, no-progress stop"
}

case8() {
  local d="$T/c8" rc runs
  mkdir -p "$d"
  make_cmd "$d/cmd.txt" raid-walk zul_gurub --server s --slot 2 --holder H8 \
    --sql /tmp/w.sql --out "$d/run0"
  printf '19\n19\n0\n' >"$d/seq"
  printf '204\n210\n260\n' >"$d/steps"
  FAKE_SEQ_FILE="$d/seq" FAKE_STEP_SEQ_FILE="$d/steps" FAKE_ARGS_LOG="$d/args.log" FAKE_LEG="high_priest_venoxis" \
    "$SUPER_BIN" "$d/cmd.txt" 5 "$d/restart.log" >"$d/stdout.log" 2>"$d/stderr.log"
  rc=$?
  runs="$(runs_done "$d/args.log")"
  (( rc == 0 )) || { bad "case8 exit=$rc want 0"; return; }
  (( runs == 3 )) || { bad "case8 runs=$runs want 3 (the step advanced, so the restart goes on)"; return; }
  grep -qF -e 'no progress' "$d/restart.log" && { bad "case8 stopped for no progress though the step advanced"; return; }
  ok "case8 exit-19 19 0 at 204/210, same leg: 3 runs, exit 0"
}

case9() {
  local d="$T/c9" rc runs
  mkdir -p "$d"
  make_cmd "$d/cmd.txt" raid-walk zul_gurub --server s --slot 2 --holder H9 \
    --sql /tmp/w.sql --out "$d/run0"
  printf '19\n19\n0\n' >"$d/seq"
  printf '202\n202\n202\n' >"$d/steps"
  printf 'high_priest_venoxis\nhigh_priest_thekal\nhigh_priest_thekal\n' >"$d/legs"
  FAKE_SEQ_FILE="$d/seq" FAKE_STEP_SEQ_FILE="$d/steps" FAKE_LEG_SEQ_FILE="$d/legs" FAKE_ARGS_LOG="$d/args.log" \
    "$SUPER_BIN" "$d/cmd.txt" 5 "$d/restart.log" >"$d/stdout.log" 2>"$d/stderr.log"
  rc=$?
  runs="$(runs_done "$d/args.log")"
  (( rc == 0 )) || { bad "case9 exit=$rc want 0"; return; }
  (( runs == 3 )) || { bad "case9 runs=$runs want 3 (another leg is progress)"; return; }
  grep -qF -e 'no progress' "$d/restart.log" && { bad "case9 stopped for no progress though the leg changed"; return; }
  ok "case9 same death step 202, another leg: the restart goes on (3 runs, exit 0)"
}

# case10 (review round 1, L4): the same leg and the same death step on every run, but no run wiped, so the
# no-progress stop has no evidence and must not fire. Half two is the same run with no summary.json at all.
case10() {
  local d="$T/c10" rc runs d2="$T/c10b" rc2 runs2
  mkdir -p "$d"
  make_cmd "$d/cmd.txt" raid-walk zul_gurub --server s --slot 2 --holder H10 \
    --sql /tmp/w.sql --out "$d/run0"
  printf '19\n19\n0\n' >"$d/seq"
  printf '41\n41\n41\n' >"$d/steps"
  FAKE_SEQ_FILE="$d/seq" FAKE_STEP_SEQ_FILE="$d/steps" FAKE_ARGS_LOG="$d/args.log" FAKE_LEG="high_priest_venoxis" \
    FAKE_SUMMARY="$NO_WIPE_SUMMARY" \
    "$SUPER_BIN" "$d/cmd.txt" 5 "$d/restart.log" >"$d/stdout.log" 2>"$d/stderr.log"
  rc=$?
  runs="$(runs_done "$d/args.log")"
  (( rc == 0 )) || { bad "case10 exit=$rc want 0"; return; }
  (( runs == 3 )) || { bad "case10 runs=$runs want 3 (a run that did not wipe is no no-progress evidence)"; return; }
  grep -qF -e 'no progress' "$d/restart.log" && { bad "case10 stopped for no progress though no run wiped"; return; }
  mkdir -p "$d2"
  make_cmd "$d2/cmd.txt" raid-walk zul_gurub --server s --slot 2 --holder H10 --sql /tmp/w.sql --out "$d2/run0"
  printf '19\n19\n0\n' >"$d2/seq"
  printf '41\n41\n41\n' >"$d2/steps"
  FAKE_SEQ_FILE="$d2/seq" FAKE_STEP_SEQ_FILE="$d2/steps" FAKE_ARGS_LOG="$d2/args.log" FAKE_LEG="high_priest_venoxis" \
    "$SUPER_BIN" "$d2/cmd.txt" 5 "$d2/restart.log" >"$d2/stdout.log" 2>"$d2/stderr.log"
  rc2=$?
  runs2="$(runs_done "$d2/args.log")"
  (( rc2 == 0 )) || { bad "case10 no-summary exit=$rc2 want 0"; return; }
  (( runs2 == 3 )) || { bad "case10 no-summary runs=$runs2 want 3 (a missing summary claims no wipe)"; return; }
  grep -qF -e 'no progress' "$d2/restart.log" && { bad "case10 no-summary stopped for no progress"; return; }
  ok "case10 exit-19 x2 at step 41 with no wipe (and with no summary): 3 runs, no no-progress stop"
}

mutation() {
  local d="$T/mut" mrc
  mkdir -p "$d"
  # Break step 2 in a COPY (original untouched): the old --from is kept,
  # so run 2 ends up with two --from flags.
  sed 's/^\( *\)--from)$/\1--from-KEPT)/' "$HERE/walk-super.sh" >"$d/walk-super-mut.sh"
  chmod +x "$d/walk-super-mut.sh"
  grep -q -F -e '--from-KEPT)' "$d/walk-super-mut.sh" || { bad "mutation setup: branch not renamed"; return; }
  grep -q -F -e '      --from)' "$d/walk-super-mut.sh" && { bad "mutation setup: original branch still present"; return; }
  WALK_SUPER_BIN="$d/walk-super-mut.sh" bash "$0" 2 >"$d/mut-out.log" 2>&1
  mrc=$?
  (( mrc != 0 )) || { bad "mutation: case2 passed with broken step 2 (want FAIL)"; return; }
  grep -q -F -e 'case2 run2 --from count=2 want 1' "$d/mut-out.log" || { bad "mutation: case2 did not fail on --from count"; return; }
  ok "mutation: keeping the old --from makes case2 fail (--from count=2); original restored (copy only)"
}

mutation2() {
  local d="$T/mut2" mrc
  mkdir -p "$d"
  # Break the no-progress guard in a COPY (original untouched): `-le` becomes `-lt`, so a run that dies at the SAME
  # step as the run before it is no longer "no progress" and the super restarts into another fresh copy.
  sed 's/"\$step" -le "\$prev_step"/"$step" -lt "$prev_step"/' "$HERE/walk-super.sh" >"$d/walk-super-mut2.sh"
  chmod +x "$d/walk-super-mut2.sh"
  grep -qF -e '"$step" -lt "$prev_step"' "$d/walk-super-mut2.sh" || { bad "mutation2 setup: guard not changed"; return; }
  grep -qF -e '"$step" -le "$prev_step"' "$d/walk-super-mut2.sh" && { bad "mutation2 setup: original guard still present"; return; }
  WALK_SUPER_BIN="$d/walk-super-mut2.sh" bash "$0" 7 >"$d/mut2-out.log" 2>&1
  mrc=$?
  (( mrc != 0 )) || { bad "mutation2: case7 passed with -lt (want FAIL)"; return; }
  grep -qF -e 'FAIL case7' "$d/mut2-out.log" || { bad "mutation2: case7 did not fail"; return; }
  ok "mutation2: -lt instead of -le restarts at an equal step, so case7 fails; original restored (copy only)"
}

main() {
  write_fake
  local run_all=1 only=" $* "
  if (( $# > 0 )); then run_all=0; fi
  if (( run_all )) || [[ "$only" == *" 1 "* ]]; then case1; fi
  if (( run_all )) || [[ "$only" == *" 2 "* ]]; then case2; fi
  if (( run_all )) || [[ "$only" == *" 3 "* ]]; then case3; fi
  if (( run_all )) || [[ "$only" == *" 4 "* ]]; then case4; fi
  if (( run_all )) || [[ "$only" == *" 5 "* ]]; then case5; fi
  if (( run_all )) || [[ "$only" == *" 6 "* || "$only" == *" 6a "* ]]; then case6a; fi
  if (( run_all )) || [[ "$only" == *" 6 "* || "$only" == *" 6b "* ]]; then case6b; fi
  if (( run_all )) || [[ "$only" == *" 7 "* ]]; then case7; fi
  if (( run_all )) || [[ "$only" == *" 8 "* ]]; then case8; fi
  if (( run_all )) || [[ "$only" == *" 9 "* ]]; then case9; fi
  if (( run_all )) || [[ "$only" == *" 10 "* ]]; then case10; fi
  if (( run_all )) || [[ "$only" == *" 11 "* ]]; then case11; fi
  if (( run_all )) || [[ "$only" == *" 12 "* ]]; then case12; fi
  if (( run_all )) || [[ "$only" == *" 13 "* ]]; then case13; fi
  if (( run_all )) || [[ "$only" == *" 14 "* ]]; then case14; fi
  if (( run_all )) || [[ "$only" == *" 15 "* ]]; then case15; fi
  if (( run_all )); then mutation; mutation2; fi
  printf -- '----\nPASS=%d FAIL=%d\n' "$PASS" "$FAIL"
  (( FAIL == 0 ))
}

main "$@"
