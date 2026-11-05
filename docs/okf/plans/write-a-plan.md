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

Most drafts start as a generated skeleton from the world database export (legs, pull groups, bosses, entrance, portal, level band), with the boss order from game knowledge[^plans-doc]. You then correct it by hand. Keep [Anatomy of a plan file](/plans/plan-format.md) open for the field meanings.

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

Read the core's instance script for the dungeon and list every door, key, gossip, and summon in the plan's notes or the `docs/PLANS.md` entry: that list is the mapping walk's checklist, not a verified claim[^plans-doc]. A boss the script summons gets no spawn row — note that instead.

## 5. Validate with plan-check

```sh
turtle-agent plan-check --mmaps <mmaps-dir> <name>
```

Fix every failing point: move it onto the mesh, mark it `needs_walk` if no floor is near, or exempt the hop with `check_skip` and a reason. A plan that loads and checks is a draft; the mapping walk makes it mapped.
