---
type: Troubleshooting
title: Agent troubleshooting
description: Real error messages from the agent, what they mean, and the fix.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/session/session.go
tags: [agent, troubleshooting, errors, rpc]
sources:
  - id: session-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/session/session.go
    title: Session and logout errors
  - id: actions-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/session/actions.go
    title: Target, cast, walk and teleport errors
  - id: worldconn-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/worldconn/conn.go
    title: Connection and auth errors
  - id: rpc-cmd-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/rpc_cmd.go
    title: RPC client errors and exit codes
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T22:45:00+01:00
status: draft
---

# Agent troubleshooting

## Cannot reach the agent

`rpc: cannot reach the agent at 127.0.0.1:<port>: <reason>` (exit 15): the slot's agent is not running or the port is wrong. Start it with `login` in the slot dir and check the `<walk-dir>/agent.pid` file is fresh.

`rpc: bad reply from the agent: <reason>` (exit 15): the agent answered outside the JSON-RPC envelope. It may be starting up; retry.

`error: <code> <message>` (exit 16): the agent answered with a JSON-RPC error. The code tells you who refused: `-32601` unknown method, `-32602` bad params (unknown fields are rejected — check spelling), `-32001` not in world, `-32002` disconnected, `-32003` the game refused, `-32004` timed out.

## Login fails

`character "<name>" is not on this account (characters: <list>)`: the character name does not match the account. Check the spelling or create it with `--create`.

`SMSG_CHARACTER_LOGIN_FAILED (code <n>): character locked or already loading`: retry; the session retries code 1 up to 5 times itself.

`realmd <server> rejected: <reason> (code ...)`: the account, password or realm is wrong, the account is inactive or banned, or the IP hit the 15-logins-per-300-seconds limit. Wait and retry.

`no SMSG_PONG within the pong timeout`: the connection died silently. With `--reconnect N` the agent logs back in; otherwise restart it.

## Calls refused

`not in world (state <state>)`: the character is not logged in far enough. Wait for `in world` (check `status`).

`already walking`: a `walk_to` is still going. Wait for it, or call `walk_path` for longer moves.

`walk_to is for short moves: <n> yards is over 40, use walk_path or teleport`: split the move or use `walk_path`.

`path_to and walk_path need the server's mmaps: set "mmaps_dir" in the settings file`: the slot has no navmesh configured. Path calls cannot work there.

`teleport: no MSG_MOVE_TELEPORT_ACK or SMSG_NEW_WORLD within 10 s (does the account have GM rights?)`: the account is not a GM. Teleport needs GM rights.

`the brain is running: brain_stop it first`: `brain_start` while the brain runs. Stop it first — unless a dungeon run owns it (`brain_stop: a dungeon run owns the brain (dungeon_stop it first)`), then stop the run.

`a player follow is going: follow_stop it first` / `a dungeon run is going: it moves the character itself (dungeon_stop it first)`: only one driver moves the character. Stop the owner first.

`a teardown is going`: a `teardown` is already running. Wait for it.

## Loot, trade and NPC answers that look like failures

`no loot window is open; call loot first`: call `loot` (or `open_object`) before `loot_take`/`loot_close`.

`trade needs "to": a player's name or guid`: name the partner. `trade: <name> is <n> yd away, past trade range (10 yd ...)`: walk closer first.

`gossip: no menu is open; call gossip_hello first`: open the menu before `gossip_select`.

`accept_resurrect: no resurrection is on offer (SMSG_RESURRECT_REQUEST)`: nobody is offering (or the 60-second offer expired). Check `death_status`.

`use_object` returning `"outcome": "none"` with a `hint`: the object is too far, unusable or locked — not an RPC error. Walk closer and retry.

## Dungeon runs

`dungeon_run needs the character in the world (state <s>)`: log in first.

`a dungeon run is going (<plan>): dungeon_stop it first`: one run at a time.

`plan <p> is a raid plan: run it with mode "observe" and prepare.party "hire"`: raid plans do not run as leader/commander parties.

`dungeon_pause: no dungeon run is going`: nothing to pause.

`live tuning is for runs in mode "remote"`: `tune_set`/`tune_list` only work on commander-mode runs.

`logout refused: <reason> (reason <n>)`: the server refused the logout (in combat, falling). The agent retries; if retries run out it closes the socket (exit 8) and the character may stay in world.
