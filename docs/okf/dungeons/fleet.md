---
type: Guide
title: Run a fleet
description: Log many characters in at once with one supervised command.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/fleet.go
tags: [dungeon, fleet, guide]
sources:
  - id: fleet
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/fleet.go
    title: The fleet supervisor
  - id: fleet-budget
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/fleet_budget.go
    title: The shared login budget
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:45:00+01:00
status: draft
---

# Run a fleet

`fleet` starts one `login` child per member of a fleet file, waits until every character is in the world, prints a table, then supervises until you stop it or every child exits. Passwords stay in the settings file; the fleet file never carries one.

## 1. Write the fleet file

```json
{"members": [{"account": "<acct>","character": "<Name>","rpc": "127.0.0.1:8771"}]}
```

Each member needs `account` (a key in the settings file), `character` (unique, case-insensitively), and `rpc` (a loopback address with a fixed port; ports unique across members). Optional per member: `"reconnect": 5` (overrides the fleet's `--reconnect`; 0 turns it off) and `"create": "race,class"` (a `login --create` spec). Unknown fields are refused.

## 2. Start the fleet

```sh
turtle-agent fleet --config <fleet-file>
```

Starts are paced 2 s apart (`--stagger`), because the login server throttles logins. When all are in:

```text
CHARACTER  RPC              STATE
<Name>     127.0.0.1:8771   in_world
fleet: 1 character(s) in world; Ctrl-C logs them all out
```

Ctrl-C (or SIGTERM/SIGHUP) logs every character out cleanly, waiting up to 60 s per child before killing it. Exit code is 0 only if every child exited 0, else 9: a child failed, or not every character reached the world in time (`--wait`, default 2 min).

## 3. Tune the flags

| Flag | Default | What it does |
|---|---|---|
| `--config FILE` | required | The fleet file |
| `--settings FILE` | the default settings path | Settings file (login address, accounts) |
| `--stagger D` | `2s` | Pause between starting children |
| `--wait D` | `2m` | Give up if not every character is in world after this long |
| `--reconnect N` | `0` | Each child logs in again up to N times after an unplanned disconnect (0: off) |
| `--stop-on-failure` | off | Stop every child when one fails (default: report it and keep the others) |

## Why logins are budgeted

The login server refuses the 15th distinct account login from one address within 300 s. The fleet supervisor holds one shared budget of 14 logins per 300 s (plus 10 s slack, since it books before the child starts) and paces starts and reconnects against it. A delayed start logs `fleet: waiting <d> to log in <name>: realmd allows 14 logins per 5m0s from one address and the fleet has used them`. Logins from outside the fleet (a game client, another fleet) are not seen by the budget.
