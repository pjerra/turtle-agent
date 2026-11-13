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

The walk waits up to `--hire-settle` (default 20s, a poll of the group that ends as soon as every hire has joined) after the last hire for its join summons, then refits with `.bot gear raid <commander> tier <N>`. The module's gear queue is one slot for the whole server, and the verdict of the job holding it goes only to the character that queued it, so a walk refused with `ACTION_ERR|gear|busy` (a gear job is already running) cannot wait that job out: it re-sends the same line after a jittered backoff (2 s, 4 s, 8 s, 16 s, then 20 s plus 0-3 s), each retry noted, until a send is accepted or the 120 s budget leaves no room for another. The verdict line carries no job or requester id, and the job holding the slot can be the commander's own (a walk restarted while its refit still ran), so each backoff is also a drain: a `TBM:GEAR` line read in that window is dropped (and noted) as the earlier job's, and only a verdict that follows a send the module accepted counts. Each send is answered before the next goes out, so two gear requests never overlap. A budget spent on busy answers ends the walk on the module's own line. Any `--hire-settle` below the default marks the summary `roster.settle_short: true`, and the suite's baseline pick skips that walk.
