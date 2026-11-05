---
type: Concept
title: Captures
description: What a capture file records, what it redacts, and what the tools around it do.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/capture/capture.go
tags: [captures, fixtures]
sources:
  - id: capture
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/capture/capture.go
  - id: testfixture
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/testfixture/testfixture.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:41:00+01:00
status: draft
---

# Captures

A capture is the decrypted world packets of one session, written to a file (`TWCAP1` magic, then one record per packet: direction, time, opcode, body). It exists so a bug can be replayed offline: the packet parser, the session handlers and the map drawer all read captures instead of a live server.

What it holds: every decrypted world packet after the handshake, in both directions.

What it never holds:

- Realmd traffic, `CMSG_AUTH_SESSION`, and Warden bodies are dropped whatever they contain.
- Any packet containing a secret is dropped and counted as redacted: the account name, the password and its upper case, the session key and its hex forms, in raw, reversed, hex and upper/lower ASCII shapes.
- A secret shorter than 3 bytes is refused outright (`secret too short to redact safely`).

The file is mode 0600 and is never overwritten. A capture of an agent that was killed ends in a cut-off record; readers decode what is there and say so.

Three tools read captures: `capture-stats` counts packets by opcode, `capture events` replays the server packets through the session's own handlers into the event rows the `events` RPC would have returned, and `turtle-agent map` draws the run's positions from them.

## Test fixtures

The parser and session tests play back committed captures (`testdata/captures/m1-login.twcap` through `m7-gameobjects.twcap`, one per milestone's live step) through `internal/testfixture`: `Load(t, name)` reads one and fails the test when it is missing — the message says to record it with the milestone's live step. Run `scrub-check` on every fixture before committing. For fuzzing, `Bodies(t, ops...)` returns up to 12 distinct server bodies per opcode, and `FuzzBody` feeds arbitrary bytes to a parse function, failing on a panic or on no result after 5 s.
