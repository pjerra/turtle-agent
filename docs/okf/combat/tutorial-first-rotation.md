---
type: Tutorial
title: Check your first rotation
description: List, show and check a rotation file, then A/B it against another with the pull test.
resource: https://github.com/pjerra/turtle-agent/blob/dev/docs/ROTATIONS.md
tags: [combat, rotation, tutorial]
sources:
  - id: rotations-doc
    resource: https://github.com/pjerra/turtle-agent/blob/dev/docs/ROTATIONS.md
  - id: rotation-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/rotation_cmd.go
  - id: pulltest-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/pulltest_cmd.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:35:00+01:00
status: draft
---

# Check your first rotation

You take a rotation file from idea to proof: see what is built in, read one, check yours, and compare two arms on the same pull. Nothing here logs in; the first three steps read files only.

## 1. See the built-ins

```sh
turtle-agent rotation list
```

Each line names a rotation, its class and spec, and the Go strategy it replaces. Pick the one closest to what you want to change, for example `frost_mage`.

## 2. Read it

```sh
turtle-agent rotation show --level 30 frost_mage
```

This prints the priority list with the rank your character would cast at level 30. Entries run top to bottom: the first entry that can run now (known, off cooldown, enough power, in range, its `when` true) wins.

## 3. Check your file

Copy the built-in's shape into `my_frost.json`, edit one priority, then check it against the spell data:

```sh
turtle-agent rotation check my_frost.json
```

`my_frost.json: ok` means the spell names, first-rank levels, aura names, ids, bands and conditions all parse. Anything else prints the file and the reason, for example a misspelled spell name.

## 4. Run both arms on one pull

The test is the leader-mode pull test: the party kills the same pack three or more times with arm A, then arm B. Change only the rotation between arms:

```sh
tools/run-uldaman.sh --mode leader --pulltest 0:0:5 --rotation frost_mage
tools/run-uldaman.sh --mode leader --pulltest 0:0:5 --rotation my_frost
```

Keep the brain preset, the role, the party, the pull and the character the same. Each run leaves a `<trace without .jsonl>.pulltest.json` file beside its trace. Put flags before the file names: `pulltest` uses plain flag parsing.

## 5. Compare

```sh
turtle-agent pulltest --compare a.json b.json
```

The table prints which brain and rotation digest each side played, then party numbers (time to kill, deaths, rest) and your character's numbers (damage and healing per second and per power, refused casts by reason, idle time in combat, mobs taken off the tank, deaths, lowest power). Each row says better, worse or same.

Keep a rotation only if it is better on the rows you care about and nothing is worse. Run one arm twice first to see the spread: a difference inside it is noise. A try with a death or wipe counts in the sums, not the means.

Next: [Write a rotation](/combat/guide-write-rotation.md) for the file format, and [Score a fight](/combat/guide-score-fight.md) for judging finished fights.
