---
type: Guide
title: Logging in
description: Start the agent in a slot, pick the flags you need, and read the login output.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/login.go
tags: [agent, login, cli, flags]
sources:
  - id: login-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/login.go
    title: Login flags and flow
  - id: cli-args-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/cli_args.go
    title: Slot, lease, rpc and teardown flags
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T22:45:00+01:00
status: draft
---

# Logging in

## 1. Log in inside a slot directory

```sh
turtle-agent login --config <walk-dir>/settings.json --pid-file <walk-dir>/agent.pid <character>
```

The account comes from the settings file when you pass only the character; pass `<account> <character>` to override it.

## 2. Pick the flags your run needs

- `--stay <duration>`: log out after this long in world. `0` (default) stays until you call `logout`.
- `--rpc <addr>`: listen address for RPC. Default is the settings file's `rpc` value.
- `--reconnect N`: log back in up to N times after an unplanned disconnect. Off by default; refuses to combine with `--capture`.
- `--capture <file>`: record decrypted world packets to a new file. Never combine with `--reconnect`.
- `--create <race>,<class>[,<gender>]`: create the character at login when the account lacks it (numbers).
- `--follow <lead>` with `--follow-plan <plan>`: follow the lead bot of a bot-led run as a player; add `--follow-assist` to attack the lead's target.
- `--tune <file>`: tunables file of remote dungeon runs that name none.
- `--login-budget <addr>`: loopback address of the fleet's shared login budget (set by `fleet`).
- `--pid-file <file>`: write the pid here (mode 0600) and remove it at exit.

## 3. Read the login lines

A good login prints `realmd: authenticated`, `world: authenticated`, then:

`in world: <character> guid=<hex> map=<n> x=<f> y=<f> z=<f>`

A wrong character name stops the login:

`character "<name>" is not on this account (characters: <list>)`

## 4. Call RPC against the slot

```sh
turtle-agent rpc [--timeout <dur>] [--raw] <walk-dir> <method> [<params-json>|-]
```

`--timeout` is a Go duration, default `30s`. `--raw` prints the result compactly. `-` reads the params from stdin (up to 1 MiB). Params must be a JSON object or array. Exit codes: 0 on success, 2 on bad flags or arguments, 15 when the agent cannot be reached (or answers a bad envelope), 16 when the agent answers with a JSON-RPC error (`error: <code> <message>`).

Unknown fields in the params object are rejected with `-32602`; unknown methods answer `-32601` (`Method not found: <m>`).
