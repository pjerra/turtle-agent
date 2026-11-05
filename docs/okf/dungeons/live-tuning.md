---
type: Guide
title: Tune a live run
description: Change a running commander run's numbers with tunables files, tune_set, and tune-diff.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/dungeon/tune.go
tags: [dungeon, commander, tuning, guide]
sources:
  - id: tune
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/dungeon/tune.go
    title: Tunables, limits, and file parsing
  - id: tune-methods
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/tune_methods.go
    title: tune_set, tune_list, and tune-diff
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:45:00+01:00
status: draft
---

# Tune a live run

A commander run reads a tunables file: one JSON object of key to number. Change a value mid-run with `tune_set`; read back what the run changed with `tune-diff`. Keys starting with `_` are comments. Every change lands at the next leg or pull-group start, never mid-step.

## 1. Give the run a tunables file

```sh
turtle-agent rpc <dir> dungeon_run '{"name":"<plan>","mode":"commander","prepare":{"party":"hire"},"tune":"<path>.json"}'
```

Empty `tune` takes the login's `--tune`, else `<plan>.tune.json` beside the plan file, which may be absent. A named file must exist at the start. `live_reload: false` stops the run from re-reading the plan and tunables files at leg and pull-group starts (`tune_set` still works).

## 2. Change one value mid-run

```sh
turtle-agent rpc <dir> tune_set '{"key":"pull_range","value":40}'
turtle-agent rpc <dir> tune_set '{"key":"pull_range","value":"default"}'
turtle-agent rpc <dir> tune_list
```

`value` is a number or `"default"` (back to the file's value, or the driver's own). A value outside the key's limits is refused. `tune_list` shows every key with its limits and current value. Tuning belongs to commander runs; with no run going, or a run in another mode, the call is refused.

## 3. Read back what changed

```sh
turtle-agent tune-diff <trace.jsonl>
```

`tune-diff` reads a run trace and prints the run's tune changes as a JSON patch for the tunables file and one for the plan file. Fold the numbers that helped back into the file.

## Tunable keys and limits

Units: `yd` yards, `s` seconds, `pct` percent (whole numbers), `count` (whole numbers).

| Key | Unit | Limits | Default | What it does |
|---|---|---|---|---|
| `move_leg` | yd | 10 to 150 | 120 | Longest single move of a walk |
| `move_arrive` | yd | 1 to 15 | 5 | How near a point the lead bot has arrived |
| `look_ahead` | yd | 2 to 40 | 10 | How near its waypoint the lead bot must be for the next move |
| `look_ahead_eta_s` | s | 0.5 to 10 | 2 | The look-ahead by time |
| `move_gap_s` | s | 0.2 to 10 | 1 | Least time between two move orders |
| `move_stall_s` | s | 3 to 60 | 10 | A lead bot with no ground gained this long gets the move again |
| `move_retries` | count | 1 to 10 | 4 | Stalls and refusals of one walk before it fails |
| `walk_attempts` | count | 1 to 8 | 3 | Refusals of one walk (on top of `move_retries`) before it fails |
| `lead_wait_s` | s | 5 to 120 | 30 | How long an order waits out lead-unavailable |
| `pull_range` | yd | 10 to 80 | 45 | A farther target is approached first |
| `pull_standoff` | yd | 5 to 70 | 38 | How far from the target the lead bot stops (at most `pull_range`) |
| `pack_radius` | yd | 5 to 60 | 25 | How far from a pull position a pack member may stand |
| `pull_attempts` | count | 1 to 6 | 3 | Failed pulls of one group before the run fails |
| `engage_timeout_s` | s | 5 to 90 | 25 | A pull drawing no fight this long has failed |
| `evade_grace_s` | s | 3 to 60 | 12 | A fight with nobody in combat this long is over |
| `assist_every_s` | s | 3 to 60 | 10 | How often the party is re-ordered onto the nearest mob |
| `aggro_quiet_s` | s | 1 to 15 | 3 | A fight on a walk is over when nothing hit the party this long |
| `post_fight_s` | s | 1 to 30 | 4 | Wait after the last pack member died |
| `adds_wait_s` | s | 1 to 90 | 15 | Longest wait for loose hostiles after a fight |
| `fight_timeout_s` | s | 30 to 900 | 300 | One pull group |
| `boss_timeout_s` | s | 60 to 1800 | 600 | One boss fight |
| `boss_search_s` | s | 0 to 600 | 60 | A kill-boss step that sees no boss at his spot waits this long for him to walk back into range (a patrolling boss), then fails; **0 turns the search off** |
| `look_standoff` | yd | 5 to 24 | 21 | How far short of a pack the lead bot stops to look |
| `attack_hold_yd` | yd | 10 to 80 | 45 | How near the mob the tank must be for threat to count |
| `tank_busy_rounds` | count | 1 to 6 | 3 | Waits of one pull for a tank staying in combat |
| `add_orders` | count | 1 to 6 | 3 | Tank orders at one loose mob |
| `fallback_tries` | count | 1 to 4 | 2 | Tries of the carrier's order before your character takes over |
| `threat_wait_s` | s | 1 to 30 | 6 | Longest wait for the tank to hold a mob |
| `opener_delay_s` | s | 0 to 15 | 2 | How long the tank must have held a mob before the party is sent in |
| `add_cooldown_s` | s | 2 to 60 | 8 | Least time between two tank orders at the same add |
| `tank_busy_wait_s` | s | 2 to 60 | 10 | One wait of a pull the module refused as tank-busy |
| `rez_wait_s` | s | 5 to 120 | 30 | How long a dead bot waits for resurrection before release |
| `revive_safe_yd` | yd | 10 to 80 | 30 | A dead bot is raised in place only with no live hostile this near |
| `flee_pct` | pct | 5 to 50 | 20 | A fought mob at/below this health moving away is a runner |
| `flee_wait_s` | s | 5 to 120 | 30 | How long a pull waits for an escaped runner |
| `flee_near_yd` | yd | 10 to 80 | 40 | A runner last seen within this of the lead bot is waited for |
| `rest_health_pct` | pct | 30 to 100 | 90 | Rested: every member's health at least this |
| `rest_mana_pct` | pct | 10 to 100 | 35 | Rested: every other mana user's mana at least this |
| `rest_healer_mana_pct` | pct | 10 to 100 | 60 | Rested: every healer's mana at least this |
| `graves_healer_mana_pct` | pct | 10 to 100 | 60 | The next grave batch waits while healer mana is under this |
| `rest_timeout_s` | s | 5 to 300 | 60 | Longest rest gate |
| `rest_min_health_pct` | pct | 20 to 100 | 50 | After the timeout the run goes on only with this health |
| `rest_pull_tank_pct` | pct | 30 to 100 | 80 | A giving-up rest gate waits longer for the pull floor |
| `rest_pull_healer_mana_pct` | pct | 10 to 100 | 60 | A giving-up rest gate waits longer for the healer floor |
| `rest_pull_extra_s` | s | 5 to 300 | 90 | How long past the cap a rest gate waits for pull floors |
| `rest_walk_yd` | yd | 10 to 500 | 60 | Walk length from which the start gate rests the party walking |
| `rest_walk_health_pct` | pct | 10 to 100 | 70 | Health floor for resting on a walk |
| `rest_walk_healer_mana_pct` | pct | 5 to 100 | 40 | Healer mana floor for resting on a walk |
| `rest_walk_mana_pct` | pct | 5 to 100 | 25 | Mana floor for resting on a walk |
| `maintenance_every_legs` | count | 0 to 20 | 3 | `.bot maintenance` every Nth leg and after a recovered wipe (0: never) |
| `gather_radius` | yd | 8 to 80 | 40 | The rest gate before a pull wants every living bot this near the lead |
| `careful_hop` | yd | 5 to 60 | 25 | Longest single move of a walk inside the dungeon |
| `gather_wait_s` | s | 1 to 60 | 12 | How long the party is waited for to gather |
| `gather_near` | yd | 4 to 40 | 12 | Every living bot within this of the lead bot counts as gathered |
| `gather_progress` | yd | 0.5 to 10 | 2 | Nearer-come bots are waited for again |
| `aggro_guard` | yd | 8 to 40 | 22 | A hostile this near the next hop is pulled first |
| `ahead_max_dz` | yd | 2 to 30 | 8 | How far above/below the hop a hostile may stand to be pulled first |
| `gather_clear` | yd | 10 to 60 | 30 | Clearing radius at a hop's end |
| `big_pack` | count | 3 to 20 | 5 | A pack this large gets one member crowd-controlled before the pull |
| `patrol_aggro` | yd | 10 to 50 | 25 | A patrol member this near a pull is a danger |
| `patrol_window_s` | s | 5 to 180 | 40 | A patrol arriving within this is a danger to the pull |
| `patrol_wait_s` | s | 0 to 180 | 90 | How long a pull waits for a patrol to leave |
| `patrol_locate_s` | s | 0 to 300 | 120 | How long a patrol is looked for |
| `patrol_pull_yd` | yd | 10 to 60 | 40 | A dangerous patrol is pulled from this far |
| `min_move` | yd | 0 to 10 | 6 | Shortest move the module carries out |
| `max_ahead_pulls` | count | 1 to 30 | 12 | At most this many pulls ahead are started |
| `look_arrive` | yd | 1 to 10 | 3 | How near its stand point the lead bot must get to look |
| `party_down_dead` | count | 1 to 5 | 3 | Dead bots that stop the leg |
| `rez_reach_yd` | yd | 10 to 100 | 30 | A farther body is released at once, not left for resurrection |
| `rejoin_tp_dist` | yd | 0 to 500 | 60 | A living bot farther than this is summoned to the lead (0: off) |
| `rejoin_tp_gap_s` | s | 10 to 600 | 60 | Least time between two summons of the same bot |
| `respawn_settle_s` | s | 1 to 30 | 6 | Pull test: wait after `.respawn` for the pack to show |

Tied values are checked together: `pull_standoff` within `pull_range`, `rest_min_health_pct` within `rest_health_pct`, `move_arrive` and `look_ahead` within `move_leg`.

The file boundary is the run start: changes land at the next leg or pull-group start, and a bad file fails once per version (`tune_change ok:false`). A plan reload matches legs by position; `ignored` rows stay out, and header/entrance/teleport/watchdog rows never reload. Pulltest reads the tunables before each try without reloading the plan.
