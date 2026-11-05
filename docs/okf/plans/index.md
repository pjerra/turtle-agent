# Plans

One-line intro: dungeon and raid plans are the JSON routes parties run, and the tools that make, check and ship them.

## Start here
- [Plans overview](overview.md) — what a plan is and how the tools around it fit together.
- [Check your first plan](first-plan-check.md) — run the three offline checks on a mapped plan, start to finish.

## Make and change plans
- [Write a plan file](write-a-plan.md) — draft a new plan and edit an existing one by hand.
- [Record a patrol](record-a-patrol.md) — measure a patrolling pack with `patrol-record` for a plan's `patrol` block.
- [Apply route edits](apply-route-edits.md) — move a dashboard route version into `dungeons/` and ship it.

## Check plans
- [Check a plan on the navmesh](check-a-plan.md) — `plan-check`, its lints, and what to do about each finding.
- [Scout a plan before a run](scout-a-plan.md) — `plan scout` offline rules and the live pass.
- [Probe the run leader's rows](probe-a-plan.md) — `plan-probe` and what a bad segment means.
- [See what a walk wakes](trash-check.md) — `trash-check` and the patrol windows.
- [Compare a route with the navmesh](route-oracle.md) — `route-oracle` walk and bosses modes.

## Ship plans
- [Export a migration](export-a-plan.md) — `plan-export`, `plan-diff`, and the `CUT:` line.

## Understand plans
- [Anatomy of a plan file](plan-format.md) — legs, pull groups, steps, bosses, and the plan-wide lists.
- [Draft, mapped, live-checked](plan-lifecycle.md) — plan statuses and the mapping walk.
- [Dungeon notes](dungeon-notes.md) — RFC/ZF/Uldaman/WC/Deadmines evidence and first-run risks.

## Reference
- [plan-check, trash-check, path-check](ref-check-tools.md) — every flag, default and exit code.
- [plan scout, plan-probe, patrol-record](ref-scout-probe.md) — every flag, default and exit code.
- [plan-export, plan-diff, route-edits](ref-export-tools.md) — every flag, default and exit code.
- [route-oracle](ref-route-oracle.md) — every flag, default and exit code.

## Fix things
- [Plan troubleshooting](troubleshooting.md) — real error messages, what they mean, the fix.
