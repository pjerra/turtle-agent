---
type: Concept
title: The instance cap ledger
description: What the per-server ledger counts, how reservations work, and why the count is per account.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/raidwalk/instcap.go
tags: [raids, instance-cap]
sources:
  - id: instcap
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/raidwalk/instcap.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:45:00+01:00
status: draft
---

# The instance cap ledger

The core lets an account enter only N distinct instance ids an hour (`MAX_INSTANCE_PER_ACCOUNT_PER_HOUR`), and its list lives in the worldserver's memory. The local test core is patched to 30; the stock core is 5. The ledger is the walk's local mirror of that list, so walks do not burn the hourly budget blindly.[^instcap]

## Per account, not per raid

Hires pack up to 10 to a pool account, so a re-placed raid soon meets the cap. The count is per pool account: N fresh instance ids in the last hour. After the last hire the walk reads the party's accounts with one read-only `.pinfo <hire>` per hire (the reply's account id; names are never stored) and records them on the entry. A ledger whose live entries all name their accounts counts the busiest account and waits at N; a ledger with an older entry that names no accounts keeps the conservative raid count at N-1.

## Reservations under one lock

The check and the admission are one step under the ledger's lock. A walk below N-1 reserves a place (holder plus pid, 30 minutes); the reservation counts like an entry, becomes the entry once the raid has entered, and is released on every exit before that — so walks that start together cannot all pass at the last place. The ledger file (`instance-cap.json` beside the slots file) also carries the `.debug instancedata` marks, which outlive the attempt's out dir.

## Aging and resets

The ledger drops every entry older than a worldserver start time when one is read; until a start time is exposed, `--instance-cap-reset` is the way to forget the server's entries after a restart. A corrupt ledger is moved aside (`instance-cap.json.corrupt-<stamp>`) with a warning and a new one started.
