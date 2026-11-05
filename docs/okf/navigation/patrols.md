---
type: Guide
title: Record patrols
description: Record what walks a circle for an hour, and feed it to the checks.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/patrol_record_cmd.go
tags: [navigation, patrols]
sources:
  - id: patrol-record-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/patrol_record_cmd.go
    title: The patrol-record command
  - id: patrols-format
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/patrols/format.go
    title: The tb-patrols file format
  - id: patrols-analyze
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/patrols/analyze.go
    title: Turning samples into patrols
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:34:00+01:00
status: draft
---

# Record patrols

You park a GM character near a route, record every creature that moves through a circle for up to an hour, and get a `tb-patrols` file: loops with their period and path, static units and wanderers. `trash-check` reads the file to warn about patrols at the route and the stands.

The slot agent must be logged in, a GM, and out of any group: the recording goes into the GM's own instance.

## 1. Pick the circle and the floor

Name the map, the circle `x,y,r` and the recording length. Radius is 10 to 200 yd, length 1 to 60 minutes, samples every 500 to 5000 ms (default 1000):

```sh
turtle-agent patrol-record <slot-dir> --map 36 --area -800,150,60 --minutes 20 --mmaps <walk-dir>
```

The floor question: several navmesh floors can sit at one `x,y`. Without `--mmaps` you must pass `--z` with the floor's height; with `--mmaps` the command reads the floors at `x,y` itself and asks for `--z` only when there is more than one. Narrow multi-floor areas need `--z`.

## 2. Name the spawns (optional)

Pass `--spawns` with the map's world export to name each patrol's spawns, and `--entries` to record only some creature entries. The output defaults to `./patrols-<map>-<stamp>.json` and is refused if it exists:

```sh
turtle-agent patrol-record <slot-dir> --map 36 --area -800,150,60 --minutes 20 --mmaps <walk-dir> --spawns tools/dungeongen/testdata/map36 --out /tmp/patrols-36.json
```

The command only sends stance GM commands (`.gm on`, `.gm visible`, `.go xyz`); it samples units within the radius plus 60 yd, tracks each unit once it enters the circle, and stops early for combat, death or a lost world. A safety stop exits 22 and writes the file with `stopped` set.

## 3. Read the file

A `tb-patrols` file (format `tb-patrols`, version 1) holds the map, the area, the minutes and sample rate, and three lists: `patrols` (each with its path, waits, period in seconds, cycles seen and whether the period is an estimate), `static` (units that moved under 3 yd) and `wanderers` (movers under 15 yd with no loop). A loop needs a return within 3 yd after at least 10 s plus 5 confirming samples; the period is estimated until two full cycles are seen.

Feed it to the checks:

```sh
turtle-agent trash-check deadmines --mmaps <walk-dir> --patrols /tmp/patrols-36.json
```

A recording replaces the world-export patrol it matches and is otherwise added.
