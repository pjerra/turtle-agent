---
type: Guide
title: Walk the navmesh
description: Plan a route and walk it with path_to, walk_path and walk_to.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/session/navwalk.go
tags: [navigation, navmesh, rpc]
sources:
  - id: navwalk
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/session/navwalk.go
    title: Path planning and walking in the session
  - id: methods
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/methods.go
    title: RPC method registration
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:33:00+01:00
status: draft
---

# Walk the navmesh

You move the logged-in character along the server's navmesh: first plan the route, then walk it. The agent must be logged in, in the world, and its settings file must set `mmaps_dir` to a folder of server mmaps.

Send these calls with `turtle-agent rpc <slot-dir> <method> <json>`:

```sh
turtle-agent rpc <slot-dir> path_to '{"x": -1287.0, "y": -2379.6, "z": 91.7}'
```

## 1. Check whether the route exists

Call `path_to` with the goal coordinates. It plans from where you stand and does not move:

```sh
turtle-agent rpc <slot-dir> path_to '{"x": -1287.0, "y": -2379.6, "z": 91.7}'
```

The answer carries `status` (`complete`, `partial` or `no_path`), `points` (the corners, first is your position, last is the goal or the nearest reachable point), `length` in yards and `engine` (`detour`).

A `no_path` answer means the goal is off the mesh, not an error: pick a goal nearer the walkable ground.

## 2. Walk the route

Call `walk_path` with the same goal:

```sh
turtle-agent rpc <slot-dir> walk_path '{"x": -1287.0, "y": -2379.6, "z": 91.7}'
```

The agent walks the plan's corners at run speed, facing into each turn the way a real client does. The answer's `result` is `arrived` (it got there), `partial` (it stopped short: no route all the way, the server kept moving it, or took it to another map) or `no_path` (nothing walked). `dist_left` says how far is left, `walked` how many yards the agent walked itself, and `trace` lists each plan, walk and interruption step.

## 3. Read an interruption

When the server moves the player (fear, a scripted move, a knockback) or roots it, the walk waits for the server to let go, plans again from the new position, and walks on. It plans again at most 8 times; the 9th interruption ends the walk with a `partial` result and a reason like `the server moved the player 9 times; gave up`. Only a lost connection, a cancelled call, a missing navmesh or a planner error fails the call itself.

## Short walks without the mesh

`walk_to` walks a straight line to a nearby point, with no navmesh involved:

```sh
turtle-agent rpc <slot-dir> walk_to '{"x": -1285.0, "y": -2380.0, "z": 91.7}'
```

Use it for small steps you can see. For anything farther, use `walk_path`. `teleport` (a GM `.go xyz`) needs a GM account and skips walking entirely.
