---
type: Overview
title: Plans overview
description: What a dungeon or raid plan is and how the tools around it fit together.
resource: https://github.com/pjerra/turtle-agent/blob/dev/dungeons/embed.go
tags: [plans, overview]
sources:
  - id: plans-doc
    resource: https://github.com/pjerra/turtle-agent/blob/dev/docs/PLANS.md
  - id: plan-struct
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/dungeon/plan.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:45:00+01:00
status: draft
---

# Plans overview

A plan is one JSON file in `dungeons/` that describes a whole dungeon or raid run: where the party walks, which packs it pulls, what it clicks and waits for, and which boss dies in each leg[^plan-struct].

Everything else in this area turns a plan file into something a party can run, or checks that it is safe to run:

- You **write** a plan by hand or from a world-database export, then correct it on a mapping walk ([Draft, mapped, live-checked](/plans/plan-lifecycle.md)).
- You **check** it offline: [plan-check](/plans/check-a-plan.md) walks every point on the navmesh, [plan scout](/plans/scout-a-plan.md) lints the exported rows, [plan-probe](/plans/probe-a-plan.md) scores the run leader's hops, [trash-check](/plans/trash-check.md) lists what a walk wakes, and [route-oracle](/plans/route-oracle.md) compares the stored route with what the navmesh would plan.
- You **ship** it: [plan-export](/plans/export-a-plan.md) writes the TortoiseBots world migration, and [plan-diff](/plans/export-a-plan.md) shows what changed between two exports.
- You **fix routes in place**: the dashboard route editor records versions, and `route-edits` moves a checked version back into `dungeons/` ([Apply route edits](/plans/apply-route-edits.md)).

A plan never runs by itself. The commander reads it for dungeon runs, and the run leader rows the export writes are what TortoiseBots executes. Read [Anatomy of a plan file](/plans/plan-format.md) for the format, then [Check your first plan](/plans/first-plan-check.md) to run the checks.
