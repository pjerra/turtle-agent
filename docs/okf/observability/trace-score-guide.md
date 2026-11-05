---
type: Guide
title: Re-score a trace
description: The trace-score flags, its output, and its exit codes.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/trace_cmd.go
tags: [traces, trace-score]
sources:
  - id: trace-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/trace_cmd.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:48:00+01:00
status: draft
---

# Re-score a trace

`turtle-agent trace-score` reads trace files and prints each check's verdict with its measured value and threshold. It logs in to nothing.

## 1. Score one or more traces

```sh
turtle-agent trace-score <walk-dir>/traces/r-20261014T093012-a1b2.jsonl
turtle-agent trace-score --json <walk-dir>/traces/live-1.jsonl
```

Every flag and its default:

| Flag | Default | What it does |
| --- | --- | --- |
| `--json` | off | print the verdicts as JSON, one object per trace (`run`, `file`, `verdict`, `failed`, `checks`, `spells`) |
| `--summary` | off | write `<run_id>.summary.json` beside each trace |
| `--set` | none | change a threshold: `check=value`, repeatable |

A bad `--set` fails at once: `--set "rest_share"` prints `--set "rest_share": want check=number`; an unknown check prints `--set: no threshold for check "nope" (have rest_share, ...)`. Exit codes: 0 when no check failed, 11 when one did, 2 on usage.

## 2. Read the text output

The first line is `<run> <source> <mode> <plan> <seconds> s <VERDICT>`, then one line per check (`PASS`, `FAIL`, or `NA` with the reason), then the spell report lines. `spells`, `spell_oracle` and `oom_in_fight` are report only: they print `report:` lines and never fail.

## 3. Re-score after a change

```sh
turtle-agent trace-score --summary --set rest_share=30 <walk-dir>/traces/live-1.jsonl
```

The scorer is a pure function of the file, so an old trace can be scored again after a threshold or a check changes. `--summary` records the new verdict where the dashboard reads it.
