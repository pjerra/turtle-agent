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

The gate: the check refuses a live-checked plan that fails, cannot run, or starts from a bad leg unless you pass `force` (then it notes `forced past ...`). A draft only warns (`warning (... draft ... so it runs)`) and runs on. Leader, group, observe, and pull-test runs are never checked — only the commander's remote run is.

Keys: a locked object needs its item in `requires_item`, and every `requires_item` needs a maker (an earlier step, a boss `loot_item`, or an `optional` leg's item). A lock without an item fails with `no-item`; see [Plan troubleshooting](troubleshooting.md).

## 2. Start from a later leg when iterating

```sh
turtle-agent plan-check --mmaps <mmaps-dir> --from-leg 3 <plan>
```

`--from-leg N` checks from leg `N` on; earlier legs only place the party. With it, `--from-pull-group N` (0-based) starts inside that leg's groups.

## 3. Read the lints

Pull groups and bosses are numbered like the run status (`pull group 9 of 12`). Looks use a 21 yd standoff, never past arrival (3 yd), and never farther than 25 yd without a look. The check re-anchors the next point onto the mesh and reports `re-anchored after step N` plus `N of M (R root, K re-anchored)`; it never cascades. Step stands (use, loot, ritual, trigger, gossip before and after the boss) count as `via` points. A trigger stand must sit in its zone and within 30 yd of the boss; a gossip stand sits within 5 yd of `near` and never moves the party for agent gossip. Team-gated packs are checked and named.

Run-leader-only rows (approach points, hold spots from the leader point) are checked as `via` (`run leader pull group 3 approach 0`). Every complete offline walk is also asked of the server (2048 nodes, 256 polygons, ground and water, steep and smooth walks up to 256 yd) and a `server unreachable` walk continues from the server's end. A direct step within 3 yd of the previous point reports `hop inside arrival radius`. The path-check replay prints two lines per hop: the plan path and the server path, each with type, verdict, end, and distances.

Lint thresholds: off-floor fails past 1.5 yd; edge warns within 2.5 yd (4 yd for gather and stand rows); ledge spans 4.5-8 yd down to 4.5; short warns under 8 yd (arrival is 3); aggro uses the level-60 detection radius of 20 yd plus the export's 8; `boss_aggro` fails on the named bosses (own approach, boss entry, and after-boss rows exempt); hop length warns past 120 yd and fails past 140; arrival warns past 3 yd and fails past 6; a stand more than 8 yd above its anchor fails `climb_anchor`; joiners warn at 1-2 and fail at 3 or over the item. Water crossings must be one hop from dry to dry.

Aggro, patrol, and joiner lints need `--spawns` (default `<dir>/../testdata/mapN`); without it they are skipped with a note.

## Limits

The check snaps bot heights within 2-3 yd, misses swim shortcuts between dry rows, and asks the server only at walk starts and ends. A server-unreachable point inside the 25 yd ring may still save you live.

## CI gate

CI turns every error lint into a warning count and only a failing point fails the build; `--strict-lints` fails on lints. Statuses never change from a check run.

## 4. Check a deployed migration instead

```sh
turtle-agent plan-check --mmaps <mmaps-dir> --sql <migration.sql> [--plan <name>]
```

This lints the rows of a world migration — the exact rows the server runs — instead of a plan file. `--plan` picks the plan when the file holds several.

## 5. Fix or exempt

Move a failing point onto the mesh. When the hop is unwalkable on purpose (a jump down the mesh does not link), exempt it with `"check_skip": "why"` on the waypoint — the check lists it as skipped with your reason instead of failing it. When a spawn truly stands off-mesh, mark it `needs_walk` and put it on the mapping walk's list. A run start places the party at `LegStart` or `GatherSpot`, so earlier legs stay unwalked. A complete near-straight hop warns as unneeded `direct` (re-send it plain). Known-failing drafts stay listed in `planCheckKnownFailing`; a pass is not a promise — fight drag and server-only refusals stay unseen.
