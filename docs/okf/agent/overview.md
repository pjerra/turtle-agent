---
type: Overview
title: The agent and its RPC
description: What one turtle-agent process is, how its parts fit, and where each thing is documented.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/methods.go
tags: [agent, session, rpc, brain]
sources:
  - id: methods-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/methods.go
    title: Core RPC methods (status, chat, movement, combat)
  - id: brain-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/brain/engine.go
    title: The brain's trigger-action engine
  - id: session-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/session/session.go
    title: Session lifecycle and world state
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T22:45:00+01:00
status: draft
---

# The agent and its RPC

One `turtle-agent login` process plays one character: it holds the world connection, keeps a live picture of what the character sees, and takes orders over RPC while you watch the event log.

The parts:

- **Session and world connection** — login, logout, reconnect, and the client-authentic packets ([session and world connection](/agent/session-concept.md)). The login command and its flags are in [logging in](/agent/login-guide.md).
- **The world as the agent sees it** — `status`, `units`, `party`, positions, auras ([world state](/agent/world-concept.md)).
- **Movement** — `walk_to`, `path_to`, `walk_path`, `teleport` ([moving](/agent/movement-guide.md)).
- **The brain** — the trigger-action engine that fights, rests and rezzes ([brain](/agent/brain-concept.md)); start it with [brain and follow](/agent/brain-guide.md).
- **Death** — release, corpse run, resurrect ([dying and coming back](/agent/death-guide.md)).
- **Every RPC call** — params, results, errors ([RPC reference](/agent/rpc-reference.md), [dungeon and party RPC](/agent/rpc-dungeon-reference.md), [addons, LFG and tuning RPC](/agent/rpc-advanced-reference.md)).
- **When something goes wrong** — exact error strings and fixes ([troubleshooting](/agent/troubleshooting.md)).

Start with [your first RPC calls](/agent/first-rpc-tutorial.md) if you have never driven an agent.
