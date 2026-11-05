---
type: Reference
title: Combat RPC
description: Every combat RPC method, its params, defaults and an example.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/brain_methods.go
tags: [combat, rpc, reference]
sources:
  - id: brain-methods
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/brain_methods.go
  - id: spell-methods
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/spell_methods.go
  - id: cast-methods
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/methods.go
  - id: addon-host
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/addon_host.go
  - id: eluna-methods
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/eluna_methods.go
  - id: lfg-methods
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/lfg_methods.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:55:00+01:00
status: draft
---

# Combat RPC

Every combat call, what it takes, what it defaults to, and one example. All calls below need a logged-in agent; the ones marked in-world need the character in the world.

## Fighting directly

| Method | Params | Defaults | Example |
|---|---|---|---|
| `cast` | `spell_id` (number, required), `target` (guid, optional) | No target: the selected target | `{"spell_id": 116, "target": 0}` casts Frostbolt |
| `cancel_cast` | `spell_id` (number, optional) | `0`: whatever is being cast | `{}` cancels the cast in progress |
| `attack` | None | — | Starts auto-attack on the selected target |
| `stop_attack` | None | — | Stops auto-attack |
| `face` | `guid` (guid, required) | — | Turns to face the unit |
| `target` | `guid` (guid, optional), `name` (string, optional) | Selects by guid or name | `{"name": "<mob>"}` |

## Spell state (in-world)

| Method | Params | Defaults | Example |
|---|---|---|---|
| `spells` | `spell_id` (number, optional), `name` (string, optional, part of the name, case-insensitive), `highest` (bool, optional), `passive` (bool, optional) | No filter: every active spell, all ranks; passives excluded | `{"name": "Frostbolt", "highest": true}` |
| `cast_state` | None | — | Reports the cast (`idle`/`casting`/`channelling`), last success and failure, the GCD, and combo points |

`spells` rows carry the spell's metadata (level, flags, cast time, ranges, cost, cooldowns, school, effects) plus whether it can be cast now. `cast_state` mirrors the live tracker in [Spell data and spell state](concept-spell-data.md).

## The brain

| Method | Params | Defaults | Example |
|---|---|---|---|
| `brain_start` | `strategies` (list, required), `role` (string, optional), `tank` (string, optional), `no_move` (bool, optional), `rotation` (string, optional), `run_id` (string, optional), `trace` (bool, optional), `trace_meta` (string-to-string object, optional, at most 16 entries) | Role from the strategies; generated run id; trace on | `{"strategies": ["frost_mage"], "rotation": "my_frost"}` |
| `brain_stop` | None | — | Stops the brain |
| `brain_status` | None | — | Always answers, even mid-run |

`role` is `dps`, `tank` or `healer`. `rotation` names a built-in or `rotations_dir` file. `run_id` allows letters, digits, dot, underscore and dash, at most 64 characters. `trace_meta` is an object of string to string with at most 16 entries; keys and values need 1–200 characters each. Refused while a dungeon run owns the brain, except `brain_status`. Full walkthrough in [Run the brain](guide-run-brain.md).

## The addon host

| Method | Params | Defaults | Example |
|---|---|---|---|
| `addon_start` | `clock` (string, optional) | `"real"`: ticks every 100 ms; `"manual"`: only `addon_tick` advances it | `{}` |
| `addon_stop` | None | — | Stops the sidecar |
| `addon_state` | None | — | The addon state, host cursor and last sends |
| `addon_tick` | `ms` (number, required) | 1–120000 | `{"ms": 1000}` steps one second (manual clock) |
| `addon_click` | `button` (string, optional), `tab` + `label` (strings, optional), row selectors (`bot`, `name`, `names`, `mark`, `row`, `view`, `bag`, `slot`), `before` (string, optional), `force` (bool, optional), `wait_ms` (number, optional) | `wait_ms` default 8000 | `{"button": "Fight.AoE"}` |

One of `button` or `tab` + `label` is required; tabs are `Fight`, `Camp`, `Party`, `Roster`, `Guild`, `Raid`, `Log`, `BotPanel`. Full walkthrough in [Drive the addon host](guide-addon-host.md).

## Eluna

| Method | Params | Defaults | Example |
|---|---|---|---|
| `tt` | `cmd` (string, required) | — | `{"cmd": "q units 40"}` |

`cmd` carries no `.tt` prefix and no `#id`; at most 200 characters, one line. The result is `replies`: the raw reply objects in order. See [Eluna test hooks](concept-eluna.md).

## LFT

| Method | Params | Defaults | Example |
|---|---|---|---|
| `lfg_join` | `instances` (list, required), `roles` (string, required), `force` (bool, optional), `wait_ms` (number, optional) | `wait_ms` default 5000 | `{"instances": ["ragefirechasm"], "roles": "td"}` |
| `lfg_role_check_answer` | `roles` (string, required), `force` (bool, optional), `wait_ms` (number, optional) | `wait_ms` default 5000 | `{"roles": "td"}` |
| `lfg_proposal_answer` | `accept` (bool, required), `force` (bool, optional), `wait_ms` (number, optional) | `wait_ms` default 5000 | `{"accept": true}` |
| `lfg_leave` | `wait_ms` (number, optional) | `wait_ms` default 5000 | `{}` |
| `lfg_status` | `refresh` (bool, optional), `wait_ms` (number, optional) | No refresh: the folded state only; `wait_ms` default 5000 | `{"refresh": true}` |
| `lfg_auto` | `roles` (string), `accept` (bool, optional), `delay_ms` (number, optional), `off` (bool, optional) | `delay_ms` default 1500 (0–30000) | `{"roles": "d", "accept": true}` |
| `lfg_listings` | `refresh` (bool, optional), `id` (number, optional), `wait_ms` (number, optional) | `wait_ms` default 5000 | `{"refresh": true}` then `{"id": 7}` |
| `lfg_listing_role` | `role` (string, required), `wait_ms` (number, optional) | `wait_ms` default 5000 | `{"role": "t"}` |

Every answer returns `since` (the log cursor before the request), `sent` (the text sent), and `state` (the folded queue state). `roles` is the subset of `t`, `h`, `d`. `lfg_auto` with `"off": true` stops the answerer. Full walkthrough in [Queue through LFT](guide-queue-lft.md).
