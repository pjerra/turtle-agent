---
type: Guide
title: Check a route before the run
description: Walk a plan on the navmesh and estimate what it wakes, before anyone logs in.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/plan_check_cmd.go
tags: [navigation, plans, aggro]
sources:
  - id: plan-check-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/plan_check_cmd.go
    title: The plan-check command
  - id: trash-check-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/trash_check_cmd.go
    title: The trash-check command
  - id: stage-check-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/stage_check_cmd.go
    title: The stage-check command
  - id: route-oracle-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/route_oracle_cmd.go
    title: The route-oracle command
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:33:00+01:00
status: draft
---

# Check a route before the run

You check a dungeon plan offline, with no server and no login: the navmesh says whether each point is reachable, the world export says what the walk wakes. Run these in the repo root, where `dungeons/` and `tools/dungeongen/testdata/` live. Every command needs `--mmaps <walk-dir>`: the server's mmaps folder. Put every flag before the plan name: these commands stop reading flags at the first positional argument, so a flag after the plan name is never applied.

## 1. Walk every point on the navmesh

`plan-check` plans each leg of a plan on the mesh and prints each failing point:

```sh
turtle-agent plan-check --mmaps <walk-dir> ragefire_chasm
```

Each failure names the leg, the part and the reason (off the mesh, across an edge, through water). Exit 0 means every point routes; exit 11 means a point the navmesh cannot take. Add `--strict` to also fail on lint warnings (aggro and patrol lints are warnings by default), `--from-leg 3` to check from one leg on, and `--json` for machine-readable output.

## 2. Estimate what the walk wakes

`trash-check` reports what one walk of the plan pulls: body pulls before the plan's own pulls, the family assist hop, links and groups, and the patrol windows at the route and the stands:

```sh
turtle-agent trash-check --mmaps <walk-dir> deadmines
```

The output lines name the step, the verdict and the cause:

```text
step 1->2 (pull 'pull group 0') DANGER: body pull: step 2 walks within 8.2 yd of Rat (aggro 18.0, pack of 1, unplanned)
step 1->2 (pull 'pull group 0') PATROL: patrol 79138 (Defias Evoker): 36% of 32.3 s in 18.0-29.5 s, at route
```

Tune the estimate with `--level` (default: the plan's level band), `--spread` (default 10 yd for a raid plan, else 3), `--aggro-rate` (default 1) and `--assist-yd` (default 10). Pass `--patrols <file>` for each `tb-patrols` recording; it replaces the world-export patrol it matches. Without `--mmaps` the route is straight lines and the report says so. Exit 21 with `--fail-on danger|patrol|any` when that class of finding exists.

## 3. Check where the run would stage

`stage-check` checks where `dungeon_stage` would put the party for one leg, offline: the leg's first row (else its first plan point), the checks, the back-off proposal and the earlier-leg proposal:

```sh
turtle-agent stage-check --mmaps <walk-dir> ragefire_chasm tunnel
```

Exit 0 means the leg stages; exit 13 means it refuses. `--cleared-through` counts earlier legs cleared (their packs dead).

## 4. Compare the stored route with the mesh

`route-oracle` compares each plan's stored route with what the navmesh plans for the same objectives, and writes a text table plus a JSON report per plan into `--out`:

```sh
turtle-agent route-oracle --plans dungeons --mmaps <walk-dir> --out /tmp/oracle --plan ragefire_chasm
```

The default `--mode walk` scores the plan's stored hops; `--mode bosses` plans entrance to each boss live with no hand points. Exit 12 on failure.
