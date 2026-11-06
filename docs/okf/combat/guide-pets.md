---
type: Guide
title: Pets and their orders
description: What the shared pet strategy sends, when it recalls, and what it never drives.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/brain/pet.go
tags: [combat, pets]
sources:
  - id: pet-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/brain/pet.go
generated:
  by: omp
  at: 2026-11-05T21:45:00+01:00
status: draft
---

# Pets and their orders

You drive the hunter's pet or the warlock's demon with the shared `pet` strategy (`pet_imp` names the Imp-only demon list). A game that cannot drive a pet (no `PetGame` half) plays the class without it. Feed Pet is never driven: the diet is not visible, so the pet RPC shows happiness (1 unhappy, 2 content, 3 happy) and you feed by hand.

## 1. Attack and recall

| Order | Relevance | When |
|---|---|---|
| Attack | 20 | Fight's mob is held (on the tank, you, or the pet; no tank to wait for), in combat (never pulls), not ordered at in the last 2.5 s (`petAttackHold`); a pet told to stay is left alone |
| Follow (fight over) | 16 | Fight over with the pet still on a target |
| Follow (fleeing) | 32 | You move in combat with the pet's mob more than 40 yd off (`petLeash`); no attack goes out meanwhile (3 s recall hold, `petRecallHold`) |

The pet is sent at the tank's mob only after the 2 s opening delay and only once the tank holds it. The order goes out once per mob (the pet's own target shows it).

## 2. Stance and taunt

In a group a pet on aggressive is put on defensive (relevance 13), once per pet for 20 min (`petOnce`); passive and defensive are left alone, and a solo player's pet is left as it is. With a tank in the group the pet's taunt (Growl, Torment, Suffering) has its autocast switched off (relevance 13), once per pet and spell; solo it stays on, and it is never switched back on.

## 3. Summon, revive, mend

The hunter's Call Pet (no pet out), Revive Pet (pet dead, relevance 15), and Mend Pet (below 50% in combat, 70% out of combat, within 20 yd `petMendMaxRange`, relevance 22) look after the pet at rest; the warlock summons the best of its list the spellbook and bags allow (Voidwalker needs a shard, Imp none; relevance 14). A summon or revive is not retried within 12 s (`petSummonHold`); Call Pet with no pet to call holds a minute (`no_pet`).
