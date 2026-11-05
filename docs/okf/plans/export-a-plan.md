---
type: Guide
title: Export a plan
description: Write a TortoiseBots world migration with plan-export and compare two exports with plan-diff.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/plan_export_cmd.go
tags: [plans, export]
sources:
  - id: plan-export
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/plan_export_cmd.go
  - id: plan-diff
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/plan_diff_cmd.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:58:00+01:00
status: draft
---

# Export a plan

`plan-export` writes a plan as one idempotent world migration for TortoiseBots' run leader; `plan-diff` compares two exports of one plan[^plan-export].

## 1. Export one plan

```sh
turtle-agent plan-export --out <file.sql> <plan>
```

Put flags before the plan names here too (plain flag parsing). On/off flags (every `false` default in the plan-export table) take `=` when you give a value.

`--out` is required: a file, or a folder (the file is then named `<stamp>_world.sql`). Only TortoiseBots' updater takes a `YYYYMMDDHHMMSS_world.sql` name — anything else gets a note on stderr. `--out` never replaces an existing file; pass `--force` to overwrite (never edit a migration that is applied).

## 2. Export a mapped plan on the navmesh

```sh
turtle-agent plan-export --mmaps <mmaps-dir> --out <file.sql> <plan>
```

With `--mmaps`, hops of more than 60 yd between two moves are cut into via points along the navmesh path (without it: only listed). Add `--approach-hops` to cut the hop to a pull, boss, or hold too, or `--floor-aware` for the floor-aware cut instead.

## 3. Handle drafts, loot, and special legs

- A draft is refused unless you pass `--allow-draft`; `--all-live` exports every live-checked plan instead of named ones.
- A `companions_loot: false` leg is refused unless you pass `--drop-companions-loot` (add `--carrier-chests` to export its chest steps as carrier rows).
- `--skip-legs a,b` leaves legs out; `--with-optional gahzrilla` writes an optional leg in anyway; `--with-legs farraki_arena` writes a `skip_export` leg in anyway. Unknown names are refused.

## 4. Read the CUT line

A step the run leader cannot do cuts the export at its leg: stdout ends with a `CUT:` line, stderr says which leg and why, and the plan row's comment records the cut. The migration still holds every leg before the cut.

## 5. Diff two exports

```sh
turtle-agent plan-diff <old> <new> [--plan <name>]
```

Each side is a `.sql` migration or a `.json` plan. The text lists every moved, changed, added, and removed row; `--json` prints the machine report; `--out DIR` writes `diff-<plan>.txt`, `.json`, and `.svg`; `--svg FILE` writes the route picture (over `--picture worldmap|minimap|navmesh|none`). `--fail-on-change` exits 25 on any non-comment change.
