---
type: Guide
title: Moving
description: Walk short hops, plan paths, walk them, and teleport with a GM account.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/session/actions.go
tags: [agent, movement, walk, teleport]
sources:
  - id: actions-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/session/actions.go
    title: walk_to, teleport, target, cast
  - id: navwalk-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/session/navwalk.go
    title: path_to and walk_path on the navmesh
  - id: move-mode-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/move_mode.go
    title: Single movement owner (follow vs dungeon run)
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T22:45:00+01:00
status: draft
---

# Moving

## 1. Walk a short hop

```sh
turtle-agent rpc <walk-dir> walk_to '{"x": 1.0, "y": 2.0, "z": 3.0}'
```

`walk_to` walks straight, at most 40 yards and 30 seconds. Longer or slower is refused — use `walk_path` or `teleport`:

`walk_to is for short moves: <n> yards is over 40, use walk_path or teleport`

While a walk is going, `cast` and `face` are refused with `walking; wait for walk_to to finish`.

## 2. Plan a route without moving

```sh
turtle-agent rpc <walk-dir> path_to '{"x": 1.0, "y": 2.0, "z": 3.0}'
```

`path_to` answers `{map, from, to, status, points, length}` and moves nothing. Both it and `walk_path` need the server's mmaps configured, else:

`path_to and walk_path need the server's mmaps: set "mmaps_dir" in the settings file`

## 3. Walk a planned route

```sh
turtle-agent rpc <walk-dir> walk_path '{"x": 1.0, "y": 2.0, "z": 3.0}'
```

The result is `{result, reason, goal, position, dist_left, walked, replans, seconds, trace}` where `result` is `arrived`, `partial` or `no_path`. Shortfalls are in the report, not errors: `partial` with a reason such as still being rooted or moved by the server.

## 4. Teleport (GM account only)

```sh
turtle-agent rpc <walk-dir> teleport '{"x": 1.0, "y": 2.0, "z": 3.0}'
```

Omit `map` to stay on the same map. Without GM rights the call times out:

`teleport: no MSG_MOVE_TELEPORT_ACK or SMSG_NEW_WORLD within 10 s (does the account have GM rights?)`

## 5. Yield the legs when something else drives

Only one thing moves the character at a time. A running `follow` or `dungeon_run` owns movement; your calls are refused until you stop it:

`a player follow is going: follow_stop it first`

`a dungeon run is going: it moves the character itself (dungeon_stop it first)`
