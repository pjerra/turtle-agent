---
type: Guide
title: See what a walk wakes
description: Run trash-check and read which packs and patrols one walk of the plan wakes.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/trash_check_cmd.go
tags: [plans, trash]
sources:
  - id: trash-check
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/trash_check_cmd.go
  - id: trashcheck
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/trashcheck/check.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:52:00+01:00
status: draft
---

# See what a walk wakes

`trash-check` reports what one walk of a plan wakes before a raid runs it: the trash packs a body pull brings, and the windows in which a patrolling pack crosses the route[^trash-check]. No server, no login.

## 1. Run it on the plan

```sh
turtle-agent trash-check <plan>
```

By default it rebuilds the rows from the plan file and walks straight lines between them. Pass `--mmaps <mmaps-dir>` to walk the server's own path instead.

## 2. Check the deployed rows

```sh
turtle-agent trash-check --sql <migration.sql> [--plan <name>]
```

The migration holds the exact rows the server runs — the recommended input[^trash-check]. `--spawns DIR` points at the map's world export when the default lookup misses it.

## 3. Use recorded patrols

```sh
turtle-agent trash-check --patrols <recording.json> <plan>
```

`--patrols` (repeatable) replaces the database patrol it matches with a `patrol-record` measurement, and is otherwise added. Use it wherever the database path is stale.

## 4. Read the verdicts

Each segment ends with a count line and the notes, for example `onyxias_lair: 29 steps, 28 segments: 16 clear, 10 own, 2 patrol, 0 danger`[^trash-check]. Worst first: `danger` (the segment wakes a pack no row pulls there — split the pull or move the route), `patrol` (a patrol crosses the route or a stand — add a `patrol` block so the party waits it out), `own` (the plan pulls it there), `clear`. Tune the model with `--level N` (default: the plan band's low end), `--spread YD`, `--aggro-rate R`, and `--assist-yd` (default 10).

## 5. Save it and gate on it

`--out DIR` writes `trash-<plan>.json` and `trash-<plan>.txt`; `--json` prints JSON; `--dash-out FILE` writes the patrols as the dashboard's patrol data file. `--fail-on danger|patrol|any` exits 21 when that class of finding exists, for scripts.
