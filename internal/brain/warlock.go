package brain

import (
	"context"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/spells"
)

// The warlock (issue #80 step 4d, preset affliction_warlock): a ranged damage dealer that fights with damage over time, drains and its
// demon. The order of the rotation and the thresholds are ideas from mod-playerbots' warlock behaviour (GPL-2.0+; ideas and numbers
// only, no code); every spell name, level, cost and range is checked against the core's spell data in TestWarlockSpellData. The class has
// no stance or form in the core's spell_template.
//
//   - Corruption on a mob that has none and is above warlockDotMinPct; a curse (one per mob, see below); Shadow Bolt as the filler. Casts
//     with a cast time wait until the warlock stands still.
//   - One curse per mob, and never over another: a mob that carries any "Curse of ..." aura gets none from the brain (the aura does not
//     say whose it is, so another warlock's curse is as good as ours). Curse of Agony is the damage curse; Curse of the Elements goes
//     instead on a mob above 60% health when two or more other casters are in the group (the mage's and the priest's spells gain from it).
//   - Drain Life below 45% health (the drain heals), not on a mob that is nearly dead. Drain Soul on a mob at 25% or less when the bags hold
//     fewer than warlockShardsWanted soul shards and the mob still gives experience (a grey mob gives no shard).
//   - Life Tap in combat below 25% mana while health is above 60%; out of combat below 40% mana while health is above 85%, before the drink.
//   - Emergencies. Death Coil with a mob on the warlock and health below 40%. Fear, one step below it, only when no tank is in view
//     (a feared mob runs into the dungeon's next pack) and no unengaged mob stands within 30 yd. Shadow Ward when a mob in combat is
//     casting a shadow spell at the warlock.
//   - Upkeep out of combat at a rest gate: Demon Armor (Demon Skin, the older name) when it is down; the best Healthstone the warlock can
//     make when it carries none of that tier or better and the bags hold two shards (one stays for the demon).
//
// The demon is the shared strategy "pet" (pet.go): the Voidwalker when the bags hold a shard, else the Imp.

const (
	warlockDotMinPct       = 30 // Corruption on a mob above this health
	warlockAgonyMinPct     = 40 // Curse of Agony above this
	warlockElementsMinPct  = 60
	warlockDrainLifePct    = 45 // Drain Life below this own health
	warlockDrainMobMinPct  = 15 // and on a mob above this
	warlockDrainSoulPct    = 25 // Drain Soul on a mob at or below this
	warlockShardsWanted    = 5  // Drain Soul while the bags hold fewer
	warlockTapManaPct      = 25 // Life Tap in combat below this mana
	warlockTapHealthPct    = 60 // with at least this health
	warlockTapRestMana     = 40 // out of combat below this mana
	warlockTapRestHealth   = 85 // with at least this health
	warlockCoilPct         = 40 // Death Coil below this health, with a mob on the warlock
	warlockFearPct         = 35 // Fear below this
	warlockFearRange       = 12.0
	warlockLooseFree       = 30 // Fear only when no unengaged mob stands within this
	warlockElementsCasters = 2
	warlockShieldSchool    = 5 // a shadow spell: School 5 in the spell data
)

func onWarlock(c *Ctx) bool { return onPower(c, powerMana) && c.Self.Class == ClassWarlock }

func moving(c *Ctx) bool { return c.Self.Moving }

// hasCurse says the mob carries a curse, whoever cast it.
func hasCurse(m *objects.Unit) bool { return m != nil && auraWithPrefix(m, "Curse of ") != "" }

// castersInGroup counts the other party members in view whose class casts spells a curse of the Elements helps.
func castersInGroup(c *Ctx) int {
	n := 0
	for _, m := range c.Party {
		if !m.Online || m.Dead || m.GUID == c.Self.GUID {
			continue
		}
		if u, ok := c.Unit(m.GUID); ok && !u.Dead {
			switch u.Class {
			case ClassMage, ClassWarlock, ClassPriest, ClassShaman, ClassDruid:
				n++
			}
		}
	}
	return n
}

// curseWant is the curse to cast on the fight's mob now, "" for none.
func curseWant(c *Ctx) string {
	m := fightMob(c)
	if m == nil || hasCurse(m) {
		return ""
	}
	pct := mobHealthPct(m)
	if knows(c, "Curse of the Elements") && castersInGroup(c) >= warlockElementsCasters && pct >= warlockElementsMinPct {
		return "Curse of the Elements"
	}
	if knows(c, "Curse of Agony") && pct >= warlockAgonyMinPct {
		return "Curse of Agony"
	}
	return ""
}

// grayLevel is the highest mob level that gives a player of the level no experience (the 1.12 formula, Formulas.h).
func grayLevel(level uint32) uint32 {
	switch {
	case level < 6:
		return 0
	case level < 40:
		return level - 5 - level/10
	case level < 60:
		return level - 1 - level/5
	}
	return level - 9
}

// mobYieldsXP says a kill of the mob gives experience, so Drain Soul on it can yield a shard. An unknown level counts as yes.
func mobYieldsXP(c *Ctx, m *objects.Unit) bool {
	return m == nil || m.Level == 0 || m.Level > grayLevel(c.Self.Level)
}

// drainSoulOK says the fight's mob is dying, would give a shard, and the bags are short of them.
func drainSoulOK(c *Ctx) bool {
	m := fightMob(c)
	if m == nil {
		return false
	}
	pct := mobHealthPct(m)
	return pct <= warlockDrainSoulPct && pct > 0 && soulShards(c) < warlockShardsWanted && mobYieldsXP(c, m) && !HasAura(m, "Drain Soul")
}

// shadowCastAtMe is a mob in combat casting a shadow spell at the warlock.
func shadowCastAtMe(c *Ctx) bool {
	for i := range c.Units {
		u := &c.Units[i]
		if !hostileMob(u) || !u.InCombat || u.Casting == nil || u.Casting.Target != c.Self.GUID {
			continue
		}
		if m, ok := spells.Lookup(u.Casting.SpellID); ok && m.School == warlockShieldSchool {
			return true
		}
	}
	return false
}

// fearOK says Fear may be used: a mob on the warlock within reach, the warlock hurt, no tank in view and no unengaged mob near.
func fearOK(c *Ctx) bool {
	m := ValMobOnMe.Get(c)
	return m != nil && m.Distance >= 0 && m.Distance <= warlockFearRange && healthPct(c) < warlockFearPct && tankInView(c) == nil && looseMobs(c, warlockLooseFree) == 0
}

func coilOK(c *Ctx) bool {
	m := ValMobOnMe.Get(c)
	return m != nil && healthPct(c) < warlockCoilPct
}

func mobOnMeGUID(c *Ctx) packet.GUID {
	if m := ValMobOnMe.Get(c); m != nil {
		return m.GUID
	}
	return 0
}

// armorWanted: Demon Armor (or Demon Skin) is down.
var warlockArmor = []string{"Demon Armor", "Demon Skin"}

func armorSpell(c *Ctx) (uint32, bool) {
	for _, n := range warlockArmor {
		if id, ok := c.Spells.Highest(n); ok {
			return id, true
		}
	}
	return 0, false
}

// healthstoneMakers, best first: the spell and the stones it makes (the plain one and the two talent ones, as the healthstones table has them).
var healthstoneMakers = []struct {
	spell  string
	stones []uint32
}{
	{"Create Healthstone (Major)", []uint32{19013, 19012, 9421}},
	{"Create Healthstone (Greater)", []uint32{19011, 19010, 5510}},
	{"Create Healthstone", []uint32{19009, 19008, 5509}},
	{"Create Healthstone (Lesser)", []uint32{19007, 19006, 5511}},
	{"Create Healthstone (Minor)", []uint32{19005, 19004, 5512}},
}

// healthstoneToMake is the spell of the best tier the warlock knows when the bags hold no stone of that tier or a better one.
func healthstoneToMake(c *Ctx) (uint32, bool) {
	bag := ValBag.Get(c)
	for _, mk := range healthstoneMakers {
		id, ok := c.Spells.Highest(mk.spell)
		if !ok {
			// A better tier the warlock cannot make may still be in the bags; it counts below.
			for _, e := range mk.stones {
				if bag[e] > 0 {
					return 0, false
				}
			}
			continue
		}
		for _, e := range mk.stones {
			if bag[e] > 0 {
				return 0, false
			}
		}
		return id, true
	}
	return 0, false
}

func lifeTapOK(c *Ctx) bool {
	if !onWarlock(c) || !knows(c, "Life Tap") {
		return false
	}
	if c.Self.InCombat {
		return PowerPct(c) < warlockTapManaPct && healthPct(c) >= warlockTapHealthPct
	}
	return restNow(c) && PowerPct(c) < warlockTapRestMana && healthPct(c) >= warlockTapRestHealth
}

func warlockAffliction() *Strategy {
	mobPct := func(c *Ctx) int { return mobHealthPct(fightMob(c)) }
	dps := func(name, spell string, possible func(*Ctx) bool) *Action {
		return &Action{Name: name, Spell: spell, Harmful: true, Threat: ThreatSingle, Target: fightTarget, Pre: []string{"face_target"},
			Possible: func(c *Ctx) bool { return onWarlock(c) && (possible == nil || possible(c)) }}
	}
	standing := func(c *Ctx) bool { return !moving(c) }
	corruption := dps("corruption", "Corruption", func(c *Ctx) bool {
		return standing(c) && !HasAura(fightMob(c), "Corruption") && mobPct(c) > warlockDotMinPct
	})
	curse := &Action{Name: "curse", Harmful: true, Threat: ThreatSingle, Target: fightTarget, Pre: []string{"face_target"},
		Resolve:  spellByName(curseWant),
		Possible: func(c *Ctx) bool { return onWarlock(c) && curseWant(c) != "" }}
	bolt := dps("shadow_bolt", "Shadow Bolt", standing)
	drainLife := dps("drain_life", "Drain Life", func(c *Ctx) bool {
		return standing(c) && healthPct(c) < warlockDrainLifePct && mobPct(c) > warlockDrainMobMinPct
	})
	drainSoul := dps("drain_soul", "Drain Soul", func(c *Ctx) bool { return standing(c) && drainSoulOK(c) })
	return &Strategy{
		Name: "warlock_affliction",
		Actions: []*Action{
			corruption, curse, bolt, drainLife, drainSoul,
			{Name: "life_tap", Spell: "Life Tap", Possible: lifeTapOK},
			{Name: "death_coil", Spell: "Death Coil", Harmful: true, Threat: ThreatSingle, Target: mobOnMeGUID, Pre: []string{"face_target"},
				Possible: func(c *Ctx) bool { return onWarlock(c) && coilOK(c) }},
			{Name: "fear", Spell: "Fear", Harmful: true, Threat: ThreatSingle, Target: mobOnMeGUID, Pre: []string{"face_target"},
				Possible: func(c *Ctx) bool { return onWarlock(c) && fearOK(c) }},
			{Name: "shadow_ward", Spell: "Shadow Ward",
				Possible: func(c *Ctx) bool { return onWarlock(c) && shadowCastAtMe(c) },
				Useful:   func(c *Ctx) bool { return !HasAura(&c.Self, "Shadow Ward") }},
		},
		Rules: []Rule{
			{Trigger: Trigger{Name: "fight_target", Active: func(c *Ctx) bool { return onWarlock(c) && fightMob(c) != nil }},
				Do: []Next{{"corruption", RelNormal + 6}, {"curse", RelNormal + 5}, {"drain_soul", RelNormal + 4}, {"shadow_bolt", RelNormal}}},
			{Trigger: Trigger{Name: "health_low", Active: func(c *Ctx) bool { return onWarlock(c) && fightMob(c) != nil && healthPct(c) < warlockDrainLifePct }},
				Do: []Next{{"drain_life", RelNormal + 8}}},
			{Trigger: Trigger{Name: "mana_low", Active: lifeTapOK}, Do: []Next{{"life_tap", RelNormal + 7}}},
			{Trigger: Trigger{Name: "mob_on_warlock_hurt", Active: func(c *Ctx) bool { return onWarlock(c) && coilOK(c) }},
				Do: []Next{{"death_coil", relEmergency}}},
			{Trigger: Trigger{Name: "mob_on_warlock_alone", Active: func(c *Ctx) bool { return onWarlock(c) && fearOK(c) }},
				Do: []Next{{"fear", relEmergency - 1}}},
			{Trigger: Trigger{Name: "shadow_cast_at_warlock", Active: func(c *Ctx) bool { return onWarlock(c) && shadowCastAtMe(c) }},
				Do: []Next{{"shadow_ward", RelInterrupt + 1}}},
		},
	}
}

// warlockUpkeep is what the warlock does out of combat at a rest gate: Demon Armor, a Healthstone, and the Life Tap that turns health into
// mana before the drink (lifeTapOK; the rule lives with the combat ones).
func warlockUpkeep() *Strategy {
	ooc := func(c *Ctx) bool { return onWarlock(c) && restNow(c) }
	armorDown := func(c *Ctx) bool { return ooc(c) && auraMissing(&c.Self, warlockArmor...) }
	stoneWanted := func(c *Ctx) bool {
		if !ooc(c) || PowerPct(c) < 40 {
			return false
		}
		id, ok := healthstoneToMake(c)
		if !ok || !reagentsOK(c, id) {
			return false
		}
		// One shard stays for the demon, unless it is out already.
		return soulShards(c) >= 2 || (soulShards(c) >= 1 && ValPet.Get(c) != nil)
	}
	return &Strategy{
		Name: "warlock_upkeep",
		Actions: []*Action{
			{Name: "demon_armor", Resolve: armorSpell, Useful: func(c *Ctx) bool { return auraMissing(&c.Self, warlockArmor...) }},
			{Name: "create_healthstone",
				Resolve:  func(c *Ctx) (uint32, bool) { return healthstoneToMake(c) },
				Possible: stoneWanted,
				Do: func(ctx context.Context, c *Ctx) error {
					id, ok := healthstoneToMake(c)
					if !ok {
						return nil
					}
					return c.cast(ctx, id, c.Self.GUID)
				}},
		},
		Rules: []Rule{
			{Trigger: Trigger{Name: "armor_missing", Active: armorDown}, Do: []Next{{"demon_armor", RelNormal + 2}}},
			{Trigger: Trigger{Name: "no_healthstone", Active: stoneWanted}, Do: []Next{{"create_healthstone", RelNormal + 1}}},
		},
	}
}

func init() {
	strategyBuilders["warlock_affliction"] = warlockAffliction
	strategyBuilders["warlock_upkeep"] = warlockUpkeep
	presets["affliction_warlock"] = []string{"combat_dps", "warlock_affliction", "warlock_upkeep", "pet", "ranged_position", "avoid_ground",
		"combat_consumables", "wand", "rest_eat", "rest_drink"}
}
