---
type: Reference
title: raid-walk report reference
description: The report command's flags, inputs, the per-pull table and report.json.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/raid_walk_report_cmd.go
tags: [raids, report, reference]
sources:
  - id: report-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/raid_walk_report_cmd.go
  - id: report
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/raidwalk/report.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:50:00+01:00
status: draft
---

# `raid-walk report` reference

Usage: `turtle-agent raid-walk report <dir> [--spell ID[,ID]...] [--window 5s] [--clock-offset 1h] [--log FILE] [--fights FILE] [--md FILE] [--json FILE] [--out DIR]`. It reads a finished walk's folder and turns it into the per-pull table the walks otherwise build by hand. It reads files only: no server, no login, no settings or token file.[^report-cmd]

## Flags

| Flag | Default | What it does | Example |
|---|---|---|---|
| `<dir>` | required | The finished walk's folder. | `report <walk-dir>` |
| `--spell` | none | An enemy spell id to list the casts of (repeatable, or a comma list). | `--spell <id>` |
| `--window` | `5s` | A death this soon after a watched cast counts as its death. Must be positive. | `--window 10s` |
| `--clock-offset` | `0` | The run log's clock is UTC plus this (the servers run UTC+1: `1h`); every time in the report is UTC. A log has no date: the day is the latest one that puts its first line within 5 minutes of the first fight. | `--clock-offset 1h` |
| `--log` | `<dir>/run.log` | The run log. The table still comes without one, with the status columns empty. | `--log <file>` |
| `--fights` | none | A file of many fights (id to fight, or a list) in place of `<dir>/combat-fight-*.json` (or `<dir>/fights-full.json`). | `--fights <file>` |
| `--md` | none | Write the Markdown here. | `--md <file>` |
| `--json` | none | Write the JSON here; `--json -` prints it instead of the Markdown. | `--json -` |
| `--out` | none | Write `report.md` and `report.json` into this folder. Markdown goes to stdout unless `--md`, `--json` or `--out` name files. | `--out <walk-dir>/report` |

## The table

One row per fight: step, fight id and kind, boss, start, duration, outcome, the engaged units by name and count, the deaths (name, seconds into the fight, killer), **contact before pull** (`YES` when a status line of the step says the tank engages a unit "on arrival" before any `pulling` line of the step), **joined mid-fight** (the `a new unit joined` status lines inside the fight with seconds into the fight, plus the frames' own count of engaged hostiles outside the pulled pack within 10 s of the pull, from `pulljoiners.json`), and the **stand wait** (from the first stand-pull status line of the pull to its `pulling` line, or to the fight's start).[^report]

Below the table: the stand-pull status lines of each pull merged by text, the pauses, resumes (with how long each pause lasted), stops and the end of the run, and the watched casts. A record outside every window is left off the table and counted in a `joiners unmatched` note.

`report.json` (format `tb-raid-walk-report` v1) carries the same rows: `pulls[]` (`engaged`, `deaths`, `contact_before_pull`, `neighbour_joins`, `joiners_seen`, `joiner_guids`, `stand_wait_s`, `casts`), `control[]`, `casts[]`, `stand_pull_unattached[]`, `totals` and `notes[]`.
