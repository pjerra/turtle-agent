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

`--out` writes `probe-<plan>.json` and `probe-<plan>.txt`; `--json` prints the report as JSON. `--from-step N` only reports the segments that end at run-leader step `N` or later, for iterating on one stretch.

## 4. Fix bad segments

A `bad` segment is a hop the run leader should not walk as it stands (exit 20 when at least one exists). Cut it with `via` points along the walkable path, reroute it around the obstacle, or — for a gap the mesh cannot link — make it a `direct` step with a `check_skip` reason. Re-probe until no bad segment is left.
