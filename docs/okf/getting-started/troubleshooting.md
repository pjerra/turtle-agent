---
type: Troubleshooting
title: Getting started troubleshooting
description: Real error messages from login, fleet, lease, slot setup and rpc, what they mean, and the fix.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/login.go
tags: [getting-started, troubleshooting, login, lease, slots]
sources:
  - id: login
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/login.go
  - id: lease
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/lease_cmd.go
  - id: slot-setup
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/slot_setup_cmd.go
  - id: teardown
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/teardown_cmd.go
  - id: rpc
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/rpc_cmd.go
  - id: config
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/config/config.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:25:00+01:00
status: draft
---

# Getting started troubleshooting

## Login fails before realmd

- `usage: turtle-agent login [flags] [<account>] <character>` (exit 2): wrong argument count. Pass one or two positionals.
- `config: settings <file> has mode <mode>; it holds passwords, run: chmod 600 <file>` (exit 2): the file is readable by group or others. Fix the mode.
- `config: settings <file> is not valid settings JSON: unknown field <name>` (exit 2): the file names a key the agent does not know. Delete or rename it.
- `config: "accounts" must map at least one account name to its password` (exit 2): the file has no accounts.
- `config: the settings file holds <n> accounts; name one: login <account> <character>` (exit 2): the short `login <character>` form needs exactly one account.
- `config: the requested account is not in the settings file` (exit 2): the account is missing (lookup ignores case). Check the spelling.
- `login: --reconnect must be 0 (off) or more` (exit 2): pass 0 or more.
- `login: --reconnect cannot be combined with --capture` (exit 2): drop one.
- `rpc: ...` before `realmd: authenticated` (exit 2): the RPC address is bad, not loopback, or busy. The RPC port binds before realmd so a bad address never costs a login attempt.

## realmd or world rejects the login

- `realmd: ...` (exit 3): the login server refused. Check the address, account, password and that the server is up.
- `world: ...` (exit 5): the world server rejected the session or the character (a missing character without `--create` ends here).
- `session: ...` with exit 6: the server closed the connection mid-login.
- `fleet: waiting 4m12s to log in <name>: realmd allows 14 logins per 5m0s ...`: you hit realmd's login limit from one address. Wait; the fleet books the wait itself.

## Logout and disconnect

- `logout: still refused after 35s; closing the connection, the character may stay in world` (exit 8): the server kept refusing the logout (in combat). Move out of combat and log out again.
- `logout: timed out without SMSG_LOGOUT_COMPLETE; connection closed, the character may stay in world` (exit 10): the server never answered within 30 seconds.
- `logout: ...; the character may stay in world for a while` (exit 10): the socket died before the server accepted the logout. The core keeps a dropped character in world for about 20 seconds; this is not a logout.
- `rpc: serving status for 30s after the disconnect (Ctrl-C to quit now)`: the session dropped but the RPC stays up read-only so `status` and `events` show why. Ctrl-C quits.

## One Warden timeout line per session

The agent drops `SMSG_WARDEN_DATA` and never sends `CMSG_WARDEN_DATA`. With no Windows module file in the server's module dir you see no Warden packet at all; with a module the server logs one `WARDEN: ... timeout` line per session and treats it as noise.

With a module the clock runs out after `Warden.Timeout` seconds (default 30). The core logs the timeout once per session and its `KickPlayer()` next to it is commented out. That commented line is the only thing keeping an agent online: a server that enables it kicks every agent about `Timeout` seconds after login. No config key does that; it is a source change. Other Warden kicks (failed challenge, failed module load, unknown opcode) need a client reply, which the agent never sends. Answering Warden is not implemented.

`WinEnabled` / `OSXEnabled` (`0` in the template and on the test server) gate the older non-`USE_ANTICHEAT` code; there `0` means no Warden. In the `USE_ANTICHEAT` build `WinEnabled` only feeds an assert, so `0` does not stop the request when a module is present. `Warden.Enable` is read but nothing tests it as far as the code shows. The old code kicks after `ClientResponseDelay` (120 s) without an answer. `PlayersOnly` and level gates only matter once scans are answered.

## Lease and slot setup

- `lease: --holder must be 1-64 letters, digits, . _ -` (exit 2): fix the holder name.
- `lease acquire: pass --slot ID or --any` (exit 2): pass exactly one of them.
- `lease: --ttl <n>: 1 to 720 minutes` (exit 2): pick a TTL in range.
- `lease: <server>/<slot> is held by <holder> since ... until ...` (exit 15): someone else holds it. Wait, renew nothing, or take another slot.
- `lease: <server>/<slot> is reserved (...)` (exit 15): the slot is never leased. Pick another.
- `lease: <server>/<slot>: port <port> has a listener on 127.0.0.1 (an agent outside the leases?): stop it or pick another slot` (exit 15): something already listens on the slot's port.
- `lease renew: pass --lease-id ...` / `lease release: pass --lease-id ...` (exit 2): the id from `lease acquire` is required; the holder alone is not enough.
- `lease: <server>/<slot> is not leased by ... (lease id mismatch: it was re-acquired)` (exit 15): the lease changed hands since your acquire. Acquire again.
- `slot setup: <server>/<slot> is not leased by <holder>: run turtle-agent lease acquire ... first` (exit 15): acquire (or renew) before setup.
- `slot setup: <dir> exists and is not empty: pick a new --dir (setup never overwrites)` (exit 2): pick an empty dir.
- `slot setup: --mmaps-dir <dir> is not a folder ...` (exit 15): point at the navmesh folder or drop the flag.
- `slot setup: <server>/<slot> allows ...` (exit 2): the slot only plays its own characters.

## rpc and teardown

- `rpc: the token file is not an agent token` (exit 15): the token file has a foreign shape. Log in again so the agent writes a fresh one; the content is never printed.
- `rpc: cannot reach the agent at 127.0.0.1:<port>: ...` (exit 15): the agent is not running or the port is wrong.
- `error: <code> <message>` (exit 16): the agent answered a JSON-RPC error. Fix the call; the agent is fine.
- `teardown: <dir>/slot.json missing or unreadable: teardown works on a dir made by slot setup` (exit 2): the dir is not a slot dir.
- `teardown: ... : run turtle-agent lease release <server> <slot> --holder <holder>` (lease step `fail`): game-side steps failed, so the lease was kept. Check the slot, then release.
- `agent pid ... is gone without a clean exit: game-side steps not run` : the agent died without logging out. Its hires end after the server's offline grace.
