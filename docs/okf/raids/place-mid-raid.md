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

`--from "leg L"` is staged: the walk starts at the highest quiet move row before the leg's first pull (see [How placement works](placement.md)). The first leg needs no placement: `--place --from "leg <first leg>"` starts at step 1 from the door exactly as a non-placed run does.

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

`--reuse-instance` keeps the raid's current instance: nothing is entered, reserved or counted, so the instance cap is untouched. A live raid is kept; a logged-out raid resumes from the commander's live bind. With no live bind the walk says `no live bind for map <n> is left: falling back to a fresh instance` and enters normally. A same-instance placement that finds half the hires dead or the start leg's boss fighting the party does not retry: it exits 29 with `fresh instance needed`, and the autopilot relaunches a fresh instance. Never combine a re-place with a fresh-instance retry for a boss whose summon is spent: re-entering the same copy re-meets the dead trigger.

## 4. Clear packs with GM commands (test servers only)

```sh
turtle-agent raid-walk <plan> --server <server> --slot <slot> \
    --dir <plans-dir> --sql <world-sql> --from "leg <leg>" --place \
    --gm-clear "<leg>:<group>[,<group>...]" --gm-clear-boss <leg>
```

After the placement and before the raid moves, the commander GM-kills every live unit of the named pull groups (or the leg's boss), so a later leg can be tested while a trash wipe is still being fixed. Groups are 1-based, in route order among the leg's pull and hold rows; `--dry-run` lists them as `8c gm-clear:` lines. A group that clears no hostile pack (a boss's or event's units) is skipped with a `gm-clear: WARN: skipped ...` note and the other groups go on. Every walk that used it is marked **ASSISTED** in `summary.json` and the result line: never a real clear.
Each named group expands to its whole pack: the row's entries and reach, the export's `AGGRO_TOGETHER` links, and both aggro-link directions. A bad spec, an unknown leg, or a missing world export is a usage error before any GM line goes out. Spawn positions come from `--gm-clear-spawns` first, else the installed export (`~/.local/share/turtle-agent/world/map<N>`) when it holds `creatures.tsv`, else the checkout's `tools/dungeongen/testdata/map<N>`.[^place]

The commander visits stops (each covering members within 100 yd, read from 150 yd out) and kills by guid plus entry with select-confirm; after every `.go xyz` it waits for the landing: the status is read every 250 ms and must show a teleport the session has acked (its `TeleportSeq` past the read before the go) at the stop's position, because the session reports the go's destination at once and the previous stop can already be within the slack; a landing the status never shows within 3 s falls back to the old fixed 2 s. A landed stop settles 1 s, then the units read polls the pack (up to the old 2 s) until a member shows. Then it re-reads its instance before any kill (the same map does not prove the same copy: a stop in another copy stops the clear with no kill). The second round runs only while a member is alive or unseen (a pack whose members all died in the first round is not walked again), and a pack this walk's own corridor clear despawned whole is not visited at all: its members are settled before round 1, exactly as the later rounds skip them. A `.die` goes through the `SEC_DEVELOPER` path. A member gets retries (5 s reads, then up to three 3 s retries); the whole tool has a 5-minute budget, and members it never reaches are recorded `not tried: the gm-clear budget ran out`. Reasons name what happened: `not seen`, `alive`, `already dead`, `despawned by the corridor clear` (the walk's own clear already removed it: the frames still show it alive and every `.die` is a no-op), or the budget reason. A `.die` on a slot's own character is refused.[^place]

A clear with a member left is `PARTIAL`: `summary.json` carries `gm_clear_partial`, and the result line says `ASSISTED ... PARTIAL` with the uncleared `leg:range` groups. Failed groups never fail the placement: they warn with `skip=` so the quiet and the landing skip them too. The commander returns to the start point before the quiet check. Every kill and failure is recorded: `run.log` (`gm-clear: killed ...`, `FAILED`, per-group lines), `summary.json` (`gm_clear[]`, `assisted`), and the walk report's ASSISTED table. In the pair form the flags apply to both walks when unprefixed, or to one walk with a slot prefix.[^place]

## 5. Send instance data for a gate

```sh
turtle-agent raid-walk <plan> --server <server> --slot <slot> \
    --dir <plans-dir> --sql <world-sql> --from "leg <leg>" --place \
    --gm-instance-data "<leg>:<a> <b>[;<a> <b>...]"
```

The lines go out as `.debug instancedata <a> <b>`, in order, 0.5 s apart (each line's 2 s reply window is the guard before the next), on every launch and again after each recovered wipe (a same-instance `--reuse-instance` restart included, because a boss `Reset()` can overwrite the priest data on a wipe), ahead of `run start` — so the gate is satisfied before the first pull. A walk that reaches the leg mid-run pauses the run first and resumes after the last line. A refused command is retried on the next status, at most 3 times per walk, and never fails the walk.

A malformed value (not two integers) or an unknown leg is a usage error before any GM line goes out; two specs for one leg are one firing in flag order. Each line is confirmed by the server's chat: 2 s of silence counts as `sent` (the core's debug handler prints nothing on success), a refusal reads `REFUSED` and is retried on the next status, at most 3 times per walk, then logged `NOT confirmed after 3 tries` while the walk continues.[^place]

One marker file per instance and leg in the out dir remembers the sent commands: one walk never sends a leg twice, but every new launch sends again (a `--reuse-instance` restart included, each in its own out dir), and a changed command list resends. After `run start` a send waits for an out-of-combat rest or gather that passes the pre-pull gate (never while pulling or fighting), and the lines are resent once per boss row at the rest of the step before it: a GM kill does not stop the respawn, and a respawned priest's `Reset()` overwrites its data again. Without `--place` the instance id is read the way the cap ledger reads it at entry; when that read fails the walk warns once and fires marker-only under `unplaced`, and a read of 0 is never cached: the next firing retries it.[^place]
