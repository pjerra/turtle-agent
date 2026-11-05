# Raids

Walk a raid plan with hired bots using `raid-walk`, keep many raids running with `raid-autopilot`, and regression-check plans with `raid-suite`.

## Start here
- [Raid walks, autopilot and suite](/raids/overview.md) — what the three commands do and how they fit together.
- [Walk your first raid](/raids/first-raid-walk.md) — one trash walk on a test server, start to finish.

## Guides
- [Run a raid walk](/raids/run-a-raid-walk.md) — pick an `until` clause, launch the walk, read the result.
- [Start partway into an instance](/raids/place-mid-raid.md) — `--place` with `--from`, plus `--reuse-instance` and `--gm-clear`.
- [Recover from wipes](/raids/recover-wipe.md) — `--wipe-resume`, `--wipe-rest`, `--revive-at-raid` and the revive quiet check.
- [Supervise raids with the autopilot](/raids/run-autopilot.md) — write `targets.txt`, run the supervisor, stop one raid or all of them.
- [Regression-check plans with the suite](/raids/run-raid-suite.md) — walk every plan and judge it against its last good walk.
- [Stay under the instance cap](/raids/manage-instance-cap.md) — the ledger, waiting, `--instance-cap-reset` and `--reuse-instance`.

## Concepts
- [How placement works](/raids/placement.md) — corridor clear, quiet check, landing check and the four tries in one instance.
- [The instance cap ledger](/raids/instance-cap.md) — what counts, per-account counting, reservations and the lock.
- [Until clauses and autopilot goals](/raids/until-and-goals.md) — `boss`, `boss-done`, `none`, `step N`, `leg L`, and `full`, `leg:`, `boss:` goals.
- [Hire gear tiers](/raids/tiers.md) — the committed tier table, `--tier`, `--tier-override` and comparison runs.

## Reference
- [`raid-walk` flags](/raids/raid-walk-reference.md) — every flag, default and example.
- [`raid-autopilot` reference](/raids/raid-autopilot-reference.md) — flags, `targets.txt`, `STOP`/`YIELD`, `status.txt`, checkpoint, exit codes.
- [`raid-suite` reference](/raids/raid-suite-reference.md) — flags, suite file, verdicts, exit codes.
- [`raid-walk report` reference](/raids/report-reference.md) — flags, inputs, the per-pull table and `report.json`.
- [Roster file format](/raids/roster-format.md) — `tb-raid-roster` v1 and v2 batches, specs and faction.
- [Two raids at once](/raids/pair-reference.md) — the `--slot A,B` pair and multi form, `pair.json`, exit order.
- [Troubleshooting raids](/raids/troubleshooting.md) — real error messages, what they mean, the fix.
