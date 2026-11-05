---
type: Concept
title: Eluna test hooks
description: The one tt call that sends test-toolkit commands and returns the server's replies.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/eluna_methods.go
tags: [combat, eluna, concept]
sources:
  - id: eluna-methods
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/eluna_methods.go
  - id: eluna-client
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/eluna/client.go
  - id: eluna-ttjson
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/eluna/ttjson.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:50:00+01:00
status: draft
---

# Eluna test hooks

The `tt` call is one Eluna test-toolkit command to the TEST server: the agent sends `.tt <cmd> #<id>` as a GM line and answers with the `TTJSON` replies that carry that id. A GM tool (the server refuses accounts below rank 3 itself), for hand queries and run setup.

## The exchange

You send the toolkit command without the `.tt` prefix and without a request id; the agent adds the id. The command must fit one chat line (at most 200 characters), stay on one line, and carry no `#`. The agent waits up to 30 s, long enough for slow commands and the verified second read. Replies come back as sent, in order: one for a one-shot query, two for `quiet` and `place` (read 1, then read 2).

```json
{"method": "tt", "params": {"cmd": "q units 40"}}
```

```json
{"replies": [{"id": "ta1", "ok": true, "read": 1, "final": true, "...": "..."}]}
```

## Failures

A refused command keeps its toolkit code in the error data (`err`) with the refusing reply, so you can see what the server said. No answer in time is a timeout, with whatever replies did arrive. See [Combat troubleshooting](/combat/troubleshooting.md) for the exact texts.
