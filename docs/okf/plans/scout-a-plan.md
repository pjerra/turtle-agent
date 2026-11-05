---
type: Guide
title: Scout a plan before a run
description: Run the offline plan scout pass and read its report and fix file.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/plan_scout_cmd.go
tags: [plans, scout]
sources:
  - id: plan-scout
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/plan_scout_cmd.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:50:00+01:00
status: draft
---

# Scout a plan before a run

`plan-scout` checks a plan's exported rows offline — placement lints, the four scout rules, and the joiner estimate — and writes a report plus a fix file[^plan-scout].

## 1. Run the offline pass

```sh
turtle-agent plan-scout <plan> --mmaps <mmaps-dir>
```

The scout reads `dungeons/<plan>.json` by default (`--dir` changes the folder) and the map's world export (`--spawns`, defaulting to the `tools/dungeongen` export for the map).

## 2. Limit the legs under review

```sh
turtle-agent plan-scout <plan> --mmaps <mmaps-dir> --from <leg> --until <leg>
```

`--from` and `--until` take an exported leg number or a leg name; only that range is reported.

## 3. Save the report and the fixes

```sh
turtle-agent plan-scout <plan> --mmaps <mmaps-dir> --out <dir> --fix-out <file>
```

Fixes are `tb-scout-fix v1`, one per origin: `snap_point`, `insert_via` (within 50 yd, 6 yd of climb), `dry_detour`, `move_stand` (24 bearings, 15-55 yd), and `split_pull`. Coordinate-only fixes carry `apply: false`. `--apply-fixes` never writes in place: it writes the patched plan copy plus its diff, and refuses a folder that exists non-empty, overlaps `dungeons/`, or a slot plan. A fix that adds an issue refuses the whole apply (exit 27, nothing written). With `--sql`, the migration must equal the plan's own export (`rows_sha256`) or the run exits 2.

## 4. Decide what fails the run

`--fail-on fail` (the default) fails on a failing step verdict; `--fail-on warn` treats warnings as failures too. `--party-level N` sets the party level for the aggro radius (default: the plan band's high end).

## 5. Run the live pass when the offline one is clean

```sh
turtle-agent plan-scout <plan> --mmaps <mmaps-dir> --live <server> --slot <n>
```

The live pass runs after the offline pass on a test server only (the exact names in the usage line; any other name is refused before anything is sent). It leases one slot (never slot 3), turns the GM on and invisible, unbinds the instance, and walks the route with `.bot scout cfg|path|walk|where` plus `.tt q inst|units|los`. Server paths run per hop origin (8 in flight, id-matched, 1 retry); stands, anchors, and pulls are queried per point (hops past 120 yd get their own `.go`). The walk budget (`--walk-budget`, default `5m`) bounds the walk and `--max-minutes` (default 20) the whole pass. Findings merge by source: `live_path` (swim type `0x80`), `live_walk` (stop plus anchor check), `live_aggro`, `live_los`, and `live_joiners` replace the offline verdict. The pass writes `<out>/scout.log` with every scout line. `--slot` picks the slot (default 0), `--holder` the lease name, `--slots` a slot file, `--binary` the server binary. Exit 26 means the instance cap fired before anything started (nothing ran); exit 28 means the live pass aborted part-way (report and fixes are still written). `--fail-on warn` also fails on warnings. The instance-cap flag wins over the per-server JSON file when both are given.
