---
type: Reference
title: Navigation commands
description: Every flag of map, path-check, minimap-extract and patrol-record.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/map_cmd.go
tags: [navigation, reference, cli]
sources:
  - id: map-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/map_cmd.go
  - id: path-check-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/path_check_cmd.go
  - id: minimap-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/minimap_cmd.go
  - id: patrol-record-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/patrol_record_cmd.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:45:00+01:00
status: draft
---

# Navigation commands

Every flag of the four navigation commands. All flags have the defaults shown; a flag marked required has no working default. Exit 0 draws, records or replays; exit 2 is a usage error; other exits are listed per command. `patrol-record` accepts flags before or after the slot dir; `map`, `path-check` and `minimap-extract` take no positional arguments besides `path-check`'s coordinates after `--`. On/off flags take `=` when you give a value (for example `--background=false`); a space-separated value after an on/off flag is not read as its value.

## map

`usage: turtle-agent map [--capture FILE] [--events FILE] [--report FILE] [--botwatch FILE [--members a,b]] [--plan dungeons/x.json] [--out run.svg] [--html] [--force] [--config FILE | --mmaps DIR] [--map N] [--box x0,y0,x1,y1] [--from RFC3339] [--to RFC3339] [--pad YD] [--floors auto|off|z1,z2] [--background=false] [--width PX]`

Needs `--capture` and/or `--botwatch`; no positional arguments.

| Flag | Default | What it does | Example |
|---|---|---|---|
| `--capture` | `""` | A capture (`login --capture`): party positions, hostile creatures, events. | `--capture testdata/captures/m6-fight.twcap` |
| `--events` | `""` | Saved events (the `events` method, or capture events) used instead of the capture's own. | `--events /tmp/ev.json` |
| `--report` | `""` | A `dungeon_status` report (JSON): teleports, revives, deaths, wipes. | `--report /tmp/report.json` |
| `--botwatch` | `""` | Botwatch samples (JSON lines of time plus bot positions) as extra routes. | `--botwatch /tmp/bw.jsonl` |
| `--members` | `""` | With `--botwatch`, only these bots, comma separated. | `--members agentone,agenttwo` |
| `--plan` | `""` | A dungeon plan (`dungeons/x.json`) drawn over the run. | `--plan dungeons/uldaman.json` |
| `--out` | `map.svg` | The SVG to write (`.svg` appended when missing); several floors write `NAME-f1.svg`, `NAME-f2.svg`, … | `--out /tmp/fight.svg` |
| `--html` | `false` | Also write the single-file HTML replay next to the SVG with `.html`. | `--html` |
| `--force` | `false` | Let `--out` replace an existing file (never a capture). | `--force` |
| `--config` | `~/.config/turtle-agent/settings.json` | Settings file; its `mmaps_dir` gives the navmesh background. | `--config <slot-dir>/settings.json` |
| `--mmaps` | `""` | Folder with the server's mmaps (overrides `mmaps_dir`); empty with no `mmaps_dir` draws no background. | `--mmaps <walk-dir>` |
| `--map` | `-1` | The map to draw (default: the one with most positions). | `--map 1` |
| `--box` | `""` | Draw this area only: `x0,y0,x1,y1` in game coordinates, corners in any order. | `--box -1314,-2409,-1251,-2349` |
| `--from` | `""` | Only what happened at or after this time (RFC 3339). | `--from 2026-11-01T20:00:00Z` |
| `--to` | `""` | Only what happened at or before this time (RFC 3339). | `--to 2026-11-01T21:00:00Z` |
| `--pad` | `25` | Yards of margin round what the run covers. | `--pad 40` |
| `--floors` | `auto` | How to split floors: `auto` (from the navmesh), `off`, or heights such as `"-30,-10"`. | `--floors off` |
| `--background` | `true` | Draw the navmesh floor (`false`: leave it out, smaller and quicker). | `--background=false` |
| `--width` | `960` | The longest side of the map in pixels. | `--width 1400` |

## path-check

`usage: turtle-agent path-check --mmaps DIR --map N [--ray] [--json] -- x y z x y z`

Both `--mmaps` and `--map` are required; exactly 6 coordinates follow `--`.

| Flag | Default | What it does | Example |
|---|---|---|---|
| `--mmaps` | `""` (required) | Folder with the server's mmaps (`<map>.mmap` and `<map>*.mmtile`). | `--mmaps <walk-dir>` |
| `--map` | `""` (required) | Map id, numeric; negative refused. | `--map 1` |
| `--ray` | `false` | Also cast the walkability ray between the two points (`navmesh.Planner.Raycast`, the agent's line-of-sight stand-in): a third line (clear, or blocked after N of M yd; unknown when an end is off the mesh or a tile is missing) and `"ray":{known,clear,hit}` in `--json`. | `--ray` |
| `--json` | `false` | Print the results as JSON instead of text. | `--json` |

Exit 1 when a query fails (`path-check: ...` on stderr).

## minimap-extract

`usage: turtle-agent minimap-extract --client <Data dir> --out <dir> [--maps 70,209|all-instances] [--mmaps DIR] [--list] [--verbose]`

`--client` is required; `--out` is required unless `--list`.

| Flag | Default | What it does | Example |
|---|---|---|---|
| `--client` | `""` (required) | The client's `Data` folder (the MPQ archives; read only). | `--client <client-data>` |
| `--out` | `""` (required) | Folder to write `<map>.png` and `<map>.json` into. | `--out ~/turtle-agent-minimaps` |
| `--maps` | `all-instances` | Map ids, comma separated, or `all-instances` (every dungeon and raid of `Map.dbc`). | `--maps 70,209` |
| `--mmaps` | `""` | Server mmaps folder(s), comma separated: a map with no minimap tiles gets a height-shaded floor render of its navmesh. | `--mmaps <walk-dir>` |
| `--prefer` | `""` (empty means `worldmap,minimap,navmesh`) | Order of the image sources for `<map>.png`, comma separated: `minimap`, `worldmap`, `navmesh`. | `--prefer minimap,worldmap,navmesh` |
| `--list` | `false` | Only report what each map would get; write nothing. | `--list` |
| `--verbose` | `false` | Log every map and every skipped tile to stderr. | `--verbose` |

Exit 1 when extraction fails (`minimap-extract: ...` on stderr) or any map errors.

## patrol-record

`usage: turtle-agent patrol-record <slot-dir> --map M --area x,y,r [--z Z] --minutes N [--sample-ms 1000] [--out FILE] [--spawns DIR] [--mmaps DIR] [--entries e1,e2]`

One positional argument (the slot dir); `--map`, `--area` and `--minutes` are required.

| Flag | Default | What it does | Example |
|---|---|---|---|
| `--map` | `""` (required) | Map id, numeric. | `--map 36` |
| `--area` | `""` (required) | The circle to watch, `x,y,r`; radius 10–200 yd. | `--area -800,150,60` |
| `--z` | `""` | The floor's z; required without `--mmaps` (several floors may sit at one `x,y`). | `--z 12.5` |
| `--minutes` | `0` (required) | Recording length in minutes (1–60). | `--minutes 20` |
| `--sample-ms` | `1000` | Milliseconds between samples (500–5000). | `--sample-ms 2000` |
| `--out` | `./patrols-<map>-<stamp>.json` | Output file; refused if it exists. | `--out /tmp/patrols-36.json` |
| `--spawns` | `""` | World export folder with `creatures.tsv`, to name the patrols' spawns. | `--spawns tools/dungeongen/testdata/map36` |
| `--mmaps` | `""` | Navmesh folder, to find z when `--z` is absent. | `--mmaps <walk-dir>` |
| `--entries` | `""` | Comma-separated creature entries to record (default: every creature). | `--entries 123,456` |

Exit 22 when the recording stops early for safety (combat, death, lost world); the file keeps what was recorded with `stopped` set.
