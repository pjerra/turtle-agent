---
type: Concept
title: Session and world connection
description: How the agent logs in, what the connection does, and how logout and reconnect work.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/session/session.go
tags: [agent, session, connection, login, logout, reconnect]
sources:
  - id: session-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/session/session.go
    title: Session states, Run loop, logout outcomes
  - id: worldconn-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/worldconn/conn.go
    title: Socket, auth handshake, packet cipher
  - id: packet-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/packet/writer.go
    title: Packet writer, GUID layout
  - id: opcode-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/opcode/opcodes_gen.go
    title: Generated 1.12 opcode table
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T22:45:00+01:00
status: draft
---

# Session and world connection

The session is the agent's connection to the world server and everything it learns through it. One session plays one character at a time.

## Login

`login` authenticates against realmd, picks the realm, then opens the world connection: it reads the 4-byte auth challenge, sends `CMSG_AUTH_SESSION` with the account name, a client seed and the session key, and starts the packet cipher. Build `5875` is sent as the client version[^worldconn-go].

The session moves through the states `connecting`, `char_select`, `logging_in`, `in_world`, `logging_out`, `logged_out` and `disconnected`[^session-go]. You can read the current one in `status`'s `state` field. A wrong character name is refused at once:

`character "<name>" is not on this account (characters: <list>)`

## Client-authentic packets

The agent sends only packets a real 1.12 client sends. Each packet layout lives in `internal/game` (a `Build*` function per action) and the opcode table is generated from the 1.12 header (`internal/opcode/opcodes_gen.go`). Writes past opcode `NUM_MSG_TYPES` (`0x33C`) are refused client-side:

`world: opcode 0x... is at or past NUM_MSG_TYPES; the server would close the socket`

Captures never store the auth session body, Warden bytes or secrets.

## Heartbeat

In world the agent pings every 30 seconds and expects the pong within 90 seconds. A missed pong ends in:

`no SMSG_PONG within the pong timeout`

## Logout outcomes

`logout` sends `CMSG_LOGOUT_REQUEST` and waits up to 30 seconds for `SMSG_LOGOUT_COMPLETE`. The result's `outcome` is `complete` or `closed_by_server` (the server accepted the logout, then closed the socket)[^session-go]. Two failures arrive as RPC errors that carry `data.outcome`: `lost` (the connection died before the server accepted) and `timeout`. A refused logout (in combat, falling) is a refused error with the server's reason:

`logout refused: <reason> (reason <n>)`

The `logout` RPC always exits the process once the logout ends.

## Reconnect

With `--reconnect N` the agent logs back in after an unplanned disconnect, up to N times. The wait starts at 5 seconds and doubles to a 2-minute cap. Realmd logins stay under 14 per 300 seconds; when realmd answers busy the agent waits out the whole window. Only a disconnect from inside the world reconnects; anything else serves `status` for 30 seconds (a grace window for your scripts) and exits with code 6[^reconnect].

`--reconnect` refuses to combine with `--capture`. The reconnect prints one line per attempt:

`reconnect: attempt <n> of <N> in <wait>`
