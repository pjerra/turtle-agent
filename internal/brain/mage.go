package brain

import (
	"context"
	"errors"

	"github.com/pjerra/turtle-agent/internal/packet"
)

// The frost mage, the first class of the brain (issue #80 step 2). Numbers come from the harvest of
// mod-playerbots' mage behaviour (docs: PLAYERBOTS-HARVEST, Mage table): Frostbolt is the filler, Frost Nova and
// Fire Blast answer a mob in melee, Cone of Cold a cluster in front, and out of combat the mage buffs, drinks and
// evocates. Spells are named; the brain casts the highest rank the spellbook holds.

// Thresholds, in percent of the mana pool (mod-playerbots' ManaLow 15 / Medium 40 / High 65, rounded up).
const (
	manaCriticalPct = 20 // Evocation
	manaDrinkPct    = 65 // sit and drink below this
)

var (
	armorAuras  = []string{"Frost Armor", "Ice Armor", "Mage Armor"}
	intellectAu = []string{"Arcane Intellect", "Arcane Brilliance"}
)

// drinkItems are the water a mage drinks: the conjured waters of every rank and the vendor drinks that restore only
// mana or both. They are item entries of the 1.12 item table.
var drinkItems = []uint32{
	8079, 8078, 8077, 3772, 2136, 2288, 5350, // conjured water, best rank first
	1645, 1708, 8766, 1205, 1179, 159, // Moonberry Juice, Sweet Nectar, Morning Glory Dew, Melon Juice, Ice Cold Milk, Refreshing Spring Water
}

// drinkEntry is the best drink in the bags, 0 when there is none.
func drinkEntry(c *Ctx) uint32 {
	have := map[uint32]bool{}
	for _, it := range c.G.Items() {
		have[it.Entry] = true
	}
	for _, e := range drinkItems {
		if have[e] {
			return e
		}
	}
	return 0
}

func meleeMobGUID(c *Ctx) packet.GUID {
	if u := ValMeleeMob.Get(c); u != nil {
		return u.GUID
	}
	return 0
}

func onSelf(c *Ctx) bool { return c.SelfOK && c.Self.PowerType == 0 }

func inCombat(c *Ctx) bool { return c.Self.InCombat }

// mageFrost is the combat rotation of a frost mage in a party.
func mageFrost() *Strategy {
	return &Strategy{
		Name: "mage_frost",
		Actions: []*Action{
			{Name: "frostbolt", Spell: "Frostbolt", Harmful: true, Threat: ThreatSingle, Target: fightTarget, Pre: []string{"face_target"}},
			{Name: "fire_blast", Spell: "Fire Blast", Harmful: true, Threat: ThreatSingle, Target: meleeMobGUID, Pre: []string{"face_target"}},
			{Name: "frost_nova", Spell: "Frost Nova",
				Possible: func(c *Ctx) bool { return ValMeleeMob.Get(c) != nil },
				Useful:   func(c *Ctx) bool { return !HasAura(ValMeleeMob.Get(c), "Frost Nova") }},
			{Name: "cone_of_cold", Spell: "Cone of Cold", Harmful: true, Threat: ThreatAoE,
				Possible: func(c *Ctx) bool { return ValConeMobs.Get(c) >= 2 }},
		},
		Rules: []Rule{
			{Trigger: Trigger{Name: "fight_target", Active: func(c *Ctx) bool { return ValFight.Get(c).Mob != nil }},
				Do: []Next{{"frostbolt", RelNormal}}},
			{Trigger: Trigger{Name: "mob_in_melee", Active: func(c *Ctx) bool { return ValMeleeMob.Get(c) != nil }},
				Do: []Next{{"frost_nova", RelHigh}, {"fire_blast", RelNormal + 2}}},
			{Trigger: Trigger{Name: "mobs_in_cone", Active: func(c *Ctx) bool { return ValConeMobs.Get(c) >= 2 }},
				Do: []Next{{"cone_of_cold", RelNormal + 5}}},
		},
	}
}

// mageRest is what a mage does out of combat: keep armor and Arcane Intellect up, drink, evocate, conjure water.
// It runs only while the brain may do upkeep (Ctx.RestOK) and the player is out of combat with no mob to fight.
func mageRest() *Strategy {
	ooc := func(c *Ctx) bool {
		return c.RestOK && onSelf(c) && !inCombat(c) && ValFight.Get(c).Mob == nil
	}
	return &Strategy{
		Name: "mage_rest",
		Actions: []*Action{
			{Name: "arcane_intellect", Spell: "Arcane Intellect", Useful: func(c *Ctx) bool { return auraMissing(&c.Self, intellectAu...) }},
			{Name: "ice_armor", Spell: "Ice Armor", Alt: []string{"frost_armor"}, Useful: func(c *Ctx) bool { return auraMissing(&c.Self, armorAuras...) }},
			{Name: "frost_armor", Spell: "Frost Armor", Useful: func(c *Ctx) bool { return auraMissing(&c.Self, armorAuras...) }},
			{Name: "evocation", Spell: "Evocation", Alt: []string{"drink"}, Possible: func(c *Ctx) bool { return !inCombat(c) }},
			{Name: "conjure_water", Spell: "Conjure Water",
				Possible: func(c *Ctx) bool { return !inCombat(c) },
				Useful:   func(c *Ctx) bool { return drinkEntry(c) == 0 }},
			{Name: "drink", Pre: []string{"conjure_water"},
				Possible: func(c *Ctx) bool { return !inCombat(c) && drinkEntry(c) != 0 },
				Useful:   func(c *Ctx) bool { return !HasAura(&c.Self, "Drink") },
				Do: func(ctx context.Context, c *Ctx) error {
					e := drinkEntry(c)
					if e == 0 {
						return errors.New("no drink in the bags")
					}
					return c.G.UseItem(ctx, e)
				}},
		},
		Rules: []Rule{
			{Trigger: Trigger{Name: "armor_missing", Active: func(c *Ctx) bool { return ooc(c) && auraMissing(&c.Self, armorAuras...) }},
				Do: []Next{{"ice_armor", RelNormal + 2}}},
			{Trigger: Trigger{Name: "intellect_missing", Active: func(c *Ctx) bool { return ooc(c) && auraMissing(&c.Self, intellectAu...) }},
				Do: []Next{{"arcane_intellect", RelNormal + 2}}},
			{Trigger: Trigger{Name: "mana_critical", Active: func(c *Ctx) bool { return ooc(c) && PowerPct(c) < manaCriticalPct }},
				Do: []Next{{"evocation", RelNormal + 4}}},
			{Trigger: Trigger{Name: "mana_low", Active: func(c *Ctx) bool { return ooc(c) && PowerPct(c) < manaDrinkPct }},
				Do: []Next{{"drink", RelNormal}}},
		},
	}
}
