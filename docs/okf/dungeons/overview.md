---
type: Overview
title: Dungeon runs overview
description: What a dungeon run is, the three modes, and how the parts of a run fit together.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dungeon_methods.go
tags: [dungeon, commander, overview]
sources:
  - id: dungeon-methods
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dungeon_methods.go
    title: dungeon_run RPC, validation, and run lifecycle
  - id: dungeon-modes
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dungeon_modes.go
    title: Run modes and leader brain presets
  - id: remote-driver
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/dungeon/remote.go
    title: The commander-mode driver
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:30:00+01:00
status: draft
---

# Dungeon runs overview

A dungeon run walks a five-player party through a dungeon from a plan file (`dungeons/<name>.json`): which packs to pull, which bosses to kill, which doors to open. You start one with the `dungeon_run` call, poll it with `dungeon_status`, and end it with `dungeon_stop`. One run goes at a time; a second `dungeon_run` is refused until the first ends.

## The three modes

The `mode` field picks who does the fighting. See [Run modes](modes.md) for the full comparison.

- **Commander** (`mode: "commander"`, old name `"remote"`): your character stands outside the group and drives hired or named bots through the TortoiseBots remote commander (`.bot commander bind`, `.bot cmd ...` orders, the `TBM:VIEW` perception feed). This is the main mode for testing dungeons.
- **Leader** (`mode: "leader"`, old name `"group"`): your character is in the party, leads it, and fights, played by the bot brain. Omit `mode` for the older group form, where the brain is optional.
- **Observe** (`mode: "observe"`): a TortoiseBots run leader on the server clears the dungeon (`.bot run start`); your agent prepares, binds, and enters the party, then only watches and scores.

The answer names both: `mode` is `group`, `remote`, or `observe`; `run_mode` is `leader`, `commander`, or `observe`.

## The parts of a run

A commander run goes through these phases, in order:

1. **Prepare.** Bring the bots online, reset their instances, hire a fresh party if asked. See [Hire the party and enter](hire-party.md).
2. **Bind.** Take command of the party (`commander bind`) and turn the perception feed on (`commander view on`).
3. **Enter.** Get the party into the dungeon: teleport (GM), walk through the portal, or `cmd enter`. The hourly entry cap applies; see [Refusals, the watchdog, and the entry ledger](refusals-watchdog.md).
4. **Start.** Move the party to the first step (`from_leg`, `from_pull_group`), or the whole plan from leg 0. See [Start anywhere](start-anywhere.md).
5. **Steps.** Walk, pull, kill bosses, loot, talk to NPCs. The carrier bot does object steps; gossip steps go to a bot or your character. See [Loot and gossip](loot-gossip.md).
6. **End.** `cmd stay`, unbind, optional logout. The report keeps everything: steps, orders, refusals, loot, timings.

Two guards watch every run: the **plan check** walks the plan on the navmesh before a commander run starts (refused plans fail unless you pass `force`), and the **progress watchdog** warns, recovers, then fails a run that stops making progress.

## The calls around a run

- `dungeon_run` starts, `dungeon_status` polls, `dungeon_stop` ends. `dungeon_pause` halts a commander run with its binding kept so the next `dungeon_run` resumes the same instance; `dungeon_resume` and `dungeon_skip` belong to observe mode only.
- `dungeon_stage` walks the party to a leg start and optionally starts the run there.
- `plan_check` runs the navmesh check by hand.
- `tune_set` and `tune_list` change a running commander run's tunables; see [Tune a live run](live-tuning.md).
- `commander` and `bot_view` drive the remote commander and read its feed by hand; see [Remote-control calls](ref-remote.md).

## Variants that replace the plan loop

- **Pull test** (`pulltest` inside `dungeon_run`): pull one pull group several times and score each try. Minutes instead of a full clear; see [Score one pack](pulltest.md).
- **Many characters** (`fleet` shell command): log a whole fleet in at once; see [Run a fleet](fleet.md).
- **Group finder** (`lfg_*` calls): queue through Turtle's LFT instead of commanding bots; see [Find a group](lft.md).
