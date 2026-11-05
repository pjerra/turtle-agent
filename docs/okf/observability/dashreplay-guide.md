---
type: Guide
title: Replay a recording into a test daemon
description: Drive the TortoiseBots observability dashboard from a replay file with no server running.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dashreplay_cmd.go
tags: [dashreplay, dashboard]
sources:
  - id: dashreplay-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dashreplay_cmd.go
  - id: dashreplay-send
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/dashreplay/send.go
  - id: dashreplay-format
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/dashreplay/format.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:40:00+01:00
status: draft
---

# Replay a recording into a test daemon

`dashboard-replay` feeds a recorded TortoiseBots run to the observability daemon's real UDP port, so its Commanders tab and fight debugger can be exercised on a laptop with no server, no bots and no game client. It logs in to nothing and reads no settings, token or key file.

## 1. Check what a recording would send

```sh
turtle-agent dashboard-replay <walk-dir> --dry-run
```

A folder holding `replay.jsonl` (or the file itself) is sent one datagram per line. `--dry-run` prints the schedule and sends nothing, e.g. `120 datagrams over 60.0 s at speed 1: COMMANDERS 60, HEARTBEAT 59, COMBAT_FIGHT 1`.

## 2. Start a test daemon

From the TortoiseBots `tools/observability` checkout, start the daemon with the replay guard on and no auth:

```sh
go run ./cmd/server -udp-port 9195 -http-port 8095 -accept-replay -dev-no-auth
```

## 3. Play the recording at speed

```sh
turtle-agent dashboard-replay <walk-dir>/replay.jsonl --to 127.0.0.1:9195 --speed 10
```

Every flag and its default:

| Flag | Default | What it does |
| --- | --- | --- |
| `--to` | `127.0.0.1:9195` | the daemon's UDP address |
| `--speed` | `1` | play this many times faster than recorded; must be above 0 |
| `--max-gap` | `10s` | longest pause between two datagrams (kept under the daemon's 12 s commanders staleness) |
| `--loop` | off | start over with a newer session after the last datagram |
| `--dry-run` | off | print the schedule; send nothing |
| `--i-know` | off | allow a non-loopback target (a test dashboard) |

Each datagram is rewritten on the way out: `session` becomes the replay's, `ts` becomes now, `seq` is renumbered from 1, and `replay: true` is added. A replay's session is newer than a running module's, so a replay overwrites what a live daemon shows until the module restarts: point it at a test daemon. Exit code 17 means the target was refused: anything whose host does not resolve only to a loopback address needs `--i-know`.

## 4. Convert API dumps when you have no replay file

```sh
turtle-agent dashboard-replay convert <api-dump-dir> --out <walk-dir>
```

A folder of `commanders-*.json` and `combat-fight-*.json` API dumps becomes `<out>/replay.jsonl` (mode 0600): one COMMANDERS snapshot plus a synthetic HEARTBEAT every 2 s, each fight once when the module would have sent it. This is approximate: the API output already passed through the daemon's caps. Without `--out` the file is written into the dump folder.

## 5. A replay file, in brief

Line 1 is the header (`format: tb-dash-replay`, `version: 1`, datagram counts by type); each following line is `{"t_ms": <ms since start>, "dg": <the UDP datagram>}`. A datagram over the 65507-byte UDP limit cannot be replayed.
