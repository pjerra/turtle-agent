---
type: Guide
title: Score one pack
description: Run a pull test on one pull group, read its table, and compare two results.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/dungeon/pulltest.go
tags: [dungeon, pulltest, guide]
sources:
  - id: pulltest
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/dungeon/pulltest.go
    title: The commander-mode pull-test scenario
  - id: pulltest-report
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/dungeon/pulltest_report.go
    title: Pull-test report, table, and comparison
  - id: pulltest-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/pulltest_cmd.go
    title: The pulltest shell command
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:40:00+01:00
status: draft
---

# Score one pack

A pull test pulls one pull group several times with the same party and scores each try. It answers "does this pack go down cleanly" in minutes instead of a full clear, and two results side by side compare two builds. The party must stand at the pack in a partly-cleared instance.

## 1. Run the test

```sh
turtle-agent rpc <dir> dungeon_run '{"name":"<plan>","mode":"commander","lead_bot":"<Lead>","pulltest":{"leg":"velratha","pull_group":2,"tries":3,"reset":"auto"}}'
```

`pulltest` names its own start (a leg number or name, and the pull group in it), so leave `from_leg`, `from_pull_group`, `start_here`, and `resume` out. `tries` defaults to 3, at most 10. `reset` is how the pack comes back between tries: `auto` (the default: equivalent packs first, then respawn), `equivalent` (a pack with the same `equiv` label), `respawn` (your character's own `.respawn`, standing within 80 yards), or `none` (one try per spawn).

Each try gathers the party, pulls, fights, and rests, then records kills, time to kill (pull order to last kill), deaths, mobs fought off the tank, adds, the healer's lowest mana, and rest seconds. The result lands in `<run id>.pulltest.json` beside the trace.

## 2. Read the table

```sh
turtle-agent pulltest <run id>.pulltest.json
turtle-agent pulltest --json <run id>.pulltest.json
```

A saved `dungeon_status` answer reads the same way. A try with outcome `no_pack` (the reset did not bring the pack back) is not scored; a wipe ends the scenario.

## 3. Compare two runs

```sh
turtle-agent pulltest --compare a.json b.json
```

The table shows b minus a. The comparison warns when either side has under 3 scored tries, or when the two scenarios differ (plan, leg, party) and the numbers may not compare.

## 4. Test your own character

In leader mode the pull test scores your character too: damage and healing done, dps/hps, power spent, actions, failed casts by reason, idle time, and time spent with a mob on you. It runs where the party stands (leave `teleport` and `resume` out) and needs a brain:

```sh
turtle-agent rpc <dir> dungeon_run '{"name":"<plan>","mode":"leader","pulltest":{"leg":1,"pull_group":0,"tries":3}}'
```
