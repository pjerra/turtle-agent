---
type: Guide
title: Rogue, hunter, retribution paladin, warlock
description: What each melee and ranged damage rotation casts, when, and where it stands.
resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/brain/rogue.go
tags: [combat, dps, rogue, hunter, paladin, warlock]
sources:
  - id: rogue-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/brain/rogue.go
  - id: hunter-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/brain/hunter.go
  - id: paladin-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/brain/paladin.go
  - id: warlock-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/brain/warlock.go
generated:
  by: omp
  at: 2026-11-05T21:45:00+01:00
status: draft
---

# Rogue, hunter, retribution paladin, warlock

You play one of four damage rotations. Every one fights the tank's mob after the 2 s opening delay, never pulls with area damage while a loose mob stands near, and leaves Stealth openers, poisons, Backstab-by-dagger, Exorcism-by-type and over-watch choices out (the agent cannot see what they need).

## 1. Rogue: points, Slice and Dice, escape

Preset `combat_rogue` (`combat_dps`, `rogue_combat`, `melee_position`, ...). Combo points sit 0–5 on one target; energy is whole points.

| Cast | Relevance | When |
|---|---|---|
| Auto-attack | 19 | Mob in reach |
| Sinister Strike | 15 | Fewer than 5 combo points on the fight's mob |
| Slice and Dice | 18 | 2+ combo points, mob above 40% health, aura down or under 1.5 s left (cast at the mob: the server refuses it aimed at yourself) |
| Eviscerate | 17 | 5 combo points, or 4 on a mob at or below 35% |
| Feint | 20 | Melee mob on you with a tank in view (threat off, 10 s own cooldown) |
| Evasion | 43 | Mob in melee and health below 50% |
| Vanish | 44 | Mob on you and health below 25% (last resort: threat wipe plus stealth, no opener follows) |
| Sprint + run | 42 / 33 | Health below 30% with a mob on you and a tank farther than 8 yd away (`pickupFrom`) |

Kick is the interrupt (relevance 40). No Stealth openers (Cheap Shot, Garrote, Ambush need a stealthed walk-up), no Backstab (needs a dagger the agent cannot tell), no poisons (applying one needs an item target).

## 2. Hunter: shots from the band, aspects, feint

Preset `marksman_hunter` (`combat_dps`, `hunter_marksman`, `hunter_position`, `pet`, ...). Shots fly 8–35 yd; the dead zone runs to 8 yd.

| Cast | Relevance | When |
|---|---|---|
| Auto Shot (spell 75) | 19 | Fight's mob; kept running (a shot seen in the last 6 s, `autoShotLive`), restarted after a walk, refusal, feint, or new mob |
| Hunter's Mark | 18 | Mob above 50% health, mana above 20% |
| Multi-Shot | 17 | 2+ mobs within 10 yd of the target (`multiShotRadius`), mana above 20%, no unengaged mob within 12 yd (`multiShotLooseRad`) |
| Serpent Sting | 16 | Mob above 40% health, mana above 20% |
| Arcane Shot | 15 | Mana above 20% |
| Concussive Shot | 32 | A mob running at you from outside the dead zone (moving, unslowed) |
| Feign Death | 41 | Mob on you within 15 yd (`feignRange`) with a tank in view; cancels Auto Shot first, shots wait 6 s (`feignWait`) |
| Swing + Raptor Strike | 19 / 16 | Mob within 6 yd (`hunterMeleeReach`); Raptor Strike is a next-swing queue (1.5 s hold) |
| Aspect of the Viper | 20 | Mana below 20% in combat (level 56+, `viperOnPct`); back to Hawk from 60% (`viperOffPct`) |
| Aspect of the Hawk | 12 | No aspect up (home aspect; Cheetah/Pack never cast, shaken off in combat) |

The hunter stands 10–30 yd from the fight's mob (`Band`), stepping back within 9 and forward past 31, out of its front within 14 yd (`hunterFrontMax`), with sight. A walk restarts the shots at the new place. A `no_ammo`/`need_ammo` refusal holds the shots 30 s. Feed Pet is not driven (the diet table is not visible); the pet RPC shows its happiness.

## 3. Retribution paladin: seals, judgement, mana floors

Preset `ret_paladin` (`combat_dps`, `paladin_retribution`, `melee_position`, ...). One seal is kept up while a mob is fought, judged on its 10 s cooldown, then cast again.

| Cast | Relevance | When |
|---|---|---|
| Auto-attack | 19 | Mob in reach |
| Seal | 18 | No seal up and fight's mob within 15 yd (`sealRange`): Command, else Righteousness; Crusader only on a healthy mob (60%+) with mana (50%+) and no Judgement of the Crusader; Wisdom below 30% mana (`wisdomPct`) without Judgement of Wisdom |
| Judgement | 17 | Seal up, mob in reach, mana above 30% or the seal is Wisdom (its judgement pays mana back) |
| Crusader Strike | 16 | Mob in reach, mana above 15% (`manaFloorPct`) |
| Consecration | 14 | 2+ mobs in melee reach, mana above 40%, area veto clear |
| Hammer of Justice | 42 | Mob in melee below 45% health (`hammerPct`); also the interrupt (40) |
| Divine Protection | 44 | Mob on you below 35% health (`divineProtPct`; pacifies: other attacks wait) |
| Lay on Hands | 50 | Health below 20% in combat (`layOnHandsPct`) |

Below 30% mana only the Wisdom seal is cast and judged; below 15% no seal but Wisdom and no Crusader Strike: the swing and free casts go on, drinking and mana potions are the way back. Out of combat the paladin keeps one aura (Retribution, else Devotion, relevance 13) and one blessing on itself (Might, else Kings, relevance 12, renewed under a minute left). A companion paladin's aura or blessing counts: never cast over it. No Exorcism or Holy Wrath (creature type not visible), no party blessings, no Redemption here.

## 4. Warlock: dots, drains, taps, fear gates

Preset `affliction_warlock` (`combat_dps`, `warlock_affliction`, `ranged_position`, `pet`, ...), band 18–26 yd. Casts with a cast time wait until the warlock stands still.

| Cast | Relevance | When |
|---|---|---|
| Corruption | 16 | Mob above 30% health without it |
| Curse | 15 | One per mob, never over another's: Elements above 60% with 2+ casters in group, else Agony above 40% |
| Shadow Bolt | 10 | Filler |
| Drain Soul | 14 | Mob at or below 25% with fewer than 5 shards (`warlockShardsWanted`), mob still gives experience (grey formula) |
| Drain Life | 18 | Health below 45%, mob above 15% |
| Life Tap | 17 | In combat below 25% mana above 60% health; out of combat below 40% mana above 85% health, before the drink |
| Death Coil | 43 | Mob on you below 40% health |
| Fear | 42 | Mob on you within 12 yd (`warlockFearRange`), below 35% health, no tank in view, no unengaged mob within 30 yd (`warlockLooseFree`) |
| Shadow Ward | 41 | A mob casting a shadow spell at you |
| Demon Armor | 12 | Down at a rest gate (Demon Skin fallback) |
| Create Healthstone | 11 | At rest above 40% mana, none of the tier or better, 2 shards in the bags (one stays for the demon) |

The demon is the shared pet strategy: Voidwalker with a shard, else Imp (`pet_imp` names the Imp-only list). Every spell name, level, cost and range above is pinned against the core's spell data (`TestWarlockSpellData`, and the same `Test*SpellData` per class).
