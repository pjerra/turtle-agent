---
type: Tutorial
title: Your first dungeon run
description: Hire a bot party and run a dungeon to the end, start to finish, with real commands.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dungeon_methods.go
tags: [dungeon, commander, tutorial, hire]
sources:
  - id: dungeon-methods
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dungeon_methods.go
    title: dungeon_run RPC, validation, and run lifecycle
  - id: hire
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/dungeon/hire.go
    title: The hire step
  - id: prepare
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/dungeon/prepare.go
    title: The prepare block
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:30:00+01:00
status: draft
---

# Your first dungeon run

You hire five bots, walk them through a dungeon, and read the report. Your character needs to be logged in and standing in the world. `<dir>` below is your agent's slot directory (the one you pass to `turtle-agent rpc`).

## 1. Start the run with a hired party

```sh
turtle-agent rpc <dir> dungeon_run '{"name":"<plan>","mode":"commander","prepare":{"party":"hire"}}'
```

The agent hires a warrior tank, a priest healer, and three damage dealers at the plan's level, binds them, enters the dungeon, and starts at leg 0. The answer comes back at once:

```json
{"started":true,"plan":"<plan>","mode":"remote","run_mode":"commander","run_id":"r-20261105t200000-ab12"}
```

`started: true` means the run's goroutine is going, not that the dungeon is clear. Keep the `run_id`: it names the trace file.

## 2. Watch it go

```sh
turtle-agent rpc <dir> dungeon_status
```

The answer carries `running`, `state` (`running`, later `done`, `failed`, `stopped` or `refused`), and the stall fields `last_progress_at`, `stalled_for_s`, `stall_level`. Poll every minute or so. A healthy run shows fresh `last_progress_at` and `stall_level: "none"`.

## 3. End it or let it finish

When `state` is `done`, `failed`, `stopped` or `refused`, the run has ended by itself (`refused`: it stopped before it started, nothing was sent, and `report.error` names the reason). The full `report` in the status answer says what happened: legs cleared, bosses killed, loot taken, orders refused, and the trace file path. To stop a running run early:

```sh
turtle-agent rpc <dir> dungeon_stop
```

`dungeon_stop` unbinds the party (the bots go back to following your character) and returns the final status.

## 4. Read the trace

The report's `trace_file` points at the run's trace. Score a finished fight from it with `fight-score`, or print a pull-test table with `pulltest`; see [Score one pack](pulltest.md) and [Dungeon shell commands](ref-cli.md).

## What to try next

- Run a plan you mapped yourself: drop the `prepare` block and name your bots with `lead_bot` and `bots`; see [Run a commander dungeon](run-commander.md).
- Start halfway in: pick a leg with `from_leg`; see [Start anywhere](start-anywhere.md).
- Change a number mid-run without restarting: [Tune a live run](live-tuning.md).
