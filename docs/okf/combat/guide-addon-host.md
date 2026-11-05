---
type: Guide
title: Drive the addon host
description: Start the headless TortoiseBotsManager addon, click its buttons, and stop it.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/addon_host.go
tags: [combat, addon, rpc]
sources:
  - id: addon-host
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/addon_host.go
  - id: addonhost-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/addonhost/host.go
  - id: addon-events
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/addonhost/events.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:45:00+01:00
status: draft
---

# Drive the addon host

The addon host runs the real TortoiseBotsManager addon headless: a `lua5.1` sidecar loads the addon's Lua on a 1.12 widget mock and is driven over stdin/stdout. The host itself touches no connection: what the addon asks the client to do comes back to the agent, which performs it through the normal send paths. You need `lua5.1` on `PATH` and the addon checkout (settings key `tbm_addon_dir`, else the pinned cache fetched by `tools/tbmcontract/fetch.sh`).

## 1. Start it

```json
{"method": "addon_start", "params": {}}
```

The answer carries the checkout `dir`, the addon's `.toc` `version`, the loaded `files`, the `clock`, and what the addon sent while loading (its handshake). `"clock": "manual"` ticks only when you call `addon_tick`; the default `real` ticks with the wall clock every 100 ms. Starting twice is refused (`the addon host is already running (addon_stop first)`); an unknown clock is refused (`clock "fast": use real or manual`).

## 2. Click a button

```json
{"method": "addon_click", "params": {"button": "Fight.AoE"}}
```

Name a button (`"Fight.AoE"`) or a widget by tab and label (`"tab": "Fight", "label": "AoE"`), with row selectors (`bot`, `name`, `names`, `mark`, `row`, `view`, `bag`, `slot`) as needed. `"force": true` clicks a greyed-out button anyway; `"wait_ms"` (default 8000) bounds the wait for the reply on a real clock. The answer lists everything clicked (widgets pressed and `/tbm` commands typed, in order), what the addon sent and which server lines it received in that time, whether it timed out, and the addon state.

## 3. Step it by hand

With a manual clock, advance time yourself:

```json
{"method": "addon_tick", "params": {"ms": 1000}}
```

`ms` runs 1–120000. The answer carries the addon's `GetTime()` clock and what it sent.

## 4. Read state and stop

```json
{"method": "addon_state", "params": {}}
```

This returns the addon's state plus the host cursor and the last sends. When done:

```json
{"method": "addon_stop", "params": {}}
```

Stopping when nothing runs is refused (`the addon host is not running`). Calls after the sidecar has exited or was stopped fail with `addon host stopped`.
