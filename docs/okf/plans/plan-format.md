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

## Top level

- `version` is 1. `name` matches the file stem (`a-z`, `0-9`, `_`, `-`).
- `kind` is `dungeon` (the default) or `raid`.
- `status` is the truth about readiness, for example `draft (not mapped, not live-checked)`. See [Draft, mapped, live-checked](/plans/plan-lifecycle.md).
- `map` is the instance map id. `entrance` is where the party starts inside; `entrance_trigger` is the area trigger at the portal; `entrance_tele` and `outside_tele` are `game_tele` names; `portal` is the entrance portal on the outer map.
- `level_band` is `[low, high]`; the hired party takes the middle as its level.
- `legs` is the run in order. You need at least one leg.

## Legs

A leg is one stretch: walk `via` points, then `to`, clear `pull_groups` in order, run `steps` in order, kill `boss`[^plan-struct]. Every part is optional, but a leg must hold at least one of groups, steps, boss, or `after_boss` (steps done once the boss is dead).

- `via` waypoints are walked in order before `to`. A plain waypoint follows the navmesh path. `"direct": true` is a straight-line step for a gap the navmesh does not bridge (at most 25 yd across, 8 yd down). `"exact": true` marks the launch point a straight step starts from. `"gm_cross": true` means a GM moves the party there; two plans use it (`naxxramas` on 2 waypoints, `upper_karazhan_halls` on 3) and the export refuses them without `--gm-cross-direct`. `"check_skip": "why"` exempts the hop from the plan check.
- `skip` lists creature entries the leg never waits for (rares, escorts). `leave` lists spawns the run never pulls, fights, or walks up to, with a `why` each.
- `optional: {item, name}` names the item the leg needs. At the leg's start the run looks for the item entry in the lead bot's and the carrier's bags (a remote run; the group driver checks the leader's bags) and skips the whole leg when neither carries it; a bag read that fails is an error, not a skip. `item` is the entry (required), `name` the name for the messages. `skip_export: true` leaves the leg out of the run leader rows unless asked for by name.
- `companions_loot: false` turns corpse and object looting off for the whole leg. `leftover_entries` names summoned creatures the driver sweeps before the next batch of chests.

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

A step does exactly one thing: `use_object`, `wait_for` (game object state, no hostiles, or a unit in a state), `loot_object` (optionally in a `batch` opened back to back), `use_item`, `ritual` (several bots together; remote runs only), `gossip` (talk to an NPC and pick `option` or `text`), or `area_trigger` (someone steps into the zone until `expect` holds)[^plan-struct]. A step the run leader cannot do (agent gossip, a unit wait without a hold) cuts the export at its leg; see [From plan to run leader rows](/plans/run-leader-rows.md).

## Plan-wide lists

- `cc_priority`, `cc_always`, `no_cc`: which entries the pack crowd control takes, avoids, or never takes.
- `kill_first`: entries the party kills before the rest; `summoned_kill_first` is the summoned subset, never crowd-controlled.
- `fleers`: entries that flee at low health and are killed before they get away.
- `pull_ceiling`: plan-wide pull body cap (default 6, 8 for raids).
- `avoid`: boxes of deadly ground the party never stands in or walks through.
- `raid: {size, tanks, healers, dps}`: required on raid plans.

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
