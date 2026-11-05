---
type: Reference
title: Addons, LFG and tuning RPC
description: tbm and addon calls, Eluna tt, LFG queue, and live tuning params and results.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/lfg_methods.go
tags: [agent, rpc, reference, addon, lfg, tune]
sources:
  - id: lfg-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/lfg_methods.go
    title: LFG methods and params
  - id: social-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/social_methods.go
    title: addon_send, tbm, tbm_handshake, gossip
  - id: addon-host-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/addon_host.go
    title: addon_start, addon_stop, addon_state, addon_tick, addon_click
  - id: tbm-click-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/tbm_click.go
    title: tbm_buttons, tbm_click, tbm_send
  - id: eluna-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/eluna_methods.go
    title: tt
  - id: tune-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/tune_methods.go
    title: tune_set and tune_list
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T22:45:00+01:00
status: draft
---

# Addons, LFG and tuning RPC

## Talking to the bots module

| Method | Params | Result |
|---|---|---|
| `tbm` | `command*` (without `.bot`), `via?` (`chat` default, or `addon`) | `{since}`. `via "<v>": use chat or addon`. |
| `tbm_handshake` | `via?`, `wait_ms?` (default 5000) | `{since, next, version, caps, transport, roster, bot_state, errors, lines, complete}`. |
| `tbm_send` | `command*`, `transport?` (`auto` default, `chat`, `addon`), `wait_ms?` (default 8000) | Click-shaped result with `sent` and the parsed `reply`. |
| `tbm_buttons` | `tab?` | `{addon_commit, send_delay_ms, poll_after_cmd_ms, action_timeout_ms, buttons[]}`. |
| `tbm_click` | `button*`, `args?`, `transport?` (`auto`/`chat`/`addon`), `wait_ms?` (default 8000), `force?`, `follow_up?` | `{clicked[], sent{transport, text, ...}, reply{kind ack\|err\|text\|inventory\|snapshot\|timeout, ...}, timed_out?, state}`. The addon greys some buttons out until their condition holds; `force` sends anyway. |
| `addon_send` | `prefix*`, `text*`, `channel*` (`party raid guild officer raid_leader raid_warning battleground battleground_leader channel`), `to?` (channel name) | `{}`. |
| `tt` | `cmd*` (without `.tt`; one line, no `#`, ≤ 200 chars) | `{replies[]}`. Toolkit refusal is a `-32003` with `data{err, replies}`; timeout is `-32004` with `data{replies}`. |

## Driving the in-game addon

| Method | Params | Result |
|---|---|---|
| `addon_start` | `clock?` (`real` default; `manual`: only `addon_tick` advances it) | `{dir, version, files[], clock, sent[]}`. Needs the addon dir set; `the addon host is already running (addon_stop first)`. |
| `addon_stop` | `{}` | `{}`. |
| `addon_state` | `{}` | Raw JSON of the addon state. |
| `addon_tick` | `ms*` (1–120000) | `{clock, sent[]}`. |
| `addon_click` | `button?` or `tab`+`label`, `bot?`, `name?`/`names?`, `mark?`, `row?`, `view?`, `bag?`+`slot?`, `before?`, `force?`, `wait_ms?` (default 8000) | `{clicked[], sent[], reply[], timed_out?, state}`. |

## LFG

`roles` letters are `t` (tank), `h` (healer), `d` (damage). Every call answers `{since, sent, state{phase idle|rolecheck|queued|offer|grouped|left|error, instances, responses, offer, counts, listings, history}}` — read on from `since` with `events`. The guild channel prefix is `TW_LFG`.

`wait_ms` defaults to 5000 and caps at 25000 on every LFG call. A wrong-phase answer needs `force: true`:

`phase is <now>, not <want>: cannot <action> (force overrides)`

| Method | Params | Waits for |
|---|---|---|
| `lfg_join` | `instances*` (1–10 lowercase names), `roles*`, `force?`, `wait_ms?` | `S2C_ROLECHECK_START`, `S2C_QUEUE_ERROR` or `S2C_QUEUE_JOINED`. |
| `lfg_role_check_answer` | `roles*`, `force?`, `wait_ms?` | `S2C_QUEUE_JOINED`, or a `S2C_ROLECHECK_INFO` row naming you. |
| `lfg_proposal_answer` | `accept*`, `force?`, `wait_ms?` | `S2C_OFFER_UPDATE_COUNT`, `S2C_OFFER_COMPLETE` or `S2C_QUEUE_LEFT`. There is no decline message: `accept: false` leaves the queue. |
| `lfg_leave` | `wait_ms?` | `S2C_QUEUE_LEFT`. |
| `lfg_status` | `refresh?`, `wait_ms?` | Nothing unless `refresh` is true; then `S2C_UPDATE_QUEUE_STATUS`. An idle player gets no answer at all from the core. |
| `lfg_listings` | `refresh?`, `id?`, `wait_ms?` | With `refresh` and no `id`: every `S2C_GROUPS_LIST_UPDATE` line until the `end` marker. With `id`: `S2C_UPDATE_GROUP` lines for that id until its `end`. Without either: no send, only the folded state. |
| `lfg_listing_role` | `role*` (`t`, `h` or `d`), `wait_ms?` | `S2C_GROUPS_LIST_UPDATE`. Confirms the role on the party channel (`C2C_ROLECHECK_RESPONSE`), not the guild one; empty or anything but `t`/`h`/`d` sends `0` and removes the row. |
| `lfg_auto` | `roles?`, `accept?`, `delay_ms?` (default 1500, 0–30000), `off?`. Answers `{..., auto{on, roles?, accept?, delay_ms}}`. | Nothing: starts or stops the background answerer. |

Join names 1–10 instances of 1–32 lowercase letters or digits, deduplicated in order; `roles` is normalised to the `t,h,d` subset in that order. The join text after the prefix caps at 248 bytes (255 with the prefix, the chat limit). Role checks and offers stay open 90 s on the core side.

Classes gate their roles before anything is sent: `roles "<r>" are not allowed for a <class> (allowed: <...>)`. An empty role mask is a param error (`roles "<r>" name none of t, h, d`); the core itself silently ignores a zero mask, which would only time the check out. A class the agent has not seen yet answers `the character's class is not known yet`. Read your class id from your own `units` row (byte 1 of `UNIT_FIELD_BYTES_0`).

Spoof guard: the core answers as yourself (`Player::SendAddonMessage` from your own GUID on the guild channel), so only lines from your own GUID fold into `state`. A guild-mate's forged `S2C_*` line carries his GUID and is ignored.

`state` fields beyond the phase: `rolecheck_since`/`rolecheck_deadline` and `offer_since`/`offer_deadline` bound the 90 s windows; `responses` maps each answerer's name to roles; `offer` is `{instance, role, counts}`; `queued_since` and `counts[t,h,d]` track the queue; `last_error` keeps the latest `S2C_QUEUE_ERROR` reason; `listings` maps each id to its detail rows once fetched; `history` keeps the last 50 folded lines (`auto:`-prefixed lines are the auto answerer's own sends, kept for the view only).

`lfg_auto delay_ms` must sit in 0–30000 (`delay_ms <n> is outside 0-30000` otherwise) and needs non-empty `roles`. A reconnect or logout stops it; `off: true` stops it at once. `lfg_status` without `refresh` and `lfg_listings` without `refresh` or `id` send nothing and only report the folded state.

Wire shape: every line is `TW_LFG\t<body>` on the guild channel (answers arrive as guild chat from yourself). Bodies are `;`-separated with empty fields kept; listing and signup payloads are JSON and may hold `;` themselves, so only the leading fields split. Unknown `S2C_*` kinds fold into `history` only.

## Live tuning

| Method | Params | Result |
|---|---|---|
| `tune_list` | `{}` | `{tunables[{key, unit, min, max, default, base, value, source, queued?, doc}]}`. |
| `tune_set` | `key*`, `value*` (number, `"default"` or null to reset) | `{key, old, new, applies: "at the next leg start or pull-group start"}`. |

Both need a running dungeon run in commander (`remote`) mode: `no dungeon run is going`; ``live tuning is for runs in mode "remote"``.
