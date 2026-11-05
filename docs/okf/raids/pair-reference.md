---
type: Reference
title: Two raids at once
description: The --slot A,B pair and multi form, pair.json, and how the exit code is picked.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/raid_walk_pair.go
tags: [raids, pair, reference]
sources:
  - id: pair
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/raid_walk_pair.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:55:00+01:00
status: draft
---

# Two raids at once

`turtle-agent raid-walk <plan> [<plan2>] --slot A,B` walks two raids at the same time, one per slot. Slot `3` is refused before anything for any server (exit 15); a repeated id is a usage error (exit 2). One plan walks that plan on both slots (two instances of it); two plans walk `plan1` on A and `plan2` on B.[^pair]

## How the two walks run

Each half is an ordinary raid-walk with its own lease, `slot setup`, agent process, dashboard client and output dir: `<out>/s<A>-<plan1>/` and `<out>/s<B>-<plan2>/` (each the normal `summary.json`, `run.log`, frames, ...), plus `<out>/pair.json` and `<out>/pair.txt` (`--out` default `./raid-walk-pair-<stamp>`, refused if it exists). The default holder is `raid-walk-<plan>-s<slot>`; two equal holders are refused. Walk B starts 30 s after walk A (`--stagger 0-600s`) so the two hire bursts (up to 40 bots each) do not hit the server at once. The walks are independent: one failing does not stop the other; SIGINT stops both and each tears down its own slot.

Per-walk flags may carry the slot id as a prefix: `--sql 1=a.sql --sql 2=b.sql`, `--inject 2=lag`. Unprefixed `--sql --hires --dir --patrols --until --from --place --tier --tier-override --holder --inject --decisions --decisions-max-mib` applies to both walks. `--place`, `--decisions` and `--allow-split-ledger` are switches: a bare flag turns them on for every walk, `--place 2=on` (or `2=off`) for one of them, and each placed walk needs its own `--from`. Every other flag is global and refuses a slot prefix (exit 2). `--dry-run` prints both phase plans, every line prefixed `[s<N> <plan>]`, without leasing or writing.


### Failures, prefixes and files

`--slot A,B` is its own flag: `--slots` (plural) names the slots file, so the pair takes the singular `--slot` with a comma list. A panic in either half is caught at the pair boundary: it becomes that half's failure, stops the other walk, and still runs the deferred cleanup before the report is written. Cleanup is done only on success plus one deferred retry: a step counts as done only after it confirms (teardown exit 0 with a report that parses, release returned nil), so a panic or a failure leaves it incomplete and the deferred pass retries it once. What is still held comes back as a lease-named note.[^pair]

Per-walk flags may carry a slot prefix: `--sql`, `--hires`, `--dir`, `--patrols`, `--until`, `--from`, `--place`, `--tier`, `--tier-override`, `--holder`, `--inject`, `--decisions` and `--decisions-max-mib` apply to both walks unprefixed, or to one walk prefixed (`--sql 1=a.sql`). `--place`, `--decisions` and `--allow-split-ledger` are switches: a bare flag turns them on for every walk, `2=on` or `2=off` for one of them. Every other flag is global and refuses a slot prefix with exit 2.[^pair]

`pair.json` carries one entry per walk: slot, plan as given, character, out dir, exit code, end reason, steps reached and total, deaths, fights, pauses, tier, and the dashboard tab (`#/commanders/<name>`). `pair.txt` is one line per walk (`s<slot> <plan>: <reason>; steps <r>/<t>; deaths <d>; fights <f>; pauses <p>; out <dir>; exit <c>`), one `note:` line per note, then `pair: exit <c>`. A two-slot run always keeps `pair.json`/`pair.txt`; three or more slots (up to 8, never 3) write `multi.json`/`multi.txt` instead, with the same rows plus `totals{walks, ended_by_until, failed, steps_reached, deaths, fights, pauses}` and a `total:` line. N full raids are N times the hires: two 40-man raids are already up to 80 bots, so plan the worldserver's memory.[^pair]

## The combined files

`pair.json` (format `tb-raid-walk-pair` v1) has one entry per walk (slot, plan, character, out, exit code, end reason, steps, deaths, fights, pauses, tier, and the dashboard tab), `overlap_s` (seconds both runs were between their start and their end, from the two summaries), `dashboard_both_listed` (true when one `/api/v1/commanders` dump of either walk names both characters; `null` with the dashboard off) and `notes`. `pair.txt` is one line per walk, one `note:` line per note, then `pair: exit <c>`. The pair's exit is the first code that occurs in the order 2, 15, 18, 19, 23, 0 over the two walks, so a wipe (19) wins over a failed fault (23); a blocking incident (a bot left frozen, 23) ranks ahead of 18 and 19 in both walks, so it never hides.

Three or more raids at once: `--slot A,B,C[,...]` (up to 8 distinct ids, never `3`) walks one raid per slot, in the slots' order (any other plan count is a usage error). The combined files are `<out>/multi.json` (format `tb-raid-walk-multi` v1) and `<out>/multi.txt`; a two-slot run keeps `pair.json`/`pair.txt` unchanged. `multi.json` has the pair's `walks[]`, `overlap_s` (seconds all walks ran together), `dashboard_all_listed`, `exit_code`, `notes` and `totals`: `walks`, `ended_by_until`, `failed`, `steps_reached`, `deaths`, `fights`, `pauses`. N full raids are N times the hires: plan the worldserver's memory (two 40-man raids are already up to 80 bots).
