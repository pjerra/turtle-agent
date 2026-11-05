---
type: Troubleshooting
title: Observability troubleshooting
description: Real error messages from the observability commands, what they mean, and the fix.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dashboard_cmd.go
tags: [dashboard, traces, troubleshooting]
sources:
  - id: dashboard-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dashboard_cmd.go
  - id: dashreplay-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dashreplay_cmd.go
  - id: capturecmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/capturecmd.go
  - id: scenario-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/scenario_cmd.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:54:00+01:00
status: draft
---

# Observability troubleshooting

## `dashboard: --control refuses the listen host ...`

You started `--control` on a LAN address. A control token must not cross a LAN in plain HTTP. Listen on loopback or a literal Tailscale address.

## `dashboard: --control-trust-local needs --control`

`--control-trust-local` without `--control` is a usage error. Add `--control`, or drop the trust flag.

## `dashboard: --edit-trust-local needs --edit`

Same for the editor: `--edit-trust-local` without `--edit` is a usage error.

## `--control-tokens ... is the route editor's --edit-tokens file`

The control token file must be its own file, never the route editor's — an editing token must not press run buttons. The same refusal fires when the two files merely share one token. Make a second file.

## `dashboard: --tests-dir ... is not a folder: no files will be linked`

The tests dir is missing. The dashboard still serves; `/files/...` links nothing. Create it or pass the right `--tests-dir`.

## `refusing to replay to <host:port>: not a loopback address; pass --i-know if this daemon is a test dashboard`

`dashboard-replay` refuses non-loopback targets with exit 17. Replay only into a test daemon: pass `--i-know` to vouch for it. A replay's session is newer than a running module's, so it overwrites what a live daemon shows until the module restarts.

## `dashboard-replay: --speed must be > 0`

`--speed` (and `--max-gap`) must be above 0.

## `capture: ... already exists (captures are never overwritten)`

`login --capture` refuses an existing file. Remove it or pick a new path. `login --reconnect cannot be combined with --capture` likewise: drop one flag.

## `scrub-check: the settings file has no accounts, so there is nothing to check against`

The `--config` file holds no accounts. Point `--config` at the settings file whose accounts the capture was recorded with.

## `capture events: warning: ...; decoding the N records before it`

The capture ends in a cut-off record (usually an agent that was killed). The rows before it still decode; this is expected, not a failure.

## `--set "x": want check=number`

A `--set` value (trace-score, fight-score) must be `check=number`. An unknown check prints `--set: no threshold for check "x" (have ...)`.

## `trace: ... is schema version N; this reader knows M`

The trace was written by a newer agent. Update the binary and re-score; the format refuses a higher `v`.

## `scenario: --session NAME: the scenario has no session NAME`

`--session` overrides an existing session only. Fix the name, or add the session to the file.

## `scenario: ... exists and is not empty (pass --force to write into it)`

`scenario run --out` refuses a non-empty directory. Pass `--force` or pick an empty one.

## `decisions: missing --dash-url --dash-pass-file --commander --out`

These four `decisions pull` flags are required (`--dash-user` defaults to `TADASH`, `--max-mib` to 1024). `decisions: budget stopped the pull at N MiB` means the budget, not a failure: raise `--max-mib` or narrow `--since`/`--until`.

## `no readable trace for this agent's run`

The dashboard's live view has no trace to tail for that agent: tracing is off (`"trace": false`) or the traces dir is wrong. Check `traces_dir` and the run's `trace.error`.
