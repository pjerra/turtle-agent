---
type: Reference
title: Fault injection
description: The --inject grammar, arbitration, pass rules, allow-list, cleanup, exits and inject.json.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/raidwalk/inject.go
tags: [raids, inject, reference]
sources:
  - id: inject
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/raidwalk/inject.go
  - id: inject-allow
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/raidwalk/inject_allow.go
generated:
  by: omp
  at: 2026-11-05T21:45:00+01:00
status: draft
---

# Fault injection

`--inject` makes the walk inject a controlled fault and measure what the raid does about it. It is refused with `--place`, and the autopilot refuses it. Give at most 4 faults, each kind at most once; `kill-mt` needs an `ot` batch in the roster, or nobody would take over.[^inject]

## Grammar

One spec per `--inject` flag: `<kind>[@from=N][,key=int]`. `@from` (a step number from 1, default 1) goes right after the kind: the fault fires at or past that step. Anything the grammar does not name is a usage error naming the spec.[^inject]

| Kind | Keys | Defaults | What it does | Example |
|---|---|---|---|---|
| `kill-mt` | none | — | Kills the main tank with `.die` mid-pull and measures the pickup. | `--inject kill-mt` |
| `runaway` | `dist` 110-250 yd, `hold` 30-180 s | `dist=150`, `hold=90` | Moves a rest hire out, freezes it there, and waits for the run leader's summon ladder. | `--inject runaway,dist=150,hold=90` |
| `add-pack` | `max` 30-80 yd | `max=60` | Orders a hire to attack a nearby idle hostile and measures the extra fight. | `--inject "add-pack@from=5,max=50"` |
| `lag` | `secs` 10-90 s | `secs=30` | Freezes a rest hire in place, then releases it and measures the rejoin. | `--inject lag,secs=30` |

## Arbitration

One fault is active at a time, in command-line order. A fault fires only when its own step and fight rules hold; otherwise the last unmet condition is kept as its reason. The run blocks every fault while paused, on a boss step, while a boss is engaged, during a wipe, or within 2 steps of the `until` stop. After a verdict, no fault fires for 60 s, and none fires while a fight is on. A fight means a hostile targeting the party, or a hire in combat. A failed action ends its fault `inconclusive`, never the walk.[^inject]

- `kill-mt` fires on a pull step after 6 s of fight with 2 or more hostiles on the main tank, averaging 20-90% HP.
- `runaway` fires out of fight: the victim is the last alive rest hire out of combat.
- `add-pack` fires after 3 s of fight with an idle candidate inside `max` yd of the lead.
- `lag` fires out of fight with the same victim rule as `runaway`.

## Pass rules

- `kill-mt`: pass when another tank picks the attackers up within 10 s, the fight ends within 180 s, and the raid is still going 60 s after the fight. Fail on no pickup in 10 s or no end in 180 s.
- `runaway`: the victim is summoned back by the run leader's ladder. Pass on an F4 summon with the victim back within 15 s of it; fail on a straggler-ladder summon or no return. A fight over half the hold, or any boss sighting, ends the fault `inconclusive`.
- `add-pack`: pass when the extra pack engages, the fight ends within 240 s, the run continues past the step, and no pause stopped the walk. The pack must engage within 10 s or the fault is `inconclusive`.
- `lag`: pass when the victim rejoins within 30 yd of the lead within 120 s of the release and stays 30 s with no open pause. Fail on no rejoin in 120 s or a pause stop.

A wipe ends an active fault: fail when the fault already took effect, `inconclusive` while it still waited for it. Faults that never fire are `not-fired` with the last unmet condition. `inconclusive` and `not-fired` never change the exit code.[^inject]

## Selection proof and allow-list

Every acting line is proved by selection first: the walk targets the unit, reads `status.target` back, and only then sends the line. The `.gps` read-back (observe-only) plus the `.pinfo` identity read (`LANG_PINFO_ACCOUNT`) prove the target is real; else the fault is `inconclusive` and nothing is sent. Only the target and status reads get one retry.[^inject]

The injector may send only these lines, checked before sending (a refused line is a programming error and stops the walk):[^inject-allow]

- `.gps` and `.pinfo` (the read-backs, no arguments).
- `.gm visible off` before any fault starts.
- `.go xyz <x> <y> <z> <map>` on the plan's map; under a fault within 260 yd of the lead.
- `.summon <victim>` under `runaway` only.
- `.freeze <victim>` under `runaway` or `lag`, with the commander's own selection on it.
- `.unfreeze <name>` for cleanup, with the commander's own selection on it.
- `.die` under `kill-mt` only, with the main tank selected.
- One commander order: `attack <candidate-hex> <hire>` under `add-pack` only.

## Cleanup and exits

Cleanup runs on every exit before hold and teardown: select self, `.unfreeze` every bot still frozen, then select self once. A bot is released only when the walk acknowledges its `.unfreeze`; a failed or unattempted release is asked for again. When the recovery deadline runs out with a bot still frozen, the walk records a `blocking` `cleanup_incomplete` incident with the exact manual `.unfreeze` command, and exits 23 at least, whatever the walk's own reason.[^inject]

`inject.json` (format `tb-raid-inject` v1) holds the faults (kind, verdict, reason, step, victim, measured fields, lines sent) and the incidents. `summary.json` carries `injections[]`, `incidents[]`, and `deaths.injected` (the deaths a fault caused, not deaths of the run's own walk). A fault with verdict `fail` makes an otherwise clean walk exit 23; exits 18 and 19 keep their meaning over 23, while a blocking 23 wins over every code below it.[^inject]
