---
type: Guide
title: Score a finished fight
description: Read a fight file the way the dashboard fight debugger does, with fight-score.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/fight_score_cmd.go
tags: [fight-score, guide]
sources:
  - id: fight-score-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/fight_score_cmd.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:50:00+01:00
status: draft
---

# Score a finished fight

`turtle-agent fight-score` reads finished fights the way the dashboard's fight debugger reads them: the two tank readings with the debugger's exact rules, then one line per bot the spells oracle warns about. It reads files only and never logs in.

## 1. Score a fight dump

```sh
turtle-agent fight-score cmd/turtle-agent/testdata/fights/combat-fight-5-3.json
```

The input is `{fight, warnings}` as `GET /api/v1/combat/fight/<key>` returns it, or a bare fight. Output starts `fight <id> <commander> <s> s, <n> bots`, then one row per reading.

## 2. Name the tanks

```sh
turtle-agent fight-score --mt <tank> --ot <ot1>,<ot2> --json <walk-dir>/fight.json
```

Every flag and its default:

| Flag | Default | What it does |
| --- | --- | --- |
| `--json` | off | print the reading as JSON |
| `--mt` | none (the first tank in the fight) | main tank name |
| `--ot` | none (the other tanks) | off-tank names, comma separated |
| `--set` | none | change a threshold: `check=value` (`spell_oracle`) |

## 3. Read the verdicts

- `mt_sunder`: a warrior main tank's Sunder Armor casts per 10 s — ok at 1.5 or more, warn at 0.8, else fail. A fight under 10 s is not judged, and neither is a non-warrior tank.
- `ot_taunt`: one line per off-tank — warn when it never taunted (Taunt, Growl, Hand of Reckoning, Mocking Blow, Challenging Shout, Challenging Roar).
- `oracle`: one line per bot the spec oracle warns about, then the count of bots judged ok. A bot is judged after 20 s of the fight; a fight's spell list is capped at 8 lines per bot, so a core spell outside it reads `unknown`, not missing.

## 4. Mind the exit code

Exit 0 whatever the warnings say; 2 on usage. Warnings are readings, not failures.
