---
type: Concept
title: The spell oracle
description: What each knowledge-base spec casts every fight, and how finished fights are judged.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/kbspells/oracle.go
tags: [combat, oracle, concept]
sources:
  - id: kbspells-oracle
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/kbspells/oracle.go
  - id: kbspells-judge
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/kbspells/judge.go
  - id: rotations-doc
    resource: https://github.com/pjerra/turtle-agent/blob/dev/docs/ROTATIONS.md
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:50:00+01:00
status: draft
---

# The spell oracle

The oracle says what each Turtle knowledge-base spec casts: the spells it presses every fight (`core`), the ones for some fights (`situational`), the ones it must never cast (`never`), and the talents that tell one spec from another (`signature`). One list per spec, generated from the KB rotations by `tools/kbrotgen` and shipped in the binary.

## Judging a bot

The judge reads one bot's casts after the fact, from a trace or a finished fight. A core spell with no cast is a warning, unless the bot is too young for its first rank or the cast list was capped. A never spell is always a warning. Spells outside every list are reported as off-list, information only. The judge never fails anything: other checks keep the exit code.

When the bot carries no spec, the judge guesses it: the role picks a tank or healer spec, and a dps bot takes the spec whose signature talents it cast most. No signature, or a tie, means the class-wide lists: the core every dps spec shares, and the union of their situational lists.

## Not judged

A bot stays `na` when it was active too briefly (under a minute in a trace, under 20 seconds in a fight by default), when its cast list may be capped, or when its class has no oracle data. A fight under the minimum length is `na` too.

## The shared lists

Beside the per-spec lists, the oracle holds what every class shares: the taunts the fight debugger counts, the consumables and item effects bots use, each class's generic buffs, forms and utility, and the debugger's limits. The `fight-score` command prints the debugger's two tank checks with the same numbers, then one oracle line per bot. Start from [Score a fight](/combat/guide-score-fight.md).
