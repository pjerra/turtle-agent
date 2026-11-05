---
type: Tutorial
title: First login
description: Build turtle-agent, write the settings file, log a character in, and send one RPC call.
resource: https://github.com/pjerra/turtle-agent/blob/dev/README.md
tags: [getting-started, tutorial, login, rpc, settings]
sources:
  - id: readme
    resource: https://github.com/pjerra/turtle-agent/blob/dev/README.md
  - id: main
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/main.go
  - id: login
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/login.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:25:00+01:00
status: draft
---

# First login

This tutorial takes you from a fresh checkout to a character standing in the world that answers an RPC call. It takes about ten minutes plus the build.

## What you need

- A checkout of turtle-agent and a Go toolchain (see [Install and build](install.md)).
- A reachable test server and one account with a character on it.

## Steps

1. Build the binary:

   ```sh
   make build
   ```

   This writes `bin/turtle-agent`.

2. Create the settings file with mode `0600` (the agent refuses a file other users can read):

   ```sh
   touch <settings-file>
   chmod 600 <settings-file>
   ```

3. Put your server and account in it (placeholders stand for your real values):

   ```json
   {
     "realmd": "<server>:3724",
     "accounts": { "<account>": "<password>" }
   }
   ```

   Only `realmd` and `accounts` are required. Every key is listed in [Settings file](settings-reference.md).

4. Log the character in:

   ```sh
   bin/turtle-agent login --config <settings-file> --rpc 127.0.0.1:8765 <character>
   ```

   When the settings file holds exactly one account, leave the account name off: it then stays out of `ps` and shell history. Watch stderr: `realmd: authenticated`, then `world: authenticated`, then `in world: <character> ...`, then `rpc: listening on http://127.0.0.1:8765/rpc`.

5. From another shell, ask the agent for its status. First find the token file the agent wrote next to the settings file (its name ends in `.token` and names the port), then:

   ```sh
   token=$(cat <token-file>)
   curl -s -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
     -d '{"jsonrpc":"2.0","id":1,"method":"status","params":{}}' \
     http://127.0.0.1:8765/rpc
   ```

   The answer names your character and its position. (Slot workers use `turtle-agent rpc` instead, which reads the token file for you; see [Drive the agent with RPC](drive-rpc.md).)

6. Log out cleanly: press Ctrl-C in the login shell. The agent logs the character out and exits 0. If the server refuses the logout (you are in combat), the agent retries for 35 seconds; a second Ctrl-C quits at once.

## What you have now

A built binary, a settings file with one account, and a character you can log in and drive. Next steps:

- [Log a character in](log-in.md) for staying online, timeouts and exit codes.
- [Write the settings file](write-settings.md) for folders (navmesh, plans, traces) and RPC tokens.
- [The agent and its RPC](../agent/index.md) for everything the agent can do once it is online.
