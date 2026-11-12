---
type: Overview
title: Raid walks, autopilot and suite
description: What raid-walk, raid-autopilot and raid-suite do and how they fit together.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/raid_walk_cmd.go
tags: [raids, raid-walk, autopilot, suite]
sources:
  - id: walk-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/raid_walk_cmd.go
  - id: auto-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/raid_autopilot_cmd.go
  - id: hire-raid
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/dungeon/hire_raid.go
  - id: suite-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/raid_suite_cmd.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:30:00+01:00
status: draft
---

# Raid walks, autopilot and suite

Three commands drive TortoiseBots parties through raids for testing.

- `turtle-agent raid-walk <plan>` walks one raid plan with hired bots and the TortoiseBots run leader. It leases a slot, logs the slot agent in, puts the commander outside the entrance, hires the roster, binds the raid to its main tank, enters, starts the run with an `until` clause, watches it and tears everything down.[^walk-cmd]
- `turtle-agent raid-autopilot <walk-dir>` keeps a walk dir's raids running without a worker watching them. It reads one goal per line from `targets.txt`, launches a `raid-walk` per id, and re-places any walk that ends, stalls or loses progress until every goal is met.[^auto-cmd]
- `turtle-agent raid-suite` walks a list of raid plans one after another with `raid-walk` and judges each walk against the last good walk of the same plan in a history dir. It is the regression check for the raid plans.[^suite-cmd]

A walk never takes slot 3 (owner rule), and it never walks without `until`: a server without the run-leader `until` support is refused with exit 18.

A raid plan runs in observe mode only, with `prepare.party: hire`. The hire step needs a level-60 commander, first dismisses every group member the walk did not hire (`.bot uninvite`, a failed attempt's late joiners: the run start refuses while the whole party is not in the lead bot's instance) and polls until they have left the group (15 s; a stray that stays stops the walk with exit 18), then hires the roster (`.bot hire raid <tanks>t <healers>h <dps>d t<tier>` with the plan's counts and the tier: the hire's tier, else the preset's tier, else 2), waits for the hires to come online (poll 2 s, ceiling 120 s plus 3 s per bot), takes the first tank-read tank-class member as the lead (warrior, paladin or druid, in group order, at most 6 talent reads), gears the raid with `.bot gear raid <commander> preset <preset>` (90 s, no failure; a partial gear is a ready warning), binds with `.bot commander bind <lead> raid`, and checks the commander stands outside the group before the run starts. The gear preset follows the plan's name (`lower_karazhan_halls` to `lkh`, `zul_gurub` to `zg`, `ruins_of_ahnqiraj` to `aq20`, `timbermaw_hold` to `timbermaw`, `onyxias_lair` to `onyxia`, `molten_core` to `mc`, `blackwing_lair` to `bwl`, `emerald_sanctum` to `es`, `temple_of_ahnqiraj` to `aq40`, `naxxramas` to `naxx`, `upper_karazhan_halls` to `kara40`; anything else to `mc`), with the preset's own tier when the hire names none. The preset tiers are `lkh`, `zg`, `aq20` and `prebis` at 2; `t1`, `timbermaw`, `onyxia` and `mc` at 3; `t2`, `bwl` and `es` at 4; `aq40` at 5; `naxx` at 6; `kara40` at 7. On rehire every hire goes; otherwise hires are kept only when the count by role equals the plan's split exactly, else all are dismissed. No `.bot maintenance` is sent: supplies come from the hire.[^hire-raid]

## How a walk runs

A walk moves through phases in order: validate (no network), lease, `slot setup`, `login`, GM `.go xyz` to the plan's portal, hire and gear, `commander bind <MT> raid`, enter, `run start`, watch, hold, `commander status` and `teardown`, then a summary. The output dir (default `./raid-walk-<plan>-<stamp>`, mode 0700) holds `run.log`, `frames.jsonl`, `summary.json` and `summary.txt`, the combat fight dumps, `pulljoiners.json`, `slot-setup.txt` and `teardown.json`.[^walk-cmd]

While watching, the walk reads the run's `TBM:RUN` lines, the `bot_view` frames and the dashboard. A wipe revives the raid and optionally resumes the run (see [Recover from wipes](recover-wipe.md)). A boss engagement stops the run and waits for a kill, a reset or a wipe.

Exit codes: 0 means the run ended by its `until` (or the hold). 18 means a phase failed or was refused. 19 means the walk ran and was torn down but a wipe or a boss engagement happened. 23 means a fault verdict failed or a cleanup left a bot frozen. 15 means the lease was refused (including slot 3). 26 means the instance cap ledger is at the cap. 29 means a placement failed every try in the same instance. 2 is a usage error.[^walk-cmd]

## The test-server tools

Two flags change what the raid fights, and both only work on the test servers with `--place`:

- `--place` with `--from "leg L"` or `--from "step N"` starts the raid partway into its instance instead of at the door, clearing the legs it skips (see [Start partway into an instance](place-mid-raid.md)).
- `--gm-clear` and `--gm-clear-boss` GM-kill named pull groups or a leg's boss after the placement, so a later leg can be tested while a trash wipe is still being fixed. A walk that used them is marked **ASSISTED**, never a real clear.
- `--gm-instance-data` sends `.debug instancedata` lines when each launch first starts the named leg (a `--reuse-instance` restart sends them again: the data does not survive a wipe), for gates like a boss's priest gate.

`--inject` makes the walk inject a controlled fault (kill the main tank, run a hire away, pull an extra pack, freeze a hire for lag) and measure what the raid does about it. Faults are never combined with `--place`, and the autopilot refuses `--inject` lines.

## Where to go next

- First walk: [Walk your first raid](first-raid-walk.md).
- Concepts you will need: [Until clauses and autopilot goals](until-and-goals.md), [Hire gear tiers](tiers.md), [The instance cap ledger](instance-cap.md).
- Every flag: [`raid-walk` flags](raid-walk-reference.md), [`raid-autopilot` reference](raid-autopilot-reference.md).
- Something failed: [Troubleshooting raids](troubleshooting.md).
