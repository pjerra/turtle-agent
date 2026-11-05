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

Attempts wait 5 s, then 10 s, 20 s and so on up to 2 min. The agent keeps its own realmd logins under realmd's per-IP throttle (15 logins per 300 s; after a `busy` answer it waits the whole 300 s). In a fleet the supervisor books the wait for every child.

A rejection that cannot change (unknown account, wrong password, ban, build) stops the attempts at once, since failed logins count towards realmd's autoban. The RPC address and the event log stay the same across a reconnect, so event cursors stay valid; a `session` event with state `reconnected` marks the new session. Out of attempts the agent behaves as without the flag: status stays up for 30 s, then exit 6.

## Exit codes

- `0`: logged out (`logout: complete`, or the server closed the socket after accepting the logout).
- `2`: bad flags, arguments or settings file.
- `3`: realmd rejected the login.
- `4`: realm missing or offline.
- `5`: world server rejected the session or the character.
- `6`: the server closed the connection.
- `8`: the server kept refusing the logout.
- `10`: the logout did not complete (no answer within 30 seconds, or the socket died first); the character may stay in world.

A logout from Ctrl-C, `--stay` or the `logout` RPC always ends the process at once. There is no disconnect grace window on this path.

- `0`: `complete` (`SMSG_LOGOUT_COMPLETE` arrived) or `closed_by_server` (server accepted with `SMSG_LOGOUT_RESPONSE` reason 0 then closed the socket; state `logged_out`).
- `10`: `lost` (socket died before accept; RPC `-32002`, state `disconnected`; the core keeps a dropped character in world for about 20 s, so this is not a logout) or `timeout` (no answer within 30 s; agent closes socket; RPC `-32004`; character may stay in world).

## If the logout looks lost but was clean

The 1.18.1 core sometimes closes the socket right after a GM's logout request without sending either packet, in the open world and inside instances. That ends as `lost`, although the server's `char.log` recorded a normal logout each time it was checked.

Every flag is listed in [login](/getting-started/login-reference.md).
