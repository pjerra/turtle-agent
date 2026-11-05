---
type: Guide
title: Running dungeons with the agent
description: Drive a party through a plan with dungeon_run, stage it, tune it live, and clean up with teardown.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dungeon_methods.go
tags: [agent, dungeon, commander, teardown, lfg]
sources:
  - id: dungeon-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dungeon_methods.go
    title: dungeon_run, dungeon_status, dungeon_stop, pause, resume, skip
  - id: stage-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/stage_methods.go
    title: dungeon_stage params and result
  - id: teardown-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/teardown_methods.go
    title: teardown steps and result
  - id: lfg-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/lfg_methods.go
    title: LFG queue, role check, proposals, listings
  - id: tune-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/tune_methods.go
    title: tune_set and tune_list
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T22:45:00+01:00
status: draft
---

# Running dungeons with the agent

These RPC calls drive dungeon parties (the full plans system lives in `/dungeons/index.md`). The agent's character either leads the group from inside or commands bots from outside.

## 1. Run a plan

```sh
turtle-agent rpc <walk-dir> dungeon_run '{"name": "<plan>"}'
```

`mode` picks how: `leader` (default; the agent's character leads and plays, brain on), `commander` (the agent commands a bot party from outside; takes `lead_bot`, optional `bots`, `tank`, `healer`, `carrier`), `observe` (a bot run leader clears and the agent watches). Anything else is refused:

`mode must be "commander", "observe" or "leader" ("remote" and "group" are the older names of commander and leader)`

Useful options: `from_leg` (leg number or name), `from_pull_group`, `start_here`, `teleport` (GM summon to the start), `buffs`, `brain` (preset), `role`, `gm`/`gm_level`, `on_end` (`stay`, `logout`, `none`), `recover_wipe`, `run_id`. A second run while one goes is refused:

`a dungeon run is going (<plan>): dungeon_stop it first`

## 2. Watch and steer it

```sh
turtle-agent rpc <walk-dir> dungeon_status
turtle-agent rpc <walk-dir> dungeon_pause
turtle-agent rpc <walk-dir> dungeon_resume
turtle-agent rpc <walk-dir> dungeon_skip
```

`dungeon_status` answers `{running, state, report?, last_progress_at?, stalled_for_s, stall_level}` where `state` is `idle`, `running`, `done`, `failed` or `stopped`. Pause, resume and skip act on the active run by its mode; with no run:

`dungeon_pause: no dungeon run is going`

## 3. Check the plan first

```sh
turtle-agent rpc <walk-dir> plan_check '{"name": "<plan>"}'
```

Answers what the plan check says (or what `force: true` on `dungeon_run` would push past). Draft plans need `allow_draft: true` on the run.

## 4. Stage a leg without running

```sh
turtle-agent rpc <walk-dir> dungeon_stage '{"name": "<plan>", "leg": 0}'
```

Moves the party to the leg's start (`use_safe_point`, `dry_run`, `start`, `resume`, `speed` 1.0–3.0, `quiet_wait_s` 0–60). Refused while a run or another stage goes:

`a dungeon_run is going: dungeon_stop it first`

## 5. Tune a remote run live

```sh
turtle-agent rpc <walk-dir> tune_list
turtle-agent rpc <walk-dir> tune_set '{"key": "<key>", "value": 3}'
```

`value` is a number, `"default"` or null to reset. Only remote (commander-mode) runs:

`live tuning is for runs in mode "remote"`

## 6. Queue through LFG

```sh
turtle-agent rpc <walk-dir> lfg_join '{"instances": ["<id>"], "roles": "d"}'
turtle-agent rpc <walk-dir> lfg_status
turtle-agent rpc <walk-dir> lfg_proposal_answer '{"accept": true}'
turtle-agent rpc <walk-dir> lfg_leave
```

`roles` letters are `t` (tank), `h` (healer), `d` (damage); instances are 1–10 lowercase names. Every call answers `{since, sent, state}` — read on from `since` with `events`. Wrong phases need `force: true`:

`phase is <now>, not <want>: cannot <action> (force overrides)`

`lfg_auto` keeps the queue joined in the background (`delay_ms` default 1500, `off: true` stops it); `lfg_listings` and `lfg_listing_role` read the guild listings.

## 7. Clean up everything

```sh
turtle-agent rpc <walk-dir> teardown
```

`teardown` stops the run, the follow, the brain, unbinds the commander, removes hired bots, leaves the group, switches GM off and logs out — best effort, every step recorded as `ok`, `skip` or `fail` in `steps` with a `failed` count. Options: `dry_run` (plan only), `gm` (`off` default, `on`, `keep`), `wait_ms` (default 4000, 1000–10000). A second teardown while one goes is refused:

`a teardown is going`

Related: the commander's own calls (`commander`, `bot_view`, `tbm_*`, `addon_*`, `tt`, `map_snapshot`) are listed with every param in [addons, LFG and tuning RPC](/agent/rpc-advanced-reference.md).
