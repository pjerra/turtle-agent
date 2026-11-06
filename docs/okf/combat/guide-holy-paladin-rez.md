---
type: Guide
title: Holy paladin and resurrection
description: Blessings by role, emergency buttons, the off-preset judge, and how the shared resurrection works.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/brain/paladin_holy.go
tags: [combat, paladin, healer, resurrection]
sources:
  - id: paladin-holy-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/brain/paladin_holy.go
  - id: rez-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/brain/rez.go
generated:
  by: omp
  at: 2026-11-05T21:45:00+01:00
status: draft
---

# Holy paladin and resurrection

You heal as `holy_paladin` (the healer role with the paladin's kit: Holy Light plus fast Flash of Light, Cleanse at 42 else Purify), bless the party by role out of combat, and answer emergencies. Resurrection is the shared `rez` strategy every healing class carries.

## 1. Blessings by role, one aura

Out of combat at a rest gate, above 30% mana, the paladin blesses every member in view and itself (relevance 13/12, within 28 yd `buffReach`, 6 s resend hold `blessingHold`): the tank gets Kings else Might (never Salvation: the tank wants the threat); healers and mana casters get Wisdom; melee and hunters get Might; the rest get Kings then Salvation. A member who carries any blessing is left alone (a companion paladin's counts; never cast over it); the paladin's own is renewed under a minute left. One aura is kept (Concentration, else Devotion); a companion's paladin aura is never fought.

## 2. Emergencies

Lay on Hands lands on the least-healthy member (or the paladin) below 15% in a fight, in reach and sight (`layOnHandsHolyPct`, relevance 45). Hand of Protection (data name of Blessing of Protection) lands on a non-tank, non-self member below 40% with a mob hitting it in melee — two on it, or a quarter of its health lost in 3 s — in reach and sight (`protectPct`, `protectMelee` 8 yd, relevance 44; never with Forbearance, never on the tank, never over itself). Divine Shield lands on the paladin below 15% in a fight with a mob on it or burst damage and no Forbearance (`shieldHolyPct`, relevance 43; in this data it only reduces damage, so healing goes on). Divine Favor, when known as a castable spell, goes first (relevance 42; in this data its ranks are talent passives 51312–16, so the cast never happens; while it is up the big heal replaces the quick one, `NoFast`).

`paladin_judge` is off by default (not in the preset): seals and Judgement above 60% mana (`judgeMana`) with no heal needed, held by the tank, within 9 yd (`judgeRange`). Hammer of Justice is the interrupt (relevance 40, within 10 yd).

## 3. Resurrection, shared

Out-of-combat resurrections (Resurrection 75% base mana, Redemption 80%, Ancestral Spirit 90%; 10 s cast, 30 yd, relevance 14) fire at a rest gate with no party combat; Rebirth (85%, 2 s, 30 min own cooldown, reagent 17034–38) fires in combat on a key member (tank or healer, relevance 26). Cast from within 28 yd (`rezReach`); walk to a dead member out of reach but within 40 yd (`rezFarMax`, `rez_approach` relevance 13). Order is healers, then tank, then others, nearest first; forms bar some (Resurrection in Shadowform, Rebirth in Moonkin). One at a time (14 s hold `rezSentHold`); three declines leave the member alone 5 min (`rezMaxTries`, `rezCapSkip`); another caster's in-flight resurrection is never doubled. While one waits on mana, the healer drinks to the top. The dungeon driver's gate asks `CanRez` (true 3 s after the brain last saw a member it could raise, `rezSeenFor`) and waits 120 s (`BrainRezWait`) before the corpse run.
