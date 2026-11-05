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

`--area` is the circle to watch (`x,y,r`); `--minutes` is 1 to 60. `--entries e1,e2` records only those creature entries (default: every creature). `--sample-ms` (default 1000, 500–5000) sets the sample gap.

## 2. Settle the floor question

When the navmesh has several floors at your `x, y`, pass `--z <z>` for the floor to watch. `--mmaps DIR` lets the recorder find it when `--z` is absent.

## 3. Save with a fresh name

```sh
turtle-agent patrol-record <slot-dir> --map <M> --area <x>,<y>,<r> --minutes <N> --out <file>
```

The default output is `./patrols-<map>-<stamp>.json`; `--out` names the file and is refused when it exists. `--spawns DIR` names the world's `creatures.tsv` export so the recording names the patrols' spawns.

## 4. Copy the loop into the plan

Take the recording's `path` (waypoints in walk order — the patrol loops from the last back to the first), `period_s` (one round), and `entries` into the pull group's `patrol` block. Keep `period_estimate: true` out: the round is now measured, so the driver may predict its phase. The plan check warns for each patrol that still carries the flag.
