---
type: Guide
title: Compare a route with the navmesh
description: Run route-oracle in walk and bosses modes and read the per-leg reports.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/route_oracle_cmd.go
tags: [plans, oracle]
sources:
  - id: route-oracle
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/route_oracle_cmd.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:52:00+01:00
status: draft
---

# Compare a route with the navmesh

`route-oracle` compares each plan's stored route with what the navmesh plans for its objective chain, leg by leg, and writes a human table plus a machine report[^route-oracle].

## 1. Run walk mode

```sh
turtle-agent route-oracle --plans <dir> --mmaps <mmaps-dir> --out <dir> --plan <plan>
```

Walk mode (the default) plans each plan's own stored sequence hop by hop and scores the stored hops against the navmesh, twice: plain, and on a scratch planner with the plan's avoid boxes. The report's metrics use the avoid-aware path — the one a run plans with. `--plan` is repeatable; the default set is `wailing_caverns`, `ragefire_chasm`, `uldaman`, `zulfarrak`, and `deadmines`.

## 2. Run bosses mode

```sh
turtle-agent route-oracle --plans <dir> --mmaps <mmaps-dir> --out <dir> --mode bosses --plan <plan>
```

Bosses mode plans entrance to each leg's objective live, with no hand points and no break hops, to see what a boss list alone could do. Add `--objectives bosses` for the chain entrance to boss 1 to boss 2 (`<plan>-bosses-only` files) instead of each leg's own objective (its boss, else its `to` point).

## 3. Read the per-leg rows

`<out>/<plan>.txt` is the human table; `<out>/<plan>.json` the machine report[^route-oracle]. Per leg, in the order the driver does them: the `via` waypoints, pull group positions, boss, and step stands, each scored against the navmesh. A leg with no stored objective in bosses mode reports `no_objective`.

## 4. Act on the gap

Where the stored route is much longer than the live plan, the hand waypoints are doing real work (or are stale detours): walk that stretch on the mapping walk and either keep the points or drop them. Where the live plan finds no path at all, the plan's hops need cutting or rerouting before any run trusts them. Exit 12 means the oracle could not report every plan — a failing plan is not a usage error.
