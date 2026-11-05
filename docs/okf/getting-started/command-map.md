---
type: Reference
title: Command map
description: Every turtle-agent subcommand and which docs area covers it.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/main.go
tags: [getting-started, reference, cli]
sources:
  - id: main
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/main.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:25:00+01:00
status: draft
---

# Command map

Every `turtle-agent` subcommand, and where it is documented. This area owns `login`, `fleet`, `lease`, `slot setup`, `teardown`, `rpc` and `version`; the other areas own the rest.

## This area

- `login [<account>] <character>` — log one character in ([log in](/getting-started/log-in.md), [login flags](/getting-started/login-reference.md)).
- `fleet --config FILE` — log several characters in ([fleet](/getting-started/fleet-party.md), [flags](/getting-started/rpc-fleet-reference.md)).
- `lease acquire|renew|release|list` — manage slot leases ([leases](/getting-started/lease-slot.md), [flags](/getting-started/lease-slot-reference.md)).
- `slot setup <server> <slot> --holder NAME` — build a slot's client dir ([leases](/getting-started/lease-slot.md), [flags](/getting-started/lease-slot-reference.md)).
- `teardown <dir>` — end a slot run and free it ([leases](/getting-started/lease-slot.md), [flags](/getting-started/lease-slot-reference.md)).
- `rpc <dir> <method> [<params>]` — call a slot agent ([drive](/getting-started/drive-rpc.md), [flags](/getting-started/rpc-fleet-reference.md)).
- `version` — print `turtle-agent <version>`.

- `0` ok. `2` bad flags, arguments or settings file. `3` realmd rejected the login. `4` realm missing or offline. `5` world rejected the session or character. `6` server closed the connection. `7` scrub-check found a secret in a capture. `8` logout refused. `9` a fleet child failed or the fleet never got in world. `10` logout incomplete. `11` plan-check found a point the navmesh cannot take; also trace-score when a check failed. `12` route-oracle could not report every plan. `13` stage-check refused. `14` teardown with a failed step (also scenario checks). `15` lease/slot refusal, scenario aborted, or rpc cannot-reach. `16` the agent answered with a JSON-RPC error.

- [The agent and its RPC](/agent/index.md): the calls `rpc` sends (`status`, `gm`, `commander`, movement, combat, loot, vendors, quests).
- [Dungeon runs](/dungeons/index.md): `dungeon_*` calls, `plan-check`, `trash-check`, `stage-check`, `patrol-record`.
- [Raids](/raids/index.md): `raid-walk`, `raid-autopilot`, `raid-suite`, `decisions`.
- [Plans](/plans/index.md): `plan-export`, `plan-diff`, `plan-probe`, `plan-scout`, `route-oracle`, `route-edits`.
- [Navigation and world data](/navigation/index.md): `path-check`, `map`, `minimap-extract`.
- [Combat](/combat/index.md): `rotation`, `pulltest`, `fight-score`, `tune-diff`.
- [Observability and testing](/observability/index.md): `dashboard`, `dashboard-replay`, `capture`, `capture-stats`, `scrub-check`, `trace-score`, `scenario`.

## Exit codes

- `0` ok. `2` bad flags, arguments or settings file. `3` realmd rejected the login. `4` realm missing or offline. `5` world rejected the session or character. `6` server closed the connection. `7` scrub-check found a secret in a capture. `8` logout refused. `9` a fleet child failed or the fleet never got in world. `10` logout incomplete. `14` teardown with a failed step (also scenario checks). `15` lease/slot refusal (also rpc cannot-reach). `16` the agent answered with a JSON-RPC error.
