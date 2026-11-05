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

The leg turns looting off and the rows cannot carry that. Either pass `--drop-companions-loot` (and turn the bots' looting off in the run setup), or add `--carrier-chests` to export the chest steps as carrier rows.

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

Only a `checked` (or accepted) version can be exported or promoted. A version with plan-check issues the base plan did not have stays `unchecked`: fix the route and record a new version.

## `route-edits: plan file is not the one the record names (sha256 differs)`

The worktree's plan changed since the version's base. Rebase your edit: record a new version from the current file instead of promoting the stale one.
