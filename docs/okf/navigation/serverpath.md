---
type: Concept
title: How the server walks
description: The server's own path query, and how the agent replays it.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/navmesh/serverpath.go
tags: [navigation, navmesh, server]
sources:
  - id: serverpath
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/navmesh/serverpath.go
    title: The server's own path query
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:40:00+01:00
status: draft
---

# How the server walks

The agent plans on the same mesh the server walks on, but with its own query. The server's query is stricter in places, so a route the planner calls `complete` can still be one the server would shorten. `ServerPath` replays the server's exact query so the checks see what the server sees.

## Two queries, two answers

The offline `Plan` asks Detour for a straight-path route the agent then walks corner to corner. `ServerPath` replays the core's `BuildPolyPath` plus `findSmoothPath` (2048 nodes, at most 256 polygons, ground and water included, smoothed to at most 256 points). The answer is a `ServerPath`: a `Type` bitmask (`0x1` normal, `0x4` incomplete, `0x8` no path), the smoothed `Points`, the `ActualEnd` (where the server's path really ends), the `Length`, and how far each endpoint sat from the mesh (`StartDist`, `EndDist`).

Unreachable is a normal answer here too: `Type` carries `0x8`, not an error. Only a missing tiles folder, a missing or broken map, or a closed planner fails the call.

## The verdict

`ServerMoveVerdict` reads one replayed path as the run leader's move check does:

- `ok`: the path ends within 5 yd of the goal (3D).
- `unreachable`: the server has no path and the path's end is within 2 yd of the start (no progress), or the start itself is within 1 yd of the goal.
- `off-mesh`: the path ends more than 5 yd short of the goal.
- `too-far`: the whole hop is longer than the caller's react distance (only when one is passed; `path-check` passes 0 and never reports it).

`path-check` prints this verdict next to the offline status, so one line shows both halves: what the agent planned and what the server would have walked.
