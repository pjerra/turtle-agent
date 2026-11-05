---
type: Concept
title: World exports and spawns
description: The files that say what lives on a map, and how packs and patrols are read from them.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/worldspawn/worldspawn.go
tags: [navigation, spawns]
sources:
  - id: worldspawn
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/worldspawn/worldspawn.go
    title: Reading the world export
  - id: packs
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/worldspawn/packs.go
    title: Packing spawns into groups
  - id: spawn-patrol
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/worldspawn/patrol.go
    title: Patrol loops from the export
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:40:00+01:00
status: draft
---

# World exports and spawns

A world export is a read-only snapshot of one map's spawns, written by `tools/dungeongen/export.sh` over a read-only connection. The checks read it; they never touch a server.

## The files

One folder per map (for example `tools/dungeongen/testdata/map36`). The full export holds `creatures.tsv` (one row per spawn: guid, entry, name, levels, faction, position, movement, detection range, flags, speed, group, pool), `groups.tsv` and `links.tsv` plus `link_templates.tsv` (what moves and wakes together), `movement.tsv` and `movement_template.tsv` (waypoint loops), and `portals.tsv`, `entrances.tsv`, `gameobjects.tsv` (doors and entries). Older exports may lack the raid columns and the movement files.

## Packs

`Packs` groups hostile spawns with a union-find: spawns sharing an `AGGRO_TOGETHER` group join, and every aggro link joins slave to master in both directions. Each pack records its member guids (lowest first), its centroid and radius, and whether it patrols (a member with movement type 2, or a formation follower of one) or is pooled (any member with a pool entry, so it may not be spawned at all).

## Patrols

The export's waypoint loops become patrols: the loop's path and waits, the period (loop length at 2.5 yd/s plus waits), and the source (`creature_movement <guid>` or `creature_movement_template <entry>`). Formation followers attach to their leader's loop. The DB gives positions, not phases: the checks know where a patrol walks, not where it is right now, so [Record patrols](/navigation/patrols.md) exists to measure real loops and feed them in.
