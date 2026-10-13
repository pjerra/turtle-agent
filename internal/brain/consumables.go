package brain

import (
	"context"
	"errors"
	"time"
)

// Combat consumables and the wand (issue #80 step 3b). Thresholds follow mod-playerbots' health bands (critical 25, low 45; GPL-2.0+,
// numbers only). The agent does not see item cooldowns, so a used item sets a hold of the item group's cooldown (2 minutes for
// potions and healthstones, as item_template.spellcategorycooldown says).

// An item the brain may use, with the character level its use needs (item_template required_level in the core's world database;
// the bandages carry no level, only a First Aid skill, which the agent cannot read: it tries the best one in the bags and
// leaves it alone for five minutes when no First Aid channel starts).
type itemTier struct {
	entry uint32
	level uint8
}

// healthPotions, best first: Major, Superior, Greater, Healing, Lesser, Minor Healing Potion.
var healthPotions = []itemTier{{13446, 45}, {3928, 35}, {1710, 21}, {929, 12}, {858, 3}, {118, 1}}

// manaPotions, best first: Major, Superior, Greater, Mana, Lesser, Minor Mana Potion.
var manaPotions = []itemTier{{13444, 49}, {13443, 41}, {6149, 31}, {3827, 22}, {3385, 14}, {2455, 5}}

// healthstones, best first. Each rank has the talent-improved stones (the 19004-19013 range) next to the plain one.
var healthstones = []itemTier{
	{19013, 48}, {19012, 48}, {9421, 48}, // Major
	{19011, 36}, {19010, 36}, {5510, 36}, // Greater
	{19009, 24}, {19008, 24}, {5509, 24}, // Healthstone
	{19007, 12}, {19006, 12}, {5511, 12}, // Lesser
	{19005, 1}, {19004, 1}, {5512, 1}, // Minor
}

// bandages, best first: Heavy Runecloth ... Linen (First Aid skill 225 ... 1; the level column is 0).
var bandages = []itemTier{{14530, 0}, {14529, 0}, {8545, 0}, {8544, 0}, {6451, 0}, {6450, 0}, {3531, 0}, {3530, 0}, {2581, 0}, {1251, 0}}

const (
	healthCriticalPct = 25 // healthstone and potion below this, in combat
	bandageBelowPct   = 45 // bandage below this, once nothing has hurt the player for a while
	manaPotionBelow   = 10 // mana potion below this in a boss fight
	wandBelowPct      = 15 // wand filler below this

	itemGroupCooldown = 2 * time.Minute // item_template.spellcategorycooldown 120000
	bandageSettle     = 12 * time.Second
	bandageSkipFor    = 5 * time.Minute // a bandage that started no First Aid channel
	bandageWatch      = 1500 * time.Millisecond
	damageQuiet       = 4 * time.Second // no health lost for this long: out of the combat-damage window
)

// bestItem is the best of a tier list the bags hold, the character's level allows, and no hold keeps back.
func bestItem(c *Ctx, tiers []itemTier) uint32 {
	have := map[uint32]bool{}
	for _, it := range c.G.Items() {
		have[it.Entry] = true
	}
	for _, t := range tiers {
		if have[t.entry] && uint32(t.level) <= c.Self.Level && !c.mem.entryHeld(t.entry, c.Now) {
			return t.entry
		}
	}
	return 0
}

// useBest uses the best item of a list on the player.
func useBest(tiers []itemTier, what string) func(context.Context, *Ctx) error {
	return func(ctx context.Context, c *Ctx) error {
		e := bestItem(c, tiers)
		if e == 0 {
			return errors.New("no " + what + " in the bags")
		}
		return c.G.UseItem(ctx, e)
	}
}

// ValBoss tells whether the fight's mob looks like a boss: three levels above the player, or with many times the player's health.
// A 1.12 client sees no rank of a creature, so this is a stand-in.
var ValBoss = &Value[bool]{Name: "boss_fight", Calc: func(c *Ctx) bool {
	m := ValFight.Get(c).Mob
	if m == nil {
		return false
	}
	return m.Level >= c.Self.Level+3 || (c.Self.MaxHealth > 0 && m.MaxHealth >= 6*c.Self.MaxHealth)
}}

// combatConsumables uses a healthstone or a health potion below the critical threshold, a bandage when nothing has hurt the player
// for a few seconds, and a mana potion when a boss fight has left a mana user dry.
func combatConsumables() *Strategy {
	hasItems := func(c *Ctx, tiers []itemTier) bool { return bestItem(c, tiers) != 0 }
	critical := func(c *Ctx) bool {
		return inCombat(c) && healthPct(c) < healthCriticalPct && !HasAura(&c.Self, "Ice Block")
	}
	return &Strategy{
		Name: "combat_consumables",
		Before: func(c *Ctx) {
			e := &c.mem.ext
			e.mu.Lock()
			defer e.mu.Unlock()
			if c.Self.Health < e.prevHealth {
				e.lastDamage = c.Now
			}
			e.prevHealth = c.Self.Health
			if ValMobOnMe.Get(c) != nil {
				e.lastOnMe = c.Now
			}
			if b := e.bandage; b != nil && c.Now.Sub(b.at) > bandageWatch {
				if !(c.Spells.Casting() || HasAura(&c.Self, "Recently Bandaged")) {
					c.mem.holdEntry(b.entry, c.Now.Add(bandageSkipFor))
				}
				e.bandage = nil
			}
		},
		Actions: []*Action{
			{Name: "healthstone", Settle: itemGroupCooldown,
				Possible: func(c *Ctx) bool { return hasItems(c, healthstones) },
				Useful:   func(c *Ctx) bool { return !HasAura(&c.Self, "Ice Block") },
				Do:       useBest(healthstones, "healthstone")},
			{Name: "health_potion", Settle: itemGroupCooldown,
				Possible: func(c *Ctx) bool { return hasItems(c, healthPotions) },
				Useful:   func(c *Ctx) bool { return !HasAura(&c.Self, "Ice Block") },
				Do:       useBest(healthPotions, "health potion")},
			{Name: "bandage", Settle: bandageSettle,
				Possible: func(c *Ctx) bool {
					return hasItems(c, bandages) && !HasAura(&c.Self, "Recently Bandaged") && !c.Self.Moving && !c.Spells.Casting() && damageQuietNow(c)
				},
				Do: func(ctx context.Context, c *Ctx) error {
					e := bestItem(c, bandages)
					if e == 0 {
						return errors.New("no bandage in the bags")
					}
					if err := c.G.UseItem(ctx, e); err != nil {
						return err
					}
					c.mem.ext.mu.Lock()
					c.mem.ext.bandage = &pendingBandage{entry: e, at: c.Now}
					c.mem.ext.mu.Unlock()
					return nil
				}},
			{Name: "mana_potion", Settle: itemGroupCooldown,
				Possible: func(c *Ctx) bool { return onSelf(c) && c.Class().ManaPotions && hasItems(c, manaPotions) },
				Do:       useBest(manaPotions, "mana potion")},
		},
		Rules: []Rule{
			{Trigger: Trigger{Name: "health_critical", Active: critical}, Do: []Next{{"healthstone", RelDispel - 3}, {"health_potion", RelDispel - 4}}},
			{Trigger: Trigger{Name: "health_low_quiet", Active: func(c *Ctx) bool {
				return inCombat(c) && healthPct(c) < bandageBelowPct && damageQuietNow(c)
			}}, Do: []Next{{"bandage", RelNormal + 1}}},
			{Trigger: Trigger{Name: "boss_out_of_mana", Active: func(c *Ctx) bool {
				return onSelf(c) && inCombat(c) && c.Self.MaxPower > 0 && PowerPct(c) < manaPotionBelow && ValBoss.Get(c)
			}}, Do: []Next{{"mana_potion", RelNormal + 3}}},
		},
	}
}

// damageQuietNow is the out-of-combat-damage window: no mob has the player as its target, and no health was lost, for damageQuiet.
func damageQuietNow(c *Ctx) bool {
	e := &c.mem.ext
	e.mu.Lock()
	defer e.mu.Unlock()
	if ValMobOnMe.Get(c) != nil {
		return false
	}
	return c.Now.Sub(e.lastDamage) >= damageQuiet && c.Now.Sub(e.lastOnMe) >= damageQuiet
}

// ---- the wand ----

// wand is the filler of a caster that has run dry: Shoot at the fight's mob below wandBelowPct of the mana. Shoot is an
// auto-repeat spell, cast once and left running until another spell is cast; a wand that is not equipped is refused by the server
// (a long back-off, see holdFor). Behind Frostbolt in relevance, so that it takes over only when the bolt cannot be cast.
func wandStrategy() *Strategy {
	shooting := func(c *Ctx) bool {
		id, ok := c.Spells.Highest("Shoot")
		if !ok {
			return false
		}
		last := c.Spells.CastState().LastCast
		return last != nil && last.SpellID == id && last.AgoMS < 3000
	}
	return &Strategy{
		Name: "wand",
		Actions: []*Action{{Name: "wand", Spell: "Shoot", Harmful: true, Threat: ThreatSingle, Target: fightTarget, Pre: []string{"face_target"},
			Useful: func(c *Ctx) bool { return !shooting(c) },
			Do: func(ctx context.Context, c *Ctx) error {
				id, ok := c.Spells.Highest("Shoot")
				if !ok {
					return nil
				}
				// Shoot repeats by itself: do not send it again for a few seconds, whatever the shots do to the spell state.
				c.mem.holdAction("wand", c.Now.Add(3*time.Second), "shooting")
				return c.cast(ctx, id, fightTarget(c))
			}}},
		Rules: []Rule{{Trigger: Trigger{Name: "out_of_mana", Active: func(c *Ctx) bool {
			return onSelf(c) && c.Self.MaxPower > 0 && PowerPct(c) < wandBelowPct && ValFight.Get(c).Mob != nil
		}}, Do: []Next{{"wand", RelNormal - 1}}}},
	}
}
