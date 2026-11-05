---
type: Guide
title: Check a plan on the navmesh
description: Run plan-check, read its lints, and fix each kind of finding.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/plan_check_cmd.go
tags: [plans, plan-check]
sources:
  - id: plan-check
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/plan_check_cmd.go
  - id: planlint
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/planlint/lint.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:50:00+01:00
status: draft
---

# Check a plan on the navmesh

`plan-check` walks every point of the named plans on the navmesh and prints each failing point. It reads the plan files and the mmaps folder and nothing else[^plan-check].

## 1. Run the check

```sh
turtle-agent plan-check --mmaps <mmaps-dir> <plan>...
```

Put flags before the plan names: this command uses plain flag parsing, which stops at the first plan name. On/off flags (every `false` default in the reference table) take `=` when you give a value.

`--mmaps` is required. The check walks entrance, `via`, `to`, pull group positions, boss, step stands, and boss from-steps.

## 2. Start from a later leg when iterating

```sh
turtle-agent plan-check --mmaps <mmaps-dir> --from-leg 3 <plan>
```

`--from-leg N` checks from leg `N` on; earlier legs only place the party. With it, `--from-pull-group N` (0-based) starts inside that leg's groups.

## 3. Read the lints

Beyond unreachable points, plan-check lints the exported rows: `water` (point in liquid), `off-floor`, `edge` (within 2.5 yd of a mesh edge), `ledge`, `short hop`, `aggro` (a walk that wakes an unplanned pack), `boss_aggro`, `patrol`, plus the scout-parity lints `reach`, `hop_len`, `arrival`, `climb_anchor`, and `joiners` (a pull that drags extra packs)[^planlint]. Lints are warnings by default; `--strict` makes any lint finding fail the check (exit 11).

## 4. Check a deployed migration instead

```sh
turtle-agent plan-check --mmaps <mmaps-dir> --sql <migration.sql> [--plan <name>]
```

This lints the rows of a world migration — the exact rows the server runs — instead of a plan file. `--plan` picks the plan when the file holds several.

## 5. Fix or exempt

Move a failing point onto the mesh. When the hop is unwalkable on purpose (a jump down the mesh does not link), exempt it with `"check_skip": "why"` on the waypoint — the check lists it as skipped with your reason instead of failing it. When a spawn truly stands off-mesh, mark it `needs_walk` and put it on the mapping walk's list.
