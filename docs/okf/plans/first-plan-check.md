---
type: Tutorial
title: Check your first plan
description: Run the three offline checks on a mapped plan, start to finish.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/plan_check_cmd.go
tags: [plans, tutorial]
sources:
  - id: plan-check
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/plan_check_cmd.go
  - id: plan-probe
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/plan_probe_cmd.go
  - id: trash-check
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/trash_check_cmd.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:50:00+01:00
status: draft
---

# Check your first plan

You check a mapped plan with three offline commands. They read plan files, the world export, and the navmesh tiles; no server, no login[^plan-check].

## 1. Walk every point with plan-check

Run plan-check against the server's navmesh tiles:

```sh
turtle-agent plan-check --mmaps <mmaps-dir> ragefire_chasm
```

`ok, N points on the map M navmesh` means every waypoint, pull, stand, boss, and step point is reachable. A failing point prints its leg, part, pull group, and reason. Fix the plan file and run again until it is clean.

## 2. Score the run leader's hops with plan-probe

```sh
turtle-agent plan-probe --mmaps <mmaps-dir> ragefire_chasm
```

The probe measures each run leader hop on the navmesh and marks segments `bad` or worth a look. A bad segment is a hop the run leader should not walk as it stands — cut it with via points or reroute it. See [Probe the run leader's rows](/plans/probe-a-plan.md).

## 3. See what a walk wakes with trash-check

```sh
turtle-agent trash-check ragefire_chasm
```

The report lists what one walk of the plan wakes: packs a body pull brings, the family assist hop, and the windows in which patrolling packs cross the route or a stand. `danger` means the segment wakes a pack no row pulls there; `patrol` means a patrol crosses while you stand there. See [See what a walk wakes](/plans/trash-check.md).

## What you have

A plan with a clean plan-check, no bad probe segments, and no unexplained `danger` findings is ready for the next step: a scout pass ([Scout a plan before a run](/plans/scout-a-plan.md)) or, if it is still a draft, its mapping walk ([Draft, mapped, live-checked](/plans/plan-lifecycle.md)).
