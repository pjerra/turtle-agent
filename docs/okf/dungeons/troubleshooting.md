---
type: Troubleshooting
title: Dungeon troubleshooting
description: Real dungeon error messages, what they mean, and the fix.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dungeon_methods.go
tags: [dungeon, troubleshooting]
sources:
  - id: dungeon-methods
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dungeon_methods.go
    title: dungeon_run RPC, validation, and run lifecycle
  - id: dungeon-remote
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dungeon_remote.go
    title: Commander-mode checks and remote start
  - id: fleet
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/fleet.go
    title: The fleet supervisor
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T22:05:00+01:00
status: draft
---

# Dungeon troubleshooting

Each entry is a message the code really sends (RPC error text, report note, or stderr line), what it means, and the fix. Messages with `<...>` stand for the names and numbers of your run.

## The run would not start

- **`a dungeon run is going (<plan>): dungeon_stop it first`.** One run goes at a time. Fix: `dungeon_stop`, then start again.
- **`dungeon_run needs the character in the world (state ...)`.** Your character is logging in, disconnected, or at the character screen. Fix: wait for the world, then retry.
- **`mode must be "commander", "observe" or "leader" ("remote" and "group" are the older names of commander and leader)`.** Bad `mode`. Fix: use one of the three (or the older names).
- **`mode "remote" needs lead_bot: the name of the bot that walks (letters only)`.** Commander (or observe) mode without a party. Fix: pass `lead_bot`, or `prepare.party "hire"`.
- **`bots: at most 4 besides the lead bot (a party holds five)`.** Too many bots named. Fix: drop to five total.
- **`plan <name> is a draft (...): ... walk it first, or pass allow_draft to run it anyway`.** The plan is an unchecked skeleton. Fix: map it first, or pass `allow_draft`.
- **`plan <name> is a raid plan: run it with mode "observe" and prepare.party "hire"`.** Raid plans observe only. Fix: switch mode and hire.
- **`pulltest needs leg (a number or a name) and pull_group`.** The `pulltest` block is missing its start. Fix: give both `leg` and `pull_group`.
- **`pulltest names its own start (leg and pull_group): leave from_leg, from_pull_group, start_here and resume out`.** Both a pull test and a start were given. Fix: drop the `from_*` fields.
- **`from_pull_group and start_here exclude each other: start_here does not move the party`.** Fix: keep one.
- **`teleport is a GM command of group mode; a remote run enters the dungeon itself (enter)`.** (And the twins for `buffs`, `brain`, `gm`, `wait_sickness`, `tune`.) Fix: move to the mode the message names, or drop the field.
- **`the previous dungeon run still owns the brain (it is ending): try again in a moment`.** The last run's goroutine has not released the brain yet. Fix: wait a moment and retry.
- **`a bot brain that was started by hand is running: brain_stop it first`.** Fix: stop the hand-started brain first.

## The start was refused

- **`from_pull_group <n>: the gather spot ... is <d> yd from pull group <m> ... alive in a fresh instance: pass force_start to start there anyway`.** The unsafe-start gate: the party would land next to live mobs. Fix: start earlier, clear first, or pass `force_start`.
- **`plan check failed: ...` / `plan check could not run: ...`.** The navmesh check refused the plan (or could not run: no navmesh files or planner). Fix: fix the plan, or pass `force` to run past it (the failures land in the result's `plan_check`).
- **`commander bind refused: <reason>`.** The module refused the bind (another commander holds the party, a bot is offline, the group is a raid, ...). Fix: read the reason, free the party, retry.
- **`enter: the account entered 5 new instances in the last hour (limit 5); the oldest was at ...`.** The hourly entry ledger is full. Fix: wait for the oldest entry to age out, or pass `ignore_entry_limit`.
- **`enter: the lead bot <name> is still on map <m>, <n> s after it was sent in ...`.** The party did not get through the portal. Likely causes the message lists: the hourly instance cap, a missing requirement, or a stuck bot. Fix: work through the listed causes.

## The run failed or stalled

- **`stalled: <phase> <step> for <n> s, last progress <what>`.** The watchdog gave up after warn and recover. Fix: read the phase and step in the report, tune the matching key (see [Tune a live run](/dungeons/live-tuning.md)), and retry.
- **`wipe`.** The whole party died with `recover_wipe` off (the group-mode default). Fix: pass `recover_wipe: true`, or fix the pull that kills the party (a pull test isolates it).
- **`party short: the party has <n> of <m> bots, <names> left the group`.** A bot left mid-run. Fix: rebind and resume, or check why the bot left.
- **`the commander binding ended: <reason>`.** The module ended the binding mid-run. Fix: rebind (resume keeps the instance when the binding was kept) and look at the reason.
- **`the view feed stopped: no new frame for 45 s`.** The `TBM:VIEW` feed went quiet. Fix: check `commander view on` and the connection, then resume.
- **`dungeon_pause: the run is a group-mode run with no commander binding to keep; dungeon_stop ends it`.** Pause is commander-only. Fix: `dungeon_stop` the group run.
- **`no dungeon run is going`.** Pause/resume/skip/tune with no active run. Fix: start a run first.

## Fleet messages

- **`fleet: member <n> needs account, character and rpc`.** A fleet-file entry is missing a field (1-based index). Fix: fill it in.
- **`fleet: member <n> rpc needs a fixed port (the supervisor polls it)`.** Port 0. Fix: give each member its own fixed loopback port.
- **`fleet: character "<name>" is listed twice`.** Case-insensitive duplicate. Fix: rename one.
- **`fleet: waiting <d> to log in <name>: realmd allows 14 logins per 5m0s from one address and the fleet has used them`.** Not an error: the shared budget paces the start. Fix: wait.
- **`fleet: not every character was in world after <d>`.** `--wait` expired. Fix: raise `--wait`, or check the failing child's log.
- **`fleet: <name> exited with code <n> (<reason>)`.** A child failed; the reason names it (bad flags, login refused, realm offline, world refused, connection closed, logout refused). Fix: fix that member and restart.
