---
type: Reference
title: login
description: Every login flag with its default, an example, and the login exit codes.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/login.go
tags: [getting-started, reference, login, flags]
sources:
  - id: login
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/login.go
  - id: main
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/main.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:25:00+01:00
status: draft
---

# login

```text
turtle-agent login [--config FILE] [--capture FILE] [--stay DURATION] [--rpc ADDR] [--reconnect N] [--create race,class[,gender]] [--tune FILE] [--pid-file FILE] [--follow LEAD --follow-plan PLAN [--follow-assist]] [<account>] <character>
```

Logs one character in and keeps it online with the RPC server running. One process per character. Flags work before, between or after the positionals.

## Positionals

- `<account>` (optional): settings-file account. Leave it off when the file holds exactly one account. Example: `login <character>`.
- `<character>` (required): character to play. Example: `login <character>`.

## Flags

- `--config FILE`: settings file. Default: the default settings path (`<home>/.config/turtle-agent/settings.json` where a home folder exists). Example: `--config <settings-file>`.
- `--capture FILE`: record decrypted world packets to this new file. Default: off. The file must not exist. Cannot be combined with `--reconnect`. Example: `--capture <capture-file>`.
- `--stay DURATION`: log out after this long in world. Default `0` (until interrupted or the logout RPC). Example: `--stay 10m`.
- `--rpc ADDR`: JSON-RPC listen address. Default: the settings file's `rpc` (`127.0.0.1:8765` when unset). Must be a loopback address; it is bound before realmd so a bad address never costs a login attempt. Example: `--rpc 127.0.0.1:8765`.
- `--reconnect N`: after an unplanned disconnect, log in again up to N times. Default `0` (off). Must be 0 or more. Example: `--reconnect 5`.
- `--create race,class[,gender]`: create the character at login when the account lacks it (numbers). Default: off (the world rejects a missing character instead). Example: `--create 1,2`.
- `--tune FILE`: tunables file of remote dungeon runs that name none. Default: `<plan>.tune.json` beside the plan (optional; a file named by `tune` must exist). Example: `--tune <tune-file>`. See [Dungeon runs](/dungeons/index.md) ("Live tuning").
- `--pid-file FILE`: write this process's pid here (`0600`) and remove it at exit; slot setup's start line passes it. Default: none. Example: `--pid-file <walk-dir>/agent.pid`.
- `--login-budget ADDR`: loopback address of the fleet's shared login budget (set by `fleet`; its token is in the environment). Default: none. Not for direct use.
- `--follow LEAD`: follow the lead bot of a bot-led run as a player. Needs `--follow-plan`. Default: none. See [Raids](/raids/index.md).
- `--follow-plan PLAN`: plan file of the follow (its map, portal and entrance trigger). Default: none.
- `--follow-assist`: attack the lead bot's target while following (melee auto-attack only). Default: off.

## Exit codes

- `0`: logged out (`logout: complete`, or the server closed the connection after accepting the logout).
- `2`: bad flags, arguments or settings file.
- `3`: realmd rejected the login.
- `4`: realm missing or offline.
- `5`: world server rejected the session or the character.
- `6`: the server closed the connection.
- `8`: the server kept refusing the logout.
- `10`: the logout did not complete; the character may stay in world.
