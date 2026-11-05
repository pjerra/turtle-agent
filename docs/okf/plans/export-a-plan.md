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

With `--mmaps`, hops of more than 60 yd between two moves are cut into via points along the navmesh path (without it: only listed). Cut points sit at most 50 yd apart. Add `--approach-hops` to cut the hop to a pull, boss, or hold too (past 50 yd from the anchor; the run leader's own hops are 8 of 40 yd, last at least 25 yd short), or `--floor-aware` for the floor-aware cut instead (hops up to 110 yd stay whole; corners under 8 yd apart and turns under 20 degrees get no row; kept corners move 3 yd off the wall; split points take the floor z and drop over dips or liquid beds with a warning each). A `leave` that clears 15 yd from every stand stays uncut with a warning (an island the mesh cannot link).

## 3. Handle drafts, loot, and special legs

- A draft is refused unless you pass `--allow-draft`; `--all-live` exports every live-checked plan instead of named ones.
- A `companions_loot: false` leg is refused unless you pass `--drop-companions-loot` (add `--carrier-chests` to export its chest steps as carrier rows).
- `--rev` names the commit in the plan row (default: `git rev-parse`); `--stamp` names the migration time (default: now, UTC).
- Heads-up warnings name each dropped field: skip, leave, leftover, rest, `needs_walk`, patrol, tactics, boss companions, dropped-leg mute, fleers, `no_cc`, summoned, hire, band, team, raid, watchdog, entrance teleports, avoid without mmaps, loot and ritual stands, spawn, range, batch, trigger expects and timeouts, agent-only steps, and a `first` under a hold. Never warned: name, version, status, kind, map, notes, `check_skip`, `equiv`, `phase_entries`.
- `--carrier-chests` (needs `--drop-companions-loot`) exports each chest as a triple: a move to the stand, the loot row (flag 256, gone-is-done), and the leftover sweep pull (radius 15, `sweep`). The setup still mutes looting, takes free-for-all with the key on the lead, and keeps 16 free bag slots.

## 4. Read the CUT line

A step the run leader cannot do cuts the export at its leg: stdout ends with a `CUT:` line, stderr says which leg and why, and the plan row's comment records the cut. The migration still holds every leg before the cut. `--all-live` carries on past the cut with exit 0. A bad first leg refuses the export outright.

## 5. Diff two exports

```sh
turtle-agent plan-diff <old> <new> [--plan <name>]
```

Legs match by `leg.name` first (an insert keeps its steps together), then by number; an unmatched leg is added or removed with its steps. Steps match by kind, entry, entries, radius, item, param, flags, and `timeout_s`, plus position within 0.1 yd (float32 noise never counts), then longest common subsequence. A comment-only change is a comment (`--fail-on-change` ignores it). Between matched pairs, a move past 0.05 yd 3D is moved (with the distance and both points); else a field change is changed; else removed and added. Global numbers count `(leg, seq) + 1`; an insertion counts 1 added and renumbered rows are counted (first 5 in the notes). Plan fields (`cc_priority`, `cc_always`, `kill_first`, source, comment) report under `plan:`.

`--out DIR` writes `diff-<plan>.txt`, `.json`, and `.svg` (0700 dirs, 0600 files) and refuses existing files. The SVG draws the old route grey-dashed and the new solid, with green adds, red crosses, orange arrows, purple rings, z in the labels, and hover text. `--minimaps` (default `~/turtle-agent-minimaps`) feeds `--picture worldmap|minimap|navmesh|none`; a missing picture stays blank with a note, and a world map is 2D (z decides).
