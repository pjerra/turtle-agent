---
type: Guide
title: Log a character in
description: Log one character in with the login command, stay online, and log out cleanly.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/login.go
tags: [getting-started, login, rpc, logout, exit-codes]
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

# Log a character in

`login` logs one character in and keeps it online with the RPC server running. Run one process per character.

## Steps

1. Log in with the account and the character:

   ```sh
   turtle-agent login --config <settings-file> --rpc 127.0.0.1:8765 <account> <character>
   ```

2. If the settings file holds exactly one account, leave the account name off:

   ```sh
   turtle-agent login --config <settings-file> <character>
   ```

   The name then stays out of `ps` and shell history. With several accounts in the file, the short form is refused: name one explicitly.

3. Watch stderr for the three stages:

   ```text
   realmd: authenticated, 1 realm(s)
   world: authenticated on <realm> (<server>)
   in world: <character> guid=<guid> map=<map> x=<x> y=<y> z=<z>
   rpc: listening on http://127.0.0.1:8765/rpc
   ```

4. Stay online as long as you need the character. Useful options:

   ```sh
   turtle-agent login --stay 10m <character>
   turtle-agent login --reconnect 5 <character>
   ```

   `--stay` logs out after that long. `--reconnect N` logs in again up to N times after an unplanned disconnect (it cannot be combined with `--capture`).

5. Log out with Ctrl-C. The agent logs out cleanly and the process ends at once. If the server refuses the logout (you are in combat), the agent says so once and retries for 35 seconds; a second Ctrl-C ends the process at once, and the character may stay in world.

## Exit codes

- `0`: logged out (`logout: complete`, or the server closed the socket after accepting the logout).
- `2`: bad flags, arguments or settings file.
- `3`: realmd rejected the login.
- `4`: realm missing or offline.
- `5`: world server rejected the session or the character.
- `6`: the server closed the connection.
- `8`: the server kept refusing the logout.
- `10`: the logout did not complete (no answer within 30 seconds, or the socket died first); the character may stay in world.

Every flag is listed in [login](/getting-started/login-reference.md).
