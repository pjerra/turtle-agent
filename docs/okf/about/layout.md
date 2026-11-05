---
type: Reference
title: Code layout
description: What each package in the repository does, and how the SMSG parser tests stay honest.
resource: https://github.com/pjerra/turtle-agent/blob/dev/README.md
tags: [about, layout, packages, parsers, fuzz]
sources:
  - id: readme
    resource: https://github.com/pjerra/turtle-agent/blob/dev/README.md
generated:
  by: omp
  at: 2026-11-05T21:45:00+01:00
status: draft
---

# Code layout

Each package has one job. You read a run from the session outwards: the CLI starts it, the session owns it, and the game packages decode what the server sent.

| Package | What it does |
| --- | --- |
| `internal/auth` | SRP6 and realmd: logon challenge/proof, realm list |
| `internal/worldconn` | TCP framing, the 1.12 header cipher, auth session with the addon block |
| `internal/session` | single-owner session loop: login, ping, chat, addon messages, channels, gossip, names, party, world, actions, combat, raid icons, loot rolls, inventory, equip and use, vendors, trainers and quests, corpse loot, mounts, logout, and `Replay`, which runs a capture's packets through the handlers offline |
| `internal/game` | packet codecs for chat, addon messages, channels, gossip, queries, groups, actions, the combat log, raid icons, loot rolls, the loot window, items, mounts, vendors, trainers and quests |
| `internal/objects` | update-object parser and the object store: units (positions, splines, auras, casts), the items in the backpack and bags, and the quest log |
| `internal/spells` | spell names, mounts and metadata (cost, cast time, range, cooldowns, GCD, effects) by id, from the core's `spell_template` and the server's DBCs (`tools/spellnamegen`) |
| `internal/spellstate` | the agent's own spellbook, cooldowns, global cooldown and cast state, fed by the session |
| `internal/navmesh` | tile loading and path planning over the core's Detour (cgo); `navfixture` builds synthetic tiles |
| `internal/events` | ordered event log with cursors |
| `internal/dungeon`, `dungeons/`, `tools/dungeongen` | dungeon plans and runs: the JSON format, loader, stepper and the driver (`dungeon_run`), the plan files, and the skeleton generator over world database exports |
| `internal/trace`, `internal/tracecheck`, `internal/tracerun` | run traces: the `bt-trace` format, writer, reader and summary files; the pure checks that score a trace; the recorder a run writes with |
| `internal/tbm` | the TortoiseBots addon (TBM) protocol: handshake commands, the `TBM:` reply lines, the generated button table, the transport rule and reply matching (`tools/tbmbuttongen` writes the table) |
| `tools/tbmlines`, `tools/tbmcontract` | extract `TBM:` lines from an events file; replay them through the real TBM addon (Lua) |
| `internal/addonhost` | the addon host: the lua5.1 sidecar that runs the real TBM addon on the 1.12 widget mock, and the events it gets |
| `internal/mapview` | map images: the run from frames, events and reports, floors by height, the SVG and HTML renderers |
| `internal/capture` | packet capture files and the secret scan |
| `internal/dashboard` | the live dashboard: agent discovery, the read-only poller, the card model, the plan drawing, the secrets filter, the embedded page |
| `internal/rpc`, `internal/netutil` | loopback JSON-RPC server |
| `internal/tactics` | the `tactics` block of a plan's boss: the file format and its checks |
| `cmd/turtle-agent` | the CLI (`login`, `fleet`, capture tools including `capture events`, `map`, `dashboard`, `trace-score`, `pulltest`, `rotation`) |

Parser tests: every SMSG parser in `internal/game` and `internal/objects` has a `Fuzz*` target (no panic, no hang within 5 s per input) seeded from the committed captures, a few literal packets copied from live captures (`internal/game/realpackets_test.go`: gossip, vendor list, raid icons, root and knock back orders, new world; the runs never opened a trainer, a quest panel or a loot window, so those parsers still run only on `gametest` packets) and the `gametest` builders. `make fuzz` runs them all; an input a fuzzer finds is saved under `<package>/testdata/fuzz/` and stays as a regression seed that `make test` runs.
