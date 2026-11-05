---
type: Concept
title: Spell data and spell state
description: The embedded spell tables, and what the agent knows about its own spells mid-fight.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/spellstate/spellstate.go
tags: [combat, spells, concept]
sources:
  - id: spells-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/spells/spells.go
  - id: spells-meta
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/spells/meta.go
  - id: spellstate-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/spellstate/spellstate.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:50:00+01:00
status: draft
---

# Spell data and spell state

The server sends spell ids only. The 1.12 client reads the names from its own `Spell.dbc`, which the agent does not have. So the agent ships the tables itself.

## The static tables

`internal/spells` packs three generated files into the binary (made by `tools/spellnamegen` from the core's `spell_template`): `names.tsv.gz` (id to name), `mounts.txt.gz` (which spells apply `SPELL_AURA_MOUNTED`), and `meta.tsv.gz` (one row per spell: level, cost, cast time, cooldown, ranges, school, flags, effects, reagents). They load lazily on first use; `Warm()` loads them up front so a fight never pays the parse.

`Meta` is what the core says about one spell. Its `Flags` bit set names what the spell is: `passive`, `ranged`, `next_swing`, `channeled`, `combo_points`, `harmful`, `not_in_combat`, `stealth_only`, `move_interrupts`, `interruptible`, `auto_repeat`, `cooldown_on_fade`, `damage`, `heal`, `aura`, `dispel`, `interrupt`, `resurrect`, `energize`, `taunt`. A name lookup is case-insensitive and returns every id with that name, lowest first: one name covers every rank.

An id the table does not hold (a spell added after the table was made) has an empty name and no metadata. Rotation and tactics files are checked against `HasName`, so a misspelling fails at load.

## The live state

`internal/spellstate` is what the agent knows about its own spells right now: the spellbook, the cooldowns, the global cooldown, and the state of its cast. The session feeds it from server packets; the brain and the `spells` / `cast_state` calls read it.

The 1.12 server tells the client very little about cooldowns. It sends none for a spell you cast yourself, so the agent works them out from the spell data when its own cast lands: the spell's own cooldown, its category cooldown, and the global cooldown all start from the template's numbers. What the server does send is applied on top: the cooldowns at login, a school lockout after an interrupt, start-the-cooldown-now and clear-cooldown messages.

Four deliberate limits, all on the side of waiting too long: talents and items that shorten a cooldown are not tracked, so a shortened cooldown still counts as running until its nominal end; the server rounds cooldowns down to whole seconds, so a spell can read ready up to a second early; haste is not applied to the GCD; and a cast the server refuses as not ready counts as not ready for one second (`NotReadyHold`), whatever the numbers say.

## Reading it

`spells` lists your known spells with metadata, cooldowns and whether each can be cast now, sorted by name and rank. `cast_state` reports the cast (`idle`, `casting`, `channelling`), the last success and failure, the GCD, and combo points. The event log carries the changes as they happen: `cooldown_start` (with ms, category and source: `cast`, `server`, `event`, `login`, `aura`) and `cooldown_end`.
