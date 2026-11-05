# Navigation and world data

Move on the navmesh, draw the run as a map, and know what wakes when the party walks.

## How it fits together

- [How navigation works](overview.md) — the navmesh, the planner, the walk, the map, the spawns: one tour.
- [Draw your first run map](first-map.md) — from a capture file to an SVG, start to finish.

## Move

- [Walk the navmesh](walk-navmesh.md) — `path_to` and `walk_path`, arrivals, interruptions, replans.
- [Replay one hop with path-check](path-check.md) — rerun a refused walk offline with both queries.
- [Check a route before the run](checks.md) — `plan-check`, `stage-check`, `trash-check` and `route-oracle`.
- [Record patrols](patrols.md) — `patrol-record` and the `tb-patrols` file.

## Look

- [Draw a run map](draw-map.md) — the `map` command: inputs, floors, overlays, outputs.
- [Watch the run live with map_snapshot](map-snapshot.md) — sample, snapshot, and read the live map.
- [Put a real map under the run](minimaps.md) — `minimap-extract` and `--minimap-dir`.

## Understand

- [The navmesh and the planner](navmesh.md) — tiles, statuses, water, avoid boxes, floors, rays.
- [How the server walks](serverpath.md) — the server's own path and the move verdict.
- [What wakes when you walk](aggro.md) — aggro radius, packs, rings, estimates and their limits.
- [World exports and spawns](world.md) — the export files, packs, patrols and bosses.
- [Client data: MPQ and BLP](client-data.md) — the archives the minimaps come from.

## Look up

- [Navigation commands](commands.md) — every flag of `map`, `path-check`, `minimap-extract`, `patrol-record`.
- [Check commands](check-commands.md) — every flag of `trash-check`, `plan-check`, `stage-check`, `route-oracle`, `plan-diff`.
- [Navigation RPCs](rpc.md) — `path_to`, `walk_path`, `map_snapshot`, `plan_check` and the walk family.
- [When navigation fails](troubleshooting.md) — real error messages, what they mean, the fix.
