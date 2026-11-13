---
type: Reference
title: plan-check, trash-check, path-check
description: Every flag, default, and exit code of the three offline route checks.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/plan_check_cmd.go
tags: [plans, reference]
sources:
  - id: plan-check
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/plan_check_cmd.go
  - id: trash-check
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/trash_check_cmd.go
  - id: path-check
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/path_check_cmd.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:58:00+01:00
status: draft
---

# plan-check, trash-check, path-check

## plan-check

Usage: `turtle-agent plan-check [--dir dungeons] --mmaps DIR [--from-leg N [--from-pull-group N]] [--spawns DIR] [--floor-aware] [--approach-hops] [--strict] [--json] <plan>... | turtle-agent plan-check --mmaps DIR --sql FILE [--plan NAME] [--spawns DIR] [--strict] [--json]`[^plan-check].

Put flags before the plan names: these commands use plain flag parsing, which stops at the first plan name, so a flag after it is never read. On/off flags (every `false` default on this page) take `=` when you give a value (`--strict=true`); a bare `--strict` turns the flag on.
| Flag | Default | What it does |
| --- | --- | --- |
| `--dir` | `dungeons` | Folder with the plan files. Example: `--dir dungeons`. |
| `--mmaps` | _(required)_ | Folder with the server's mmaps. Example: `--mmaps <mmaps-dir>`. |
| `--from-leg` | `0` | Check from this leg on; earlier legs only place the party. Example: `--from-leg 3`. |
| `--from-pull-group` | `-1` | With `--from-leg`: check from this 0-based pull group on. Example: `--from-leg 3 --from-pull-group 2`. |
| `--sql` | _(empty)_ | Lint a world migration's rows instead of a plan file. The reader skips every statement that is no INSERT (a hand-set `UPDATE`, a `START TRANSACTION` wrapper) up to its `;` and names each in the output, warning that its hand edits are NOT applied; the rows around it are read. Example: `--sql 20261020120000_world.sql`. |
| `--plan` | _(empty)_ | With `--sql`: the plan in the file when it holds several. Example: `--plan zulfarrak`. |
| `--spawns` | _(empty)_ | The map's world export (default `<dir>/../tools/dungeongen/testdata/mapN`). Example: `--spawns tools/dungeongen/testdata/map209`. |
| `--floor-aware` | `false` | Build the rows for the lints with the floor-aware cutter. |
| `--approach-hops` | `false` | Build the rows for the lints with the approach hops. |
| `--strict` | `false` | A lint finding fails the check; by default lints are warnings. |
| `--json` | `false` | Print the results as JSON. |

Exits: 0 clean; 11 a point fails (or a lint errors, or `--strict` with lints); 2 usage (`--from-pull-group` without `--from-leg`, `--plan` without `--sql`, bad plan name).

## trash-check

Usage: `turtle-agent trash-check [--dir dungeons] [--sql FILE [--plan NAME]] [--spawns DIR] [--patrols FILE]... [--mmaps DIR] [--level N] [--spread YD] [--aggro-rate R] [--assist-yd 10] [--json] [--out DIR] [--dash-out FILE] [--fail-on danger|patrol|any] [<plan>|<path.json>]`[^trash-check].

Put flags before the plan name here too: this command also uses plain flag parsing, so a flag after the plan name is never read. On/off flags (`--json`) take `=` when you give a value.

| Flag | Default | What it does |
| --- | --- | --- |
| `--dir` | `dungeons` | Folder with the plan files. Example: `--dir dungeons`. |
| `--sql` | _(empty)_ | A deployed migration (the rows the server runs); the recommended input. The reader skips every statement that is no INSERT (a hand-set `UPDATE`, a `START TRANSACTION` wrapper) up to its `;` and names each in the output, warning that its hand edits are NOT applied; the rows around it are read. Example: `--sql 20261020120000_world.sql`. |
| `--plan` | _(empty)_ | With `--sql`: the plan to report when the file holds several. |
| `--spawns` | _(empty)_ | The world export of the plan's map (default `<dir>/../tools/dungeongen/testdata/mapN`). |
| `--patrols` | _(repeatable)_ | A `tb-patrols` recording: replaces the DB patrol it matches, otherwise added. Example: `--patrols patrols-249-1.json`. |
| `--mmaps` | _(empty)_ | Navmesh folder: the route is the server's own path (without it: straight lines). |
| `--level` | `0` | The party's lowest level (default: the plan's `level_band[0]`). Example: `--level 55`. |
| `--spread` | `0` | How far the party spreads from the route, yd (default 10 for a raid, else 3). Example: `--spread 5`. |
| `--aggro-rate` | `1` | Scales every aggro radius. Example: `--aggro-rate 1.2`. |
| `--assist-yd` | `10` | The family assist hop's radius, yd. |
| `--json` | `false` | Print the report as JSON. |
| `--out` | _(empty)_ | Write `trash-<plan>.json` and `trash-<plan>.txt` there. Example: `--out /tmp/trash`. |
| `--dash-out` | _(empty)_ | Write the patrols as the dashboard's patrol data file. |
| `--fail-on` | _(empty)_ | Exit 21 when a finding of this class exists: `danger`, `patrol`, or `any`. Example: `--fail-on danger`. |

Exits: 0; 21 with `--fail-on` when the named class exists; 2 usage (including when the navmesh cannot answer: the check refuses instead of silently walking straight lines).

Usage: `turtle-agent path-check --mmaps DIR --map N [--json] -- x y z x y z`[^path-check]. Put flags before the coordinates (plain flag parsing). `--json` takes `=` when you give a value. Replays one hop with the offline planner and the server's exact query — how a live refusal is replayed from the logged lead position.

| Flag | Default | What it does |
| --- | --- | --- |
| `--mmaps` | _(required)_ | Folder with the server's mmaps. |
| `--map` | _(required)_ | Map id, numeric. Example: `--map 249`. |
| `--json` | `false` | Print the results as JSON. |

Exits: 0 on a replayed hop; 1 when a query fails (no tiles, broken tile); 2 usage.
