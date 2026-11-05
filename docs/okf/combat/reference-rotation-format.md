---
type: Reference
title: Rotation file format
description: Every field of a version-1 rotation file, what it does, and an example.
resource: https://github.com/pjerra/turtle-agent/blob/dev/docs/ROTATIONS.md
tags: [combat, rotation, reference]
sources:
  - id: rotations-doc
    resource: https://github.com/pjerra/turtle-agent/blob/dev/docs/ROTATIONS.md
  - id: rotation-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/brain/rotation.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:58:00+01:00
status: draft
---

# Rotation file format

Version 1. `rotation check` enforces all of this against the spell data. Unknown `when` fields are an error.

## Top level

| Field | What it does | Default | Example |
|---|---|---|---|
| `version` | Format version; always `1` | Required | `1` |
| `name` | Rotation name (`rotation show`, `brain_start`) | Required | `"my_frost"` |
| `class` | Class it plays | Required | `"mage"` |
| `spec` | Spec it plays | Optional | `"frost"` |
| `mode` | `"add"`: replace nothing; entries play right after `role_healer` (a healing class's extra casts; the class must have a heal kit and the preset must have `role_healer`) | Replace mode | `"add"` |
| `replaces` | Go strategies this file takes the place of | Required (empty with `"mode": "add"`) | `["mage_frost"]` |
| `notes` | What it plays and why | Optional | `"..."` |
| `sources` | Where the idea comes from | Optional | `["..."]` |
| `priorities` | The entries, first to last | Required | See below |
| `kb` | Where a generated file came from: guide `file`, its `sha256`, the `preset` it plays in, the `recipe`, and `unexpressed` rows (`row`, `text`, `why`) | Optional | See [Write a rotation](/combat/guide-write-rotation.md) |

## One priority

| Field | What it does | Default | Example |
|---|---|---|---|
| `id` | Entry name in `brain_decision` events | Required | `"fire_blast"` |
| `kind` | `cast`, `attack`, `next_swing` or `stance` | `cast` | `"attack"` |
| `spell` | Spell name; the highest known rank is cast | One of `spell` / `spell_id` | `"Fire Blast"` |
| `spell_id` | Spell id instead of a name | One of `spell` / `spell_id` | `10199` |
| `level` | Level of the first rank, checked against the spell data | Optional | `6` |
| `talent` | `true`: the spell is a talent | `false` | `true` |
| `rank` | Cast exactly rank N; never falls back (a missing rank means the entry cannot run) | Highest rank | `1` |
| `target` | `fight`, `melee_mob`, `self`, or `heal` (the triage's pick this tick) | `fight` | `"melee_mob"` |
| `band` | `low`, `normal`, `high`, `move`, `emergency` | `normal` | `"high"` |
| `threat` | Threat note (e.g. `aoe`) | Optional | `"aoe"` |
| `harmful` | Override the spell's harmful flag | From spell data | `false` |
| `alt` | Alternate spells | Optional | `["..."]` |
| `stances` | Stances this entry plays in | All | `["Battle Stance"]` |
| `note` | Human note | Optional | `"..."` |
| `when` | Conditions, all of which must hold | Empty: state default only | See below |

## Conditions (`when`)

State: `state` (`combat`, `rest`, `any`; default `combat` for an enemy target, `any` for self). Mobs: `melee_mob`, `mob_in_reach`, `mobs_in_reach_min`, `mobs_in_cone_min`, `mob_distance_min`, `mob_distance_max`, `mob_on_me`, `tank_holds`, `mob_fleeing`, `mobs_near_mob_min` (hostiles in combat within 8 yd of the fight's mob, the ranged "3+ targets"). Self: `moving`, `in_combat`, `feared`, `after` (`dodge` or `avoided`), `combo_points_min`, `stance`, `self_hp_below` / `self_hp_above`, `power_below` / `power_above` (percent), `mob_hp_below` / `mob_hp_above` (percent). Auras: `self_aura`, `no_self_aura`, `self_aura_missing` (or under a minute left), `mob_aura`, `no_mob_aura`, `mob_stacks_below`, `mobs_lacking_aura`. Levels: `level_min`, `level_max` (1–60, min at most max). Heal target only: `heal_hp_below`, `heal_is_tank`, `heal_no_aura`; any target: `members_hurt_min`.
