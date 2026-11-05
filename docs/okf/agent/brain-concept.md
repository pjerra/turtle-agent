---
type: Concept
title: The brain
description: How the trigger-action engine plays the character, and what brain_start, brain_stop and brain_status do.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/brain/engine.go
tags: [agent, brain, combat, rotation]
sources:
  - id: engine-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/brain/engine.go
    title: Trigger-action engine and relevance bands
  - id: controller-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/brain/controller.go
    title: Brain start, stop and status
  - id: strategies-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/brain/strategies.go
    title: Presets and the strategy list
  - id: roles-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/brain/roles.go
    title: Role resolution (dps, tank, healer)
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T22:45:00+01:00
status: draft
---

# The brain

The brain plays the character's rotation while something else decides where to go: `dungeon_run` in leader mode, `follow` with `assist`, or your own `walk_path` calls. You start it, it ticks every 200 ms, you stop it.

## Triggers, strategies, relevance

Each tick the engine snapshots the agent state, asks every strategy for candidate actions, and runs the most relevant one that can run[^engine-go]. Relevance bands order the urgency: idle 0, default 5, normal 10, high 20, movement 30, interrupt 40, dispel 50, raid 60. A strategy can also veto (never do this now) or damp another's relevance.

A result is `ok`, `not_possible` (with a reason), `vetoed` or `error`. Failed casts hold the spell back for a while — 1.5 s for out-of-range, 2 s for no power, 3 s for no line of sight, 30 s for missing reagents — so the brain does not spam what cannot work.

## Presets and roles

`brain_start` takes `strategies`: either a preset name (a whole class kit) or individual strategy names. Presets at this head:

`affliction_warlock`, `arms_warrior`, `balance_druid`, `bear_tank_druid`, `beast_mastery_hunter`, `combat_rogue`, `discipline_priest`, `elemental_shaman`, `enhancement_shaman`, `feral_cat_druid`, `frost_mage`, `fury_warrior`, `holy_paladin`, `holy_priest`, `marksman_hunter`, `protection_paladin`, `resto_druid`, `resto_shaman`, `ret_paladin`, `shadow_priest`, `survival_hunter`, `tank_shaman`, `tank_warrior`

An unknown name is refused with the full list:

`strategy "<name>" is unknown (presets: <...>; strategies: <...>)`

`role` is `dps`, `tank` or `healer` and must match the strategies: a tank needs the `role_tank` strategy, a healer needs `role_healer`. `tank` names the tank to follow as DPS; `rotation` swaps in a rotation file; `no_move: true` lets the brain fight but never walk; `run_id` names the session (letters, digits, dot, underscore, dash, at most 64 chars)[^controller-go].

## Status

`brain_status` reports `running`, the active `strategies`, `role`, `ticks`, `actions`, the last decisions (trigger, action, relevance, result, spell, target) and the current holds. The same object comes back from `brain_start` and `brain_stop`.

Starting twice is refused; stopping a run-owned brain is refused too (stop the run first):

`the brain is running: brain_stop it first`

`brain_stop: a dungeon run owns the brain (dungeon_stop it first)`

## What the brain does not do

The brain never moves the party and never talks to NPCs on its own. It fights, interrupts, dispels, rests (eat and drink), stands up when it must act, rezzes out of combat, recalls the pet when the fight ends, and idles with a reason (`no_trigger`, `not_ready`, `dead`) when there is nothing to do. Looting, questing, vendoring and travelling stay your RPC calls — or the dungeon driver's.
