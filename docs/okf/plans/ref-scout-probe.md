---
type: Reference
title: plan scout, plan-probe, patrol-record
description: Every flag, default, and exit code of the scout, probe, and patrol recorder.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/plan_scout_cmd.go
tags: [plans, reference]
sources:
  - id: plan-scout
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/plan_scout_cmd.go
  - id: plan-probe
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/plan_probe_cmd.go
  - id: patrol-record
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/patrol_record_cmd.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:58:00+01:00
status: draft
---

# plan scout, plan-probe, patrol-record

## plan-scout

Usage (offline and live flags in one set):

```text
turtle-agent plan-scout <dungeon> [--dir dungeons] [--sql FILE] [--mmaps DIR] [--spawns DIR]
    [--from LEG] [--until LEG] [--party-level N] [--out DIR] [--json]
    [--fix-out FILE] [--apply-fixes DIR] [--fail-on fail|warn]
    [--live <server>] [--slot N] [--holder NAME] [--slots FILE] [--binary PATH]
    [--walk-budget 5m] [--max-minutes 20] [--instance-cap-per-account N] [--instance-cap-wait 20m] [--instance-cap-fail] [--instance-cap-reset]
```

[^plan-scout]

| Flag | Default | What it does |
| --- | --- | --- |
| `--dir` | `dungeons` | Folder with the plan files. |
| `--sql` | _(empty)_ | Check a deployed migration's rows instead of the plan file's export. |
| `--mmaps` | `<home>/turtle-agent-mmaps` | Folder with the server's mmaps. |
| `--spawns` | _(empty)_ | The map's world export (default `<dir>/../tools/dungeongen/testdata/mapN`). |
| `--from` | _(empty)_ | First leg to report (exported leg number or leg name). Example: `--from 3`. |
| `--until` | _(empty)_ | Last leg to report (inclusive). |
| `--party-level` | `0` | Party level for the aggro radius (default: the plan band's high end). |
| `--out` | _(empty)_ | Folder for `scout-<plan>.json` and `.txt` (default `./plan-scout-<plan>-<stamp>`). |
| `--json` | `false` | Print the report as JSON. |
| `--fix-out` | _(empty)_ | Write the fix file (`tb-scout-fix` v1) here, also with no fixes. |
| `--apply-fixes` | _(empty)_ | Write the patched plan copy and its diff to this folder (must not exist or be empty). |
| `--fail-on` | `fail` | `fail` or `warn`: which step verdict fails the run. |
| `--live` | _(empty)_ | Run the live pass on this test server. |
| `--slot` | `0` | The slot to use (3 is never allowed). |
| `--holder` | _(empty)_ | Lease holder's name (default `plan-scout-<plan>`). |
| `--slots` | _(empty)_ | The slots file. |
| `--binary` | _(empty)_ | The server binary path. |
| `--walk-budget` | `5m` | Live walk budget. |
| `--max-minutes` | `20` | Live time ceiling in minutes. |
| `--instance-cap-per-account` | `5` | Core's limit of fresh instance ids one account may enter in an hour. |
| `--instance-cap-wait` | `20m` | How long to wait for a free instance place. |
| `--instance-cap-fail` | `false` | Fail when the instance ledger is at its cap. |
| `--instance-cap-reset` | `false` | Reset the instance ledger entry. |

Exits: 0 clean; 27 a step fails (or warns with `--fail-on warn`); 28 the live pass stopped part-way; 18 live-phase refused; 15 slot 3; 1 the navmesh cannot answer; 2 usage.

## plan-probe

Usage: `turtle-agent plan-probe [--dir dungeons] [--sql FILE [--plan NAME]] --mmaps DIR [--json] [--out DIR] [--from-step N] [<plan>|<path.json>]`[^plan-probe].

| Flag | Default | What it does |
| --- | --- | --- |
| `--dir` | `dungeons` | Folder with the plan files. |
| `--sql` | _(empty)_ | A deployed migration; the recommended input. |
| `--plan` | _(empty)_ | With `--sql`: the plan to probe (needed when the file holds several). |
| `--mmaps` | _(required)_ | Folder with the server's mmaps. |
| `--json` | `false` | Print the report as JSON. |
| `--out` | _(empty)_ | Folder for `probe-<plan>.json` and `probe-<plan>.txt`. |
| `--from-step` | `0` | Only the segments ending at this run-leader step or later. Example: `--from-step 40`. |

Exits: 0; 20 at least one segment is bad; 1 the navmesh could not answer; 2 usage.

## patrol-record

Usage: `turtle-agent patrol-record <slot-dir> --map M --area x,y,r [--z Z] --minutes N [--sample-ms 1000] [--out FILE] [--spawns DIR] [--mmaps DIR] [--entries e1,e2]`[^patrol-record].

| Flag | Default | What it does |
| --- | --- | --- |
| `--map` | _(required)_ | Map id, numeric. Example: `--map 249`. |
| `--area` | _(required)_ | The circle to watch, `x,y,r`. Example: `--area 100,-200,60`. |
| `--z` | _(empty)_ | The floor's z; needed when the navmesh has several floors at `x, y`. |
| `--minutes` | _(required)_ | Recording length, 1–60 minutes. Example: `--minutes 10`. |
| `--sample-ms` | `1000` | Milliseconds between samples (500–5000). |
| `--out` | _(empty)_ | Output file (default `./patrols-<map>-<stamp>.json`); refused when it exists. |
| `--spawns` | _(empty)_ | World export folder with `creatures.tsv`, to name the patrols' spawns. |
| `--mmaps` | _(empty)_ | Navmesh folder, to find `z` when `--z` is absent. |
| `--entries` | _(empty)_ | Comma-separated creature entries to record (default: every creature). Example: `--entries 12129,10184`. |

Exits: 0 recorded; 22 stopped for safety; 15 slot refused or the agent is gone; 16 an RPC error; 2 usage.
