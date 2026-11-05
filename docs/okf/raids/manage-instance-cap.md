---
type: Guide
title: Stay under the instance cap
description: How the per-account instance ledger works and how to wait, reset or walk around it.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/raidwalk/instcap.go
tags: [raids, instance-cap]
sources:
  - id: instcap
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/raidwalk/instcap.go
  - id: walk-instcap
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/raid_walk_instcap.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:40:00+01:00
status: draft
---

# Stay under the instance cap

The core lets an account enter only N distinct instance ids an hour, and its list lives in the worldserver's memory. Every walk that enters a fresh instance is written to a per-server ledger (`instance-cap.json` beside the slots file), and the count is the core's own: per pool account, N fresh instance ids in the last hour.[^instcap]

## 1. Read the wait line

```text
instance cap: 4 fresh instances in the last hour on <server>; the core's cap is 5 per account and hour
instance cap: waiting 16m5s (earliest free time <time>) for the oldest of 4 ...
```

At the cap's raid count (one below `--instance-cap-per-account`) the walk waits up to `--instance-cap-wait` (default 20m) for the oldest entry to age out, then exits 26. `--instance-cap-wait 0` exits 26 at the count at once; `--instance-cap-fail` exits 26 at the count instead of waiting (the autopilot does not restart a 26: it keeps the cap wait and re-places later).

## 2. Set the cap for your server

`--instance-cap-per-account N` (default 5, the stock core) is the core's limit. The server's entry in `instance-cap-per-account.json` beside the slots file (`{"<server>": 30}`) is used when the flag is not given. A patched test core runs 30.

## 3. Walk around the cap

`--reuse-instance` (with `--place`) keeps the raid's current instance: nothing is entered, reserved or counted, so the cap is untouched. The check and the admission are one step under the ledger's lock: a walk below the count reserves a place (holder plus pid, 30 minutes) and the reservation counts like an entry, so walks that start together cannot all pass at the last place.

## 4. Reset after a worldserver restart

A worldserver restart clears the core's list, so the ledger must be cleared too:

```sh
turtle-agent raid-walk <plan> --server <server> --slot <slot> \
    --dir <plans-dir> --sql <world-sql> --instance-cap-reset
```

`--instance-cap-reset` forgets the server's entries. A corrupt ledger is moved to `instance-cap.json.corrupt-<stamp>` with a warning and a new one started; a ledger that cannot be read at all is never written over — the walk waits one minute and then exits 26. `--instance-cap-unsafe` goes on without a reservation instead of exiting 26; concurrent walks may then each create a fresh instance.
