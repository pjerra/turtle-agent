# Plans

One-line intro: dungeon and raid plans are the JSON routes parties run, and the tools that make, check and ship them.

## Start here
- [Plans overview](/plans/overview.md) — what a plan is and how the tools around it fit together.
- [Check your first plan](/plans/first-plan-check.md) — run the three offline checks on a mapped plan, start to finish.

## Make and change plans
- [Write a plan file](/plans/write-a-plan.md) — draft a new plan and edit an existing one by hand.
- [Record a patrol](/plans/record-a-patrol.md) — measure a patrolling pack with `patrol-record` for a plan's `patrol` block.
- [Apply route edits](/plans/apply-route-edits.md) — move a dashboard route version into `dungeons/` and ship it.

## Check plans
- [Check a plan on the navmesh](/plans/check-a-plan.md) — `plan-check`, its lints, and what to do about each finding.
- [Scout a plan before a run](/plans/scout-a-plan.md) — `plan scout` offline rules and the live pass.
- [Probe the run leader's rows](/plans/probe-a-plan.md) — `plan-probe` and what a bad segment means.
- [See what a walk wakes](/plans/trash-check.md) — `trash-check` and the patrol windows.
- [Compare a route with the navmesh](/plans/route-oracle.md) — `route-oracle` walk and bosses modes.

## Ship plans
- [Export a migration](/plans/export-a-plan.md) — `plan-export`, `plan-diff`, and the `CUT:` line.

## Understand plans
- [Anatomy of a plan file](/plans/plan-format.md) — legs, pull groups, steps, bosses, and the plan-wide lists.
- [Draft, mapped, live-checked](/plans/plan-lifecycle.md) — plan statuses and the mapping walk.
- [Dungeon notes](/plans/dungeon-notes.md) — RFC/ZF/Uldaman/WC/Deadmines evidence and first-run risks.

## Reference
- [plan-check, trash-check, path-check](/plans/ref-check-tools.md) — every flag, default and exit code.
- [plan scout, plan-probe, patrol-record](/plans/ref-scout-probe.md) — every flag, default and exit code.
- [plan-export, plan-diff, route-edits](/plans/ref-export-tools.md) — every flag, default and exit code.
- [route-oracle](/plans/ref-route-oracle.md) — every flag, default and exit code.

## Fix things
- [Plan troubleshooting](/plans/troubleshooting.md) — real error messages, what they mean, the fix.
