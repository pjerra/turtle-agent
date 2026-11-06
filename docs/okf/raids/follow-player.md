---
type: Guide
title: Follow the lead bot as a player
description: Walk behind a bot-led run on its trail, hold, regroup and stop rules.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/dungeon/follow.go
tags: [raids, follow, guide]
sources:
  - id: follow
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/dungeon/follow.go
generated:
  by: omp
  at: 2026-11-05T21:45:00+01:00
status: draft
---

# Follow the lead bot as a player

`follow_start` walks your character behind the lead bot of a bot-led run, on the lead's own trail. It never moves ahead of the lead, sends no chat and no GM line, and refuses to run beside `dungeon_run`: the two own the same movement mode, so one must stop before the other starts.[^follow]

## Start it

```sh
turtle-agent rpc <slot-dir> follow_start '{"plan":"<plan>","lead":"<character>","near":8,"far":15,"assist":false,"lost_s":30}'
```

| Field | Default | What it does | Example |
|---|---|---|---|
| `plan` | required | The plan file name (`a-z`, `0-9`, `_`, `-`). It gives the map, the portal and the entrance trigger. | `"plan":"<plan>"` |
| `lead` | required | The lead bot's character name. It must not be your own character. | `"lead":"<character>"` |
| `near` | `8` | The inner edge of the band behind the lead, 3-30 yd. | `"near":8` |
| `far` | `15` | The outer edge: a walk starts when the lead is farther, from `near+2` to 45 yd. | `"far":15` |
| `assist` | `false` | Melee auto-attack the lead's target (see below). Off while a brain runs. | `"assist":true` |
| `lost_s` | `30` | Stop when the lead stays out of view this long, 10-120 s. | `"lost_s":30` |

A repeated call while following keeps the run and only switches the lead; its `near`, `far`, `assist` and `lost_s` are ignored.

## How the walk works

The mode records the lead's trail: one point every 2 yd the lead moves, up to 300 yd kept, oldest dropped. A lead that jumps 30 yd between two looks was teleported or summoned: the trail starts again. The lead's heading comes from the trail point 5 yd back, and counts as known once the trail (with the newest look) is 6 yd long.[^follow]

When the lead is over `far` away, you walk to the halfway point between `near` and `far` behind the lead; when you stand ahead of the lead, you walk back behind it. While the heading is unknown (the lead moved under 6 yd, died, zoned, switched, or jumped) you hold still. A way that would pass in front of the lead is never walked. A lead 20 yd or more away with no heading for 20 s is walked to directly. One walk lasts at most 30 s; when its target moved 8 yd it is made again.[^follow]

In a fight you move only when the lead is over 40 yd away, then to 25 yd short of the lead along the path. A sitting player (eating, drinking) stands only when the lead is over 45 yd away. With `assist` on, the lead's target counts as tanked when it stands within 8 yd of the lead; you walk to it only from within 30 yd and stop 3 yd short, with melee auto-attack. The run leader holds pulls while you stand over 60 yd from the lead, and a regroup waits 60 s for every living player within 20 yd.[^follow]

Your own death goes through leader-mode recovery without `.bot action follow`: you release, run the corpse, reclaim, and wait in view of the lead again. After a zone, you walk to the portal only when the lead was last seen within 40 yd of it; the portal farther than 300 yd stops the mode. The portal walk lasts at most 180 s with 3 trigger tries 10 s apart.[^follow]

## When it stops

The mode stops on `lost_s` out of view, when the lead leaves the party, when your party is gone, after 5 walks in a row that did not arrive while the lead stands over 60 yd away, after a fallback way stays blocked for 60 s, or after 10 deaths. It never chats and never sends a GM line: no `Game.Chat`, `GM`, `GMOn` or `Teleport` call.[^follow]

`follow_status` reports `running`, `state`, `lead_yd`, `trail_yd`, `walks`, `walk_fails`, `deaths`, `corpse_runs`, `zone_ins`, `assists`, `max_lead_yd`, `seconds_over_60` and `stopped_reason`. The events carry `player_follow{phase, lead, reason, how, seconds}` with phases `started`, `walk`, `ahead`, `lost`, `zoning`, `zoned`, `dead`, `back` and `stopped`.
