---
type: Guide
title: Draw the run map
description: Render a capture as a top-down SVG map, with an optional HTML replay.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/map_cmd.go
tags: [map, guide]
sources:
  - id: map-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/map_cmd.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:45:00+01:00
status: draft
---

# Draw the run map

`turtle-agent map` draws a run as a top-down map from what the agent already recorded, so a run can be read at a glance. It renders nothing live and takes no screenshots: the images are SVG (standard library only), with an optional single-file HTML replay.

## 1. Draw a capture

```sh
turtle-agent map --capture <walk-dir>/run.twcap --out <walk-dir>/run.svg
```

Positions of the party and of hostile creatures come from replaying the capture; the leader's route comes from the movement packets the agent sent. Give `--capture` and/or `--botwatch` (harness samples): with neither the command refuses with `map: give --capture and/or --botwatch: positions come from one of them`.

## 2. Add events, the report, and the plan

```sh
turtle-agent map --capture <walk-dir>/run.twcap --events <walk-dir>/events.jsonl --report <walk-dir>/report.json --plan dungeons/uldaman.json --out <walk-dir>/run.svg --html
```

`--events` replaces the capture's events with saved ones (a dungeon run's `dungeon_*` events are in the live log only). `--report` is the `dungeon_status` report: GM shortcuts flag the teleports and revives, deaths and wipes come from it. `--plan` draws the plan's walk, pull groups, bosses and objects under the run. `--html` also writes `run.html`: slider, play and pause, 1x to 600x, floor tabs and an event list.

## 3. Tune the frame

```sh
turtle-agent map --capture <walk-dir>/run.twcap --mmaps DIR --box 1000,1000,2000,2000 --from 2026-10-13T18:00:00Z --to 2026-10-13T19:00:00Z --out <walk-dir>/leg.svg --force
```

Every flag and its default:

| Flag | Default | What it does |
| --- | --- | --- |
| `--capture` | none | a `login --capture` file |
| `--events` | none | saved events used instead of the capture's own |
| `--report` | none | a `dungeon_status` report |
| `--botwatch` | none | harness samples (`t`, `bots[name, map, x, y, z]`) as extra routes |
| `--members` | none | with `--botwatch`, only these bots |
| `--plan` | none | a dungeon plan drawn over the run |
| `--out` | `map.svg` | the SVG to write; several floors write `NAME-f1.svg`, `NAME-f2.svg`, ... |
| `--html` | off | also write the single-file HTML replay next to the SVG (on/off: takes `=`, e.g. `--html=true`) |
| `--force` | off | let `--out` replace an existing file (never a capture) (on/off: takes `=`, e.g. `--force=true`) |
| `--config` | the settings file | its `mmaps_dir` gives the navmesh background |
| `--mmaps` | none | folder with the server's mmaps (overrides `mmaps_dir`) |
| `--map` | -1 | the map to draw (default: the one with most positions) |
| `--box` | none | draw only `x0,y0,x1,y1` in game coordinates |
| `--from`, `--to` | none | only what happened between these RFC 3339 times, both inclusive |
| `--pad` | 25 | yards of margin round what the run covers |
| `--floors` | `auto` | split stacked floors from the navmesh, `off`, or cut heights like `-30,-10` |
| `--background` | on | draw the navmesh floor (off: white, smaller and quicker; on/off: takes `=`, e.g. `--background=false`) |
| `--width` | 960 | the longest side of the map in pixels |

## 4. Read the map

The background is the navmesh floor under the run (ground, water, lava and slime told apart by colour). One line per member (a leap of 60 yd or more is a dashed pink teleport arrow; a hole over 30 s is dotted). Hostile creatures are dots where first seen: killed red, evaded orange, fought-but-alive purple, skipped hollow, bosses as stars with names. Pulls are numbered diamonds, deaths are crosses, leg starts and teleports are marked, and the plan overlay shows the planned walk under the real one. A north arrow, a scale bar and a legend say what is missing.

## 5. What the colours, floors, and HTML mean (and limits)

Floors split where the navmesh stacks 6+ yd apart (2% of walkable area to count) and write `NAME-f1.svg`, `NAME-f2.svg`, …; `--floors off` keeps one map, `--floors -30,-10` cuts at those heights. Route ticks pick the finest 15 s–4 h step leaving ≤12 ticks; holes over 30 s are dotted, teleports (60 yd+ leaps) pink dashed arrows. Hostile dots colour by fate (killed red, evaded orange, left purple, skipped hollow); packs within 10 yd join, bosses are stars. Pulls are diamonds after 15 s quiet; deaths crosses; game objects, teleports/GM shortcuts and leg starts are marked, GM flags from the report. The HTML replay adds a slider, play, 1–600x, floor tabs, an event list, `#t=`/`#f=` links. Writes are 0600; `--force` replaces an SVG, never a capture. The map sees view distance only: first-seen dots, no flight following, no textures or doors.
