---
type: Concept
title: The trace checks
description: What each trace check measures, its default threshold, and when it answers n/a.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/tracecheck/tracecheck.go
tags: [traces, tracecheck]
sources:
  - id: tracecheck
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/tracecheck/tracecheck.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:49:00+01:00
status: draft
---

# The trace checks

The scorer runs 13 checks over a trace. Each returns an `id`, a verdict (`pass`, `fail`, or `na` when the trace lacks what the check needs), the measured value against the threshold, a count, and evidence times. Ten can fail; the last three only warn.

| `id` | Fails when | Default | n/a when |
| --- | --- | --- | --- |
| `rest_share` | the rest gates took more than this share of the run | 40 % | the run is shorter than 60 s |
| `stale_party` | a mana user's power did not change for this long while someone else's health or power did (the party list stopped updating) | 60 s | no frames with a mana user |
| `walk_stops` | the lead stood still mid-walk of the plan more often than this per 100 yd walked; the last 3 s of a walk count as arriving | 4 stops / 100 yd | under 100 yd walked |
| `threat_off_tank` | a mob in combat stayed on a non-tank during a pull longer than this | 4 s | no threat data (group runs have none) |
| `deaths` | any death or wipe | 0, no threshold | never |
| `order_refusals` | more than this share of orders was refused | 20 % | fewer than 3 orders |
| `feed_stalls` | the longest gap between two feed frames is over this | 10 s | no feed frames (group runs) |
| `gm_shortcuts` | the run used any GM shortcut or was set up with `setup` commands | 0, no threshold | never |
| `stuck` | a member that should move stood still for this long | 5 s, any stall | no walk of the plan, or no frames |
| `facing` | a bot in melee range of its hostile target stood turned away (over 90 degrees) for this long | 2 s | no `facing` capability, or no such frame |
| `spells` | report only: what each class cast and did, as readings of 1 Hz samples (cast episodes from the `cs` spell field, action mix, idle share, interrupts matched to enemy casts by proximity, taunts, crowd-control holds) | warn at `idle_share` 15 %, `interrupt_cover` 30 %, `taunt_latency` 3 s, `cc_hold` 50 %, `pet_threat` 5 % | no party bot with casts |
| `spell_oracle` | report only: each bot's casts judged against its Turtle WoW knowledge-base spec (a missing core spell warns once the bot fought for this long; a never-cast spell always warns) | 60 s (fight data: 20 s) | no party bot with casts, or none active long enough |
| `oom_in_fight` | report only: a mana user sat under 5 % mana in combat for this long | 10 s | no mana user in combat |

A group run takes hp and power from the agent's own party list (so a list that stopped updating shows up as `stale_party`) and positions, facing, targets, combat and movement from the object store, for members in view. In a remote run the party records have no class or level and `role` is only `tank` where `tank` was given.
