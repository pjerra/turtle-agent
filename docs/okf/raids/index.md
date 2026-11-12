# Raids

Walk a raid plan with hired bots using `raid-walk`, keep many raids running with `raid-autopilot`, and regression-check plans with `raid-suite`.

## Start here
- [Raid walks, autopilot and suite](overview.md) — what the three commands do and how they fit together.
- [Walk your first raid](first-raid-walk.md) — one trash walk on a test server, start to finish.

## Guides
- [Run a raid walk](run-a-raid-walk.md) — pick an `until` clause, launch the walk, read the result.
- [Start partway into an instance](place-mid-raid.md) — `--place` with `--from`, plus `--reuse-instance` and `--gm-clear`.
- [Recover from wipes](recover-wipe.md) — `--wipe-resume`, `--wipe-rest`, `--revive-at-raid` and the revive quiet check.
- [Supervise raids with the autopilot](run-autopilot.md) — write `targets.txt`, run the supervisor, stop one raid or all of them.
- [Regression-check plans with the suite](run-raid-suite.md) — walk every plan and judge it against its last good walk.
- [Follow the lead bot as a player](follow-player.md) — trail, holds and stops.

## Concepts
- [How placement works](placement.md) — corridor clear, quiet check, landing check and the four tries in one instance.
- [The instance cap ledger](instance-cap.md) — what counts, per-account counting, reservations and the lock.
- [Until clauses and autopilot goals](until-and-goals.md) — `boss`, `boss-done`, `none`, `step N`, `leg L`, and `full`, `leg:`, `boss:` goals.
- [Hire gear tiers](tiers.md) — the committed tier table, `--tier`, `--tier-override` and comparison runs.

## Reference
- [`raid-walk` flags](raid-walk-reference.md) — every flag, default and example.
- [`walk-preflight` reference](walk-preflight.md) — check a walk directory before any raid-walk starts.
- [`raid-autopilot` reference](raid-autopilot-reference.md) — flags, `targets.txt`, `STOP`/`YIELD`, `status.txt`, checkpoint, exit codes.
- [`raid-suite` reference](raid-suite-reference.md) — flags, suite file, verdicts, exit codes.
- [`raid-walk report` reference](report-reference.md) — flags, inputs, the per-pull table and `report.json`.
- [Roster file format](roster-format.md) — `tb-raid-roster` v1 and v2 batches, specs and faction.
- [Fault injection](fault-injection.md) — kill-mt, runaway, add-pack, lag.
- [Troubleshooting raids](troubleshooting.md) — real error messages, what they mean, the fix.
