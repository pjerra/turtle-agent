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

## 5. Read the wire

Your client writes `TW_LFG\t<text>` lines to the guild channel; the server answers as guild-channel addon lines from yourself (only your own GUID counts: the spoof guard drops anything else). Fields split on `;` with empties kept, but listing payloads are JSON and may hold `;`, so only the leading fields split. Client lines: `C2S_QUEUE_JOIN;<instances colon-joined>;<roles>`, `C2S_QUEUE_LEAVE` (the only decline there is), `C2S_GET_QUEUE_STATUS` (no answer when idle), `C2S_ROLECHECK_RESPONSE;<roles>`, `C2S_OFFER_ACCEPT`, `C2S_GET_GROUPS_LIST`, `C2S_GET_GROUP_DETAILS;<id>`. Server lines start `S2C_` (`ROLECHECK_START`, `UPDATE_QUEUE_STATUS`, `QUEUE_JOINED`, `OFFER_NEW`, `OFFER_UPDATE_COUNT`, `OFFER_COMPLETE`, `QUEUE_LEFT`, `QUEUE_ERROR`, `ROLECHECK_INFO`, `GROUPS_LIST_UPDATE`, `UPDATE_GROUP`). Join text caps at 248 bytes (255 with the prefix, the 1.12 chat limit). `lfg_listing_role` sends `C2C_ROLECHECK_RESPONSE;<t|h|d|0>` on the party channel (not guild); empty or anything else sends `0` and removes the row.

## 6. Read the state

Every `lfg_*` answer folds the lines into one state: `phase`, `instances`, `rolecheck_since`/`rolecheck_deadline`, `offer_since`/`offer_deadline`, `responses` (name to roles), `offer` (`instance`, `role`, `counts`), `queued_since`, `counts` (`[t,h,d]`), `last_error`, `listings` (id to signups), and `history` (last 50 lines, sends included). Role checks and offers each time out after 90 s (`LFT_ROLECHECK_TIMEOUT`, `LFT_OFFER_TIMEOUT`). All LFG waits default 5000 ms and cap at 25000 (`wait_ms`).
