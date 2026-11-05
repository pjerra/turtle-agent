---
type: Guide
title: Run a raid walk
description: Pick an until clause, launch the walk, and read its result.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/raid_walk_cmd.go
tags: [raids, raid-walk]
sources:
  - id: walk-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/raid_walk_cmd.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:35:00+01:00
status: draft
---

# Run a raid walk

## 1. Pick the `until` clause

`--until` defaults to `boss`: the walk stops before the first boss step, so a trash walk never wakes a boss. Your options:[^walk-cmd]

- `boss` — stop before the first boss step.
- `boss-done` — stop after the first boss step has completed. It resolves to `step <the step after the boss>` from the rows, so it needs `--sql` or a plan JSON whose export has the server's step count.
- `none` — a full clear: the run leader fights every boss and no boss or until rule stops the run.
- `step N` or `leg L` — stop at that step or leg.

## 2. Launch the walk

```sh
turtle-agent raid-walk <plan> --server <server> --slot <slot> \
    --dir <plans-dir> --sql <world-sql> --until boss-done \
    --max-run-minutes 90 --hold-minutes 5
```

`--from "leg L"` or `--from "step N"` is only for a raid already standing at that point (for example after a hold): the walk does not stage. `--max-run-minutes` (default 90) is the ceiling from `run start`; `--hold-minutes` keeps the raid bound afterwards. `--enter` picks how the raid enters: `auto` (default), `near`, or `tele` (needs the plan's `entrance_tele`).

## 3. Watch the phases

The walk runs validate, lease, `slot setup`, `login`, hire and gear, `commander bind <MT> raid`, enter, `run start`, watch, hold, `commander status` and `teardown`, then writes the summary. The lease lasts `max-run-minutes + hold-minutes + 30` and renews every 30 minutes; a refused renewal stops the run, tears down and exits 18 (`lease lost`). SIGINT/SIGTERM stops the run, tears down and exits 18 (`interrupted`); during the hold it exits 0.

## 4. Read the result

Exit 0 means the run ended by its `until` (or the hold). Anything else names its cause in the notes: exit 18 a phase failed or was refused, exit 19 a wipe or boss engagement happened, exit 23 a fault verdict failed or a cleanup left a bot frozen, exit 15 the lease was refused, exit 26 the instance cap was hit, exit 29 a placement failed every try. `summary.json` (format `tb-raid-walk` v1) carries the end reason, steps, timeline, legs, deaths, wipes, `wipe_resumes`, boss records and every note; `summary.txt` is the one-line result.

Without `--sql`, step numbers and the first point come from the plan JSON and the summary says so.
