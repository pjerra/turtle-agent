---
type: Guide
title: Dying and coming back
description: Read death state, release, find the corpse, reclaim it, and accept a resurrect.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/death_methods.go
tags: [agent, death, corpse, resurrect]
sources:
  - id: death-methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/death_methods.go
    title: death_status, release_spirit, corpse_query, reclaim_corpse, accept_resurrect, area_trigger
  - id: death-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/dungeon/death.go
    title: Leader death recovery, corpse run, portal, bounds
  - id: dungeon-driver-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/dungeon/driver.go
    title: Driver limits ResWait CorpseRunTimeout MaxLeaderDeaths SicknessMax
  - id: session-death-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/session/death.go
    title: Reclaim delay packet, corpse query, resurrect offer
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T22:45:00+01:00
status: draft
---

# Dying and coming back

## 1. Check whether you are dead

```sh
turtle-agent rpc <walk-dir> death_status
```

The answer is `{dead, ghost, resurrect?, reclaim_in_ms}`. `dead` is health 0 with the spirit not released; `ghost` is a released spirit. A `resurrect` offer (caster, sickness, timer) lasts 60 seconds.

## 2. Release the spirit

```sh
turtle-agent rpc <walk-dir> release_spirit
```

Refused while alive, or twice:

`release_spirit: the player is alive`

`release_spirit: the spirit is already released`

## 3. Find the corpse

```sh
turtle-agent rpc <walk-dir> corpse_query
```

The answer is `{found, map, x, y, z, corpse_map}`. Without a server answer within 5 seconds:

`corpse_query: no MSG_CORPSE_QUERY answer within 5s`

## 4. Reclaim it

```sh
turtle-agent rpc <walk-dir> reclaim_corpse
```

Walk the ghost within 39 yards of the corpse first. The reclaim delay the server sends after release must have run out:

`reclaim_corpse: the reclaim delay has <n> s left`

## 5. Or accept a resurrect

```sh
turtle-agent rpc <walk-dir> accept_resurrect
```

Needs a live offer:

`accept_resurrect: the player is alive`

`accept_resurrect: no resurrection is on offer (SMSG_RESURRECT_REQUEST)`

## 6. Step through area triggers on the corpse run

```sh
turtle-agent rpc <walk-dir> area_trigger '{"id": 123}'
```

Sends the trigger the client would send when stepping into its volume — the dungeon entrance on a corpse run. Without an id:

`area_trigger: needs a trigger id`

## 7. Leader corpse runs in dungeons

A leader that dies is not a wipe while a member lives: the driver pauses its orders and gets the leader back the way a player does, with no GM command. First it waits up to 60 s (`ResWait`) for a resurrection a living member offers (`SMSG_RESURRECT_REQUEST`, answered with `CMSG_RESURRECT_RESPONSE`); three answers that do not bring the leader back end the hope. Else it releases the spirit (`CMSG_REPOP_REQUEST`, up to 3 tries), runs as a ghost from the graveyard to the corpse along the navmesh, and reclaims it (`CMSG_RECLAIM_CORPSE`) within 34 yd of the corpse (the server accepts 39 yd; the ghost stops a few yards short so a position error never leaves the packet ignored) and past the server's reclaim delay (`SMSG_CORPSE_RECLAIM_DELAY`: the wait reads `waiting_reclaim_delay` with the milliseconds left).[^death-go]

A corpse in a dungeon is reached through the entrance portal: the ghost walks to the entrance and sends the portal's area trigger (`CMSG_AREATRIGGER` for the plan's `entrance_trigger`), which revives it on the way in. No `entrance_trigger` in the plan fails the run. A resurrection offered on the way is still accepted (at most 3 answers per phase). Back at the corpse the leader sends `.bot action follow`, gathers the party, and rests behind the rest gate; the brain stays off while the leader is dead or a ghost and comes back at the gate.[^death-go]

Bounds: a corpse run over 10 min fails the run (`corpse run: gave up after 10 min`); 5 leader deaths per run (`MaxLeaderDeaths`); at most 3 answers to offers per phase (`maxAccepts`). `wait_sickness` waits Resurrection Sickness out, up to 11 min (`SicknessMax`); else it is only noted. The report counts `leader_deaths`, `resurrections`, `corpse_runs` and `seconds_dead`, in total and per leg. The events carry `dungeon_leader{phase, leg, how, seconds, error, trigger, ms, mode}` with phases `died`, `resurrect_accepted`, `released`, `corpse_run`, `waiting_reclaim_delay`, `reclaimed`, `portal`, `back`, `gave_up` and `wipe_recovery`, plus `resurrect_request{caster, name, sickness, timer}`, `corpse_reclaim_delay{ms}`, and the follow mode's own `player_follow` recovery (`mode: follow`).[^death-go][^dungeon-driver-go]

