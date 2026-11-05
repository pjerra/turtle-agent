---
type: Reference
title: raid-suite reference
description: Every raid-suite flag, the suite file format, the verdicts and the exit codes.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/raid_suite_cmd.go
tags: [raids, raid-suite, reference]
sources:
  - id: suite-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/raid_suite_cmd.go
  - id: compare
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/raidwalk/compare.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:50:00+01:00
status: draft
---

# `raid-suite` reference

Usage: `turtle-agent raid-suite (--suite FILE | --dir DIR) --history DIR [--server <server>] [--slot N] [--only p1,p2] [--speed X] [--gap 60s] [--out DIR] [--dash-url URL] [--dash-user U] [--dash-pass-file PATH] [--mmaps DIR] [--slots FILE] [--binary PATH] [--compare-only] [--dry-run] [--json]`. No subcommands.[^suite-cmd]

## Flags

| Flag | Default | What it does | Example |
|---|---|---|---|
| `--suite` | none | The `tb-raid-suite` v1 file: its raids, in order. Exactly one of `--suite` and `--dir` is required. | `--suite <file>` |
| `--dir` | none | A folder of plan files: every raid plan in it, by plan name. | `--dir <plans-dir>` |
| `--history` | none (required) | The history dir: each walk lives at `<history>/<plan>/<stamp>/`. | `--history <dir>` |
| `--server` | the agent default | The slot server. | `--server <server>` |
| `--slot` | any free slot | The slot id; `3` is refused before anything (exit 15). `--slot A,B` is refused: the suite walks one raid at a time. | `--slot 2` |
| `--only` | every raid | Plan names to walk, comma separated. An unknown name is a usage error. | `--only <plan-a>,<plan-b>` |
| `--speed` | `1.0` | The run's test speed, 1.0 to 3.0. | `--speed 3.0` |
| `--gap` | `60s` | The pause between two raids, 0 to 10m. | `--gap 0s` |
| `--out` | `./raid-suite-<stamp>` | The report dir, refused if it exists. Mode 0700; `suite.json` and `suite.txt` mode 0600. | `--out <dir>` |
| `--dash-url`, `--dash-user`, `--dash-pass-file` | as `raid-walk` | The dashboard capture, forwarded to each `raid-walk`. | `--dash-url <url>` |
| `--mmaps` | none | The navmesh dir for slot setup. | `--mmaps <nav-dir>` |
| `--slots` | slots file | The slots file. | `--slots <file>` |
| `--binary` | this executable | The agent binary slot setup copies. | `--binary ./turtle-agent` |
| `--compare-only` | off | Walk nothing: compare each plan's newest walk in the history with the last good one before it. A plan with no walk is `skipped`. | `--compare-only` |
| `--dry-run` | off | Print each raid's tier and until, send nothing. | `--dry-run` |
| `--json` | off | Print the report as JSON on stdout (`suite.json` is written either way). The walks' own output goes to stderr. | `--json` |

## Suite file

Format `tb-raid-suite` v1, walked in file order (`dir` is required, `sql` and `hires` are optional):

```json
{"format":"tb-raid-suite","version":1,"raids":[
  {"plan":"<plan>","dir":"<plans-dir>","sql":"<world-sql>","hires":""}]}
```

`--dry-run` prints `<plan> tier <N> (<source>) until <until> out <history>/<plan>/<stamp>` per raid, or `<plan>: refused: <reason>`.

## Verdicts and exit codes

One raid's verdict: `ok`, `regression`, `new` (exit 0, no baseline), `failed` (did not exit 0), `refused` (a raid with no sourced tier, a non-raid plan, a duplicate plan), or `skipped`. A walk is a `regression` when its run time is more than 20 % and more than 60 s above the baseline, its deaths are above `base + max(2, 25 % of base)`, its pauses or stops are above the baseline, its end reason changed, or its fights are more than one above it. `faster` and `fewer fights` are info. Rows that changed since the baseline (`rows_sha256`) are a note; the rules still apply.[^compare]

Exit 0 when no raid regressed or failed; 24 when one did; 15 for `--slot 3`; 2 for usage.
