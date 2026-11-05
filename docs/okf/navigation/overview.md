---
type: Overview
title: How navigation works
description: A tour of how the agent moves, sees and draws the world it walks through.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/navmesh/planner.go
tags: [navigation, navmesh, map]
sources:
  - id: planner
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/navmesh/planner.go
    title: The navmesh planner
  - id: navwalk
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/session/navwalk.go
    title: Path planning and walking in the session
  - id: mapview
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/mapview/scene.go
    title: Map scenes, floors and backgrounds
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:30:00+01:00
status: draft
---

# How navigation works

The agent moves on the server's own navmesh, watches what walks nearby, and draws what happened as a top-down map.

Five pieces work together:

1. **The navmesh** is a set of walkable polygons the server exported: one `.mmap` file per map plus `.mmtile` tiles. You keep copies in a folder and point the agent at it with `mmaps_dir` in the settings file. Without it the agent can still log in and fight, but it cannot plan a route.
2. **The planner** (`internal/navmesh/planner.go`) answers questions about that mesh: plan a route from here to there, list the floors stacked over one spot, cast a sight ray, find the nearest walkable point. A route's status is `complete` (all the way there), `partial` (to the nearest reachable point) or `no_path`.
3. **The walk** (`internal/session/navwalk.go`) turns a plan into movement. `path_to` plans without moving; `walk_path` plans and walks the corners like a real client, waiting out roots and server moves, planning again up to 8 times when the server shoves the player around.
4. **The world data** says what wakes when the party walks: the world export (spawn positions, packs, patrol loops) plus an aggro model (aggro radius per creature, the call-for-help ring, body-pull estimates). Offline commands check a plan against this data before anyone logs in.
5. **The map** draws it all: the navmesh as background, the party's routes, hostile creatures, pulls, deaths and doors, one image per floor, or a live snapshot of the run so far.

The usual order is: check the route offline (`plan-check`, `trash-check`), walk it live (`walk_path`, the dungeon run), then draw it (`map`, `map_snapshot`).

The dashboard can draw the real game map under the run. That picture comes from the game client's own files: the `minimap-extract` command reads the client's MPQ archives and writes PNG tiles the dashboard serves.

Map ids are the server's: 0 is Eastern Kingdoms, 1 is Kalimdor, and each dungeon has its own number (70 is Uldaman, 389 is Ragefire Chasm).

Start with [Draw your first run map](/navigation/first-map.md), or read the checks in [Check a route before the run](/navigation/checks.md).
