---
type: Concept
title: Until clauses and autopilot goals
description: What boss, boss-done, none, step and leg mean, and how the autopilot maps goals onto them.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/raidwalk/until.go
tags: [raids, until, autopilot]
sources:
  - id: until
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/raidwalk/until.go
  - id: autopilot
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/raidwalk/autopilot.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:45:00+01:00
status: draft
---

# Until clauses and autopilot goals

`--until` tells the run leader where the run stops, and the word goes to `run start`, so the server's status label must show `until <clause> (step N)`.[^until]

## The five clauses

- `boss` (default): a trash walk never wakes a boss — it stops before the first boss step.
- `boss-done`: stops after the first boss step has completed. It resolves to `step <the step after the boss>` from the rows, so it needs `--sql` or a plan JSON whose export has the server's step count; else the walk stops the run and exits 18.
- `none`: a full clear — the run leader fights every boss and no boss or until rule stops the run.
- `step N`: stop at that step.
- `leg L`: stop when that leg starts (the suite uses the first leg's end: a boss in leg 0 stops at the boss, else the walk stops when leg 2 starts, else it runs to the end).

A plain `--from` without `--place` or `--until boss-done` goes to the server unresolved. A placed run inside a later leg shortens an explicit clause that would cross a guarded segment (see [How placement works](placement.md)).

## How the autopilot maps its goals

A `targets.txt` goal is one token:[^autopilot]

- `full` — walk to the plan's end.
- `leg:<L>` — `--until "leg L"`.
- `boss:<ENTRY>` — `--until boss-done`, done when that boss's `boss_kills` entry reads killed. A goal boss matches a kill of any entry of that boss: the summary's `entry` and its `phase_entry` both count, and so do the entries of a boss the walk reports under another entry.
- `practice:<ENTRY>:<CYCLES>` — `--until none` and `--practice <ENTRY> --practice-cycles <CYCLES>`; done when the cycles SUMMED over the goal's runs reach CYCLES. It is never met by a boss kill (a practice run credits none), and the practice loop, not the run's until, ends the walk. A relaunch is bounded: an exit 18 relaunches at most twice, then the id stops with `practice: gave up after 2 relaunches`; a boss death during practice (exit 0 below the cycle target) gets one relaunch in a fresh instance, then stops. The summed cycles and the relaunch counts live in the checkpoint.

A goal met by any attempt advances to the id's next goal line; a goal boss already dead ends the goal as done, never escalated — even when the walk ended some other way afterwards.
