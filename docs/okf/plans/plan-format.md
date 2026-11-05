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

- `via` waypoints are walked in order before `to`. A plain waypoint follows the navmesh path. `"direct": true` is a straight-line step for a gap the navmesh does not bridge (at most 25 yd across, 8 yd down). `"exact": true` marks the launch point a straight step starts from. `"gm_cross": true` means a GM moves the party there; no plan uses it now. `"check_skip": "why"` exempts the hop from the plan check.
- `skip` lists creature entries the leg never waits for (rares, escorts). `leave` lists spawns the run never pulls, fights, or walks up to, with a `why` each.
- `optional: {item, name}` skips the whole leg when neither the lead bot nor the carrier carries the item. `skip_export: true` leaves the leg out of the run leader rows unless asked for by name.
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
