---
type: Concept
title: The brain engine
description: How the brain ticks, scores actions by band, and moves, melees and remembers.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/brain/brain.go
tags: [combat, brain, concept]
sources:
  - id: brain-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/brain/brain.go
  - id: brain-methods
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/brain_methods.go
  - id: brain-melee
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/brain_melee.go
  - id: brain-move
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/brain_move.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:50:00+01:00
status: draft
---

# The brain engine

The brain is a ticking priority engine. Every 200 ms it gathers candidate actions from its strategies, scores each by relevance band, and plays the winner through real client packets.

## Bands

Relevance bands order everything: low (5), normal (10), high (20), move (30), interrupt (40), emergency (43), dispel (50), raid (60). A rotation entry's band decides where it competes: most casts fight in low/normal/high, positioning in move, boss-tactics interrupts above heals. The winning decision lands in the event log as `brain_decision` with its trigger, action and result; ticks with nothing to do log `brain_idle`.

## Roles and strategies

Strategies are named behaviors (`frost_mage`, `role_healer`, `avoid_ground`, ...); presets are named lists of them. `brain_start` takes strategies and resolves them: at least one, all known, all playing one role. The role comes from the strategies unless you pin it (`dps`, `tank`, `healer`); a tank role needs the `role_tank` strategy, a healer role needs `role_healer`. A data rotation replaces the preset's class strategy in place (`rot:<name>`), or, with `"mode": "add"`, inserts itself after `role_healer`.

Encounters (boss tactics) arrive as extra strategies the plan adds: `avoid_ground` walks out of `move_out` circles and `encounter` bends targeting, interrupts and dispels while the boss fight is on.

## Melee memory

The brain watches the event log for what its swings did: dodges per mob (remembered 30 s, so Overpower can answer them), avoided attacks (dodge, parry, block), the current auto-attack target, and swings the server refused for facing. An auto-attack already running when the brain starts counts as on, so the brain does not order it again every tick.

## Movement and sight

Walks go along the navmesh (`walk_to` semantics: `arrived` or `partial` is success, no route is the error). Without map data the walks fail, sight answers "unknown", and the positioning triggers stay quiet. `"no_move": true` pins the character's feet: it casts but never walks.
