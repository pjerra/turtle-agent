package brain

import (
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// The retribution paladin (issue #80 step 4b, preset ret_paladin): a melee damage dealer that stands behind its mob (melee_position),
// keeps a seal up, judges it on cooldown and strikes with Crusader Strike. The rotation is the ideas of mod-playerbots' paladin behaviour
// (GPL-2.0+; ideas and numbers only), the spells are checked against the core's spell data in TestPaladinSpellData. No paladin spell has
// a stance or form in the core's spell_template (the auras are ordinary spells: one paladin aura at a time is the player's own rule).
//
//   - Seals. One seal is kept up while a mob is being fought: Seal of Command, else Righteousness. Judgement (on its 10 s cooldown)
//     spends it, and the seal is cast again. The TortoiseBots companion paladins judge too and their debuffs must not be fought: Seal of
//     the Crusader is chosen only when the mob carries no Judgement of the Crusader yet and is healthy enough to pay for the extra
//     seal, and Seal of Wisdom (the mana fallback, below wisdomPct of the mana) only when it carries no Judgement of Wisdom. A seal
//     already up is never swapped: a swap costs a global cooldown and the seal's mana.
//   - Out of combat: one aura (Retribution, else Devotion) and one blessing on the paladin itself (Might, Kings when it has no Might).
//     A blessing from a companion paladin counts as the blessing (the buff's caster is not visible), so the brain never casts over
//     it; party blessings come later. The aura is kept up in combat too. Drinking is rest_drink.
//   - Hammer of Justice is the class's interrupt (class_interrupt, a stun breaks the cast) and an emergency stun for a mob in melee with
//     the paladin below hammerPct health; Lay on Hands at critical health; Divine Protection when hurt with a mob on the paladin
//     (it pacifies the paladin for its length: the other attacks wait). Consecration only with two or more mobs in melee reach and when
//     the damage dealer's area veto allows it.
//
// Mana floor: Judgement, the seals and Crusader Strike all cost mana. Below wisdomPct (30%) Seal of Wisdom is the seal (unless the mob
// carries Judgement of Wisdom already), and only that seal is judged, since its judgement gives mana back; below manaFloorPct (15%) no seal
// but Wisdom is cast and Crusader Strike waits; the swing and the free abilities go on, and rest_drink / mana potions are the way back.
//
// Not in this step: Exorcism and Holy Wrath (undead and demons only; the creature type is not visible to a 1.12 client), the
// blessings of the party, Redemption, and the tank's side of the paladin (Righteous Fury, Hand of Reckoning).

const (
	sealCommand       = "Seal of Command"
	sealRighteousness = "Seal of Righteousness"
	sealCrusader      = "Seal of the Crusader"
	sealWisdom        = "Seal of Wisdom"

	wisdomPct          = 30 // Seal of Wisdom below this mana
	manaFloorPct       = 15 // below this mana no seal but Wisdom is cast, and no Crusader Strike; below wisdomPct no Judgement of another seal
	crusaderMobPct     = 60 // Seal of the Crusader on a mob with at least this health ...
	crusaderManaPct    = 50 // ... and when the paladin has at least this much mana
	sealRange          = 15 // a seal is cast when the fight's mob is this near (it lasts 30 s)
	consecrationManaPc = 40
	layOnHandsPct      = 20 // Lay on Hands below this health, in combat
	divineProtPct      = 35 // Divine Protection below this health with a mob on the paladin
	hammerPct          = 45 // Hammer of Justice on a mob in melee below this health
)

var (
	paladinAuras     = []string{"Retribution Aura", "Devotion Aura"}
	paladinBlessings = []string{"Blessing of Might", "Blessing of Kings"}
)

func onPaladin(c *Ctx) bool { return onPower(c, powerMana) && c.Self.Class == ClassPaladin }

// currentSeal is the seal the paladin carries, "" when it has none.
func currentSeal(c *Ctx) string { return auraWithPrefix(&c.Self, "Seal of ") }

// wantSeal is the seal to cast when none is up, "" when the spellbook has none.
func wantSeal(c *Ctx) string {
	mob := fightMob(c)
	want := ""
	switch {
	case knows(c, sealWisdom) && PowerPct(c) < wisdomPct && (mob == nil || !HasAura(mob, "Judgement of Wisdom")):
		want = sealWisdom
	case knows(c, sealCrusader) && mob != nil && !HasAura(mob, "Judgement of the Crusader") && mobHealthPct(mob) >= crusaderMobPct &&
		PowerPct(c) >= crusaderManaPct:
		want = sealCrusader
	case knows(c, sealCommand):
		want = sealCommand
	case knows(c, sealRighteousness):
		want = sealRighteousness
	}
	if want != sealWisdom && PowerPct(c) < manaFloorPct {
		return "" // below the floor only the Wisdom seal is worth its mana: its judgement gives mana back
	}
	return want
}

// wantAura is the aura to cast, "" when one is up or the spellbook has none: Retribution if it is known, else Devotion. The aura of a
// companion paladin counts for the other kind only: Retribution is cast over a companion's Devotion, since they stack.
func wantAura(c *Ctx) string {
	switch {
	case knows(c, paladinAuras[0]):
		if !HasAura(&c.Self, paladinAuras[0]) {
			return paladinAuras[0]
		}
	case knows(c, paladinAuras[1]):
		if !HasAura(&c.Self, paladinAuras...) {
			return paladinAuras[1]
		}
	}
	return ""
}

// wantBlessing is the blessing to cast on the paladin itself, "" for none: Might (Kings if there is no Might in the spellbook) when
// neither is on it, the one it carries again when that is about to end.
func wantBlessing(c *Ctx) string {
	if have := seenAura(&c.Self, paladinBlessings...); have != "" {
		if auraMissing(&c.Self, have) && knows(c, have) { // present, but under a minute left
			return have
		}
		return ""
	}
	for _, n := range paladinBlessings {
		if knows(c, n) {
			return n
		}
	}
	return ""
}

func hasSeal(c *Ctx) bool { return currentSeal(c) != "" }

// pacified says the paladin cannot attack for now (Divine Protection and Divine Shield pacify it).
func pacified(c *Ctx) bool { return HasAura(&c.Self, "Divine Protection", "Divine Shield") }

func paladinRetribution() *Strategy {
	meleeMob := func(c *Ctx) *objects.Unit { return ValMeleeMob.Get(c) }
	return &Strategy{
		Name: "paladin_retribution",
		Mult: []Multiplier{{Name: "pacified", F: func(c *Ctx, a *Action, rel float32) float32 {
			if a.Harmful && pacified(c) {
				return 0
			}
			return 1
		}}},
		Actions: []*Action{
			swingAction("auto_attack", fightMob),
			{Name: "seal", Resolve: spellByName(wantSeal),
				Possible: func(c *Ctx) bool { return onPaladin(c) && !hasSeal(c) && wantSeal(c) != "" }},
			// Judgement spends the seal and costs mana: below the Wisdom threshold only a Seal of Wisdom is judged (its judgement gives mana back).
			meleeAction("judgement", "Judgement", ThreatSingle, nil, func(c *Ctx) bool {
				return hasSeal(c) && mobInReach(c) && (PowerPct(c) >= wisdomPct || currentSeal(c) == sealWisdom)
			}),
			meleeAction("crusader_strike", "Crusader Strike", ThreatSingle, nil, func(c *Ctx) bool { return mobInReach(c) && PowerPct(c) >= manaFloorPct }),
			{Name: "consecration", Spell: "Consecration", Harmful: true, Threat: ThreatAoE,
				Possible: func(c *Ctx) bool { return ValMeleeMobs.Get(c) >= 2 && PowerPct(c) >= consecrationManaPc }},
			{Name: "hammer_of_justice", Spell: "Hammer of Justice", Harmful: true, Threat: ThreatNone, Pre: []string{"face_target"},
				Target: func(c *Ctx) packet.GUID {
					if m := meleeMob(c); m != nil {
						return m.GUID
					}
					return 0
				},
				Possible: func(c *Ctx) bool { return meleeMob(c) != nil },
				Useful:   func(c *Ctx) bool { return !HasAura(meleeMob(c), "Hammer of Justice") }},
			selfAction("lay_on_hands", "Lay on Hands", nil, nil, nil),
			selfAction("divine_protection", "Divine Protection", nil, nil, func(c *Ctx) bool { return !pacified(c) }),
			{Name: "paladin_aura", Resolve: spellByName(wantAura), Possible: func(c *Ctx) bool { return onPaladin(c) && wantAura(c) != "" }},
			{Name: "blessing", Resolve: spellByName(wantBlessing), Possible: func(c *Ctx) bool { return onPaladin(c) && wantBlessing(c) != "" }},
		},
		Rules: []Rule{
			{Trigger: Trigger{Name: "mob_in_reach_paladin", Active: mobInReach}, Do: []Next{
				{"auto_attack", RelNormal + 9}, {"judgement", RelNormal + 7}, {"crusader_strike", RelNormal + 6}}},
			{Trigger: Trigger{Name: "seal_down", Active: func(c *Ctx) bool {
				m := fightMob(c)
				return onPaladin(c) && !hasSeal(c) && m != nil && m.Distance >= 0 && m.Distance <= sealRange
			}}, Do: []Next{{"seal", RelNormal + 8}}},
			{Trigger: Trigger{Name: "pack_in_reach_paladin", Active: func(c *Ctx) bool { return mobInReach(c) && ValMeleeMobs.Get(c) >= 2 }},
				Do: []Next{{"consecration", RelNormal + 4}}},
			{Trigger: Trigger{Name: "hurt_in_melee_paladin", Active: func(c *Ctx) bool {
				return onPaladin(c) && meleeMob(c) != nil && healthPct(c) < hammerPct
			}}, Do: []Next{{"hammer_of_justice", relEmergency - 1}}},
			{Trigger: Trigger{Name: "critical_mob_on_paladin", Active: func(c *Ctx) bool {
				return onPaladin(c) && ValMobOnMe.Get(c) != nil && healthPct(c) < divineProtPct
			}}, Do: []Next{{"divine_protection", relEmergency + 1}}},
			{Trigger: Trigger{Name: "health_critical_paladin", Active: func(c *Ctx) bool {
				return onPaladin(c) && inCombat(c) && healthPct(c) < layOnHandsPct
			}}, Do: []Next{{"lay_on_hands", RelDispel}}},
			{Trigger: Trigger{Name: "aura_missing", Active: func(c *Ctx) bool { return onPaladin(c) && wantAura(c) != "" && (inCombat(c) || restNow(c)) }},
				Do: []Next{{"paladin_aura", RelNormal + 3}}},
			{Trigger: Trigger{Name: "blessing_missing_ooc", Active: func(c *Ctx) bool { return onPaladin(c) && restNow(c) && wantBlessing(c) != "" }},
				Do: []Next{{"blessing", RelNormal + 2}}},
		},
	}
}

func init() {
	strategyBuilders["paladin_retribution"] = paladinRetribution
	presets["ret_paladin"] = []string{"combat_dps", "paladin_retribution", "melee_position", "avoid_ground", "class_interrupt", "combat_consumables",
		"rest_drink", "rest_eat"}
}
