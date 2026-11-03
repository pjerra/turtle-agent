#!/usr/bin/env bash
#
# walk-super.sh -- restart a dead raid walk at the leg it died on.
#
# Usage: walk-super.sh <cmd-file> <max-restarts> <restart-log>
#
# <cmd-file>: one line, the full `turtle-agent raid-walk ...` command
#   (see tests/raid-zg-walk23/cmd-*.txt). Its --out DIR names the first run's folder.
#   Read as words, never eval'd: plain words and "double-quoted" words only. $, backticks, backslashes, ;, &, |,
#   <, >, parentheses, quotes other than double, globs, ~, # and a second line are refused (exit 2, nothing runs).
# Runs the command, waits for it, reads its exit code. The run's own stdout+stderr pass through to ours (so the
# operator sees the refusal) via a pipe to `tee`; the backgrounded pipeline's exit-status file reports the walk's
# own code (`wait` on a background pipeline only reports tee's, so PIPESTATUS is unavailable there).
# Signals stop the supervisor at once, during a run and during the step-6 wait alike (#370): INT removes the temp
# dir, forwards the signal to the running child walk (and the wait-sleep, and tee so the pipe drains), and exits
# 130; TERM does the same and exits 143. EXIT only removes the temp dir. Backgrounded children ignore SIGINT with
# job control off, so the INT handler escalates to TERM after forwarding INT: no ghost walk survives us.
# An exit-2 line naming `pass --from "step N"` is also written into the restarts log (walk 26c: the placed Jeklik
# restart was refused for no quiet move row, and the log only said "exit 2 is not 18/19").
#   exit 0, or any code other than 18/19: stop, log, exit with that code.
#   no exit status (the rc file is missing or not a number: the wrapper died first): exit 70, logged
#     "status unknown", stop. It is a failure, so it restarts nothing.
#   exit 18 (paused) or 19 (incident), restarts left: re-read the cmd file,
#     rebuild the next command for the last point it reached, wait, run it again.
#   restart count reached: stop, exit with that code.
#
# The cmd file is re-read before EVERY run (startup and each restart), so a binary or flag change applies to the next
# run without a relaunch (walk 32: the supervisor read the file once, so the operator had to relaunch by hand).
#
# Rebuilding the restart command:
#   1. Leg = field 5 of the last `TBM:RUN|...` line in <out>/run.log
#      (TBM:RUN|plan|state|step/total|leg|kind|...). No run.log: stop (no
#      restart), log why. No leg: step 8 (an exit-18 --place run is retried
#      as-is) or stop.
#   2. Start = the furthest MOVE row the run's statuses showed (kind move,
#      move_direct or move_hop_exact: field 4's step), or the --from step the
#      run was LAUNCHED with when that is further; the leg's start only when
#      the log shows neither. Walk 32: every restart went back to the leg
#      start, re-hit the water wall at step 236 and burned an instance.
#      Drop any existing --from / --place, add --from "<start>" --place.
#   3. --place needs --sql FILE: no --sql, stop (no restart),
#      log "no --sql, cannot place".
#   4. --out DIR -> DIR-r<n> (n = 1, 2, ...). --holder is unchanged.
#   5. --until is kept.
#   6. Wait 20 s before each restart (the server finishes the old run's stop).
#   7. No-progress stop: a restart whose death step is at or before the run
#      before it, on the same leg, died at the same wall again and a fresh
#      placed copy repeats it (walk 25: four placed restarts, each wiping on
#      the same pack). Only a run that really wiped counts (review round 1,
#      L4): summary.json's `wipes` array non-empty, or its `end_reason` naming
#      a wipe or a refused revive ("not revived"). A run that exited 19 at its
#      start step with no wipe restarts as usual.
#      Stop, log "stop: no progress (run N died at step S,
#      run N-1 at step P, same leg L): a fresh placed instance repeats it" and
#      exit with the run's code. The death step is field 4 of the last
#      `TBM:RUN|...` line ("204/1095" gives 204), read like the leg.
#   8. Empty-leg place retry (walk 26d, #373 fix C): no TBM:RUN leg means the
#      run stopped in the place phase, before the run started. When it exited
#      18 and the command carries --place, retry the SAME command once per
#      restart budget with only --out changed to <base>-r<n>, after the step-6
#      wait. A no-progress guard stops when two consecutive place failures
#      have the same summary.json `place.start_step` and `end_reason`,
#      logging "no progress (place failed before the run started at start
#      step S twice with the same end reason: R)". Exit 19 with no leg keeps
#      today's stop.
#   9. Stuck stop: never re-place into a step that just failed by the same
#      stuck reason twice. The reason is the walk's own last `paused: ...`
#      run.log line with digits folded to #, so the same wall reads the same
#      however far the lead stood. When the restart's start step equals the
#      one the previous restart used and the signature repeats, stop with
#      "no progress (a restart would place step S again: the same stuck
#      reason twice: R)" and the run's exit code, instead of burning another
#      instance (walk 32: 236's water wall, three restarts, three instances).
# Log: one line per finished run, appended to <restart-log> and printed to
# stdout: UTC time, run number, exit code, leg, out, and the new --out and
# start word (restart) or the stop reason.
#
# Test hook: WALK_SUPER_WAIT seconds between runs (default 20).
set -u

(( $# == 3 )) || { echo "usage: walk-super.sh <cmd-file> <max-restarts> <restart-log>" >&2; exit 2; }
cmdfile="$1"
max_restarts="$2"
restart_log="$3"

[[ "$max_restarts" =~ ^[0-9]+$ ]] || { echo "walk-super: <max-restarts> must be a non-negative integer, got '$max_restarts'" >&2; exit 2; }
[[ -f "$cmdfile" ]] || { echo "walk-super: cmd file not found: $cmdfile" >&2; exit 2; }
# The cmd line is split into argv by parse_cmd_words below, never by eval (a `& touch x` or `| touch x` in the file
# used to run through eval). A word is plain (no whitespace, and none of $ ` \ ; & | < > ( ) ' * ? [ ] { } ~ # or
# CR/LF) or double-quoted (no $ ` \ or newline inside; the quotes are dropped). Anything else refuses the file.
refuse_cmd() {
  echo "walk-super: refusing cmd file $cmdfile: $1" >&2
  exit 2
}
# parse_cmd_words SETS CMD_ARGV from the cmd line $1. It runs no shell code and expands nothing.
parse_cmd_words() {
  local s="$1" n i c word="" inword=0 closed=0
  CMD_ARGV=()
  n=${#s}
  i=0
  while (( i < n )); do
    c="${s:i:1}"
    if [[ "$c" == " " || "$c" == $'\t' ]]; then
      if (( inword )); then CMD_ARGV+=("$word"); word=""; inword=0; fi
      i=$((i + 1))
    elif [[ "$c" == '"' ]]; then
      inword=1
      closed=0
      i=$((i + 1))
      while (( i < n )); do
        c="${s:i:1}"
        i=$((i + 1))
        if [[ "$c" == '"' ]]; then closed=1; break; fi
        case "$c" in
          '$' | '`' | '\' | $'\n' | $'\r')
            refuse_cmd "$(printf 'character %q inside a double-quoted word at column %d' "$c" "$i")" ;;
        esac
        word+="$c"
      done
      (( closed )) || refuse_cmd "unterminated double quote"
    else
      case "$c" in
        '$' | '`' | ';' | '&' | '|' | '<' | '>' | '(' | ')' | '\' | "'" | '*' | '?' | '[' | ']' | '{' | '}' | '~' | '#' | $'\n' | $'\r')
          refuse_cmd "$(printf 'character %q outside quotes at column %d' "$c" "$((i + 1))")" ;;
      esac
      inword=1
      word+="$c"
      i=$((i + 1))
    fi
  done
  if (( inword )); then CMD_ARGV+=("$word"); fi
  return 0
}

# read_cmd reads <cmdfile> afresh into ARGS. It runs at startup and again before EVERY restart, so a binary or flag
# change in the file applies to the next run (walk 32: the operator had to relaunch by hand because only the first
# command was read). It runs no shell code: parse_cmd_words rejects anything but plain and double-quoted words.
read_cmd() {
  local text
  text="$(cat -- "$cmdfile")"
  [[ "$text" != *$'\n'* ]] || refuse_cmd "more than one line"
  parse_cmd_words "$text"
  (( ${#CMD_ARGV[@]} > 0 )) || { echo "walk-super: empty command in $cmdfile" >&2; exit 2; }
  ARGS=("${CMD_ARGV[@]}")
}
read_cmd

wait_secs="${WALK_SUPER_WAIT:-20}"
mkdir -p -- "$(dirname -- "$restart_log")"
walkpid=""
pipejob=""
waitpid=""
run_dir=""
rc_file=""

# Signal handling (#370): Ctrl-C / SIGTERM stop the supervisor at once, during
# a run and during the step-6 wait alike. EXIT only removes the temp dir.
# INT removes it too, forwards the signal to the running child walk (its pid),
# the wait-sleep and tee so the pipe drains, then exits 130; TERM does the
# same and exits 143. The run pipeline waits in the background (see the loop),
# so these traps fire at once instead of after the foreground child exits.
# Backgrounded pipeline children ignore SIGINT with job control off, so the
# INT handler escalates to TERM on the walk and tee after forwarding INT:
# no ghost walk survives us.
cleanup_tmp() {
  [[ -n "${run_dir:-}" ]] && rm -rf -- "$run_dir"
}
child_pids() {
  local pids="" j
  [[ -n "${walkpid:-}" ]] && pids="$pids $walkpid"
  [[ -n "${pipejob:-}" ]] && pids="$pids $pipejob"
  [[ -n "${waitpid:-}" ]] && pids="$pids $waitpid"
  j="$(jobs -p 2>/dev/null || true)"
  [[ -n "$j" ]] && pids="$pids $j"
  printf '%s' "$pids"
}
on_int() {
  local pids
  pids="$(child_pids)"
  # shellcheck disable=SC2086
  kill -INT $pids 2>/dev/null || true
  # `sleep` (and backgrounded pipeline children generally) ignore SIGINT with
  # job control off, so escalate: TERM the whole subtree — waitpid's sleep,
  # the inner walk shell's children (pkill -P), the inner shell, and tee —
  # then reap. Without this the trailing `wait` stalls until the sleep or
  # walk exits on its own (a wait-phase INT took the full WALK_SUPER_WAIT).
  [[ -n "${walkpid:-}" ]] && pkill -TERM -P "$walkpid" 2>/dev/null || true
  [[ -n "${pids:-}" ]] && kill -TERM $pids 2>/dev/null || true
  wait 2>/dev/null || true
  [[ -n "${run_dir:-}" ]] && rm -rf -- "$run_dir"
  exit 130
}
on_term() {
  local pids
  pids="$(child_pids)"
  # shellcheck disable=SC2086
  [[ -n "${walkpid:-}" ]] && pkill -TERM -P "$walkpid" 2>/dev/null || true
  [[ -n "${pids:-}" ]] && kill -TERM $pids 2>/dev/null || true
  wait 2>/dev/null || true
  [[ -n "${run_dir:-}" ]] && rm -rf -- "$run_dir"
  exit 143
}
trap cleanup_tmp EXIT
trap on_int INT
trap on_term TERM


log_event() {
  local line
  line="$(date -u '+%Y-%m-%dT%H:%M:%SZ') $1"
  printf '%s\n' "$line" >>"$restart_log"
  printf '%s\n' "$line"
}

# reload_cmd re-reads the cmd file before a restart (a binary or flag change applies). A file that vanished stops the
# supervisor with a line in the restarts log; a file that is refused stops with exit 2 and its reason on stderr.
reload_cmd() {
  if [[ ! -f "$cmdfile" ]]; then
    log_event "run $run_no exit $rc leg ${leg:-<none>} out ${out:-<none>} stop: the cmd file is gone ($cmdfile)"
    exit 2
  fi
  read_cmd
}

opt_value() {
  local name="$1" i a
  for (( i = 0; i < ${#ARGS[@]}; i++ )); do
    a="${ARGS[$i]}"
    if [[ "$a" == "--$name" ]]; then
      printf '%s' "${ARGS[$((i + 1))]:-}"
      return 0
    elif [[ "$a" == "--$name="* ]]; then
      printf '%s' "${a#--$name=}"
      return 0
    fi
  done
  return 1
}

has_flag() {
  local name="$1" a
  for a in "${ARGS[@]}"; do
    if [[ "$a" == "--$name" || "$a" == "--$name="* ]]; then
      return 0
    fi
  done
  return 1
}

leg_of_runlog() {
  local runlog="$1" last leg
  last="$(grep -F 'TBM:RUN|' -- "$runlog" 2>/dev/null | tail -n 1)"
  [[ -n "$last" ]] || return 1
  leg="$(printf '%s' "$last" | cut -d'|' -f5 | tr -d '\r' | sed 's/^ *//;s/ *$//')"
  [[ -n "$leg" && "$leg" != "-" ]] || return 1
  printf '%s' "$leg"
}

# step_of_runlog prints the death step: field 4's step of the same last TBM:RUN line ("204/1095" -> 204). Nothing
# when there is no line or the field is not a number: the no-progress rule then has nothing to compare.
step_of_runlog() {
  local runlog="$1" last step
  last="$(grep -F 'TBM:RUN|' -- "$runlog" 2>/dev/null | tail -n 1)"
  [[ -n "$last" ]] || return 1
  step="$(printf '%s' "$last" | cut -d'|' -f4 | tr -d '\r' | sed 's#/.*##;s/^ *//;s/ *$//')"
  [[ "$step" =~ ^[0-9]+$ ]] || return 1
  printf '%s' "$step"
}
# move_step_of_runlog prints the furthest step the run's statuses showed on a MOVE row (kind move, move_direct or
# move_hop_exact): the last placement point it really stood on. A restart goes back there instead of to the leg's
# start (walk 32: restarts re-entered at the leg start, hit the same water wall at step 236 and burned an instance).
# Nothing when no move row was reached.
move_step_of_runlog() {
  local runlog="$1" line kind step best=0
  [[ -f "$runlog" ]] || return 1
  while IFS= read -r line; do
    kind="$(printf '%s' "$line" | cut -d'|' -f6 | tr -d '\r' | sed 's/^ *//;s/ *$//')"
    case "$kind" in
      move | move_direct | move_hop_exact) ;;
      *) continue ;;
    esac
    step="$(printf '%s' "$line" | cut -d'|' -f4 | tr -d '\r' | sed 's#/.*##;s/^ *//;s/ *$//')"
    [[ "$step" =~ ^[0-9]+$ ]] || continue
    (( 10#$step > best )) && best=$((10#$step))
  done < <(grep -F 'TBM:RUN|' -- "$runlog" 2>/dev/null)
  (( best > 0 )) || return 1
  printf '%s' "$best"
}

# stuck_of_run prints what the run died on: its last `paused: ...` note (the walk's own line for the pause it ended
# on). Digits are folded to #, so two runs at the same wall read the same signature and a wall that only moved a yard
# does not look new. Nothing when the run never logged a pause: a wipe or crash end reason is not a stuck reason (the
# wipe rule and the exit codes have their own guards).
stuck_of_run() {
  local out="$1" line
  line="$(grep -F 'paused: ' -- "$out/run.log" 2>/dev/null | tail -n 1)"
  [[ -n "$line" ]] || return 1
  line="${line#*paused: }"
  printf '%s' "$line" | sed -E 's/[0-9]+/#/g'
}

# reason_of_runout prints the placement refusal a stopped run died with: the `pass --from "step N"` line of an
# exit-2 run (walk 26c: Jeklik's leg had no quiet move row). Nothing when the output holds no such line: the stop
# reason stays what it was.
reason_of_runout() {
  local file="$1" line
  [[ -f "$file" ]] || return 1
  line="$(grep -F 'pass --from "step N"' -- "$file" 2>/dev/null | head -n 1 | tr -d '\r')"
  [[ -n "$line" ]] || return 1
  printf '%s' "$line"
}

# wiped_of_summary says the run REALLY wiped: summary.json's `wipes` array is non-empty, or its `end_reason` names a
# wipe or a refused revive ("not revived"). The no-progress stop (step 7) needs that: a run that exited 19 at its
# start step without a wipe is no evidence that a fresh placed copy would repeat the same wall, so it restarts. A
# missing summary claims nothing, and neither does a summary with an empty wipes array and another end reason.
wiped_of_summary() {
  local summary="$1" end
  [[ -f "$summary" ]] || return 1
  # Go writes an empty array as `[]` on one line and a non-empty one across lines (summary.json is MarshalIndent).
  if grep -q '"wipes"' -- "$summary" 2>/dev/null && ! grep -qE '"wipes": *\[\]' -- "$summary" 2>/dev/null; then
    return 0
  fi
  end="$(sed -n 's/.*"end_reason": *"\(.*\)".*/\1/p' -- "$summary" 2>/dev/null | head -n 1)"
  case "$end" in
    *wipe* | *"not revived"*) return 0 ;;
  esac
  return 1
}

# place_fail_of_summary prints the place failure a no-TBM:RUN run died with: summary.json's `place.start_step` and
# its `end_reason`, tab-separated (walk 26d, #373 fix C). Nothing when either is missing: the empty-leg no-progress
# guard (step 8) then has nothing to compare, and the retry goes on until the budget ends it.
place_fail_of_summary() {
  local summary="$1" start end
  [[ -f "$summary" ]] || return 1
  start="$(sed -n 's/.*"start_step": *\([0-9][0-9]*\).*/\1/p' -- "$summary" 2>/dev/null | head -n 1)"
  [[ -n "$start" ]] || return 1
  end="$(sed -n 's/.*"end_reason": *"\(.*\)".*/\1/p' -- "$summary" 2>/dev/null | head -n 1)"
  [[ -n "$end" ]] || return 1
  printf '%s\t%s' "$start" "$end"
}

# rebuild_cmd rebuilds the restart from the freshly read ARGS: --out becomes new_out, every old --from/--place is
# dropped, and from_word ("step N" or "leg L") is the start the next run is placed at.
rebuild_cmd() {
  local new_out="$1" from_word="$2" i a
  local -a rebuilt=()
  i=0
  while (( i < ${#ARGS[@]} )); do
    a="${ARGS[$i]}"
    case "$a" in
      --from)
        i=$((i + 2)) # MUT2: drop old --from and its value
        ;;
      --from=*)
        i=$((i + 1))
        ;;
      --place|--place=*)
        i=$((i + 1))
        ;;
      --out)
        rebuilt+=("$a" "$new_out")
        i=$((i + 2))
        ;;
      --out=*)
        rebuilt+=("--out=$new_out")
        i=$((i + 1))
        ;;
      *)
        rebuilt+=("$a")
        i=$((i + 1))
        ;;
    esac
  done
  rebuilt+=(--from "$from_word" --place)
  ARGS=("${rebuilt[@]}")
}

# swap_out_cmd renames only the restart's output: --out OLD becomes --out NEW, every other arg (including --from and
# --place) stays exactly as the operator wrote it. Walk 26d, #373 fix C: an exit-18 place failure never started, so
# there is no leg to rebuild a --from for; appending one would say `--from "leg "` and a rebuild_cmd call would
# refuse the empty value.
swap_out_cmd() {
  local new_out="$1" i a
  local -a swapped=()
  i=0
  while (( i < ${#ARGS[@]} )); do
    a="${ARGS[$i]}"
    case "$a" in
      --out)
        swapped+=("$a" "$new_out")
        i=$((i + 2))
        ;;
      --out=*)
        swapped+=("--out=$new_out")
        i=$((i + 1))
        ;;
      *)
        swapped+=("$a")
        i=$((i + 1))
        ;;
    esac
  done
  ARGS=("${swapped[@]}")
}

run_no=1
restarts_done=0
base_out=""
rc=0
out=""
leg=""
step=""
prev_leg=""
prev_step=""
status_unknown=0
new_out=""
run_dir=""
run_out=""
run_reason=""
place_fail=""
prev_place_fail=""
place_start=""
launched_from=""
start_step=""
start_word=""
reached=""
stuck=""
prev_restart_step=""
prev_stuck=""

while true; do
	status_unknown=0
	run_dir="$(mktemp -d "${XDG_RUNTIME_DIR:-${TMPDIR:-/tmp}}/walk-super-run.XXXXXX" 2>/dev/null || mktemp -d "${out:-${TMPDIR:-/tmp}}/walk-super-run.XXXXXX")"
	run_out="$run_dir/run.out"
	rc_file="$run_dir/rc"
	# In the background so a trapped INT/TERM fires at once (#370): a
	# foreground pipeline would defer the trap until the child exits. The
	# outer `wait` reports tee's status, so the walk's own code arrives
	# through the rc file instead of PIPESTATUS.
	{ nice -n 10 "${ARGS[@]}" 2>&1; printf '%s' "$?" >"$rc_file"; } | tee "$run_out" &
	pipejob=$!
	walkpid="$(jobs -p | head -n 1)"
	[[ -n "$walkpid" && "$walkpid" != "$pipejob" ]] || walkpid="$pipejob"
	wait "$pipejob"
	# The walk's code is the rc file the wrapper writes last. A missing or malformed file is no status (the wrapper
	# died before writing it): exit 70, "status unknown", and the crash rule applies (no restart).
	rc=70
	rc_text="$(cat -- "$rc_file" 2>/dev/null || true)"
	if [[ "$rc_text" =~ ^(0|[1-9][0-9]*)$ ]]; then
	  rc="$rc_text"
	else
	  status_unknown=1
	fi
	walkpid=""
	pipejob=""
	run_reason="$(reason_of_runout "$run_out" || true)"

  out="$(opt_value out || true)"
  # The --from the run was LAUNCHED with, read before the cmd file is re-read: the restart never goes back further
  # than the point this run was placed at.
  launched_from="$(opt_value from || true)"
  if (( run_no == 1 )); then
    base_out="$out"
  fi

  if (( rc != 18 && rc != 19 )); then
    if (( status_unknown )); then
      log_event "run $run_no exit $rc leg - out ${out:-<none>} stop: status unknown (no exit status from the run: the wrapper died before writing it)"
    elif [[ -n "$run_reason" ]]; then
      log_event "run $run_no exit $rc leg - out ${out:-<none>} stop: exit $rc is not 18/19 ($run_reason)"
    else
      log_event "run $run_no exit $rc leg - out ${out:-<none>} stop: exit $rc is not 18/19"
    fi
    exit "$rc"
  fi

  leg="$(leg_of_runlog "${out:-<none>}/run.log" || true)"

  if (( restarts_done >= max_restarts )); then
    log_event "run $run_no exit $rc leg ${leg:-<none>} out ${out:-<none>} stop: restart count reached ($restarts_done/$max_restarts)"
    exit "$rc"
  fi
  if [[ -z "$out" ]]; then
    log_event "run $run_no exit $rc leg ${leg:-<none>} out <none> stop: no --out in command, cannot find run.log"
    exit "$rc"
  fi
  if [[ ! -f "$out/run.log" ]]; then
    log_event "run $run_no exit $rc leg - out $out stop: no run.log at $out/run.log"
    exit "$rc"
  fi
  if [[ -z "$leg" ]]; then
    # Walk 26d, #373 fix C: a run that stopped in the place phase never started, so its run.log holds no TBM:RUN
    # leg. When it exited 18 and the command carries --place, retry the SAME command (only --out becomes
    # <base>-r<n>): there is no leg to rebuild a --from for. An exit-19 run with no leg keeps today's stop.
    if [[ "$rc" == 18 ]] && has_flag place; then
      place_fail="$(place_fail_of_summary "$out/summary.json" || true)"
      if [[ -n "$place_fail" && "$place_fail" == "$prev_place_fail" ]]; then
        place_start="${place_fail%%$'\t'*}"
        log_event "run $run_no exit $rc leg <none> out $out stop: no progress (place failed before the run started at start step $place_start twice with the same end reason: ${place_fail#*$'\t'})"
        exit "$rc"
      fi
      prev_place_fail="$place_fail"
      prev_leg=""
      prev_step=""
      prev_restart_step=""
      prev_stuck=""
      restarts_done=$((restarts_done + 1))
      new_out="${base_out}-r${restarts_done}"
      log_event "run $run_no exit $rc leg <none> out $out restarting as run $((run_no + 1)) out $new_out (restart $restarts_done/$max_restarts): place failed before the run started (no TBM:RUN), retrying the same command"
      sleep "$wait_secs" &
      waitpid=$!
      wait "$waitpid"
      waitpid=""
      reload_cmd
      swap_out_cmd "$new_out"
      run_no=$((run_no + 1))
      continue
    fi
    log_event "run $run_no exit $rc leg <none> out $out stop: no TBM:RUN leg in $out/run.log"
    exit "$rc"
  fi
  step="$(step_of_runlog "$out/run.log" || true)"

  # Step 7: a restart that dies no further on than the run before it, on the SAME leg, repeats a wall a fresh placed
  # copy cannot pass — but only when the run really wiped (summary.json's wipes, or its end reason naming a wipe or a
  # refused revive). Without a wipe the run is no evidence of a repeated wall, so the restart goes on (review round 1,
  # L4). Stop instead of spending the rest of the restarts on it.
  if (( run_no > 1 )) && wiped_of_summary "$out/summary.json" && [[ -n "$step" && -n "$prev_step" && "$leg" == "$prev_leg" && "$step" -le "$prev_step" ]]; then
    log_event "run $run_no exit $rc leg $leg out $out stop: no progress (run $run_no died at step $step, run $((run_no - 1)) at step $prev_step, same leg $leg): a fresh placed instance repeats it"
    exit "$rc"
  fi
  # The cmd file is re-read HERE, so a binary or flag change applies to the restart (walk 32: walk-super read it once).
  reload_cmd
  if ! has_flag sql; then
    log_event "run $run_no exit $rc leg $leg out $out stop: no --sql, cannot place"
    exit "$rc"
  fi

  # The restart's start: the furthest move row the run really reached, or the --from step it was launched with when
  # that is further; the leg's start only when the log shows neither (walk 32: every restart went back to the leg
  # start, re-hit the water wall at step 236 and burned an instance).
  reached="$(move_step_of_runlog "$out/run.log" || true)"
  start_step=""
  case "$launched_from" in
    step\ [0-9]*) start_step="${launched_from#step }" ;;
  esac
  if [[ -n "$reached" ]] && { [[ -z "$start_step" ]] || (( 10#$reached > 10#$start_step )); }; then
    start_step="$reached"
  fi

  # Never re-place into a step that just failed by the same stuck reason twice (walk 32: 236's water wall, three
  # restarts, three instances). The signature folds digits, so the same wall reads the same however far the lead stood.
  stuck="$(stuck_of_run "$out" || true)"
  if [[ -n "$start_step" && "$start_step" == "$prev_restart_step" && -n "$stuck" && "$stuck" == "$prev_stuck" ]]; then
    log_event "run $run_no exit $rc leg $leg out $out stop: no progress (a restart would place step $start_step again: the same stuck reason twice: $stuck)"
    exit "$rc"
  fi
  prev_restart_step="$start_step"
  prev_stuck="$stuck"

  prev_leg="$leg"
  prev_step="$step"
  prev_place_fail=""
  restarts_done=$((restarts_done + 1))
  new_out="${base_out}-r${restarts_done}"
  if [[ -n "$start_step" ]]; then
    start_word="step $start_step"
  else
    start_word="leg $leg"
  fi
  log_event "run $run_no exit $rc leg $leg out $out restarting as run $((run_no + 1)) out $new_out (restart $restarts_done/$max_restarts) from \"$start_word\""
  sleep "$wait_secs" &
  waitpid=$!
  wait "$waitpid"
  waitpid=""

  rebuild_cmd "$new_out" "$start_word"
  run_no=$((run_no + 1))
done
