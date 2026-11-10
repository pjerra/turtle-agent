---
type: Guide
title: Supervise raids with the autopilot
description: Write targets.txt, run the supervisor, and stop one raid or all of them.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/raid_autopilot_cmd.go
tags: [raids, autopilot]
sources:
  - id: auto-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/raid_autopilot_cmd.go
  - id: autopilot
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/raidwalk/autopilot.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:40:00+01:00
status: draft
---

# Supervise raids with the autopilot

`turtle-agent raid-autopilot <walk-dir>` keeps a walk dir's raids running without a worker watching them. It reads `<walk-dir>/targets.txt`, one goal per line, launches a `raid-walk` for each id, and re-places any walk that ends, stalls or loses progress until every goal is met.[^auto-cmd]

## 1. Write targets.txt

One goal per line. Blank lines and `#` lines are skipped:

```text
# id   server    slot holder  goal         -- raid-walk args...
<id-a> <server>  1    <holder> full        -- <plan> --dir <dir> --hires <file> --sql <file>
<id-b> <server>  2    <holder> boss:<entry> -- <plan> --dir <dir> --hires <file> --sql <file> --from step <n>
<id-c> <server>  5    <holder> leg:<leg>    -- <plan> --dir <dir> --hires <file> --sql <file>
```

The header is exactly `id server slot holder goal`, then `--`, then the raid-walk args starting with the plan. A goal is `full` (walk to the plan's end), `leg:<L>` (`--until "leg L"`) or `boss:<ENTRY>` (`--until boss-done`; done when that boss's kill is recorded). An id may repeat: its lines walk in file order, and a met goal advances to the next line.[^autopilot]

`--sql FILE` is required on every line. `--from` in a line is the goal's own start (a restart with no step reached uses it). These flags are managed and dropped if present: `--server`, `--slot`, `--holder`, `--until`, `--from`, `--place`, `--reuse-instance`, `--out`, `--boss-wipe-max` (the goal's boss-wipe budget, default 3: the ZG Hakkar goals set 5), and `--boss-wipe-fresh` (needs `--from`: after an exit 19 at the boss, relaunch fresh from the goal's own start instead of resuming the step). Slot 3, `--instance-cap-unsafe` and `--inject` are refused before anything runs. Two ids may not name the same server and slot: one slot runs one walk at a time.

## 2. Run the supervisor

```sh
turtle-agent raid-autopilot <walk-dir>
```

By default the launcher re-execs itself detached and returns a pid and log path. `--foreground` runs it in place. `--dry-run` validates `targets.txt` and prints each goal's first command; nothing is launched. `--poll` (default 10s) is how often the runs are read, `--stall` (default 15m) how long without step or boss-HP change before a run is re-placed, `--settle` (default 20s) the wait after a teardown before the next launch, and `--status-every` (default 1m) how often `status.txt` is written.

## 3. Watch the files

The walk dir holds the supervisor's state:

- `EVENTS.txt` — one line per action (UTC, id, action, reason).
- `status.txt` — written at start and every `--status-every`: id, goal, attempt, out dir, state, step, progress age and reuse per id; an escalated id is one `ESCALATE <kind> <id> <slot>: <reason>` line.
- `autopilot-state.json` — the checkpoint: per id its current goal, goals met, last reached step, the reuse/fresh decision, attempt number, child pid and out dir. Missing at start means fresh; a file that does not parse refuses the start with exit 2.
- `<id>-<n>.out` — the walk's own stdout/stderr. `autopilot.log` is the detached supervisor's log.

On any end (any exit code, a used-up restart budget, or a stall) the autopilot stops the walk, tears the slot down, picks a new `--out` (`<walk-dir>/<id>-<n>`) and re-places: the same instance first (`--place --from "step N" --reuse-instance`), a fresh instance only when that placement failed. A wipe at a boss (exit 19 with the run's last row a boss row or a hold row, or the row before it) is the exception for a goal line carrying `--gm-instance-data` or `--boss-wipe-fresh`: it takes a fresh instance at once, up to the goal's boss-wipe budget (default 3: the first two wipes relaunch fresh, the third stops the id; `--boss-wipe-max`; the checkpoint keeps the count and progress resets it). With `--boss-wipe-fresh` the relaunch starts again from the goal's own `--from` (e.g. `leg tunnel`) instead of resuming the reached step. Exit 2 (bad arguments) stops that id with an `ESCALATE` line and no retry. Exit 29 (placement failed every try in the same instance) is retried once per goal in a fresh instance (`--from step N --place` without `--reuse-instance`, logged `fresh-instance retry after exit 29`, usual backoff, counts toward the instance cap); a second 29 on the goal stops the id with an `ESCALATE` line; on an exit 26 (no fresh instance place was free) the autopilot waits out the goal's `--instance-cap-wait` (default 20m) and relaunches a fresh instance, with no backoff and no count toward the wipe or no-progress budgets.

A restart inside a goal starts at the furthest step a run of it reached, else the target's own `--from`, else a fresh start (a restart with no step and no `--from` never sends `--reuse-instance`: the walk would refuse it). When that row is the goal's boss or one of its boss-approach rows, the restart resumes from the goal's own start (the row before the boss stands inside his reach and the placement pulls him); any other row `raid-walk` refuses as a place start (a boss row, a hold row, a row inside an event) steps back to the nearest accepted row of the same leg, else the leg's own start. Both log a `resume:` line — exit 2 for a refused resume start is never escalated. An id whose goal boss is already dead is `DONE` at start and never placed again: the checkpoint's `boss_dead` keeps it across a supervisor restart (a kill counts whatever the exit code, even 19 or a stopped ending, and Thekal's 14509 and 14599 are one boss).[^autopilot]

## 4. Stop one raid

```sh
touch <walk-dir>/STOP-<id>
```

`STOP-<id>` stops just that raid the clean way — its run stopped, its slot torn down and verified — and the id is marked stopped and never relaunched, while the other ids keep running. Once acted on, the file is renamed `STOP-<id>.done`. A stopped id is not a met goal: when it is the last one, the supervisor ends with `not every target's goals are met ...` and exit 0.

## 5. Stop everything

```sh
touch <walk-dir>/YIELD
```

`YIELD` tears every run down, releases every lease, writes `YIELD-DONE` and exits 0. A `YIELD` whose teardown does not verify writes no `YIELD-DONE`. SIGINT/SIGTERM does the same without `YIELD-DONE`.
