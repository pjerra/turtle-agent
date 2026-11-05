---
type: Reference
title: RPC reference
description: "Every core RPC method: params, result, and what can go wrong."
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/methods.go
tags: [agent, rpc, reference, status, movement, combat]
sources:
  - id: methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/methods.go
    title: Core RPC methods and param structs
  - id: rpc-server-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/rpc/server.go
    title: Error codes and the JSON-RPC envelope
  - id: brain-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/brain_methods.go
    title: brain_start, brain_stop, brain_status
  - id: follow-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/follow_methods.go
    title: follow_start, follow_stop, follow_status
  - id: death-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/death_methods.go
    title: Death methods
  - id: group-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/group_methods.go
    title: Group methods
  - id: social-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/social_methods.go
    title: addon_send, tbm, channels, gossip
  - id: item-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/item_methods.go
    title: use_item, loot, open_object, mount
  - id: npc-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/npc_methods.go
    title: vendor, trainer, quest methods
  - id: pet-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/pet_methods.go
    title: pet and pet_action
  - id: gameobject-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/gameobject_methods.go
    title: gameobjects and use_object
  - id: trade-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/trade_methods.go
    title: trade
  - id: spell-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/spell_methods.go
    title: spells and cast_state
  - id: map-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/map_methods.go
    title: map_snapshot
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T22:45:00+01:00
status: draft
---

# RPC reference

Call form: `turtle-agent rpc [--timeout <dur>] [--raw] <walk-dir> <method> [<params-json>|-]`. `--timeout` defaults to `30s`; params must be a JSON object or array (`-` reads stdin). Exit 0 on success, 15 when the agent cannot be reached, 16 on a JSON-RPC error (`error: <code> <message>`).

Error codes: `-32700` parse, `-32600` invalid request, `-32601` method not found, `-32602` invalid params (also: unknown fields are rejected), `-32000` internal, `-32001` not in world, `-32002` disconnected, `-32003` game refusal, `-32004` timeout. `logout`'s `lost`/`timeout` errors carry `data.outcome`. Unless noted, in-world-only calls answer `-32001` (`not in world (state <state>)`) outside the world.

`{}` means no params and an empty-object result. `wait_ms` defaults to a per-method wait and caps at 25000 unless noted.

## Session and observation

| Method | Params | Result |
|---|---|---|
| `status` | `{}` | `{state, character?, team?, guid?, level?, map, x, y, z, o, target?, health, max_health, power, max_power, falling, z_unknown, teleport_seq, latency_ms, unknown_opcodes, parse_errors, last_seq, mono_ms, base_time, wall_offset_ms, error?, combo_points, combo_target?, gcd_ms, dead, ghost}`. Never fails. |
| `events` | `since`, `kinds?`, `party_only?`, `limit?`, `wait_ms?` | `{events[], next, oldest, dropped, more}`. Stale `since` returns `dropped: true`; re-read from `oldest`. |
| `chat_log` | `since`, `limit?`, `wait_ms?` | Same page, filtered to `chat`, `whisper`, `system`. |
| `logout` | `{}` | `{outcome: complete\|closed_by_server}`. `lost`/`timeout` are errors with `data.outcome`. Exits the process. |
| `units` | `within?`, `near?` (older name), `entry?`, `hostile?`, `name?` (substring), `limit?` (0: 100), `bound?` (commander binding only, newest `TBM:VIEW` frame ≤ 60 s) | Unit rows, nearest first. Never fails. |
| `unit` | `guid?`, `name?` | One unit row. `target needs a guid or a name`; `no unit named "<n>" nearby`. |
| `party` | `{}` | `{in_group, raid?, assistant?, subgroup?, leader?, members[], pending_invite?, loot_method?, loot_master?, loot_threshold?, marks?}`. Never fails. |
| `map_snapshot` | `floor?`, `plan?`, `floors?` (`auto`/`off`/`"<lo>,<hi>"`), `background?` (default draws navmesh), `html?`, `pad?` | `{map, floor, floors[], seconds, members, creatures, events, background, notes[], svg, html?}`. `nothing to draw yet: the recorder has no position on the map (is the character in the world?)`. |

## Chat, GM, group

| Method | Params | Result |
|---|---|---|
| `chat` | `type*` (`say party yell emote guild raid whisper channel` + leader/warning/battleground variants), `text*` (≤ 255 bytes), `to?` (whisper target or channel) | `{}`. `chat: a whisper needs a target name`; `chat: a channel message needs the channel name in to`. |
| `gm` | `text*` (dot added when missing) | `{}`. |
| `gm_kill` | `guid*` (creature) | `{}`. Refused outside the agent's test setup. |
| `invite` | `name*` | `{}`. |
| `accept_invite` | `{}` | `{}`. `no pending group invite` when none. |
| `leave_group` | `{}` | `{}`. |
| `promote` | `guid?`, `name?` | `{}`. Leader only. |
| `assistant` | `guid?`, `name?`, `clear?` | `{}`. Raid only, leader only. |
| `convert_raid` | `{}` | `{}`. Leader only. |
| `raid_move` | `guid?`, `name?`, `subgroup*` (1–8), `swap_with?` | `{}`. Leader or assistant; raid only. |
| `loot_method` | `method*` (`free_for_all round_robin master_loot group_loot need_before_greed`), `master?`, `master_name?`, `threshold?` (1–5) | `{}`. Leader only. |
| `disband` | `{}` | `{}`. Leader only. |
| `mark` | `guid?`, `name?`, `icon*` (`skull cross square moon triangle diamond circle star clear`) | `{mark}` once the server echoes (2 s wait). `unknown raid icon "<i>": use ...`; echo timeout notes the leader/assistant rule. |
| `roll` | `roll_id?`, `item_slot?`, `looted?`, `choice*` (`need greed pass`) | `{roll_id, looted, item_slot, item_entry, choice}`. `unknown roll choice "<c>": use need, greed or pass`; open rolls come from `loot_roll_start` events. |

## Movement and combat

| Method | Params | Result |
|---|---|---|
| `target` | `guid?`, `name?` | Unit row after selection. |
| `attack` | `{}` | `{}`. `no target selected; call target first`. |
| `stop_attack` | `{}` | `{}`. |
| `cast` | `spell_id*`, `target?` (0: current target, else self) | `{}`. `cast needs a spell id`. |
| `cancel_cast` | `spell_id?` (0: whatever is casting) | `{}`. |
| `cast_state` | `{}` | `{cast{state idle\|casting\|channelling, ...}, last_cast?, last_failure?, gcd{active, remaining_ms, ...}, combo_points, combo_target?}`. |
| `spells` | `spell_id?`, `name?` (substring), `highest?`, `passive?` | Spell rows with cooldowns and costs. |
| `face` | `guid*` | `{}`. `unit 0x... is not in view`; refused while walking. |
| `walk_to` | `x*, y*, z*` (straight, ≤ 40 yd, ≤ 30 s) | `{}`. Longer: `walk_to is for short moves: ...`. |
| `path_to` | `x*, y*, z*` | `{map, from, to, status, points, length}`. Needs mmaps. |
| `walk_path` | `x*, y*, z*` | `{result arrived\|partial\|no_path, reason?, goal, position, dist_left, walked, replans, seconds, trace[]}`. Needs mmaps. |
| `teleport` | `x*, y*, z*`, `map?` | `{}`. `teleport: no MSG_MOVE_TELEPORT_ACK or SMSG_NEW_WORLD within 10 s (does the account have GM rights?)`. |

## Brain, follow, death

| Method | Params | Result |
|---|---|---|
| `brain_start` | `strategies*` (preset or names), `role?`, `tank?`, `no_move?`, `rotation?`, `run_id?` (≤ 64 chars), `trace?`, `trace_meta?` | Brain status. `the brain is running: brain_stop it first`; unknown strategy names list presets and strategies. |
| `brain_stop` | `{}` | Brain status. Refused while a dungeon run owns the brain. |
| `brain_status` | `{}` | `{running, strategies, role?, tank?, no_move?, move_held?, rest_ok, ticks, actions, idle?, last_decisions[], holds[], error?, run_id?}`. Never fails. |
| `follow_start` | `plan*`, `lead*`, `near?` (default 8, 3–30), `far?` (default 15, `near+2`–45), `assist?` (default false), `lost_s?` (default 30, 10–120) | Follow status (`running, state, plan, lead, ..., started, seconds`). Repeating only changes the lead. |
| `follow_stop` | `{}` | Final follow status. Never fails. |
| `follow_status` | `{}` | Follow status, or `{state: "stopped"}`. Never fails. |
| `death_status` | `{}` | `{dead, ghost, resurrect?, reclaim_in_ms}`. Never fails. |
| `accept_resurrect` | `{}` | The resurrect offer. `accept_resurrect: the player is alive`; `...: no resurrection is on offer (SMSG_RESURRECT_REQUEST)`. |
| `release_spirit` | `{}` | `{}`. Refused while alive or twice. |
| `corpse_query` | `{}` | `{found, map, x, y, z, corpse_map}`. `corpse_query: no MSG_CORPSE_QUERY answer within 5s`. |
| `reclaim_corpse` | `{}` | `{}`. Needs the ghost within 39 yd and the delay run out. |
| `area_trigger` | `id*` | `{}`. `area_trigger: needs a trigger id`. |

## Items, loot, trade, NPCs, pets

| Method | Params | Result |
|---|---|---|
| `inventory` | `bags?` (default backpack only) | Item rows `{bag, slot, guid, item_entry, count, temp_enchant?}`. Never fails. |
| `equip` | `item_entry?`, `bag?`, `slot?` | The sent item. Refusals arrive as `inventory_error` events. |
| `use_item` | `item_entry?`, `bag?`, `slot?`, `target?` (0: self) | The used item. Needs an entry or a slot. |
| `loot` | `guid?`, `name?`, `wait_ms?` (default 3000) | The loot window, or null. `loot needs a guid or a name`; `no lootable dead unit named "<n>" nearby`. |
| `loot_take` | `slot?`, `all?`, `money?`, `wait_ms?` | `{taken[], left?, skipped?, money?, window?}`. `loot_take needs a slot, all, or money`; `no loot window is open; call loot first`. |
| `loot_close` | `wait_ms?` | `{}`. |
| `open_object` | `guid*`, `spell_id?` (default tries the Opening spells), `take?`, `wait_ms?` (default 15000) | `{window, taken[], money?, left?}`. |
| `mount` | `item_entry?`/`bag?`+`slot?` xor `spell_id?`, `wait_ms?` (default 12000) | `{mounted, display_id?, via?, result?, learned_spell?}`. `already mounted`. |
| `dismount` | `spell_id?`, `wait_ms?` (default 3000) | Mount info. `not mounted`. |
| `gameobjects` | `within?`/`radius?`, `entry?`, `type?` (name or 0–30), `name?`, `limit?` | Game-object rows. Unknown type names are rejected. |
| `use_object` | `guid*`, `wait_ms?` (default 3000, cap 25000) | `{since, object, outcome, event?, hint?}`. `use_object needs a guid`; `game object 0x... is not in view`. |
| `trade` | `to*` (name in view or guid), `items?` (`[{item, count?}]`), `gold?` (copper), `wait_ms?` (default 12000), `open_wait_ms?` (default 8000) | `{result completed\|cancelled\|timeout\|too_far\|busy\|rejected\|..., partner?, gave[], gold?, received[], partner_accepted, verified, detail?}`. Partner must be within 10 yd; `trade needs "to": a player's name or guid`. |
| `vendor_list` | `guid*` | `{since, item?, event?}`. |
| `buy` | `guid*`, `item_entry*`, `count?` (≤ 255), `bag?`+`slot?` | Same envelope; `vendor_buy`/`vendor_buy_failed` events carry the outcome. |
| `sell` | `guid*` + (`item_entry`\|`bag`+`slot`), `count?` | Same envelope. One sale at a time. |
| `trainer_list` | `guid*` | Same envelope. |
| `train` | `guid*`, `spell_id*` | Same envelope; `trainer_trained`/`trainer_failed` carry the outcome. |
| `quest_hello` | `guid*` | `{since, event?}` with the first gossip/quest event. |
| `quest_query` | `guid*`, `quest_id*` | Same envelope (`quest_details`). |
| `quest_accept` | `guid*`, `quest_id*` | Same envelope. `quest <n> is already in the quest log`. |
| `quest_complete` | `guid*`, `quest_id*` | Same envelope (`quest_request_items`, then the offer). |
| `quest_reward` | `guid*`, `quest_id*`, `choice?` | Same envelope. Out-of-range choices are rejected. |
| `quest_abandon` | `quest_id*` | Same envelope. `quest <n> is not in the quest log (see quest_log)`. |
| `quest_log` | `{}` | `{quests[]}`. Never fails. |
| `gossip_hello` | `guid*`, `option?`, `code?`, `wait_ms?` | `{since, event?}` (null on timeout when `wait_ms` ≤ 0). |
| `gossip_select` | same (`option` required) | Same envelope. `gossip: no menu is open; call gossip_hello first`. |
| `join_channel` | `name*`, `password?` | `{}`. Answers arrive as `channel_notify` events. |
| `leave_channel` | same | `{}`. |
| `pet` | `{}` | `{present, guid?, entry?, name?, dead, health, ..., spells?[]}`. Never fails. |
| `pet_action` | `action*` (`attack follow stay passive defensive aggressive cast autocast_on autocast_off`), `target?`, `slot?`, `spell_id?`, `name?` | Pet info after the order. `the player has no pet`; `pet_action: the pet is dead`; unknown actions list the valid ones. |
