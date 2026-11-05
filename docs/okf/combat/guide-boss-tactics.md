---
type: Guide
title: Add boss tactics
description: Put a kill-first, interrupt, dispel and move-out block on a plan boss.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/tactics/tactics.go
tags: [combat, tactics, plan]
sources:
  - id: tactics-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/tactics/tactics.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:40:00+01:00
status: draft
---

# Add boss tactics

A tactics block is the four things a player does on a boss fight beyond the class rotation: kill the add first, interrupt the cast, dispel the aura, leave the area. It lives on the plan's boss, and only the leader-mode brain acts on it.

## 1. Find the boss

Open the dungeon plan and find the boss entry (`entry` plus its `first` adds). Tactics is a block on that boss.

## 2. Name what dies first

```json
"tactics": {
  "kill_first": [7209],
  "never_target": [9001],
  "interrupt": ["Chain Lightning"],
  "dispel": ["Fevered Plague"],
  "no_dispel": ["Mutating Injection"],
  "move_out": [{"cast": "Fire Nova", "yards": 12}, {"ground": 10132}]
}
```

`kill_first` holds creature entries to kill before anything else while the fight is on. `never_target` holds entries the leader never attacks. `interrupt` and `dispel` name enemy casts and party auras by spell id or name (a name matches every rank). `no_dispel` names auras the healers must leave alone because dispelling hurts more. `move_out` with `cast` leaves a circle of `yards` around the caster while it casts; with `ground` it leaves the ground effect of that spell, whoever cast it, using its own radius.

Rules: each list holds at most 32 entries. A creature entry cannot be in both `kill_first` and `never_target`. A `move_out` rule names exactly one of `cast` and `ground`; `yards` goes with `cast` only (1–60, default 10).

## 3. Check the plan loads

Load the plan (`plan_check`, or run the leader). Spell names are checked against the spell data at load, so a typo fails there and not as a rule that never fires: `legs[0].boss.tactics: interrupt: no spell is named "Chain Lightening"` means fix the spelling.

## 4. Know what plays it

The encounter turns on when a boss or `first` entry is in view, hostile, and in combat. Then the brain aims `kill_first` targets within 40 yd, interrupts planned casts first, dispels planned auras first at any mana, and walks out of `move_out` circles (tanks hold their ground). Commander and group runs ignore the block except through plan-level kill order; see [Combat troubleshooting](/combat/troubleshooting.md) if a rule never seems to fire.

## 5. Limits and shipped bosses

The encounter stays on while a boss or `first` entry is in view, hostile, and in combat; when the last one dies or leaves, the brain logs `brain_state encounter_end`. Effects: the nearest planned target within 40 yd dies first (skipping the tank's target and the selected target unless the plan names the fight's own mob); planned interrupts fire before class interrupts with the class spell unless the cast is not interruptible; planned dispels fire before class dispels at any mana (slows count at 20% mana); avoid walks hold for the tank. A tactics block with a `first` entry treats those adds as the fight: losing sight of both boss and `first` ends it.

Shipped plans name Uldaman and Zul'Farrak bosses (entries such as 7785/8179 plus a 10 yd summon rule for called adds). Remote (commander) runs carry the block but do not act on it, except through `kill_first`/`summoned_kill_first` kill order.

Shipped plans with tactics blocks include Uldaman, Zul'Farrak, Deadmines, Wailing Caverns, Ragefire Chasm, Scarlet Library, Stormwind Stockade and Crescent Grove.

Remote (commander) runs carry the block but do not act on it, except through plan-level `kill_first`/`summoned_kill_first` kill order.
