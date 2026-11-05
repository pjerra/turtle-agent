---
type: Guide
title: Put a real map under the run
description: Extract the game's own map pictures and draw the run over them.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/minimap_cmd.go
tags: [navigation, minimap, dashboard]
sources:
  - id: minimap-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/minimap_cmd.go
    title: The minimap-extract command
  - id: minimap-extract
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/minimap/extract.go
    title: The extraction pipeline
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:36:00+01:00
status: draft
---

# Put a real map under the run

You draw the game's own map pictures out of the client you own, so the dashboard can lay the run over the real terrain instead of a white page. The pictures are local-only: they stay in a folder on your machine.

## 1. Extract the pictures

Point `minimap-extract` at the client's `Data` folder (the MPQ archives; read only) and an output folder:

```sh
turtle-agent minimap-extract --client <client-data> --out ~/turtle-agent-minimaps
```

The default `--maps all-instances` extracts every dungeon and raid in `Map.dbc`; pass `--maps 70,209` for just some maps. Each map gets `<id>.png` plus `<id>.json` (position box, pixels per yard, tile list), one per image source: `worldmap` (the area picture, 4x3 tiles cut to 1002x668), `minimap` (the terrain and indoor WMO tiles, 2 pixels per yard) and `navmesh` (a height-shaded render of the server mmaps, needs `--mmaps`). The first source in `--prefer` that exists (default `worldmap,minimap,navmesh`) is copied to `<id>.png`. `--list` reports what each map would get and writes nothing; `--verbose` logs every map and skipped tile.

## 2. Serve them to the dashboard

Pass the folder to the dashboard:

```sh
turtle-agent dashboard --minimap-dir ~/turtle-agent-minimaps
```

The run map draws over the image of its map id when the folder has one; otherwise it draws over white. A folder that is not there only warns (`dashboard: --minimap-dir %s is not a folder: the map has no background image`) and the maps draw without pictures.

## What can be missing

Five dungeon maps (Deadmines, Shadowfang Keep, Scholomance, Stratholme, Karazhan Crypt) overlay indoor WMO tiles on the terrain; indoor-only dungeons get the WMO tiles alone. When a source is missing, `<id>.status.json` says why in one line per source, and the dashboard picker shows that slot as unavailable with the reason. Coordinates convert with the JSON box: column `c` sits at `y = west - c / px_per_yard`, row `r` at `x = north - r / px_per_yard`. North is up, west is left, one unit is one yard.

## Sources, slots, limits

The extractor reads the MPQ archives in order: base `dbc.MPQ`, then `patch-9.MPQ` (later wins). Each map's JSON holds `map`, `name`, `source`, `width`, `height`, `px_per_yard`, and the `north`/`south`/`west`/`east` box. Draw the picture at `(-west, -north)` in game yards. The worldmap source cuts the area picture's 4×3 tiles to 1002×668 (multi-floor maps get a `-Nf` suffix); the minimap source reads the WDT/ADT tiles at 2 px per yard, bottom-left origin; the navmesh source height-shades the server mmaps (no cgo needed for reading).

The JSON `from` says what a minimap picture is made of: `terrain`, `wmo`, or `terrain+wmo`. Five maps draw WMO tiles over terrain (33, 36, 29, 329, 800); maps 189/819/822 carry notes about their client-data gaps. Caps keep one map from hanging the run: longest side 8192 px, 4096 decoded tiles, 4096 placements, 64 ADTs, 16384 drawables, 33.5 M sampled pixels (largest real map Dire Maul samples 16.6 M); a bigger client file gets a reason in `<id>.status.json`, not a hang. Reasons cap at 160 chars; a stale status file is removed when every source exists.

The dashboard serves three slots in Zul'Farrak order (`GET /api/minimap/<id>`): worldmap (main, then one per floor), minimap, navmesh. Each slot answers `kind`, `floor`, `label`, `available`, `reason`, `url`, `x`, `y`, `w`, `h`, `name`, `source`; a missing source stays listed as unavailable with its one-line reason. A 200 with three missing slots means the folder exists but the map has no pictures; 404 means no folder. The status file is folder-local only (64 KiB max, no symlinks, malformed/oversized/unknown kinds ignored, control chars become spaces), never served. The Uldaman pin in the JSON box is the worked check that the math above is right.
