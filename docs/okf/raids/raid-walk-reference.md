---
type: Reference
title: raid-walk flags
description: Every raid-walk flag, its default, and an example.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/raid_walk_cmd.go
tags: [raids, raid-walk, reference]
sources:
  - id: walk-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/raid_walk_cmd.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:50:00+01:00
status: draft
---

# `raid-walk` flags

Usage: `turtle-agent raid-walk <plan> [--server <server>] [--slot N] ...` or `turtle-agent raid-walk report <dir> ...`. A leading plan moves past the flags before parsing, so flags-first callers parse as before. On/off flags (`--place`, `--decisions`, `--allow-split-ledger`) take `=`: `--flag=off` turns one off, and a bare flag means on.[^walk-cmd]

## Identity and inputs

| Flag | Default | What it does | Example |
|---|---|---|---|
| `--server` | none: always pass it | The slot server. Pass `--server <server>` on every run: the built-in default is a test server name, so always set it. | `--server <server>` |
| `--slot` | any free slot | The slot id. `3` is refused before anything (owner rule, exit 15). | `--slot 2` |
| `--holder` | `raid-walk-<plan>` | The lease holder (1-64 letters, digits, `.` `_` `-`). Two equal holders in the pair form are refused. | `--holder <holder>` |
| `--dir` | `dungeons` | The folder of plan files; the plan JSON is `<dir>/<plan>.json`. | `--dir <plans-dir>` |
| `--sql` | none | The deployed migration: step numbers and the first point come from it. Required with `--place` and with `--until boss-done` on mismatched exports. | `--sql <world-sql>` |
| `--out` | `./raid-walk-<plan>-<stamp>` | The output dir, refused if it exists. Mode 0700, files 0600. | `--out <walk-dir>` |
| `--binary` | this executable | The agent binary slot setup copies. | `--binary ./turtle-agent` |
| `--mmaps` | none | The navmesh dir for slot setup. | `--mmaps <nav-dir>` |
| `--slots` | slots file | The slots file. `--allow-split-ledger=on` permits a file outside the default dir (test sandboxes only). | `--slots <file>` |

## Where the run goes

| Flag | Default | What it does | Example |
|---|---|---|---|
| `--until` | `boss` | `boss` stops before the first boss step, `boss-done` after it, `none` is a full clear, `step N` or `leg L` stop there. | `--until boss-done` |
| `--from` | none | `"leg L"` or `"step N"` for the run leader; only for a raid already standing there, unless `--place` stages it. | `--from "leg <leg>"` |
| `--place` | off | With `--from`: put the raid at that start inside the instance. Test servers only, needs `--sql`, refused with `--inject`. Bare `--place` is on; `--place=off` turns it off. A same-instance try that finds half the hires dead or the start leg's boss fighting the party ends the placement at once with exit 29 (`place: party dead ...` / `place: boss engaged with the party ...: fresh instance needed`), where the autopilot relaunches a fresh instance instead of feeding the boss. | `--from "leg <leg>" --place` |
| `--no-place-unfreeze` | off | With `--place`: turn off the unfreeze check. The `spread=5` place can drop two seats onto a small disconnected navmesh island beside the place point, where every move fails; by default, from the first watch poll after each placement move (the `.tt place`: 11 yd ring; every GM summon wave of a retry, a retreat or the reuse/bind gather: 6 yd ring at its own point; a re-arm first writes the running check's summary) and for 120 s, sampled once per poll, a hire that is alive, in the instance, not the main tank, has stood still (under 2 yd for 10 s) within the ring of the placement point and more than 35 yd from the main tank while the main tank is more than 15 yd from the point is put onto the main tank (the commander `.go xyz` onto him at z + 2, waits up to 5 s for the landing within 5 yd, then `.summon <hire>`; the commander stays there, possibly in the fight), at most twice per hire. Combat is not part of the rule. A successful summon writes `placement: re-placed <hire> (frozen at x,y,z for <s>s)` to run.log; a failed one `placement: re-place failed <hire> (<reason>)` (at most 3 per hire, 5 s apart). Three failed `.go` moves in a row (no landing counts as one), the main tank's death or a wipe stop the check (`placement: unfreeze check stopped (<reason>)`), and it ends with `placement: frozen=<n> re-placed=<n>`. | `--place --no-place-unfreeze` |
| `--reuse-instance` | off | With `--place`: keep the raid's current instance (no fresh entry, no cap count). Combines with `--gm-clear`. | `--place --reuse-instance` |
| `--speed` | `1.0` | The run's test speed, 1.0 to 3.0 with one decimal. Above 1.0 sends `speed=X` and marks a test run. | `--speed 3.0` |
| `--enter` | `auto` | `auto` tries `near` and falls back to `tele` once on `requirement`; `tele` needs the plan's `entrance_tele`. | `--enter tele` |
| `--max-run-minutes` | `90` | The ceiling from `run start` to the end. The lease lasts max-run plus hold plus 30 (limit 720). | `--max-run-minutes 120` |
| `--hold-minutes` | `0` | Keep the raid bound this many minutes after the run. | `--hold-minutes 5` |

## Recovery

| Flag | Default | What it does | Example |
|---|---|---|---|
| `--wipe-resume` | `0` | Revive, rest and resume a wipe or near-wipe stop this many times per walk (0-3; 0 ends the walk). | `--wipe-resume 2` |
| `--wipe-rest` | `10m` | How long a revived raid rests out of combat before the resume (30s or more). | `--wipe-rest 15m` |
| `--auto-resume` | `0` | Resume a regroup pause this many times per step (0-3). | `--auto-resume 2` |
| `--paused-wait` | `10m` | How long a pause that is not a regroup may last. | `--paused-wait 5m` |
| `--reset-wait` | `5m` | How long a wipe may wait for the instance to reset. | `--reset-wait 3m` |
| `--revive-at-raid` | on | Revive the raid at the revive point: summon hires more than 100 yd away to it, and wait up to 60s for every hire to gather. `false` is the old revive in place. | `--revive-at-raid=false` |
| `--patrols` | none | A tb-patrols file for the revive quiet check. | `--patrols <file>` |

## Hires and tier

| Flag | Default | What it does | Example |
|---|---|---|---|
| `--hires` | from the plan | The tb-raid-roster file (v1 or v2); without it the roster comes from the plan's `raid {size, tanks, healers, dps}` block. | `--hires <roster>` |
| `--tier` | `0` | The hire gear tier (1-3) of a raid the committed table leaves unknown. | `--tier 1` |
| `--tier-override` | `0` | The hire gear tier (1-3) in place of the raid's minimum: a comparison, not a tier proof. | `--tier-override 3` |
| `--hire-settle` | `20s` | How long to wait after the last hire for its join summon before the refit and the bind; `0` skips the wait. Below the default marks the summary `settle_short`. | `--hire-settle 30s` |

## Instance cap

| Flag | Default | What it does | Example |
|---|---|---|---|
| `--instance-cap-per-account` | `5` | The core's limit of fresh instance ids one account may enter in an hour. The server's entry in `instance-cap-per-account.json` wins when the flag is absent. | `--instance-cap-per-account 30` |
| `--instance-cap-wait` | `20m` | At the cap's raid count, wait up to this long for the oldest entry to age out, then exit 26. `0` exits 26 at once. | `--instance-cap-wait 5m` |
| `--instance-cap-fail` | off | At that count exit 26 instead of waiting. | `--instance-cap-fail` |
| `--instance-cap-unsafe` | off | Unsafe: go on without a reservation when the wait runs out or the ledger cannot be read (logged). The autopilot refuses it. | `--instance-cap-unsafe` |
| `--instance-cap-reset` | off | Forget this server's instance ledger first (after a worldserver restart). | `--instance-cap-reset` |

## Test-server tools

| Flag | Default | What it does | Example |
|---|---|---|---|
| `--gm-clear` | none | Repeatable: `"<leg>:<group>[,<group>...]"` — GM-kill those pull groups after the placement. Needs `--place`; the walk is then ASSISTED. | `--gm-clear "<leg>:1,2"` |
| `--gm-clear-boss` | none | Repeatable: `<leg>` — GM-kill the leg's boss and its pack. Needs `--place`. | `--gm-clear-boss <leg>` |
| `--gm-clear-spawns` | pack default | The world export dir the clears read the packs from. | `--gm-clear-spawns <dir>` |
| `--gm-instance-data` | none | Repeatable: `"<leg>:<a> <b>[;<a> <b>...]"` — send each as `.debug instancedata` when the walk first starts the leg. Test servers only. Every launch sends the lines after placement, `--reuse-instance` restarts included (one walk never sends a leg twice), and that includes a launch that resumes from `--from step N --place --reuse-instance` (the autopilot's restart), pinned by `TestGMInstanceDataReuseResumeFromStepSendsOnceBeforeRunStart`. The instance data can be lost between launches: the zulgurub boss scripts overwrite their own slot on Reset (`boss_jeklik.cpp:105-106` sets `FAIL` on a living Jeklik, `boss_venoxis.cpp:152-153` sets `NOT_STARTED`, both without a DONE guard; Hakkar then casts that priest's Aspect, `boss_hakkar.cpp:228-240`). Walk 44, raid hk7m attempt -3, a `--reuse-instance` restart that sent nothing, saw exactly that. A wipe recovered in place inside one running walk may keep the data, so the walk clears its markers on every wipe recovery and the next status that names the leg resends the lines under their own pause and resume; the resend is idempotent, so both cases are safe. A GM `.die` kill does not stop the respawn, and a respawned priest's Reset() overwrites its data again, so after `run start` the walk sends the lines (first send of a leg, wipe-recovery resend, or the once-per-boss-row resend) ONLY at an allowlisted status: phase `rest` or `gather` that passes the pre-pull gate (no pulling or fighting note, no later pull line in the same event page, no bot of the party in combat in a frame under 15 s old), under the usual pause and resume. The only other send is the launch send before `run start`. At any other status (walking, begin, pull, fight, hold, boss rows, unknown phases) a pending send is deferred and never fires, because a pause there can land mid-pull (the boss row becomes current about 1 s before the run can pull, and a mid-fight pause drops the focus for ~7 s). The once-per-boss-row resend goes out at the rest/gather of the step before the boss row (walk 45 hk7m: a GM `.die` kill does not stop the respawn, and a respawned priest's Reset() overwrites its DONE data); a deferred send whose leg never produces a qualifying rest/gather (the walk leaves the leg, or the run ends inside it) is reported, not dropped silently: a summary note and a run.log WARN `gm-instance-data: <leg> lines NOT sent (no out-of-combat rest/gather in the leg)`, counted as `gm_instance_data_unsent` in the summary (the walk does not fail); a boss row that becomes current with no such rest logs `gm-instance-data: no out-of-combat rest before boss row N; resend skipped`. The `Initialize()` memset (`instance_zulgurub.cpp:38`) only runs when an instance is created. | `--gm-instance-data "<leg>:1 3"` |
| `--inject` | none | Repeatable (at most 4, each kind at most once): `kill-mt`, `runaway`, `add-pack` or `lag`, each with `@from=N` and its keys. Refused with `--place`. | `--inject kill-mt` |

## Practice runs

`--practice <entry>` keeps one boss fight alive for mechanic practice on a test server: the run walks to and pulls the boss as usual, then the walk collects the mechanic's cycles, revives the raid and restores the boss between attempts, and resets the boss with GM tools instead of losing the raid after two or three cycles. It needs `--place` and `--from` (it starts the raid at its boss row) and is refused on every server outside the test pair, at option parse and again before every GM action. The walk is marked **PRACTICE ASSISTED**, and it never credits a boss kill: a boss death during practice ends the practice at once ("boss killed during practice") without a respawn.

| Flag | Default | What it does | Example |
|---|---|---|---|
| `--practice <entry>` | none | The boss's creature entry. `14834` is Hakkar (the mechanic table); any other entry is window mode (see `--practice-window`). Needs `--place` and `--from`; test servers only. | `--practice 14834` |
| `--practice-cycles` | `10` | The mechanic cycles the run collects before it ends. | `--practice-cycles 10` |
| `--practice-max` | `20m` | The practice loop's wall-time ceiling. Reaching it stops the practice with exit 18 (`practice stopped: max time`). | `--practice-max 25m` |
| `--practice-rest` | `30s` | The out-of-combat rest between attempts (restarts if a hire enters combat). | `--practice-rest 45s` |
| `--practice-boss-floor` | `40` | The boss HP percent below which a cycle restores it to its own max. | `--practice-boss-floor 50` |
| `--practice-sustain` | `on` | Revive dead hires and restore the boss at each cycle (`on` or `off`). | `--practice-sustain=off` |
| `--practice-window` | `120s` | The cycle window of an entry the mechanic table does not know: one cycle per window, no spell detection, 3 cycles per attempt, no respawn set. | `--practice-window 90s` |

The mechanic table's timings are the core's own (cited in `internal/raidwalk/practice.go`):

| Entry | Boss | Mechanic | First cast | Every | Eval after | Enrage | Cycles per attempt | Respawn set |
|---|---|---|---|---|---|---|---|---|
| `14834` | Hakkar | Blood Siphon 24324 | 90 s | 90 s | 20 s | 600 s | 5 | Sons 11357 |

Safety rules (binding; each has a test):

1. A practice run only where `gm_kill` is allowed today: the server is in `placeServers` AND the agent's session dialled that server's configured world_addr (its `AllowGMKill`). The walk refuses at option parse and re-checks the server before every GM action.
2. The GM line set is closed: `.die` (the existing gm_kill), `.npc evade`, `.modify hp <n> <n>`, `.respawn` (the selected form only), `.revive <name>`, plus the existing `.debug instancedata`. `session.PracticeGMLine` builds every line; any other text is refused before it reaches the session.
3. Targets: `.npc evade` and `.modify hp` only on the practice boss's guid; `.respawn` only on guids of the mechanic's respawn set recorded from the client's unit list at the attempt start (and confirmed dead); `.revive` only for names in the walk's own hire list.
4. Bounded: `--practice-max` (default 20 m), at most 4 attempts, 3 failed resets in a row → `practice stopped: <reason>` with exit 18.
5. Marking: the summary says `PRACTICE` and `ASSISTED`; a practice walk never credits a boss kill and never meets a `boss:` goal; a boss death during practice ends the practice ("boss killed during practice", no respawn of the boss).
6. No DB writes, no config edits, no server restarts, no `.respawn` without a selection, no `.reset`/`.instance` commands.

Each attempt records one row per cycle in `<out>/practice.jsonl`:
`{attempt, cycle, total_cycle, cast_at, detect, alive, dead_since_last, boss_hp_pct, sons_alive, sons_known, sustain:{revived:[...], boss_hp_set}}`.
`detect` is `event` (a `spell_start`/`spell_go` of the mechanic's SpellID from the boss guid), `timer` (no cast seen by FirstAtS+10 s, so the boss's own timer is used) or `window` (window mode). The summary carries
`practice{entry, attempts, cycles, ended, rows}` and `practice: <cycles> cycles in <attempts> attempts, ended: <reason>` in `summary.txt`. Expected pace for Hakkar: 5 cycles per attempt (~8 min), so 10 cycles are 2 attempts and 2 resets (~17 min).

## GM and toolkit allow-lists

Every GM line passes the allow-list before it is sent; a refused line is a programming error and stops the walk. A practice run's hands are the closed set above (`.npc evade`, `.modify hp <n> <n>`, `.respawn` and `.revive <name>`); they go through the agent's gated RPCs (`gm_evade`, `gm_set_hp`, `gm_respawn`, `gm_revive`), never through the generic `gm` method. The walk may send: `.gm on` (the place phase's probe) and `.gm visible off` (the revive hides the commander); `.go xyz <x> <y> <z> <map>`; `.pinfo [<hire>]` (the instance ledger's account read; a name must be a hire); `.tele name <hire> <word>`; `.summon <hire>` and `.revive <hire>`; `.appear <hire>` (into the raid's copy); `.instance unbind <plan map>` (that map and no other) and `.instance listbinds`; `.debug instancedata <a> <b>` (the `--gm-instance-data` tool, two integers, nothing else); `.bot hire raid ... level=<n>[ t<tier>]`; `.bot gear raid <commander> tier <N>`; `.bot role <hire> <spec>`; `.bot run stop[ time limit reached (raid-walk <m>m)]` and `.bot run resume`. Nothing else: no `.die`, no `.npc`, no `.kick` through this path (the `--gm-clear` kill goes through the agent's `gm_kill` RPC instead).[^walk-cmd]

Every toolkit line passes its own allow-list too: one line, at most 200 characters, no `#`. The walk may send `q inst`, `q floor <x> <y> <z>`, `q group of=<hire>`, `q units <r<=80> <x> <y> <z> all [dead] [e=<entry>]`, `quiet <r<=50> <x> <y> <z> w=<0..30> dz=<1..15> [skip=<ids>]`, `place <x> <y> <z> spread=<0..8> of=<hire>`, and `clearto <map> <x> <y> <z> <r<=30> dz=<1..15> all=1 [skip=<ids>]` (the map must be the plan's). Never `kill`, `boss`, `force`, `me`, `reset` or `here`; never a `clearto` without `dz=`; never another map; never a name that is not a hire. At most 16 entries ride one `skip=`; a plan whose legs' skip union is longer is refused before any GM line.[^walk-cmd]

## Output files

The out dir holds `run.log` (every phase line with `HH:MM:SS.mmm` stamps), `commanders-NNNN-<reason>.json`, `combat-fight-*.json` with the full `combat-fight-detail` beside it (the detail stays live while the fight runs), `combat-fights.json` (the dashboard's raw fight list) with `fights-run.json` beside it (`walk_started` plus the sorted `history_ids` the dashboard already listed when the walk began, so an analyser can filter the raw list without `START.epoch`; `history_ids` is the reliable filter and `walk_started` is informational (this raid-walk's own start on the laptop clock). When the pre-placement dashboard read failed, the at-start set falls back to the records' start times: a row counts as history only if it started over 10 s before the walk began), `frames.jsonl` (one frame per 10 s idle, one per 2 s in fight; `power_type` 0-3, 0 omitted; tier recorded), `pulljoiners.json` (one record per pull attempt by pull time: the frames' own count of engaged hostiles outside the pulled pack within 10 s of the pull), `slot-setup.txt`, `teardown.json`, `summary.json` (v1) and `summary.txt`.[^walk-cmd]

A practice walk's `summary.json` carries `practice{entry, attempts, cycles, ended, rows}` and no `boss_kill`/`boss_kills` (practice never credits a kill). Otherwise `summary.json` carries `boss_kill{entry, step, engaged, killed, skipped, phase, phase_entry, evidence}` for the last engaged boss (the run leader's own kill report, the phase the boss unit was seen with, and the evidence or the reason it does not hold), one `boss_kills[]` record per boss step the run's statuses showed, and `teardown{failed, hires_left, reason}` (the first failing step, empty when nothing failed). A dashboard fight record with outcome `kill` for the goal boss's own row credits `boss_kills` even when the run's frames stopped with the fight; a record the dashboard already listed when the walk began (an earlier walk's or instance's) credits only when this walk's frames never showed that boss alive. Each fight in `summary.json` carries `history: true` when the dashboard already listed it before this walk filed one (an earlier walk's or instance's record: kept for the kill credit, with no `combat-fight-<id>.json` copied into this walk's dir). A `--place` walk adds `place{start_step, leg, until_step, instance, circles[], not_cleared[], quiet, placed, hires, summoned[], notes[]}`; a `--gm-clear` walk adds `assisted` and `gm_clear[]`; an `--inject` walk adds `injections[]`, `incidents[]` and `deaths.injected`.[^walk-cmd]

## Capture

| Flag | Default | What it does | Example |
|---|---|---|---|
| `--dash-url` | none (or `TBDASH_URL`) | The dashboard URL. Without a URL and a password file the capture is off. | `--dash-url <url>` |
| `--dash-user` | `TADASH` (or `TBDASH_USER`) | The dashboard login. | `--dash-user <user>` |
| `--dash-pass-file` | none (or `TBDASH_PASS_FILE`) | The dashboard password file (0600, not a symlink; never printed). A failed login is a warning and the walk goes on. | `--dash-pass-file <file>` |
| `--decisions` | on | Pull decision captures after teardown (`on` or `off`). | `--decisions=off` |
| `--decisions-max-mib` | `1024` | The decisions pull budget in MiB. | `--decisions-max-mib 512` |

## Other

| Flag | Default | What it does | Example |
|---|---|---|---|
| `--dry-run` | off | Validate and print the phase plan, send nothing. | `--dry-run` |

## Exit codes

0 the run ended by its `until` (or the hold). 18 a phase failed or was refused (setup, login, hire, bind, enter, start, no `until`, stall, ceiling, interrupt, `lease lost`). 19 the walk ran and was torn down but a wipe or a boss engagement happened, or a hire was still listed as online. A practice that stopped before its cycle budget (`practice stopped: max time`, `3 failed resets`, `4 attempts`, a stall) uses 18. 23 a fault verdict failed or a cleanup left a bot frozen (at least 23, even over 18/19). 15 the lease was refused (including slot 3). 14 teardown failed. 26 the instance ledger is at the cap. 29 the placement failed every try in the same instance. 2 usage.
