---
type: Overview
title: Observability and testing
description: What the dashboard, captures, traces, events and scenarios are and how they fit together.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/dashboard/server.go
tags: [dashboard, traces, captures, events, scenarios]
sources:
  - id: dashboard-server
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/dashboard/server.go
  - id: capture
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/capture/capture.go
  - id: trace
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/trace/trace.go
  - id: events
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/events/log.go
  - id: scenario
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/scenario/runner.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:35:00+01:00
status: draft
---

# Observability and testing

You watch a run on the **dashboard**, you record what happened in **captures** and **traces**, you read the **event log** while it runs, and you repeat a live test with a **scenario**.

The pieces in run order:

- The **dashboard** (`turtle-agent dashboard`) is one web page that polls every running agent's loopback RPC and draws where each party is on the dungeon map. Read-only by default; `--edit` and `--control` each opt into one kind of write with its own token file.
- A **capture** (`login --capture`) records the decrypted world packets of one session, so a bug can be replayed offline. Secrets never land in the file: account names, passwords and keys are redacted, and `scrub-check` proves it before a fixture is committed.
- A **trace** is one JSONL file per dungeon run or brain session (schema `bt-trace` v1) plus a small summary. The agent writes it while the run goes; `trace-score` turns it into pass/fail verdicts afterwards.
- The **event log** is the agent's in-memory ring of everything it saw, paged over the `events` RPC. `capture events` replays a capture through the same handlers, so offline analysis sees the same rows.
- A **scenario** drives one or more running agents over RPC through scripted steps and checks, printing PASS/FAIL, so a live behavior can be repeated before and after a deploy.
- The **TortoiseBots dashboard** is a separate daemon with its own UDP protocol. `dashboard-replay` feeds it a recording so its views can be exercised with no server; `decisions pull` fetches that daemon's decision captures into a walk folder.

Two commands in this area never touch the game: `trace-score`, `fight-score`, `stage-check` and `trash-check` read files only and log in to nothing.
