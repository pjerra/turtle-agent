---
type: Concept
title: Anatomy of a plan file
description: Legs, pull groups, steps, bosses, and the plan-wide lists that steer a run.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/dungeon/plan.go
tags: [plans, format]
sources:
  - id: plan-struct
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/dungeon/plan.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:45:00+01:00
status: draft
---

# Anatomy of a plan file

A plan is `dungeons/<name>.json`, format version 1. Every position is an `{"x", "y", "z"}` point in map coordinates; every `*_s` field is seconds[^plan-struct].

## Example

The first leg of `dungeons/uldaman.json`, as the loader reads it. A test parses this block, so it stays a valid plan.

```json
{
  "version": 1, "name": "uldaman", "map": 70,
  "entrance": {"x": -228.9, "y": 46.1, "z": -46},
  "legs": [
    {"name": "keystone", "to": {"x": -247.5, "y": 173.1, "z": -43.5},
     "pull_groups": [{"entries": [4850, 4850], "ambush": true, "positions": [{"x": -232, "y": 198.1, "z": -44.5}, {"x": -223.6, "y": 198, "z": -44.5}]}],
     "skip": [11073],
     "steps": [
       {"use_object": {"entry": 124371, "near": {"x": -234.7, "y": 239.6, "z": -50.9}, "stand": {"x": -234.7, "y": 237, "z": -50.4}}},
       {"wait_for": {"gameobject": {"entry": 124372, "state": "active"}, "timeout_s": 60}}],
     "boss": {"entry": 7228, "position": {"x": -235.7, "y": 309.6, "z": -47.6}, "first": []}}
  ]
}
```

## Top level

- `version` is 1. `name` matches the file stem (`a-z`, `0-9`, `_`, `-`).
- `kind` is `dungeon` (the default) or `raid`.
- `status` is the truth about readiness, for example `draft (not mapped, not live-checked)`. See [Draft, mapped, live-checked](plan-lifecycle.md).
- `map` is the instance map id. `entrance` is where the party starts inside; `entrance_trigger` is the area trigger at the portal; `entrance_tele` and `outside_tele` are `game_tele` names; `portal` is the entrance portal on the outer map.
- `level_band` is `[low, high]`; the hired party takes the middle as its level.
- `legs` is the run in order. You need at least one leg.

## Legs

A leg is one stretch: walk `via` points, then `to`, clear `pull_groups` in order, run `steps` in order, kill `boss`[^plan-struct]. Every part is optional, but a leg must hold at least one of groups, steps, boss, or `after_boss` (steps done once the boss is dead).

- `via` `direct` steps are at most 25 yd long, drop at most 8 yd, and need ground within 3 yd of the landing point. The loader refuses a longer step or one with no prior `via` gather point. Followers gather within 5 yd horizontally (1 yd, 1.5 yd on stairs) before a direct step or the lead sends `cmd move <lead>`, then `cmd stay` every 3 s for 9 s; it never sends `cmd summon` there. The step caps at 25 s.
- `exact: true` turns a plain waypoint into a walked straight step (never a GM cross). Both ends walk along the travelled path at least ~6 yd (2D) and the landing proof needs looks within 1.5 yd and 0.5 yd of the anchor, 2 s apart. The step fails after 25 s (at most 3 rounds).
- `gm_cross: true` rides a direct step the party never walks: a GM `.appear`/`.go` moves the party. The step is refused unless the landing is GM-reachable. Naxxramas (2 waypoints) and Upper Karazhan Halls (3) still use it.
- Leg order is `via`, `to`, pulls, steps, boss, `after_boss`. Each part is optional but a leg needs at least one of groups, steps, boss, or `after_boss`. `boss.first` entries die in order before the boss.
- `summon_open_step` is the 1-based `use_object` step of the leg that summons the boss; the loader refuses any other step kind or number there.
- A pull group's `equiv` names equivalent packs (at most 40 characters). Its `radius` is 1-60 yd (default 25). A `patrol` loops its `path` (at least 2 points, last back to first) with `period_s` 1-900 s; `period_estimate: true` means the round is estimated, so the driver never predicts its phase and the check warns.
- `stand_pull` needs an `approach` list and never combines with `hold` or `ambush`. `clear_yd` is 1-60 (0 means the run leader's 40).
- `companions_loot: false` on a leg turns `.bot behavior loot off` for the whole leg and back on after; the group driver refuses it. It differs from the boss's `companions_loot`, which only mutes around the boss pull.
- `leave` entries need `{entry, at, why}` (plus `spawn` when set) and cover the whole run; the loader refuses a leave that names the leg's own pull position.
- `optional: {item, name}` skips the whole leg when neither carrier nor lead carries the item. The item must be a `requires_item` of a `use_object` step or the loader refuses it. `via_item: true` on a `use_object` step sends `cmd use <item> <bot> <object>` from within 4.5 yd; the export refuses it for the run leader. `proof` names exactly one of a game object or a creature (radius 0-20 yd).
- `use_object` checks the carrier's bags before the run and the step (backpack plus bags); an empty bag still loading waits 3 s (`ItemsWait`) and is not a missing item.
- `wait_for` on a game object takes `state` `ready`, `active`, or `active_alt` and `timeout_s` (default 60, at most 600). `fail_text` is at most 400 characters on one line. A unit wait names `entries` and one of seen, hostile, dead, or gone, and is never exported. `gone_ok` (default false) needs `near`, and the export sets flag 256 on the wait row.
- A unit wait on its own is never exported; with `agent_only` it runs on the agent only and never writes a row (with a warning).
- `area_trigger {id, center, radius, stand, by, expect, timeout_s}`: `stand` must sit inside the trigger (the loader refuses it outside) and within 30 yd of the boss when set; the default `by` is the lead via `BotAreaTriggers` entry 1002.
- `loot_object` is a `use_object` plus a spawn GUID and `expect_item`; the driver fails when the item is not taken within 8 s and retries once.
- `ritual {entry, position, participants}` takes 2-5 participants; the carrier calls first, then dps, healer, tank, and lead. The module holds until the object is active and never waits after.
- `gossip {npc, near, option|text, by, stand, retry_s, fail_text}`: `npc` must be the nearest spawn within 15 yd; `option` and `text` never combine. `stand` is within 5 yd of `near`. The default `by` is `agent`. `dead_is_done` sets flag 512 on the row. `retry_s` is 1-120 s (default 15). Text gossip matches one word per option word from the set `_ . , : + -`.
- `use_item {entry, name}` uses the item on the carrier itself; a second `requires_item` is consumed and pre-checked. A step that makes an item (loot, `use_item`, boss loot) exempts the leg-start check for it.
- `boss.loot_item {entry, name}` fails the run when the walk-loot check finds the corpse refused or empty. Loot method is one of `round_robin`, `group`, `need`, or `free_for_all`.
- The boss's `companions_loot: false` orders `.bot action loot off` before the pull and on again after the leader loots; a crash can leave looting off.
- A boss or group `team` skips the fight for the other team with a note; unknown races run everything.
- A bad file reports every problem with its path (`legs[2].steps[5]...`), refuses unknown fields, and refuses `skip` combined with a pull group or `first`.

## Pull groups

A pull group is one pack: `entries` (creature entries) and `positions` (where members stand; the first is the pull target)[^plan-struct]. The extras:

- `radius` (default 25): how far from a position a unit may stand and still count as a member.
- `ambush`: stealthed pack; the driver walks through and fights what shows itself.
- `patrol`: a pack that walks a `path` of waypoints in a loop, with `period_s` for one round. `period_estimate: true` means the round is only estimated, so the driver does not predict its phase.
- `approach`: up to four points the run leader walks before pulling; the last is the stand. The own driver ignores them.
- `stand_pull: true` holds the party at the last approach point and pulls from there.
- `max_bodies`: the pull's body-count ceiling for the `pull_size` lint.
- `hold`: replaces the pull with a scripted event the party fights on one spot until an instance data value reads DONE.

## Boss

A boss is `entry` plus `position`, with `first` entries killed before the boss itself[^plan-struct]. Useful extras: `loot_item` (the leader takes it from the corpse), `wait_s` (wait at the point for a patrolling boss), `radius` (search range for a patrolling boss), `approach` and `stand_pull` as for groups, `hold` for an event fight, and `tactics` (only `kill_first` reaches the remote driver).

## Steps

A step does exactly one thing: `use_object`, `wait_for` (game object state, no hostiles, or a unit in a state), `loot_object` (optionally in a `batch` opened back to back), `use_item`, `ritual` (several bots together; remote runs only), `gossip` (talk to an NPC and pick `option` or `text`), or `area_trigger` (someone steps into the zone until `expect` holds)[^plan-struct]. A step the run leader cannot do (agent gossip, a unit wait without a hold) cuts the export at its leg; see [From plan to run leader rows](run-leader-rows.md).

## Plan-wide lists

- `cc_priority`, `cc_always`, `no_cc`: which entries the pack crowd control takes, avoids, or never takes. Entries below the big pack still count, but the pull target is never crowd-controlled. Bosses, `first` entries, `kill_first` entries, and pets are never crowd-controlled.
- `kill_first`: entries the party kills before the rest; the list is stable-sorted after the tank rank and only orders targets that exist. The loader refuses a plan whose entries sit only in `cc_priority`. `summoned_kill_first` is the summoned subset inside `kill_first`: living hostiles within 10 yd of the encounter, never reactively crowd-controlled.
- `fleers`: entries that flee at low health (EventAI flees at 15% health) and are killed before they get away.
- `pull_ceiling`: plan-wide pull body cap, 1-80 (default 6, 8 for raids). `max_bodies` is the per-group ceiling, 1-80.
- `tank_min_hp`: 0 or 100-20000; a tank below it warns (strict preflight refuses). 0 or absent means no check. Every banded dungeon carries one; raids carry none.
- `hire_composition`: the hired party when `prepare.hire.composition` gives none, as `class` or `class:role` entries — 1 tank, 1 healer, 2-5 bots, for example `["warrior:tank", "priest:healer", "mage", "hunter"]`. Never on a raid plan.
- `watchdog {warn_s, recover_s, fail_s}`: warn < recover < fail must hold; zero keeps the default (120/240/420) and the run's watchdog wins field by field.
- `entrance_tele` and `outside_tele` are `game_tele` names (letters, digits, `_`). Without any trigger or teleports you walk in by hand.
- `avoid`: boxes of deadly ground the party never stands in or walks through; at most 32, refused when `min` is outside `max`, wider than 400 yd, or holding a plan point. See Deadly ground below.
- `raid: {size, tanks, healers, dps}`: required on raid plans.
- `boss.wait_s` is 1-600 s (default 60). `rest` percents are 1-100 and `max_s` is 1-600 s; 0 or absent keeps the driver's value.

## Deadly ground

The planner marks a clipped polygon lava when the `avoid` box covers at least a quarter of it. The driver never pulls round a box (past twice the distance plus 25 yd), never summons onto boxed ground, and walks the lead back along the travelled path when it stands boxed. A silent out-of-combat death at 80% health or more inside a box is only noted.

## Stepper

The stepper (`NewStepper`, `Next`) walks one leg without any network: states are walk, pull, `use_object`, `loot_object`, `use_item`, `ritual`, `area_trigger`, `gossip`, `kill_boss`, wait, done, and failed. It skips unneeded walks, ends timed-out waits, and walks first when a target is out of reach (at most 4 walks and 120 s per step).

## Other fields

The fields above cover the common case. The rest, each with its JSON name, what it does, and an example:

| Where | Field | What it does | Example |
| --- | --- | --- | --- |
| Plan | `team` | The party the plan needs (`alliance` or `horde`), when a step only works for one team; empty means either. A run of the other team is refused before its first order. | `"team": "horde"` |
| Plan | `tank_min_hp` | The least max health the hired tank should have for the plan's level band; 0 means no check. A tank below it is a preflight warning. | `"tank_min_hp": 800` |
| Plan | `hire_composition` | The hired party (`prepare.party "hire"`) when `prepare.hire.composition` gives none, as `class` or `class:role` entries. Empty means the default composition. Never on a raid plan (it has a `raid` block instead). | `"hire_composition": ["warrior:tank", "priest:healer", "mage:healer"]` |
| Plan | `watchdog` | The plan's stall levels in seconds without progress (`warn_s`, `recover_s`, `fail_s`); a zero field keeps the default (120/240/420). `dungeon_run`'s watchdog parameter wins field by field. | `"watchdog": {"warn_s": 120, "recover_s": 240, "fail_s": 420}` |
| Plan, leg | `notes` | Free-text notes for the reader and the mapping walk. | `"notes": ["Keystone opens the Seal of Khaz'Mul for Ironaya"]` |
| Leg | `needs_walk` | Marks a `to` that is not on the navmesh and could not be snapped to it: the mapping walk has to find the real waypoint. The same flag on a pull group or the boss says the same of its position. | `"needs_walk": true` |
| Leg | `rest` | Overrides the rest gates' targets and cap for this leg (`health`, `healer_mana`, `mana` percents, `max_s` seconds, plus the walk-while-resting floors). Absent means the driver's defaults. | `"rest": {"health": 90, "healer_mana": 50, "mana": 35, "max_s": 60}` |
| Pull group | `team` | Limits the group to a leader of that team; the driver skips it for the other. | `"team": "horde"` |
| Pull group | `equiv` | Names a set of equivalent packs of the leg (same mobs, same kind of ground); a pull-test scenario pulls the next pack of the set for its next try. Empty means no equivalent. | `"equiv": "stonevault_row"` |
| Pull group | `needs_walk` | The positions are not on the navmesh and could not be snapped to it; see the leg's `needs_walk`. | `"needs_walk": true` |
| Pull group | `clear_yd` | How far from the lead bot another patrol must be for a `stand_pull` group's pull to go out, yd (1-60; 0 means the run leader's 40). Only with `stand_pull`. | `"clear_yd": 40` |
| Boss | `phase_entries` | Entries the encounter turns into or summons that no other field names; only the raid walk's boss gates read them. The driver ignores them and the export does not carry them. | `"phase_entries": [14509]` |
| Boss | `team` | Limits the fight to a leader of that team; the driver skips the boss with its `first` entries for the other one. | `"team": "horde"` |
| Boss | `companions_loot` | `false` orders the party's corpse looting off before the boss is pulled and on again after the leader has looted, so a boss drop the plan needs (`loot_item`) goes to the leader. Absent or true leaves the companions as they are. | `"companions_loot": false` |
| Boss | `skip_unreachable` | Says the boss may stand out of the party's reach by design; the export sets the run leader's skip-unreachable flag on the boss row and carries this reason in its comment. Must be non-empty when set. | `"skip_unreachable": "swims a pool cut off from the shore"` |
| Boss | `summon_open_step` | Names the `use_object` step of this leg that summons the boss, as its 1-based place in the leg's `steps`. Only the raid walk reads it. | `"summon_open_step": 3` |
| Boss | `needs_walk` | The position is not on the navmesh and could not be snapped to it; see the leg's `needs_walk`. | `"needs_walk": true` |
| Hold | `instance_data` | The instance data value the event reads DONE from (the core's encounter state 3). A boss's hold may leave it out (0): the event is then done by its units. | `"instance_data": 1` |
| Hold | `object` / `state` | Makes the hold's done condition a game object state (`ready`, `active`, `active_alt`) instead of an instance data value. `instance_data` stays 0 then. | `"object": {"entry": 177233}, "state": "active"` |
| Hold | `spot` | Where the party stands and fights what the event sends. | `"spot": {"x": 100.0, "y": -200.0, "z": 10.0}` |
| Hold | `radius` | How far from `spot` the event's units may be fought, yd. | `"radius": 40` |
| Hold | `timeout_s` | How long the hold waits for DONE before it ends, seconds. | `"timeout_s": 120` |
| Hold | `entries` | More creature entries of the event (a group's own entries are always in). | `"entries": [7276]` |
| Hold | `value` | The script's own instance data value that ends the event, read as its number rather than as an encounter state. A wait's or a boss's hold may take one, never a pull group's, and never 0. | `"value": 7` |
| Hold | `skip_if_dead_of_gossip` | Lets a units hold end without a fight when the encounter's starting NPC was already dead at its gossip step. Only a boss's hold done by its units takes it. | `"skip_if_dead_of_gossip": true` |
| Hold | `reset_to_gossip` | Lets a units hold go back to the gossip step that started it when every unit of it is friendly again after the event started. Only a boss's hold done by its units takes it, and never with `skip_if_dead_of_gossip`. | `"reset_to_gossip": true` |
