---
type: Guide
title: Write a plan file
description: Draft a new plan and edit an existing one by hand.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/dungeon/plan.go
tags: [plans, authoring]
sources:
  - id: plan-struct
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/dungeon/plan.go
  - id: plans-doc
    resource: https://github.com/pjerra/turtle-agent/blob/dev/docs/PLANS.md
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:50:00+01:00
status: draft
---

# Write a plan file

The generator reads the world export over read-only ssh (never from a laptop credential) and writes three TSV files plus auras. `dungeongen -dir -name -map -bosses -out` writes a one-leg-per-boss skeleton: creature groups become clusters, friendly, `npc_flags`, and rare entries are skipped, and stealthed packs become `ambush`. The output is byte-repeatable. `-format` rewrites a plan into canonical form. `testdata/map5` (Uldaman) is the checked-in source example.

## 1. Copy the smallest plan as a skeleton

```sh
cp dungeons/onyxias_lair.json dungeons/<name>.json
```

Onyxia's Lair is the smallest plan: one leg, five pull groups, one boss.

## 2. Fill in the top level

Set `version` to 1, `name` to the file stem, `map` to the instance map id, `entrance` to the party start, and `level_band` to the dungeon's band. Set `status` to `draft (not mapped, not live-checked)`. Add `notes` saying what is still a guess.

## 3. Add one leg per boss stretch

Each leg holds the packs on the way to its boss, the boss itself, and any doors or events between. Keep packs to what the navmesh reaches in at most twice the straight-line distance plus 18 yd; anything farther is a detour the plan check will flag.

## 4. Name doors, keys, and events in the notes

The spec (`-spec plans.json -out-dir -table PLANS.md`) covers 46 drafts with one table row each: map and trigger become entrance and portal, bosses order the route, the bounding box sets the wings, the band runs median-2 to p90, teles past 80 yd are dropped, scripted `npc_flags` and faction-35 entries become needs, and specials become the Needs column. Each map testdata folder holds 5 files (portals, movement, and the rest). Tests regenerate byte-identical files, reload every plan, and re-check scripted entries.

## 5. Validate with plan-check

```sh
turtle-agent plan-check --mmaps <mmaps-dir> <name>
```

Fix every failing point: move it onto the mesh, mark it `needs_walk` if no floor is near, or exempt the hop with `check_skip` and a reason. A bad file names its path (`legs[2].steps[5]...`) and refuses `skip` combined with a pull group or a `first` entry. A plan that loads and checks is a draft; the mapping walk makes it mapped.
