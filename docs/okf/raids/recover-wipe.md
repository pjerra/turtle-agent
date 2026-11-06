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

### Quiet, gather and boss evidence

The revive's quiet is the walk's own read: nothing alive and hostile within 45 yd, nothing alive hostile and moving within 80 yd, no patrol whose path comes within 45 yd. An idle hostile (not in combat, no target, not moving) at its home stands outside the rule when it is over 30 yd out and within 3 yd of a plan spawn: it is what the run faced when it started there. Critters never count: the frame's own critter flag decides first, else a level-1 unit inside the max-HP band. The walk waits up to 120 s plus re-reads; only moving patrols earn a re-read (up to 2), and a revive that never finds quiet ends the walk with exit 19.[^reviveraid]

The gather brings every living hire more than 100 yd away to the revive point with a GM summon and waits up to 60 s for every hire to be in range and out of combat. `summary.json` counts `revive_brought`, `revive_unreached` and `revive_gather_missed`. The commander checks it still stands in the raid's copy first: a commander in another copy would summon the hires out of the raid's instance.[^reviveraid]

Boss evidence decides whether a boss step counts as dead. The frames must show the boss's own guid dead this attempt, with no alive sighting within 15 s, and a hire alive: a stale low-HP frame never kills. Adds with the boss's `first` entries and guardians do not count as the boss. An unknown state fails closed: no resume, exit 19.[^reviveraid]

### Boss-not-found and re-run

When the run leader pauses `boss <entry> not found:` on the boss's own step, the walk re-runs the leg's summoning open row instead of failing it. The link is the plan's own annotation (`boss.summon_open_step`): an open or open-proof row of the same leg, before the boss row, with the annotated object's entry. Zul'Gurub's Arlokk is the case it exists for: after a wipe she despawns, and only the gong 180526 starts her script again. A boss with no annotated summon gets no re-run, whatever stands around it.[^reviveraid]

The re-run sends `.bot run stop`, waits up to 15 s for the run's own status to show it stopped, then starts the run again at the open row with the walk's own until and speed, and reads `re-running step N` into the notes. It re-runs at most 3 times per walk; a refused stop falls back to the pause path. When the frames saw no unit of the boss alive since the re-run went out, the instance still counts the encounter started and refuses the object: the walk reports `this instance cannot bring him back; a fresh instance is needed` and ends restartable with exit 18, for walk-super to re-place in a fresh instance (never a reuse of the spent copy).[^reviveraid]


## 4. Know what blocks recovery

No `run stop` goes out while a boss row has a target: a boss engagement stops the run and waits up to 15 minutes for a kill, a reset or a wipe. The one exception is a wipe on the boss's own step whose run leader fights that boss (an `until` past the boss step, or `--until none`): then the walk owns no boss attempt, so the wipe follows the ordinary wipe-resume flow and never sends `run stop` at the wipe.

### Paused on the boss row

A boss that engages while the run is paused on that boss's own row (the run leader paused there, e.g. its pullback found no firing stand, and the tank's own late pull woke the boss under the pause) resumes instead of stopping: with an until that goes past the row the walk sends `.bot run resume` (once per boss row) and lets the run leader fight it. The ACK alone is no hand-over: the walk trusts the resume only once the newest accepted status is a fresh RunRunning of the boss row within 10 s. A run that stays paused, pauses again, or shows a later paused/done/none line inside the wait goes through the boss path at once, so the engaged boss is never left without run-leader control. A wipe or stall drained during the wait runs its normal handler first and fails the handoff; a paused run anywhere else, or a refused resume, still stops the walk. The wipe and recovery paths never take this resume (a dead raid must not be resumed, and a recovery sends its own resume).[^walk-cmd]
