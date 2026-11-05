---
type: Concept
title: Run modes
description: "Commander, leader, and observe modes: who acts, what each allows, and how results differ."
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dungeon_modes.go
tags: [dungeon, modes, concept]
sources:
  - id: dungeon-modes
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dungeon_modes.go
    title: Run modes and leader brain presets
  - id: dungeon-remote
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dungeon_remote.go
    title: Commander-mode checks and remote start
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:50:00+01:00
status: draft
---

# Run modes

`mode` picks who does the fighting. The names `"remote"` and `"group"` still work and mean commander and leader; an omitted `mode` is the older group form (leader without a mandatory brain).

## Commander: the agent drives from outside

Your character is not in the group. It binds the bots (`commander bind`), reads their world through the `TBM:VIEW` feed, and sends orders (`.bot cmd ...`): walk, pull, loot, gossip, revive. The bots do everything physical; your character only steps in for object-step fallbacks (opening a chest itself and trading the item over) and GM moves (teleporting the party to a partway start).

Commander mode owns the largest option set: the party (`lead_bot`, `bots`, `tank`, `healer`, `carrier`, `cc_bot`, `cc`), the setup (`prepare`, `enter`, `preflight`, `team`, `have_items`, `give_items`, `from_pull_group`, `start_here`, `force_start`), the ending (`on_end`, `keep_on_fail`), and tuning (`tune`, `live_reload`). It refuses the GM options (`teleport`, `buffs`, `gm`, `gm_level`), the brain options (`brain`, `rotation`, `role`), and `wait_sickness`: it sends no `.aura` and plays no character. The result answers `mode: "remote"`, `run_mode: "commander"`, `leader_gm: "false"`.

## Leader: your character plays

Your character is in the party, leads it, and fights, played by the bot brain. The run walks with your character's own movement and loots with its own hands. `mode: "leader"` picks the class preset when `brain` is empty; the old group form leaves the brain off unless named. Leader mode keeps the GM options (`teleport`, `buffs`, `gm`, `gm_level`, `recover_wipe` defaulting off) and refuses everything commander-only. The result answers `mode: "group"`, `run_mode: "leader"`.

`mode: "leader"` with named commander bots (`lead_bot`/`bots`) is not live-checked: leader mode walks the party itself, so it refuses the commander party fields.

## Observe: the server plays

A TortoiseBots run leader on the server clears the dungeon; your agent prepares, binds, and enters exactly like a commander run, sends `.bot run start`, and then only reads the `TBM:RUN` status. No order follows the start, so every option that steers bots is refused. What remains besides the shared fields: where the run leader starts (`from_leg`, `from_pull_group`, `from_step`), when the watch ends (`observe.until`, `max_run_s`, `stall_s`, `paused_wait_s`), and how it behaves (`auto_resume`, `maintenance`, `allow_far_start`, `keep_loot_setting`). Raid plans run in observe mode only, with a hired party: leading a raid of bots from outside is the server's job.

## How to choose

Testing a dungeon plan or a TortoiseBots build: commander. Testing your character's rotation against a pack: leader with a pull test. Keeping many raids running or checking a raid plan: observe; see [Raids](/raids/index.md).
