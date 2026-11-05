---
type: Guide
title: Log several characters in
description: Start one login process per party member with the fleet command and its fleet file.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/fleet.go
tags: [getting-started, fleet, party, login]
sources:
  - id: fleet
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/fleet.go
  - id: config
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/config/config.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:25:00+01:00
status: draft
---

# Log several characters in

`fleet` starts one `login` process per entry of a fleet file: a 5-player dungeon party, for example, each with its own RPC port. Passwords stay in the settings file; the fleet file names only accounts.

## Steps

1. Write the fleet file:

   ```json
   {
    "members": [
      { "account": "<account>", "character": "<character>", "rpc": "127.0.0.1:8771" },
      { "account": "<account>", "character": "<character>", "rpc": "127.0.0.1:8772" }
    ]
   }
   ```

   Every entry needs an account, a character and a loopback RPC address with a fixed port (no port 0: the supervisor polls it). Characters and ports must be distinct. Each account must exist in the settings file. A member's `"reconnect"` overrides the fleet's `--reconnect` for that member (`0` turns it off).

2. Start the fleet:

   ```sh
   turtle-agent fleet --config <fleet-file> --settings <settings-file> --stagger 3s --wait 3m
   ```

   Children start `--stagger` apart (default 2 seconds). When all are in world the supervisor prints a table of character, RPC address and state; each child's log goes to stderr with a `[<character>]` prefix.

3. Talk to one member through its own RPC port:

   ```sh
   token=$(cat <token-file-for-that-port>)
   curl -s -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
     -d '{"jsonrpc":"2.0","id":1,"method":"status","params":{}}' \
     http://127.0.0.1:8771/rpc
   ```

   Each child writes `rpc-<port>.token` next to the settings file. With `rpc_token` in the settings file every child shares that token.

4. Stop the fleet with Ctrl-C. It sends one interrupt to every child so they all log out cleanly; a child that has not exited after 60 seconds is killed. On Linux a dead supervisor's children still get an interrupt and log out instead of staying in world.

## Limits and exit codes

- realmd takes about 14 logins per 300 seconds from one address, so the fleet shares one login budget: a start that would be the 15th waits and says how long (`fleet: waiting 4m12s to log in ...: realmd allows 14 logins per 5m0s from one address ...`). `--wait` (default 2 minutes) bounds the wait for every character to reach the world.
- A child that exits non-zero is reported and the others keep running. `--stop-on-failure` stops the rest instead. An exit 10 (a logout that did not complete) never stops the others.
- Exit 0 only when every child exited 0; exit 9 when any child failed, was killed or could not be launched, or when the fleet is not fully in world after `--wait`.

Every flag is listed in [rpc, fleet, version](rpc-fleet-reference.md).
