---
type: Guide
title: Control a run from the dashboard
description: Pause, resume, skip or stop a run with control tokens, and read the control log.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dashboard_cmd.go
tags: [dashboard, controls]
sources:
  - id: dashboard-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dashboard_cmd.go
  - id: dashboard-control
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/dashboard/control.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:39:00+01:00
status: draft
---

# Control a run from the dashboard

With `--control` the page can pause, resume, skip or stop a run. Every control is logged with who sent it and what the agent answered.

## 1. Make a control token file of its own

Create a second file of `label token` lines, mode 0600. It must not be the route editor's file, and it must share no token with it: an editing token must never press run buttons. The dashboard refuses to start otherwise.

## 2. Start the dashboard with controls on

```sh
turtle-agent dashboard --control --control-tokens ~/.config/turtle-agent/dashboard-control-tokens --control-log ~/.local/state/turtle-agent/dashboard-control.jsonl
```

Every flag and its default:

| Flag | Default | What it does |
| --- | --- | --- |
| `--control` | off | turns run controls on |
| `--control-tokens` | `~/.config/turtle-agent/dashboard-control-tokens` | `label token` lines, mode 0600; the label is the who in the control log |
| `--control-trust-local` | off | controls from this machine need no token typed |
| `--control-trust-label` | `owner` | the token label trusted controls are made as |
| `--control-log` | `~/.local/state/turtle-agent/dashboard-control.jsonl` | every control appends a request and a result line; the folder is made 0700, the file 0600 |

`--control` refuses a LAN listen host: listen on loopback or a literal Tailscale address, or a control token would cross a LAN in plain HTTP.

## 3. Send a control

The page sends `POST /api/control/<agent-id>/run` with `{"verb": "pause" | "resume" | "skip" | "stop", "confirm": true|false}`. `pause` and `stop` need `"confirm": true`. The verbs reach the agent as `dungeon_pause`, `dungeon_resume`, `dungeon_skip`, `dungeon_stop`. One control runs per agent at a time; each token label may send 30 a minute. The same works with curl:

```sh
curl -X POST http://127.0.0.1:8790/api/control/<slot>/run \
  -H 'Authorization: Bearer <token>' -H 'Content-Type: application/json' \
  -d '{"verb":"pause","confirm":true}'
```

## 4. Read the control log

Each control appends two lines: the request (who, via, agent, verb) and the result (`ok`, `error`, or `indeterminate` when the RPC answer could not be read). The page's `GET /api/control/<id>/log?n=20` shows the last result lines, newest last. An `indeterminate` answer means the command may already have run: read the run state before repeating it.
