---
type: Reference
title: Check commands
description: Every flag of trash-check, plan-check, stage-check, route-oracle and plan-diff.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/trash_check_cmd.go
tags: [navigation, reference, cli]
sources:
  - id: trash-check-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/trash_check_cmd.go
  - id: plan-check-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/plan_check_cmd.go
  - id: stage-check-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/stage_check_cmd.go
  - id: route-oracle-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/route_oracle_cmd.go
  - id: plan-diff-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/plan_diff_cmd.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:45:00+01:00
status: draft
---

# Check commands

Every flag of the five offline check commands. All run with no server and no login. Exits: 0 clean, 2 usage error; per-command finding exits are listed.

## trash-check

`usage: turtle-agent trash-check [--dir dungeons] [--sql FILE [--plan NAME]] [--spawns DIR] [--patrols FILE]... [--mmaps DIR] [--level N] [--spread YD] [--aggro-rate R] [--assist-yd 10] [--json] [--out DIR] [--dash-out FILE] [--fail-on danger|patrol|any] [<plan>|<path.json>]`

One plan name or `.json` path, or `--sql`; not both.

| Flag | Default | What it does | Example |
|---|---|---|---|
| `--dir` | `dungeons` | The folder with the plan files. | `--dir dungeons` |
| `--sql` | `""` | A deployed migration (the rows the server runs); the recommended input. | `--sql /tmp/world.sql` |
| `--plan` | `""` | With `--sql`: the plan to report when the file holds several. | `--plan deadmines` |
| `--spawns` | `<dir>/../tools/dungeongen/testdata/map<N>` | The world export of the plan's map. | `--spawns tools/dungeongen/testdata/map36` |
| `--patrols` | (repeatable) | A `tb-patrols` recording: replaces the export patrol it matches, else added. | `--patrols /tmp/p.json` |
| `--mmaps` | `""` | Folder with the server's mmaps: the route is the server's own path (without it: straight lines). | `--mmaps <walk-dir>` |
| `--level` | `0` | The party's lowest level (default: the plan's `level_band[0]`). | `--level 20` |
| `--spread` | `0` | How far the party spreads from the route, yd (default 10 for a raid plan, else 3). | `--spread 5` |
| `--aggro-rate` | `1` | Scales every aggro radius. | `--aggro-rate 1.2` |
| `--assist-yd` | `10` | The family assist hop's radius, yd. | `--assist-yd 12` |
| `--json` | `false` | Print the report as JSON (`tb-trash-check`, version 1). | `--json` |
| `--out` | `""` | Write `trash-<plan>.json` and `trash-<plan>.txt` there. | `--out /tmp` |
| `--dash-out` | `""` | Write the patrols as the dashboard's `patrols.json` data file. | `--dash-out /tmp/patrols.json` |
| `--fail-on` | `""` | Exit 21 when a finding of this class exists: `danger`, `patrol` or `any`. | `--fail-on danger` |

## plan-check

`usage: turtle-agent plan-check [--dir dungeons] --mmaps DIR [--from-leg N [--from-pull-group N]] [--spawns DIR] [--floor-aware] [--approach-hops] [--strict] [--json] <plan>... | turtle-agent plan-check --mmaps DIR --sql FILE [--plan NAME] [--spawns DIR] [--strict] [--json]`

`--mmaps` is required. Exit 11 when a point the navmesh cannot take is found.

| Flag | Default | What it does | Example |
|---|---|---|---|
| `--dir` | `dungeons` | The folder with the plan files. | `--dir dungeons` |
| `--mmaps` | `""` (required) | Folder with the server's mmaps. | `--mmaps <walk-dir>` |
| `--from-leg` | `0` | Check from this leg on (earlier legs only place the party). | `--from-leg 3` |
| `--from-pull-group` | `-1` | With `--from-leg`: check from this pull group (0-based) of that leg on. | `--from-leg 3 --from-pull-group 1` |
| `--sql` | `""` | Lint the rows of a world migration instead of a plan file. | `--sql /tmp/world.sql` |
| `--plan` | `""` | With `--sql`: the plan in the file (needed when it holds several). | `--plan deadmines` |
| `--spawns` | `<dir>/../tools/dungeongen/testdata/mapN` | The map's world export for the aggro and patrol lints (skipped with a note when missing). | `--spawns tools/dungeongen/testdata/map36` |
| `--floor-aware` | `false` | Build the plan's rows with the floor-aware cutter. | `--floor-aware` |
| `--approach-hops` | `false` | Build the plan's rows with the approach hops. | `--approach-hops` |
| `--strict` | `false` | Make a lint finding fail the check (by default lints are warnings). | `--strict` |
| `--json` | `false` | Print the results as JSON. | `--json` |

## stage-check

`usage: turtle-agent stage-check [--dir dungeons] --mmaps DIR [--cleared-through LEG] [--server-steps N] [--json] <plan> <leg>`

`--mmaps` is required. Exit 13 when the leg refuses to stage.

| Flag | Default | What it does | Example |
|---|---|---|---|
| `--dir` | `dungeons` | The folder with the plan files. | `--dir dungeons` |
| `--mmaps` | `""` (required) | Folder with the server's mmaps. | `--mmaps <walk-dir>` |
| `--cleared-through` | `""` | Legs 0 through this one count as cleared (their packs dead). | `--cleared-through 2` |
| `--server-steps` | `0` | The step count `.bot run list` shows for the plan (picks the exporter option set). | `--server-steps 40` |
| `--json` | `false` | Print the result as JSON. | `--json` |

## route-oracle

`usage: turtle-agent route-oracle --plans DIR --mmaps DIR --out DIR [--mode walk|bosses] [--objectives plan|bosses] [--plan NAME]...`

`--mmaps` and `--out` are required. Exit 12 on failure.

| Flag | Default | What it does | Example |
|---|---|---|---|
| `--plans` | `dungeons` | The folder with the plan files. | `--plans dungeons` |
| `--mmaps` | `""` (required) | Folder with the server's mmaps. | `--mmaps <walk-dir>` |
| `--out` | `""` (required) | Folder for `<plan>.txt` and `<plan>.json` reports. | `--out /tmp/oracle` |
| `--mode` | `walk` | `walk`: score the plan's stored hops on the navmesh; `bosses`: plan entrance to each boss live. | `--mode bosses` |
| `--objectives` | `plan` | Bosses mode only. `plan`: each leg's own objective; `bosses`: only the boss chain. | `--objectives bosses` |
| `--plan` | `wailing_caverns,ragefire_chasm,uldaman,zulfarrak,deadmines` | A plan name; repeatable. | `--plan deadmines --plan uldaman` |

## plan-diff

`usage: turtle-agent plan-diff <old> <new> [--plan NAME] [--json] [--out DIR] [--svg FILE] [--minimaps DIR] [--picture worldmap|minimap|navmesh|none] [--fail-on-change]`

Compares two plans (or two world migrations) step by step.

| Flag | Default | What it does | Example |
|---|---|---|---|
| `--plan` | `""` | The plan to compare when a migration holds several. | `--plan deadmines` |
| `--json` | `false` | Print the `tb-plan-diff` JSON on stdout instead of the text. | `--json` |
| `--out` | `""` | Write `diff-<plan>.txt`, `diff-<plan>.json` and `diff-<plan>.svg` there. | `--out /tmp` |
| `--svg` | `""` | Also write the SVG to this file. | `--svg /tmp/diff.svg` |
| `--minimaps` | `~/turtle-agent-minimaps` | Folder with the map pictures (`<map>.json`/`.png`, `<map>.minimap.*`, `<map>.navmesh.*`). | `--minimaps ~/turtle-agent-minimaps` |
| `--picture` | `worldmap` | The picture under the route: `worldmap`, `minimap`, `navmesh` or `none`. | `--picture navmesh` |
| `--fail-on-change` | `false` | Exit 25 when there is any change other than a comment. | `--fail-on-change` |
