---
type: Concept
title: How placement works
description: "What --place does inside the instance: the staged start, the corridor clear, the quiet check and the four tries."
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

The walk judges the start point quiet itself, not the toolkit's flag: a unit blocks only when it is not a skip entry, not a rank-0 critter of at most 100 HP, and either stands within 25 yd of the point or belongs to a skipped leg's circle by its home. Roamers — units that moved between the quiet's two reads — are re-read jointly, and a near list cut short by the toolkit refuses the placement when its last listed unit stands at 25 yd or nearer. A boss of a later plan row standing 25 to 30 yd out never blocks, but is noted (`place: the quiet sees boss ...`): inside a boss's detection range the landing can pull it. Then the commander teleports the raid onto the point, and the walk verifies the landing by the walk's own frames: a hire counts as placed when its freshest frame is inside the landing area (within 25 yd 2D of the point and within 8 yd of its floor). A missing hire gets a GM summon, up to three waves of 10 s each, each read only after a 4 s settle: summoned hires land a moment after their line. A `.summon` never moves a corpse: dead hires are named apart.

The `.gm on` probe runs before the first teleport: the commander must be in GM mode or `.appear` and the clears fail late. The commander drops his own bind to the plan's map after the portal, so `.appear` lands him in the raid's copy, not an older walk's; under `--reuse-instance` the bind is kept because the walk resumes it. A wipe revives at the placed start, not at step 1.

### Floor unknown, offline clear and ownership

You read the toolkit floor with `q floor` under the start point. A floor of exactly `0.0`, or one at or below the core's `INVALID_HEIGHT` (-100000), is unknown, not wrong: the core leaves `z` at 0 when the height lookup finds nothing (an unloaded grid, no vmap, the wrong map). The walk reads once more after 2 s; when the floor is still unknown it takes the plan's `z` only when the point is a recorded walk row's own point (`PlaceWalkRow`: a walk/move row the plan walked through), and writes `place: floor unknown, using plan z ...` to `run.log` and the summary notes. A pull stand point, or any other row's point, keeps its failure: nothing walked through it to vouch for its `z`.[^walk-place]

A floor the toolkit knows that differs from the plan by more than 3 yd refuses the placement (`place: the start step's floor is X, not Y`). A reply with no `floor` at all (a null `floor`) is no answer and fails the same way.

Before a fresh instance is entered, the walk also walks the requested start back along the plan's positioned move rows, with no network, to the nearest row whose point stands clear of every static spawn of the packs the run has not pulled yet, plus the next boss row of the start's own leg. A row must stand at least each spawn's aggro radius plus 8 yd away; a spawn-less boss counts its default 20 yd detection range plus the same 8 yd. The requested row is tested first, and the note names the chosen row and the nearest hazard. A world with no such spawn leaves the start as given.[^place]

Ownership decides which units the quiet and the extra rounds may touch. A unit is BEHIND trash only when a pull row before the start holds its entry and owns its home: the home lies within that row's radius plus 6 yd and within the AHEAD height window. An entry alone is not ownership: entries are shared, so a later leg's patrol can carry a corridor row's entry while spawning outside every BEHIND row's area. Boss evidence where the unit stands or at its home (for example Thekal's 11361 tigers, which count only within 40 yd of Thekal) is never BEHIND trash. A unit with no home in the reply has an unknown spawn, so no row owns it: the extra clear never runs for it, and the quiet fails closed unless the unit stands beyond the landing reach.[^place][^walk-place]

## The four tries

A quiet refusal, a `fighting after the place` after the move, or hires that did not arrive (`not at the start`) does not end the walk: in the same instance the placement gets up to four tries. While a move row stands before the start inside its leg, the attempt moves one move row back (up to three rows: stepping back over a boss row would re-fight it), re-runs the corridor and quiet there and teleports the party again; after that it retries the same point, with the units the last quiet named GM-killed first when `--gm-clear` or `--gm-clear-boss` is given. A try that finds at least half the hires dead in the freshest frames, or the start leg's boss engaged with (targeting) a hire, ends the placement at once instead of trying again: more tries in that copy only feed the boss, so the walk exits 29 with `place: party dead ...` or `place: boss engaged with the party ...: fresh instance needed` and the autopilot takes a fresh instance. Only when every other try fails does the walk give up with exit 29 (`place: 4 tries in the same instance ...`). No second instance is ever entered for the same placement: a fresh-instance retry would only burn the hourly cap.[^walk-place]

