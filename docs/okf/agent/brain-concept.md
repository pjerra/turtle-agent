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

`role` defaults to `dps` when no strategy names one. The resolution errors name the fix:

`role "tank" does not match the strategies [...] (which play dps)`

`role "tank" needs the role_tank strategy (presets tank_warrior, bear_tank_druid)`

`role "healer" needs the role_healer strategy (presets holy_priest, holy_paladin, resto_druid, resto_shaman)`

`role "bard": want dps, tank or healer`

`tank` names the party member whose target you fight; empty finds the tank by class and target. `no_move: true` keeps the brain from walking you (positioning, stepping out of ground effects, running to the tank) while it still fights, interrupts and dispels; a dungeon run sets it until the driver holds the move lock around its own walks. Blink is a spell, not a walk, and is not held back.

## Status

`brain_status` reports `running`, the active `strategies`, `role`, `ticks`, `actions`, the last decisions (trigger, action, relevance, result, spell, target) and the current holds. The same object comes back from `brain_start` and `brain_stop`.

Starting twice is refused; stopping a run-owned brain is refused too (stop the run first):

`the brain is running: brain_stop it first`

`brain_stop: a dungeon run owns the brain (dungeon_stop it first)`

### Dungeon-run ownership

A dungeon run owns the brain while it goes, so your idea of whether the brain runs cannot drift from the run's. Starting or stopping by hand meanwhile is refused:

`<method>: a dungeon run owns the brain (dungeon_stop it first)`

Starting a run while a hand-started brain runs is refused the other way:

`a bot brain that was started by hand is running: brain_stop it first`

And a run that has not finished ending still owns it:

`the previous dungeon run still owns the brain (it is ending): try again in a moment`

`brain_status` always answers, owned or not. The run's own brain preset rides in `dungeon_run brain` — see the combat run guides for the preset links.

Preset details live with the combat guides: start and watch a run-owned brain in the run-brain guide, the engine bands and timing in the brain-engine concept, and boss behaviour in the boss-tactics guide.

## What the brain does not do

The brain never moves the party and never talks to NPCs on its own. It fights, interrupts, dispels, rests (eat and drink), stands up when it must act, rezzes out of combat, recalls the pet when the fight ends, and idles with a reason (`no_trigger`, `not_ready`, `dead`) when there is nothing to do. Looting, questing, vendoring and travelling stay your RPC calls — or the dungeon driver's.
