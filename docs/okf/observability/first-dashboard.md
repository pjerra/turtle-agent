---
type: Tutorial
title: Watch your first run on the dashboard
description: Start the read-only dashboard, open it in a browser, and read one run card.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dashboard_cmd.go
tags: [dashboard, tutorial]
sources:
  - id: dashboard-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dashboard_cmd.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:36:00+01:00
status: draft
---

# Watch your first run on the dashboard

Start the dashboard, open the page, find your run.

## 1. Start one agent

Log a character in so the dashboard has something to show. Any running agent works.

## 2. Start the dashboard

```sh
turtle-agent dashboard
```

It prints the address it listens on:

```text
turtle-agent dashboard 1.2.3 on http://127.0.0.1:8790/ (read-only; agents: auto)
```

`agents: auto` means it found your agents in the settings files. Leave this terminal open: the page only lives while this command runs.

## 3. Open the page

Open `http://127.0.0.1:8790/` in a browser on the same machine.

You see one card per agent: character name, level, map and position, and, while a dungeon run goes, the plan, the leg, and the run's counters.

## 4. Read the run card

Each card shows the run's state at the last fetch: the server samples each agent every 1.5 s, and the page fetches the state every 2 s by default. What you read is at most one page interval old: orders the commander sent, the party list with health and power, recent events, and the trail of where the party walked. A stall shows yellow after 2 minutes and red after 5.

## 5. Change the refresh rate

The header's "Refresh every" picker sets how often the page polls (1 / 2 / 5 / 10 / 30 s, or paused). Paused freezes the map so you can look closely. Your choice is saved in the browser.

## 6. Stop the dashboard

Press Ctrl-C in the dashboard terminal. Nothing was written anywhere: without `--edit` or `--control` the page only reads.
