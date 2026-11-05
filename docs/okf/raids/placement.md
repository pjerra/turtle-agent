---
type: Concept
title: How placement works
description: What --place does inside the instance: the staged start, the corridor clear, the quiet check and the four tries.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/raidwalk/place.go
tags: [raids, placement]
sources:
  - id: place
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/raidwalk/place.go
  - id: walk-place
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/raid_walk_place.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:45:00+01:00
status: draft
---

# How placement works

`--place` puts the raid at a staged start inside its instance instead of walking it in from the door. The walk clears the corridor of the legs it skips, judges the start point quiet itself, teleports the raid onto it, and verifies the landing — all in the same instance, up to four tries.[^place]

## The staged start

From `--from "leg L"` the walk starts at the highest quiet move row before the leg's first pull that has no later creature row within 50 yd of it. When the leg holds no such row, the walk places at the last quiet move row searching backward through the earlier legs, and the notes name it; only when no quiet row stands anywhere before the first pull is the placement refused (`leg X has no quiet move row before its first pull: pass --from "step N"`, exit 2). The fallback is refused too when a boss or hold row (or the move into an event) lies between it and the leg's first row: the raid would re-fight it on the way in. A `--from "step N"` is taken as given instead: a positioned move row at its own point, a pull row at that pull's own stand point.

## The corridor clear

Before the fresh instance is entered, the walk despawns every pull row before the start whose circle comes within 45 yd of the route to the `until` stop, with `.tt clearto ... all=1` after a pre-read of the units there — never `kill`, never a boss. A boss or hold row (or a row inside an event) of an earlier leg within 30 yd of the route refuses the placement; when the route passes such a row in a later leg and the operator asked for an `until` clause, the walk shortens that clause to stop before the segment and prints the asked-for until and the effective one. `--until none` is never replaced: the run walks the whole plan and the run leader fights whatever it meets.

**Known limit:** the pre-read and the `clearto` are two commands, so a unit that walks into a circle between them can be despawned along with the row's own. Hence test servers only.[^walk-place]

## The quiet check and the landing

The walk judges the start point quiet itself, not the toolkit's flag: a unit blocks only when it is not a skip entry, not a rank-0 critter of at most 100 HP, and either stands within 25 yd of the point or belongs to a skipped leg's circle by its home. Roamers — units that moved between the quiet's two reads — are re-read jointly, and a near list cut short by the toolkit refuses the placement when its last listed unit stands at 25 yd or nearer. Then the commander teleports the raid onto the point, and the walk verifies the landing by the walk's own frames: a hire counts as placed when its freshest frame is inside the landing area (within 25 yd 2D of the point and within 8 yd of its floor). A missing hire gets a GM summon, up to three waves of 10 s each.

## The four tries

A quiet refusal, a `fighting after the place` after the move, or hires that did not arrive (`not at the start`) does not end the walk: in the same instance the placement gets up to four tries. While a move row stands before the start inside its leg, the attempt moves one move row back (up to three rows: stepping back over a boss row would re-fight it), re-runs the corridor and quiet there and teleports the party again; after that it retries the same point, with the units the last quiet named GM-killed first when `--gm-clear` or `--gm-clear-boss` is given. Only when every try fails does the walk give up with exit 29 (`place: 4 tries in the same instance ...`). No second instance is ever entered for the same placement: a fresh-instance retry would only burn the hourly cap.
