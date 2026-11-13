---
type: Reference
title: Run calls
description: Every dungeon_run field with its default, plus status, stop, pause, stage, and plan check.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dungeon_methods.go
tags: [dungeon, reference, rpc]
sources:
  - id: dungeon-methods
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dungeon_methods.go
    title: dungeon_run RPC, validation, and run lifecycle
  - id: dungeon-remote
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dungeon_remote.go
    title: Commander-mode checks and remote start
  - id: stage
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/stage_methods.go
    title: dungeon_stage RPC
  - id: plan-check
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/plan_check_cmd.go
    title: plan-check CLI and plan_check RPC
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:55:00+01:00
status: draft
---

# Run calls

All calls go through `turtle-agent rpc <dir> <method> '<json>'`. Every field is optional unless noted. Defaults say what leaving the field out does.

## dungeon_run

`name` is required: a plan in the `dungeons` folder (`a-z0-9_-`, starting alphanumerically, at most 40 characters). A draft plan is refused without `allow_draft`; a raid plan runs in observe mode with a hired party only. The answer reports `started`, `plan`, `map`, `from_leg`, `legs`, `mode` (`group`/`remote`/`observe`), `run_mode` (`leader`/`commander`/`observe`), `run_id`, `trace_file`, and the `plan_check` note.

| Field | Default | What it does | Example |
|---|---|---|---|
| `name` | required | Plan to run | `{"name":"uldaman"}` |
| `mode` | group form (leader) | `commander` (`remote`), `leader` (`group`), or `observe` | `{"mode":"commander"}` |
| `allow_draft` | `false` | Run a draft plan (unchecked skeleton) | `{"allow_draft":true}` |
| `from_leg` | leg 0 | Leg number from 0 or leg name | `{"from_leg":"velratha"}` |
| `from_step` | off | Observe only: the run leader's step number, from 1; excludes `from_leg`, `from_pull_group` | `{"from_step":41}` |
| `from_pull_group` | off | Commander: 0-based pull group inside `from_leg`; excludes `start_here`, `resume` | `{"from_pull_group":2}` |
| `start_here` | `false` | Commander: start where the lead bot stands, no move | `{"start_here":true}` |
| `force` | `false` | Commander: run past a refused or failed plan check | `{"force":true}` |
| `force_start` | `false` | Commander: run past the unsafe-start gate (start under 30 yd from a skipped pack) | `{"force_start":true}` |
| `have_items` | none | Commander: item entries the party already carries (at most 20, none 0) | `{"have_items":[9240]}` |
| `give_items` | none | Commander: GM `.additem` at prepare, at most 4 (`bot`: `lead`, `carrier`, or a name) | `{"give_items":[{"bot":"carrier","item":9240}]}` |
| `pulltest` | off | Score one pack: `leg` (number or name, required), `pull_group` (required), `tries` (default 3, 1–10), `reset` (`auto` default, `equivalent`, `respawn`, `none`) | `{"pulltest":{"leg":1,"pull_group":0,"tries":3}}` |
| `teleport` | `false` | Group/leader: GM-teleport to the start and summon the party (in the report) | `{"teleport":true}` |
| `no_shortcuts` | `false` | No GM revive of a dead companion at a rest gate | `{"no_shortcuts":true}` |
| `setup` | none | GM commands you ran yourself, at most 40 of at most 400 chars; copied into the report, never sent | `{"setup":["go entrance"]}` |
| `buffs` | none | Group/leader: preset `"world"` (the classic world buffs) or `"world-raid"` (the same list without Warchief's Blessing, which is Horde-only), a spell id, or a list; applied with `.aura` (developer GM) | `{"buffs":"world"}` |
| `brain` | leader: class preset; else off | Preset or strategy name playing your character (group/leader) | `{"brain":"frost_mage"}` |
| `rotation` | preset's own | Rotation file replacing the preset's class rotation (with a brain) | `{"rotation":"my_mage"}` |
| `role` | preset's own (`dps` if none) | `dps`, `tank`, or `healer` (with a brain) | `{"role":"healer"}` |
| `gm` | probe (`.account` once) | `true`: GM, no probe; `false`: player, no GM command | `{"gm":true,"gm_level":3}` |
| `gm_level` | probed | With `gm: true`: 1 observer – 6 console | `{"gm_level":3}` |
| `recover_wipe` | on (commander), off (group) | Release and run back after a wipe instead of ending | `{"recover_wipe":false}` |
| `resume` | `false` | Skip what the party already passed (with `from_leg`) | `{"resume":true}` |
| `wait_sickness` | `false` | Group/leader: wait resurrection sickness out (up to 11 min) | `{"wait_sickness":true}` |
| `lead_bot` | required (commander/observe) | The bot that walks (letters only) | `{"lead_bot":"Tanky"}` |
| `bots` | the lead's party as is | At most 4 other bots | `{"bots":["A","B"]}` |
| `tank` | first threat holder | Threat tank bot | `{"tank":"Tanky"}` |
| `healer` | every caster | Healer-mana target bot | `{"healer":"Healy"}` |
| `carrier` | first living non-lead/tank/healer | Object-step bot | `{"carrier":"Carry"}` |
| `cc_bot` | hired mage | Big-pack CC bot | `{"cc_bot":"Maggy"}` |
| `cc` | none | `[{"entry":4863,"mark":"moon"}]` (marks: skull, cross, square, moon, triangle, diamond, circle, star) | `{"cc":[{"entry":4863,"mark":"moon"}]}` |
| `team` | your own | `alliance` or `horde`, for faction-gated NPCs | `{"team":"horde"}` |
| `preflight` | `warn` | Kit check: `warn`, `strict` (fail on warning), `off` | `{"preflight":"strict"}` |
| `on_end` | `stay` | Bots at the end: `stay` (`cmd stay` + unbind), `logout`, `none` | `{"on_end":"logout"}` |
| `keep_on_fail` | `false` | A failed run keeps its binding and instance for a resume | `{"keep_on_fail":true}` |
| `enter` | `auto` | How an outside party gets in: `auto`, `gm`, `walk`, `cmd`, `off` | `{"enter":"walk"}` |
| `ignore_entry_limit` | `false` | Try a 6th new instance within the hour | `{"ignore_entry_limit":true}` |
| `prepare` | none | `online`, `fresh`, `gm_setup` (≤4 `.gm visible` lines), `online_wait_s` (default 45, ≤180), `party: "hire"`, `hire` block | `{"prepare":{"party":"hire"}}` |
| `prepare.hire` | defaults | `composition`, `level` (0: plan middle, 1–60), `rehire`, `no_fix`, `wait_s` (default 30, ≤120) | `{"hire":{"level":44}}` |
| `loot_restore` | `group` | Loot method after the carrier's free-for-all: `group`, `rr`, `master`, `nbg` | `{"loot_restore":"master"}` |
| `object_fallback` | `agent` | Carrier failing twice: `agent` (your character finishes), `off` | `{"object_fallback":"off"}` |
| `graves_batch` | plan's own | Fixed grave batch size, 1–20 | `{"graves_batch":4}` |
| `graves_ramp` | plan's own | `start` (0–20, default 2), `step` (0–10, default 1), `max` (0–40, 0: no limit) | `{"graves_ramp":{"start":2}}` |
| `graves_open_by` | `carrier` | Who opens grave batches: `carrier`, `agent` (needs GM) | `{"graves_open_by":"agent"}` |
| `run_id` | generated (`r-<utc>-<hex>`) | Letters, digits, `.` `_` `-`, at most 64, starting alphanumerically | `{"run_id":"zf-3b"}` |
| `trace` | on | `false` writes no trace file | `{"trace":false}` |
| `trace_meta` | none | Free header text, at most 16 entries of at most 200 chars | `{"trace_meta":{"tb_sha":"abc"}}` |
| `run_meta` | slot default | AI driver `{"model","effort","run","server_trace"}`; opt-in server trace | `{"run_meta":{"model":"m"}}` |
| `watchdog` | 120/240/420 | Stall levels in s (`warn_s`, `recover_s`, `fail_s`); 0: default, negative: off | `{"watchdog":{"fail_s":-1}}` |
| `tune` | login `--tune`, else `<plan>.tune.json` | Commander: tunables file; a named file must exist | `{"tune":"fast.json"}` |
| `live_reload` | on | `false` stops re-reading plan and tunables at leg starts | `{"live_reload":false}` |
| `observe` | defaults | `plan`, `poll_s` (15), `max_run_s` (3 h), `stall_s` (10 min), `paused_wait_s` (10 min), `auto_resume` (0–3, off), `maintenance`, `allow_far_start`, `keep_loot_setting`, `until` | `{"observe":{"poll_s":10}}` |

## dungeon_status, dungeon_stop, dungeon_pause, dungeon_resume, dungeon_skip

No fields. `dungeon_status` answers `running`, `state` (`idle`, `running`, `done`, `failed`, `stopped`, `refused`), the `report`, and `last_progress_at`, `stalled_for_s`, `stall_level` (`none`, `warn`, `recover`, `fail`). `dungeon_stop` ends the run (and with no run going, unbinds a kept binding). `dungeon_pause` halts a commander run with its binding kept (group mode: refused; idle: no-op). `dungeon_resume` and `dungeon_skip` go to the run leader of an observe run; otherwise refused.

`dungeon_status` is one snapshot. The end event (`dungeon_done`, `dungeon_failed` or `dungeon_refused`) is emitted before the report says `done`, `failed`, `stopped` or `refused`: a final state is never read before its event. `refused` is a run stopped before it started (a team gate, an item a skipped leg was to provide, a grave or GM cross check): nothing was sent, and `report.error` names the reason. A non-final state always has `running: true`; `running` stays true for a moment after a final state while the run's goroutine lets go of the brain.

## dungeon_stage

`name` and `leg` (number from 0 or name) are required. Refused while a run or another staging goes.

| Field | Default | What it does | Example |
|---|---|---|---|
| `name` | required | Plan to stage | `{"name":"uldaman"}` |
| `leg` | required | Leg number or name | `{"leg":"velratha"}` |
| `cleared_through` | none | Leg number or name before the staged leg | `{"cleared_through":2}` |
| `use_safe_point` | `false` | Stand off the exact start | `{"use_safe_point":true}` |
| `force` | `false` | Stage past a refusal | `{"force":true}` |
| `dry_run` | `false` | Check without moving | `{"dry_run":true}` |
| `start` | `false` | End the staging with a run start (excludes `resume`) | `{"start":true}` |
| `resume` | `false` | End the staging with a resume (excludes `start`) | `{"resume":true}` |
| `speed` | none | With `start`: 1.0–3.0, appended to the run start line | `{"start":true,"speed":2.0}` |
| `quiet_wait_s` | 20 | 0–60: how long the party is waited for | `{"quiet_wait_s":30}` |

The answer carries `staged`, `started`, `plan`, `map`, `leg`, `leg_name`, `first`, `point`, `kind`, `back_yd`, `refusal`, a safer-leg `proposal`, `warnings`, `notes`, `gm_commands`, the `party`, the `run` start, and `seconds`.

## plan_check

| Field | Default | What it does | Example |
|---|---|---|---|
| `name` | required | Plan in the `dungeons` folder | `{"name":"uldaman"}` |
| `from_leg` | leg 0 | Leg number or name to check from | `{"from_leg":3}` |
| `from_pull_group` | leg's own walk too | 0-based pull group (needs `from_leg`) | `{"from_pull_group":1}` |

The answer carries `checked` (false: `skipped` says why — no navmesh files or planner), the plan, map, points, `issues` (leg, pull group, point, reason, detail), `warnings`, and the run-leader rows.
