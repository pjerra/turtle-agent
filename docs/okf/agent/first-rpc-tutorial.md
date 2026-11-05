---
type: Tutorial
title: Your first RPC calls
description: Log an agent in, look around with status and units, and log back out.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/rpc_cmd.go
tags: [agent, rpc, tutorial, status, logout]
sources:
  - id: rpc-cmd-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/rpc_cmd.go
    title: The rpc subcommand (usage, timeouts, exit codes)
  - id: methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/methods.go
    title: status, logout, events
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T22:45:00+01:00
status: draft
---

# Your first RPC calls

You drive a running agent with `turtle-agent rpc <walk-dir> <method> [params]`. This walkthrough logs the character in, asks where it is, and logs it out. It assumes the slot is already set up (see [logging in](login-guide.md)); it leaves nothing running.

## 1. Log the character in

Start the agent in the background. It stays in the foreground of its own process; your shell comes back at once:

```sh
turtle-agent login --config <walk-dir>/settings.json --pid-file <walk-dir>/agent.pid <character> &
```

Wait for the `in world` line in its output. Every later step needs `state: "in_world"`.

## 2. Ask where the character is

```sh
turtle-agent rpc <walk-dir> status
```

You get the session state, map id, position, health and power. If the agent is not reachable you see `rpc: cannot reach the agent at 127.0.0.1:<port>...` with exit 15; if it is still logging in, `status` answers with `state` different from `in_world` — wait and retry.

## 3. Look around

```sh
turtle-agent rpc <walk-dir> units '{"within": 40, "limit": 10}'
```

Each row is a nearby unit with its distance in yards, health and hostility. An empty list means nobody is in view, not an error.

## 4. Watch the event log

```sh
turtle-agent rpc <walk-dir> events '{"since": 0, "limit": 20}'
```

The reply carries `events`, `next`, `oldest`, `dropped` and `more`. Keep `next` and pass it as `since` next time; that is your cursor.

## 5. Log out

```sh
turtle-agent rpc <walk-dir> logout
```

The answer is `{"outcome": "complete"}` and the agent process exits. A `timeout` or `lost` outcome arrives as an error with `data.outcome` — the character may stay in the world for a while either way.

You have now done the whole loop: start, observe, stop. Next try [moving](movement-guide.md) the character.
