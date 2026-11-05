---
type: Reference
title: route-oracle
description: Every flag, default, exit code, and report file of route-oracle.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/route_oracle_cmd.go
tags: [plans, reference]
sources:
  - id: route-oracle
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/route_oracle_cmd.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T22:00:00+01:00
status: draft
---

# route-oracle

Usage: `turtle-agent route-oracle --plans DIR --mmaps DIR --out DIR [--mode walk|bosses] [--objectives plan|bosses] [--plan NAME]...`[^route-oracle]. Flags come before any other word: this command uses plain flag parsing, which stops at the first non-flag. On/off flags: none on this page.

| Flag | Default | What it does |
| --- | --- | --- |
| `--plans` | `dungeons` | Folder with the plan files. Example: `--plans dungeons`. |
| `--mmaps` | _(required)_ | Folder with the server's mmaps. |
| `--out` | _(required)_ | Folder for `<plan>.txt` and `<plan>.json` (`<plan>-bosses.*` in bosses mode, `<plan>-bosses-only.*` with `--objectives bosses`). |
| `--objectives` | `plan` | Bosses mode only. `plan`: each leg's own objective (its boss, else its `to` point). `bosses`: only the chain entrance to boss 1 to boss 2 (a leg without a boss folds into the next). |
| `--mode` | `walk` | `walk`: score the plan's stored hops on the navmesh. `bosses`: plan entrance to each boss live, with no hand points. |
| `--plan` | _(repeatable)_ | A plan name; repeatable (default: `wailing_caverns,ragefire_chasm,uldaman,zulfarrak,deadmines`). Example: `--plan zulfarrak --plan uldaman`. |

## Rules

`--objectives bosses` needs `--mode bosses` (the walk mode plans the plan's own stored sequence). Every leg is planned twice — plain, and on a scratch planner with the plan's avoid boxes — and the report's metrics use the avoid-aware path.

## Reports

`<out>/<plan>.txt` is the human table; `<out>/<plan>.json` the machine report. Per leg, in driver order: the `via` waypoints, pull group positions, boss, and step stands, each with its plain and avoid-aware lengths and their ratio. A leg reports `complete`, `partial`, `no_path`, `no_objective`, or `indeterminate`.

Exits: 0 all reported; 12 the oracle could not report every plan (a failing plan is not a usage error); 2 usage.
