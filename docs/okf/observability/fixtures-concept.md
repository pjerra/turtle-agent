---
type: Concept
title: Test fixtures
description: The recorded captures the unit tests play back, and how to use them.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/testfixture/testfixture.go
tags: [fixtures, testing]
sources:
  - id: testfixture
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/testfixture/testfixture.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:53:00+01:00
status: draft
---

# Test fixtures

The parser and session tests do not need a server: they play back recorded captures committed under `testdata/captures` (e.g. `m1-login.twcap` through `m7-gameobjects.twcap`, one per milestone's live step). `internal/testfixture` loads them for tests only.

`Load(t, name)` reads `testdata/captures/<name>` from the repository root and fails the test when the fixture is missing or unreadable — the message says to record it with the milestone's live step. Run `scrub-check` on every fixture before committing: a committed capture must hold no account name or password.

For parser fuzzing, `Bodies(t, ops...)` returns up to 12 distinct server bodies per opcode (shortest, longest, even spread) across the milestone captures, and `FuzzBody` feeds arbitrary bytes to a parse function, failing on a panic or on no result after 5 s. The nightly fuzz job runs every `Fuzz*` target in `internal/game`, `internal/objects` and `internal/tbm` for 2 minutes each.

Use a fixture in a test like this:

```go
recs := testfixture.Load(t, "m7-gameobjects.twcap")
bodies := testfixture.Bodies(t, opcode.SMSG_UPDATE_OBJECT)
```
