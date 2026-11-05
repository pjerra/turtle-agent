---
type: Guide
title: Play in the party
description: Run in leader mode, where your own character leads the party and fights with the bot brain.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dungeon_modes.go
tags: [dungeon, leader, brain, guide]
sources:
  - id: dungeon-modes
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dungeon_modes.go
    title: Run modes and leader brain presets
  - id: dungeon-methods
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dungeon_methods.go
    title: dungeon_run RPC, validation, and run lifecycle
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:40:00+01:00
status: draft
---

# Play in the party

In leader mode your character is in the group, leads it, and fights, played by the bot brain. The brain starts at a leg's first rest gate and stops at the leg's end. Omit `mode` for the older group form, where the brain is optional.

## 1. Start the run

```sh
turtle-agent rpc <dir> dungeon_run '{"name":"<plan>","mode":"leader"}'
```

With `mode: "leader"`, an empty `brain` picks the preset of your character's class (role `dps` unless `role` says otherwise):

| Class | dps | tank | healer |
|---|---|---|---|
| Warrior | `arms_warrior` | `tank_warrior` | — |
| Paladin | `ret_paladin` | — | `holy_paladin` |
| Hunter | `marksman_hunter` | — | — |
| Rogue | `combat_rogue` | — | — |
| Priest | `shadow_priest` | — | `holy_priest` |
| Shaman | `enhancement_shaman` | — | `resto_shaman` |
| Mage | `frost_mage` | — | — |
| Warlock | `affliction_warlock` | — | — |
| Druid | `feral_cat_druid` | `bear_tank_druid` | `resto_druid` |

A class without a preset for the role is refused with the list. Name another preset with `brain`, another role with `role`, and a rotation file with `rotation` (it replaces the preset's class rotation; the report and trace name it with the file's digest).

## 2. Know what leader mode refuses

`from_pull_group`, `start_here`, and `have_items` go with commander mode (leader mode walks the party itself; `teleport` moves it). `give_items`, `tune`, `live_reload`, `force_start`, and the commander party fields (`lead_bot`, `bots`, `tank`, `healer`, `carrier`, `cc`, `team`, `preflight`, `on_end`, `keep_on_fail`, `enter`, `ignore_entry_limit`, `prepare`) are refused too. These stay available: `teleport` (GM-moves your character to the start and summons the party), `buffs` (preset `"world"` or spell ids, applied with `.aura`), `recover_wipe` (off by default: a wipe ends the run), `wait_sickness`, `no_shortcuts`, `setup`.

## 3. Score your own pull

A leader-mode pull test runs where the party stands, at the pack: leave `teleport` and `resume` out, and pass a `brain` (or `mode: "leader"`, which picks one). See [Score one pack](/dungeons/pulltest.md).

## 4. Where the run starts

Before a run the leader is in a group, in the world, on the plan's map: already inside, or `"teleport": true`, which GM-teleports it to the start (the `entrance` for leg 0, else where the previous leg ended), summons every companion (`.bot summon`) and waits up to 60 s for them.

`from_leg` (number or name) assumes the legs before it are cleared. Without `teleport` the party walks from where it stands to the leg and fights earlier packs as walk aggros (noted). With `"teleport": true` beside `from_leg` the leader is GM-teleported to the leg start, the party summoned, flagged `teleport_to_leg_start` in `gm_shortcuts`.

`resume: true` (off by default) skips what the party already passed with `from_leg`. The leg route is straight lines from its start through `to`, each pull's first position and the boss. Within 80 yd past `to` the walk to `to` is skipped. A pull behind is skipped only if a member stands within 25 yd of each position (the remote `ClearSeeRadius`) and no hostile is listed there; from the first kept group on everything is kept. The report has `resumed{leg,at,skipped_walk,skipped_groups}`. Straight lines, not the navmesh path; not verified live.

## 5. GM or player

A GM leader gets `.gm visible on` first and may use the GM shortcuts. `"gm": false` sends no GM command at all (no visible, no `.go`, no `.revive`, no `.aura`); `teleport` and `buffs` are then refused at the RPC before anything is sent.

Omitted `gm` probes once with `.account` and reads `Your account level is: N` in a 2 s window; other lines are ignored. No answer counts as player, with a note saying to pass `gm: true` (+`gm_level`) to override. `"gm": true` skips the probe (`gm_level` 1–6, 3 when omitted).

The level gates each command: `teleport` (L1), `visible` (L2), `revive`/`buffs` (L3). Below level the capability is off from the start and the report says so (`leader_gm`, `gm_caps`, `account_level`; answer `true`/`false`/`probe`).

## 6. Ambushes, walks, pulls

A walk is `walk_path`, tried up to 3 times. A walk is watched every 0.5 s; an attacking pack stops it, is fought (nearest attacker, till 3 s quiet), then the walk is remade. It costs no try, counts as an extra `pulls` + `walk_aggros`. Over 12 such fights in one walk, or a fight over 300 s, fails the run.

A pull targets the nearest living entry within 25 yd of a plan position (`radius` overrides) or fighting the party, approached to 38 yd when out of 45 yd. Distant same-entry packs are not members; a group whose own mobs are dead (corpse within radius) is taken as already dead. `pullback` pulls, the rest join after 5 s, re-targeted about every 10 s to what the tank holds best. No fight in 25 s or 12 s evade retries (≤3); a kill is progress and costs no try. Adds are fought up to 15 s.

A group all-stealthed in view (aura ids 5916, 22766, 1784-1787, 8822, 5215, 6783, 9913, 20580) or flagged `"ambush"` with none in view is not pulled: the leader walks positions, stands 6 s each, fights reveals. It clears when none is left alive in view or after passing with no fight (noted; leg `ambushes`).

## 7. Tank first, bosses

The leader targets what the party attacks (no threat list): tank is the warrior/paladin/druid (in order) whose target is a living hostile in combat, else the first in view. The tank-best mob goes first, then nearest. CC (by mechanic/name, or moon icon) is left unless last (`cc_respected`). A mob on a non-tank 3 s within 40 yd gets the tank via `.bot action pull 0` (`loose_add_orders`). The first attack waits 2 s for the tank hold. See `report.tank_first{tank,tank_threat_share,samples,loose_add_orders,cc_respected,target_switches}` (absent with no tank).

`kill_boss` fights `boss.first` then the boss as one fight. A boss is never taken as dead unseen: only its corpse in view counts, else the run fails.

## 8. Rest gates

A gate opens every leg and before every pull/boss, out of combat only, up to 60 s (`rest.max_s` overrides): 90% health, 60% healer mana, 35% other mana, within 40 yd. A flat mana bar 15 s gets one `.bot command <Name> u drink`; a caster flat 30 s no longer holds the gate (note) unless a healer is under 30% mana. Past the cap the run goes on noted if everyone has 50% health, else fails. Walking opens (first step walk ≥60 yd) let the party go at 70/40/25% (`walking`, skipped). `rest_share` is the run share in gates. The report keeps `rest_seconds`, `rest_gates[]{leg,seconds,max_s,result:rested/stalled/gave_up/failed,short[],drink_orders[],skipped,refills[]}`.

## 9. Refills, maintenance, buffs, death, wipes

`maintenance_every_legs` (default 3, 0 off) sends `.bot maintenance` on legs N, 2N…, never in combat. A remote gate refills: when a bot's `.bot inv` reads under 3 drinks (`RefillBelow`), one `.bot maintenance <bot>` per bot per leg goes out. See `remote.refills[]`, `remote.maintenance_runs/skips`, and the `maintenance`/`refill` events.

`buffs` (preset `world` = 22888, 15366, 24425, 16609, 22817, 22818, 22820, or ids/list ≤20) is a GM boost via `.aura` with per-companion selection checks (in view, ≤80 yd, ≤10 s wait together after a teleport; the first aura must show within 2×1.5 s, one resend, rest of the 10 s window). Every app is a `buff` in `gm_shortcuts`. A dead member is GM-`.revive`d (2 per member, brain gets 120 s first) unless `no_shortcuts`/non-GM. All dead is `wipe` (1) unless `recover_wipe` (release + corpse runs ≤12 min, ≤2/run).

## 10. Report and limits

`dungeon_status.report` keeps `legs[]` (pre-`from_leg`: `skipped (from_leg)`), `deaths[]`, `notes[]`, `refusals[]`, `gm_shortcuts[]` (`teleport_to_entrance`, `teleport_to_leg_start`, `revive`, `buff`), `buffs[]`, `setup_shortcuts[]` (≤40×400, never sent). Any shortcut means no player could have done it. Limits: no CC marks, no loot orders beyond `companions_loot`, no stuck detection beyond the bounds + watchdog; stealthed packs are walked, not pulled.
