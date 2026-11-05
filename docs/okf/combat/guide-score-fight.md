---
type: Guide
title: Score a fight
description: Judge finished fights against the spell oracle, and compare two pull tests.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/fight_score_cmd.go
tags: [combat, oracle, testing]
sources:
  - id: fight-score
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/fight_score_cmd.go
  - id: pulltest-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/pulltest_cmd.go
  - id: kbspells-judge
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/kbspells/judge.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:45:00+01:00
status: draft
---

# Score a fight

Two offline commands judge what already happened. `fight-score` reads finished fight JSON and scores each bot against its spec's spell lists; `pulltest` prints one pull-test result or the change between two. Both read files only and never log in. Warnings never fail: `fight-score` exits 0, or 2 on usage.

## 1. Score one fight

```sh
turtle-agent fight-score combat-fight-1.json
```

Each fight prints the debugger's two tank checks (`mt_sunder`, `ot_taunt`) and one oracle line per bot with a warning: a missing core spell or a never spell. Fights under the minimum length score `na` instead.

## 2. Name the tanks

```sh
turtle-agent fight-score --mt <name> --ot <name1>,<name2> combat-fight-1.json
```

Without `--mt` the first tank in the fight is the main tank; without `--ot` the other tanks are the off-tanks.

## 3. Tune the threshold

```sh
turtle-agent fight-score --set spell_oracle=20 combat-fight-1.json
```

`--set` changes a threshold as `check=value`. `spell_oracle` (default 20) is the seconds of a fight a bot must be active before it is judged. `--json` prints the reading as JSON instead of a table.

## 4. Compare two pull tests

```sh
turtle-agent pulltest --compare a.json b.json
```

A result is the `<run id>.pulltest.json` file beside the trace, or a `dungeon_status` answer saved as JSON. The comparison prints which brain and rotation digest each side played, warns for fewer than 3 scored tries and when the two are not the same pull, then the party and character rows, each marked better, worse or same. Without `--compare`, `turtle-agent pulltest <result.json>` prints one result as a table; `--json` prints JSON.
