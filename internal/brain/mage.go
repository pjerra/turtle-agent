package brain

import (
	"context"
	"errors"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
)

// The frost mage, the first class of the brain (issue #80 step 2). Numbers come from the harvest of
// mod-playerbots' mage behaviour (docs: PLAYERBOTS-HARVEST, Mage table): Frostbolt is the filler, Frost Nova and
// Fire Blast answer a mob in melee, Cone of Cold a cluster in front, and out of combat the mage buffs, drinks and
// evocates. Spells are named; the brain casts the highest rank the spellbook holds.

// Thresholds, in percent of the mana pool (mod-playerbots' ManaLow 15 / Medium 40 / High 65, rounded up).
// drinkSettle is how long the brain waits for the Drink aura after it used a drink.
const drinkSettle = 1500 * time.Millisecond

const (
	manaCriticalPct = 20 // Evocation
	manaDrinkPct    = 65 // sit and drink below this
)

var (
	armorAuras  = []string{"Frost Armor", "Ice Armor", "Mage Armor"}
	intellectAu = []string{"Arcane Intellect", "Arcane Brilliance"}
)

// drinkItem is a drink the mage may have in the bags, with the character level its use needs (item_template
// required_level in the core's world database).
type drinkItem struct {
	entry uint32
	level uint8
}

// conjuredDrinks are the waters a mage conjures, best first; vendorDrinks are the ones every mana class buys, best first (Moonberry
// Juice and Sweet Nectar restore mana only, the others also health). Each group runs from the highest level down; the entries
// and levels are the core's.
var (
	conjuredDrinks = []drinkItem{{8079, 55}, {8078, 45}, {8077, 35}, {3772, 25}, {2136, 15}, {2288, 5}, {5350, 1}} // Conjured Crystal ... Conjured Water
	vendorDrinks   = []drinkItem{{8766, 45}, {1645, 35}, {1708, 25}, {1205, 15}, {1179, 5}, {159, 1}}              // Morning Glory Dew, Moonberry Juice, Sweet Nectar, Melon Juice, Ice Cold Milk, Refreshing Spring Water
)

// drinksFor is the drink table of a class, best first: a mage's own waters then the vendor drinks, the vendor drinks for the other
// mana classes, none for a class that plays on rage or energy.
func drinksFor(ci ClassInfo) []drinkItem {
	var out []drinkItem
	if ci.ConjuredDrinks {
		out = append(out, conjuredDrinks...)
	}
	if ci.Drinks {
		out = append(out, vendorDrinks...)
	}
	return out
}

// drinkUseHold is how long a drink that was just used is not used again: the Drink aura needs the server's answer, and
// a refusal the brain cannot see (the item cooldown, a stack that is gone) must not loop.
const drinkUseHold = 5 * time.Second

// drinkEntry is the best drink of the leader's class in the bags that its level allows, 0 when there is none. Unless anyOK, a drink
// used within drinkUseHold is passed over for the next one.
func drinkEntry(c *Ctx, anyOK bool) uint32 {
	have := map[uint32]bool{}
	for _, it := range c.G.Items() {
		have[it.Entry] = true
	}
	for _, d := range drinksFor(c.Class()) {
		if !have[d.entry] || uint32(d.level) > c.Self.Level {
			continue
		}
		if !anyOK && c.mem.entryHeld(d.entry, c.Now) {
			continue
		}
		return d.entry
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
			{Name: "frost_nova", Spell: "Frost Nova", Threat: ThreatAoE, // it hits everything within 10 yd
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
				Useful:   func(c *Ctx) bool { return drinkEntry(c, true) == 0 }},
			{Name: "drink", Pre: []string{"conjure_water"}, Settle: drinkSettle,
				Possible: func(c *Ctx) bool { return !inCombat(c) && drinkEntry(c, false) != 0 },
				Useful:   func(c *Ctx) bool { return !HasAura(&c.Self, "Drink") },
				Do: func(ctx context.Context, c *Ctx) error {
					e := drinkEntry(c, false)
					if e == 0 {
						return errors.New("no drink in the bags")
					}
					if err := c.G.UseItem(ctx, e); err != nil {
						return err
					}
					c.mem.holdEntry(e, c.Now.Add(drinkUseHold))
					return nil
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
