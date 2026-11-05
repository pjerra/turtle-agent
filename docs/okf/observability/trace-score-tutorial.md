---
type: Tutorial
title: Check a run with trace-score
description: Score one trace, tune one threshold, and record the verdict beside the trace.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/trace_cmd.go
tags: [traces, trace-score, tutorial]
sources:
  - id: trace-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/trace_cmd.go
  - id: tracecheck
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/tracecheck/tracecheck.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:47:00+01:00
status: draft
---

# Check a run with trace-score

A dungeon run scores its own trace when it ends. This tutorial re-scores one by hand: run the scorer, tune a threshold, and write the summary file back beside the trace.

## 1. Score the trace

```sh
turtle-agent trace-score <walk-dir>/traces/r-20261014T093012-a1b2.jsonl
```

One line per check, e.g.:

```text
r-20261014T093012-a1b2  dungeon_run  group  uldaman  1843 s  PASS
  PASS rest_share       ...
  FAIL stuck            ...
```

The first line names the run, the source, the mode, the plan, the length, and the verdict. Exit code 11 means a check failed; 0 means none did.

## 2. Tune one threshold

Say the run rested long but played fine, and `rest_share` (default 40 %) failed it. Re-score with a looser bound:

```sh
turtle-agent trace-score --set rest_share=60 <walk-dir>/traces/r-20261014T093012-a1b2.jsonl
```

`--set` takes `check=number` and may repeat. The scorer reads nothing but the file, so an old trace can be re-scored after a threshold or a check changes. The tunable checks are `rest_share`, `stale_party`, `walk_stops`, `threat_off_tank`, `order_refusals`, `feed_stalls`, `stuck`, `facing`, and the spell-warning limits `idle_share`, `interrupt_cover`, `taunt_latency`, `cc_hold`, `pet_threat`, `spell_oracle`, `oom_in_fight`. `deaths` and `gm_shortcuts` have no threshold.

## 3. Write the verdict back beside the trace

```sh
turtle-agent trace-score --summary <walk-dir>/traces/r-20261014T093012-a1b2.jsonl
```

`--summary` writes `<run_id>.summary.json` next to the trace, so the dashboard lists the new verdict without parsing the trace. The file is written whole and atomically.
