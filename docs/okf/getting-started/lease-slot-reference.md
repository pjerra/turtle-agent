---
type: Reference
title: lease, slot setup, teardown
description: Every lease, slot setup and teardown flag, printed line, and refusal, with defaults.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/lease_cmd.go
tags: [getting-started, reference, lease, slots, teardown]
sources:
  - id: lease
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/lease_cmd.go
  - id: slot-setup
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/slot_setup_cmd.go
  - id: teardown
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/teardown_cmd.go
  - id: cli-args
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/cli_args.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:25:00+01:00
status: draft
---

# lease, slot setup, teardown

## lease acquire

```text
turtle-agent lease acquire <server> (--slot ID | --any) --holder NAME [--ttl MIN] [--slots FILE]
```

- `--slot ID`: slot id to take. Example: `--slot 1`. Pass this or `--any`.
- `--any`: take the first free slot of the server. Example: `--any`.
- `--holder NAME`: lease holder name (required). 1-64 letters, digits, `.` `_` `-`. Example: `--holder <name>`.
- `--ttl MIN`: lease length in minutes, 1-720. Default `180`. Example: `--ttl 60`.
- `--slots FILE`: slot table file. Default: the default slot table path. Example: `--slots <slots-file>`.

Prints `leased <server>/<slot> to <holder> until <time>: account <account>, character <character>, port <port>, lease <id>`, plus `(already yours: expiry renewed)` when you re-acquire your own slot, or a `took over a stale lease ...` line first when the old lease was stale. Refusals exit 15: `lease: <server>/<slot> is reserved (...)`, `is held by <holder> since ... until ...`, or a busy port line naming the listener. Usage errors exit 2.

## lease renew

```text
turtle-agent lease renew <server> <slot> --holder NAME --lease-id ID [--ttl MIN] [--slots FILE]
```

- `--holder NAME`: required, same shape as above.
- `--lease-id ID`: lease id from acquire (required; the holder alone is not enough). Example: `--lease-id <id>`.
- `--ttl MIN`: new length in minutes, 1-720. Default `180`.
- `--slots FILE`: slot table file. Default: the default slot table path.

Prints `renewed <server>/<slot> for <holder> until <time>`. Refusals exit 15 (`is not leased by ... (free|held by ...|lease id mismatch|stale)`). Forgetting `--lease-id` exits 2.

## lease release

```text
turtle-agent lease release <server> <slot> --holder NAME [--lease-id ID] [--force] [--slots FILE]
```

- `--holder NAME`: required, same shape as above.
- `--lease-id ID`: required unless `--force` is passed. Example: `--lease-id <id>`.
- `--force`: release another holder's or a live lease. Default: off. Example: `--force`.
- `--slots FILE`: slot table file. Default: the default slot table path.

Prints `released <server>/<slot> (was <holder>)`, plus ` (forced)` with `--force`. Refusals exit 15 (`only the holder releases it`, `lease id mismatch`, `its agent ... is still running: run turtle-agent teardown <dir> first`).

## lease list

```text
turtle-agent lease list [<server>] [--json] [--slots FILE]
```

- `[<server>]`: optional server filter. Example: `lease list <server>`.
- `--json`: print the slot states as JSON (account names redacted). Default: off.
- `--slots FILE`: slot table file. Default: the default slot table path.

Text form prints one line per slot: server, slot, character, port, and state (`free`, `reserved: <reason>`, `stale: held by ...`, `held by ... since ..., expires ...`, each with agent and dir detail).

## slot setup

```text
turtle-agent slot setup <server> <slot> --holder NAME [--dir DIR] [--binary PATH] [--dungeons-dir DIR] [--mmaps-dir DIR] [--dungeons-from DIR] [--character NAME] [--slots FILE]
```

- `--holder NAME`: required, same shape as above.
- `--dir DIR`: client dir. Default: `ta-<holder>` under the home folder. Must be new or empty; setup never overwrites and never writes through a symlink. Example: `--dir <walk-dir>`.
- `--binary PATH`: turtle-agent binary to install. Default: the running binary. Example: `--binary bin/turtle-agent`.
- `--dungeons-dir DIR`: dungeons dir. Default: `<dir>/dungeons`. Example: `--dungeons-dir <plans-dir>`.
- `--mmaps-dir DIR`: navmesh folder written to `settings.json` `mmaps_dir`. Must exist and be a folder. Default: the server's or the source settings' folder. Example: `--mmaps-dir <mmaps-dir>`.
- `--dungeons-from DIR`: folder of `*.json` plans copied into the slot's dungeons dir (plain files only; a symlink is refused). Default: none. Example: `--dungeons-from <plans-dir>`.
- `--character NAME`: character to play. Default: the slot's. Must be one of the slot's characters. Example: `--character <character>`.
- `--slots FILE`: slot table file. Default: the default slot table path.

Prints five lines (ready, account/character/port, start, rpc, teardown), plus `mmaps: <dir>` and `dungeons: <n> plans copied from <dir>` when those flags were given. The dir holds `turtle-agent`, `settings.json`, `slot.json`, `dungeons/` and `traces/`. Refusals exit 15 (not leased by you, reserved, busy port, bad maps folder, lease lost before the write). Usage and safety refusals exit 2.

## teardown

```text
turtle-agent teardown [--keep-lease] [--release-anyway] [--gm off|on|keep] [--dry-run] [--json] [--slots FILE] <dir>
```

- `--keep-lease`: keep the slot lease. Default: off (released).
- `--release-anyway`: release the lease even when game-side steps failed. Default: off.
- `--gm MODE`: GM mode at the end: `off`, `on` or `keep`. Default `off`. Example: `--gm on`.
- `--dry-run`: ask the agent for a plan, change nothing. Default: off.
- `--json`: print the result as JSON. Default: off (text table plus `teardown: <ok> ok, <skipped> skipped, <failed> failed`).
- `--slots FILE`: slot table file. Default: the dir's own recorded path.

Runs game-side steps over RPC (run, binding, hires, GM, logout), then agent exit, port, other-process, file and lease steps. A dead agent leaves an `agent pid ... is gone without a clean exit` failure. Exit `0` when every step passed or was skipped; exit `14` when any failed. With game-side failures the lease is kept unless `--release-anyway` is passed.
