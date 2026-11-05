---
type: Guide
title: Run a commander dungeon
description: Drive a bot party through a dungeon from outside the group, start to finish.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dungeon_remote.go
tags: [dungeon, commander, guide]
sources:
  - id: dungeon-remote
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dungeon_remote.go
    title: Commander-mode checks and remote start
  - id: dungeon-methods
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dungeon_methods.go
    title: dungeon_run RPC, validation, and run lifecycle
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:35:00+01:00
status: draft
---

# Run a commander dungeon

In commander mode your character stands outside the group and drives the bots: it binds them (`commander bind`), reads the `TBM:VIEW` feed, and sends orders (`.bot cmd ...`). Your character must be in the world. One run goes at a time.

## 1. Name the plan and the party

```sh
turtle-agent rpc <dir> dungeon_run '{"name":"<plan>","mode":"commander","lead_bot":"<Lead>","bots":["<BotA>","<BotB>","<BotC>","<BotD>"]}'
```

`lead_bot` is the bot that walks (letters only, 2–12). `bots` holds at most 4 more; a party holds five. Or hire a fresh party instead of naming bots:

```sh
turtle-agent rpc <dir> dungeon_run '{"name":"<plan>","mode":"commander","prepare":{"party":"hire"}}'
```

With `prepare.party "hire"`, leave `lead_bot`, `bots`, `tank`, and `healer` out: the hire step names them. See [Hire the party and enter](hire-party.md).

## 2. Name the tank, healer, and carrier when the defaults are wrong

```sh
turtle-agent rpc <dir> dungeon_run '{"name":"<plan>","mode":"commander","lead_bot":"<Lead>","tank":"<Tank>","healer":"<Heal>","carrier":"<Carry>"}'
```

Empty `tank` means the bot that holds the first pulled mob. Empty `healer` means every caster is a mana target at rest gates. Empty `carrier` means the first living bot that is not lead, tank, or healer; it does every object step (chests, boss loot, levers). All three take bot names (letters only).

## 3. Check the answer

```json
{"started":true,"plan":"<plan>","mode":"remote","run_mode":"commander","lead_bot":"<Lead>","run_id":"r-...","plan_check":"passed: ..."}
```

`mode` reads `"remote"` (the old name) and `run_mode` reads `"commander"`. `plan_check` says what the navmesh check found. The run now goes on by itself.

## 4. Poll the run

```sh
turtle-agent rpc <dir> dungeon_status
```

`state` is `running`, then `done`, `failed`, or `stopped`. `stall_level` (`none`, `warn`, `recover`, `fail`) tells you at a glance whether the run hangs.

## 5. Stop or pause it

```sh
turtle-agent rpc <dir> dungeon_stop
```

`dungeon_stop` ends the run and unbinds the party. To halt with the binding kept, so the next run resumes the same instance:

```sh
turtle-agent rpc <dir> dungeon_pause
```

Then continue with `dungeon_run` plus `resume` and `from_leg`; see [Start anywhere](start-anywhere.md). With no run going, `dungeon_pause` changes nothing.

## What commander mode refuses

Commander mode sends no GM commands of its own (only the enter phase's `.tele name`, which it asks the server about first). These options belong to group/leader mode and are refused: `teleport`, `buffs`, `brain`, `rotation`, `role`, `gm`, `gm_level`, `wait_sickness`. Crowd control goes in `cc` (`[{"entry":4863,"mark":"moon"}]`, marks `skull, cross, square, moon, triangle, diamond, circle, star`); the team gate goes in `team` (`alliance` or `horde`).

## Threat, tank-first, loose adds, fleeing mobs

The party holds a pull until the tank holds each mob's threat (`threat_wait_s` 1–30, default 6), then goes in anyway (`threat_overrides`). The tank must stand within 45 yd of the mob for its threat to count. The tank-first rules pick the tank-best mob first, then nearest (below 50% tank share another mob leading by 25+ takes over; the tank stays 5 s on a loose add before another). CC (moon mark or mechanic/name) is left unless last. A mob on a non-tank for 3 s (`looseGrace`) gets the tank via `cmd attack <guid> <tank>` (never `cmd pull`: that was refused `tank-busy` in melee), at most 3 orders per mob with an 8 s cooldown. The first attack waits 2 s for the tank's opener (`opener_delay_s` 0–15). A `tank-busy` refusal waits 10 s (`tank_busy_wait_s` 2–60) and pulls again. A fought mob at or under 20% health moving away is a runner: killed first, waited up to 30 s within 40 yd of the lead. See `report.remote{threat_holds,threat_waits,threat_overrides,loose_add_orders,tank_threat_share,fled,flee_orders,flee_waits}`.
