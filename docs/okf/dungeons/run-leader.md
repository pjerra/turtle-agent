---
type: Guide
title: Play in the party
description: Run in leader mode, where your own character leads the party and fights with the bot brain.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dungeon_modes.go
tags: [dungeon, leader, brain, guide]
sources:
  - id: dungeon-modes
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dungeon_modes.go
    title: Run modes and leader brain presets
  - id: dungeon-methods
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dungeon_methods.go
    title: dungeon_run RPC, validation, and run lifecycle
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:40:00+01:00
status: draft
---

# Play in the party

In leader mode your character is in the group, leads it, and fights, played by the bot brain. The brain starts at a leg's first rest gate and stops at the leg's end. Omit `mode` for the older group form, where the brain is optional.

## 1. Start the run

```sh
turtle-agent rpc <dir> dungeon_run '{"name":"<plan>","mode":"leader"}'
```

With `mode: "leader"`, an empty `brain` picks the preset of your character's class (role `dps` unless `role` says otherwise):

| Class | dps | tank | healer |
|---|---|---|---|
| Warrior | `arms_warrior` | `tank_warrior` | — |
| Paladin | `ret_paladin` | — | `holy_paladin` |
| Hunter | `marksman_hunter` | — | — |
| Rogue | `combat_rogue` | — | — |
| Priest | `shadow_priest` | — | `holy_priest` |
| Shaman | `enhancement_shaman` | — | `resto_shaman` |
| Mage | `frost_mage` | — | — |
| Warlock | `affliction_warlock` | — | — |
| Druid | `feral_cat_druid` | `bear_tank_druid` | `resto_druid` |

A class without a preset for the role is refused with the list. Name another preset with `brain`, another role with `role`, and a rotation file with `rotation` (it replaces the preset's class rotation; the report and trace name it with the file's digest).

## 2. Know what leader mode refuses

`from_pull_group`, `start_here`, and `have_items` go with commander mode (leader mode walks the party itself; `teleport` moves it). `give_items`, `tune`, `live_reload`, `force_start`, and the commander party fields (`lead_bot`, `bots`, `tank`, `healer`, `carrier`, `cc`, `team`, `preflight`, `on_end`, `keep_on_fail`, `enter`, `ignore_entry_limit`, `prepare`) are refused too. These stay available: `teleport` (GM-moves your character to the start and summons the party), `buffs` (preset `"world"` or spell ids, applied with `.aura`), `recover_wipe` (off by default: a wipe ends the run), `wait_sickness`, `no_shortcuts`, `setup`.

## 3. Score your own pull

A leader-mode pull test runs where the party stands, at the pack: leave `teleport` and `resume` out, and pass a `brain` (or `mode: "leader"`, which picks one). See [Score one pack](/dungeons/pulltest.md).
