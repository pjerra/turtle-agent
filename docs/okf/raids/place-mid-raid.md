---
type: Guide
title: Start partway into an instance
description: Place the raid at a leg or step with --place, re-place without burning the cap, and clear packs with --gm-clear.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/raid_walk_cmd.go
tags: [raids, placement, gm-clear]
sources:
  - id: walk-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/raid_walk_cmd.go
  - id: place
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/raidwalk/place.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:35:00+01:00
status: draft
---

# Start partway into an instance

`--place` (with `--from`) starts the raid partway into its instance instead of at the door, so several raids on one server can each work a different boss leg. It only runs on the test servers, needs `--sql`, and is refused with `--inject`.[^walk-cmd]

## 1. Place at a leg

```sh
turtle-agent raid-walk <plan> --server <server> --slot <slot> \
    --dir <plans-dir> --sql <world-sql> --from "leg <leg>" --place
```

`--from "leg L"` is staged: the walk starts at the highest quiet move row before the leg's first pull (see [How placement works](/raids/placement.md)). The first leg needs no placement: `--place --from "leg <first leg>"` starts at step 1 from the door exactly as a non-placed run does.

## 2. Place at an exact step

```sh
turtle-agent raid-walk <plan> --server <server> --slot <slot> \
    --dir <plans-dir> --sql <world-sql> --from "step <n>" --place
```

`--from "step N"` is taken as given: a positioned move row is placed at its own point, and a pull row at that pull's own stand point. A boss or hold row (or a row inside an event) is refused — the raid would re-fight it on the way in.

## 3. Re-place in the same instance

```sh
turtle-agent raid-walk <plan> --server <server> --slot <slot> \
    --dir <plans-dir> --sql <world-sql> --from "step <n>" --place --reuse-instance
```

`--reuse-instance` keeps the raid's current instance: nothing is entered, reserved or counted, so the instance cap is untouched. A live raid is kept; a logged-out raid resumes from the commander's live bind. With no live bind the walk says `no live bind for map <n> is left: falling back to a fresh instance` and enters normally. Never combine a re-place with a fresh-instance retry for a boss whose summon is spent: re-entering the same copy re-meets the dead trigger.

## 4. Clear packs with GM commands (test servers only)

```sh
turtle-agent raid-walk <plan> --server <server> --slot <slot> \
    --dir <plans-dir> --sql <world-sql> --from "leg <leg>" --place \
    --gm-clear "<leg>:<group>[,<group>...]" --gm-clear-boss <leg>
```

After the placement and before the raid moves, the commander GM-kills every live unit of the named pull groups (or the leg's boss), so a later leg can be tested while a trash wipe is still being fixed. Groups are 1-based, in route order among the leg's pull and hold rows; `--dry-run` lists them as `8c gm-clear:` lines. A group that clears no hostile pack (a boss's or event's units) is skipped with a `gm-clear: WARN: skipped ...` note and the other groups go on. Every walk that used it is marked **ASSISTED** in `summary.json` and the result line: never a real clear.

## 5. Send instance data for a gate

```sh
turtle-agent raid-walk <plan> --server <server> --slot <slot> \
    --dir <plans-dir> --sql <world-sql> --from "leg <leg>" --place \
    --gm-instance-data "<leg>:<a> <b>[;<a> <b>...]"
```

The lines go out as `.debug instancedata <a> <b>`, in order, 3 s apart, once per instance, ahead of `run start` — so the gate is satisfied before the first pull. A walk that reaches the leg mid-run pauses the run first and resumes after the last line. A refused command is retried on the next status, at most 3 times per walk, and never fails the walk.
