---
type: Guide
title: Talking, trading and the world
description: Chat, whispers, group control, spells, items, loot, vendors, trainers, quests and pets.
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
    title: addon_send, tbm, channels, gossip
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

Roll with `roll '{"choice": "need"}'` plus the `roll_id` or `item_slot` from the `loot_roll_start` event; unknown choices are refused (`unknown roll choice "<c>": use need, greed or pass`).

## 6. Use items, chests and mounts

```sh
turtle-agent rpc <walk-dir> use_item '{"item_entry": 1234}'
turtle-agent rpc <walk-dir> open_object '{"guid": "0x..."}'
turtle-agent rpc <walk-dir> mount '{"spell_id": 123}'
```

`use_item` takes an entry or a bag slot. `open_object` casts the Opening spells on a chest or node and optionally takes (`take: true`). `mount` takes an item or a `spell_id`; `dismount` reverses it. An item that teaches instead of mounting tells you so:

`not mounted: the item taught spell <n>, which is not a mount spell, so the agent did not cast it`

## 7. Trade with a nearby player

```sh
turtle-agent rpc <walk-dir> trade '{"to": "<name>", "items": [{"item": 1234, "count": 2}]}'
```

`to` is a name in view or a guid; `gold` is copper. The partner must be within 10 yards and alive; the window opens within 8 seconds and the partner accepts within 12. The result names the outcome (`completed`, `cancelled`, `timeout`, `too_far`, `busy`, `rejected`, `no_target`).

## 8. Talk to NPCs: gossip, vendor, trainer, quests

```sh
turtle-agent rpc <walk-dir> gossip_hello '{"guid": "0x..."}'
turtle-agent rpc <walk-dir> vendor_list '{"guid": "0x..."}'
turtle-agent rpc <walk-dir> buy '{"guid": "0x...", "item_entry": 1234}'
turtle-agent rpc <walk-dir> quest_hello '{"guid": "0x..."}'
turtle-agent rpc <walk-dir> quest_accept '{"guid": "0x...", "quest_id": 123}'
```

`gossip_hello` opens the menu; `gossip_select` picks `option` (with `code` when the option asks). `buy` takes `count` (at most 255) and an optional bag slot; `sell` takes an entry or a slot. `train` teaches a `spell_id`. Quest calls return `since` plus the first matching event (`quest_details`, `quest_request_items`, `quest_offer_reward`, ...): read on from `since` with `events`. Without an open menu:

`gossip: no menu is open; call gossip_hello first`

## 9. Read updates from game objects

```sh
turtle-agent rpc <walk-dir> gameobjects '{"within": 30}'
turtle-agent rpc <walk-dir> use_object '{"guid": "0x..."}'
```

`gameobjects` filters by `within`/`radius`, `entry`, `type` (name or number) and `name`. `use_object` uses the object and waits up to 3 seconds (cap 25) for the first answer event (`loot_open`, `gossip`, `quest_list`, ...); out-of-sight guids are refused (`game object 0x... is not in view`).

## 10. Command the pet

```sh
turtle-agent rpc <walk-dir> pet
turtle-agent rpc <walk-dir> pet_action '{"action": "attack"}'
```

`action` is `attack`, `follow`, `stay`, `passive`, `defensive`, `aggressive`, `cast`, `autocast_on` or `autocast_off`. Attack without a target needs a selection:

`pet_action attack: no target (give target or select one)`
