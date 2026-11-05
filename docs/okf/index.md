---
okf_version: "0.2"
---

# turtle-agent documentation

turtle-agent is a headless WoW 1.12 client in Go: it logs a character in, plays it from a script or an RPC call, and drives TortoiseBots parties through dungeons and raids for testing.

## Start here
- [Getting started](getting-started/index.md) — what turtle-agent is, install it, and log a character in.

## Use it
- [The agent and its RPC](agent/index.md) — how one agent plays a character, and the calls you can send it.
- [Dungeon runs](dungeons/index.md) — run a TortoiseBots party through a dungeon with the commander.
- [Raids](raids/index.md) — walk a raid with `raid-walk`, and keep many raids running with `raid-autopilot`.
- [Plans](plans/index.md) — the dungeon and raid plans: how they are made, checked and exported.

## Under the hood
- [Navigation and world data](navigation/index.md) — the navmesh, maps, spawns and patrols the agent moves by.
- [Combat](combat/index.md) — spells, rotations, tactics and the addon host.
- [Observability and testing](observability/index.md) — the dashboard, captures, traces and scenarios.

## About these docs
- [How this documentation is written](about/conventions.md) — the OKF format and the style rules every page follows.
- [Change log](log.md) — what changed in these docs, newest first.
