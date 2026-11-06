---
type: Guide
title: Draw a run map
description: Turn captures, events and plans into SVG maps and HTML replays.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/map_cmd.go
tags: [navigation, map]
sources:
  - id: map-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/map_cmd.go
    title: The map command
  - id: mapview-build
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/mapview/build.go
    title: Building a run from frames and events
  - id: mapview-floors
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/mapview/floors.go
    title: Splitting a map into floors
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:34:00+01:00
status: draft
---

# Draw a run map

You draw an already-recorded run as a top-down map: one SVG per floor, and optionally a single-file HTML replay with a time slider. No server, no login: the command reads files and writes pictures.

## 1. Give it positions

The map needs positions from at least one of `--capture` (a `login --capture` file: party positions, hostile creatures, events) or `--botwatch` (botwatch samples as extra routes, with `--members a,b` to keep only some bots):

```sh
turtle-agent map --capture testdata/captures/m6-fight.twcap --background=false --out /tmp/fight.svg
```

Layer more on top: `--events` replaces the capture's own events with saved ones (the dungeon driver's events live only in the live log), `--report` adds a `dungeon_status` report (GM teleports, revives, deaths, wipes), `--plan` draws a dungeon plan over the run (its walk, pull groups, bosses; the plan's map becomes the drawn map, so pair it with a capture from that map). `--map` picks the map to draw (default: the one with most positions).

## 2. Frame the picture

The command draws the run's bounding box plus `--pad` yards of margin (default 25), or exactly `--box x0,y0,x1,y1` in game coordinates. `--from` and `--to` (RFC 3339) draw one stretch of a long run. The longest side is `--width` pixels (default 960).

Floors: `--floors auto` (the default) splits the map where walkable surfaces stack at least 6 yd apart, lowest floor first, hills and ramps never split; `off` draws one image; `--floors "-30,-10"` cuts at your heights (they must ascend). A multi-floor map writes `NAME-f1.svg`, `NAME-f2.svg`, and so on.

## 3. Add the background and the replay

By default the background is the navmesh clipped to the box, from `--mmaps` (which overrides `mmaps_dir` in `--config`). `--background=false` leaves it out for a smaller, quicker image. `--html` writes the replay next to the SVG with an `.html` extension: play, speeds 1x to 600x (30x selected), a 1000-step slider, floor tabs, and `#t=` / `#f=` deep links.

Files are written with mode `0600` and never replace an existing file unless you pass `--force` (which still never replaces a capture).

The legend reads: party routes (leader thick, a tick every 15 s to 4 h with at most 12 ticks, dotted gaps over 30 s, dashed jumps with arrows), hostile creatures by fate (red killed, grey never fought, orange evaded, purple fought but left alive; a dotted ring marks the pull radius), events (pull, death, wipe, door or lever, teleport or GM move, leg began), the plan overlay (dashed walk, diamond groups, star bosses) and the floor note. Only what the agent saw is drawn: units outside its view distance are missing.
