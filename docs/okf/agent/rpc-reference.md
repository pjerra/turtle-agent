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
    title: addon_send, tbm, channels, gossip, taxi
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

Envelope: every call POSTs one JSON-RPC 2.0 object (`{"jsonrpc": "2.0", "id": <n>, "method": "<m>", "params": {...}}`) and gets one back (`{"jsonrpc": "2.0", "id": <same>, "result": ...}` or `"error": {"code", "message", "data?"}`). Arrays (batches) are rejected as `Invalid Request`. Params decode into a struct with unknown fields rejected, so a misspelled field is a `-32602`, not a silent default.

Error codes: `-32700` parse, `-32600` invalid request, `-32601` method not found, `-32602` invalid params (also: unknown fields are rejected), `-32000` internal, `-32001` not in world, `-32002` disconnected, `-32003` game refusal, `-32004` timeout. `logout`'s `lost`/`timeout` errors carry `data.outcome`. Unless noted, in-world-only calls answer `-32001` (`not in world (state <state>)`) outside the world.

`{}` means no params and an empty-object result. `wait_ms` defaults to a per-method wait and caps at 25000 unless noted.

## Session and observation

| Method | Params | Result |
|---|---|---|
| `status` | `{}` | `{state, character?, team?, guid?, level?, map, x, y, z, o, target?, health, max_health, power, max_power, falling, z_unknown, teleport_seq, latency_ms, unknown_opcodes, parse_errors, last_seq, mono_ms, base_time, wall_offset_ms, error?, combo_points, combo_target?, gcd_ms, dead, ghost}`. Never fails. `z_unknown` is true while `z` is a guess (GM teleport, climb, landing) until the server sends a position. `combo_points` is 0–5 from your own update fields; `combo_target` is absent without points. `gcd_ms` is the global cooldown left, 0 when none. |
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
| `gm_evade` | `guid*` (creature) | `{}`: `.npc evade` on the selected unit (leaves combat, walks home and resets). Refused outside the agent's test setup. |
| `gm_set_hp` | `guid*` (creature). No `hp`: an explicit one is refused. | `{}`: the session reads the unit's MaxHealth from the client and sends `.modify hp <max> <max>` (a full heal to its own max, never a lower maximum). A unit the client does not hold, or one with max 0, is refused with nothing sent. |
| `gm_respawn` | `guid*` (creature) | `{}`: `.respawn` on the selected dead unit. Refused when the client shows the unit alive (the unselected form would respawn every dead creature in range). |
| `gm_revive` | `name*` (a character name) | `{}`: `.revive <name>`, full health. No selection. |
| `gm_aura` | `guid*` (a player), `spell_id*` (nonzero) | `{}`: select the player by guid, wait for the server to show that selection, and send `.aura <spell_id>` only while it stands (`.aura` acts on the selected unit, and below developer level the core puts it on the sender). Refused outside the agent's test setup; a creature guid or spell id 0 is refused with nothing sent. `raid-walk --prepull-aura` is its caller. |
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
| `cast` | `spell_id*`, `target?` (0: current target, else self), `trade_slot?` (0-6) | `{}`. `cast needs a spell id`. `trade_slot` casts at the trade partner's item in that slot of an open trade window instead (`CMSG_CAST_SPELL` with `TARGET_FLAG_TRADE_ITEM`, `FA 1C 00 00 00 10 01 06` for spell 7418 on slot 6): an enchant goes on slot 6, the slot that is not traded, and the core applies it when the trade completes. `trade_slot` with a `target` is invalid params (`cast takes a target or a trade_slot, not both`), as is a slot above 6; with no open window: `no trade window is open: trade_open or trade_expect first` (a one-shot `trade` window is refused too). A `trade_slot` cast is refused with `trade: another trade step is in flight` while a `trade_set_item` or `trade_accept` runs. Slots 0-5 are sent too (the core answers `ITEM_NOT_READY`). See [social-guide](social-guide.md) §7. |
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
| `equipment` | none | Worn items as rows `{slot, slot_name, guid, item_entry, temp_enchant?}`, occupied slots 0-18 only, in slot order (`head neck shoulders body chest waist legs feet wrists hands finger1 finger2 trinket1 trinket2 back main_hand off_hand ranged tabard`). `[]` (never null) when nothing is worn. `item_entry` is 0 until the item's create block arrives, as in `inventory`. Never fails. |
| `equip` | `item_entry?`, `bag?`, `slot?` | The sent item. Refusals arrive as `inventory_error` events. |
| `use_item` | `item_entry?`, `bag?`, `slot?`, `target?` (0: self) | The used item as it stood when sent. Needs an entry or a slot: `use_item needs an item_entry or a slot (bag defaults to the backpack, 255)`. An entry searches the backpack, then the bags. `target` is a unit only (an item or object guid is not a target); 0 uses it on yourself, which is also how the client uses an item with no target selected. A clam or lockbox does not open this way: `CMSG_OPEN_ITEM` is not covered. Answers arrive as events: `spell_start`, `aura_applied`, `stand_state` (eating sits), or `inventory_error` / `spell_fail` on refusal. |
| `loot` | `guid?`, `name?`, `wait_ms?` (default 3000) | The loot window, or null. `loot needs a guid or a name`; without a guid the call takes the nearest dead lootable unit (`UNIT_DYNFLAG_LOOTABLE`) whose name contains `name`: `no lootable dead unit named "<n>" nearby`. A refused window names its reason (`loot refused: <text> (<reason>)`, e.g. `too_far`, `didnt_kill`, `locked`, `not_standing`) plus a `loot_error` event. |
| `loot_take` | `slot?`, `all?`, `money?`, `wait_ms?` | `{taken[], left?, skipped?, money?, window?}`. `loot_take needs a slot, all, or money`; `no loot window is open; call loot first`. Each row carries `slot_type`: only `allow_loot` and `owner` can be taken; a running roll (`roll_ongoing`), the master looter's rows (`master`) and `locked` rows are refused alone and land in `skipped` with `all: true`. `left` holds requested slots still in the window when the wait ended (a full bag refuses with an `inventory_error` event). |
| `loot_close` | `wait_ms?` | `{}`. |
| `open_object` | `guid*`, `spell_id?` (default tries the Opening spells), `take?`, `wait_ms?` (default 15000) | `{window, taken[], money?, left?}`. Default (`spell_id` 0) tries the book's Opening spells in order `[6477, 3365, 6247, 6478, 22810]`: only spells the book holds are tried. Spell 21651 ("Opening" on some lists) is never auto-tried; pass `"spell_id": 21651` explicitly and it is the only spell tried, and it must be in the book. `bad_targets`/`low_castlevel` refusals move to the next candidate; other failures end the call with the `SpellCastResult` name. A spell given but not in the book is refused at once: `spell <n> is not in the spell book: the core drops a cast of it without an answer`. The call needs a chest or node in view: another type is refused before anything is sent. |
| `mount` | `item_entry?`/`bag?`+`slot?` xor `spell_id?`, `wait_ms?` (default 12000) | `{mounted, display_id?, via?, result?, learned_spell?}`. `mount needs an item_entry (or bag and slot), or a spell_id`; `already mounted`. A spell casts on yourself; an item uses itself. The call waits for `UNIT_FIELD_MOUNTDISPLAYID`; refusals (indoors, in combat, too low a level) arrive as `spell_fail`/`inventory_error`/`mount_result` events. A bridle teaches instead of mounting: after the learn settles, the agent casts the taught spell when it is a mount spell, else the call fails naming `learned_spell`. |
| `dismount` | `spell_id?`, `wait_ms?` (default 3000) | Mount info. `not mounted`. |
| `gameobjects` | `within?`/`radius?`, `entry?`, `type?` (name or 0–30), `name?`, `limit?` | Game-object rows, nearest first (`{guid, entry, name, type, type_name, x, y, z, o, distance, distance_2d, state, flags, dyn_flags, locked, usable, display_id, created_by?}`). `name` is empty until the `SMSG_GAMEOBJECT_QUERY_RESPONSE` arrives. `state` is `ready` (door closed), `active` (open or used) or `active_alt`. `distance` is -1 for objects whose position the server never sent (ships, elevators on a transport path). `usable` is a hint from the update fields (false for generic, no-interact, in-use or interact-condition objects); reach, key, quest and faction are checked by the server alone. `locked` is `GO_FLAG_LOCKED`. Unknown type names are rejected. |
| `use_object` | `guid*`, `wait_ms?` (default 3000, cap 25000) | `{since, object, outcome, event?, hint?}`. `use_object needs a guid`; `game object 0x... is not in view`. Sends `CMSG_GAMEOBJ_USE` and waits for the first answer event: `loot_open`, `loot_error`, `gossip`, `gossip_complete`, `quest_list`, `quest_details`, `quest_request_items`, `quest_offer_reward`, `vendor_list`, `trainer_list`, `gameobject`, `spell_start` or `spell_go`. `outcome` is the answering event kind, or `none`. The core answers nothing when the object is out of reach, generic, flagged no-interact, or refuses you; then `hint` names the likely reason (distance over 5 yd, unusable flags, locked). A chest does not answer a use: `open_object` is the call for chests and nodes. |
| `trade` | `to*` (name in view or guid), `items?` (`[{item, count?}]`), `gold?` (copper), `wait_ms?` (default 12000), `open_wait_ms?` (default 8000) | `{result completed\|cancelled\|timeout\|too_far\|busy\|rejected\|..., partner?, gave[], gold?, received[], partner_accepted, verified, detail?}`. `trade needs items or gold to hand over`; `trade needs "to": a player's name or guid`. The core allows 11.11 yd; the call keeps a yard in hand, so the partner must be within 10 yd. `verified` is true when a completed trade shows in your bags and purse afterwards. Whole stacks go in (the wire has no count): a part count splits the stack first, and the leftover stays in the bags when the trade fails. Accepts wait out the 1.1 s settle delay; both sides changing the window re-accepts up to 4 times. See [Talking, trading and the world](social-guide.md) §7 for the flow, timing and result checks. |
| `trade_open` | `to*` (name in view or guid), `open_wait_ms?` (default 8000) | Step trade state (see `trade_status`) once the window is open; the window then **stays open** until `trade_accept` completes it or `trade_cancel`. Same checks as `trade` (within 10 yd, alive). A refused or unanswered request is a `TradeFailed` error carrying the result and the trade is cancelled. |
| `trade_expect` | `from*` (name or guid), `wait_ms?` (default 15000) | Step trade state once the window is open. Arms the accept for one incoming request from that player: the agent answers it with `CMSG_BEGIN_TRADE` and the `trade_status` event carries `accepted: true`. Every other incoming request is still declined, and the arm ends when the wait does (`no trade request from <who> within <wait>`). The name must be the **full** name (any case): a name in view is resolved to its guid when armed, a part of a name that fits a player in view is an error (`trade: no player named "Gor" in view (trade_expect needs the full name; ...)`), and a name nobody in view has is matched on the cached name when the request arrives. Refused while another trade is running or an earlier `trade_expect` is still waiting (`trade: a trade_expect is waiting for a request (trade_cancel ends it)`); `trade_open` and `trade` are refused the same way while an arm is live. |
| `trade_set_item` | `trade_slot*` (0-6; 6 is the slot that is not traded), `item_entry?`, `bag?`, `slot?` | `{trade_slot, bag, slot, guid, item_entry, unchanged?}`. Returns on the first `back_to_trade` that arrives after the agent's own send, within 3 s: the core answers an effective set with it (`TradeData::SetItem` calls `SetAccepted(false)`). It has no sender, so a partner's change landing in the same instant can be taken for the answer. The core ignores a set of the item already in the slot without a reply, so a slot this agent itself last set to that item (entry, bag, slot, count, guid; the record ends with the trade, a cancel or a close) answers `unchanged: true` and sends nothing. Only one `trade_set_item`, `trade_accept` or `cast` with `trade_slot` runs at a time per trade: a second call made while one is in flight returns at once with `trade: another trade step is in flight` (no queueing; `trade_status` and `trade_cancel` stay allowed, and a cancel ends the call in flight with result `cancelled`). Invalid params without a `trade_slot` 0-6; `trade_set_item needs an item_entry or a slot (bag defaults to the backpack, 255)`; `no trade window is open: trade_open or trade_expect first`. A refused item (soulbound) closes the window: a `TradeFailed` error with result `cancelled`. |
| `trade_accept` | `wait_ms?` (default 12000) | `trade` result shape (`result completed\|changed\|cancelled\|timeout\|...`, `partner`, `received[]`, `partner_accepted`; no `gave`/`verified`). Accepts once after the 1.1 s settle and does **not** accept again by itself. `changed`: a `back_to_trade` came after the accept (the partner or a cast changed the window; the core un-accepts both sides); the result carries the new window in `trade_status` so you see what changed, then call `trade_accept` again or `trade_cancel`. `timeout` (error carrying the result): the partner did not accept in time; the agent withdraws its accept (`CMSG_UNACCEPT_TRADE`) and the window stays open. Refused with `trade: another trade step is in flight` while a `trade_set_item` or another `trade_accept` runs. `received` is a snapshot of the partner's window, not proof of delivery: an item can stay with the partner (a slot 6 item is never traded, so it shows in `received` while it stays in the partner's bags). |
| `trade_cancel` | none | `{status}`, the last trade status by name. Sends `CMSG_CANCEL_TRADE` for an open step trade and drops an armed accept; safe to call twice. |
| `trade_status` | none | `{active, step, open, partner?, partner_name?, status?, their_items[], their_gold, their_spell?, my_items[], my_gold, my_spell?, their_accept, armed_from?}`. Both sides of the window from `SMSG_TRADE_STATUS_EXTENDED`; `my_spell` is the enchant your own trade-slot cast has pending. The core sends your own side only when a spell is set on it (not after `trade_set_item`), so `my_items` and `my_gold` can lag what you set. Never fails. |
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
| `taxi` | `guid*` (flight master), `nodes*` (taxi node ids, source first), `wait_ms?` | `{since, event?}` (event null on timeout or without `wait_ms`), in the same style as the gossip rows. Two nodes send a direct flight request, more send a multi-hop one, as the client does. `taxi: need at least a source and a destination node`; `taxi: too many nodes (max 64)`. Most refusals arrive as that event; a flight master out of reach, an unknown node or disabled movement gets no reply (event null). One request at a time per session: a second call within 10 s of an unanswered one returns `taxi: previous request still unanswered`. A taxi_reply has no request id, so a reply from another taxi start (e.g. a spell) can also match. |
| `join_channel` | `name*`, `password?` | `{}`. Answers arrive as `channel_notify` events. |
| `leave_channel` | same | `{}`. |
| `pet` | `{}` | `{present, guid?, entry?, name?, level?, dead, health, max_health, power_type, power, max_power, happiness?, happiness_state?, loyalty?, in_combat, target?, distance, moving, bar, react?, command?, enabled, spells?[], refused?}`. Never fails. `power_type` is 0 mana (a demon) or 2 focus (a hunter pet). `happiness`/`happiness_state`/`loyalty` are set for a hunter pet only (demons read 0). `react` is `passive`, `defensive` or `aggressive`; `command` is `stay` or `follow`. `spells[]` rows are `{spell_id, name?, slot, autocast, passive?}`; `slot` is the bar button 3–6, -1 when the spell is known but not on the bar. `refused` is the last `SMSG_PET_CAST_FAILED` reason, kept until the next summon. |
| `pet_action` | `action*` (`attack follow stay passive defensive aggressive cast autocast_on autocast_off`), `target?`, `slot?` (bar button), `spell_id?`, `name?` (highest rank on the bar wins) | Pet info (same shape as `pet`) after the order. `attack` and `cast` fall back to your current target when `target` is 0. `cast` needs the spell on the bar and refuses passive spells; `autocast_on`/`autocast_off` refuse passive spells too. Errors: `the player has no pet`; `pet_action: the pet is dead`; `pet_action: the pet's action bar is off` (while mounted); `pet_action attack: no target (give target or select one)`; `pet_action: unknown action "<a>" (attack, follow, stay, passive, defensive, aggressive, cast, autocast_on, autocast_off)`; `pet_action: the pet has no such spell (slot, spell_id or name) on its bar; it has <list>`. Server refusals of a bad target or unreachable target arrive as `pet_error` events. |
