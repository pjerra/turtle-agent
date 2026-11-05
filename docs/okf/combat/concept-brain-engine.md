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

## Timing, back-off, threat, positioning, MoveLock, LOS

The engine ticks every 200 ms (`Every`), waits 300 ms after a cast for the server's start (`Settle`), lets one game call run at most 3 s (`DoTimeout`), and reports the same idle reason at most every 10 s (`IdleEvery`) and the same refused decision at most every 3 s (`DecEvery`).

A cast the server refused stays off for a hold by reason (`brain_status.holds`): `out_of_range` 1.5 s, `too_close` 1 s, `no_power` 2 s, `line_of_sight` 3 s, `not_ready` 0.5 s, `not_infront` 0.4 s, `interrupted` 0.3 s, `moving` 0.5 s, `immune` 5 s, anything else 1 s. `bad_targets` holds 2 s; only a cast aimed at the player itself holds 10 s (`selfBadTargetsHold`). A failed game call holds the spell 1 s (`ErrorHold`).

Harmful casts wait out the opening delay: 2 s after the tank's mob was first fought (`OpeningDelay`, default 2 s). The fight's mob is the tank's target, else a mob on the leader, else the selected target. Area abilities are vetoed while a loose mob (one the party has not engaged) stands near. A harmful cast aimed at the enemy also breaks the player's own cast only from the interrupt band (relevance 40) up.

Ranged fighters stand 20–28 yd from the fight's mob (`rangedMin`/`rangedMax`), with 1 yd of slack (`bandSlack`), sight of the mob, and out of its 90° front. Spots are searched on rings at the band's low+2, middle, and high−1 yd, up to 48 candidates (`spotChecks`), snapped to places 1.5–24 yd away (`maxStep`). Ground hazards add relevance 42 (`RelInterrupt+2`) and the escape spot lands 2 yd past the hazard's edge. Only interrupt-band actions (40+) break the player's own cast; everything below waits for the walk or the cast to end.

Sight is a navmesh ray, cached 250 ms per target while neither end moves 1 yd (`losTTL`, `losMoved`). A target the server refused for line of sight counts as known-blocked for 5 s while both ends stay within 3 yd (`losBlockFor`, `losBlockMoved`); the next spot search then looks somewhere else (more than 4 yd away).

`MoveLock` says who moves the player: the dungeon driver or the brain, never both. The driver takes it (`HoldMove`) around its own walks; holds nest, and the lock is free once every hold is let go. While it is held the brain starts no walk (`move_held` in the status) but keeps casting. A brain step under way is stopped first, so `HoldMove` must never be called from inside the session's do-loop (the walker's own goroutine): call it from the driver's goroutine. `Stop` waits up to 2 s for a running game call.

Interrupts fire only with at least 350 ms left on the enemy cast (`interruptMinLeft`); a cast already tried stays alone for 3 s (`interruptedHold`). Enemy heals and crowd control interrupt first (priority 3), then damage on the party (priority 2); a cast the plan's tactics names beats both (priority 4).

Consumables follow fixed bands: healthstone then potion below 25% health in combat (`healthCriticalPct`); bandage below 45% after 4 s without losing health (`bandageBelowPct`, `damageQuiet`); mana potion below 10% mana in a boss fight (`manaPotionBelow`); wand filler (`Shoot`, relevance 9) below 15% mana (`wandBelowPct`). A used item holds its group for the item cooldown (2 min for potions and healthstones). Out of combat the leader eats below 85% health (`foodEatPct`) and drinks below 65% mana (80% for a healer, `manaDrinkPct`/`healerDrinkPct`).

## Roles, melee, warrior

`brain_start` resolves the strategies to one role: `dps` (default), `tank` (needs `role_tank`), or `healer` (needs `role_healer`). A pinned role that does not match the strategies is refused (`role "tank" does not match the strategies [...]`). The tank fights the loose add first, then the mob on itself, and answers it with the class taunt (`taunt`, relevance 25); it never pulls with a gap closer into a fight the tank has not engaged. The healer heals by triage (see [Healer and priest](/combat/guide-healer-priest.md)).

Melee fighters keep auto-attack on the fight's mob (relevance 19, within 8 yd `meleeReach`), reordered at most every 6 s (`swingRefresh`) and resent after 3 s (`swingResend`). A swing the server refused for facing holds 3 s while `face_target` turns the player (`swingFaceUntil`); the brain faces the mob again for 3 s after (`faceUntil`). Melee spots sit 3.2/4/4.6 yd behind the mob, more than 100° from its facing (`rearMin`); a tank that must turn the mob away stands 2.6/3.2/4 yd on the far side of the group, more than 110° round (`tankAwayMin`), at most twice per mob per minute (`faceTries`, `faceGap`, `faceKeep`). Spots keep 10 yd from mobs that have not noticed the party (`meleeKeepAway`).

Gap closers fire in the 8–25 yd window (`mobGap`): `Charge` out of combat in Battle Stance, `Intercept` in combat in Berserker Stance (relevance 32), gated on the tank holding the pull. The warrior keeps a home stance and changes it in combat only below 25 rage (`rageStanceKeep`): Arms lives in Battle, Fury in Berserker (Battle below 30, before the stance is learned), the tank in Defensive. Rage is counted in tenths (50 rage is 500). Key thresholds: `Execute` below 20% mob health; `Hamstring` on a runner at or below 25%; `Bloodrage` below 20 rage above 50% health within 12 yd; `Heroic Strike`/`Cleave` as the rage dump from 50 rage (Cleave with 2+ mobs in reach); `Overpower` within 4.5 s of a dodge (`dodgeWindow`); `Rend` above 35% mob health; `Sunder Armor` up to 5 stacks; `Revenge` within 3.5 s of dodging, parrying or blocking (`avoidedWindow`).
