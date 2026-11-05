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

`roles` letters are `t` (tank), `h` (healer), `d` (damage). Every call answers `{since, sent, state{phase idle\|rolecheck\|queued\|offer\|grouped\|left\|error, instances, responses, offer, counts, listings, history}}` — read on from `since` with `events`. The guild channel prefix is `TW_LFG`.

| Method | Params |
|---|---|
| `lfg_join` | `instances*` (1–10 lowercase names), `roles*`, `force?`, `wait_ms?` |
| `lfg_role_check_answer` | `roles*`, `force?`, `wait_ms?` |
| `lfg_proposal_answer` | `accept*`, `force?`, `wait_ms?` |
| `lfg_leave` | `wait_ms?` |
| `lfg_status` | `refresh?`, `wait_ms?` |
| `lfg_listings` | `refresh?`, `id?`, `wait_ms?` |
| `lfg_listing_role` | `role*` (`t`, `h` or `d`), `wait_ms?` |
| `lfg_auto` | `roles?`, `accept?`, `delay_ms?` (default 1500, 0–30000), `off?`. Answers `{..., auto{on, roles?, accept?, delay_ms}}`. |

Wrong-phase answers need `force: true`: `phase is <now>, not <want>: cannot <action> (force overrides)`. Classes gate their roles: `roles "<r>" are not allowed for a <class> (allowed: <...>)`.

## Live tuning

| Method | Params | Result |
|---|---|---|
| `tune_list` | `{}` | `{tunables[{key, unit, min, max, default, base, value, source, queued?, doc}]}`. |
| `tune_set` | `key*`, `value*` (number, `"default"` or null to reset) | `{key, old, new, applies: "at the next leg start or pull-group start"}`. |

Both need a running dungeon run in commander (`remote`) mode: `no dungeon run is going`; ``live tuning is for runs in mode "remote"``.
