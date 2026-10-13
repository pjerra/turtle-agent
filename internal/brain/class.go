package brain

// Class tables (issue #80 step 3c). What the brain needs to know about a class that is not a rotation: the power it plays
// with, the spells it interrupts with, and the items it eats and drinks. A strategy asks the player's class at the tick
// (Ctx.Class) instead of being written for one class, so the mage specifics (Counterspell, conjured water) sit in this table
// and another class is a row, not a new engine. The rotations stay strategies (mage.go, warrior.go).

// Class ids as UNIT_FIELD_BYTES_0 has them (the 1.12 ChrClasses ids; 6 and 10 do not exist).
const (
	ClassWarrior uint8 = 1
	ClassPaladin uint8 = 2
	ClassHunter  uint8 = 3
	ClassRogue   uint8 = 4
	ClassPriest  uint8 = 5
	ClassShaman  uint8 = 7
	ClassMage    uint8 = 8
	ClassWarlock uint8 = 9
	ClassDruid   uint8 = 11
)

// Power types of the unit fields (Powers, UnitDefines.h).
const (
	powerMana   uint8 = 0
	powerRage   uint8 = 1
	powerEnergy uint8 = 3
)

// InterruptSpell is a spell a class interrupts a cast with, and the stance it needs.
type InterruptSpell struct {
	Spell string
	// Stances are the stance auras the spell works in, from the core's spell_template `stances`; empty: any. SpellStances overrides
	// them for a spell id whose data differs. The brain does not change stance for an interrupt: a stance change costs a warrior its
	// rage (without Tactical Mastery), and Pummel costs rage.
	Stances []string
}

// ClassInfo is the table row of a class.
type ClassInfo struct {
	ID    uint8
	Name  string
	Power uint8 // the primary power: mana, rage or energy
	// Interrupts are the spells the class interrupts with, best first. Kick (rogue), Hammer of Justice (paladin, a stun) and Earth Shock (shaman)
	// are used by the step-4b and 4c presets.
	Interrupts []InterruptSpell
	// Drinks and ConjuredDrinks: the class drinks at rest (mana users), and conjures its own water (mages).
	Drinks, ConjuredDrinks bool
	// ManaPotions: the class uses mana potions in a boss fight.
	ManaPotions bool
	// Taunt is the class's single-target taunt, for a player who tanks (empty: the class does not tank).
	Taunt string
	// Heals is what the healer role casts with (heal.go); nil: the class does not heal.
	Heals *HealKit
	// Band is the distance from the fight's mob (min, max yards) a ranged class of this row stands at in ranged_position; zero: the
	// mage's 20 to 28. A class whose main spell reaches less than a Frostbolt (the shadow priest's Mind Flay, 20 yd) sets a nearer one.
	Band [2]float64
}

// Stance auras of the warrior.
const (
	stanceBattle    = "Battle Stance"
	stanceDefensive = "Defensive Stance"
	stanceBerserker = "Berserker Stance"
)

// SpellStances are the stances of a spell id where it differs from the class table's row (core tortoise-wow 5d79486,
// sql/base/tw_world_spell_template.sql, `stances` bitmask: 0x10000 Battle, 0x20000 Defensive, 0x40000 Berserker).
var SpellStances = map[uint32][]string{
	13491: {stanceBattle}, // Pummel, the rank-1 spell of level 6
}

var classTable = map[uint8]ClassInfo{
	ClassWarrior: {ID: ClassWarrior, Name: "warrior", Power: powerRage, Taunt: "Taunt", Interrupts: []InterruptSpell{
		{Spell: "Pummel", Stances: []string{stanceBattle, stanceBerserker}},      // spell 6552; the Turtle rank 13491 is Battle only (SpellStances)
		{Spell: "Shield Bash", Stances: []string{stanceBattle, stanceDefensive}}, // needs a shield; the server's refusal backs it off
	}},
	ClassPaladin: {ID: ClassPaladin, Name: "paladin", Power: powerMana, Drinks: true, ManaPotions: true, Taunt: "Hand of Reckoning", Interrupts: []InterruptSpell{{Spell: "Hammer of Justice"}}},
	ClassHunter:  {ID: ClassHunter, Name: "hunter", Power: powerMana, Drinks: true, ManaPotions: true, Band: [2]float64{10, 30}}, // the dead zone is 8 yd, the shots reach 35
	ClassRogue:   {ID: ClassRogue, Name: "rogue", Power: powerEnergy, Interrupts: []InterruptSpell{{Spell: "Kick"}}},
	ClassPriest: {ID: ClassPriest, Name: "priest", Power: powerMana, Drinks: true, ManaPotions: true, Heals: &priestHeals, Band: [2]float64{14, 19},
		Interrupts: []InterruptSpell{{Spell: "Silence"}}}, // a talent (spell 15487): the spellbook has it or it is skipped
	ClassShaman: {ID: ClassShaman, Name: "shaman", Power: powerMana, Drinks: true, ManaPotions: true, Heals: &shamanHeals, Band: [2]float64{14, 19}, // the shocks reach 20 yd
		Interrupts: []InterruptSpell{{Spell: "Earth Shock"}}},
	ClassMage: {ID: ClassMage, Name: "mage", Power: powerMana, Drinks: true, ConjuredDrinks: true, ManaPotions: true,
		Interrupts: []InterruptSpell{{Spell: "Counterspell"}}},
	ClassWarlock: {ID: ClassWarlock, Name: "warlock", Power: powerMana, Drinks: true, ManaPotions: true},
	ClassDruid:   {ID: ClassDruid, Name: "druid", Power: powerMana, Drinks: true, ManaPotions: true, Taunt: "Growl", Heals: &druidHeals},
}

// ClassOf is the table row of a class id. An id the table does not know (0: the class field is not read yet) answers as a
// mage, the class the brain had before the table existed: mana, drinks, conjured water.
func ClassOf(id uint8) ClassInfo {
	if ci, ok := classTable[id]; ok {
		return ci
	}
	return classTable[ClassMage]
}

// Class is the table row of the player's class.
func (c *Ctx) Class() ClassInfo { return ClassOf(c.Self.Class) }
