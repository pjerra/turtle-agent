---
type: Concept
title: Refusals, the watchdog, and the entry ledger
description: What a bot's "no" means, how the stall guard works, and why the 6th instance entry is refused.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/dungeon/refusals.go
tags: [dungeon, refusals, watchdog, concept]
sources:
  - id: refusals
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/dungeon/refusals.go
    title: Refusal types and classification
  - id: watchdog
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/dungeon/watchdog.go
    title: The progress watchdog
  - id: entry-ledger
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/entry_ledger.go
    title: The hourly instance-entry ledger
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:50:00+01:00
status: draft
---

# Refusals, the watchdog, and the entry ledger

Three mechanisms keep a run honest: the bots' refusals, the progress watchdog, and the hourly entry ledger.

## Refusals: the bots say no

A refusal is TortoiseBots answering `TBM:ACTION_ERR|<intent>|<code>|<message>` to an order, or a party bot whispering `<mob> is evading`, `<mob> is dead`, or `<mob> is too far away`. (A whisper names no order; the driver takes it for the answer to its last order.) The run records up to 50 refusal kinds in its report, with leg, count, and first time.

The driver sorts refusals by what to do about them:

- **Unreachable** (`unreachable`, `evading`, `evade`, or a message about evading): the tank cannot reach the mob or it evades. No point pulling it again; the run skips it.
- **Dead** (`dead-target`, `dead`, or a message ending `is dead`): the target died before the order arrived. The run picks again.
- **No target** (`no-target`): the order arrived with no live hostile selected. The run picks again (at most a few re-picks).
- **Anything else**: noted; the step decides (some are retried: a busy carrier's order goes up to 4 times, a cancelled one twice).

Refusals you will meet often: `too-far` (regroup: the party is scattered), `no-bot` (no live owned bot is controllable), `no-tank` (no tank in the party), `in-combat` (wait it out), `busy` (retry).

## The watchdog: the stall guard

Both drivers feed a progress watchdog and ask it at every wait. Progress is anything real: the step advanced, a pull started or ended, a kill, a fought mob's health at a new low, the lead bot displaced by over 5 yards, an order ACKed, a rest gate finished, an object step ended. After `warn_s` seconds without progress (default 120) the run logs a stall event and a note; after `recover_s` (default 240) it makes one recovery attempt suited to the phase; after `fail_s` (default 420) it stops with `stalled: ...`. Set levels per run with the `watchdog` field (`{"warn_s":..,"recover_s":..,"fail_s":..}`); a zero field takes the plan's own or the default, a negative one turns that level off; the call wins field by field over the plan. `dungeon_status` lifts the watchdog's view out for pollers: `last_progress_at`, `stalled_for_s`, `stall_level`.

Progress detail: a fought mob's new low counts at ≥1% max; displacement counts net 5 yd; an ACK counts when fresh (not in the last 16). After recovery, repeats no longer count: only kills, new lows, steps, object ends or walks-on. `fail_s` is a ceiling; rest gates (out of combat), `wait_for`, leader death (≤5/run, corpse ≤10 min), wipe recovery (≤12 min, ≤2/run) and remote revive pause it. Recoveries are per phase: remote walks re-plan ≤40 yd, pull fights `follow` + re-pick, silent feed re-`view on`; group pull fights `follow` + re-pick. The watchdog is read from the drivers' waits only. `ACTION_PENDING` waits ≤90 s (`enter`: 130 s).

## The entry ledger: five fresh instances an hour

The server allows 5 new dungeon instances per account per hour (re-entering a saved one is free). Your agent's hires share its account, so the agent keeps a small JSON file of entry times next to the settings file (named by a hash, so the account name stays out of it) and refuses the 6th entry with the oldest entry's time. A server restart clears the server's list but not this file; `ignore_entry_limit` overrides it. The report's `remote.enter` carries the entries used, the entries in the last hour, and any warning.
