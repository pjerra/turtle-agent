---
type: Guide
title: Replay one hop with path-check
description: Rerun one walk step offline against the navmesh with both path queries.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/path_check_cmd.go
tags: [navigation, navmesh, debugging]
sources:
  - id: path-check-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/path_check_cmd.go
    title: The path-check command
  - id: serverpath
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/navmesh/serverpath.go
    title: The server's own path query
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:33:00+01:00
status: draft
---

# Replay one hop with path-check

You rerun one step of a walk offline: the same offline plan the agent uses, plus the server's own path query, side by side. Use it when a live walk refused a hop and you want to know why.

## 1. Take the hop's endpoints from the log

Find the refused hop in the agent log: the lead position it planned from and the goal it planned to. Both are `x y z` triples on one map.

## 2. Run both queries

Pass `--mmaps` (the server's mmaps folder), `--map` (the map id) and the six coordinates after `--`:

```sh
turtle-agent path-check --mmaps <walk-dir> --map 1 -- -1287 -2379.6 91.7 -1290 -2382 91.5
```

The `--` keeps negative coordinates from parsing as flags. The command prints the offline result and the server result:

```text
offline: complete 7.3 yd
server: type 0x1 ok end -1290.0 -2382.0 91.5 0.0 yd short, start poly 0.1 yd, end poly 0.2 yd
```

`type 0x1` is a normal server path; `NOPATH` means the server has no route either. `start poly` and `end poly` are how far each endpoint sits from the mesh: large values mean the point itself is off the walkable ground, not that the route is blocked.

## 3. Get machine-readable output

Pass `--json` for one JSON line with the offline status, length and reason plus the server type, verdict, actual end and distances:

```sh
turtle-agent path-check --mmaps <walk-dir> --map 1 --json -- -1287 -2379.6 91.7 -1290 -2382 91.5
```

Exit 0 means the hop replayed; exit 1 means a query failed (`path-check: ...` on stderr); exit 2 is a usage error (wrong coordinates, missing `--mmaps` or `--map`, negative map id).
