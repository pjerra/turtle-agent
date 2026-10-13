package brain

import (
	"context"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// The priest, the third class of the brain (issue #80 step 4a): a healer (holy_priest: the healer role with the priest's kit) and a damage
// dealer (shadow_priest). The rotation is the harvest of mod-playerbots' priest behaviour (docs: PLAYERBOTS-HARVEST, Priest table; ideas and
// numbers only, GPL-2.0+), with the spell names, levels, costs and ranges checked against the core's spell data in TestPriestSpellData.
// Spells are named and the brain casts the highest known rank (the healer chooses its ranks by the deficit, heal.go), except Shadowform,
// which is cast by id: the spellbook also holds its passives under the same name.

// spellShadowform is Shadowform, the talent (the passives 16592, 22917, 51125 and 52669 share its name).
const spellShadowform uint32 = 15473

// priestHeals is what a priest heals and dispels with. Lesser Heal (3 ranks, level 1 to 10), Heal (4, level 16 to 34) and Greater Heal
// (5, level 40 to 60) are the direct heals; Flash Heal (7, level 20 to 56) is the quick and dear one. Dispel Magic removes the magic of a
// friend (harmful: a fear, a slow, a polymorph), Abolish Disease the better disease cure, Cure Disease the other.
var priestHeals = HealKit{
	Direct: []string{"Lesser Heal", "Heal", "Greater Heal", "Flash Heal"},
	Fast:   []string{"Flash Heal"},
	HoT:    "Renew",
	Dispels: []DispelSpell{
		{Spell: "Dispel Magic", Types: []uint8{dispelMagic}},
		{Spell: "Abolish Disease", Types: []uint8{dispelDisease}, Cures: "Abolish Disease"},
		{Spell: "Cure Disease", Types: []uint8{dispelDisease}},
	},
}

const (
	shieldTankMana  = 50 // a shield for the tank before the pull needs this much mana
	shieldBurstMana = 20
	screamHealthPct = 70 // Psychic Scream below this health with 2 or more mobs in melee
	screamFree      = 30 // and no mob that is not fighting the party, or fighting someone else, within this many yards
	shieldHold      = 6 * time.Second
)

func init() {
	strategyBuilders["priest_holy"] = priestHoly
	strategyBuilders["priest_defense"] = priestDefense
	strategyBuilders["priest_buffs"] = priestBuffs
	strategyBuilders["priest_shadow"] = priestShadow
	presets["holy_priest"] = []string{"role_healer", "priest_holy", "priest_defense", "priest_buffs", "rez", "healer_position", "avoid_ground",
		"combat_consumables", "wand", "rest_eat", "rest_drink"}
	presets["shadow_priest"] = []string{"combat_dps", "priest_shadow", "priest_defense", "priest_buffs", "ranged_position", "avoid_ground",
		"class_interrupt", "combat_consumables", "wand", "rest_eat", "rest_drink"}
}

// ---- Power Word: Shield ----

// shielded says the unit carries a shield or the Weakened Soul it leaves: no second shield can be cast on it.
func shielded(u *objects.Unit) bool { return HasAura(u, "Power Word: Shield", "Weakened Soul") }

// shieldPlan is the shield of the tick.
type shieldPlan struct {
	Target *objects.Unit
	Spell  uint32
	Burst  bool // a member who is losing health fast, against the tank before a pull
}

// ValShield picks who gets a Power Word: Shield. First a member taking burst damage (a quarter of its health lost within 3 s and not
// above 85% now), the one that lost the most; else the tank when it has a living mob as its target (the pull is on, or the fight is) and
// mana is not short. A member that carries a shield or Weakened Soul (15 s after a shield) is left alone, and so is one that was shielded
// within the last 6 s while the auras are on their way.
var ValShield = &Value[shieldPlan]{Name: "shield_plan", Calc: func(c *Ctx) shieldPlan {
	id, ok := c.Spells.Highest("Power Word: Shield")
	if !ok || !healAllowed(c) || HasAura(&c.Self, "Shadowform") {
		return shieldPlan{}
	}
	mana := PowerPct(c)
	free := func(u *objects.Unit) bool {
		return !shielded(u) && c.Now.Sub(c.mem.heal.shieldedAt(u.GUID)) > shieldHold && inHealReach(u) && sightOK(c, u)
	}
	var burst *objects.Unit
	var most uint32
	if mana >= shieldBurstMana {
		for _, u := range healMembers(c) {
			hp, max := memberHealth(c, u)
			lost := c.mem.heal.lost(c.Now, u.GUID, u.Health)
			if max > 0 && float64(lost)/max*100 >= burstLossPct && hp/max < 0.85 && lost > most && free(u) {
				burst, most = u, lost
			}
		}
	}
	if burst != nil {
		return shieldPlan{Target: burst, Spell: id, Burst: true}
	}
	if t := ValTank.Get(c); t != nil && mana >= shieldTankMana && free(t) {
		if m, ok := c.Unit(t.Target); ok && hostileMob(m) {
			return shieldPlan{Target: t, Spell: id}
		}
	}
	return shieldPlan{}
}}

// priestHoly is the holy priest's own abilities on top of the healer role: the shield on the tank at the pull and on a member in burst.
func priestHoly() *Strategy {
	return &Strategy{
		Name: "priest_holy",
		Actions: []*Action{{Name: "shield",
			Resolve: func(c *Ctx) (uint32, bool) { s := ValShield.Get(c); return s.Spell, s.Target != nil },
			Target:  func(c *Ctx) packet.GUID { return targetGUID(ValShield.Get(c).Target) },
			Do: func(ctx context.Context, c *Ctx) error {
				s := ValShield.Get(c)
				if s.Target == nil {
					return nil
				}
				c.mem.heal.noteShield(s.Target.GUID, c.Now)
				return healCast(ctx, c, s.Spell, s.Target.GUID)
			}}},
		Rules: []Rule{
			{Trigger: Trigger{Name: "member_in_burst", Active: func(c *Ctx) bool { s := ValShield.Get(c); return s.Target != nil && s.Burst }},
				Do: []Next{{"shield", RelHigh + 6}}},
			{Trigger: Trigger{Name: "tank_pulling", Active: func(c *Ctx) bool { s := ValShield.Get(c); return s.Target != nil && !s.Burst }},
				Do: []Next{{"shield", RelHigh + 1}}},
		},
	}
}

// ---- Fade and Psychic Scream ----

// meleeMobs counts the living hostile mobs in combat that have the player as their target within melee reach.
func meleeMobs(c *Ctx) int {
	n := 0
	for i := range c.Units {
		u := &c.Units[i]
		if hostileMob(u) && u.InCombat && u.Target == c.Self.GUID && u.Distance >= 0 && u.Distance <= MeleeRange {
			n++
		}
	}
	return n
}

// looseMobs counts the living hostile mobs within the distance that the party is not fighting: not in combat (asleep, patrolling), or
// in combat with someone who is not in the party. A fear sends the mobs it hits running, and they run into these.
func looseMobs(c *Ctx, within float32) int {
	party := map[packet.GUID]bool{c.Self.GUID: true}
	for _, m := range c.Party {
		party[m.GUID] = true
	}
	n := 0
	for i := range c.Units {
		u := &c.Units[i]
		if !hostileMob(u) || u.Distance < 0 || u.Distance > within {
			continue
		}
		if !u.InCombat || (u.Target != 0 && !party[u.Target]) {
			n++
		}
	}
	return n
}

func screamNow(c *Ctx) bool {
	return inCombat(c) && meleeMobs(c) >= 2 && healthPct(c) < screamHealthPct && looseMobs(c, screamFree) == 0
}

// priestDefense is the priest's answer to a mob that has picked it: Fade (a threat drop; the mob then loses interest, or the tank takes it)
// when a mob has the priest as its target, and Psychic Scream as an emergency, with 2 or more mobs in melee and the priest below 70% health.
// A fear scatters the mobs, and a scattered mob that runs into a sleeping pack pulls it: the scream is held back while any mob that the
// party is not fighting stands within 30 yd.
func priestDefense() *Strategy {
	return &Strategy{
		Name: "priest_defense",
		Actions: []*Action{
			{Name: "fade", Spell: "Fade", Useful: func(c *Ctx) bool { return !HasAura(&c.Self, "Fade") }},
			{Name: "psychic_scream", Spell: "Psychic Scream", Threat: ThreatAoE, Possible: screamNow},
		},
		Rules: []Rule{
			{Trigger: Trigger{Name: "mob_on_priest", Active: func(c *Ctx) bool { return inCombat(c) && ValMobOnMe.Get(c) != nil }},
				Do: []Next{{"fade", RelHigh + 3}}},
			{Trigger: Trigger{Name: "mobs_in_melee_hurt", Active: screamNow}, Do: []Next{{"psychic_scream", RelInterrupt + 3}}},
		},
	}
}

// ---- buffs ----

var (
	fortitudeAuras = []string{"Power Word: Fortitude", "Prayer of Fortitude"}
	buffReach      = 28.0 // Power Word: Fortitude reaches 30 yd
)

// fortitudeTarget is the first of the player and the members in view that lacks Fortitude (the player's own also when it has under a
// minute left, if the server says how long).
func fortitudeTarget(c *Ctx) packet.GUID {
	for _, u := range healMembers(c) {
		if u.GUID == c.Self.GUID {
			if auraMissing(u, fortitudeAuras...) {
				return u.GUID
			}
			continue
		}
		if float64(u.Distance) <= buffReach && !HasAura(u, fortitudeAuras...) && sightOK(c, u) {
			return u.GUID
		}
	}
	return 0
}

// buffsAllowed says the priest may buff now: out of combat at a rest gate, with no mob to fight, not in Shadowform, with a third of the mana.
func buffsAllowed(c *Ctx) bool {
	return c.RestOK && onSelf(c) && !inCombat(c) && ValFight.Get(c).Mob == nil && !HasAura(&c.Self, "Shadowform") && PowerPct(c) >= 30
}

// buffsPending says a buff the priest knows is missing and it may cast now: Shadowform waits for them, since no Holy spell can be
// cast in the form.
func buffsPending(c *Ctx) bool {
	if !buffsAllowed(c) {
		return false
	}
	if _, ok := c.Spells.Highest("Inner Fire"); ok && auraMissing(&c.Self, "Inner Fire") {
		return true
	}
	_, ok := c.Spells.Highest("Power Word: Fortitude")
	return ok && fortitudeTarget(c) != 0
}

// priestBuffs is the upkeep out of combat, at the rest gates (and for a brain started by hand, always): Inner Fire on the priest and Power
// Word: Fortitude on the priest and every member in view. The buffs are Holy spells, which Shadowform forbids, so a shadow priest buffs
// before it takes the form and not after. Not with less than a third of the mana: drinking comes first.
func priestBuffs() *Strategy {
	ooc := buffsAllowed
	return &Strategy{
		Name: "priest_buffs",
		Actions: []*Action{
			{Name: "inner_fire", Spell: "Inner Fire", Useful: func(c *Ctx) bool { return auraMissing(&c.Self, "Inner Fire") }},
			{Name: "fortitude", Spell: "Power Word: Fortitude", Target: fortitudeTarget},
		},
		Rules: []Rule{
			{Trigger: Trigger{Name: "inner_fire_missing", Active: func(c *Ctx) bool { return ooc(c) && auraMissing(&c.Self, "Inner Fire") }},
				Do: []Next{{"inner_fire", RelNormal + 3}}},
			{Trigger: Trigger{Name: "fortitude_missing", Active: func(c *Ctx) bool { return ooc(c) && fortitudeTarget(c) != 0 }},
				Do: []Next{{"fortitude", RelNormal + 2}}},
		},
	}
}

// ---- the shadow priest ----

// priestShadow is the damage rotation of a shadow priest: Shadowform when known, Vampiric Embrace and Shadow Word: Pain on the fight's mob
// (not on one that is nearly dead), Mind Blast on cooldown, Mind Flay as the filler (Smite when Mind Flay is not known or not in range
// and the priest is not in Shadowform, which forbids Holy spells), and a shield on the priest when a mob has picked it. Silence, a talent,
// is the interrupt (class_interrupt); the wand takes over when mana runs dry. Spell data: Mind Flay reaches 20 yd, so the preset stands
// the priest 14 to 19 yd from the mob (ClassInfo.Band).
func priestShadow() *Strategy {
	form := func(c *Ctx) bool { return HasAura(&c.Self, "Shadowform") }
	mobPct := func(c *Ctx) int { return mobHealthPct(fightMob(c)) }
	harm := func(name, spell string) *Action {
		return &Action{Name: name, Spell: spell, Harmful: true, Threat: ThreatSingle, Target: fightTarget, Pre: []string{"face_target"}}
	}
	vampiric := harm("vampiric_embrace", "Vampiric Embrace")
	vampiric.Useful = func(c *Ctx) bool { return !HasAura(fightMob(c), "Vampiric Embrace") && mobPct(c) > 30 }
	pain := harm("shadow_word_pain", "Shadow Word: Pain")
	pain.Useful = func(c *Ctx) bool { return !HasAura(fightMob(c), "Shadow Word: Pain") && mobPct(c) > 25 }
	blast := harm("mind_blast", "Mind Blast")
	flay := harm("mind_flay", "Mind Flay")
	flay.Possible = func(c *Ctx) bool { return !c.Self.Moving }
	flay.Alt = []string{"smite"}
	smite := harm("smite", "Smite")
	smite.Possible = func(c *Ctx) bool { return !form(c) && !c.Self.Moving }
	return &Strategy{
		Name: "priest_shadow",
		Actions: []*Action{
			{Name: "shadowform",
				Resolve: func(c *Ctx) (uint32, bool) { return spellShadowform, c.Spells.Known(spellShadowform) },
				Useful:  func(c *Ctx) bool { return !form(c) }},
			vampiric, pain, blast, flay, smite,
			{Name: "shield_self", Spell: "Power Word: Shield",
				Possible: func(c *Ctx) bool {
					return !form(c) && !shielded(&c.Self) && c.Now.Sub(c.mem.heal.shieldedAt(c.Self.GUID)) > shieldHold
				},
				Do: func(ctx context.Context, c *Ctx) error {
					id, ok := c.Spells.Highest("Power Word: Shield")
					if !ok {
						return nil
					}
					c.mem.heal.noteShield(c.Self.GUID, c.Now)
					return healCast(ctx, c, id, c.Self.GUID)
				}},
		},
		Rules: []Rule{
			{Trigger: Trigger{Name: "no_shadowform", Active: func(c *Ctx) bool {
				// Out of combat the buffs come first (the Holy spells are barred in the form); in a fight the form does not wait.
				return !form(c) && c.Spells.Known(spellShadowform) && (inCombat(c) || ValFight.Get(c).Mob != nil || (c.RestOK && !buffsPending(c)))
			}}, Do: []Next{{"shadowform", RelNormal + 6}}},
			{Trigger: Trigger{Name: "fight_target", Active: func(c *Ctx) bool { return ValFight.Get(c).Mob != nil }},
				Do: []Next{{"vampiric_embrace", RelNormal + 5}, {"shadow_word_pain", RelNormal + 4}, {"mind_blast", RelNormal + 3}, {"mind_flay", RelNormal}}},
			{Trigger: Trigger{Name: "mob_on_priest", Active: func(c *Ctx) bool { return ValMobOnMe.Get(c) != nil && healthPct(c) < 80 }},
				Do: []Next{{"shield_self", RelHigh + 4}}},
		},
	}
}
