---
type: Guide
title: Talking, trading and the world
description: Chat, whispers, group control, spells, items, loot, vendors, trainers, quests, flights and pets.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/methods.go
tags: [agent, chat, group, trade, spells, loot, quests, pets]
sources:
  - id: methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/methods.go
    title: chat, gm, group basics, cast, loot, roll
  - id: group-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/group_methods.go
    title: promote, assistant, convert_raid, raid_move, loot_method, disband
  - id: social-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/social_methods.go
    title: addon_send, tbm, channels, gossip, taxi
  - id: trade-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/trade_methods.go
    title: trade params and result
  - id: npc-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/npc_methods.go
    title: vendor, trainer, quest methods
  - id: item-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/item_methods.go
    title: use_item, loot, open_object, mount
  - id: pet-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/pet_methods.go
    title: pet and pet_action
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T22:45:00+01:00
status: draft
---

# Talking, trading and the world

## 1. Speak and whisper

```sh
turtle-agent rpc <walk-dir> chat '{"type": "say", "text": "ready"}'
turtle-agent rpc <walk-dir> chat '{"type": "whisper", "to": "<name>", "text": "go"}'
```

`type` is `say`, `party`, `yell`, `emote`, `guild`, `raid`, `whisper` or `channel` (channel needs `to`). A whisper needs its target:

`chat: a whisper needs a target name`

Refusals (`chat: empty message`, `chat: message is <n> bytes; the client limit is 255`) arrive as `-32003`.

## 2. Run GM commands (GM account only)

```sh
turtle-agent rpc <walk-dir> chat '{"type": "say", "text": ".tele name <name>"}'
turtle-agent rpc <walk-dir> gm '{"text": ".gm visible on"}'
```

`gm` adds the dot when missing. `gm_kill` takes a creature guid.

## 3. Manage the group

```sh
turtle-agent rpc <walk-dir> invite '{"name": "<name>"}'
turtle-agent rpc <walk-dir> accept_invite
turtle-agent rpc <walk-dir> convert_raid
turtle-agent rpc <walk-dir> loot_method '{"method": "group_loot"}'
turtle-agent rpc <walk-dir> leave_group
```

`method` is `free_for_all`, `round_robin`, `master_loot`, `group_loot` or `need_before_greed`. `promote`, `assistant`, `convert_raid`, `raid_move` and `disband` need the leader (or an assistant for `raid_move`); in a battleground the core refuses some commands and the wait ends with:

`<cmd>: no SMSG_GROUP_LIST showing it within 2s: the server ignores a group command it refuses (...)`

## 4. Cast and fight

```sh
turtle-agent rpc <walk-dir> target '{"name": "<mob>"}'
turtle-agent rpc <walk-dir> cast '{"spell_id": 116}'
turtle-agent rpc <walk-dir> cast_state
```

`cast` aims at `target` (default: current target, else self). `cancel_cast` takes an optional `spell_id` (0 cancels whatever is casting). `spells` lists the spellbook with cooldowns and costs; `attack` needs a selection first:

`no target selected; call target first`

## 5. Loot and roll

```sh
turtle-agent rpc <walk-dir> loot '{"name": "<corpse>"}'
turtle-agent rpc <walk-dir> loot_take '{"all": true}'
turtle-agent rpc <walk-dir> loot_close
```

`loot` opens the window (empty params reuse the current one); `loot_take` needs `slot`, `all` or `money`:

`loot_take needs a slot, all, or money`

`no loot window is open; call loot first`

### Range and refusal reasons

By name the call takes the nearest dead unit with loot (`UNIT_DYNFLAG_LOOTABLE`) whose name contains your string. The core checks the reach itself and refuses with a reason you get both as the error and as a `loot_error` event: `didnt_kill` (no permission), `too_far` (too far away), `bad_facing`, `locked` (someone is already looting), `not_standing` (must be standing), plus `stunned`, `already_pickpocketed` and `not_while_shapeshifted`.

### Which rows you can take

Every window row carries `slot_type`: only `allow_loot` and `owner` go into your bags. A running roll (`roll_ongoing`), the master looter's rows (`master`) and `locked` rows refuse a single-slot take and land in `skipped` when you take with `all: true`. Roll for the open item with `roll '{"choice": "need"}'` plus the `roll_id` or `item_slot` from the `loot_roll_start` event; unknown choices are refused (`unknown roll choice "<c>": use need, greed or pass`).

### Taking, money, closing

One take sends `CMSG_AUTOSTORE_LOOT_ITEM` per slot and `CMSG_LOOT_MONEY` for the copper, then waits for the server to remove each. `taken` lists the slots now in your bags; `left` lists requested slots still in the window when the wait ended (a full bag refuses with an `inventory_error` event). The window closes by itself once empty; `loot_close` ends an open window early.

## 6. Use items, chests and mounts

```sh
turtle-agent rpc <walk-dir> use_item '{"item_entry": 1234}'
turtle-agent rpc <walk-dir> open_object '{"guid": "0x..."}'
turtle-agent rpc <walk-dir> mount '{"spell_id": 123}'
```

`use_item` takes an entry or a bag slot (`bag` defaults to the backpack, 255). An entry searches the backpack first, then the bags. `target` is a unit only: 0 uses the item on yourself, which is also how the client uses an item with no target selected. Unlike `cast`, there is no fallback to your current target, and item or object guids are not targets. A clam or lockbox does not open this way (`CMSG_OPEN_ITEM` is not covered). The server answers through events: `spell_start` and `spell_go`, a `Food`/`Drink` aura, `stand_state` `sit`/`stand` while eating, or `spell_fail` / `inventory_error` on refusal.

`open_object` casts the Opening spells on a chest or node and optionally takes (`take: true`). The default tries the spells your book holds, in order `[6477, 3365, 6247, 6478, 22810]`, 6477 first. Spell 21651 ("Opening" on some lists) is never auto-tried; pass `"spell_id": 21651` explicitly and it is the only spell tried, and it must be in your book. A spell named explicitly but missing from the book is refused at once; before your book arrives, 6477 goes unchecked. A `bad_targets` or `low_castlevel` refusal moves to the next candidate; any other failure ends the call with the `SpellCastResult` name. The object must be a chest or node in view; anything else is refused before anything is sent. Casting breaks on movement, so stand still.

`mount` takes an item or a `spell_id`; `dismount` reverses it (`not mounted` on foot). The call waits for `UNIT_FIELD_MOUNTDISPLAYID`; refusals (indoors, in combat, too low a level) arrive as `spell_fail`, `inventory_error` or `mount_result` events, and the result names `via` (`item` or `spell`). A bridle (item 46499) teaches instead of mounting: after the learn settles, the agent casts the taught spell when it is a mount spell. An item that teaches instead of mounting tells you so:

`not mounted: the item taught spell <n>, which is not a mount spell, so the agent did not cast it`

## 7. Trade with a nearby player

```sh
turtle-agent rpc <walk-dir> trade '{"to": "<name>", "items": [{"item": 1234, "count": 2}]}'
```

`to` is a name in view or a guid; `gold` is copper. The partner must be within 10 yards (the core allows 11.11; the call keeps a yard in hand) and alive; the window opens within 8 seconds and the partner accepts within 12. The result names the outcome (`completed`, `cancelled`, `timeout`, `too_far`, `busy`, `rejected`, `no_target`, plus `wrong_faction`, `ignored`, `you_dead`, `target_dead`, ...).

### Counts

Whole stacks go in: the wire has no count, so a part count splits the stack first (`CMSG_SPLIT_ITEM`) and the leftover stays in your bags when the trade fails. Ask for more than you hold and the call is refused before anything is sent; ask for more gold than your purse holds and it names both numbers.

### Timing

Changes settle before the accept: the call waits 1.1 s after its last change (the core counts the scam delay in whole seconds), and re-accepts up to 4 times when either side changes the window again. A trade that never completes is cancelled with `CMSG_CANCEL_TRADE`.

### Result

`verified` is true when a completed trade shows in your bags and purse afterwards (checked for ~2.2 s after the close). A failed trade returns a `TradeFailed` error carrying the result: how far it got, what the other side had shown, and `detail` saying why. Every status step also lands as a `trade_status` event.

## 8. Talk to NPCs: gossip, vendor, trainer, quests

```sh
turtle-agent rpc <walk-dir> gossip_hello '{"guid": "0x..."}'
turtle-agent rpc <walk-dir> vendor_list '{"guid": "0x..."}'
turtle-agent rpc <walk-dir> buy '{"guid": "0x...", "item_entry": 1234}'
turtle-agent rpc <walk-dir> quest_hello '{"guid": "0x..."}'
turtle-agent rpc <walk-dir> quest_accept '{"guid": "0x...", "quest_id": 123}'
```

`gossip_hello` opens the menu; `gossip_select` picks `option` (with `code` when the option asks). `taxi` asks a flight master for a flight: `guid` plus `nodes` (taxi node ids, source first — two nodes fly direct, more fly multi-hop); `wait_ms` waits for the `taxi_reply` event (`ok` when the flight was accepted). `buy` takes `count` (at most 255) and an optional bag slot; `sell` takes an entry or a slot, and one sale runs at a time (a second `sell` while the first waits is refused). `train` teaches a `spell_id`. Quest calls return `since` plus the first matching event (`quest_details`, `quest_request_items`, `quest_offer_reward`, ...): read on from `since` with `events`. Without an open menu:

`gossip: no menu is open; call gossip_hello first`

### Vendor and trainer answers

`vendor_list` answers with a `vendor_list` event (`items[]` of `{index, item_entry, display_id, stock, price, durability, buy_count}`; `stock` -1 means unlimited; `price` is copper after your reputation discount). A dead character gets no answer; out of reach of a vendor NPC the core sends a `vendor_sell_failed` refusal instead. `buy` answers with `vendor_buy` or `vendor_buy_failed` (`cant_find_item`, `item_already_sold`, `not_enough_money`, `seller_dont_like_you`, `too_far`, `item_sold_out`, `cant_carry_more`, `rank_required`, `reputation_required`); a full bag is an `inventory_error` event. The vendor need not have been listed first. `sell` answers with `vendor_sell` once the item leaves its slot, or `vendor_sell_failed` (`cant_find_item`, `cant_sell_item`, `cant_find_vendor`, `not_your_item`, `only_empty_bag`). A `count` of 0 sells the whole stack. `trainer_list` answers with a `trainer_list` event (nothing when the NPC is out of reach or trains nothing for you); `train` answers with `trainer_trained` or `trainer_failed`.

### Quest answers

`quest_hello` answers with a `gossip`, `quest_list`, `quest_details` or `quest_request_items` event (nothing when the NPC is out of reach). `quest_query` answers with `quest_details` (nothing for a quest the NPC neither gives nor takes). `quest_complete` answers with `quest_request_items`, or at once with `quest_offer_reward` when the quest asks for no items; when the items are in hand the session presses Complete for you so the offer follows. `quest_reward` takes `choice` 0–5 (leave it out when the offer has at most one) and answers with `quest_rewarded`. Out-of-range choices are rejected against the offer seen.

## 9. Read updates from game objects

```sh
turtle-agent rpc <walk-dir> gameobjects '{"within": 30}'
turtle-agent rpc <walk-dir> use_object '{"guid": "0x..."}'
```

`gameobjects` filters by `within`/`radius`, `entry`, `type` (name or number) and `name`, nearest first. Rows carry `state` (`ready` for a closed door, `active` for open or used, `active_alt`), `locked` (`GO_FLAG_LOCKED`), `usable` (a hint from the update fields; false for generic, no-interact, in-use or interact-condition objects — reach, key, quest and faction stay server-side), and `name` (empty until the query response arrives). `use_object` uses the object and waits up to 3 seconds (cap 25) for the first answer event (`loot_open`, `loot_error`, `gossip`, `gossip_complete`, `quest_list`, `quest_details`, `quest_request_items`, `quest_offer_reward`, `vendor_list`, `trainer_list`, `gameobject`, `spell_start`, `spell_go`); out-of-sight guids are refused (`game object 0x... is not in view`). No answer means the core ignored the use (out of reach, generic, no-interact, or refused you): `outcome` is `none` and `hint` names the likely reason (over 5 yd away, unusable flags, locked). A chest does not answer a use — `open_object` is the call for chests and nodes.

## 10. Command the pet

```sh
turtle-agent rpc <walk-dir> pet
turtle-agent rpc <walk-dir> pet_action '{"action": "attack"}'
```

`action` is `attack`, `follow`, `stay`, `passive`, `defensive`, `aggressive`, `cast`, `autocast_on` or `autocast_off`. `pet` never fails: without a pet only `present: false` comes back. `attack` and `cast` fall back to your current target when `target` is 0. `cast` names the spell by bar `slot` (3–6), `spell_id` or `name` (highest rank on the bar wins); the spell must sit on the bar and must not be passive. `autocast_on`/`autocast_off` switch the autocast of a bar spell the same way. `follow` also ends an attack; `passive` also stops the attack. `react` reads `passive`, `defensive` or `aggressive`; `command` reads `stay` or `follow`. Refusals of a bad target or an unreachable target arrive as `pet_error` events; the last refused cast stays in `pet.refused` until the next summon. Attack without a target needs a selection:

`pet_action attack: no target (give target or select one)`

Unknown actions list the valid ones; a dead pet, a missing pet and a bar switched off (mounted) each have their own refusal.
