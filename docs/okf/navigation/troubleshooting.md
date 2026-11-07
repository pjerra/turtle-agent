---
type: Troubleshooting
title: When navigation fails
description: Real navigation error messages, what they mean, and the fix.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/navmesh/planner.go
tags: [navigation, troubleshooting, errors]
sources:
  - id: planner
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/navmesh/planner.go
  - id: navwalk
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/session/navwalk.go
  - id: map-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/map_cmd.go
  - id: trash-check-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/trash_check_cmd.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:45:00+01:00
status: draft
---

# When navigation fails

Each row is the exact message, what it means, and the fix. Messages from commands print on stderr; RPC failures come back as the call's error.

## No navmesh

| Message | Meaning | Fix |
|---|---|---|
| ``path_to and walk_path need the server's mmaps: set "mmaps_dir" in the settings file`` | The agent has no navmesh: `mmaps_dir` is empty. | Set `mmaps_dir` to the server's mmaps folder (slot setup takes `--mmaps-dir`). |
| ``navmesh: no tiles folder; set "mmaps_dir" in the settings file`` | An offline command or query found no tiles folder. | Pass `--mmaps <walk-dir>` or set `mmaps_dir`. |
| `"mmaps_dir" <dir> is not a folder (copy the server's data/mmaps files there)` | The settings point at a missing folder. | Copy the server's `data/mmaps` files (`NNN.mmap`, `NNNyyxx.mmtile`) there. |
| `navmesh: map 70: ... (copy the server's data/mmaps/<name> and 070*.mmtile into mmaps_dir)` | The map's files are missing or broken. | Copy that map's `.mmap` and `.mmtile` files in. |
| `navmesh: no 070*.mmtile files in <dir>` | No tiles for the map at all. | Copy that map's tiles in. |
| `navmesh: this turtle-agent was built without cgo, so it has no Detour engine; rebuild with CGO_ENABLED=1 and a C++ compiler` | The binary has the stub engine: no query can work. | Rebuild with cgo and a C++ compiler. |
| `no navmesh planner (set "mmaps_dir" in the settings file): the follow mode walks with walk_path` | `follow_start` needs the mesh. | Set `mmaps_dir` first. |

## Walks and hops

| Message | Meaning | Fix |
|---|---|---|
| `the server moved the player 9 times; gave up` | The walk replanned 8 times around server moves, then stopped `partial`. | Check the log for fear, knockbacks or teleports; clear the cause and walk again. |
| `still rooted or moved by the server after 15s` | A root outlasted the 15 s wait. | Wait out or break the root, then walk again. |
| `server: NOPATH ... start poly 12.3 yd, end poly 0.2 yd` | The server has no path, and the start sits 12 yd off the mesh. | Move the start onto walkable ground (use `path-check`'s poly distances). |
| `offline: partial ...` / `reason` naming the nearest reachable point | The mesh goes only part of the way (water, an edge, a wall). | Aim at the reachable side, or fix the plan hop. |
| `map: no position on map 1: nothing to draw (the capture holds maps [...])` | The capture has no position on the drawn map. | Pass `--map` with one the capture holds. |

## Inputs and files

| Message | Meaning | Fix |
|---|---|---|
| `map: give --capture and/or --botwatch: positions come from one of them` | `map` got no positions. | Pass `--capture` and/or `--botwatch`. |
| `--box %q has no area` / `--box needs x0,y0,x1,y1, got %q` | The `--box` area is malformed or flat. | Pass four numbers with a nonzero area. |
| `--floors: %q is not a height` / `floors: heights must ascend (...)` | The `--floors` heights do not parse or ascend. | Pass ascending numbers like `"-30,-10"`. |
| `map: --out %s exists (--force replaces it)` | The output exists. | Pass `--force` or another `--out`. |
| `--plan names a plan of --sql` / `name the plans or pass --sql, not both` | Plan and `--sql` inputs mixed. | Name plans as arguments, or pass one `--sql` with `--plan`. |
| `%s holds no plan %q (...)` / `%s holds %d plans: pick one with --plan (...)` | The `--sql` file lacks the plan or holds several. | Pass `--plan` with one it holds. |
| `no world export in %s: pass --spawns DIR (tools/dungeongen/export.sh writes it)` | No `creatures.tsv` where expected. | Pass `--spawns` with the map's export folder. |
| `%s: no creatures.tsv (tools/dungeongen/export.sh writes the export)` | The spawns folder is not an export. | Export the map first. |
| `minimap-extract: mpq: none of the archives exist in <dir>` | The `--client` folder holds no MPQ archives. | Point `--client` at the client's `Data` folder. |
| `--maps: %q is not a map id` / `--maps names no map` | The `--maps` list does not parse or is empty. | Pass ids like `--maps 70,209` or `all-instances`. |
| `--prefer: %q is not minimap, worldmap or navmesh` | Bad `--prefer` source. | Use a subset of `minimap,worldmap,navmesh`. |
| `patrol-record: --z is required without --mmaps (several floors may sit at one x,y)` | No floor disambiguation. | Pass `--z` or `--mmaps`. |
| `patrol-record: %s exists; refusing to overwrite a recording` | The `--out` file exists. | Pick another `--out`. |
| `dungeon plan: ...` / ``plan must be a plan file name such as "uldaman" (a-z, 0-9, _ and -)`` | Bad plan name in an RPC or flag. | Use a plan file name (`a-z, 0-9, _` and `-`). |
| `nothing to draw yet: the recorder has no position on the map (is the character in the world?)` | `map_snapshot` with nobody in the world. | Log the character in first. |
| `floor: this map has floors 0 to N` | Bad `floor` in `map_snapshot`. | Use a floor in range. |

## Check findings are not errors

`trash-check` lines (`DANGER`, `PATROL`), `plan-check` issues and lint warnings describe the plan, not a broken tool. A `plan-check` exit 11, `trash-check` exit 21 with `--fail-on`, `stage-check` exit 13 and `route-oracle` exit 12 mean the check found something, not that the command failed. Read them in [Check a route before the run](checks.md).
