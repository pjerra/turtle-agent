---
type: Concept
title: The live event log
description: The events RPC, its kinds, and how to page through the log.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/events/log.go
tags: [events, concept]
sources:
  - id: events
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/events/log.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:44:00+01:00
status: draft
---

# The live event log

The agent keeps an in-memory ring of everything it saw: one row per packet parsed, chat line, combat outcome, aura, loot roll, gossip panel, quest update, dungeon-run step, brain decision, and more. The `events` RPC pages through it; scenarios wait on it; `capture events` replays a capture into the same rows.

## The RPC

Call `events` with `since` (the last `seq` you saw; 0 starts at the oldest kept), `kinds` (only these kinds), `party_only`, and `limit` (default 500, at most 5000). The answer holds the events, `next` (pass it as the next `since`), `oldest`, `dropped`, and `more`. The `chat_log` method is the same narrowed to `chat`, `whisper` and `system`.

Each row carries `seq`, wall `time`, `kind`, `source` and `target` (names and guids), `spell_id`, `amount`, `crit`, `party`, `text`, and a `data` object with the kind-specific fields.

## The kinds

There are over eighty kinds. The ones you meet first:

- Talk: `session`, `chat`, `whisper`, `system`, `party`, `channel_notify`, `addon`, `raid_mark`.
- Combat: `melee`, `spell_damage`, `periodic`, `heal`, `energize`, `spell_start`, `spell_go`, `spell_fail`, `spell_miss`, `aura_applied`, `aura_removed`, `death`, `kill`, `attack_start`, `attack_stop`, `cooldown_start`, `channel_start`.
- Loot and vendors: `loot_roll_start`, `loot_roll`, `loot_roll_won`, `loot_open`, `loot_taken`, `loot_money`, `vendor_list`, `vendor_buy`, `vendor_sell`, `trainer_list`.
- Quests and gossip: `gossip`, `gossip_complete`, `quest_list`, `quest_details`, `quest_accepted`, `quest_rewarded`, `quest_update`, `quest_error`.
- Travel: `taxi_reply` (the flight master's answer: `{result, reason}`, `ok` when the flight was accepted).
- The dungeon driver: `dungeon_leg`, `dungeon_step`, `dungeon_failed`, `dungeon_done`, `dungeon_leader`, `player_follow`.
- The brain: `brain_decision`, `brain_idle`, `brain_state`, `bot_view_frame`.
- Pets and trade: `pet_summoned`, `pet_died`, `pet_revived`, `pet_dismissed`, `pet_error`, `trade_status`.

When the ring fills, the oldest tenth is dropped; the `dropped` count in the page says how many rows you missed. Poll faster or narrow `kinds` when you see it grow.
