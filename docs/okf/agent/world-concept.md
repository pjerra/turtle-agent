---
type: Concept
title: World state
description: What status, units, party and the event log tell you about the character and its surroundings.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/session/session.go
tags: [agent, status, units, party, events]
sources:
  - id: session-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/session/session.go
    title: StatusInfo and session state
  - id: objects-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/objects/store.go
    title: Unit, aura, cast, item and game-object store
  - id: events-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/events/log.go
    title: Event kinds and the polling log
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T22:45:00+01:00
status: draft
---

# World state

The agent keeps a live model of what the character sees. You read it with `status`, `units`, `unit`, `party`, `gameobjects` and the `events` log.

## status

`status` never fails. It returns the session `state` (`in_world` when the character can act), the character name, team (`alliance` or `horde`), GUID, level, map id, position (`x`, `y`, `z`, `o`), current target, health, power, and two flags: `dead` (health 0, spirit not released) and `ghost` (spirit released)[^session-go]. It also carries `teleport_seq` (server teleports acked), `latency_ms`, unknown-opcode and parse-error counters, and `dead`/`ghost`.

When the character is not in the world, most other RPC calls answer `-32001` (`CodeNotInWorld`):

`not in world (state <state>)`

## units

Each row is one unit the server has shown the agent: `guid`, `type` (`player` or `creature`), `entry`, `name`, position, `distance` (3D yards, -1 when unknown), health, power, level, `hostile`, `in_combat`, `in_party`, `auras` (up to 48 slots; remaining time only for the player's own auras), and the current `casting` spell[^objects-go]. Health can be a percent (1–100 with max 100) where the server hides the real value.

## Game objects

`gameobjects` reads the same update stream as `units`, nearest first, but game objects never appear in `units`. Each row carries `type`/`type_name` (the `GameobjectTypes` id and name), `state` (`ready` for a closed door, `active` for open or used, `active_alt`), `flags` and `dyn_flags` (bit 0 is the interact sparkle), `locked` (`GO_FLAG_LOCKED`), `usable` (a hint from the update fields, false for generic, no-interact, in-use or interact-condition objects), `display_id`, and `created_by` (the unit that summoned it, when any). `name` stays empty until the `SMSG_GAMEOBJECT_QUERY_RESPONSE` for that entry arrives. Objects whose position the server never sent (ships, elevators on a transport path) report `distance` -1. Drive them with `use_object` (doors, levers, quest objects) and `open_object` (chests, nodes) — see [Talking, trading and the world](/agent/social-guide.md) §§6,9.

## party and group

`party` returns `in_group`, `raid`, `assistant`, `subgroup`, the `members` list (name, guid, online, health, power, level, zone) and the pending invite. Out-of-view members show their last known stats. Change the group with `invite`, `accept_invite`, `leave_group`, `promote`, `assistant`, `convert_raid`, `raid_move`, `loot_method` and `disband` — all leader-gated by the server where it matters.

## Raid icons

`mark` puts a raid icon on a unit (`skull`, `cross`, `square`, `moon`, `triangle`, `diamond`, `circle`, `star`, or `clear`), or clears one. It waits 2 seconds for the server's echo. Anything else is refused:

`unknown raid icon "<icon>": use skull, cross, square, moon, triangle, diamond, circle, star or clear`

`no MSG_RAID_TARGET_UPDATE echo within 2s: only the group leader or an assistant may set raid icons`

## events and chat_log

`events` is the agent's memory: every state change, combat line, loot roll, gossip menu, trade step and dungeon step lands there with a sequence number. Poll with `since` (your last `next`), `kinds` to filter, `party_only`, `limit` and `wait_ms` for long-polling (up to 25 seconds). `chat_log` is the same log filtered to `chat`, `whisper` and `system`[^events-go].

A stale cursor (the log restarted since your `since`) returns `dropped: true`: re-read from `oldest`. Unknown param fields are rejected (`-32602`); misspelled `kinds` are not — they match nothing.
