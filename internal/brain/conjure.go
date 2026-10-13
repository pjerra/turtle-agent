package brain

// Conjuring picks the rank by what it makes. The highest rank of Conjure Water or Conjure Food may make an item the leader's level
// cannot use (live run 3: a level 45 mage with every spell learned conjured Conjured Crystal Water, level 55, with Conjure Water
// rank 7 and could never drink it). So the brain casts the highest known rank whose created item the leader may use.

// conjureRank is one rank of a conjure spell and the item it creates (the spell's first effect, SPELL_EFFECT_CREATE_ITEM).
type conjureRank struct {
	spell, item uint32
}

// conjureWater and conjureFood list the player ranks, lowest first. The ids are the rank 1 to 7 spells of spells/meta.tsv.gz that
// create items (effect 24); the NPC copies of the same names (effect 36) are left out. The items are the entries of conjuredDrinks
// and foodItems, whose levels decide; the order of the ranks is the order of the items.
var (
	conjureWater = []conjureRank{{5504, 5350}, {5505, 2288}, {5506, 2136}, {6127, 3772}, {10138, 8077}, {10139, 8078}, {10140, 8079}}
	conjureFood  = []conjureRank{{587, 5349}, {597, 1113}, {990, 1114}, {6129, 1487}, {10144, 8075}, {10145, 8076}, {28612, 22895}}
)

// conjureSpell is the highest known rank of the spell called name in ranks whose created item (levelOf) the leader's level allows.
// ok is false when none is known or none makes a usable item; a known spell that is not in ranks (a spell the table does not know) is
// never picked.
func conjureSpell(c *Ctx, ranks []conjureRank, levelOf func(item uint32) (uint8, bool)) (uint32, bool) {
	for i := len(ranks) - 1; i >= 0; i-- {
		r := ranks[i]
		if !c.Spells.Known(r.spell) {
			continue
		}
		if lvl, ok := levelOf(r.item); ok && uint32(lvl) <= c.Self.Level {
			return r.spell, true
		}
	}
	return 0, false
}

func drinkLevel(item uint32) (uint8, bool) {
	for _, d := range conjuredDrinks {
		if d.entry == item {
			return d.level, true
		}
	}
	return 0, false
}

func foodLevel(item uint32) (uint8, bool) {
	for _, f := range foodItems {
		if f.entry == item {
			return f.level, true
		}
	}
	return 0, false
}

// conjureWaterSpell and conjureFoodSpell resolve the conjure_water and conjure_food actions.
func conjureWaterSpell(c *Ctx) (uint32, bool) { return conjureSpell(c, conjureWater, drinkLevel) }
func conjureFoodSpell(c *Ctx) (uint32, bool)  { return conjureSpell(c, conjureFood, foodLevel) }
