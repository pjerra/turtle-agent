---
type: Concept
title: Draft, mapped, live-checked
description: The three plan statuses and the mapping walk that moves a plan between them.
resource: https://github.com/pjerra/turtle-agent/blob/dev/docs/PLANS.md
tags: [plans, lifecycle]
sources:
  - id: plans-doc
    resource: https://github.com/pjerra/turtle-agent/blob/dev/docs/PLANS.md
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:45:00+01:00
status: draft
---

# Draft, mapped, live-checked

A plan's `status` field is the truth about how far it has been checked[^plans-doc]. It holds one of three words plus free text.

## The three statuses

- **Draft** (`draft (not mapped, not live-checked)`): a skeleton from the world database export alone — legs, pull groups, bosses, entrance, portal, level band, hired party. Waypoints, pull order, doors, and events are unchecked. Most plans are drafts.
- **Mapped**: the route is corrected from a mapping walk (below).
- **Live-checked**: a party has run it live.

`dungeon_run` refuses a draft unless you pass `allow_draft`. A raid plan runs in mode `observe` with `prepare.party "hire"` only: the server's run leader leads the bound raid while you hire, gear, and bind it[^plans-doc].

## The mapping walk

A mapping walk turns a draft into a mapped plan. It is a GM-invisible walk of the route before the first live run: every walk call is logged, and the walker fixes waypoints, pull order, doors, patrols, and events from what the walk actually sees. `docs/PLANS.md` lists the recommended order (Uldaman band and below first, raids last) and each plan's known needs besides killing — doors, keys, gongs, gossip menus your walk must cover[^plans-doc].

## Points that need a walk

A spawn can stand where the mesh has no floor (over lava, on a ledge). Generated drafts snap such a point to the nearest walkable floor; a point with no such floor keeps its spawn position and carries `needs_walk` (on the leg's `to`, on a pull group, or on a boss)[^plans-doc]. Those points are the mapping walk's checklist: find the real waypoint. The navmesh checks skip exactly them.

## Regenerating the tables

`docs/PLANS.md` is generated from the plan files; a test fails when it is out of date. Do not edit its tables by hand: edit the generator spec and regenerate. The file header names the exact command.
