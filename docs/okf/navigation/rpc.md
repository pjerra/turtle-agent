---
type: Reference
title: Navigation RPCs
description: Every navigation call a logged-in agent answers, with params and answers.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/methods.go
tags: [navigation, reference, rpc]
sources:
  - id: methods
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/methods.go
  - id: navwalk
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/session/navwalk.go
  - id: map-methods
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/map_methods.go
  - id: follow-methods
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/follow_methods.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:45:00+01:00
status: draft
---

# Navigation RPCs

Every navigation call the agent answers. Send them with `turtle-agent rpc <slot-dir> <method> '<json>'`. All coordinates are game `x, y, z` in yards.

## path_to

Plans a route from where you stand, without moving. Params: `x`, `y`, `z`.

```sh
turtle-agent rpc <slot-dir> path_to '{"x": -1287.0, "y": -2379.6, "z": 91.7}'
```

Answer: `map`, `from`, `to`, `status` (`complete`, `partial` or `no_path`), `reason` (why partial or no path), `points` (the corners), `length` (yd) and `engine` (`detour`).

## walk_path

Plans a route and walks it as one walk, turning at the corners without stopping. Params: `x`, `y`, `z`.

```sh
turtle-agent rpc <slot-dir> walk_path '{"x": -1287.0, "y": -2379.6, "z": 91.7}'
```

Answer: `result` (`arrived`, `partial` or `no_path`), `reason`, `goal`, `position` (where it ended), `dist_left` (yd), `walked` (yards the agent walked itself), `replans` (how many times it planned again), `seconds`, and `trace` (one step per plan, walk and interruption: `t_ms`, `kind`, `status`, `from`/`to`, `points`, `length`, `text`). A server move or root ends the walk early with a `partial` result; only a missing navmesh, a planner error, a lost connection or a cancelled call fails the call itself.

## walk_to

Walks a straight line to a nearby point, no navmesh involved. Params: `x`, `y`, `z`. Answer: empty.

At most 40 yd (`maxWalk`) within 30 s (`maxWalkTime`), timed on the 3D distance at run speed; farther or slower is refused before anything walks. `teleport` below takes a GM account instead.

## teleport

A GM `.go xyz` to a point, optionally on another map. Needs a GM account. Params: `x`, `y`, `z`, optional `map`. Answer: empty.

## map_snapshot

Draws the run so far as a top-down map (see [Watch the run live with map_snapshot](map-snapshot.md)). Params:

| Field | Default | What it does | Example |
|---|---|---|---|
| `floor` | `0` | The floor to draw, from 0 (lowest first). | `1` |
| `plan` | `""` | A plan name from the dungeons folder, drawn over the run. | `"uldaman"` |
| `floors` | `"auto"` | `auto`, `off`, or heights like `"-30,-10"`. | `"off"` |
| `background` | `true` | `false` leaves the navmesh out (smaller, faster). | `false` |
| `html` | `false` | Also return the single-file replay. | `true` |
| `pad` | `25` | Yards of margin round what the run covers; 0 or less means 25. | `40` |

Answer: `map`, `floor`, `floors` (their names), `seconds`, `members`, `creatures`, `events`, `background` (whether the navmesh is drawn), `notes`, `svg` (one floor), optional `html`.

The recorder samples the session every 2 s (`mapSampleEvery`); memory is bounded (40k samples per member, 6k creatures). The background loads in a scratch planner of its own, closed when the call returns, so live plans keep their tiles. The plan's bosses mark this image only. An out-of-range `floor` is refused (`floor: this map has floors 0 to N`).

## plan_check

Checks a plan on the navmesh from inside the run (the offline `plan-check` command checks from files). Params: `name` (a plan file name such as `"zulfarrak"`), optional `from_leg` (a leg number or name) and `from_pull_group` (0-based, needs `from_leg`). Answer: `checked` (false with `skipped` saying why: no navmesh files or planner), the `plan`, `map`, `points`, `issues` (each with `leg`, `part`, `reason` and `detail`), `warnings`, and `reanchored`.

## Follow the lead bot

`follow_start` walks the agent behind the run leader's tank along the plan's route. Params: `plan` (required), `lead` (the lead bot's name; required, never yourself), `near` (default 8.0, 3–30 yd), `far` (default 15.0, near+2 to 45), `assist` (default false), `lost_s` (default 30, 10–120). `follow_status` and `follow_stop` take no params; the status answers `running`, `state`, `plan`, `lead`, positions, `walks`, `walk_fails`, `max_lead_yd`, `stopped_reason` and notes.
