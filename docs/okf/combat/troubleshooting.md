---
type: Troubleshooting
title: Combat troubleshooting
description: Real combat error messages, what they mean, and the fix.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/brain_methods.go
tags: [combat, troubleshooting]
sources:
  - id: brain-methods
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/brain_methods.go
  - id: rotation-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/rotation_cmd.go
  - id: tactics-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/tactics/tactics.go
  - id: addon-host
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/addon_host.go
  - id: eluna-methods
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/eluna_methods.go
  - id: lfg-methods
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/lfg_methods.go
  - id: lft-msg
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/lft/msg.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:58:00+01:00
status: draft
---

# Combat troubleshooting

## Brain

- `brain_start needs the character in the world (state <state>)` — the character is not in the world yet. Log in first.
- `<method>: a dungeon run owns the brain (dungeon_stop it first)` — a run holds the brain lease. `dungeon_stop` the run, or use `brain_status` (which always answers).
- `the brain is running: brain_stop it first` — stop before starting again.
- `run_id must be letters, digits, dot, underscore or dash, at most 64 characters` — rename the run id.
- `strategies: name at least one` — `brain_start` with an empty list. Name a preset or strategy.
- `strategy "X" is unknown (presets: ...; strategies: ...)` — typo. Pick from the listed names.
- `role "X": want dps, tank or healer` — fix the `role` field.
- `role "X" does not match the strategies [...], which play Y` — the strategies play another role. Name that role, or other strategies.
- `role "tank" needs the role_tank strategy ...` / `role "healer" needs the role_healer strategy ...` — the strategy list cannot play the pinned role. Use a tank/healer preset.
- `a brain was started by hand: the run leaves it alone` — informational: a dungeon leg ended without stopping your hand-started brain.

## Rotations

- `<file>: <reason>` from `rotation check` — the file fails validation (spell or aura name, first-rank level, id, band, condition). Fix what it names; only `ok` files play.
- A built-in name is refused for your file — rename it; names must be new.
- The rotation never fires an entry — check the entry's `when` in the event log's `brain_decision` lines: spell unknown, on cooldown, too little power, out of range, or a condition false. `rotation show --level N` previews the rank choice.

## Tactics

- `legs[0].boss.tactics: interrupt: no spell is named "X"` — typo in a spell name. Fix the spelling; names are checked at plan load.
- `entry N is in both kill_first and never_target` — remove it from one list.
- `move_out[0]: exactly one of cast and ground` — name one per rule.
- `move_out[0]: yards goes with cast (a ground effect has its own radius)` — drop `yards` from a `ground` rule.
- `move_out[0]: yards 99, use 1-60` — bring the radius into range (default 10 when omitted).
- `<list>: 33 entries, at most 32` — shorten the list.
- A tactics rule never fires — the encounter only turns on in leader mode, while a boss or `first` entry is in view, hostile and in combat. Commander and group runs ignore the block (they follow plan-level kill order). Tanks ignore `move_out` cast circles by design.

## Addon host

- `no addon: set tbm_addon_dir in the settings, or run tools/tbmcontract/fetch.sh ...` — the addon checkout is missing. Set the settings key or fetch the pinned cache.
- `the addon host is already running (addon_stop first)` — stop before starting again.
- `clock "X": use real or manual` — fix the `clock` param.
- `loading the addon: ...` — the addon's Lua failed at load. Read the sidecar stderr via `addon_state`.
- `the addon host is not running` / `the addon host is not running (addon_start)` — start it first.
- `the addon host stopped: ... (addon_stop, then addon_start)` — the sidecar died mid-call. Restart it.
- `addon host stopped` — calls after the sidecar exited. Restart it.
- `ms 99: use 1 to 120000` — out-of-range `addon_tick` step.
- `tab "X": use Fight, Camp, Party, Roster, Guild, Raid, Log or BotPanel` — fix the tab name.
- `give a button, or a tab and a label` — `addon_click` needs one or the other.

## Eluna (`tt`)

- `tt: cmd is empty` — send a command.
- `tt: cmd must be one line and carry no # (the agent adds the request id)` — remove the newline or `#...` part.
- `tt: cmd is longer than 200 characters` — shorten it to fit one chat line.
- A `CodeGame` error carrying `err` (e.g. `not_allowed`) — the toolkit refused the command; the refusing reply rides in `replies`. Rank 3 is needed for GM commands.
- A timeout (`eluna: no answer to the request in time`) — the server never answered; `replies` holds whatever arrived.

## LFT

- `N instances, want 1 to 10` — join with 1–10 instances.
- `instance "X" is not 1 to 32 lowercase letters or digits` — fix the LFT code.
- `roles "X" name none of t, h, d` — answer with a subset of `t`, `h`, `d`.
- `roles "X" are not allowed for a <class> (allowed: Y)` — the class cannot take that role. Answer an allowed one, or pass `"force": true`.
- `the character's class is not known yet` — the agent has not seen your unit row. Wait, then retry.
- `phase is X, not Y: cannot ... (force overrides)` — the conversation is not ready for that call. Follow the `idle → rolecheck → queued → offer → grouped` order, or force it.
- `delay_ms N is outside 0-30000` — bring the `lfg_auto` delay into range.
- `roles are empty` — `lfg_auto` needs roles to answer with.
