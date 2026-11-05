---
type: Guide
title: Queue through LFT
description: Join the Turtle group finder, answer the role check and the offer, and leave.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/lfg_methods.go
tags: [combat, lft, rpc]
sources:
  - id: lfg-methods
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/lfg_methods.go
  - id: lft-msg
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/lft/msg.go
  - id: lft-state
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/lft/state.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:45:00+01:00
status: draft
---

# Queue through LFT

LFT is Turtle's group finder, spoken as an addon conversation: your client writes `TW_LFG` lines to the guild channel and the server answers as addon lines from yourself. The agent folds those answers into a state (`idle`, `rolecheck`, `queued`, `offer`, `grouped`, `left`, `error`) and every `lfg_*` call returns it.

## 1. Join the queue

```json
{"method": "lfg_join", "params": {"instances": ["ragefirechasm"], "roles": "td"}}
```

`instances` holds 1–10 core LFT codes (lowercase letters and digits, at most 32 each), deduplicated in order. `roles` is the subset of `t`, `h`, `d` you will play, in that order. The agent checks the roles against your class first: a hunter answering `t` is refused (`roles "t" are not allowed for a hunter (allowed: d)`), because the core would silently ignore the answer and the check would time out after 90 s. `"force": true` skips that check. `"wait_ms"` (default 5000) bounds the wait for the server's answer.

## 2. Answer the role check

When the state is `rolecheck`, confirm the roles:

```json
{"method": "lfg_role_check_answer", "params": {"roles": "td"}}
```

Answering outside a role check is refused (`phase is queued, not rolecheck: cannot answer a role check (force overrides)`); `"force": true` sends anyway.

## 3. Answer the offer

When the state is `offer`, accept or decline:

```json
{"method": "lfg_proposal_answer", "params": {"accept": true}}
```

The protocol has no decline message: `"accept": false` sends `C2S_QUEUE_LEAVE`, which cancels the offer.

## 4. Leave when done

```json
{"method": "lfg_leave", "params": {}}
```

This cancels a role check, declines an offer, or leaves the queue, wherever you stand.

Helpers: `lfg_status` with `"refresh": true` asks the server where you stand (without refresh it just returns the folded state). `lfg_listings` with `"refresh": true` fetches the group listings, and with `"id": 7` the details of listing 7; `lfg_listing_role` confirms your role row in a listed group. `lfg_auto` answers role checks (and optionally offers) for you: `{"roles": "d", "accept": true, "delay_ms": 1500}` turns it on with a 0–30000 ms delay, `{"off": true}` turns it off.
