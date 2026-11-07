---
type: Guide
title: Find a group
description: Queue for dungeons through Turtle's group finder with the lfg calls.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/lfg_methods.go
tags: [dungeon, lft, guide]
sources:
  - id: lfg-methods
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/lfg_methods.go
    title: The lfg RPC methods
  - id: lft-msg
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/lft/msg.go
    title: LFT message texts and parsing
  - id: lft-state
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/lft/state.go
    title: LFT folded state
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:45:00+01:00
status: draft
---

# Find a group

Turtle's group finder (LFT) is an addon conversation, not opcodes: your client writes `TW_LFG`-prefixed lines to the guild channel and the server answers on the same channel. The `lfg_*` calls send the lines and fold the answers into a state (`idle`, `rolecheck`, `queued`, `offer`, `grouped`, `left`, `error`). Every answer carries `since` (the event-log cursor before the send), `sent` (the text sent), and `state`.

## 1. Queue

```sh
turtle-agent rpc <dir> lfg_join '{"instances":["ragefirechasm"],"roles":"d"}'
```

`instances` holds 1–10 codes (lowercase letters and digits, e.g. `ragefirechasm`). `roles` is a subset of `t` (tank), `h` (healer), `d` (damage). What your class may queue as: druid, paladin, and shaman `thd`; warrior `td`; priest `hd`; hunter, mage, rogue, and warlock `d`. A role your class cannot play is refused unless `force` is true. Each call waits up to `wait_ms` (default 5000) for the server's answer.

## 2. Answer the role check and the offer

```sh
turtle-agent rpc <dir> lfg_role_check_answer '{"roles":"d"}'
turtle-agent rpc <dir> lfg_proposal_answer '{"accept":true}'
```

`lfg_proposal_answer` with `accept: false` leaves the queue (the protocol has no decline message: it sends the queue-leave line). `lfg_leave` leaves any time; `lfg_status` reads the state, sending a status request first when `refresh` is true (the server answers nothing while you are idle).

## 3. Let the agent answer by itself

```sh
turtle-agent rpc <dir> lfg_auto '{"roles":"d","accept":true,"delay_ms":1500}'
turtle-agent rpc <dir> lfg_auto '{"off":true}'
```

`lfg_auto` runs a background answerer: role checks get `roles`, offers get accepted (`true`) or declined (`false`; omitted: offers are left alone), after `delay_ms` (default 1500, 0–30000). It returns at once; the answer carries the `auto` settings. `off` stops it.

Role checks and offers each wait 90 s: the state folds `rolecheck` and `offer` deadlines 90 s after the server's line. A reconnect starts the folded state empty: the log it folds from belongs to the old session. Fleet members queue on their own RPC; the fleet file carries no LFT state.

## 4. Browse listed groups

```sh
turtle-agent rpc <dir> lfg_listings '{"refresh":true}'
turtle-agent rpc <dir> lfg_listings '{"id":7}'
turtle-agent rpc <dir> lfg_listing_role '{"role":"d"}'
```

`refresh` fetches the group list; `id` fetches one group's details with its signup rows. `lfg_listing_role` confirms your role in a listed group (`t`, `h`, `d`; empty removes the row); it goes on the party channel, the only line that does.
