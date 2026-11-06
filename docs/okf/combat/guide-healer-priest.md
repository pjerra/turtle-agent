---
type: Guide
title: Heal and play a priest
description: How the healer triages the party, picks heals and dispels, stands, and what the priest adds.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/brain/heal.go
tags: [combat, healer, priest]
sources:
  - id: heal-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/brain/heal.go
  - id: priest-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/brain/priest.go
generated:
  by: omp
  at: 2026-11-05T21:45:00+01:00
status: draft
---

# Heal and play a priest

You heal as `role_healer` with your class's heal kit: the triage picks the member, the deficit picks the spell, dispels run beside it, and your feet stay where the heals reach. The priest adds a shield, a fear answer, buffs, and a shadow damage rotation.

## 1. Triage: who gets the heal

Every party member in view scores `hp% + distance/10`, minus 8 for the tank (`healTankBonus`). The lowest wins. A member out of reach (past 38.5 yd, `healReach`), without sight (the navmesh ray, or a remembered server refusal), or dead is no candidate. The hp% counts heals other casters already have in flight, so two healers do not heal the same hurt.

Bands pick the relevance: below 85% in combat (90% for the tank, 90% at a rest gate) is a candidate; the emergency band fires below 25% (35% for the tank) at relevance 41, low health below 45% at 28, medium below 65% at 20, light above that at 10 in combat and 8 at rest. Below 40% mana the bands drop 15 points (`manaSavePct`, `manaSaveDrop`), down to the emergency floor. Urgent dispels run at 25, plain ones at 11; the tank's heal-over-time ticks along at 9.5.

## 2. Spell pick: what covers the deficit

The brain casts the smallest heal that covers 90% of the deficit (`healCoverFrac`), else the biggest. A deficit under half of the smallest heal is left alone (mostly overheal). Below 45% the quickest covering heal goes; in an emergency (below 25%, tank below 35%) the biggest fast heal goes. Fast heals are dear: above 45% they wait unless mana is good, and below 40% mana they wait unless the member is near dying. Damage lost in the last 3 s (25% or more, `burstWindow`/`burstLossPct`) marks burst: the shield answers it on a priest. At a rest gate the top-up waits for the drink.

## 3. Heal over time and dispels

The tank keeps the class HoT (Renew, Rejuvenation) while in combat and hurt or taking damage, without the aura and with mana above 40% (relevance 9.5). Dispels remove what the class cures by aura type: urgent (crowd control: charm, fear, sleep, stun, polymorph, horror) at relevance 25 with 20% mana, anything else at relevance 11 with 40% mana (`dispelManaMin`, doubled). The dispel fires within its own range minus 1 yd (`dispelSlack`); for urgent crowd control past that the healer walks in (`heal_approach`, relevance 24). Each aura waits 4 s between dispel tries (`dispelHold`). `Mutating Injection` is never dispelled (`noDispel`), nor is an aura the plan's tactics forbids (`no_dispel`).

## 4. Where the healer stands

The healer stands 20–30 yd from the tank (`healerMin`/`healerMax`), with 3 yd of slack, sight of the party, out of the mob's front, and off ground hazards. It walks to a hurt member it cannot reach or see (`heal_approach`), and runs to the tank with a mob on it (`run_to_tank`, relevance 30). Spots sit on rings at 22/25/29 yd from the tank and keep 4 yd inside the heal reach (`healReachMargin`), so a heal cast from the spot still lands.

Sitting matters: a `not_standing` refusal stands the player first (1 s hold), and Food/Drink auras gate the meal rules. At a rest gate the healer drinks below 80% mana (`healerDrinkPct`).

## 5. Priest: shield, fear, buffs, shadow

Presets: `holy_priest` (`role_healer`, `priest_holy`, `priest_defense`, `priest_buffs`, `rez`, `healer_position`, ...) and `shadow_priest` (`combat_dps`, `priest_shadow`, ..., band 14–19 yd). The priest heals with Lesser Heal, Heal, Greater Heal, and fast Flash Heal; it dispels magic with Dispel Magic, disease with Abolish Disease (better) or Cure Disease.

Power Word: Shield goes on a member losing health fast (relevance 26) and on the tank before the pull with 50% mana (relevance 21); a shielded member (shield or Weakened Soul, or shielded in the last 6 s) is left alone (`shieldHold`). Fade answers a mob on the priest (relevance 23). Psychic Scream is the emergency below 70% health with 2+ mobs in melee (relevance 43), held back while any unengaged mob stands within 30 yd (`screamFree`) so the fear does not pull the next pack.

Out of combat the priest keeps Inner Fire on itself and Power Word: Fortitude on the party within 28 yd (relevance 13/12), only above 30% mana and out of Shadowform. The shadow priest takes Shadowform when known (relevance 16, after buffs out of combat), keeps Vampiric Embrace and Shadow Word: Pain up above 30%/25% mob health (relevance 15/14), Mind Blast on cooldown (13), Mind Flay filler (10), and shields itself below 80% with a mob on it (24). Silence is the interrupt (shadow only, relevance 40).
