package brain

import (
	"context"
	"errors"
	"time"
)

// Eating (issue #80 step 3a). Out of combat, at a rest gate, the leader eats when its health is low, for any class: sit and
// use the best food in the bags that its level allows, the way mod-playerbots' UseFoodStrategy does (ideas only: it eats
// below its "medium" health in a group; the numbers here are ours, see foodEatPct). A mage with no food conjures some.

// foodEatPct is the health, in percent of the maximum, below which the leader eats. It is mod-playerbots' "almost full"
// band (85), a little under the dungeon driver's rest gate (90), so that a leader a few points short regenerates by itself
// and one that is well short sits down.
const foodEatPct = 85

// foodSettle is how long the brain waits for the Food aura after it used a piece of food.
const foodSettle = 1500 * time.Millisecond

// foodUseHold is how long a piece of food that was just used is not used again, as drinkUseHold is for drinks.
const foodUseHold = 5 * time.Second

// foodItem is a food the leader may have in the bags, with the character level its use needs (item_template
// required_level in the core's world database).
type foodItem struct {
	entry uint32
	level uint8
}

// foodItems are the foods that restore health and nothing else, best first (the health the spell gives per tick, then the
// required level). Verified against the core's item_template and spell_template (sql/base/tw_world_item_template.sql and
// tw_world_spell_template.sql, tortoise-wow 5d79486): class 0 items whose first spell is one of the plain Food spells
// 433, 434, 435, 1127, 1129, 1131, 25660 and 29073, without the raw, deprecated, holiday and test items. Foods that also
// give a buff (the "Well Fed" kind) are left out on purpose: their spells differ. The entries and levels are the
// database's, the comment names the item and its spell's base points.
var foodItems = []foodItem{
	{22895, 55}, // Conjured Cinnamon Roll (529 base points)
	{60955, 55}, // Gargantuan Tel'Abim Banana (529 base points)
	{21023, 55}, // Dirge's Kickin' Chimaerok Chops (424 base points)
	{51717, 55}, // Hardened Mushroom (424 base points)
	{50739, 51}, // Roasted Tauren (424 base points)
	{50741, 51}, // Gnome Stew (424 base points)
	{8076, 45},  // Conjured Sweet Roll (357 base points)
	{8932, 45},  // Alterac Swiss (357 base points)
	{8948, 45},  // Dried King Bolete (357 base points)
	{8950, 45},  // Homemade Cherry Pie (357 base points)
	{8952, 45},  // Roasted Quail (357 base points)
	{8953, 45},  // Deep Fried Plantains (357 base points)
	{8957, 45},  // Spinefin Halibut (357 base points)
	{11415, 45}, // Mixed Berries (357 base points)
	{11444, 45}, // Grim Guzzler Boar (357 base points)
	{12763, 45}, // Un'Goro Etherfruit (357 base points)
	{16171, 45}, // Shinsollo (357 base points)
	{19225, 45}, // Deep Fried Candybar (357 base points)
	{21031, 45}, // Cabbage Kimchi (357 base points)
	{21033, 45}, // Radish Kimchi (357 base points)
	{22324, 45}, // Winter Kimchi (357 base points)
	{60954, 45}, // Ripe Tel'Abim Banana (357 base points)
	{13888, 45}, // Darkclaw Lobster (231 base points)
	{13893, 45}, // Large Raw Mightfish (231 base points)
	{18255, 45}, // Runn Tum Tuber (231 base points)
	{3927, 35},  // Fine Aged Cheddar (231 base points)
	{4599, 35},  // Cured Ham Steak (231 base points)
	{4601, 35},  // Soft Banana Bread (231 base points)
	{4602, 35},  // Moon Harvest Pumpkin (231 base points)
	{6887, 35},  // Spotted Yellowtail (231 base points)
	{8075, 35},  // Conjured Sourdough (231 base points)
	{9681, 35},  // Grilled King Crawler Legs (231 base points)
	{13930, 35}, // Filet of Redgill (231 base points)
	{16168, 35}, // Heaven Peach (231 base points)
	{16766, 35}, // Undermine Clam Chowder (231 base points)
	{17408, 35}, // Spicy Beefstick (231 base points)
	{18635, 35}, // Bellara's Nutterbar (231 base points)
	{19306, 35}, // Crunchy Frog (231 base points)
	{21030, 35}, // Darnassus Kimchi Pie (231 base points)
	{21552, 35}, // Striped Yellowtail (231 base points)
	{13546, 25}, // Bloodbelly Fish (231 base points)
	{13755, 35}, // Winter Squid (161 base points)
	{84605, 35}, // Mysterious Floater (161 base points)
	{1487, 25},  // Conjured Pumpernickel (161 base points)
	{1707, 25},  // Stormwind Brie (161 base points)
	{3771, 25},  // Wild Hog Shank (161 base points)
	{4539, 25},  // Goldenbark Apple (161 base points)
	{4544, 25},  // Mulgore Spice Bread (161 base points)
	{4594, 25},  // Rockscale Cod (161 base points)
	{4607, 25},  // Delicious Cave Mold (161 base points)
	{8364, 25},  // Mithril Head Trout (161 base points)
	{8543, 25},  // Underwater Mushroom Cap (161 base points)
	{16169, 25}, // Wild Ricecake (161 base points)
	{17407, 25}, // Graccu's Homemade Meat Pie (161 base points)
	{18632, 25}, // Moonbrook Riot Taffy (161 base points)
	{19224, 25}, // Red Hot Wings (161 base points)
	{70241, 25}, // Amberglaze Donut (161 base points)
	{6807, 0},   // Frog Leg Stew (161 base points)
	{422, 15},   // Dwarven Mild (114 base points)
	{1114, 15},  // Conjured Rye (114 base points)
	{3770, 15},  // Mutton Chop (114 base points)
	{4538, 15},  // Snapvine Watermelon (114 base points)
	{4542, 15},  // Moist Cornbread (114 base points)
	{4593, 15},  // Bristle Whisker Catfish (114 base points)
	{4606, 15},  // Spongy Morel (114 base points)
	{5845, 15},  // Flank of Meat (114 base points)
	{7228, 15},  // Tigule and Foror's Strawberry Ice Cream (114 base points)
	{16170, 15}, // Steamed Mandu (114 base points)
	{19305, 15}, // Pickled Kodo Foot (114 base points)
	{2685, 10},  // Succulent Pork Ribs (114 base points)
	{5478, 10},  // Dig Rat Stew (114 base points)
	{5526, 10},  // Clam Chowder (114 base points)
	{733, 5},    // Westfall Stew (114 base points)
	{1119, 0},   // Bottled Spirits (114 base points)
	{414, 5},    // Dalaran Sharp (57 base points)
	{1113, 5},   // Conjured Bread (57 base points)
	{2287, 5},   // Haunch of Meat (57 base points)
	{4537, 5},   // Tel'Abim Banana (57 base points)
	{4541, 5},   // Freshly Baked Bread (57 base points)
	{4592, 5},   // Longjaw Mud Snapper (57 base points)
	{4605, 5},   // Red-speckled Mushroom (57 base points)
	{5066, 5},   // Fissure Plant (57 base points)
	{5095, 5},   // Rainbow Fin Albacore (57 base points)
	{6316, 5},   // Loch Frenzy Delight (57 base points)
	{6890, 5},   // Smoked Bear Meat (57 base points)
	{12238, 5},  // Darkshore Grouper (57 base points)
	{16167, 5},  // Versicolor Treat (57 base points)
	{17119, 5},  // Deeprun Rat Kabob (57 base points)
	{17406, 5},  // Holiday Cheesewheel (57 base points)
	{18633, 5},  // Styleen's Sour Suckerpop (57 base points)
	{19304, 5},  // Spiced Beef Jerky (57 base points)
	{1326, 0},   // Sauteed Sunfish (57 base points)
	{6458, 5},   // Oil Covered Fish (16 base points)
	{117, 1},    // Tough Jerky (16 base points)
	{787, 1},    // Slitherskin Mackerel (16 base points)
	{2070, 1},   // Darnassian Bleu (16 base points)
	{2679, 1},   // Charred Wolf Meat (16 base points)
	{2681, 1},   // Roasted Boar Meat (16 base points)
	{4536, 1},   // Shiny Red Apple (16 base points)
	{4540, 1},   // Tough Hunk of Bread (16 base points)
	{4604, 1},   // Forest Mushroom Cap (16 base points)
	{4656, 1},   // Small Pumpkin (16 base points)
	{5057, 1},   // Ripe Watermelon (16 base points)
	{5349, 1},   // Conjured Muffin (16 base points)
	{6290, 1},   // Brilliant Smallfish (16 base points)
	{7097, 1},   // Leg Meat (16 base points)
	{16166, 1},  // Bean Soup (16 base points)
	{17344, 1},  // Candy Cane (16 base points)
	{19223, 1},  // Darkmoon Dog (16 base points)
	{80168, 1},  // Crunchy Murloc Fin (16 base points)
	{80251, 1},  // Crusty Flatbread (16 base points)
	{961, 0},    // Healing Herb (16 base points)
}

// HealthPct is the player's health as a percent of its maximum (100 when it has no maximum).
func HealthPct(c *Ctx) int {
	if c.Self.MaxHealth == 0 {
		return 100
	}
	return int(uint64(c.Self.Health) * 100 / uint64(c.Self.MaxHealth))
}

// foodEntry is the best food in the bags the leader's level allows, 0 when there is none. Unless anyOK, a food used within
// foodUseHold is passed over for the next one.
func foodEntry(c *Ctx, anyOK bool) uint32 {
	have := map[uint32]bool{}
	for _, it := range c.G.Items() {
		have[it.Entry] = true
	}
	for _, f := range foodItems {
		if !have[f.entry] || uint32(f.level) > c.Self.Level {
			continue
		}
		if !anyOK && c.mem.entryHeld(f.entry, c.Now) {
			continue
		}
		return f.entry
	}
	return 0
}

// restEat is the leader's eating: out of combat, with no mob to fight, only where upkeep is allowed (Ctx.RestOK, so in a
// dungeon run only inside a rest gate), and below foodEatPct health. It works for any class; a mage with no food conjures
// some first (Conjure Food), and for the others the spell is simply unknown.
func restEat() *Strategy {
	ooc := func(c *Ctx) bool {
		return c.RestOK && c.SelfOK && !inCombat(c) && ValFight.Get(c).Mob == nil
	}
	return &Strategy{
		Name: "rest_eat",
		Actions: []*Action{
			{Name: "conjure_food", Spell: "Conjure Food", Resolve: conjureFoodSpell,
				Possible: func(c *Ctx) bool { return !inCombat(c) },
				Useful:   func(c *Ctx) bool { return foodEntry(c, true) == 0 }},
			{Name: "eat", Pre: []string{"conjure_food"}, Settle: foodSettle,
				Possible: func(c *Ctx) bool { return !inCombat(c) && foodEntry(c, false) != 0 },
				Useful:   func(c *Ctx) bool { return !HasAura(&c.Self, "Food") },
				Do: func(ctx context.Context, c *Ctx) error {
					e := foodEntry(c, false)
					if e == 0 {
						return errors.New("no food in the bags")
					}
					if err := c.G.UseItem(ctx, e); err != nil {
						return err
					}
					c.mem.holdEntry(e, c.Now.Add(foodUseHold))
					return nil
				}},
		},
		Rules: []Rule{
			// One step under drinking: a leader low on both drinks first, then eats. A shapeshifted player asks for no meal: the core
			// refuses every item in a form. A feral druid with food leaves its form to eat first (druidHome's leave_form_to_rest).
			{Trigger: Trigger{Name: "health_low", Active: func(c *Ctx) bool { return ooc(c) && HealthPct(c) < foodEatPct && formOf(c) == "" }},
				Do: []Next{{"eat", RelNormal - 1}}},
		},
	}
}
