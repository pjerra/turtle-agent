---
type: Concept
title: Decision captures
description: What the TortoiseBots observability daemon records and how pulls carry it home.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/tbdash/client.go
tags: [decisions, dashboard]
sources:
  - id: tbdash-client
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/tbdash/client.go
  - id: decisions-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/decisions_cmd.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:46:00+01:00
status: draft
---

# Decision captures

The TortoiseBots observability daemon — a separate program from the turtle-agent dashboard — records what its commanders decided: one decision capture per commander per window. `internal/tbdash` is the client that talks to that daemon's JSON API; the password never leaves its file except into the login request, and never reaches an error text.

A pull fetches, per capture, `index.json` plus the `seg-NNNN.jsonl` segment files into `<out>/decisions`, with a manifest beside them. The pull stops at the budget (default 1024 MiB) and says so: `decisions: budget stopped the pull at 1024 MiB`.

Two commands pull:

- `turtle-agent decisions pull --dash-url <server> --dash-user TADASH --dash-pass-file <walk-dir>/pass --commander <slot> --out <walk-dir>` fetches one commander's window (`--since`/`--until` narrow it, both RFC 3339).
- `turtle-agent raid-walk --dash-url <server> --dash-pass-file <walk-dir>/pass [--dash-user TADASH] [--decisions on|off] [--decisions-max-mib N]` pulls the same files into the walk folder automatically (`TBDASH_URL`, `TBDASH_USER` and `TBDASH_PASS_FILE` fill the flags). Without `--dash-url` and `--dash-pass-file` it reports `off (no --dash-url/--dash-pass-file)`.

Point replays only at a test daemon: a recording of this data is what `dashboard-replay` plays.
