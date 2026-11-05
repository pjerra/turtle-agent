---
type: Reference
title: Dungeon and party RPC
description: commander, dungeon_run, dungeon_stage, teardown, and bot_view params and results.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dungeon_methods.go
tags: [agent, rpc, reference, dungeon, commander, teardown]
sources:
  - id: dungeon-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dungeon_methods.go
    title: dungeon_run, plan_check, dungeon_status, dungeon_stop, pause, resume, skip
  - id: commander-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/commander_methods.go
    title: commander and bot_view
  - id: stage-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/stage_methods.go
    title: dungeon_stage
  - id: teardown-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/teardown_methods.go
    title: teardown
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T22:45:00+01:00
status: draft
---

# Dungeon and party RPC

Dungeon plans themselves live in `/dungeons/index.md`. These are the agent-side calls that run them.

## commander

`commander` drives a TortoiseBots bot party the agent is *not* in. `op` is `bind`, `unbind`, `status`, `view`, `cmd` or `run`.

| Field | What it does | Default | Example |
|---|---|---|---|
| `op*` | What to do: `bind`, `unbind`, `status`, `view`, `cmd` or `run` | — | `"cmd"` |
| `lead_bot` | Bind: the lead bot | — | `"Tanky"` |
| `bots` | Bind: the other bots (empty: its party as it is) | `[]` | `["Healy"]` |
| `on` | View: turn the `TBM:VIEW` feed on or off (required for `view`) | — | `true` |
| `verb` | Cmd: `attack`, `pull`, ...; run: `start`, `pause`, `resume`, `skip`, `stop`, `status`, `list` | — | `"attack"` |
| `args` | Cmd arguments (a GUID as `"0x"` + hex) | `[]` | `["0xF130..."]` |
| `wait_ms` | How long to wait for the reply | `4000` | `8000` |

Result: `{sent, reply{kind ack\|err\|timeout, ...}, order?}` (`order` only for the `loot`, `open`, `use` and `ritual` verbs). Wiring: sends `.bot <command>` as say. Errors: `bind needs lead_bot`; `view needs on true or false`; `op must be bind, unbind, status, view, cmd or run`.

## bot_view

Reads the commander's `TBM:VIEW` perception feed.

| Field | What it does | Default | Example |
|---|---|---|---|
| `after_n` | Only frames after this number | current | `12` |
| `wait_ms` | Wait for a newer frame (0: return current) | `0` | `2000` |
| `log_every` | Debug logging cadence (0–10000; omitted: unchanged) | unchanged | `100` |

Result: `{have, n, age_ms, frame?, stats, binding_ends, log_every, view_lines}`.

## dungeon_run

Runs a plan. `name` is required (lowercase letters, digits, `_`, `-`, at most 40 chars).

| Field | What it does | Default | Example |
|---|---|---|---|
| `name*` | Plan file name | — | `"deadmines"` |
| `mode` | `leader` (agent leads and plays), `commander` (agent commands bots from outside), `observe` (a bot run leader clears, agent watches). `remote`/`group` are the older names. | `leader` | `"commander"` |
| `lead_bot`, `bots`, `tank`, `healer`, `carrier`, `cc_bot` | Commander mode: the party | lead bot's party as it is | `"Tanky"` |
| `from_leg` | Start at this leg (number or name) | `0` | `"barracks"` |
| `from_pull_group`, `from_step`, `start_here` | Start inside a leg (observe: an exact run-leader step) | — | `3` |
| `teleport` | GM-summon the party to the start (flagged in the report) | `false` | `true` |
| `force`, `force_start` | Run past a failing plan check / an unsafe start | `false` | `true` |
| `allow_draft` | Run a `draft`-status plan | `false` | `true` |
| `buffs` | GM `.aura` spells on every companion (preset `"world"` or a list) | none | `"world"` |
| `brain` | Preset that plays the leader | class default (leader), none (commander) | `"frost_mage"` |
| `role`, `rotation` | Override the brain's role / rotation file | preset's own | `"tank"` |
| `gm`, `gm_level` | `false`: send no GM command; `true`: leader is a GM (level 1–6); omitted: probe once with `.account` | probe | `true` |
| `recover_wipe` | Release and run back after a wipe | on (commander), off (else) | `false` |
| `on_end` | `stay`, `logout` or `none` at the end | `stay` | `"logout"` |
| `enter` | `auto`, `gm`, `cmd`, `walk` or `off` | `auto` | `"walk"` |
| `prepare` | `online`, `fresh`, `gm_setup`, or `{"party": "hire"}` | — | `"fresh"` |
| `preflight` | Kit check: `warn`, `strict` or `off` | `warn` | `"strict"` |
| `run_id` | Trace/report name (else `r-<utc>-<hex>`) | generated | `"friday"` |
| `trace`, `trace_meta` | Trace file on/off + ≤ 16 header entries | on | `false` |
| `tune`, `live_reload` | Tunables + reload them live | — | — |
| `observe` | Observe-mode knobs (`poll_s`, `max_run_s`, `stall_s`, `auto_resume`, ...) | — | — |

Result: `{started, plan, map, from_leg, from_pull_group?, legs[], buffs[], brain?, leader_gm true\|false\|probe, mode, run_mode, lead_bot?, run_id, trace_file?, plan_check?}`.

Key errors: `dungeon_run needs the character in the world (state <s>)`; `a dungeon run is going (<plan>): dungeon_stop it first`; `plan <p> is a raid plan: run it with mode "observe" and prepare.party "hire"`; commander mode refuses `teleport`, `buffs`, `brain`, `gm`/`gm_level` up front.

## dungeon_status, dungeon_stop, dungeon_pause, dungeon_resume, dungeon_skip

No params. Result: `{running, state idle\|running\|done\|failed\|stopped, report?, last_progress_at?, stalled_for_s, stall_level none\|warn\|recover\|fail}`. `dungeon_pause` on a leader-mode run is refused (`dungeon_pause: the run is a group-mode run with no commander binding to keep; dungeon_stop it ends it`); with no run: `dungeon_<verb>: no dungeon run is going`.

## plan_check

| Field | What it does | Default | Example |
|---|---|---|---|
| `name*` | Plan file name | — | `"deadmines"` |
| `from_leg` | Check from this leg | `0` | `2` |
| `from_pull_group` | Check from this pull group (needs `from_leg`) | — | `3` |

Result: `{checked, skipped?, plan, map, points, issues[], reanchored, warnings, exempt, run_leader[]}`.

## dungeon_stage

Moves the party to a leg's start without running it.

| Field | What it does | Default | Example |
|---|---|---|---|
| `name*` | Plan file name | — | `"deadmines"` |
| `leg*` | Stage this leg (number or name, required) | — | `"barracks"` |
| `cleared_through` | Legs up to this one count as cleared | — | `1` |
| `use_safe_point` | Gather at the safe point | `false` | `true` |
| `force` | Stage past check failures | `false` | `true` |
| `dry_run` | Plan only, move nothing | `false` | `true` |
| `start`, `resume` | Start (or resume) the run there after staging (mutually exclusive) | `false` | `true` |
| `speed` | Walk speed with `start` (1.0–3.0) | — | `2.0` |
| `quiet_wait_s` | Quiet seconds before giving up (0–60) | `20` | `30` |

Result: `{staged, started, dry_run, plan, map, leg, leg_name, first, ..., party[{name, x, y, z, dist, arrived}], run?, seconds}`. Refused while a run or another stage goes (`a dungeon_run is going: dungeon_stop it first`; `a dungeon_stage is going`).

## teardown

Stops everything best-effort: snapshot, follow, the agent-side run, the brain, the server run, unbind, bot removal, leave group, GM off, logout. Game refusals are `fail` steps, never RPC errors.

| Field | What it does | Default | Example |
|---|---|---|---|
| `dry_run` | Plan only, stop nothing | `false` | `true` |
| `gm` | `off`, `on` or `keep` at the end | `off` | `"keep"` |
| `wait_ms` | Per-step wait | `4000` (1000–10000) | `6000` |

Result: `{character, bound, lead?, listed[], steps[{step, status ok\|skip\|fail, detail}], failed, dry_run, seconds}`. Errors: `gm must be off, on or keep`; `wait_ms <n>: 1000 to 10000`; `a teardown is going`.
