---
type: Concept
title: What turtle-agent is
description: One process per character, the local RPC server, and how the agent tests TortoiseBots.
resource: https://github.com/pjerra/turtle-agent/blob/dev/README.md
tags: [getting-started, concept, rpc, tortoisebots]
sources:
  - id: readme
    resource: https://github.com/pjerra/turtle-agent/blob/dev/README.md
  - id: design
    resource: https://github.com/pjerra/turtle-agent/blob/dev/docs/superpowers/specs/2026-10-12-turtle-agent-design.md
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:25:00+01:00
status: draft
---

# What turtle-agent is

turtle-agent is a headless WoW 1.12 client in Go for vanilla 1.12 / Turtle WoW 1.18.1 servers (login build 7272, world build 5875). Headless means no game window: one process logs one character in and exposes it over a local JSON-RPC API.

You drive a test character from scripts or AI agents. It can chat, use channels and GM commands, send addon messages, use NPC gossip, join a party, list units around it, target, attack, cast, take short walks, walk on the server's own navmesh, use GM teleports, set raid icons, roll on group loot, use items from the bags, loot corpses, mount up, equip from the backpack, buy and sell at vendors, learn from trainers, do quests, and keep an ordered event log of chat, combat, marks, rolls and NPC results.

It was built to test server-side bots (TortoiseBots) on a private test server: log in, hire or invite bots, start a fight, then read back the combat log and what each bot did. Judge what you see against `docs/TORTOISEBOTS-VISION.md`: bots should play like real players, and your own GM commands are test tooling, never bot behaviour (see `AGENTS.md`).

Three facts shape everything else in these docs:

- **One process, one character.** `login` owns a single session. For a party, run one process per character with `fleet`, or one slot dir per worker with `lease` and `slot setup`.
- **The RPC server is local only.** It binds a loopback address, refuses browsers (`Origin` header) and other host names, and every call carries a bearer token. Remote players never reach it.
- **The agent behaves like a real 1.12 client.** It sends only packets a real client sends and keeps movement anticheat-clean. It never answers Warden: expect one `WARDEN: ... timeout` line per session on a server that ships a module, and treat it as noise. See [Log a character in](log-in.md) for logout outcomes that look like disconnects.
- **It does almost no client side.** It does no rendering and no inventory management beyond listing the bags, using and equipping items, taking corpse loot, and buying and selling.
