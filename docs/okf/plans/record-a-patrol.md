---
type: Guide
title: Record a patrol
description: Measure a patrolling pack with patrol-record for a plan's patrol block.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/patrol_record_cmd.go
tags: [plans, patrol]
sources:
  - id: patrol-record
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/patrol_record_cmd.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:52:00+01:00
status: draft
---

# Record a patrol

`patrol-record` watches a circle of the map for a few minutes and writes a `tb-patrols` recording: each patrolling creature's path loop and round period[^patrol-record]. `trash-check --patrols` uses the recording instead of the database patrol.

## 1. Pick the circle and the time

```sh
turtle-agent patrol-record <slot-dir> --map <M> --area <x>,<y>,<r> --minutes <N>
```

The recorder never fights: it sends `.gm on` and `.gm visible off`, teleports once with `.go xyz`, and sends `.gm visible on` at the end (or when it stops early). It never attacks, casts, targets, or moves the character.

Each sample reads the agent status, the own unit, and the units within the area radius plus 60 yd. A creature records once seen within the circle (2D) and while it stays in range. The run stops at once — file written, exit 22 — on combat, death, being targeted, a map change, or losing the world.

## 2. Settle the floor question

When the navmesh has several floors at your `x, y`, pass `--z <z>` for the floor to watch. `--z` is required without `--mmaps`; with `--mmaps` only when several floors sit at one `x,y`. `--mmaps DIR` lets the recorder find it when `--z` is absent.

## 3. Save with a fresh name

```sh
turtle-agent patrol-record <slot-dir> --map <M> --area <x>,<y>,<r> --minutes <N> --out <file>
```

`--spawns DIR` names the world's `creatures.tsv` export so the recording names each patrol after its nearest spawn within 40 yd (else the hex guid). The file is `tb-patrols v1`: path, `period_s`, estimate, cycles, waits, phase epoch, and completeness, plus static and wandering creatures. A complete run prints `recorded ...: period ... (N cycles), M points, matches spawn ...` and exits 0. Exit 15 means refused, grouped, or not in the world; 16 means the RPC call failed; 2 means usage.

## 4. Copy the loop into the plan

Take the recording's `path` (waypoints in walk order — the patrol loops from the last back to the first), `period_s` (one round), and `entries` into the pull group's `patrol` block. Keep `period_estimate: true` out: the round is now measured, so the driver may predict its phase. The plan check warns for each patrol that still carries the flag.
