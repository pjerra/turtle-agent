---
type: Guide
title: Hire the party and enter
description: Bring bots online, hire a fresh party, get it into the dungeon, and pass the kit check.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/dungeon/prepare.go
tags: [dungeon, commander, hire, prepare, enter]
sources:
  - id: prepare
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/dungeon/prepare.go
    title: The prepare block
  - id: hire
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/dungeon/hire.go
    title: The hire step
  - id: dungeon-remote
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dungeon_remote.go
    title: Commander-mode checks and remote start
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:35:00+01:00
status: draft
---

# Hire the party and enter

The prepare step runs before the bind. It brings the named bots online, resets their instances, or hires a brand-new party. Then the enter phase walks or teleports the bound party into the dungeon.

## 1. Bring named bots online and reset their instances

```sh
turtle-agent rpc <dir> dungeon_run '{"name":"<plan>","mode":"commander","lead_bot":"<Lead>","prepare":{"online":true,"fresh":true}}'
```

`online` sends `.bot add` for the bots the roster shows offline (waits up to `online_wait_s`, default 45, at most 180). `fresh` teleports the bots and your character out, leaves their groups, and sends `.instance unbind all` per bot: a clean instance. `gm_setup` holds up to 4 lines, `.gm visible off` or `.gm visible on` only:

```json
"prepare":{"online":true,"gm_setup":[".gm visible off"]}
```

The report's `prepare` block records each step's seconds.

`online` warns for an already-online bot (`already_online`): the roster never lists it but `.bot add` says `already online` (a hired bot, or another owner's), taken as online without waiting. `fresh` needs a GM account (developer level): at level 4+ each bot leaves its group and `.instance unbind all` goes out per bot; below 4 the bots bind, `.instance groupunbind all` unbinds the group, and they bind again. `give_items` (≤4, `bot` `lead`/`carrier`/name) sends `.additem` per bot with the bot selected (needs level 3), then checks `.bot inv`; an already-carried item sends nothing. Every GM line of prepare is a `setup` shortcut in the report, never a `gm_commands` line. Prepare is remote-only: a group run refuses it. Ceilings: `online_wait_s` default 45, at most 180.

## 2. Hire a fresh party instead

```sh
turtle-agent rpc <dir> dungeon_run '{"name":"<plan>","mode":"commander","prepare":{"party":"hire","hire":{"composition":["warrior:tank","priest:healer","hunter:dps","rogue:dps","mage:dps"],"level":44}}}'
```

Each entry is `"class"` or `"class:role"` (roles `tank`, `healer`, `dps`). A warrior defaults to tank, a priest to healer, most damage classes to dps; paladin, shaman, and druid need an explicit role. The party needs exactly one tank and one healer, two to five bots. Empty `composition` hires a warrior tank, a priest healer, and three damage dealers (hunter, rogue, warlock, mage in that order).

- `level` 0 (the default) hires at the middle of the plan's `level_band`; else 1–60.
- `rehire` dismisses your current hires first (without it, matching hires are kept, with their saved instances).
- `no_fix` fails instead of retrying `.bot autogear` and `.bot maintenance`.
- `wait_s` caps the wait for a hire to show in the group (default 30, at most 120).
- `online` and `fresh` do not combine with `party: "hire"`: new hires are online with no saved instance.

The hire step checks gear, talents, spells, bags, and pets, fixes what it can, and binds the party. The report's `ready` block names the bots.

### Raid-only keys and refusals

`tier`, `gear_preset` and raid-size compositions are raid-only (observe mode): a 5-man hire ignores them. A hired raid party refuses the run when the commander is not 60, when a rehire does not keep exact role counts, or when the portal floor is unknown (see below).

### Portal-floor and team refusals

Hiring through a portal whose floor the navmesh does not know is refused (`z_unknown`): the party cannot gather on an unknown floor. A bot on another team's map, or a navmesh floor over 5 yd from the portal ground, is refused the same way. A kept binding skips the check.

## 3. Pick how the party enters

```sh
turtle-agent rpc <dir> dungeon_run '{"name":"<plan>","mode":"commander","prepare":{"party":"hire"},"enter":"walk"}'
```

`enter` is `auto` (the default: teleport with `.tele name` when your account is a developer-level GM and the plan names a destination, else walk into the portal), `gm`, `walk`, `cmd` (the `cmd enter` order at the portal), or `off` (a party outside the dungeon fails the run). After entering, the run sends `.bot behavior <bot> loot on` for every bot (undoing a crashed run's `loot off`), then the preflight kit check: `.bot maintenance` and `.bot inv` per bot.

- `preflight` is `warn` (the default: report and go on), `strict` (a warning fails the run), or `off`.
- `ignore_entry_limit` lets the run try a 6th new instance within the hour; see [Refusals, the watchdog, and the entry ledger](refusals-watchdog.md).

### Enter ceilings

The `cmd enter` order at the portal retries 3× on `busy`, 2× on `cancelled` or `too-far`; an `in-combat` refusal waits out the fight up to 45 s. The whole phase (walk + order) fails past 4 min. Bots count as at the door within 30 yd (lead within 12 yd vertically).

## 4. Read the enter result in the report

`remote.enter` says how the party got in, how many instance entries the account used in the last hour, and any warning. `remote.preflight` lists what the kit check found missing.
