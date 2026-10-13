package brain

import (
	"context"
	"slices"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/spells"
)

// The holy paladin (issue #80 step 4e, preset holy_paladin): the healer role (heal.go) with the paladin's kit, its blessings on the
// party, one aura, and the paladin's emergency buttons. The rotation is the harvest of mod-playerbots' paladin behaviour (docs:
// PLAYERBOTS-HARVEST, Paladin table; ideas and numbers only, GPL-2.0+), with every spell name, level, cost, range and form mask checked
// against the core's spell data in TestHolyPaladinSpellData. No paladin spell has a stance or form in the core's spell_template (the
// auras are ordinary spells: one paladin aura at a time is the player's own rule), as in paladin.go.
//
//   - Heals. Holy Light (9 ranks, level 1 to 60, 2.5 s) is the big heal; Flash of Light (7 ranks, level 20 to 60, 1.5 s) the quick and
//     dear one, kept for low health (heal.go's choice by the deficit). In this data Flash of Light heals through a script effect (77),
//     not a heal effect, so heal.go counts it as a heal by name (scriptHeal). Dispels: Cleanse (level 42: magic, poison and disease),
//     else Purify (level 8: poison and disease).
//   - Blessings, out of combat at a rest gate, on every member in view and on the paladin: by role. The tank gets Kings, else Might (never
//     Salvation: the tank wants the threat); healers and mana casters Wisdom; melee and hunters Might; Salvation and Kings are the
//     second picks of the others. A member who carries any blessing at all is left alone: the caster is not visible, a companion paladin's
//     blessing counts as the blessing and is never cast over (as for the paladin itself in paladin.go). The paladin's own is renewed when
//     under a minute is left (the server tells it for the player's own auras).
//   - One aura: Concentration, else Devotion. A paladin aura already on the paladin, whoever cast it, is not fought: Concentration from
//     a companion paladin stays.
//   - Emergencies: Lay on Hands on the member (or the paladin) below layOnHandsHolyPct health; Hand of Protection (data name of the
//     Blessing of Protection) on a member who is not the tank and is being beaten in melee; Divine Shield on the paladin itself when it
//     is about to die (in this data it only reduces the damage done and does not pacify, so the healing goes on; see LIVE-CHECKS);
//     Divine Favor when known as a castable spell (in this data its ranks are talent passives; the cast then never happens) and, while
//     it is up, the big heal in place of the quick one. Hammer of Justice is the class's interrupt (class_interrupt in the preset): a stun
//     breaks the cast of a mob that is hurting the party.
//   - paladin_judge (off by default, not in the preset): a seal and Judgement while the mana is good and the tank holds the mob. Judgement
//     reaches 10 yd and the healer stands 20 to 30 yd from the tank, so it fires for a mob that stands near the paladin.

const (
	layOnHandsHolyPct = 15 // Lay on Hands below this health, on a member or on the paladin
	protectPct        = 40 // Hand of Protection on a member below this health, being hit in melee
	protectMelee      = 8.0
	shieldHolyPct     = 15 // Divine Shield below this health on the paladin, with a mob on it or burst damage
	judgeMana         = 60 // paladin_judge: seals and Judgement above this mana
	judgeRange        = 9.0
)

// paladinHeals is what a paladin heals and cleanses with.
var paladinHeals = HealKit{
	Direct: []string{"Holy Light", "Flash of Light"},
	Fast:   []string{"Flash of Light"},
	NoFast: func(c *Ctx) bool { return HasAura(&c.Self, "Divine Favor") },
	Dispels: []DispelSpell{
		{Spell: "Cleanse", Types: []uint8{dispelMagic, dispelPoison, dispelDisease}},
		{Spell: "Purify", Types: []uint8{dispelPoison, dispelDisease}},
	},
}

// scriptHealNames are the heals whose amount is the base points of a script effect (SPELL_EFFECT_SCRIPT_EFFECT, 77) in the data.
var scriptHealNames = []string{"Flash of Light"}

// scriptHeal says the spell is one of the heals that heal through a script effect.
func scriptHeal(m spells.Meta) bool { return slices.Contains(scriptHealNames, spells.Name(m.ID)) }

// isHealSpell says the spell heals: a heal effect or an aura that heals, or a script heal.
func isHealSpell(m spells.Meta) bool {
	if m.Flags.Has(spells.FlagHeal) {
		return true
	}
	if !scriptHeal(m) {
		return false
	}
	return slices.ContainsFunc(m.Effects[:], func(e spells.Effect) bool { return e.Effect == 77 })
}

// ---- blessings ----

// blessingPrefs are the blessings a member gets, best first, by its role and class.
func blessingPrefs(c *Ctx, u *objects.Unit) []string {
	const (
		wisdom, might, kings, salvation = "Blessing of Wisdom", "Blessing of Might", "Blessing of Kings", "Blessing of Salvation"
	)
	if t := ValTank.Get(c); t != nil && t.GUID == u.GUID {
		return []string{kings, might} // never Salvation: the tank wants the threat
	}
	switch u.Class {
	case ClassWarrior, ClassRogue, ClassHunter:
		return []string{might, kings, salvation}
	case ClassPriest, ClassPaladin, ClassShaman, ClassDruid, ClassMage, ClassWarlock:
		return []string{wisdom, kings, salvation}
	}
	return []string{kings, salvation}
}

// carriedBlessing is the first blessing of a paladin that the unit carries (Blessing of ... or Greater Blessing of ...), "" for none.
func carriedBlessing(u *objects.Unit) string {
	if n := auraWithPrefix(u, "Blessing of "); n != "" {
		return n
	}
	return auraWithPrefix(u, "Greater Blessing of ")
}

const blessingHold = 6 * time.Second // after a blessing went out, its aura takes a moment to show

// blessingPlan is the blessing to cast now: the member and the spell. The paladin itself first, then the members in view, in reach and sight.
func blessingPlan(c *Ctx) (packet.GUID, uint32) {
	if !buffsAllowed(c) {
		return 0, 0
	}
	for _, u := range healMembers(c) {
		self := u.GUID == c.Self.GUID
		if !self && (float64(u.Distance) > buffReach || !sightOK(c, u)) {
			continue
		}
		if _, held := heldKey(c, "bless:"+guidStr(u.GUID)); held {
			continue
		}
		// An aura row has no caster, so the paladin cannot tell its own blessing from a companion's recast of the same one: it renews the
		// one on itself when the server says under a minute is left, and the worst case is one blessing cast over a fresh one.
		have := carriedBlessing(u)
		if have != "" && !(self && auraMissing(u, have) && knows(c, have)) {
			continue // a blessing is there (a companion's counts); the paladin's own is renewed when under a minute is left
		}
		if have != "" {
			id, _ := c.Spells.Highest(have)
			return u.GUID, id
		}
		for _, n := range blessingPrefs(c, u) {
			if id, ok := c.Spells.Highest(n); ok {
				return u.GUID, id
			}
		}
	}
	return 0, 0
}

// ---- the aura ----

var holyAuras = []string{"Concentration Aura", "Devotion Aura"}

// anyPaladinAura names every aura of a paladin: one of them on the player, from whoever, is the aura (the companions' are not fought).
var anyPaladinAura = []string{"Concentration Aura", "Devotion Aura", "Retribution Aura", "Sanctity Aura", "Fire Resistance Aura",
	"Frost Resistance Aura", "Shadow Resistance Aura"}

// holyAuraWanted is the aura to cast, "" when the player carries one or the spellbook has none.
func holyAuraWanted(c *Ctx) string {
	if HasAura(&c.Self, anyPaladinAura...) {
		return ""
	}
	for _, n := range holyAuras {
		if knows(c, n) {
			return n
		}
	}
	return ""
}

// ---- the emergencies ----

// memberPct is a member's health in percent, 100 when it has no maximum.
func memberPct(c *Ctx, u *objects.Unit) float64 {
	hp, max := memberHealth(c, u)
	if max <= 0 {
		return 100
	}
	return hp / max * 100
}

// meleeOn counts the hostile mobs in combat that have the unit as their target and stand within protectMelee of it.
func meleeOn(c *Ctx, u *objects.Unit) int {
	n := 0
	for i := range c.Units {
		m := &c.Units[i]
		if hostileMob(m) && m.InCombat && m.Target == u.GUID && dist2(unitPos(m), unitPos(u)) <= protectMelee {
			n++
		}
	}
	return n
}

// forbearance says the unit cannot take another Divine Shield or Hand of Protection.
func forbearance(u *objects.Unit) bool { return HasAura(u, "Forbearance") }

// layOnHandsTarget is the member (or the paladin) to cast Lay on Hands on: the one with the least health below layOnHandsHolyPct, in
// reach and in sight, in a fight.
func layOnHandsTarget(c *Ctx) *objects.Unit {
	if !healCombat(c) {
		return nil
	}
	var best *objects.Unit
	bestPct := float64(layOnHandsHolyPct)
	for _, u := range healMembers(c) {
		if p := memberPct(c, u); p < bestPct && inHealReach(u) && sightOK(c, u) {
			best, bestPct = u, p
		}
	}
	return best
}

// protectTarget is the member to cast Hand of Protection on: not the tank and not the paladin, below protectPct health, with a mob
// hitting it in melee and either two on it or a quarter of its health lost within three seconds; without Forbearance, in reach and sight.
func protectTarget(c *Ctx) *objects.Unit {
	if !healCombat(c) {
		return nil
	}
	tank := ValTank.Get(c)
	var best *objects.Unit
	bestPct := float64(protectPct)
	for _, u := range healMembers(c) {
		if u.GUID == c.Self.GUID || (tank != nil && tank.GUID == u.GUID) || forbearance(u) || HasAura(u, "Hand of Protection", "Divine Shield") {
			continue
		}
		p := memberPct(c, u)
		if p >= bestPct || float64(u.Distance) > buffReach || !sightOK(c, u) {
			continue
		}
		n := meleeOn(c, u)
		if n == 0 {
			continue
		}
		_, max := memberHealth(c, u)
		burst := max > 0 && float64(c.mem.heal.lost(c.Now, u.GUID, u.Health))/max*100 >= burstLossPct
		if n >= 2 || burst {
			best, bestPct = u, p
		}
	}
	return best
}

// shieldWanted says the paladin is about to die: below shieldHolyPct health, in a fight, with a mob on it or a quarter of its health lost
// within three seconds, and no Forbearance.
func shieldWanted(c *Ctx) bool {
	if !c.SelfOK || !healCombat(c) || healthPct(c) >= shieldHolyPct || forbearance(&c.Self) || HasAura(&c.Self, "Divine Shield") {
		return false
	}
	return ValMobOnMe.Get(c) != nil || float64(c.mem.heal.lost(c.Now, c.Self.GUID, c.Self.Health)) >= float64(burstLossPct)/100*float64(c.Self.MaxHealth)
}

// divineFavorSpell is the Divine Favor the spellbook can cast: a rank that is not a talent passive (the ranks of this data are).
func divineFavorSpell(c *Ctx) (uint32, bool) {
	ranks := c.Spells.Ranks("Divine Favor")
	for i := len(ranks) - 1; i >= 0; i-- {
		if m, ok := spells.Lookup(ranks[i]); ok && !m.Flags.Has(spells.FlagPassive) {
			return ranks[i], true
		}
	}
	return 0, false
}

// favorWanted says the heal of the tick is an emergency and Divine Favor can be cast before it.
func favorWanted(c *Ctx) bool {
	if _, ok := divineFavorSpell(c); !ok || HasAura(&c.Self, "Divine Favor") {
		return false
	}
	p := ValHealPlan.Get(c)
	return p.Target != nil && (p.Pct < healEmergencyPct || (p.Tank && p.Pct < healTankEmergPct))
}

func holyPaladin() *Strategy {
	onHoly := func(c *Ctx) bool { return onPaladin(c) }
	return &Strategy{
		Name: "paladin_holy",
		Actions: []*Action{
			{Name: "holy_aura", Resolve: spellByName(holyAuraWanted), Possible: func(c *Ctx) bool { return onHoly(c) && holyAuraWanted(c) != "" }},
			{Name: "bless_member",
				Resolve:  func(c *Ctx) (uint32, bool) { g, id := blessingPlan(c); return id, g != 0 },
				Target:   func(c *Ctx) packet.GUID { g, _ := blessingPlan(c); return g },
				Possible: func(c *Ctx) bool { g, _ := blessingPlan(c); return onHoly(c) && g != 0 },
				Do: func(ctx context.Context, c *Ctx) error {
					g, id := blessingPlan(c)
					if g == 0 {
						return nil
					}
					setKey(c, "bless:"+guidStr(g), blessingHold, "sent")
					return c.cast(ctx, id, g)
				}},
			{Name: "lay_on_hands", Resolve: spellByName(func(*Ctx) string { return "Lay on Hands" }),
				Target:   func(c *Ctx) packet.GUID { return targetGUID(layOnHandsTarget(c)) },
				Possible: func(c *Ctx) bool { return onHoly(c) && layOnHandsTarget(c) != nil }},
			{Name: "hand_of_protection", Resolve: spellByName(func(*Ctx) string { return "Hand of Protection" }),
				Target:   func(c *Ctx) packet.GUID { return targetGUID(protectTarget(c)) },
				Possible: func(c *Ctx) bool { return onHoly(c) && protectTarget(c) != nil }},
			{Name: "divine_shield", Resolve: spellByName(func(*Ctx) string { return "Divine Shield" }),
				Possible: func(c *Ctx) bool { return onHoly(c) && shieldWanted(c) }},
			{Name: "divine_favor", Resolve: divineFavorSpell, Possible: func(c *Ctx) bool { return onHoly(c) && favorWanted(c) }},
		},
		Rules: []Rule{
			{Trigger: Trigger{Name: "holy_aura_missing", Active: func(c *Ctx) bool {
				return onHoly(c) && holyAuraWanted(c) != "" && (inCombat(c) || restNow(c))
			}}, Do: []Next{{"holy_aura", RelNormal + 3}}},
			{Trigger: Trigger{Name: "blessing_missing_party", Active: func(c *Ctx) bool { g, _ := blessingPlan(c); return onHoly(c) && g != 0 }},
				Do: []Next{{"bless_member", RelNormal + 2}}},
			{Trigger: Trigger{Name: "member_critical", Active: func(c *Ctx) bool { return onHoly(c) && layOnHandsTarget(c) != nil }},
				Do: []Next{{"lay_on_hands", relEmergency + 2}}},
			{Trigger: Trigger{Name: "member_beaten_in_melee", Active: func(c *Ctx) bool { return onHoly(c) && protectTarget(c) != nil }},
				Do: []Next{{"hand_of_protection", relEmergency + 1}}},
			{Trigger: Trigger{Name: "paladin_about_to_die", Active: func(c *Ctx) bool { return onHoly(c) && shieldWanted(c) }},
				Do: []Next{{"divine_shield", relEmergency}}},
			{Trigger: Trigger{Name: "emergency_heal_coming", Active: func(c *Ctx) bool { return onHoly(c) && favorWanted(c) }},
				Do: []Next{{"divine_favor", RelInterrupt + 2}}},
		},
	}
}

// ---- paladin_judge ----

// judgeSeal is the seal of paladin_judge: Wisdom when the mana is not full (its judgement gives mana back), else Light (its judgement
// heals whoever hits the mob).
func judgeSeal(c *Ctx) string {
	switch {
	case knows(c, "Seal of Wisdom") && (PowerPct(c) < 80 || !knows(c, "Seal of Light")):
		return "Seal of Wisdom"
	case knows(c, "Seal of Light"):
		return "Seal of Light"
	}
	return ""
}

// judgeOK says the paladin may spend mana on a seal and Judgement: the mana is good, nobody needs a heal, and the tank holds the mob that
// stands within Judgement's reach.
func judgeOK(c *Ctx) bool {
	m, tank := fightMob(c), ValTank.Get(c)
	if !onPaladin(c) || m == nil || tank == nil || m.Target != tank.GUID || m.Distance < 0 || float64(m.Distance) > judgeRange {
		return false
	}
	return PowerPct(c) >= judgeMana && ValHealPlan.Get(c).Target == nil
}

func paladinJudge() *Strategy {
	return &Strategy{
		Name: "paladin_judge",
		Actions: []*Action{
			{Name: "judge_seal", Resolve: spellByName(judgeSeal), Possible: func(c *Ctx) bool { return judgeOK(c) && !hasSeal(c) && judgeSeal(c) != "" }},
			{Name: "judge", Spell: "Judgement", Harmful: true, Threat: ThreatSingle, Target: fightTarget, Pre: []string{"face_target"},
				Possible: func(c *Ctx) bool { return judgeOK(c) && hasSeal(c) }},
		},
		Rules: []Rule{
			{Trigger: Trigger{Name: "judge_seal_down", Active: func(c *Ctx) bool { return judgeOK(c) && !hasSeal(c) }}, Do: []Next{{"judge_seal", RelNormal - 3}}},
			{Trigger: Trigger{Name: "judge_ready", Active: func(c *Ctx) bool { return judgeOK(c) && hasSeal(c) }}, Do: []Next{{"judge", RelNormal - 2}}},
		},
	}
}

func init() {
	strategyBuilders["paladin_holy"] = holyPaladin
	strategyBuilders["paladin_judge"] = paladinJudge
	presets["holy_paladin"] = []string{"role_healer", "paladin_holy", "rez", "healer_position", "avoid_ground", "class_interrupt", "combat_consumables",
		"rest_eat", "rest_drink"}
}
