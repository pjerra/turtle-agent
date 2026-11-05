---
type: Guide
title: Recover from wipes
description: Let the walk revive the raid and resume the run after a wipe.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/raid_walk_cmd.go
tags: [raids, wipe, recovery]
sources:
  - id: walk-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/raid_walk_cmd.go
  - id: reviveraid
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/raid_walk_reviveraid.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:40:00+01:00
status: draft
---

# Recover from wipes

By default (`--wipe-resume 0`) a wipe ends the walk with exit 19. Raise `--wipe-resume` to revive and resume instead.

## 1. Allow recoveries

```sh
turtle-agent raid-walk <plan> --server <server> --slot <slot> \
    --dir <plans-dir> --sql <world-sql> --wipe-resume 2
```

`--wipe-resume K` (0-3) revives a wipe or a near-wipe stop and resumes the run, at most K times per walk. The summary notes each recovery (`wipe-resume <n>/<K> at step <s>: revived <d>, rested <m>, resumed`) and counts them in `wipe_resumes`. `--wipe-rest` (default 10m, at least 30s) is how long the revived raid rests out of combat before the run resumes. The recovery has a deadline: the walk ceiling or start plus 3 times `--wipe-rest`, whichever is sooner.

## 2. Handle regroup pauses

```sh
turtle-agent raid-walk <plan> --server <server> --slot <slot> \
    --dir <plans-dir> --sql <world-sql> --auto-resume 2
```

`--auto-resume K` (0-3) resumes a "did not hold" regroup pause 15 s after it starts, at most K times per step and 6 per run. `--paused-wait` (default 10m) bounds a pause that is not a regroup.

## 3. Know the revive flow

On a wipe the walk stops the run, waits for the instance to reset (`--reset-wait`, default 5m), revives at the revive point (step 1's point, or the placed start with `--place`), then summons and revives every hire. `--revive-at-raid` (default on) revives the raid at the revive point: after the revives, every living hire more than 100 yd away is brought to it with a GM summon, and the resume waits up to 60 s for every hire to be in range and out of combat. With `--revive-at-raid=false` the walk uses the old revive in place, with no summons and no gather wait. `summary.json` counts `revive_brought`, `revive_unreached` and `revive_gather_missed`.

A revive needs a quiet point: no living hostile within 45 yd, no moving hostile within 80 yd and no patrol path within 45 yd, rechecked until it holds. Critters never count. `--patrols FILE` (a tb-patrols file) feeds the quiet check. A revive that never finds a quiet point ends the walk with exit 19 (`not revived: <why>`) and records `revive_refusal` in the summary.

## 4. Know what blocks recovery

No `run stop` goes out while a boss row has a target: a boss engagement stops the run and waits up to 15 minutes for a kill, a reset or a wipe. The one exception is a wipe on the boss's own step whose run leader fights that boss (an `until` past the boss step, or `--until none`): then the walk owns no boss attempt, so the wipe follows the ordinary wipe-resume flow and never sends `run stop` at the wipe.

Recovery fails closed: unknown boss state, a boss that is still live in the dashboard's fight list, or a second wipe during the rest or resume wait ends the walk with exit 19 and no resume.
