---
type: Guide
title: Probe the run leader's rows
description: Score each run leader hop on the navmesh with plan-probe and fix bad segments.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/plan_probe_cmd.go
tags: [plans, probe]
sources:
  - id: plan-probe
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/plan_probe_cmd.go
  - id: planprobe
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/planprobe/probe.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:52:00+01:00
status: draft
---

# Probe the run leader's rows

`plan-probe` measures the run leader's rows of one plan on the navmesh and prints where its hops are bad or worth a look[^plan-probe]. It reads plan files, a migration, and the mmaps folder, and nothing else.

## 1. Probe the plan file

```sh
turtle-agent plan-probe --mmaps <mmaps-dir> <plan>
```

Put flags before the plan name here too (plain flag parsing). On/off flags (`--json`) take `=` when you give a value.

`--mmaps` is required. `--dir` (default `dungeons`) changes the plan folder.

## 2. Probe the deployed rows instead

```sh
turtle-agent plan-probe --mmaps <mmaps-dir> --sql <migration.sql> [--plan <name>]
```

The migration holds the exact rows the server runs, so prefer it when one exists. `--plan` picks the plan when the file holds several. You can also name a `<path.json>` row file directly.

## 3. Save the report

```sh
turtle-agent plan-probe --mmaps <mmaps-dir> --out <dir> <plan>
```

## Read the segment

Each segment reports the straight line, the server query (type, `CheckMove`, length, end distance, polygon distances), the offline status and length (a disagreement is noted), the server-over-straight ratio, the floors over the end (`floor_dz`, snap), water and lava yards, climb and drop, steepness, the floor-index box, hops past 60 yd, and a verdict with reasons. Distances are 3D yards at two decimals.

## 4. Fix bad segments

A `bad` segment is `NOPATH` (or unreachable, or off-mesh) or ends more than 5 yd off the floor (`no floor within 5 yd (row z ..., floor ...)`). Pull and boss rows are exempt: in range but off the pack still works, and off-mesh within radius plus 5 yd works (0 means 15 yd for pulls, 30 for bosses). Warn when the ratio tops 1.6, on any water or lava, on steep ground or a drop of 6 yd or more, on a snap past 2 yd, on differing floors, on 2 or more floors (`multi-floor x,y: row z picks floor z=...`), on an offline/server disagreement, or on a hop past 60 yd. On a multi-floor map the 2D overlays mislead; the z decides. Cut it with `via` points along the walkable path, reroute it around the obstacle, or — for a gap the mesh cannot link — make it a `direct` step with a `check_skip` reason. Re-probe until no bad segment is left.
