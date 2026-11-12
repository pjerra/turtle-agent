---
type: Concept
title: Hire gear tiers
description: Why the hire tier matters, where each raid's minimum comes from, and what comparison runs are.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/raidwalk/tiers.go
tags: [raids, tier, hires]
sources:
  - id: tiers
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/raidwalk/tiers.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:45:00+01:00
status: draft
---

# Hire gear tiers

The walk hires and refits at the kit a player holds when the raid is next: a wipe at the right tier is a gear-set tuning finding, never fixed by raising the tier. The hire gear tier comes from a committed table, one row per raid, from the mercenary unlock quests.[^tiers]

## The table

Three raids have sourced rows: `onyxias_lair` is 2, `zul_gurub` and `ruins_of_ahnqiraj` are 1. Every other raid holds a 0 row: no tier sourced yet, and a walk refuses to run it without `--tier N`.

## The two flags

- `--tier N` (1-3) fills the tier of a raid the table leaves unknown. It is refused on a raid that has a row.
- `--tier-override N` (1-3) replaces a known minimum. It marks the run a **comparison, not a tier proof**: the notes and `summary.json` (`roster.tier_comparison`) say so, and the suite's baseline pick skips it.

`--tier` and `--tier-override` together are refused. A roster file tier that differs from the table's row is refused too — use `--tier-override` to state the comparison. The summary records where the tier came from in `roster.tier_source`: `table`, `roster`, `--tier` or `--tier-override`.

The walk waits up to `--hire-settle` (default 20s, a poll of the group that ends as soon as every hire has joined) after the last hire for its join summons, then refits with `.bot gear raid <commander> tier <N>`. A refit refused with `ACTION_ERR|gear|busy` (a gear job is already running) waits up to 3 s for that job's own `TBM:GEAR` line, drops it (it geared the roster before the spec was put on, so it is not the refit's verdict), and re-sends: at most 5 sends inside the one 60 s line budget, which is checked before every re-send. Only a verdict that follows a send the module accepted counts. Any `--hire-settle` below the default marks the summary `roster.settle_short: true`, and the suite's baseline pick skips that walk.
