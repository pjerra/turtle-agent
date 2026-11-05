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
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/session/death.go
    title: Release, corpse query, reclaim, resurrect offer
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
