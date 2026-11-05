---
type: Guide
title: Check a plan without a server
description: stage-check and trash-check read plan rows and world data and log in to nothing.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/stage_check_cmd.go
tags: [stage-check, trash-check, guide]
sources:
  - id: stage-check-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/stage_check_cmd.go
  - id: trash-check-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/trash_check_cmd.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:51:00+01:00
status: draft
---

# Check a plan without a server

Both commands read files only: plan rows, a world export, optionally the navmesh. No server, no login.
They complement the plan checks in [plans](/plans/index.md): `stage-check` answers where staging puts the party, `trash-check` what the walk wakes.

## 1. Check where staging would put the party

```sh
turtle-agent stage-check --mmaps <walk-dir>/mmaps uldaman keystone
```

`stage-check` checks where `dungeon_stage` would put the party for one leg: the leg's first row (else its first plan point), the checks of `dungeon.StageLeg`, the back-off proposal and the earlier-leg proposal. The leg is an index or a name.

Every flag and its default:

| Flag | Default | What it does |
| --- | --- | --- |
| `--dir` | `dungeons` | the folder with the plan files |
| `--mmaps` | none, required | folder with the server's mmaps |
| `--cleared-through` | none | legs 0..this one count as cleared (their packs dead) |
| `--server-steps` | 0 | the step count `.bot run list` shows for the plan (picks the exporter option set) |
| `--json` | off | print the result as JSON |

Text output names the staging point (`stage at <x> <y> <z>`) or the refusal (`refused: <reason>`), plus proposals and notes. Exit 0 when the leg stages, 13 when it refuses (`exitStageRefused`), 2 on usage.

## 2. Report what a walk would wake

```sh
turtle-agent trash-check --mmaps <walk-dir>/mmaps onyxias_lair
turtle-agent trash-check --sql <walk-dir>/mig.sql --plan uldaman --spawns <walk-dir>/map70 --mmaps <walk-dir>/mmaps --json --fail-on danger
```

`trash-check` reports what one walk of a plan wakes: body pulls before the plan's own pulls, the family assist hop, links and groups, and the patrol windows at the route and the stands. `--sql` is the recommended input (a deployed migration; the rows the server runs).

Every flag and its default:

| Flag | Default | What it does |
| --- | --- | --- |
| `--dir` | `dungeons` | the folder with the plan files (and where a recorded patrol's spawns are found) |
| `--sql` | none | a deployed migration; with `--plan`, the plan to report when the file holds several |
| `--plan` | none | with `--sql`: the plan to report |
| `--spawns` | none | the world export of the plan's map (default `<dir>/../tools/dungeongen/testdata/map<N>`) |
| `--patrols` | none, repeatable | recorded patrol files |
| `--mmaps` | none | folder with the server's mmaps; without it the route is straight lines |
| `--level` | 0 (the plan's `level_band[0]`) | the party's lowest level |
| `--spread` | 0 (10 for a raid plan, else 3) | how far the party spreads from the route, in yards |
| `--aggro-rate` | 1 | scales every aggro radius |
| `--assist-yd` | 10 | the family assist hop's radius, in yards |
| `--json` | off | print the report as JSON |
| `--out` | none | write `trash-<plan>.json` and `trash-<plan>.txt` there |
| `--dash-out` | none | write the patrols as the dashboard's patrol data file |
| `--fail-on` | none | exit 21 when a finding of class `danger`, `patrol` or `any` exists |

Exit 0 normally, 21 with a `--fail-on` match, 1 when the navmesh is unavailable, 2 on usage. The last text line summarizes: steps, segments, and how many are clear, own, patrol, or danger.
