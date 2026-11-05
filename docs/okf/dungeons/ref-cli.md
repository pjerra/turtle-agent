---
type: Reference
title: Dungeon shell commands
description: fleet, pulltest, fight-score, and tune-diff with every flag and default.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/fleet.go
tags: [dungeon, reference, cli]
sources:
  - id: fleet
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/fleet.go
    title: The fleet supervisor
  - id: pulltest-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/pulltest_cmd.go
    title: The pulltest shell command
  - id: fight-score
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/fight_score_cmd.go
    title: The fight-score shell command
  - id: tune-methods
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/tune_methods.go
    title: tune-diff
  - id: plan-check
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/plan_check_cmd.go
    title: The plan-check shell command
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T22:00:00+01:00
status: draft
---

# Dungeon shell commands

These run in a shell, not through `rpc`. Exit code is 0 on success, 2 on bad flags or arguments (and 9 from `fleet` when a child failed).

## fleet

```text
turtle-agent fleet --config FILE [--settings FILE] [--stagger D] [--wait D] [--reconnect N] [--stop-on-failure]
```

Starts one `login` child per fleet-file member and supervises them; see [Run a fleet](/dungeons/fleet.md).

| Flag | Default | What it does | Example |
|---|---|---|---|
| `--config FILE` | required | Fleet file: `{"members":[{"account","character","rpc"}]}` | `--config fleet.json` |
| `--settings FILE` | the default settings path | Settings file (login address, accounts) | `--settings <file>` |
| `--stagger D` | `2s` | Pause between starting children (logins are throttled) | `--stagger 5s` |
| `--wait D` | `2m` | Give up if not every character is in world after this long | `--wait 5m` |
| `--reconnect N` | `0` | Each child logs in again up to N times after an unplanned disconnect (0: off; a member's `reconnect` overrides) | `--reconnect 3` |
| `--stop-on-failure` | off | Stop every child when one fails | `--stop-on-failure` |

## pulltest

```text
turtle-agent pulltest [--json] <result.json>
turtle-agent pulltest --compare <a.json> <b.json>
```

Prints a pull-test result (`<run id>.pulltest.json` beside the trace, or a `dungeon_status` answer saved as JSON) as a table, or the change from a to b. See [Score one pack](/dungeons/pulltest.md).

| Flag | Default | What it does | Example |
|---|---|---|---|
| `--json` | off | Print the result (or, with `--compare`, the two summaries) as JSON | `--json r1.pulltest.json` |
| `--compare` | off | Print the change from the first result to the second (needs two files) | `--compare a.json b.json` |

## fight-score

```text
turtle-agent fight-score [--json] [--mt NAME] [--ot A,B] [--set spell_oracle=S] <combat-fight.json>...
```

Scores finished fights the way the dashboard's fight debugger reads them: the `mt_sunder` check (a warrior main tank's Sunder Armor per 10 s: ok at 1.5+, warn at 0.8+, else fail; fights under 10 s are not judged) and the `ot_taunt` check (each off-tank ok with any taunt cast, else warn), then one oracle line per bot judged against its spec's spell lists (judged after 20 s of the fight). Exit 0 always (warnings never fail). `--json` prints the reading as JSON.

| Flag | Default | What it does | Example |
|---|---|---|---|
| `--json` | off | Print the reading as JSON | `--json fight.json` |
| `--mt NAME` | the first tank in the fight | Main tank name | `--mt Tanky` |
| `--ot A,B` | the other tanks | Off-tank names, comma separated | `--ot OtA,OtB` |
| `--set spell_oracle=S` | 20 | Change a threshold: `check=value` | `--set spell_oracle=30` |

## tune-diff

```text
turtle-agent tune-diff <trace.jsonl>
```

No flags. Prints what the live tuning of a run changed as JSON patches for the tunables file and the plan file; see [Tune a live run](/dungeons/live-tuning.md).

## plan-check

```text
turtle-agent plan-check [--dir dungeons] --mmaps DIR [--from-leg N] [--from-pull-group N] [--sql FILE] [--plan NAME] [--spawns DIR] [--floor-aware] [--approach-hops] [--strict] [--json] <plan>...
```

Walks the named plans on the navmesh and prints each failing point. A commander `dungeon_run` runs this check by itself before starting.

| Flag | Default | What it does | Example |
|---|---|---|---|
| `--dir` | `dungeons` | Folder with the plan files | `--dir dungeons` |
| `--mmaps` | required | Folder with the server's mmaps | `--mmaps <dir>` |
| `--from-leg` | `0` | Check from this leg on | `--from-leg 3` |
| `--from-pull-group` | the leg's own walk too | With `--from-leg`: 0-based pull group to check from | `--from-pull-group 1` |
| `--sql` | off | Lint the rows of a world migration instead of a plan file | `--sql mig.sql` |
| `--plan` | off | With `--sql`: the plan in the file (needed when it holds several) | `--plan uldaman` |
| `--spawns` | `<dir>/../tools/dungeongen/testdata/mapN` | The map's world export for the aggro and patrol lints | `--spawns <dir>` |
| `--floor-aware` | off | Build rows with the floor-aware cutter | `--floor-aware` |
| `--approach-hops` | off | Build rows with the approach hops | `--approach-hops` |
| `--strict` | off | Lints fail the check (by default they are warnings) | `--strict` |
| `--json` | off | Print the results as JSON | `--json` |
