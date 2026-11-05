---
type: Troubleshooting
title: Plan troubleshooting
description: Real error messages from the plan tools, what they mean, and the fix.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/plan_check_cmd.go
tags: [plans, troubleshooting]
sources:
  - id: plan-check
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/plan_check_cmd.go
  - id: plan-export
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/plan_export_cmd.go
  - id: plan-scout
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/plan_scout_cmd.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T22:00:00+01:00
status: draft
---

# Plan troubleshooting

## `plan-check: --from-pull-group needs --from-leg ...`

A pull group number means nothing without its leg. Add `--from-leg N` before `--from-pull-group M`.

## `plan-check: "X" is not a plan name (a-z, 0-9, _ and -)`

The argument has characters outside the plan name set. Rename to lowercase letters, digits, `_`, and `-`.

## `plan-check: name the plans or pass --sql, not both` / `--plan picks a plan in the --sql file; ...`

You mixed the plan-file and migration inputs. Either name plans as arguments, or pass `--sql FILE` (with `--plan NAME` only when the file holds several).

## `plan-export: ... is a draft ... --allow-draft` (refused draft)

A draft plan is refused without opting in. Pass `--allow-draft` for a named plan (or fix the route and mark it mapped first). With `--all-live`, drafts are skipped with a note instead.

## `plan-export: companions_loot is false ... plan not exported`

The leg turns looting off and the rows cannot carry that. Pass `--drop-companions-loot` (and turn the bots' looting off in the run setup); add `--carrier-chests` as well to export the chest steps as carrier rows.

## `plan-export: ... names "X", which is no ... leg of the plan`

A name in `--skip-legs`, `--with-optional`, or `--with-legs` matches no leg (or the wrong kind: `--with-optional` needs an `optional` leg, `--with-legs` a `skip_export` leg). Check the spelling against the leg names in the plan file.

## `plan-export: ... exists: a migration that is applied is never edited`

`--out` refuses to replace a file. Export to a new timestamped name, or pass `--force` when you are sure the file was never applied.

## `plan-export: a step the run leader cannot do` / a `CUT:` line

An agent-only step (agent gossip, a `unit` wait without a hold, an agent area trigger) cut the export at its leg. Either give the step a run leader form (a `hold`), or accept the cut: everything before it still exports.

## `plan-scout: slot 3 is never used` (exit 15)

Slot 3 is reserved. Pick another slot.

## `plan-scout: "X" is not a test server ...`

The live pass runs only on the named test servers. Check the exact names in the usage line and use one of them; run the offline pass anywhere.

## `plan-probe: --mmaps is required`

The probe needs the navmesh tiles. Pass `--mmaps <mmaps-dir>`.

## `route-oracle: unknown --mode ...` / `unknown --objectives ...` / `--objectives bosses needs --mode bosses`

`--mode` takes `walk` or `bosses`; `--objectives` takes `plan` or `bosses`, and `bosses` objectives only work in `bosses` mode.

## `trash-check: --fail-on ...: danger, patrol or any`

`--fail-on` takes exactly those three words. Drop the flag to report without gating.

## `route-edits: ... has no checked version` / `is unchecked: ... no edit proven`

Only a `checked` (or accepted) version can be exported or promoted. A version stays `unchecked` when the plan already has plan-check issues of its own, so no edit of it is proven: fix the route's own issues and record a new version.

## `route-edits: the plan file is not the one its record names (sha256 differs): it was changed after it was written`

The stored version file (`vNNNN.json`) changed after it was written. Record a new version from the current file instead of exporting or promoting the changed one.

## `plan-check: no-item ...` / missing key

A locked object names no item: add `requires_item: {entry, name}` to the step, and make sure an earlier step (or a boss `loot_item`, or an `optional` leg's item) makes it.

## `plan-scout` slot, server, and cap errors

Slot 3 is never used (exit 15); the live pass only runs on the named test servers (exit 18). Exit 26 means the instance cap fired before anything started; exit 27 means a step failed or the apply was refused; exit 28 means the live pass aborted part-way (report and fixes are still written). A `rows_sha256` mismatch with `--sql` exits 2.

## `plan-probe: the navmesh cannot answer` (exit 1)

The build has no Cgo engine or no tiles for the map. Install the mmaps tiles and rebuild with the navmesh engine.

## `plan-export` draft, mute, and name errors

A draft needs `--allow-draft` (exit 2). A `companions_loot: false` leg needs `--drop-companions-loot`. An unknown `--skip-legs`, `--with-optional`, or `--with-legs` name (exit 2) matches no leg of that kind. An existing `--out` file needs `--force`.

## `plan-diff ... exists` (exit 2)

`--out` never replaces files. Diff into a fresh folder.

## `route-oracle` mode and objectives

`--mode` takes `walk` (default) or `bosses`; `--objectives` takes `plan` (default) or `bosses`, and `bosses` objectives need `bosses` mode. Exit 12 means not every plan walked fully; exit 2 means usage.

## `trash-check` mesh and cap notes

A mesh the navmesh cannot answer fails like the plan check (exit 2 through the shared unavailable path); usage errors exit 2; `--fail-on` findings exit 21.
