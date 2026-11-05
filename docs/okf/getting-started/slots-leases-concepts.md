---
type: Concept
title: Slots and leases
description: One slot per worker, lease identity, expiry, and how stale leases change hands.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/slots/lease.go
tags: [getting-started, concept, slots, lease]
sources:
  - id: lease
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/slots/lease.go
  - id: slots
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/slots/config.go
  - id: lease-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/lease_cmd.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:25:00+01:00
status: draft
---

# Slots and leases

One slot per worker. The slot table names each server's test characters; a lease hands one slot to one holder for a limited time. The holder is a short worker name (1-64 letters, digits, `.` `_` `-`).

## Lease identity

Every acquisition mints an unguessable 128-bit lease identity (32 hex characters). `renew`, `release` and `slot setup` must present the exact id: a holder name alone is never enough. Print it down when you acquire; `lease list` does not show it.

A lease records the server, slot, holder, lease id, acquire time, expiry, and the worker's dir once `slot setup` claims it. The default lease is 180 minutes (1-720). Renewing extends the expiry.

## States

- `free`: nobody holds it; acquire takes it.
- `held`: held by its holder until expiry. Acquiring your own slot again renews it (`already yours: expiry renewed`).
- `reserved`: the slot names a reason and is never leased.
- `stale`: expired AND no live agent in its dir. Acquiring takes it over and says whose it was, when it expired, and whether an agent is still running.

## Takeovers and safety

A live lease (not expired, or an agent still running in its dir) is never taken: acquire answers `is held by ...`, renew answers `is not leased by ... (held by ...)`, and release answers `only the holder releases it`. Releasing another holder's lease needs `--force`, and releasing a slot whose agent still runs is refused until you run `teardown` first.

Some slots can never be leased: the slot table marks them reserved by owner rule. `lease list` shows each slot's state, holder, expiry, running agent and dir; `--json` prints the same view with the account name redacted.
