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

- `login [<account>] <character>` — log one character in ([log in](log-in.md), [login flags](login-reference.md)).
- `fleet --config FILE` — log several characters in ([fleet](fleet-party.md), [flags](rpc-fleet-reference.md)).
- `lease acquire|renew|release|list` — manage slot leases ([leases](lease-slot.md), [flags](lease-slot-reference.md)).
- `slot setup <server> <slot> --holder NAME` — build a slot's client dir ([leases](lease-slot.md), [flags](lease-slot-reference.md)).
- `teardown <dir>` — end a slot run and free it ([leases](lease-slot.md), [flags](lease-slot-reference.md)).
- `rpc <dir> <method> [<params>]` — call a slot agent ([drive](drive-rpc.md), [flags](rpc-fleet-reference.md)).
- `version` — print `turtle-agent <version>`.

- [The agent and its RPC](../agent/index.md): the calls `rpc` sends (`status`, `gm`, `commander`, movement, combat, loot, vendors, quests).
- [Dungeon runs](../dungeons/index.md): the `dungeon_*` calls.
- [Raids](../raids/index.md): `raid-walk`, `raid-autopilot`, `raid-suite`, `decisions`.
- [Plans](../plans/index.md): `plan-export`, `plan-diff`, `plan-probe`, `plan-scout`, `route-oracle`, `route-edits`. `plan-check` and `trash-check` are covered in [plan-check, trash-check, path-check](../plans/ref-check-tools.md); `patrol-record` in [plan scout, plan-probe, patrol-record](../plans/ref-scout-probe.md).
- [Navigation and world data](../navigation/index.md): `path-check`, `map`, `minimap-extract`. `plan-check`, `trash-check`, `stage-check` and `patrol-record` flags are listed in [Check commands](../navigation/check-commands.md).
- [Combat](../combat/index.md): `rotation`, `pulltest`, `fight-score`, `tune-diff`.
- [Observability and testing](../observability/index.md): `dashboard`, `dashboard-replay`, `capture`, `capture-stats`, `scrub-check`, `trace-score`, `scenario`. `stage-check` and `trash-check` examples live in [Offline plan checks](../observability/offline-checks-guide.md) with flags in [Commands](../observability/commands-reference.md).

- `0` ok. `1` the navmesh could not answer (`path-check` query failed, `plan-probe` did not run, `plan-scout` checked nothing). `2` bad flags, arguments or settings file. `3` realmd rejected the login. `4` realm missing or offline. `5` world rejected the session or character. `6` server closed the connection. `7` scrub-check found a secret in a capture. `8` logout refused. `9` a fleet child failed or the fleet never got in world. `10` logout incomplete. `11` plan-check found a point the navmesh cannot take; also trace-score when a check failed. `12` route-oracle could not report every plan. `13` stage-check refused. `14` teardown with a failed step (also scenario checks). `15` lease/slot refusal, scenario aborted, or rpc cannot-reach. `16` the agent answered with a JSON-RPC error. `17` dashboard-replay refused (non-loopback target without `--i-know`). `18` a raid-walk phase failed or was refused; also plan-scout live phase refused. `19` raid-walk ran but a wipe, boss engagement or left hires tore it down. `20` plan-probe found a bad segment. `21` trash-check found the named class with `--fail-on`. `22` patrol-record stopped for safety (combat, death, lost world); the file is still written. `23` raid-walk would pass but an injected fault failed. `24` raid-suite regression or walk failure. `25` plan-diff found a change with `--fail-on-change`. `26` raid-walk hit the hourly instance cap (or the ledger was unreadable) and was not allowed past it. `27` plan-scout step failed (or warned with `--fail-on warn`). `28` plan-scout live pass stopped part-way. `29` raid-walk placement failed every try in the same instance. `31` raid-autopilot escalated at least one id for a person.
