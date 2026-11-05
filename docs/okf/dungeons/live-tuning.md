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

| Key | Limits | What it does |
|---|---|---|
| `move_leg` | 10–150 yd | Longest single move of a walk |
| `move_arrive` | 1–15 yd | How near a point the lead bot has arrived |
| `look_ahead` | 2–40 yd | How near its waypoint the lead bot must be for the next move |
| `look_ahead_eta_s` | 0.5–10 s | The look-ahead by time |
| `move_gap_s` | 0.2–10 s | Least time between two move orders |
| `move_stall_s` | 3–60 s | A lead bot with no ground gained this long gets the move again |
| `move_retries` | 1–10 | Stalls and refusals of one walk before it fails |
| `walk_attempts` | 1–8 | Refusals of one walk (on top of `move_retries`) before it fails |
| `lead_wait_s` | 5–120 s | How long an order waits out lead-unavailable |
| `pull_range` | 10–80 yd | A farther target is approached first |
| `pull_standoff` | 5–70 yd | How far from the target the lead bot stops (at most `pull_range`) |
| `pack_radius` | 5–60 yd | How far from a pull position a pack member may stand |
| `pull_attempts` | 1–6 | Failed pulls of one group before the run fails |
| `engage_timeout_s` | 5–90 s | A pull drawing no fight this long has failed |
| `evade_grace_s` | 3–60 s | A fight with nobody in combat this long is over |
| `assist_every_s` | 3–60 s | How often the party is re-ordered onto the nearest mob |
| `aggro_quiet_s` | 1–15 s | A fight on a walk is over when nothing hit the party this long |
| `post_fight_s` | 1–30 s | Wait after the last pack member died |
| `adds_wait_s` | 1–90 s | Longest wait for loose hostiles after a fight |
| `fight_timeout_s` | 30–900 s | One pull group |
| `boss_timeout_s` | 60–1800 s | One boss fight |
| `look_standoff` | 5–24 yd | How far short of a pack the lead bot stops to look |
| `attack_hold_yd` | 10–80 yd | How near the mob the tank must be for threat to count |
| `tank_busy_rounds` | 1–6 | Waits of one pull for a tank staying in combat |
| `add_orders` | 1–6 | Tank orders at one loose mob |
| `fallback_tries` | 1–4 | Tries of the carrier's order before your character takes over |
| `threat_wait_s` | 1–30 s | Longest wait for the tank to hold a mob |
| `opener_delay_s` | 0–15 s | How long the tank must have held a mob before the party is sent in |
| `add_cooldown_s` | 2–60 s | Least time between two tank orders at the same add |
| `tank_busy_wait_s` | 2–60 s | One wait of a pull the module refused as tank-busy |
| `rez_wait_s` | 5–120 s | How long a dead bot waits for resurrection before release |
| `revive_safe_yd` | 10–80 yd | A dead bot is raised in place only with no live hostile this near |
| `flee_pct` | 5–50 pct | A fought mob at/below this health moving away is a runner |
| `flee_wait_s` | 5–120 s | How long a pull waits for an escaped runner |
| `flee_near_yd` | 10–80 yd | A runner last seen within this of the lead bot is waited for |
| `rest_health_pct` | 30–100 pct | Rested: every member's health at least this |
| `rest_mana_pct` | 10–100 pct | Rested: every other mana user's mana at least this |
| `rest_healer_mana_pct` | 10–100 pct | Rested: every healer's mana at least this |
| `graves_healer_mana_pct` | 10–100 pct | The next grave batch waits while healer mana is under this |
| `rest_timeout_s` | 5–300 s | Longest rest gate |
| `rest_min_health_pct` | 20–100 pct | After the timeout the run goes on only with this health |
| `rest_pull_tank_pct` | 30–100 pct | A giving-up rest gate waits longer for the pull floor |
| `rest_pull_healer_mana_pct` | 10–100 pct | A giving-up rest gate waits longer for the healer floor |
| `rest_pull_extra_s` | 5–300 s | How long past the cap a rest gate waits for pull floors |
| `rest_walk_yd` | 10–500 yd | Walk length from which the start gate rests the party walking |
| `rest_walk_health_pct` | 10–100 pct | Health floor for resting on a walk |
| `rest_walk_healer_mana_pct` | 5–100 pct | Healer mana floor for resting on a walk |
| `rest_walk_mana_pct` | 5–100 pct | Mana floor for resting on a walk |
| `maintenance_every_legs` | 0–20 | `.bot maintenance` every Nth leg and after a recovered wipe (0: never) |
| `gather_radius` | 8–80 yd | The rest gate before a pull wants every living bot this near the lead |
| `careful_hop` | 5–60 yd | Longest single move of a walk inside the dungeon |
| `gather_wait_s` | 1–60 s | How long the party is waited for to gather |
| `gather_near` | 4–40 yd | Every living bot within this of the lead bot counts as gathered |
| `gather_progress` | 0.5–10 yd | Nearer-come bots are waited for again |
| `aggro_guard` | 8–40 yd | A hostile this near the next hop is pulled first |
| `ahead_max_dz` | 2–30 yd | How far above/below the hop a hostile may stand to be pulled first |
| `gather_clear` | 10–60 yd | Clearing radius at a hop's end |
| `big_pack` | 3–20 | A pack this large gets one member crowd-controlled before the pull |
| `patrol_aggro` | 10–50 yd | A patrol member this near a pull is a danger |
| `patrol_window_s` | 5–180 s | A patrol arriving within this is a danger to the pull |
| `patrol_wait_s` | 0–180 s | How long a pull waits for a patrol to leave |
| `patrol_locate_s` | 0–300 s | How long a patrol is looked for |
| `patrol_pull_yd` | 10–60 yd | A dangerous patrol is pulled from this far |
| `min_move` | 0–10 yd | Shortest move the module carries out |
| `max_ahead_pulls` | 1–30 | At most this many pulls ahead are started |
| `look_arrive` | 1–10 yd | How near its stand point the lead bot must get to look |
| `party_down_dead` | 1–5 | Dead bots that stop the leg |
| `rez_reach_yd` | 10–100 yd | A farther body is released at once, not left for resurrection |
| `rejoin_tp_dist` | 0–500 yd | A living bot farther than this is summoned to the lead (0: off) |
| `rejoin_tp_gap_s` | 10–600 s | Least time between two summons of the same bot |
| `respawn_settle_s` | 1–30 s | Pull test: wait after `.respawn` for the pack to show |

Tied values are checked together: `pull_standoff` within `pull_range`, `rest_min_health_pct` within `rest_health_pct`, `move_arrive` and `look_ahead` within `move_leg`.
