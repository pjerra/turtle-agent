---
type: Guide
title: Pets, druids, shamans
description: How the shared pet strategy attacks and recalls, and what druids and shamans cast around their forms and totems.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/brain/pet.go
tags: [combat, pets, druid, shaman]
sources:
  - id: pet-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/brain/pet.go
  - id: druid-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/brain/druid.go
  - id: shaman-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/brain/shaman.go
generated:
  by: omp
  at: 2026-11-05T21:45:00+01:00
status: draft
---

# Pets, druids, shamans

You drive the hunter's pet or the warlock's demon with one shared strategy, and play druid forms or shaman totems around it. A game that cannot drive a pet (no `PetGame` half) plays the class without it.

## 1. Pet: attack once held, recall when over

| Order | Relevance | When |
|---|---|---|
| Attack | 20 | Fight's mob is held (on the tank, you, or the pet; no tank to wait for), in combat (never pulls), not ordered at in the last 2.5 s (`petAttackHold`); a pet told to stay is left alone |
| Follow (fight over) | 16 | Fight over with the pet still on a target |
| Follow (fleeing) | 32 | You move in combat with the pet's mob more than 40 yd off (`petLeash`); no attack goes out meanwhile (3 s recall hold, `petRecallHold`) |
| Defensive | 13 | Pet on aggressive in a group (once per pet, 20 min, `petOnce`) |
| Taunt off | 13 | Growl, Torment or Suffering on autocast with a tank in the group (once per pet and spell; solo it stays on) |
| Summon | 14 | No pet out at rest: hunter's Call Pet, warlock's best list (Voidwalker with a shard, else Imp; `pet_imp` forces Imp); not retried within 12 s (`petSummonHold`) |
| Revive | 15 | Pet dead at rest with full mana within 12 s of trying (hunter's Revive Pet; a demon is gone in 15 s) |
| Mend Pet | 22 | Pet below 50% in combat (70% out of combat, `petMendCombat`/`petMendRest`) within 20 yd (`petMendMaxRange`), without the aura |

Feed Pet is not driven: it needs food by the pet's diet, which the agent cannot tell. The pet RPC shows its happiness (1 unhappy, 2 content, 3 happy) instead. The hunter's Call Pet with no pet to call holds a minute (`no_pet`).

## 2. Druid: caster home, cat and bear kits

Presets: `resto_druid`, `balance_druid` (band 20–28 yd), `feral_cat_druid`, `bear_tank_druid`. Resto and balance live in caster form and leave any other (`druid_caster`, relevance 42 with a veto on other casts); the leave needs the aura canceller and waits out the 2 s shift hold (`druidShiftHold`). Shifts are cast from caster form, where the power is mana and the cost reads. Never leave a form in combat; out of combat a feral leaves its home form to drink (below 50% mana, drink in bags), eat (below 85% health, food in bags; items never work in form), or buff (Mark of the Wild pending, mana 60%+). Item casts in form (food, drink, potions, stones, bandages) are vetoed to caster form.

Balance (caster): Moonfire above 35% mob health and Insect Swarm above 40% (relevance 15/14), Starfire above 35% mana on a mob above 30% else Wrath (11/10, standing still), Entangling Roots on a mob on you below 80% within 10 yd while standing still (relevance 26), then a step away (relevance 30). A roots refusal for indoors backs off 30 s (`outdoors_only`). Cat: Faerie Fire (Feral) on a fresh mob above 50% (16), Tiger's Fury above 60 energy on a mob above 40% (15), Rake above 50% (14), Shred from behind else Claw (13/12), Rip at 5 combo points on a mob above the bleed floor (18), Ferocious Bite at 5 (or 3+ at or below 35%) (17). Bear: Maul from 40 rage in tenths (10, costs 150), Swipe with 2+ mobs (15), Demoralizing Roar with 2+ mobs lacking it (13), Growl taunt via `role_tank` (25), Enrage with no rage in reach (12), Frenzied Regeneration below 35% in combat (emergency 43). Buffs (Mark of the Wild 13, Thorns on the tank 12, only in caster form above 30% mana), Innervate below 20% mana in a fight (23), Rebirth in combat only (see [Holy paladin and resurrection](guide-holy-paladin-rez.md)). The druid never fights in travel/aquatic form.

## 3. Shaman: shields, imbues, totems

Presets: `resto_shaman`, `elemental_shaman` (band 14–19 yd), `enhancement_shaman`. Lightning Shield out of combat above 25% mana (13), in combat above 50% (8). Weapon imbue at rest above 30% mana (12), renewed 5 min before it ends (`imbueRefresh`), shown within 10 s (`imbueShowWithin`): Windfury Weapon (30), else Flametongue (10), else Rockbiter (1). Elemental: Flame Shock above 30% mana on a mob above 40% (15), Lightning Bolt filler (10), Chain Lightning with 2+ mobs in 12 yd (13), Earth Shock interrupt (40, within 20 yd). Enhancement: Stormstrike when known (17), Flame Shock above 30% mana on a mob above 50% (15), Earth Shock filler above 60% mana (13); the shock pair shares its cooldown.

Totems drop one slot at a time (fire, air, water, earth; 10 s per slot, `totemGap`; resto relevance 7, elemental/enhancement 11) when a fight is on near the tank and the slot is empty, expired (spell lifetime, else 60 s `totemDefLife`), farther than 30 yd (`totemFar`), or its creature is gone (given 3 s to show, `totemGrace`); never below 25% mana or for a mob below 25% health (`totemMinMana`, `totemMinMob`). Each element needs its tool in the bags (Earth 5175, Fire 5176, Water 5177, Air 5178; spells 24854/27621 need none). Resto drops Mana Spring (else Healing Stream) plus Stoneskin; elemental drops Searing plus Mana Spring; enhancement drops Searing, Windfury (else Grace of Air), Strength of Earth. The shaman never turns Ghost Wolf. A totem drop recorded on send turns into the slot record when the cast lands; a refusal frees the slot at once.
