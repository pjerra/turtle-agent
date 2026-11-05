---
type: Guide
title: Record and decode a capture
description: Record a session's packets, prove they hold no secrets, and decode them into events.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/capturecmd.go
tags: [captures, guide]
sources:
  - id: capturecmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/capturecmd.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:42:00+01:00
status: draft
---

# Record and decode a capture

## 1. Record a session

```sh
turtle-agent login --capture <walk-dir>/run.twcap <character>
```

`--capture` records decrypted world packets to a new file. It cannot be combined with `--reconnect`, and the file must not exist: captures are never overwritten. The command prints `capture: N records ...` at the end.

## 2. Prove it holds no secrets

```sh
turtle-agent scrub-check --config <walk-dir>/settings.json <walk-dir>/run.twcap
turtle-agent capture-stats <walk-dir>/run.twcap
```

`scrub-check` (`--config` defaults to the settings file) fails when the capture holds an account name or password from the settings file in any stored form. Run it on every fixture before committing. A clean file prints `<path>: clean, N records`. `capture-stats` prints one line per opcode plus a `TOTAL` line; it takes no flags.

## 3. Decode the capture into events

```sh
turtle-agent capture events --kinds chat,spell_go <walk-dir>/run.twcap
turtle-agent capture events --from 2026-10-13T18:00:00Z --to 2026-10-13T19:00:00Z --out <walk-dir>/events.jsonl <walk-dir>/run.twcap
```

The capture's server packets go through the session's own handlers, so the rows are the events the `events` RPC returned while it was recorded: one JSON event per line. Flags may come before or after the capture path, but exactly one capture is required.

Every flag and its default:

| Flag | Default | What it does |
| --- | --- | --- |
| `--from` | none | only events at or after this RFC 3339 time |
| `--to` | none | only events at or before this RFC 3339 time |
| `--kinds` | none | only these event kinds, comma separated |
| `--out` | none | write to this file (mode 0600) instead of stdout; an existing file needs `--force` |
| `--force` | off | let `--out` replace an existing file (never a capture) (on/off: takes `=`, e.g. `--force=true`) |

Stderr always ends with the summary: `capture events: N of M events for <character>, from R records (Rr redacted, U server packets without a handler)`, the `kinds:` counts, and, for a capture cut off mid-record, `capture events: replay stopped early: ...`. Exit code is 2 on usage errors.
