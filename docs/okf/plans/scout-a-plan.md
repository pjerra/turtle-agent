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

`--out` writes `scout-<plan>.json` and `scout-<plan>.txt`; `--json` prints the report as JSON instead of text. `--fix-out` writes the fix file (`tb-scout-fix v1`), even when there are no fixes. `--apply-fixes <dir>` writes the patched plan copy and its diff to a folder that must not exist or be empty.

## 4. Decide what fails the run

`--fail-on fail` (the default) fails on a failing step verdict; `--fail-on warn` treats warnings as failures too. `--party-level N` sets the party level for the aggro radius (default: the plan band's high end).

## 5. Run the live pass when the offline one is clean

```sh
turtle-agent plan-scout <plan> --mmaps <mmaps-dir> --live <server> --slot <n>
```

The live pass walks the route on a test server (pass `--live <server>`; without it only the offline pass runs). The server must be a test server — any other name is refused. Slot 3 is never used. `--walk-budget` (default `5m`) bounds the walk and `--max-minutes` (default 20) the whole pass. The instance-cap flags (`--instance-cap-per-account`, `--instance-cap-wait`, `--instance-cap-fail`, `--instance-cap-reset`) handle the core's fresh-instance limit.
