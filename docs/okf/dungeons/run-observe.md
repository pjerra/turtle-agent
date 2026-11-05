---
type: Guide
title: Watch a run leader
description: Run in observe mode, where a TortoiseBots run leader on the server clears the dungeon.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/dungeon/observe.go
tags: [dungeon, observe, guide]
sources:
  - id: observe
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/dungeon/observe.go
    title: The observe driver
  - id: dungeon-remote
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dungeon_remote.go
    title: Commander-mode checks and remote start
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:40:00+01:00
status: draft
---

# Watch a run leader

In observe mode the TortoiseBots run leader on the server (`.bot run`) clears the dungeon; your agent prepares, binds, and enters the party like a commander run, sends `.bot run start <plan>`, and then only watches the `TBM:RUN` status. It sends no order after the start. Raid plans run in observe mode only, with `prepare.party "hire"`.

## 1. Start watching

```sh
turtle-agent rpc <dir> dungeon_run '{"name":"<plan>","mode":"observe","prepare":{"party":"hire"}}'
```

### Raid plans

A raid plan runs in observe mode only with `prepare.party: hire`. The hire step defaults `tier` to the preset tier else 2, and `gear_preset` to the plan preset. The flow hires, gears, binds to the main tank and starts; `.bot maintenance` is never sent.

`from_leg`, `from_pull_group`, and `from_step` only say at which step of the run leader's own plan it starts (`from_step` is the step number `.bot run status` shows, from 1). These steer the bots and are refused in observe mode: `start_here`, `pulltest`, `have_items`, `give_items`, `cc`, `carrier`, `loot_restore`, `object_fallback`, `tank`, `healer`, `recover_wipe`, `graves_batch`, `graves_ramp`, `graves_open_by`, `tune`, `live_reload`, `force_start`.

## 2. Tune the watch

```json
"observe":{"poll_s":15,"max_run_s":10800,"stall_s":600,"paused_wait_s":600,"auto_resume":0}
```

All of it is optional. `poll_s` asks `.bot run status` between the lines the run leader pushes (default 15). `max_run_s` caps the whole run (default 3 h), `stall_s` caps a run showing the same status (default 10 min), `paused_wait_s` caps a paused run waiting for a resume (default 10 min); past a ceiling the run is stopped and fails. `auto_resume` (0–3, default 0: off) resumes the run leader by itself after its regroup pause, at most that many times a step. `maintenance` sends `.bot maintenance <bot>` to every bot at the preflight. `allow_far_start` lets a partway start go on although the lead bot stands over 80 yards from the start step. `keep_loot_setting` leaves the bots' corpse looting as set (else the start sends `.bot behavior <bot> loot on`, undoing a crashed run's `loot off`). `until` stops at a point of the plan (`boss`, `boss-done`, `step N`, `leg L`, `none`).

## 3. Steer the watched run

```sh
turtle-agent rpc <dir> dungeon_pause
turtle-agent rpc <dir> dungeon_resume
turtle-agent rpc <dir> dungeon_skip
```

In observe mode these go to the run leader on the server (`.bot run pause|resume|skip`). `dungeon_stop` sends `.bot run stop` and unbinds.

## 4. Staging, ceilings, wipes

`dungeon_stage` proofs the start before the watch: it walks the party to the leg start and answers `staged`, the point, `refusal`, and `gm_commands`. Ceilings: `poll_s` 15, `max_run_s` 3 h, `stall_s` 10 min, `paused_wait_s` 10 min; past a ceiling the run is stopped and fails. `auto_resume` (0–3, off) resumes the run leader after its regroup pause, 15 s later, at most 6 per run. A partway start over 80 yd from the lead bot is refused without `allow_far_start`. Wipes pause the run leader; the watch counts them and stops at the ceilings.
