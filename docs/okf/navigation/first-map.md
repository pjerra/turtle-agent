---
type: Tutorial
title: Draw your first run map
description: Turn a recorded capture into a top-down SVG map of the run.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/map_cmd.go
tags: [navigation, map, tutorial]
sources:
  - id: map-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/map_cmd.go
    title: The map command
  - id: mapview-scene
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/mapview/scene.go
    title: Map scenes, floors and backgrounds
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:30:00+01:00
status: draft
---

# Draw your first run map

You take a capture file from a logged-in run and draw it as a top-down SVG: the party's routes, the creatures they fought, and the pulls and deaths along the way.

You need a built `turtle-agent` binary (or `go run ./cmd/turtle-agent`) and a capture file. The repo's `testdata/captures/m6-fight.twcap` works: a 7-minute fight in the open world on map 1 (Kalimdor).

## 1. Draw the capture without a navmesh

Run the `map` command with the navmesh left out:

```sh
turtle-agent map --capture testdata/captures/m6-fight.twcap --background=false --out /tmp/fight.svg
```

The command prints what it drew:

```text
map: m6-fight.twcap: 2756 records, 335 frames, character Tagentone
map: map 1, x -1314 to -1251, y -2409 to -2349, 3 members, 62 creatures, 3 events, 1 floors; background left out (--background=false)
```

Open `/tmp/fight.svg` in a browser. You see three party routes, hostile creatures coloured by fate (red killed, grey skipped, orange evaded, purple still alive), and a legend on the right.

## 2. Add the time-slider replay

Pass `--html` to get a single-file replay next to the SVG, with play, speed (1x to 600x) and a time slider:

```sh
turtle-agent map --capture testdata/captures/m6-fight.twcap --background=false --html --out /tmp/fight.svg
```

Open `/tmp/fight.html` in a browser and press play. The routes draw up to the slider time; creatures appear when first seen.

## 3. Add the navmesh background

The white background above is the map without its navmesh. Point the command at the server's mmaps folder to draw the walkable ground under the routes:

```sh
turtle-agent map --capture testdata/captures/m6-fight.twcap --mmaps <walk-dir> --out /tmp/fight.svg
```

`<walk-dir>` holds copies of the server's `data/mmaps` files (`NNN.mmap` and `NNNyyxx.mmtile`). Multi-floor maps write one file per floor (`fight-f1.svg`, `fight-f2.svg`).

## What you learned

The `map` command reads positions from a capture, an events file or botwatch samples, and draws routes, creatures and events over the navmesh. Next, read [Draw a run map](draw-map.md) for every input and flag, or [The navmesh and the planner](navmesh.md) to understand the background you just drew.
