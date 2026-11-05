---
type: Overview
title: Combat overview
description: How spells, the brain, rotations, tactics, the addon host, Eluna and LFT fit together.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/brain/brain.go
tags: [combat, overview]
sources:
  - id: brain-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/brain/brain.go
  - id: spells-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/spells/spells.go
  - id: spellstate-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/spellstate/spellstate.go
  - id: tactics-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/tactics/tactics.go
  - id: addonhost-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/addonhost/host.go
  - id: eluna-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/eluna/client.go
  - id: lft-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/lft/msg.go
  - id: rotations-doc
    resource: https://github.com/pjerra/turtle-agent/blob/dev/docs/ROTATIONS.md
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:35:00+01:00
status: draft
---

# Combat overview

Combat in turtle-agent is the brain playing your character: it reads the [spell state](concept-spell-data.md), picks actions from strategies and [rotations](reference-rotation-format.md), bends them with per-boss [tactics](guide-boss-tactics.md), and sends real client packets (`cast`, `attack`).

The parts:

- **Spell data** (`internal/spells`) names spells by id and describes their mechanics. It is generated from the core's `spell_template` and shipped inside the binary. Read [Spell data and spell state](concept-spell-data.md).
- **Spell state** (`internal/spellstate`) is the live picture: your spellbook, cooldowns, the global cooldown, and the cast in progress. The `spells` and `cast_state` calls read it.
- **The brain** (`internal/brain`) ticks every 200 ms, scores candidate actions by relevance band, and plays the winner. You start it with `brain_start`, watch it with `brain_status`, stop it with `brain_stop`. Read [The brain engine](concept-brain-engine.md) and [Run the brain](guide-run-brain.md).
- **Rotations** are JSON priority lists that replace one class strategy: what a frost mage casts, in what order. Seven are built in; 31 `kb_*` files come from the Turtle knowledge base; you can add your own with `rotations_dir`. Read [Write a rotation](guide-write-rotation.md).
- **Tactics** (`internal/tactics`) are the per-boss extras beyond the rotation: kill the add first, interrupt that cast, dispel this aura, leave that circle. They live in the dungeon plan and only the leader-mode brain acts on them.
- **The spell oracle** (`internal/kbspells`) is the post-fight judge: what each spec should cast, and whether your bots cast it. Read [The spell oracle](concept-spell-oracle.md) and [Score a fight](guide-score-fight.md).
- **The addon host** runs the real TortoiseBotsManager addon headless in a `lua5.1` sidecar, driven by `addon_start`, `addon_click`, `addon_tick`. Read [Drive the addon host](guide-addon-host.md).
- **Eluna hooks** are the one `tt` call: send a test-toolkit command to the server, get its `TTJSON` replies. Read [Eluna test hooks](concept-eluna.md).
- **LFT** is the Turtle group finder as an addon conversation (`lfg_join`, answers, `lfg_leave`). Read [Queue through LFT](guide-queue-lft.md).

A typical session: check the rotation offline (`rotation check`), start the brain with that rotation (`brain_start`), queue or walk in, and score the fight afterwards (`fight-score`). The [first-rotation tutorial](tutorial-first-rotation.md) walks that loop once.
