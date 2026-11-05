---
type: Reference
title: README section map
description: Every section of the old long README and the docs pages that now hold its detail.
resource: https://github.com/pjerra/turtle-agent/blob/dev/docs/okf/about/readme-map.md
tags: [docs, readme, map]
sources:
  - id: readme
    resource: https://github.com/pjerra/turtle-agent/blob/dev/README.md
    title: The old long README (about 5400 lines) whose detail moved here
generated:
  by: omp
  at: 2026-11-05T21:45:00+01:00
status: draft
---

# README section map

The old `README.md` (about 5400 lines) was a manual; this bundle is now the manual. Every old `##`/`###` section below points at the docs page or pages that hold its commands, flags, defaults, exit codes, error messages, file formats, limits, and "Known limits" bullets. Numbers were checked against the code while moving: the code wins, so a README claim the code contradicts is marked and the docs carry the code's fact.

## Intro and setup

| Old README section | Docs pages that now hold it |
|---|---|
| Title, badges, intro (what it is, who it is for) | [What turtle-agent is](../getting-started/what-it-is.md), [Agent overview](../agent/overview.md) |
| Branches and CI | [Install and build](../getting-started/install.md) (branch flow, `test` gate), [docs/CI.md](https://github.com/pjerra/turtle-agent/blob/dev/docs/CI.md) (jobs, secrets) |
| Quick start | [First login](../getting-started/first-login.md), [Write the settings file](../getting-started/write-settings.md), [Settings reference](../getting-started/settings-reference.md), [Log a character in](../getting-started/log-in.md) |
| CLI | [Command map](../getting-started/command-map.md), [login reference](../getting-started/login-reference.md), [lease/slot reference](../getting-started/lease-slot-reference.md), [rpc/fleet reference](../getting-started/rpc-fleet-reference.md) |
| What the server sees | [What turtle-agent is](../getting-started/what-it-is.md), [Settings reference](../getting-started/settings-reference.md) (`addons`, `shared_fingerprint`), [Log troubleshooting](https://github.com/pjerra/turtle-agent/blob/dev/docs/okf/getting-started/troubleshooting.md) (Warden section) |
| Test slots, leases and teardown | [Slots and leases concepts](../getting-started/slots-leases-concepts.md), [Lease a slot](../getting-started/lease-slot.md), [lease/slot reference](../getting-started/lease-slot-reference.md), [rpc/fleet reference](../getting-started/rpc-fleet-reference.md) (token, envelope, exits) |
| JSON-RPC API | [Drive the agent with RPC](../getting-started/drive-rpc.md), [RPC reference](../agent/rpc-reference.md) (envelope, codes) |
| RPC authentication | [Drive the agent with RPC](../getting-started/drive-rpc.md), [Settings reference](../getting-started/settings-reference.md) (`rpc_auth`, `rpc_token`) |
| Python (no client in this repo) | [Drive the agent with RPC](../getting-started/drive-rpc.md) (token note) |
| Layout | [Code layout](layout.md) |
| Licence | README only (kept); no docs page |

## The agent and its RPC

| Old README section | Docs pages that now hold it |
|---|---|
| LFG (Turtle group finder) | [Addons, LFG and tuning RPC](../agent/rpc-advanced-reference.md) (reference), [Queue through LFT](../combat/guide-queue-lft.md) (guide) |
| Items, loot and mounts | [Talking, trading and the world](../agent/social-guide.md) (§§5-6), [RPC reference](../agent/rpc-reference.md) (method rows) |
| Trading | [Talking, trading and the world](../agent/social-guide.md) (§7), [RPC reference](../agent/rpc-reference.md) |
| Game objects | [Talking, trading and the world](../agent/social-guide.md) (§9), [World state](../agent/world-concept.md), [RPC reference](../agent/rpc-reference.md) |
| Vendors, trainers and quests | [Talking, trading and the world](../agent/social-guide.md) (§8), [RPC reference](../agent/rpc-reference.md) |
| Movement | [Moving](../agent/movement-guide.md), [Navigation RPCs](../navigation/rpc.md) |
| Falling and knockbacks | [Moving](../agent/movement-guide.md) (floor/landing section) |
| Death and the corpse run | [Dying and coming back](../agent/death-guide.md) (agent + leader corpse runs) |
| Unit positions | [Unit positions and unit state](../agent/units-state.md) |
| Unit state | [Unit positions and unit state](../agent/units-state.md) |
| Agent state | [Spell data and spell state](../combat/concept-spell-data.md) (spellstate, cooldowns, GCD, combo, `spells[]`, `cast_state`) |
| Fleet: several players at once | [Log several characters in](../getting-started/fleet-party.md), [rpc/fleet reference](../getting-started/rpc-fleet-reference.md) |

## Dungeon plans and checks

| Old README section | Docs pages that now hold it |
|---|---|
| Dungeon plans (format, loader, stepper, dungeongen) | [Anatomy of a plan file](../plans/plan-format.md), [Write a plan file](../plans/write-a-plan.md), [Draft, mapped, live-checked](../plans/plan-lifecycle.md) |
| Plan check | [Check a plan on the navmesh](../plans/check-a-plan.md), [plan-check, trash-check, path-check](../plans/ref-check-tools.md) |
| Patrol recorder | [Record a patrol](../plans/record-a-patrol.md), [plan scout, plan-probe, patrol-record](../plans/ref-scout-probe.md) |
| Plan scout | [Scout a plan before a run](../plans/scout-a-plan.md), [plan scout, plan-probe, patrol-record](../plans/ref-scout-probe.md) |
| Plan probe | [Probe the run leader's rows](../plans/probe-a-plan.md), [plan scout, plan-probe, patrol-record](../plans/ref-scout-probe.md) |
| Plan diff | [Export a migration](../plans/export-a-plan.md) (§5), [plan-export, plan-diff, route-edits](../plans/ref-export-tools.md) |
| Trash check | [See what a walk wakes](../plans/trash-check.md), [plan-check, trash-check, path-check](../plans/ref-check-tools.md) |
| Route oracle | [Compare a route with the navmesh](../plans/route-oracle.md), [route-oracle](../plans/ref-route-oracle.md) |
| Exporting plans for the TortoiseBots run leader | [Export a migration](../plans/export-a-plan.md), [From plan to run leader rows](../plans/run-leader-rows.md), [plan-export, plan-diff, route-edits](../plans/ref-export-tools.md), [Apply route edits](../plans/apply-route-edits.md) (§4 route-regen) |
| The Ragefire Chasm plan | [Dungeon notes](../plans/dungeon-notes.md) (RFC) |
| Zul'Farrak events: Gahz'rilla, the courtyard and Zum'rah | [Dungeon notes](../plans/dungeon-notes.md) (ZF events) |
| Zul'Farrak: the stairs event, Bly and Ukorz | [Dungeon notes](../plans/dungeon-notes.md) (ZF stairs) |
| The Uldaman plan | [Dungeon notes](../plans/dungeon-notes.md) (Uldaman) |
| The Wailing Caverns plan | [Dungeon notes](../plans/dungeon-notes.md) (WC) |
| The Deadmines plan | [Dungeon notes](../plans/dungeon-notes.md) (DM) |

## Dungeon runs and observability

| Old README section | Docs pages that now hold it |
|---|---|
| Dungeon runs | [Run a commander dungeon](../dungeons/run-commander.md), [Run a leader dungeon](../dungeons/run-leader.md) (start, teleport, from_leg, resume), [Run calls](../dungeons/ref-run.md) (`dungeon_status` snapshot) |
| Run modes | [Run modes](../dungeons/modes.md), [Run a leader dungeon](../dungeons/run-leader.md) |
| Run watchdog | [Refusals, the watchdog, and the entry ledger](../dungeons/refusals-watchdog.md) |
| Remote leader | [Run a commander dungeon](../dungeons/run-commander.md) (threat, tank-first, flee), [Remote-control calls](../dungeons/ref-remote.md) |
| Targets by entry | [Loot and gossip](../dungeons/loot-gossip.md) (entry targets and view) |
| Prepare: one step before the bind | [Hire the party and enter](../dungeons/hire-party.md) (§1) |
| Hired party (prepare party "hire") | [Hire the party and enter](../dungeons/hire-party.md) (§§2-4: hire, raid-only, portal floor, enter ceilings) |
| Start anywhere and the pull test | [Start anywhere](../dungeons/start-anywhere.md), [Score one pack](../dungeons/pulltest.md) |
| Live tuning | [Tune a live run](../dungeons/live-tuning.md) |
| Run traces and checks, Schema `bt-trace` v1, Checks | [Run traces](../observability/traces-concept.md), [The trace checks](../observability/trace-checks-concept.md), [Re-score a trace](../observability/trace-score-guide.md) |
| Dashboard replay, fight-score | [Replay a recording](../observability/dashreplay-guide.md), [Score a finished fight](../observability/fight-score-guide.md) |
| Scenarios | [Repeat a live test with scenarios](../observability/scenarios-guide.md), [Command and flag reference](../observability/commands-reference.md) |
| Observe mode, Staging a leg for the run leader | [Watch a run leader](../dungeons/run-observe.md), [Run modes](../dungeons/modes.md), [Start anywhere](../dungeons/start-anywhere.md) (§5 staging) |
| Map images | [Draw a run map](../observability/map-guide.md) |
| Live dashboard | [Start and run the dashboard](../observability/dashboard-guide.md), [Control a run](../observability/run-controls-guide.md), [Turn on route editing](../observability/route-editing-guide.md) |
| Minimaps | [Put a real map under the run](../navigation/minimaps.md) |
| Navmesh walking | [Walk the navmesh](../navigation/walk-navmesh.md), [The navmesh and the planner](../navigation/navmesh.md), [Navigation RPCs](../navigation/rpc.md), [Check a route before the run](../navigation/checks.md), [How the server walks](../navigation/serverpath.md), [What wakes when you walk](../navigation/aggro.md) |

## Raids

| Old README section | Docs pages that now hold it |
|---|---|
| Raids | [Raid walks, autopilot and suite](../raids/overview.md), [Run modes](../dungeons/modes.md) (observe-only) |
| Following the lead bot as a player | [Follow the lead bot as a player](../raids/follow-player.md) |
| Raid walks | [Run a raid walk](../raids/run-a-raid-walk.md), [How placement works](../raids/placement.md), [`raid-walk` flags](../raids/raid-walk-reference.md), [Roster file format](../raids/roster-format.md), [Hire gear tiers](../raids/tiers.md), [Stay under the instance cap](../raids/manage-instance-cap.md) |
| --gm-clear (test servers only) | [Start partway into an instance](../raids/place-mid-raid.md) (§4) |
| --gm-instance-data (test servers only) | [Start partway into an instance](../raids/place-mid-raid.md) (§5) |
| Walk report | [Raid-walk report](../raids/report-reference.md) |
| Fault injection | [Fault injection](../raids/fault-injection.md) |
| Raid autopilot | [Supervise raids with the autopilot](../raids/run-autopilot.md), [`raid-autopilot` reference](../raids/raid-autopilot-reference.md) |
| Raid suite | [Regression-check plans with the suite](../raids/run-raid-suite.md), [`raid-suite` reference](../raids/raid-suite-reference.md) |
| Two raids at once | [Two raids at once](../raids/pair-reference.md) |

## Combat brain

| Old README section | Docs pages that now hold it |
|---|---|
| Bot brain | [The brain engine](../combat/concept-brain-engine.md) (ticks, bands, timing, back-off, threat, positioning, MoveLock, LOS), [The brain](../agent/brain-concept.md), [Brain and follow](../agent/brain-guide.md), [Run the brain](../combat/guide-run-brain.md) |
| Roles and classes (step 3c) | [The brain engine](../combat/concept-brain-engine.md) (roles, melee, warrior) |
| The healer and the priest (step 4a) | [Heal and priest](../combat/guide-healer-priest.md) |
| Rogue, hunter and retribution paladin (step 4b) | [Rogue, hunter, ret, warlock](../combat/guide-dps-rogue-hunter-ret-warlock.md) (rogue, hunter, ret) |
| Pets | [Drive the pet](../combat/guide-pets.md), [Talking, trading and the world](../agent/social-guide.md) (§10 pet RPC) |
| The druid and the shaman (step 4c) | [Druid and shaman](../combat/guide-druid-shaman.md) |
| The holy paladin and resurrecting the party (step 4e) | [Holy paladin and rez](../combat/guide-holy-paladin-rez.md) |
| Rotations as data | [Rotation file format](../combat/reference-rotation-format.md), [Add a rotation](../combat/guide-write-rotation.md), [Check your first rotation](../combat/tutorial-first-rotation.md), [Combat RPC](../combat/reference-rpc.md), [Combat CLI](../combat/reference-cli.md) |
| Boss tactics | [Add boss tactics](../combat/guide-boss-tactics.md) |
| Addon host | [Drive the addon host](../combat/guide-addon-host.md) |
| TBM contract test | [Drive the addon host](../combat/guide-addon-host.md) (contract section) |

## What the docs deliberately leave out

- `docs/CI.md`, `docs/TORTOISEBOTS-VISION.md`, `docs/PLANS.md`, `docs/LIVE-CHECKS.md`, `docs/ROTATIONS.md`: companion docs outside this bundle; the pages above link to them where the README did.
- Live anecdotes (run numbers, dates, walk ids): kept only where a docs page needs them as evidence; not carried as narrative.
- The README's stale claims (fixed to the code): RFC is `mapped`, not draft; `gm_cross` is still used by Naxxramas and Upper Karazhan Halls; exactly three raid tiers are sourced (`onyxias_lair:2`, `zul_gurub:1`, `ruins_of_ahnqiraj:1`); rest defaults are 90/60/35; `bad_targets` holds 2 s (10 s only for a self-target); the auto-tried Opening spells are 6477, 3365, 6247, 6478, 22810 (21651 explicit only); mesh-unavailable exits are 2, not 1.
