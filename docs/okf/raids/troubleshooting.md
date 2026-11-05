---
type: Troubleshooting
title: Troubleshooting raids
description: Real raid-walk, autopilot and suite error messages, what they mean, and the fix.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/raid_walk_cmd.go
tags: [raids, troubleshooting]
sources:
  - id: walk-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/raid_walk_cmd.go
  - id: auto-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/raid_autopilot_cmd.go
  - id: suite-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/raid_suite_cmd.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:55:00+01:00
status: draft
---

# Troubleshooting raids

## `raid-walk never uses slot 3 (owner rule)`

You passed `--slot 3` (or a pair including it). Slot 3 is never used, for every server. Pick another slot. Exit 15.

## `"..." is not a plan name (a-z, 0-9, _ and -)`

The plan is the name the server knows: lowercase letters, digits, `_` and `-`, at most 32. Fix the spelling.

## `--place needs --from ("leg L" or "step N")`

`--place` without `--from` has no staged start. Add `--from "leg <leg>"` or `--from "step <n>"`.

## `leg X has no quiet move row before its first pull: pass --from "step N"`

No quiet move row stands anywhere before the leg's first pull. Pass an exact `--from "step N"` instead.

## `place: 4 tries in the same instance ...`

The placement failed every try it makes in the same instance (quiet refusals, fights after the place, or hires not arriving). Exit 29. The autopilot stops that id on this code: a restart would only enter a fresh instance and burn the hourly cap. Walk from the door, or fix the blockers the notes name, before you re-place.

## `instance cap: ...`

At the cap's raid count the walk waits up to `--instance-cap-wait` for the oldest entry to age out, then exits 26. Either wait for the earliest free time the wait line names, or re-place with `--reuse-instance` (untouched by the cap), or — only after a worldserver restart — clear the stale ledger with `--instance-cap-reset`.

## `raid ... has no minimum hire tier ... pass --tier N (1-3)`

The raid's row in the committed tier table is 0: no tier sourced yet. Pass `--tier N`, or add a roster tier. `--tier` on a raid that has a row is refused the other way: use `--tier-override N` for a comparison run.

## `the roster's tier ..., but raid ...'s minimum ... use --tier-override`

A roster file tier that differs from the table's row. If you mean a comparison, pass `--tier-override` with that tier.

## `raid-walk is for raid plans; a 5-man uses dungeon_run observe`

The plan JSON has no `raid` block. Raid-walk only walks raid plans; run the dungeon path instead.

## `not revived: <why>`

The wipe's revive never found a quiet point (or the commander never landed, or the reset never came). Exit 19. The summary records `revive_refusal` and `run.log` names each blocker's entry, guid, distance and state — move the revive point's blockers or narrow the window with `--patrols` before you retry, and consider `--wipe-resume` budget left.

## `wipe resume skipped: ...`

The wipe did not recover: no `--wipe-resume` left, a boss still engaged, or unknown boss state. The notes name the cause. Raise `--wipe-resume`, or end the walk as before.

## Targets file: `line N: ...`

The autopilot validates `targets.txt` before anything runs (exit 2): `no -- between the header ...` means a line misses its `--`; `the header is ...` means the words before `--` are not exactly `id server slot holder goal`; `the owner rule: slot 3 is never used` means a line names slot 3; `--sql FILE is required` means a line misses `--sql`; `--inject is never used by the autopilot` and `--instance-cap-unsafe is never used by the autopilot` are refused outright. Fix the line and the supervisor picks it up on its next re-read.

## `ESCALATE <kind> <id> <slot>: <reason>` in status.txt

That id is left for a person: exit 2 or 29, three exit 15s, three placement failures, six hire-pool waits or three teardown failures. Clear the cause (arguments, placement, lease holder, pool pressure, slot state), then remove or fix the id's lines.

## `fighting after the place` / `not at the start`

The placement moved the raid but a fight started at the point, or the hires never arrived. The walk steps back up to three move rows (or retries the same point with the last quiet's blockers GM-killed under `--gm-clear`) and tries again in the same instance, up to four tries. Exit 29 when every try fails.

## `re-running step N`

The run leader paused `boss <entry> not found:` and the walk restarts the run at the leg's summoning open row. When the frames saw no unit of the boss alive since the re-run, the instance still counts the encounter started and refuses the object: the walk ends with exit 18 (`this instance cannot bring him back; a fresh instance is needed`). Re-place in a fresh instance; never reuse the spent copy for that boss.

## `gong spent` / `the re-run did not summon him again`

The gong's encounter is `IN_PROGRESS` or `DONE` with a home reset: the object is a no-op until the boss reaches home. Same fix as above: a fresh instance.

## `hire pool: 6 waits`

The server's shared hire pool refused the hires six times in a row (about 30 minutes at 5 minutes a wait): another raid of the server holds the mercenaries. The id is escalated. Wait for the other raid to finish hiring, then clear the id's lines.

## `teardown-failed` / `no YIELD-DONE`

The teardown did not verify (the walk process or its group still alive, or `lease list` still shows the lease): the run stays active on the same out dir and the next tick retries it. The third failure escalates the slot. A `YIELD` over it writes no `YIELD-DONE`. Clear the slot state (process, lease), then clear the id.

## `launch failed` three times

Three consecutive launch failures escalate the id. Check the walk dir's `<id>-<n>.out` for the launch error, fix the cause, then clear the id's lines.

## `raid-suite never uses slot 3 (owner rule)`

You passed `--slot 3` to the suite. Pick another slot. Exit 15.

## `raid-suite walks one raid at a time (--slot A,B is raid-walk's two-raid form)`

The pair form belongs to `raid-walk`, not the suite. Run the raids as a suite, or walk the pair directly.
