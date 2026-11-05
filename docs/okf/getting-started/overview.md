---
type: Overview
title: Getting started overview
description: What turtle-agent is, who it is for, and how the getting-started pieces fit together.
resource: https://github.com/pjerra/turtle-agent/blob/dev/README.md
tags: [getting-started, overview, install, login, slots]
sources:
  - id: readme
    resource: https://github.com/pjerra/turtle-agent/blob/dev/README.md
  - id: main
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/main.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:25:00+01:00
status: draft
---

# Getting started overview

turtle-agent is a headless World of Warcraft 1.12 client: one process logs one character in, with no game window, and exposes that character over a local JSON-RPC API. You drive a test character from scripts or AI agents: chat, GM commands, NPC gossip, parties, movement, combat.

It was built to test server-side bots (TortoiseBots) on a private test server: log in, hire or invite bots, start a fight, then read back the combat log and what each bot did.

## Who it is for

- Test workers that play a character like a real player and report what happened.
- Anyone checking TortoiseBots behaviour against the vision in `docs/TORTOISEBOTS-VISION.md`: bots should quest, grind, travel on foot and by flight path, use towns, stay active, and earn their gold and gear.

Use it only on servers you run or are allowed to test on.

## How the pieces fit

1. **Install** it: `make build` gives you `bin/turtle-agent`. See [Install and build](install.md).
2. **Write the settings file**: the server address, your accounts and passwords, and folder paths. See [The config files](settings-concepts.md) and [Write the settings file](write-settings.md).
3. **Log a character in**: `turtle-agent login` puts one character in the world and serves RPC on a loopback address. See [Log a character in](log-in.md).
4. **Drive the character**: `turtle-agent rpc` sends one JSON-RPC call to a running agent. See [Drive the agent with RPC](drive-rpc.md). The calls themselves live in [The agent and its RPC](../agent/index.md).
5. **Share test characters**: on a shared laptop, slots name each server's characters and leases hand them to one worker at a time. See [Slots and leases](slots-leases-concepts.md) and [Lease a slot and set it up](lease-slot.md).
6. **Log several characters in**: `turtle-agent fleet` starts one login process per party member. See [Log several characters in](fleet-party.md).

New here? Start with [First login](first-login.md): it walks the whole path once, start to finish.
