---
type: Guide
title: Write a rotation
description: Add a priority-list rotation file, check it against the spell data, and load it.
resource: https://github.com/pjerra/turtle-agent/blob/dev/docs/ROTATIONS.md
tags: [combat, rotation]
sources:
  - id: rotations-doc
    resource: https://github.com/pjerra/turtle-agent/blob/dev/docs/ROTATIONS.md
  - id: rotation-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/rotation_cmd.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:40:00+01:00
status: draft
---

# Write a rotation

A rotation is a JSON file (version 1): a priority list of one class and spec that the brain plays instead of the Go strategy it replaces.

## 1. Copy the shape

```json
{"version": 1, "name": "my_frost", "class": "mage", "spec": "frost", "replaces": ["mage_frost"],
 "notes": "what it plays and why", "sources": ["where the idea comes from"],
 "priorities": [
  {"id": "fire_blast", "spell": "Fire Blast", "level": 6, "target": "melee_mob", "when": {"melee_mob": true}},
  {"id": "frostbolt", "spell": "Frostbolt", "level": 4, "target": "fight"}]}
```

`replaces` names the Go strategies this file takes the place of. The preset keeps everything else: buffs, drinking, positioning, interrupts, consumables.

## 2. Order the priorities

Entries run top to bottom; an earlier entry beats a later one of the same band (`low`, `normal`, `high`, `move`, `emergency`). The engine casts the most relevant entry that can run now: spell known, off cooldown, enough power, in range, the `when` true. So the list reads "the first that works".

Each entry needs `id` (its name in `brain_decision` events), `spell` (a name; the highest known rank is cast) or `spell_id`, and `level` (the level of the first rank, checked against the Turtle spell data; `talent: true` for a talent). `target` is `fight`, `melee_mob`, `self`, or `heal` (the triage's pick this tick). `when` is a closed set of conditions that must all hold: state (`combat`, `rest`, `any`), mobs and distances, health and power percents, auras, `moving`, `stance`, `combo_points_min`, and more. An unknown `when` field is an error.

## 3. Check it

```sh
turtle-agent rotation check my_frost.json
```

This checks spell and aura names, first-rank levels, ids, bands and conditions against the spell data. It reads nothing but the files. Fix what it names; `my_frost.json: ok` is the gate.

## 4. Load it

Put the file in a folder and name the folder `rotations_dir` in the settings. Names must be new: a built-in name is refused. Then name it in `brain_start` (`"rotation": "my_frost"`) or `dungeon_run`. See the current list any time with `turtle-agent rotation list`, and preview one rank choice with `turtle-agent rotation show --level 30 my_frost`.

Special shapes: `rank: N` casts exactly rank N, never falling back; `target: "heal"` entries run through the healer's cast path; `"mode": "add"` with empty `replaces` inserts the file after `role_healer` instead of replacing anything (for a healing class's extra casts; the class must have a heal kit). The full field list is in [Rotation file format](reference-rotation-format.md).
