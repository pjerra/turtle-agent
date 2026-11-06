---
type: Reference
title: raid-autopilot reference
description: Every autopilot flag, the targets.txt format, STOP and YIELD, status.txt, the checkpoint and the exit codes.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/raid_autopilot_cmd.go
tags: [raids, autopilot, reference]
sources:
  - id: auto-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/raid_autopilot_cmd.go
  - id: autopilot
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/raidwalk/autopilot.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:50:00+01:00
status: draft
---

# `raid-autopilot` reference

Usage: `turtle-agent raid-autopilot [--foreground] [--binary PATH] [--poll D] [--stall D] [--settle D] [--status-every D] [--instance-cap-per-account N] [--dry-run] <walk-dir>`.[^auto-cmd]

## Flags

| Flag | Default | What it does | Example |
|---|---|---|---|
| `--foreground` | off | Run in this process instead of re-execd detached. | `--foreground` |
| `--binary` | this executable | The turtle-agent binary the `raid-walk`, `teardown` and `lease list` children run. | `--binary ./turtle-agent` |
| `--poll` | `10s` | How often the runs are read. Must be positive. | `--poll 5s` |
| `--stall` | `15m` | No step or boss-HP change for this long re-places the run. | `--stall 30m` |
| `--settle` | `20s` | Wait after a teardown before the next launch. | `--settle 60s` |
| `--status-every` | `1m` | How often `status.txt` is written. | `--status-every 30s` |
| `--instance-cap-per-account` | `5` | Passed to every `raid-walk`. Without the flag each walk resolves the server's entry in `instance-cap-per-account.json`, then the stock 5. | `--instance-cap-per-account 30` |
| `--dry-run` | off | Validate the targets and print the first command per goal; launch nothing. | `--dry-run` |

## targets.txt

`<walk-dir>/targets.txt`, re-read while the supervisor runs (when its mtime or size changes, every tick): an added id is queued, a removed id is stopped cleanly, an id whose goal lines changed is stopped and re-added. Format:[^autopilot]

```text
# id   server    slot holder  goal         -- raid-walk args...
<id-a> <server>  1    <holder> full        -- <plan> --dir <dir> --hires <file> --sql <file>
<id-b> <server>  2    <holder> boss:<entry> -- <plan> --dir <dir> --hires <file> --sql <file> --from step <n>
```

- Blank lines and `#` lines are skipped. The header is exactly `id server slot holder goal`, then `--`, then the raid-walk args starting with the plan.
- `goal` is one token: `full`, `leg:<L>` or `boss:<ENTRY>` (a creature entry number after the prefix).
- `--sql FILE` is required on every line: every goal may be re-placed. `--from` in a line is the goal's own start.
- Managed and dropped if present: `--server`, `--slot`, `--holder`, `--until`, `--from`, `--place`, `--reuse-instance`, `--out`.
- Slot claims go by state, not file order: an id that is done, stopped or escalated with no live run holds no slot, so a new line may name a finished or escalated id's slot (an escalated id whose teardown never verified keeps its run and so keeps the slot). Every other known id (running, in a restart wait, refused, queued) claims its slots first, then new lines take what is left, so a new line inserted above a running id's line is the one skipped. An id is claimed as a unit (accepted only if every slot it names is free or already its own). The slot a live state holds now stays that id's, and an id whose lines changed keeps only the slot its state holds now. The start applies the same rule from the checkpoint, so a file the running autopilot accepted is accepted after a restart too.
- One bad line does not reject the file: it is skipped with an `invalid targets line skipped` event (logged once), every other line applies, and the bad line's id keeps its old state whole. A skipped line is read again whenever some id's state changed, so one skipped for a slot a running id still holds starts by itself when that id finishes. An unreadable file, or one with no valid line, keeps the targets already loaded. The first read at start is strict: any bad line refuses to start. A typo in a line's id field makes the real id vanish from the read (it is stopped as removed) and starts a new id on the same slot: check `EVENTS.txt` after an edit.
- Refused: slot 3, `--instance-cap-unsafe`, `--inject`, two ids on the same server and slot, a `--gm-instance-data` value with nothing after it.
- A line holds bare words only, never quotes: the line is split on whitespace with no quote handling, so write `--from step <n>` (two words) and `--gm-instance-data <leg>:<a> <b>[;<a> <b>...]` as bare words (for example `--gm-instance-data hakkar:1 3;6 3;0 0`); the words after that flag are joined back into one value.

## STOP and YIELD

`STOP-<id>` (checked every tick) stops just that raid the clean way — its run stopped, its slot torn down and verified — and the id is marked stopped and never relaunched. Once acted on, the file is renamed `STOP-<id>.done`, so a re-added id of the same name starts instead of meeting the old order. A stale `.done` file is never acted on.

`YIELD` (checked at least once a second) tears every run down, releases every lease, writes `YIELD-DONE` and exits 0. SIGINT/SIGTERM does the same without `YIELD-DONE`. A stopped id is not a met goal: when it is the last one, the supervisor ends with `not every target's goals are met ...` and exit 0.

## status.txt and the checkpoint

`status.txt` is written at start and every `--status-every`: one id per line with its goal, attempt, out dir, state, step and reuse, or one `ESCALATE <kind> <id> <slot>: <detail>` line for an escalated id. `EVENTS.txt` gets one line per action (UTC, id, action, reason). `<id>-<n>.out` is the walk's own stdout/stderr; `autopilot.log` is the detached supervisor's log.

`autopilot-state.json` is rewritten atomically on every transition: per id its current goal, the goals met, the last reached step, the reuse/fresh decision, the attempt number, the child's pid and out dir. A start loads it and reconciles it with the attempts' `summary.json` and the slots' leases: a goal the last run met before a crash is advanced, not walked again; a live walk of ours on its slot is adopted and watched as the current run. A checkpoint that does not parse refuses the start with exit 2 and is never overwritten.

## Keep-running rules

On any end the autopilot re-places: the same instance first, a fresh instance only when that placement failed. A wipe at a boss (exit 19 with the run's last row a boss row or a hold row, or the row before it) takes a fresh instance at once, for a goal whose line carries `--gm-instance-data` (the only goals that can re-apply the boss's prerequisites in the new copy); the count of wipes in a row without progress is kept in the checkpoint, resets on progress, and escalates at 3. Every other goal keeps the same instance. A restart inside a goal starts at the furthest step the run reached; when that row is the goal's boss or one of its boss-approach rows, the restart resumes from the goal's own start (the row before the boss stands inside his reach and the placement pulls him), and any other row `raid-walk` refuses as a place start (a boss row, a hold row, a row inside an event) steps back to the nearest earlier row of the same leg the walk takes; both log a `resume:` line, and exit 2 for a refused resume start is never escalated. A legitimate instance-cap wait (the run's last line names the earliest free time) is not a stall.

Six consecutive hire-pool waits (about 30 minutes: another raid of the server holds the shared pool's mercenaries) escalate the id with `hire pool: 6 waits`. Three consecutive placement failures on one id escalate it. Three consecutive launch failures escalate it too.

Restarts wait out a backoff that grows with consecutive no-progress attempts: 20 s, 1 m, 2 m, 5 m, then 10 m for every further one. The count resets when an attempt reaches a further step. A hire-pool wait is neither progress nor failure: the pool refused the hires before the run started, so it leaves the counts alone. A fresh-instance restart (a placement that failed on the old bind) counts its own streak apart.[^autopilot]

Teardown is verified, not assumed: the walk process (and its group, which catches the login child a stopped walk left behind) must be gone, and `lease list` must show the lease released. The first attempt is a plain teardown; when it failed or the lease is kept, a second one runs with `--release-anyway`. A failed teardown keeps the run active on the same out dir with no `YIELD` line: the next tick retries it, and the third failure escalates the slot and leaves it alone. An unverifiable lease counts as held: a second supervisor must never place on a slot whose lease state is unknown. The supervisor stops walks with SIGTERM to the walk's process group, never with `.bot run stop`.[^auto-cmd]

The checkpoint is written atomically (temp file, fsync, dir fsync, rename): a crash leaves the previous file. A start reconciles it with the attempts' summaries and the slots' leases: a goal the last run met before the crash advances and is never walked again; a live walk of ours on the slot is adopted and watched as the current run; a slot whose live walk or lease is not ours refuses the id with an `ESCALATE` line instead of launching. A checkpoint that does not parse refuses the start and is never overwritten.[^auto-cmd]

A removed targets line drops only an idle, done state: an escalated or refused id stays in `status.txt` (and in the exit code), and so does a state whose slot is still leased. An invalid file keeps the loaded targets with one warning; a half-written file is never a stop order. Attempt outputs (`<id>-<n>.out`) belong to their id: they are watched, never treated as new ids, and a leftover lease on the slot refuses the id instead of launching beside it.[^auto-cmd]

## Exit codes

0: every target's goals are met — or a clean YIELD, or a finish with only done, stopped or refused ids (no escalated ones). 2: bad flags, targets or startup (including a second start on the same walk dir, which the lock refuses). 31: at least one id was escalated for a person. The walks' own exits drive the restart decision: 0 any end, 2 refused arguments (escalate, no retry), 29 placement failed every try (retried once per goal in a fresh instance, kept in the checkpoint and reset when the goal advances; a second 29 escalates), 15 lease held (third consecutive escalates), 26 cap wait (restarts later after the cap wait, no backoff), anything else a backoff restart of 20s, 1m, 2m, 5m then 10m per consecutive attempt that reached no further step.
