---
type: Reference
title: Unit positions and unit state
description: Where unit x/y/z/o come from, what pos_age_ms means, and how to read distance, combat, class, auras and casting.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/objects/store.go
tags: [agent, units, reference, position, auras, casting]
sources:
  - id: store-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/objects/store.go
    title: Unit, aura, cast and position store
  - id: session-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/session/session.go
    title: StatusInfo and session state
generated:
  by: omp
  at: 2026-11-05T21:45:00+01:00
status: draft
---

# Unit positions and unit state

`units` and `unit` snapshot what the server has shown the agent, nearest first. Your own row in `unit` reads from the session's own position (the server never moves your own object after its create block); every other row reads from the object store.

## Where x/y/z/o come from

A unit's position arrives in a create or movement block, a movement relay, or a server spline (`SMSG_MONSTER_MOVE`). Your own position is the session's: heartbeats you sent, teleports the server acked, walks and falls you ran. While a unit moves on a spline, `x`/`y`/`z` are the spline's current point and `moving` is true; `o` faces along the leg it travels.

`pos_age_ms` says how long ago the server last placed the unit there. It is -1 when the server never did — and then `distance` and `distance_2d` are -1 too, and the row sorts last. Otherwise the age counts from the block's arrival (a create-block spline counts from receipt, minus how far along the route the unit already was).

Your own `status` position carries the same idea as `z_unknown`: true while `z` is a guess (a GM teleport, a climb, a landing) until the server sends a position. A landing that never settles keeps the guess.

## Distance

`distance` is 3D yards from you; `distance_2d` ignores height. Both are -1 when the unit's position is unknown. Filter with `within` (3D yards; `near` is the older name for the same filter): units with no known position never match a `within` filter.

## Combat, target, class

`in_combat` mirrors the unit flag; `target` is the unit's current target guid. `class` is the class byte (1–11, 5 is priest) from `UNIT_FIELD_BYTES_0`; creatures carry one too. `hostile` is true for a living creature with no NPC flags and none of the spawning, player-controlled, immune or not-selectable flags. `dead` is health 0 with a nonzero max; `ghost` is a released player spirit (health reads 1, so `dead` is false). `stand_state` names byte 0 of `UNIT_FIELD_BYTES_1`, as the `stand_state` event does.

Your own combo points (`combo_points` 0–5, `combo_target`) come from your own update fields; the target is left out when there are no points. `gcd_ms` on your own row is the global cooldown left. `status` carries the same `combo_points`, `combo_target` and `gcd_ms`.

## Auras

`auras` lists the unit's 48 aura slots in slot order (never null): `{slot, spell_id, name?, stacks, harmful, remaining_ms?}`. Slots 32–47 are `harmful`. `stacks` counts proc charges. `remaining_ms` is known for your own auras only (the server sends `SMSG_UPDATE_AURA_DURATION` to you alone); it is omitted when unknown or endless.

## Casting

`casting` is `{spell_id, name?, channel, target?, ends_in_ms?}` and null when the unit casts nothing. A cast runs from `SMSG_SPELL_START` with a cast time until its `SMSG_SPELL_GO` or a failure; a channel reads from `UNIT_CHANNEL_SPELL` instead. `ends_in_ms` counts to the cast's end (negative a little past it while the spell goes off); a channel's end is not sent to other clients, so it has none. Names come from the core's `spell_template` table packed into the agent (`make spellnames`); the server sends ids only.
