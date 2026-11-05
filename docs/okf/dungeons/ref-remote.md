---
type: Reference
title: Remote-control calls
description: commander, bot_view, tbm, tune, and lfg calls with every field and default.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/commander_methods.go
tags: [dungeon, commander, reference, rpc]
sources:
  - id: commander-methods
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/commander_methods.go
    title: commander and bot_view RPC
  - id: tbm-click
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/tbm_click.go
    title: tbm_buttons, tbm_click, tbm_send RPC
  - id: tune-methods
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/tune_methods.go
    title: tune_set and tune_list RPC
  - id: lfg-methods
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/lfg_methods.go
    title: The lfg RPC methods
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T22:00:00+01:00
status: draft
---

# Remote-control calls

Drive the TortoiseBots commander, the addon buttons, live tuning, and the group finder by hand. All calls go through `turtle-agent rpc <dir> <method> '<json>'`.

## commander

One call sends one `.bot` line and waits for its `TBM:ACTION_ACK`/`TBM:ACTION_ERR` reply. The answer carries `sent` (the `.bot` line) and `reply` (`kind` `ack`, `err`, or `timeout`; `ack`/`err` parsed; `lines` kept). The long-running orders `loot`, `open`, `use`, and `ritual` wait through `ACTION_PENDING` for the final line and add `order` (`pending`, `loot`, `opened`, `ritual`, `spell`, `participants`, `waited_ns`).

| Field | Default | What it does | Example |
|---|---|---|---|
| `op` | required | `bind`, `unbind`, `status`, `view`, `cmd`, or `run` | `{"op":"status"}` |
| `lead_bot` | required for `bind` | The lead bot | `{"op":"bind","lead_bot":"Tanky"}` |
| `bots` | none | The other bots for `bind` | `{"op":"bind","lead_bot":"T","bots":["A"]}` |
| `on` | required for `view` | `true` turns the `TBM:VIEW` feed on, `false` off | `{"op":"view","on":true}` |
| `verb` | required for `cmd`/`run` | `cmd`: `attack`, `pull`, `pullback`, `cc`, `focus`, `move`, `stay`, `follow`, `rest`, `revive`, `summon`, `enter`, `loot`, `open`, `use`, `ritual`, `lootmethod`, `state`, `gossip`. `run`: `start`, `pause`, `resume`, `skip`, `stop`, `status`, `list` | `{"op":"cmd","verb":"stay"}` |
| `args` | none | Verb arguments (a GUID is `0x` + hex) | `{"op":"cmd","verb":"move","args":["1.0","2.0","3.0"]}` |
| `wait_ms` | 4000 | How long to wait for the reply | `{"op":"status","wait_ms":8000}` |

```sh
turtle-agent rpc <dir> commander '{"op":"bind","lead_bot":"Tanky","bots":["A","B","C","D"]}'
turtle-agent rpc <dir> commander '{"op":"view","on":true}'
turtle-agent rpc <dir> commander '{"op":"cmd","verb":"stay"}'
```

## bot_view

Reads the latest `TBM:VIEW` frame (`have`, `n`, `age_ms`, `frame` with `seq`, `lead`, `party`, `hostiles`, `threats`; `stats`; `binding_ends`). Needs `commander view on` first.

| Field | Default | What it does | Example |
|---|---|---|---|
| `after_n` | 0 | Wait for a frame newer than this counter (the `n` of the last answer) | `{"after_n":41,"wait_ms":5000}` |
| `wait_ms` | 0 (return what there is) | How long to wait for the next frame | `{"wait_ms":5000}` |
| `log_every` | unchanged | Debug: log every nth frame's lines too (0–10000; 0 turns it off) | `{"log_every":10}` |

## tbm, tbm_handshake, tbm_buttons, tbm_click, tbm_send

`tbm` sends a raw `.bot` command (`command`, without `.bot`; `via` is `chat` by default or `addon`). `tbm_handshake` sends what the addon sends on login (`.bot help`, `.bot roster`) and collects the module's reply (`via`, `wait_ms` default 5000).

`tbm_buttons` lists the addon buttons (`tab`: Fight, Camp, Party, Roster, Guild, Raid, BotPanel; default all), with the pinned addon commit and the send/poll/timeout delays. `tbm_click` presses one like the addon does:

| Field | Default | What it does | Example |
|---|---|---|---|
| `button` | required | Button name (`Fight.AoE`) | `{"button":"Fight.AoE"}` |
| `args` | none | Button arguments (`state`, `bot`, `name`, `mark`, `seconds`, `bag`, `slot`, ...) | `{"button":"Roster.Summon","args":{"name":"A"}}` |
| `transport` | `auto` | `auto`, `chat`, or `addon` | `{"transport":"chat"}` |
| `wait_ms` | 8000 | How long to wait for the reply | `{"wait_ms":8000}` |
| `force` | `false` | Send even where the addon greys the button out | `{"force":true}` |
| `follow_up` | per button | Poll the roster after the click as the addon does | `{"follow_up":false}` |

`tbm_send` is the same transport rule for any command text (`command`, with or without `.bot`).

## tune_set, tune_list

Commander runs only. `tune_set` takes `key` (required) and `value` (a number, or `"default"` to put the key back); a value outside the key's limits is refused. `tune_list` takes nothing and shows every key with its limits and current value. See [Tune a live run](/dungeons/live-tuning.md).

## lfg_join, lfg_role_check_answer, lfg_proposal_answer, lfg_leave, lfg_status, lfg_auto, lfg_listings, lfg_listing_role

The group-finder calls; see [Find a group](/dungeons/lft.md). `wait_ms` defaults to 5000 everywhere. `lfg_auto`'s `delay_ms` defaults to 1500 (0–30000); `accept` omitted leaves offers alone; `off` stops the answerer. Every call answers `since`, `sent`, and the folded `state` (`lfg_auto` adds `auto`).
