---
type: Reference
title: walk-preflight reference
description: Check a walk directory before any raid-walk starts: binaries, slots, leases, instance caps, plans, server keys and rosters.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/walk_preflight_cmd.go
tags: [raids, walk-preflight, reference]
sources:
  - id: preflight-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/walk_preflight_cmd.go
  - id: preflight
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/preflight/preflight.go
  - id: checks
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/preflight/checks.go
  - id: instancecap
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/slots/instancecap.go
  - id: roster
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/raidwalk/roster.go
generated:
  by: omp/deepseek-v4.1-flash
  at: 2026-11-11T22:30:00+01:00
status: draft
---

# `walk-preflight` reference

`turtle-agent walk-preflight <walk-dir>` checks a walk directory before any raid-walk starts. It reads every `cmd-*.txt`
file and the autopilot's `targets.txt` (one command per file, one command or target per line; blank and `#` lines are
skipped), and prints one `OK`/`WARN`/`FAIL` line per subject with a fix hint. It writes nothing: leases, the instance
ledger and the slot table are only read, and the server checks are read-only ssh calls.

```
turtle-agent walk-preflight [--server NAME]... [--binary PATH] [--json] <walk-dir>
```

## Flags

| Flag | Default | What it does | Example |
|---|---|---|---|
| `--server NAME` | every server | Keep only the commands of these servers (repeatable, or a comma list). A named server with no command is a failure, so a typo does not silently pass. | `--server m910q` |
| `--binary PATH` | this executable | The turtle-agent to check for a command that names none: every `targets.txt` line, and a `cmd-*.txt` line whose binary is the bare name `turtle-agent`. A command that names its own path keeps it. Without the flag the preflight checks its own executable, exactly the binary `raid-autopilot` launches without its own `--binary`. | `--binary ~/ta-1111a-bin/turtle-agent` |
| `--json` | off | Print the report as one JSON object (`dir`, `results[]` with `check`, `status`, `subject`, `detail`, `fix`). | `--json` |
| `<walk-dir>` | required | The walk directory: the commands are resolved and read from here, and relative paths are resolved against it. | `~/walks/zg38` |

## Exit codes

`0` every check OK, `1` warnings only, `2` any failure (or a bad flag or unreadable walk directory).

## The checks[^checks]

| # | Check | Passes when |
| --- | --- | --- |
| 1 | binary | Every binary the commands name (`--binary` for a target that names none) exists, runs `--help`, and prints a version; the subject line is the resolved path, so the report says which binary was checked, and it adds the binary's Go build commit when it has one. One walk may not mix binaries (WARN). |
| 2 | slots schema | The binary parses `slots.json` (`lease list` on a private copy, so no lock or lease in the real folder); an unknown field is a FAIL. |
| 3 | slots path | No command passes a `--slots` other than the default path: a split lease table also splits the instance-cap ledger (FAIL). |
| 4 | instance cap | `instance-cap-per-account.json` beside the slot table (`{"<server>": 30}`) parses and the resolved cap is printed per binary and server. A binary that would use the stock 5 while the file says more is a FAIL; an explicit `--instance-cap-per-account` on a binary without that flag is a FAIL. The binary's support is read from the flag in its `raid-walk --help`. A missing file is a WARN (and 5 everywhere). |
| 5 | lease | Every server/slot of the commands is free, or held by a live agent of this walk (same `--holder`, and the lease dir under the command's `--out`). Two commands on one slot are a FAIL; slot 3 is always a FAIL; a held slot is a FAIL; a stale lease is a WARN (acquire takes it over). |
| 6 | gm-clear | `--place` needs `--sql`, the `--sql` file exists, and `--gm-clear`/`--gm-clear-boss` resolve a spawns dir that has `creatures.tsv`: `--gm-clear-spawns DIR`, else `<--dir>/../tools/dungeongen/testdata/map<map>` (the plan's map). |
| 7 | plan | The plan JSON `<--dir>/<plan>.json` loads and, with `--sql`, the deployed migration exports the same plan: name, map, leg names in order, and the plan row's `source` (commit and plan path). A plan edited after the migration's filename stamp is a WARN, and so is an export from another commit. |
| 8 | server keys | The required keys file lists `key: value` pairs; every server's effective `aiplayerbot.conf` and `tortoise_bots.conf` lines (last active line wins) match. A missing keys file is a WARN, a missing or different value is a FAIL. Values of keys whose names contain `PASS`, `KEY` or `SECRET` are never printed (a mismatch is shown as `(hidden)`). |
| 9 | roster | Every `--hires` file exists, parses and validates against the plan. A roster with fewer than 3 tanks is a WARN when the command will fight Arlokk (14515) or Hakkar (14834) in `zul_gurub` — read from `--from`/`--until` (raid-walk's default `--until boss` fights no boss), or from a `boss:<entry>`/`leg:<name>` goal. |

Commands using the multi-raid form (`--slot 2,4`, per-slot `4=...` values) are split into one subject per slot, exactly
as raid-walk splits them; the per-slot holder and out dir use the same defaults.

Relative paths (`--dir` defaults to `dungeons`, `--sql`, `--hires`, `--out`, `--slots`) are resolved against the walk
directory, which is also where the commands are meant to run. `--sql` named by a `--from`-less full clear is still
checked for existence; `--place` alone is enough to demand it.

## `targets.txt`

The same file `raid-autopilot` reads, and it is read by the autopilot's own parser, so both accept exactly the same
lines: one goal per line, `id server slot holder goal -- <plan> [raid-walk flags...]`, where the goal is `full`,
`leg:<leg name>`, `boss:<entry>` or `practice:<entry>:<cycles>`. A two-word flag (`--from leg hakkar`) and a value that
holds spaces (`--gm-instance-data hakkar:1 3;2 3`) read as they do in the launch path, with or without quotes. A line
that starts with a turtle-agent binary and `raid-walk` is taken as a full command instead. The preflight adds
`--server`, `--slot` and `--holder` from the header, and reads a line as placed, as the autopilot launches it.

## `preflight-keys.json`

```json
{"keys": {"AiPlayerbot.TeleportLandingFall": "1", "AiPlayerbot.HireMaxBotsPerPlayer": "39"}}
```

The file lives in `~/.config/turtle-agent/preflight-keys.json`. The ssh host is the slot table's server name: that is
the name in the owner's ssh config, while realmd may carry a NAT address only the host reaches. The call is
`ssh -o BatchMode=yes -o ConnectTimeout=10 <server>` and reads `~/tortoise-wow-server/etc/aiplayerbot.conf` and
`~/tortoise-wow-server/etc/modules/tortoise_bots.conf`.

## Output

```
OK   1 binary         /home/perzi/ta-zg17-bin/turtle-agent: turtle-agent 0.1.0 (commit 35c7dc2f92bd)
FAIL 4 instance cap   m910q (/home/perzi/ta-zg17-bin/turtle-agent): this binary would use 5, but instance-cap-per-account.json says 30 for m910q -- fix: use a binary that reads instance-cap-per-account.json (or pass --instance-cap-per-account)
FAIL 5 lease          cmd-a.txt m910q/2: held by zg38-jx6 since 00:34:02Z, expires 06:04:02Z (agent pid 3607035 running) -- fix: wait for the holder or run teardown on its dir
WARN 7 plan           zul_gurub: the plan was edited 2026-11-05 00:11, after the export 2026-11-01 03:00 -- fix: export the plan again (turtle-agent plan-export) and redeploy it
```

[^checks]: The check list and the flags are read from `internal/preflight/checks.go` and `cmd/turtle-agent/walk_preflight_cmd.go`.
