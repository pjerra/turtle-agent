---
type: Concept
title: What wakes when you walk
description: Aggro radius, packs, the call-for-help ring, and what the estimate cannot see.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/aggroest/aggroest.go
tags: [navigation, aggro, patrols]
sources:
  - id: aggroest
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/aggroest/aggroest.go
    title: The offline aggro estimate
  - id: worldspawn
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/worldspawn/worldspawn.go
    title: Spawn aggro radius and packs
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:40:00+01:00
status: draft
---

# What wakes when you walk

Walking past a creature can wake it. The estimate counts what would join before a pull: the target's pack, everything in aggro reach of the walk, the call-for-help ring, and the drag sweep. It mirrors the run leader's own estimator, so the offline check and the live run agree.

## Aggro radius

Each spawn gets one aggro radius: its detection range minus the party-minus-creature level difference (floored at -25 levels), never below the smaller of detection and 5 yd, times the aggro rate. A creature with `NO_AGGRO` or no detection range never wakes: 0. Three bosses override the formula: Moam wakes at 60 yd, Ossirian at 45, Ayamiss at 40. The estimate uses the party's lowest level, the widest radius.

## Packs and rings

A pack is the set that wakes as one: spawns sharing an `AGGRO_TOGETHER` creature group, plus both directions of every aggro creature link. Only hostile spawns join (faction 35 is friendly; vendors and quest givers are not trash).

Proximity wakes every mob without `NO_AGGRO` inside its reach of the party's walk (2-yd samples, 3 yd of spread), the tank-only walk (1.5 yd), the stand (3 yd, 10 for a raid) and the fight point, with sight from the mob to the point. Then the ring carries: every member calls every creature of the same faction within 20 yd of its own position (sight caller to candidate, within 12 yd vertically), each call adding the called mob's pack, up to 2 hops. The drag sweep pulls the same-faction creatures within 20 yd along the way back.

## Limits

The estimate reads spawn positions, not live positions: a patroller counts where it stands now, and an unknown patrol phase over-counts. Without `--mmaps` there is no sight check, so through-wall aggro is over-counted and the report says so. Other floors are cut by a 15-yd 3D filter and reported, not modelled. `trash-check` adds the verdicts on top: what the plan targets itself (`clear`), what walks into the route (`danger`), and what only a patrol brings (`patrol`).
