---
type: Concept
title: The config files
description: What the settings file, the slot table and the fleet file each hold, and which secrets they carry.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/config/config.go
tags: [getting-started, concept, settings, slots, fleet]
sources:
  - id: config
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/config/config.go
  - id: slots
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/slots/config.go
  - id: fleet
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/fleet.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:25:00+01:00
status: draft
---

# The config files

Three JSON files configure the agent. Each has one job; only two carry secrets.

## Settings file

The settings file holds the server address, your account passwords, and folder paths. Default `<home>/.config/turtle-agent/settings.json`, mode `0600`. `login`, `fleet` and `slot setup` read it. Only `realmd` and `accounts` are required.

It is the only file with passwords, so nothing read from it may reach a log line, an error, an event or a capture. Account names stay out of logs and errors too: with one account in the file, `login <character>` keeps the name off the command line.

## Slot table

The slot table names each server's slots: their account, character, laptop RPC port, and the `settings_from` file that holds the account's password. It holds no passwords itself. Default `<home>/.config/turtle-agent/slots.json` (`--slots FILE` picks another file). Each server entry names its `realmd`, optional `realm` and `world_addr` overrides, and its slots. A slot needs `id`, `account`, `character`, `port` and `settings_from` (a `reserved` slot names a reason instead and is never leased).

Leases live beside the slot table, one file per server and slot, so a `--slots FILE` outside the default directory keeps its own leases. Two ledgers cannot see each other, so mixing them for one server is refused.

A `--slots FILE` outside the default directory keeps its leases (`<dir>/leases`), its instance-cap ledger and its `instance-cap-per-account.json` there. Two ledgers cannot see each other, so mixing them for one server is refused: the walk refuses to start (exit 2, `one ledger per server`) when the default directory holds a live lease for the same server. `--allow-split-ledger` overrides it for test sandboxes only.

The per-server core instance cap lives beside the table in `instance-cap-per-account.json` (`{"<server>": 30}`), never in the slot table: older binaries refuse unknown fields there.

## Fleet file

The fleet file lists one member per character: `account`, `character` and `rpc`. The account is a key into the settings file, which holds the password; the fleet file never carries a secret. Members may add `reconnect` (overrides the fleet flag) and `create` (the `login --create` spec for a missing character).

## How they connect

- Alone on your machine: write a settings file and run `login`.
- A party on your machine: write a settings file and a fleet file, run `fleet`.
- A worker on a shared laptop: take a slot from the slot table with `lease acquire`, and `slot setup` writes the slot dir's private `settings.json` from the slot's `settings_from` file.
