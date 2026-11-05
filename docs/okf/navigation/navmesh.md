---
type: Concept
title: The navmesh and the planner
description: What the navmesh is, what the planner answers, and how routes are built.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/navmesh/planner.go
tags: [navigation, navmesh]
sources:
  - id: planner
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/navmesh/planner.go
    title: The navmesh planner
  - id: engine-nocgo
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/navmesh/engine_nocgo.go
    title: The no-cgo stub engine
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:40:00+01:00
status: draft
---

# The navmesh and the planner

The navmesh is the server's model of where a character on foot can walk. The planner reads it and answers route questions for the walks, the checks and the maps.

## Tiles

The server exports one `.mmap` file per map (the mesh parameters) plus `.mmtile` tiles, each covering a 533.33-yd square named `%03d%02d%02d.mmtile` from the map, tile-y and tile-x. You copy them verbatim into a folder and set `mmaps_dir` to it. The planner loads tiles on demand around the query (first a 1-tile margin, then 3; small maps of 16 tiles or fewer load whole), keeps at most 64 per map, and re-reads the folder on each query so tiles copied in later count.

Each polygon carries flags: ground, magma, slime, water, steep. A character on foot walks ground and water, never magma, slime or steep. Hills and ramps are steep, so they never vote in floor splits and never carry a route.

## What the planner answers

`Plan` returns a `Path`: a `status` (`complete`, `partial`, `no_path`), an optional `Reason`, the `Points` (Detour's straight-path corners, first is your start, last is the goal or the nearest reachable point), the 3D `Length`, and the `Engine`. Ground samples every 4 yd between corners (`Ground`) let the walk follow the surface instead of cutting through it. `PlanNoWater` plans the same route with water excluded. `Floors` lists the walkable surfaces stacked over one `x,y` (highest first), used when a fall, knockback or teleport needs a floor to land on. `Raycast` answers sight lines (known and clear, or the hit distance), `Nearest` the nearest walkable point within a 4-by-4-by-6 box, `Polygons` the mesh under a box for the map background.

Partial is a normal answer, not an error: the status says how far the mesh goes, the reason names the nearest reachable point and its distance to the goal. `no_path` with `Points` empty means the start or the goal sits off the mesh (no walkable polygon within 5 yd) or Detour found no route.

## Avoid boxes

`SetAvoid` marks polygons whose area sits at least 25 percent inside given boxes as magma, now and on every later tile load: routes then flow around them. The route comparison (`route-oracle`) plans twice, plain and avoid-aware, and scores the avoid-aware path the run plans with.

## Without cgo

The Detour engine needs cgo and a C++ compiler. A binary built without cgo has the stub engine (`engineName "none"`): every map load fails with `navmesh: this turtle-agent was built without cgo, so it has no Detour engine; rebuild with CGO_ENABLED=1 and a C++ compiler`, and map drawing falls back to a white background.

## Walk execution, tiles, engine, limits

The engine runs the server's own Detour sources (vendored `detour/`, zlib) over mmap version 6 tiles holding Detour version 7 data with 64-bit poly refs, through cgo. It mirrors the server's `PathFinder`: `findNearestPoly` with 5 yd round / 10 yd height extents and never a floor more than 3 yd above the point, the on-foot filter (ground and water in, lava/slime/steep out), then `findPath` and `findStraightPath`. A goal over 7 yd from the mesh ends the path incomplete.

`walk_path` walks the plan's corners at run speed with the facing of a real client: `MSG_MOVE_SET_FACING` for turns of 0.01 rad or more (cumulative), heartbeats every 500 ms on a 100 ms tick, `STOP` at the end. It waits out roots up to 15 s (`rootWait`), plans again at most 8 times (the 9th server move ends the walk `partial`), and plans on from a partial path's end up to 32 times (`maxHops`) while each hop gains 10 yd (`minHop`). Landing on another map ends the walk `partial`; a missing navmesh, planner error, lost connection or cancelled call fails the call itself. Each step lands in `trace` (`t_ms`, `kind` plan/walk/interrupted, `status`, `from`/`to`, `points`, `length`, `text`).

Limits: closed doors and game objects are not on the mesh (untested); run speed applies in water; the walked line is straight corner to corner, not smoothed, through a 2–8 yd corridor.
