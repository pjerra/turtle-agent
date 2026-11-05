---
type: Guide
title: Start anywhere
description: Begin a run at any leg or pull group, stage the start first, and pass the safety gates.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dungeon_methods.go
tags: [dungeon, commander, start, stage]
sources:
  - id: dungeon-methods
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dungeon_methods.go
    title: dungeon_run RPC, validation, and run lifecycle
  - id: stage
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/stage_methods.go
    title: dungeon_stage RPC
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:35:00+01:00
status: draft
---

# Start anywhere

A run can begin at any leg, or inside a leg at any pull group. The commander's own GM move brings the party to the start. A partway start in a fresh instance is refused when its start point stands under 30 yards from a pack or boss of a skipped step: the party would pull before it is ready.

## 1. Start at a leg

```sh
turtle-agent rpc <dir> dungeon_run '{"name":"<plan>","mode":"commander","lead_bot":"<Lead>","from_leg":"velratha"}'
```

`from_leg` is a leg number from 0 or a leg name. `dungeon_status` numbers pull groups from 1 in its report; the call numbers them from 0.

## 2. Start inside a leg

```sh
turtle-agent rpc <dir> dungeon_run '{"name":"<plan>","mode":"commander","lead_bot":"<Lead>","from_leg":3,"from_pull_group":2}'
```

`from_pull_group` (0-based) needs `from_leg` in spirit: the leg it indexes into. It excludes `start_here` (which keeps the party where the lead bot stands and moves nothing) and `resume` (which skips what the party already passed). To force a start the unsafe-start gate refused, add `force_start`. To run a plan the navmesh check refused, add `force`. A start that needs both passes both.

## 3. Resume a paused run in the same instance

```sh
turtle-agent rpc <dir> dungeon_run '{"name":"<plan>","mode":"commander","resume":true,"from_leg":5}'
```

`dungeon_pause` halts a commander run with its binding kept; the next `dungeon_run` with `resume` re-enters the same group and instance (a `dungeon_stop` unbinds and loses the instance). `resume` skips the walk to the leg's first point and the pull groups behind it that a party member stands near and sees empty. A run that ended `failed` with `keep_on_fail` keeps its binding the same way; `dungeon_stop` with no run going lets a kept binding go.

## 4. Declare items the party already carries

```sh
turtle-agent rpc <dir> dungeon_run '{"name":"<plan>","mode":"commander","lead_bot":"<Lead>","from_leg":6,"have_items":[9240]}'
```

`have_items` (at most 20 entries, none 0) is for a start past the leg that makes the items. To hand items out at the prepare step instead, use `give_items` (at most 4, `bot` is `lead`, `carrier`, or a name):

```json
"give_items":[{"bot":"carrier","item":9240}]
```

## 5. Stage the start before the run

`dungeon_stage` walks the party to a leg start and tells you whether the start is safe, without starting a run. It is refused while a run goes or another staging goes.

```sh
turtle-agent rpc <dir> dungeon_stage '{"name":"<plan>","leg":"velratha"}'
```

The answer carries `staged`, the point, `kind` (`leg_first`, `before_leg`, or `forced`), `refusal` (empty when the start is safe), a `proposal` for a safer leg when it is not, `warnings`, `notes`, the `gm_commands` it sent, and the `party` with each bot's distance and arrival. Add `"dry_run":true` to check without moving, `"use_safe_point":true` to stand off, `"force":true` to stage past a refusal. With `"start":true` the staging ends in a run start (`speed` 1.0–3.0 appends to the run start line; `quiet_wait_s` 0–60, default 20, waits the party out); `start` and `resume` exclude each other.

### How the GM move and z check work

The commander's GM move is 4 steps: `.appear` to the start, `.go xyz` (Teleport navmesh) onto it, `.bot summon` for the lead plus `cmd move` holds, `cmd summon` for the rest. A GM start is booked in `setup_shortcuts` (`GM start point`), and the report's `start{point,kind entrance/leg_start/pull_gather,map,GM,summoned,seconds,skipped[]}` says what moved. Already within 20/25 yd needs no move; already in the leg within 60/80 yd resumes. A `.gps` ground/floor split over 0.5 yd refuses with the z texts; plan-z notes stay best-effort.
