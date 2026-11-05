---
type: Reference
title: Combat CLI
description: rotation, pulltest and fight-score with every flag, default and exit code.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/rotation_cmd.go
tags: [combat, cli, reference]
sources:
  - id: rotation-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/rotation_cmd.go
  - id: pulltest-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/pulltest_cmd.go
  - id: fight-score
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/fight_score_cmd.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:55:00+01:00
status: draft
---

# Combat CLI

Three offline commands. None logs in; `rotation` and `pulltest` read files only. Exit 0 on success, 2 on bad flags, arguments or files.

## rotation

```
usage: turtle-agent rotation list
       turtle-agent rotation show [--level N] <name|file.json>
       turtle-agent rotation check <file.json>...
```

| Subcommand | Flags | Defaults | Example |
|---|---|---|---|
| `list` | None | — | `turtle-agent rotation list` prints `name class spec replaces ...` per line |
| `show` | `--level N` (number) | `0`: no rank choice shown | `turtle-agent rotation show --level 30 frost_mage` |
| `check` | None (one or more files) | — | `turtle-agent rotation check my_frost.json` prints `my_frost.json: ok` |

`show` takes a built-in name, or a file when the argument ends in `.json` (read, not registered). `check` prints `<file>: ok` per good file on stdout and `<file>: <reason>` per bad file on stderr; any bad file exits 2.

## pulltest

```
usage: turtle-agent pulltest [--json] <result.json>
       turtle-agent pulltest --compare <a.json> <b.json>
```

| Flag | What it does | Default | Example |
|---|---|---|---|
| `--compare` | Print the change from the first result to the second | Off: print one result as a table | `turtle-agent pulltest --compare a.json b.json` |
| `--json` | Print the result (or, with `--compare`, the two summaries) as JSON | Off: human table | `turtle-agent pulltest --json a.json` |

A result is the `<trace without .jsonl>.pulltest.json` file beside the trace, or a `dungeon_status` answer saved as JSON. `pulltest` and `rotation show` use plain flag parsing: put flags before the file names (`turtle-agent pulltest --json a.json`, `turtle-agent rotation show --level 30 frost_mage`). On/off flags take `=` (`--json=true`); a space-separated value is read as the next file instead.

## fight-score

```
usage: turtle-agent fight-score [--json] [--mt NAME] [--ot A,B] [--set check=value] <combat-fight.json>...
```

| Flag | What it does | Default | Example |
|---|---|---|---|
| `--json` | Print the reading as JSON | Off: human table | `turtle-agent fight-score --json combat-fight-1.json` |
| `--mt NAME` | Main tank name | The first tank in the fight | `turtle-agent fight-score --mt <name> f.json` |
| `--ot A,B` | Off-tank names, comma separated | The other tanks | `turtle-agent fight-score --ot <a>,<b> f.json` |
| `--set check=value` | Change a threshold | `spell_oracle=20` | `turtle-agent fight-score --set spell_oracle=20 f.json` |

`--set` takes any known trace-score threshold (`rest_share`, `stale_party`, `walk_stops`, `threat_off_tank`, `order_refusals`, `feed_stalls`, `stuck`, `facing`, `idle_share`, `interrupt_cover`, `taunt_latency`, `cc_hold`, `pet_threat`, `spell_oracle`, `oom_in_fight`); only the oracle threshold changes a fight score, and an unknown id is refused (`--set: no threshold for check "x" (have ...)`). Warnings never fail: exit 0 with findings, 2 on usage.

Fight-score, like `trace-score`, `scenario`, and `pulltest`, uses plain flag parsing: put flags before the file names. On/off flags take `=` (`--json=true`); a space-separated value is read as the next file instead.
