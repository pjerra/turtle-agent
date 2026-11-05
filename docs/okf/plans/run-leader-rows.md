---
type: Concept
title: From plan to run leader rows
description: What the exporter writes into the run leader tables and which steps it cannot carry.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/planexport/planexport.go
tags: [plans, export]
sources:
  - id: planexport
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/planexport/planexport.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:45:00+01:00
status: draft
---

# From plan to run leader rows

`plan-export` turns `dungeons/<plan>.json` into one idempotent world migration: rows for `ai_dungeon_plan`, `ai_dungeon_leg`, and `ai_dungeon_step`[^planexport]. The run leader runs the rows; the plan file is only the source.

## What the rows carry

Each leg becomes its waypoints (`move` rows), its pulls (`pull` rows), its steps (one row per kind: `loot`, `open`, `use_item`, `wait_object`, `ritual`, `hold`, `gossip`, `use_on`), and its boss (`boss` row). Row flags say what the module must honor: alliance/horde-only, done-by-data or done-by-object, gone-is-done, dead-NPC-is-done, stand-pull, skip-unreachable, proof-is-creature, and the gossip reset flags[^planexport].

Two options reshape the rows on export. `--mmaps DIR` cuts long hops into via points along the navmesh path (a hop of more than 60 yd between two moves is cut; with `--approach-hops` the hop to a pull, boss, or hold is cut too). `--floor-aware` cuts the floor-aware way instead (longer hops stay whole, small corners are dropped). Avoid boxes reroute hops around deadly ground.

## What the run leader cannot do

Some steps need the agent's own character: a gossip by `agent`, a `wait_for` on a `unit` without a `hold`, an `area_trigger` by `agent`. The export stops before the first leg that holds one: it exports legs up to that point, prints a `CUT:` line on stdout, and notes the cut in the plan row's comment[^planexport]. A plan whose first leg holds such a step is refused outright.

## What the rows cannot carry

- `companions_loot: false` has no run leader column: export of such a leg is refused unless you pass `--drop-companions-loot` (then the bots' looting must be turned off in the run setup instead).
- `skip_export` legs stay out unless named with `--with-legs`; `optional` legs stay out unless named with `--with-optional` (and their item is yours to hand to the lead bot).
- A `gm_cross` waypoint has no run leader step and is refused. `--gm-cross-direct` exports it as a `move_direct` (a straight walk to the landing point) instead, only for a gap that is a drop the line crosses; the row's comment and a warning say it was never tried live.
