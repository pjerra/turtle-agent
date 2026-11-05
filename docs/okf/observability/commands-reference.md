---
type: Reference
title: Command and flag reference
description: Every observability command, subcommand and flag, with defaults and examples.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dashboard_cmd.go
tags: [reference, dashboard, traces, captures, scenarios]
sources:
  - id: dashboard-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dashboard_cmd.go
  - id: dashreplay-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/dashreplay_cmd.go
  - id: capturecmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/capturecmd.go
  - id: trace-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/trace_cmd.go
  - id: fight-score-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/fight_score_cmd.go
  - id: stage-check-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/stage_check_cmd.go
  - id: trash-check-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/trash_check_cmd.go
  - id: scenario-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/scenario_cmd.go
  - id: decisions-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/decisions_cmd.go
  - id: map-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/map_cmd.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:58:00+01:00
status: draft
---

# Command and flag reference

Every command in this area, every flag, its default, and an example. Exit 2 everywhere means bad flags or arguments.

## `turtle-agent dashboard`

Serves the live page. Read-only unless `--edit` or `--control` is given. Exits 2 on usage errors.

| Flag | Default | What it does |
| --- | --- | --- |
| `--listen` | `127.0.0.1:8790` | address the page is served on; a non-loopback address prints a warning |
| `--allow-host` | none | host names or IPs accepted in the `Host` header besides loopback, comma separated |
| `--agents` | `auto` | `auto`, a comma list of RPC ports/addresses (loopback only), or `auto,8770` |
| `--tests-dir` | `~/TortoiseBots-audit/tests` | the only folder `/files/...` serves |
| `--traces-dir` | `~/turtle-agent/traces` | where agents write run traces; read, never served |
| `--plans-dir` | `~/turtle-agent/dungeons` | plan files for the map, comma separated |
| `--minimap-dir` | `~/turtle-agent-minimaps` | map images from `minimap-extract` |
| `--embed-from` | none | extra `http://host:port` origins that may frame the page |
| `--config-dir` | `~/.config/turtle-agent` | settings files and `rpc-<port>.token` files |
| `--mmaps-dir` | none | navmesh folder of the server the plans run on; enables the real-route view, required with `--edit` |
| `--edit` | off | turn on the route editor (`POST /api/edit/<plan>`) |
| `--edit-dir` | `~/turtle-agent-route-edits` | where versions go (`<plan>/vNNNN.json`, `.meta.json`, `.diff`) |
| `--edit-source` | the first `--plans-dir` | whose plan files an edit starts from |
| `--edit-tokens` | `~/.config/turtle-agent/route-edit-tokens` | `label token` lines, mode 0600 |
| `--edit-allow-default-dir` | none | settings files with an `rpc` but no `dungeons_dir` vouched as not the edits folder |
| `--edit-trust-local` | off | saves from this machine need no token typed (needs `--edit`) |
| `--edit-trust-label` | `owner` | token label trusted saves are made as |
| `--control` | off | turn on run controls (`POST /api/control/<id>/run`) |
| `--control-tokens` | `~/.config/turtle-agent/dashboard-control-tokens` | `label token` lines, mode 0600; never the editor's file |
| `--control-trust-local` | off | controls from this machine need no token typed (needs `--control`) |
| `--control-trust-label` | `owner` | token label trusted controls are made as |
| `--control-log` | `~/.local/state/turtle-agent/dashboard-control.jsonl` | every control appends a request and a result line |

```sh
turtle-agent dashboard --agents auto --plans-dir <walk-dir>/dungeons --mmaps-dir <walk-dir>/mmaps
```

## `turtle-agent dashboard-replay`

Feeds a recording to a TortoiseBots observability daemon's UDP port. Logs in to nothing. Exit 17 when the target is refused.

Play: `turtle-agent dashboard-replay <recording-dir|file.jsonl> [--to host:port] [--speed N] [--max-gap D] [--loop] [--dry-run] [--i-know]`

| Flag | Default | What it does |
| --- | --- | --- |
| `--to` | `127.0.0.1:9195` | the daemon's UDP address |
| `--speed` | `1` | play this many times faster than recorded; must be above 0 |
| `--max-gap` | `10s` | longest pause between two datagrams |
| `--loop` | off | start over with a newer session after the last datagram |
| `--dry-run` | off | print the schedule; send nothing |
| `--i-know` | off | allow a non-loopback target (a test dashboard) |

Convert: `turtle-agent dashboard-replay convert <api-dump-dir> [--out <dir>]`; `--out` defaults to the dump folder itself and writes `<dir>/replay.jsonl` (mode 0600).

```sh
turtle-agent dashboard-replay <walk-dir>/replay.jsonl --to 127.0.0.1:9195 --speed 10
```

## `turtle-agent decisions pull`

Fetches decision captures from the TortoiseBots dashboard into `<out>/decisions`. Exit 18 when the pull fails or the budget stops it; 2 on usage.

| Flag | Default | What it does |
| --- | --- | --- |
| `--dash-url` | none, required | the dashboard URL |
| `--dash-user` | `TADASH` | the dashboard login |
| `--dash-pass-file` | none, required | the dashboard password file |
| `--commander` | none, required | the commander to pull |
| `--since`, `--until` | none | RFC 3339 start/end of the window |
| `--out` | none, required | the output dir |
| `--max-mib` | `1024` | the pull budget in MiB; must be above 0 |

```sh
turtle-agent decisions pull --dash-url <server> --dash-pass-file <walk-dir>/pass --commander <slot> --out <walk-dir>
```

## Captures: `login --capture`, `scrub-check`, `capture-stats`, `capture events`

- `turtle-agent login [--config FILE] [--capture FILE] ... <character>`: `--capture` records decrypted world packets to a new file (never overwrites; cannot combine with `--reconnect`).
- `turtle-agent scrub-check [--config FILE] <capture>...`: fails when a capture holds an account name or password from the settings file. `--config` defaults to the settings file. Exit 7 when a secret is found.
- `turtle-agent capture-stats <capture>`: one line per opcode plus `TOTAL`. No flags.

`capture events [--from RFC3339] [--to RFC3339] [--kinds a,b] [--out FILE [--force]] <capture>`: decodes a capture into the `events` rows, one JSON event per line.

| Flag | Default | What it does |
| --- | --- | --- |
| `--from`, `--to` | none | only events in this RFC 3339 window |
| `--kinds` | none | only these event kinds, comma separated |
| `--out` | none | write to this file (mode 0600) instead of stdout; needs `--force` when it exists |
| `--force` | off | let `--out` replace an existing file (never a capture) |

```sh
turtle-agent capture events --kinds chat,spell_go --out <walk-dir>/events.jsonl <walk-dir>/run.twcap
```

## `turtle-agent trace-score`

Scores trace files with the current checks. Exit 11 when any check failed, 0 otherwise, 2 on usage.

| Flag | Default | What it does |
| --- | --- | --- |
| `--json` | off | print the verdicts as JSON, one object per trace |
| `--summary` | off | write `<run_id>.summary.json` beside each trace |
| `--set` | none, repeatable | change a threshold: `check=value` |

```sh
turtle-agent trace-score --summary --set rest_share=30 <walk-dir>/traces/live-1.jsonl
```

## `turtle-agent fight-score`

Scores finished fights like the dashboard fight debugger. Reads files only. Exit 0 whatever the warnings say; 2 on usage.

| Flag | Default | What it does |
| --- | --- | --- |
| `--json` | off | print the reading as JSON |
| `--mt` | the first tank in the fight | main tank name |
| `--ot` | the other tanks | off-tank names, comma separated |
| `--set` | none, repeatable | change a threshold: `check=value` (`spell_oracle`) |

```sh
turtle-agent fight-score --mt <tank> --ot <ot1>,<ot2> <walk-dir>/fight.json
```

## `turtle-agent stage-check`

Checks where staging would put the party for one leg (index or name). Exit 0 when it stages, 13 when it refuses, 2 on usage. `--mmaps` is required.

| Flag | Default | What it does |
| --- | --- | --- |
| `--dir` | `dungeons` | the folder with the plan files |
| `--mmaps` | none, required | folder with the server's mmaps |
| `--cleared-through` | none | legs 0..this one count as cleared |
| `--server-steps` | `0` | the step count `.bot run list` shows for the plan |
| `--json` | off | print the result as JSON |

```sh
turtle-agent stage-check --mmaps <walk-dir>/mmaps uldaman keystone
```

## `turtle-agent trash-check`

Reports what one walk of a plan wakes. Reads files only. Exit 0 normally, 21 with a `--fail-on` match, 1 when the navmesh is unavailable, 2 on usage.

| Flag | Default | What it does |
| --- | --- | --- |
| `--dir` | `dungeons` | the folder with the plan files |
| `--sql` | none | a deployed migration (the recommended input) |
| `--plan` | none | with `--sql`: the plan to report when the file holds several |
| `--spawns` | `<dir>/../tools/dungeongen/testdata/map<N>` | the world export of the plan's map |
| `--patrols` | none, repeatable | recorded patrol files |
| `--mmaps` | none | folder with the server's mmaps; without it the route is straight lines |
| `--level` | `0` (the plan's `level_band[0]`) | the party's lowest level |
| `--spread` | `0` (10 for a raid plan, else 3) | how far the party spreads from the route, in yards |
| `--aggro-rate` | `1` | scales every aggro radius |
| `--assist-yd` | `10` | the family assist hop's radius, in yards |
| `--json` | off | print the report as JSON |
| `--out` | none | write `trash-<plan>.json` and `trash-<plan>.txt` there |
| `--dash-out` | none | write the patrols as the dashboard's patrol data file |
| `--fail-on` | none | exit 21 when a finding of class `danger`, `patrol` or `any` exists |

```sh
turtle-agent trash-check --sql <walk-dir>/mig.sql --plan uldaman --mmaps <walk-dir>/mmaps --fail-on danger
```

## `turtle-agent scenario check|run|norm`

- `scenario check [--var NAME=VALUE]... <file>`: load the file and print each step. Exit 2 on any load error.
- `scenario run [--out DIR] [--force] [--quiet] [--var NAME=VALUE]... [--session NAME=ADDR[@TOKENFILE]]... <file>`: run it. Exit 0 when every check passed, 14 when a check failed, 15 on abort or error, 2 on usage.
- `scenario norm <file> <dir>`: re-apply the file's `normalise` rules to the stored transcript and rewrite `norm.txt`.

| Flag (`run`) | Default | What it does |
| --- | --- | --- |
| `--out` | `scenario-out/<scenario>-<UTC yyyymmddThhmmss>` | output directory; a non-empty one needs `--force` |
| `--force` | off | write into a non-empty `--out` |
| `--quiet` | off | print only PASS/FAIL/ERROR/SKIP lines and the summary |
| `--var` | none, repeatable | override a file var (the name must exist) |
| `--session` | none, repeatable | override a session: `NAME=ADDR[@TOKENFILE]` (the name must exist; loopback is re-checked) |

```sh
turtle-agent scenario run --out <walk-dir>/lft-1 scenarios/lft-addon-path.json
```

## `turtle-agent map`

Draws a run as a top-down SVG map. Needs `--capture` and/or `--botwatch`, else exit 2.

| Flag | Default | What it does |
| --- | --- | --- |
| `--capture` | none | a `login --capture` file |
| `--events` | none | saved events used instead of the capture's own |
| `--report` | none | a `dungeon_status` report |
| `--botwatch` | none | harness samples as extra routes |
| `--members` | none | with `--botwatch`, only these bots, comma separated |
| `--plan` | none | a dungeon plan drawn over the run |
| `--out` | `map.svg` | the SVG to write; several floors write `NAME-f1.svg`, ... |
| `--html` | off | also write the single-file HTML replay |
| `--force` | off | let `--out` replace an existing file (never a capture) |
| `--config` | the settings file | its `mmaps_dir` gives the navmesh background |
| `--mmaps` | none | folder with the server's mmaps (overrides `mmaps_dir`) |
| `--map` | `-1` | the map to draw (default: the one with most positions) |
| `--box` | none | draw only `x0,y0,x1,y1` in game coordinates |
| `--from`, `--to` | none | only what happened in this RFC 3339 window, both inclusive |
| `--pad` | `25` | yards of margin round what the run covers |
| `--floors` | `auto` | split stacked floors from the navmesh, `off`, or cut heights |
| `--background` | on | draw the navmesh floor (off: white, smaller and quicker) |
| `--width` | `960` | the longest side of the map in pixels |

```sh
turtle-agent map --capture <walk-dir>/run.twcap --plan dungeons/uldaman.json --out <walk-dir>/run.svg --html
```

## The `events` RPC

Not a CLI command, but scenarios and the dashboard read it. Call `events` with `since` (last `seq` seen; 0 starts at the oldest kept), `kinds` (only these kinds), `party_only`, `limit` (default 500, at most 5000) and `wait_ms`. The answer holds the events, `next`, `oldest`, `dropped` and `more`. `chat_log` is the same narrowed to `chat`, `whisper` and `system`.
