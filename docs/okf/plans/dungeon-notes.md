---
type: Concept
title: Dungeon notes
description: Per-dungeon evidence, limits and first-run risks for the five hand-written plans.
resource: https://github.com/pjerra/turtle-agent/blob/dev/dungeons/embed.go
tags: [plans, dungeons]
sources:
  - id: plans-doc
    resource: https://github.com/pjerra/turtle-agent/blob/dev/docs/PLANS.md
generated:
  by: omp
  at: 2026-11-05T21:45:00+01:00
status: draft
---

# Dungeon notes

Evidence and first-run risks for the five hand-written plans: Ragefire Chasm, Zul'Farrak events, Zul'Farrak stairs, Uldaman, Wailing Caverns, and Deadmines. RFC is `mapped (not live-checked)`; the rest are mapped except Deadmines (draft).

## Ragefire Chasm (map 389, band 13-18, mapped)

Nine legs, 55 groups, 86 mobs, about 1541 yd of corridors in navmesh order (tunnel, trogg cave, Oggleflint first, molten cave, Taragaman over lava, Jergosh, Bazzalan last). Entrance trigger 2230; portal and teleports named in the plan. Six patrols measured; radii exclude the farthest member. `tank_min_hp` 800. `fleers` 11319/11324; `kill_first` 11319. Taragaman stands over lava; Bazzalan's last group stands 30 yd out. Left-out: terrace loop (320 yd) and south camps past 25 yd. First-run risk: lava is the commonest death; Bazzalan adds need the stand.

## Zul'Farrak events (Gahz, courtyard, Zumrah)

Gahz'rilla: gong stand with 8 packs listed within 45 yd; `via_item` Gong with lock 459; boss wait up to 30 s. Skipped without the Mallet (no bot-run source for it); `give_items` covers tests only. Courtyard: 7 groups listed; patrols radius 30. Zumrah: trigger entry 962 at the campfire; `kill_first` and summoned entries per the encounter; graveyard route keeps 8 yd from graves and 3.5 yd minimum. Graves run in batches of 3-6 within 15 yd of the centre; carrier opens, threat waits, then re-mutes. First-run risks: Mallet missing, unrun trap fire and zombie counts, unrun bots without the trigger entry.

## Zul'Farrak stairs, Bly, Ukorz

Stairs top holds on the pyramid event value (cage to 3, Nekrum to 7); stairs base burns Nekrum with interrupts on heal. Weegli door: wait, tank gossip, then wait on the End object active state. Bly: wait, tank gossip, boss with first entries; gossip `dead_is_done` ends it when waves already died. Ukorz: spiral walk then boss with Ruuzlu first. Fail ends the run (no optional). First-run risk: phase is kept, so there is no restart mid-event.

## Uldaman (14 legs, entrance to last)

Sixty-three groups, 8 bosses, 4 Keepers; about 3534 yd. Altars are type-18 rituals (carrier plus 2, `cmd ritual` till active). The hole crossing west-to-east is unlinked: walk through live, hires get a `cmd move` ack at the pit. Doors shut on the mesh: the door step runs first (9 of 13). First-run risk: Keepers past Grimlok, Ancient side, and the Ironaya dead-end all backtrack.

## Wailing Caverns (map 43, band 18-23, mapped)

Twelve legs, 88 groups, 182 mobs, about 5360 yd to Verdan. Serpentis ramp is a plain direct step with `check_skip` (3.9 yd gap, 6 yd down, jump-down unclimbable). Kresh is one neutral-river group; critters and rares are skipped; 8 patrols measured. Naralex return (offline draft): lip exact step to foot, then exact plus `move_hop_exact` pairs back to the entrance hall; escort stops past 80 yd straight; waves pull after waits; Mutanus last. First-run risk: the return is draft and cuts at the return gossip.

## Deadmines (map 36, draft)

Sixteen legs, 136 groups, 45 patrols (26 timed), 7 bosses; about 5112 yd with zero stuck points on the walk. Doors are scripted deaths the mesh cannot prove (walk-through shut); the foundry and cannon levers need click plus wait-active. Catwalk point stays in aggro and keeps the plan draft until a live-aggro run settles it. First-run risks: script doors, the unverified catwalk z, lone-fleer waits, and golem crowd-control immunity.
