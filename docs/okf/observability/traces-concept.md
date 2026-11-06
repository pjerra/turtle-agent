---
type: Concept
title: Run traces
description: The bt-trace file a run writes, what each record holds, and the summary beside it.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/trace/trace.go
tags: [traces, concept]
sources:
  - id: trace
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/trace/trace.go
  - id: tracerun
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/tracerun/tracerun.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:43:00+01:00
status: draft
---

# Run traces

Every `dungeon_run` (group and remote) and every brain session has a run id (`r-<utc yyyymmddThhmmss>-<4 hex>`, e.g. `r-20261014T093012-a1b2`, or your own letters, digits, `.`, `_`, `-`, at most 64) and writes a **trace**: one JSONL file `<traces_dir>/<run_id>.jsonl` plus `<run_id>.summary.json` beside it, in the shape a TortoiseBots dashboard reads, so the two files of a run join on the run id.

On by default; `"trace": false` in the run call writes no file (the run id stays). A trace that cannot be opened never stops the run: the report's `trace.error` says why.

## Records

Every line starts with the same envelope: `v` (schema version, 1), `k` (the kind), `run`, `src` (`agent` here), `t` (ms since the run started). GUIDs are `0x` lower-case hex strings. A reader ignores kinds, fields and events it does not know, and refuses a file whose `v` is higher than it reads.

| `k` | Written | Fields |
| --- | --- | --- |
| `header` | once, first | `schema` (`bt-trace`), `scope` (`party`), `requester`, `start_unix`, `interval_ms`, `max_bytes`, `map`, `caps`, plus the agent's `source` (`dungeon_run`, `brain`), `mode` (`group`, `remote`), `plan`, `from_leg`, `start`, `lead`, `tank`, `options`, `meta` |
| `party` | per member, when first seen | `g`, `name`, `class`, `role` (`tank`/`heal`/`dps`, only where known), `level` |
| `mobs` | per hostile, first time in a frame (at most 1024) | `g`, `entry`, `name`, `level` |
| `frame` | about 1 Hz | `bots[]` (alive, combat, state, hp/power, `x y z`, movement, target, facing, spell, last action) and `mobs[]` (hp percent, `x y z`, target, threat pairs) |
| `event` | as it happens | `ev` + flat fields (`x_` when colliding): `leg/step/leader`, `order` (`via` chat/cmd…), `refusal`, `pull_start/pull_end{outcome}`, `rest{seconds,leg,source}`, `instance`, `stall{level,stalled_s,last_progress,waiting,phase,recovery}`, `walk_start/walk_end`, `death/wipe`, `combat_on/off`, `gm_shortcut{kind,detail}`, `stuck/unstuck` (5 s), remote `rez/rejoin_tp/silent_death{spot}/lead_off_deadly/feed_stall/roster`, `note`, `brain/brain_state`, `trace_limit{dropping}` |
| `end` | once | `reason` (`done`, `failed`, `stopped`), `seconds`, `counters{}` |
| `oracle` | after the end, one per check | `id`, `name`, `verdict`, measured `value` against `threshold`, `evidence[]` |

`caps` says what the producer records (`facing`, `moving`, `threat`, `orders`, `walks`, `partylist`), so the scorer answers n/a instead of passing what it cannot see. A group run has no `threat`; a remote run has no `moving`.

## Size and retention

Frames come about once a second. At 32 MiB the frames stop with a `trace_limit` event; events continue for a quarter more, then stop; the end record and the checks are always written. The folder keeps the newest 200 traces of generated ids; ids you chose yourself are never deleted.

## The summary

`<run_id>.summary.json` is written when the run starts (`status: "running"`) and again, whole and atomically, when it ends and is scored: the same checks the trace holds, `verdict` (`fail` if any check failed, `pass`, `na`), the party, the counters, and the agent's `result`, `error`, `failed[]`. The dashboard lists summaries without parsing traces.

## Driver label and server trace

`run_meta{model,effort,run}` names the AI driver (call replaces slot default whole). With `server_trace: true` (off; needs model+effort+run) a remote run also stamps the server (`.bot trace start <id> driver=<m>:<e>:<r>` … `.bot trace stop` on every exit). A group run never sends it. The stop is retried (3× + end-of-run) unless refused `not-running`/`forbidden`; when all fail the report says to stop by hand. The server stop is unscoped: enable only where nobody hand-starts traces.

`trace_meta` (≤16 pairs) is copied into the header.
