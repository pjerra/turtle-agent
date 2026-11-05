---
type: Guide
title: Watch the run live with map_snapshot
description: Sample the running agent and read the run so far as a map.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/map_methods.go
tags: [navigation, map, rpc]
sources:
  - id: map-methods
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/map_methods.go
    title: The map_snapshot RPC
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:34:00+01:00
status: draft
---

# Watch the run live with map_snapshot

You read the run so far as a top-down map while the agent is still playing: the party's routes, hostile creatures, events, and the plan overlay, one floor at a time.

The agent samples positions every 2 s while it runs (up to 40000 samples per member, 6000 creatures). Each `map_snapshot` call takes one fresh frame and builds the picture from the recording so far plus the live event log and the dungeon report.

## 1. Take the snapshot

```sh
turtle-agent rpc <slot-dir> map_snapshot '{}'
```

The answer carries `map`, `floor`, `floors` (their names), `seconds`, `members`, `creatures`, `events`, `background` (whether the navmesh is drawn), `notes` and `svg` (one floor as a string). Nothing is drawn yet when the character is not in the world: the call fails with `nothing to draw yet: the recorder has no position on the map (is the character in the world?)`.

## 2. Pick the floor and the overlay

Floors are 0-based; floor 0 is the lowest. Pass `floor` for another floor, `plan` (a plan name from the login's dungeons folder) to draw it over the run, and `floors` (`auto`, `off` or heights like `"-30,-10"`) to change the split:

```sh
turtle-agent rpc <slot-dir> map_snapshot '{"floor": 1, "plan": "uldaman"}'
```

Pass `"background": false` for a smaller, faster image without the navmesh, `"html": true` to also get the single-file replay, and `"pad"` (yards of margin; 25 or less means 25) to frame the picture. An unknown floor fails with `floor: this map has floors 0 to N`.

The snapshot reads the navmesh on its own tile cache, closed when the call returns, so a walk going on at the same time keeps its tiles.
