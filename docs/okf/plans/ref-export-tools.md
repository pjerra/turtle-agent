---
type: Reference
title: plan-export, plan-diff, route-edits
description: Every flag, default, and exit code of the export, diff, and route edit tools.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/plan_export_cmd.go
tags: [plans, reference]
sources:
  - id: plan-export
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/plan_export_cmd.go
  - id: plan-diff
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/plan_diff_cmd.go
  - id: route-edits
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/route_edits_cmd.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T22:00:00+01:00
status: draft
---

# plan-export, plan-diff, route-edits

## plan-export

Usage: `turtle-agent plan-export [--dir dungeons] [--allow-draft] [--drop-companions-loot] [--carrier-chests] [--gm-cross-direct] [--approach-hops] [--floor-aware] [--skip-legs NAME,...] [--with-optional NAME,...] [--with-legs NAME,...] [--mmaps DIR] [--rev SHA] [--stamp YYYYMMDDHHMMSS] [--force] --out FILE|DIR (<plan>... | --all-live)`[^plan-export].

| Flag | Default | What it does |
| --- | --- | --- |
| `--dir` | `dungeons` | Folder with the plan files. |
| `--out` | _(required)_ | Migration file to write, or a folder (file then named `<stamp>_world.sql`). Example: `--out /tmp/20261105220000_world.sql`. |
| `--all-live` | `false` | Export every plan whose status is live-checked. |
| `--allow-draft` | `false` | Export a named plan whose status is draft (refused without). |
| `--mmaps` | _(empty)_ | Navmesh folder: cut hops over 60 yd into via points (without it: only listed). |
| `--rev` | _(empty)_ | Turtle-agent commit in `ai_dungeon_plan.source` (default: git rev-parse of the plan folder). |
| `--stamp` | _(empty)_ | Migration timestamp `YYYYMMDDHHMMSS` (default: now, UTC). |
| `--drop-companions-loot` | `false` | Export a `companions_loot: false` leg anyway, without it (bots' looting must be off in the setup). |
| `--carrier-chests` | `false` | With it, export such a leg's chest steps as carrier rows (needs `--drop-companions-loot`). |
| `--gm-cross-direct` | `false` | Export a `gm_cross` waypoint as a `move_direct` instead of refusing the plan. |
| `--approach-hops` | `false` | With `--mmaps`, also cut the hop to a pull, boss, or hold over 50 yd into move rows. |
| `--floor-aware` | `false` | With `--mmaps`, cut hops the floor-aware way instead. |
| `--skip-legs` | _(empty)_ | Leg names (comma list) left out of the rows. Example: `--skip-legs farraki_arena,zeljeb`. |
| `--with-optional` | _(empty)_ | Optional leg names written into the rows anyway. Example: `--with-optional gahzrilla`. |
| `--with-legs` | _(empty)_ | `skip_export` leg names written into the rows anyway. |
| `--force` | `false` | Let `--out` replace an existing file (never edit an applied migration). |

Exits: 0 exported (a `CUT:` line on stdout still means legs were cut — read why); 2 usage. A draft without `--allow-draft`, an unknown leg name in `--skip-legs`/`--with-optional`/`--with-legs`, or a `companions_loot: false` leg without `--drop-companions-loot` refuses the plan.

## plan-diff

Usage: `turtle-agent plan-diff <old> <new> [--plan NAME] [--json] [--out DIR] [--svg FILE] [--minimaps DIR] [--picture worldmap|minimap|navmesh|none] [--fail-on-change]`[^plan-diff]. Flags may come before, between, or after the two inputs.

| Flag | Default | What it does |
| --- | --- | --- |
| `--plan` | _(empty)_ | The plan to compare when a migration holds several. Example: `--plan zulfarrak`. |
| `--json` | `false` | Print the `tb-plan-diff` JSON report instead of text. |
| `--out` | _(empty)_ | Write `diff-<plan>.txt`, `.json`, and `.svg` there (refused when a file exists). |
| `--svg` | _(empty)_ | Also write the SVG route picture to this file. |
| `--minimaps` | `<home>/turtle-agent-minimaps` | Folder with the map pictures. |
| `--picture` | `worldmap` | Picture under the route: `worldmap`, `minimap`, `navmesh`, or `none`. |
| `--fail-on-change` | `false` | Exit 25 when there is any change other than a comment. |

Exits: 0 (no change, or changes without `--fail-on-change`); 25 changes with `--fail-on-change`; 1 an input file is unreadable; 2 usage (different maps, unknown picture, bad plan name).

## route-edits

Usage (`list | export | promote`):

```text
turtle-agent route-edits list [--edit-dir DIR] [--json] [<plan>]
turtle-agent route-edits export [--edit-dir DIR] [--config-dir DIR] --to DIR <plan> [vN|latest]
turtle-agent route-edits promote [--edit-dir DIR] [--message-file FILE] --worktree PATH <plan> vN
```

[^route-edits]

| Flag | Default | What it does |
| --- | --- | --- |
| `--edit-dir` | `<home>/turtle-agent-route-edits` | Where the route editor wrote its versions. Applies to all three subcommands. |
| `--json` | `false` | `list`: print the records as JSON. |
| `--config-dir` | `<home>/.config/turtle-agent` | `export`: folder with the slot settings (which folders a slot uses; guards overlap). |
| `--to` | _(required)_ | `export`: fresh folder to write `<plan>.json` into (must not exist or be empty). |
| `--worktree` | _(required)_ | `promote`: worktree whose `dungeons/<plan>.json` is replaced. |
| `--message-file` | _(empty)_ | `promote`: also write the commit message to this file. |

Exits: 0; 2 usage; 1 the store cannot open, a version is missing or refused, the target overlaps a running slot, or the base no longer matches the worktree file.
