package brain

import (
	"context"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// The druid, the fifth class of the brain (issue #80 step 4c): a healer (resto_druid), a caster (balance_druid), a cat (feral_cat_druid)
// and a bear tank (bear_tank_druid). The rotations are the harvest of mod-playerbots' druid behaviour (docs: PLAYERBOTS-HARVEST, Druid
// table; ideas and numbers only, GPL-2.0+), with every spell name, level, cost, range and form mask checked against the core's spell data
// in TestDruidSpellData and TestFormGatesFollowTheCoreData.
//
// Forms. A shapeshift form is an aura, like a warrior's stance, and it decides what the druid can cast: the core refuses a spell with
// SPELL_ATTR_NOT_SHAPESHIFT (every heal, the cures, Mark of the Wild, Innervate) in any form, and the feral abilities outside their form
// (SpellEntry::GetErrorAtShapeshiftedCast; the spell_template `stances` bitmask is 1 << (form - 1): Cat 0x1, Bear 0x10, Dire Bear 0x80,
// Moonkin 0x40000000). The presets therefore keep a home form:
//   - resto and balance live in caster form and leave any other (druid_caster): the form aura is cancelled (CMSG_CANCEL_AURA, the
//     optional AuraCanceller half of the Game) and every other cast waits until it is gone;
//   - feral and bear shift into Cat or (Dire) Bear Form when a fight starts, and do not leave it for anything in combat. Out of combat,
//     at a rest gate, they leave it only to drink or eat (the core refuses every item used in a form; the real mana pool is read off
//     Unit.Mana, which the form's own power hides) or to buff with Mark of the Wild, and shift back after.
// A shift between forms needs mana the unit's Power does not show, so the shift is cast from caster form only (Affordable can read it).

// The shapeshift forms of the druid, by the name of the aura.
const (
	formCat        = "Cat Form"
	formBear       = "Bear Form"
	formDireBear   = "Dire Bear Form"
	formAquatic    = "Aquatic Form"
	formTravel     = "Travel Form"
	formSwiftTrav  = "Swift Travel Form"
	formMoonkin    = "Moonkin Form"
	formTree       = "Tree Form"
	druidShiftHold = 2 * time.Second // the shapeshift cooldown is 1.5 s; a refusal must not loop
)

// druidForm is a form: the aura's name, the spell that casts it, and its bit in the `stances` bitmask of spell_template.
type druidForm struct {
	Name  string
	Spell uint32
	Mask  uint32
}

// druidForms are the forms the brain knows, with the spells listed by id (the spellbook also holds passives and NPC copies under the
// same names).
var druidForms = []druidForm{
	{formCat, 768, 0x1},
	{formTree, 775, 0x2},
	{formTravel, 783, 0x4},
	{formAquatic, 1066, 0x8},
	{formBear, 5487, 0x10},
	{formDireBear, 9634, 0x80},
	{formSwiftTrav, 51398, 0x400},
	{formMoonkin, 24858, 0x40000000},
}

// druidAbilityForms are the forms each gated feral ability works in: the core's `stances` of every rank (tortoise-wow 5d79486,
// sql/base/tw_world_spell_template.sql; the database_updates change none of them), pinned by TestFormGatesFollowTheCoreData.
var druidAbilityForms = map[string][]string{
	"Claw":                  {formCat},
	"Shred":                 {formCat},
	"Rake":                  {formCat},
	"Rip":                   {formCat},
	"Ferocious Bite":        {formCat},
	"Tiger's Fury":          {formCat},
	"Faerie Fire (Feral)":   {formCat, formBear, formDireBear},
	"Maul":                  {formBear, formDireBear},
	"Swipe":                 {formBear, formDireBear},
	"Demoralizing Roar":     {formBear, formDireBear},
	"Growl":                 {formBear, formDireBear},
	"Enrage":                {formBear, formDireBear},
	"Frenzied Regeneration": {formBear, formDireBear},
	"Bash":                  {formBear, formDireBear},
}

var (
	bearForms = []string{formDireBear, formBear}
	catForms  = []string{formCat}
)

// formAuraOf is the form the unit carries and the aura's spell, "" when it is in caster form.
func formAuraOf(u *objects.Unit) (string, uint32) {
	for _, a := range u.Auras {
		for _, f := range druidForms {
			if strings.EqualFold(a.Name, f.Name) {
				return f.Name, a.SpellID
			}
		}
	}
	return "", 0
}

// formOf is the form the player is in, "" in caster form.
func formOf(c *Ctx) string { n, _ := formAuraOf(&c.Self); return n }

// inForm says the player is in one of the forms.
func inForm(c *Ctx, forms ...string) bool { return slices.Contains(forms, formOf(c)) }

// formSpell is the id of the form's spell when the player knows it.
func formSpell(c *Ctx, name string) (uint32, bool) {
	for _, f := range druidForms {
		if f.Name == name {
			return f.Spell, c.Spells.Known(f.Spell)
		}
	}
	return 0, false
}

// homeFormSpell is the spell of the first of the forms (best first) that the spellbook has.
func homeFormSpell(c *Ctx, homes []string) (uint32, bool) {
	for _, h := range homes {
		if id, ok := formSpell(c, h); ok {
			return id, true
		}
	}
	return 0, false
}

// AuraCanceller is the optional half of the Game that removes one of the player's own auras (CMSG_CANCEL_AURA), as a right-click on the
// buff does. A druid leaves its shapeshift form with it. A game without it never leaves a form.
type AuraCanceller interface {
	CancelAura(ctx context.Context, spell uint32) error
}

// manaPct is the mana of the player in percent of the pool, also while a form shows another power; false when the game does not say.
func manaPct(c *Ctx) (int, bool) {
	if !c.SelfOK {
		return 0, false
	}
	if c.Self.PowerType == powerMana {
		return PowerPct(c), true
	}
	if c.Self.MaxMana > 0 {
		return int(uint64(c.Self.Mana) * 100 / uint64(c.Self.MaxMana)), true
	}
	return 0, false
}

// behindMob says the player stands behind the mob: the way from the mob to the player is more than rearMin off the mob's facing, a
// margin inside the half plane the core wants for Shred (SPELL_FAILED_NOT_BEHIND).
func behindMob(c *Ctx, m *objects.Unit) bool {
	return m != nil && math.Abs(angleDiff(m.O, float32(bearing(unitPos(m), selfPos(c))))) > rearMin
}

// comboPoints is how many combo points the player has on the mob.
func comboPoints(c *Ctx, m *objects.Unit) int {
	if m == nil || c.Self.ComboTarget != m.GUID {
		return 0
	}
	return int(c.Self.ComboPoints)
}

// relLifeSaver is the relevance of an emergency answer (Frenzied Regeneration): just above the interrupt band, so that it also cancels
// a walk and may break a cast.
const relLifeSaver = RelInterrupt + 3

func init() {
	strategyBuilders["druid_caster"] = druidCaster
	strategyBuilders["druid_buffs"] = func() *Strategy { return druidBuffs("druid_buffs", true) }
	strategyBuilders["druid_buffs_feral"] = func() *Strategy { return druidBuffs("druid_buffs_feral", false) }
	strategyBuilders["druid_innervate"] = druidInnervate
	strategyBuilders["druid_balance"] = druidBalance
	strategyBuilders["druid_form_cat"] = func() *Strategy { return druidHome("druid_form_cat", catForms) }
	strategyBuilders["druid_form_bear"] = func() *Strategy { return druidHome("druid_form_bear", bearForms) }
	strategyBuilders["druid_cat"] = druidCat
	strategyBuilders["druid_bear"] = druidBear
	presets["resto_druid"] = []string{"role_healer", "druid_caster", "druid_buffs", "druid_innervate", "healer_position", "avoid_ground",
		"combat_consumables", "rest_eat", "rest_drink"}
	presets["balance_druid"] = []string{"combat_dps", "druid_caster", "druid_balance", "druid_buffs", "druid_innervate", "ranged_position",
		"avoid_ground", "combat_consumables", "rest_eat", "rest_drink"}
	presets["feral_cat_druid"] = []string{"combat_dps", "druid_form_cat", "druid_cat", "druid_buffs_feral", "melee_position", "avoid_ground",
		"combat_consumables", "rest_eat", "rest_drink"}
	presets["bear_tank_druid"] = []string{"role_tank", "druid_form_bear", "druid_bear", "druid_buffs_feral", "melee_position", "avoid_ground",
		"combat_consumables", "rest_eat", "rest_drink"}
}

// ---- the heal kit ----

// druidHeals is what a druid heals and cures with. Healing Touch (11 ranks, level 1 to 60) is the big slow heal, Regrowth (9, level 12 to
// 60) the quick and dear one, chosen by what it heals at once; Rejuvenation (11, level 4 to 60) is the heal over time kept on the tank,
// as a priest keeps Renew. Abolish Poison leaves a cure working on its target for 8 s, Cure Poison is the plain one, Remove Curse takes
// curses.
var druidHeals = HealKit{
	Direct:     []string{"Healing Touch", "Regrowth"},
	Fast:       []string{"Regrowth"},
	DirectOnly: []string{"Regrowth"},
	HoT:        "Rejuvenation",
	Dispels: []DispelSpell{
		{Spell: "Abolish Poison", Types: []uint8{dispelPoison}, Cures: "Abolish Poison"},
		{Spell: "Cure Poison", Types: []uint8{dispelPoison}},
		{Spell: "Remove Curse", Types: []uint8{dispelCurse}},
	},
}

// ---- druid_caster: caster form only ----

// druidCaster keeps a healing or casting druid out of every form: all its spells are barred in one (the heals and cures by
// SPELL_ATTR_NOT_SHAPESHIFT, the damage spells but for Moonkin Form by their `stances`), so the form is cancelled at once, above an
// emergency heal, and every cast waits until the aura is gone (the server's update takes a moment).
func druidCaster() *Strategy {
	return &Strategy{
		Name: "druid_caster",
		Mult: []Multiplier{{Name: "caster_form_only", F: func(c *Ctx, a *Action, rel float32) float32 {
			if a.isCast() && a.Name != "leave_form" && formOf(c) != "" {
				return 0
			}
			return 1
		}}},
		Actions: []*Action{leaveFormAction(func(c *Ctx) bool { return true })},
		Rules: []Rule{{Trigger: Trigger{Name: "in_form", Active: func(c *Ctx) bool { return c.SelfOK && formOf(c) != "" }},
			Do: []Next{{"leave_form", RelInterrupt + 2}}}},
	}
}

// leaveFormAction cancels the form the player is in, when allowed says so. It is not a cast, so it neither needs mana nor waits for the
// global cooldown; it holds itself back for druidShiftHold while the aura goes.
func leaveFormAction(allowed func(*Ctx) bool) *Action {
	return &Action{Name: "leave_form", Settle: druidShiftHold,
		Possible: func(c *Ctx) bool {
			if _, ok := c.G.(AuraCanceller); !ok || !c.SelfOK {
				return false
			}
			_, spell := formAuraOf(&c.Self)
			return spell != 0 && allowed(c)
		},
		Do: func(ctx context.Context, c *Ctx) error {
			ac, ok := c.G.(AuraCanceller)
			_, spell := formAuraOf(&c.Self)
			if !ok || spell == 0 {
				return nil
			}
			return ac.CancelAura(ctx, spell)
		}}
}

// ---- druid_buffs ----

// motwAuras are the auras of Mark of the Wild and its group version.
var motwAuras = []string{"Mark of the Wild", "Gift of the Wild"}

// motwTarget is the first of the player and the members in view that lacks Mark of the Wild (the player's own also when it has under
// a minute left, if the server says how long).
func motwTarget(c *Ctx) packet.GUID {
	for _, u := range healMembers(c) {
		if u.GUID == c.Self.GUID {
			if auraMissing(u, motwAuras...) {
				return u.GUID
			}
			continue
		}
		if float64(u.Distance) <= buffReach && !HasAura(u, motwAuras...) && sightOK(c, u) {
			return u.GUID
		}
	}
	return 0
}

// thornsTarget is the tank when it is in view and lacks Thorns (the thorns are for the one who is hit).
func thornsTarget(c *Ctx) packet.GUID {
	t := ValTank.Get(c)
	if t == nil || t.GUID == c.Self.GUID || float64(t.Distance) > buffReach || HasAura(t, "Thorns") || !sightOK(c, t) {
		return 0
	}
	return t.GUID
}

// motwPending says Mark of the Wild is known and missing on someone the druid may buff.
func motwPending(c *Ctx) bool {
	_, ok := c.Spells.Highest("Mark of the Wild")
	return ok && motwTarget(c) != 0
}

// druidBuffs is the upkeep out of combat, at the rest gates (and for a brain started by hand, always): Mark of the Wild on the druid
// and every member in view, and, for a caster, Thorns on the tank. Only in caster form with a third of the mana: the spells are barred
// in every other form (druid_form_* leaves its form for them, druid_caster is never in one).
func druidBuffs(name string, thorns bool) *Strategy {
	ok := func(c *Ctx) bool {
		return restNow(c) && c.Self.PowerType == powerMana && formOf(c) == "" && PowerPct(c) >= 30
	}
	acts := []*Action{{Name: "mark_of_the_wild", Spell: "Mark of the Wild", Target: motwTarget}}
	rules := []Rule{{Trigger: Trigger{Name: "motw_missing", Active: func(c *Ctx) bool { return ok(c) && motwTarget(c) != 0 }},
		Do: []Next{{"mark_of_the_wild", RelNormal + 3}}}}
	if thorns {
		acts = append(acts, &Action{Name: "thorns", Spell: "Thorns", Target: thornsTarget})
		rules = append(rules, Rule{Trigger: Trigger{Name: "thorns_missing", Active: func(c *Ctx) bool { return ok(c) && thornsTarget(c) != 0 }},
			Do: []Next{{"thorns", RelNormal + 2}}})
	}
	return &Strategy{Name: name, Actions: acts, Rules: rules}
}

// ---- druid_innervate ----

const innervateBelow = 20 // Innervate below this mana, in a fight

// druidInnervate is Innervate on the druid itself when a fight has left it dry: the mana regenerates five times as fast for 20 s, and the
// spell is barred in every form.
func druidInnervate() *Strategy {
	return &Strategy{
		Name: "druid_innervate",
		Actions: []*Action{{Name: "innervate", Spell: "Innervate",
			Possible: func(c *Ctx) bool { return c.Self.PowerType == powerMana && formOf(c) == "" },
			Useful:   func(c *Ctx) bool { return !HasAura(&c.Self, "Innervate") }}},
		Rules: []Rule{{Trigger: Trigger{Name: "dry_in_fight", Active: func(c *Ctx) bool {
			return c.SelfOK && c.Self.PowerType == powerMana && c.Self.MaxPower > 0 && PowerPct(c) < innervateBelow && (inCombat(c) || ValFight.Get(c).Mob != nil)
		}}, Do: []Next{{"innervate", RelHigh + 3}}}},
	}
}

// ---- balance ----

const (
	moonfireMinPct   = 35 // Moonfire and the damage over time only on a mob that will live to see them tick
	swarmMinPct      = 40
	starfireMana     = 35 // Starfire (3.5 s, dear) only above this mana
	starfireMinPct   = 30 // and on a mob above this health: Wrath finishes
	rootsHurtPct     = 80 // Entangling Roots on a mob that has picked the druid when it is below this health
	rootsMobDistance = 10.0
)

// druidBalance is the damage rotation of a balance druid in caster form: Moonfire and Insect Swarm (when known) on the fight's mob,
// Starfire as the main nuke when the mana is good and the mob healthy, Wrath as the filler (and when moving only the instants go), and
// Entangling Roots on a mob that has picked the druid, which the druid then steps away from (ranged_position). Hibernate is not used: it
// puts a beast to sleep, and a sleeping mob that wakes pulls the pack. The spells are cast in caster form (druid_caster); Moonkin Form
// would allow them too but bars the heals and Mark of the Wild, so the preset does not take it.
func druidBalance() *Strategy {
	mobPct := func(c *Ctx) int { return mobHealthPct(fightMob(c)) }
	harm := func(name, spell string) *Action {
		return &Action{Name: name, Spell: spell, Harmful: true, Threat: ThreatSingle, Target: fightTarget, Pre: []string{"face_target"}}
	}
	moonfire := harm("moonfire", "Moonfire")
	moonfire.Useful = func(c *Ctx) bool { return !HasAura(fightMob(c), "Moonfire") && mobPct(c) > moonfireMinPct }
	swarm := harm("insect_swarm", "Insect Swarm")
	swarm.Useful = func(c *Ctx) bool { return !HasAura(fightMob(c), "Insect Swarm") && mobPct(c) > swarmMinPct }
	wrath := harm("wrath", "Wrath")
	wrath.Possible = func(c *Ctx) bool { return !c.Self.Moving }
	starfire := harm("starfire", "Starfire")
	starfire.Possible = func(c *Ctx) bool {
		return !c.Self.Moving && PowerPct(c) >= starfireMana && mobPct(c) > starfireMinPct
	}
	starfire.Alt = []string{"wrath"}
	roots := &Action{Name: "entangling_roots", Spell: "Entangling Roots", Harmful: true, Threat: ThreatSingle, Pre: []string{"face_target"},
		Target: func(c *Ctx) packet.GUID {
			if m := ValMobOnMe.Get(c); m != nil {
				return m.GUID
			}
			return 0
		},
		Possible: func(c *Ctx) bool {
			m := ValMobOnMe.Get(c)
			return m != nil && m.Distance >= 0 && m.Distance <= rootsMobDistance && !HasAura(m, "Entangling Roots") && healthPct(c) < rootsHurtPct && !c.Self.Moving
		}}
	return &Strategy{
		Name:    "druid_balance",
		Actions: []*Action{moonfire, swarm, wrath, starfire, roots},
		Rules: []Rule{
			{Trigger: Trigger{Name: "fight_target", Active: func(c *Ctx) bool { return ValFight.Get(c).Mob != nil }},
				Do: []Next{{"moonfire", RelNormal + 5}, {"insect_swarm", RelNormal + 4}, {"starfire", RelNormal + 1}, {"wrath", RelNormal}}},
			{Trigger: Trigger{Name: "mob_on_druid_hurt", Active: func(c *Ctx) bool { return ValMobOnMe.Get(c) != nil && healthPct(c) < rootsHurtPct }},
				Do: []Next{{"entangling_roots", RelHigh + 6}}},
			{Trigger: Trigger{Name: "rooted_mob_close", Active: func(c *Ctx) bool { return canMove(c) && ValRootedMob.Get(c) != nil }},
				Do: []Next{{"step_away", RelMove}}},
		},
	}
}

// ---- the home form of the feral druids ----

// formBoundItems are the actions of other strategies that use an item, which a shapeshifted druid cannot.
var formBoundItems = map[string]bool{"eat": true, "drink": true, "healthstone": true, "health_potion": true, "bandage": true, "mana_potion": true}

const (
	feralDrinkBelow  = 50 // leave the form to drink below this mana, out of combat
	feralBuffMinMana = 60 // and to buff above this: the shift back costs a third of the mana
)

// druidHome keeps a feral druid in its form (the first of homes that the spellbook has). It shifts from caster form when a fight starts,
// or out of combat at a rest gate when nothing else is to be done; it leaves a form that is not its own out of combat; and, out of combat
// at a rest gate, it leaves its own to drink (the mana is low and there is a drink in the bags), to eat (the health is low and there is
// food: items cannot be used in a form) or to buff with Mark of the Wild (the mana is good). In combat it never leaves the form,
// whatever the mana. The shift itself is a cast from caster form, where the power is mana and Affordable can read it.
func druidHome(name string, homes []string) *Strategy {
	isHome := func(c *Ctx) bool { return inForm(c, homes...) }
	needsDrink := func(c *Ctx) bool {
		pct, ok := manaPct(c)
		return ok && pct < feralDrinkBelow && drinkEntry(c, false) != 0
	}
	needsFood := func(c *Ctx) bool { return healthPct(c) < foodEatPct && foodEntry(c, false) != 0 }
	wantForm := func(c *Ctx) bool {
		if !c.SelfOK || formOf(c) != "" {
			return false
		}
		if _, ok := homeFormSpell(c, homes); !ok {
			return false
		}
		if inCombat(c) || ValFight.Get(c).Mob != nil {
			return true
		}
		return c.RestOK && !eating(c) && !motwPending(c) && !needsDrink(c) && !needsFood(c)
	}
	leaveToRest := func(c *Ctx) bool {
		if !restNow(c) || !isHome(c) || eating(c) {
			return false
		}
		pct, ok := manaPct(c)
		return ok && (needsDrink(c) || needsFood(c) || (pct >= feralBuffMinMana && motwPending(c)))
	}
	return &Strategy{
		Name: name,
		Mult: []Multiplier{{Name: "no_items_in_form", F: func(c *Ctx, a *Action, rel float32) float32 {
			// The core refuses every item used while shapeshifted (SPELL_FAILED_NO_ITEMS_WHILE_SHAPESHIFTED, SpellHandler.cpp): food, drink,
			// potions, healthstones, bandages. They wait for caster form, and the druid leaves its form for the meal (leaveToRest).
			if formOf(c) != "" && formBoundItems[a.Name] {
				return 0
			}
			return 1
		}}},
		Actions: []*Action{
			{Name: "enter_form",
				Resolve:  func(c *Ctx) (uint32, bool) { return homeFormSpell(c, homes) },
				Target:   func(c *Ctx) packet.GUID { return c.Self.GUID },
				Possible: func(c *Ctx) bool { return formOf(c) == "" && c.Self.PowerType == powerMana },
				Useful:   func(c *Ctx) bool { return !isHome(c) },
				Do: func(ctx context.Context, c *Ctx) error {
					id, ok := homeFormSpell(c, homes)
					if !ok {
						return nil
					}
					c.mem.holdAction("enter_form", c.Now.Add(druidShiftHold), "shifting") // the form's aura takes a moment to show
					return c.cast(ctx, id, c.Self.GUID)
				}},
			leaveFormAction(func(c *Ctx) bool {
				f := formOf(c)
				if isHome(c) {
					return leaveToRest(c)
				}
				return f != "" && !inCombat(c) // a form that is not the home form goes out of combat, then the home form comes
			}),
		},
		Rules: []Rule{
			{Trigger: Trigger{Name: "caster_form_wanted", Active: wantForm}, Do: []Next{{"enter_form", RelHigh + 2}}},
			{Trigger: Trigger{Name: "wrong_form", Active: func(c *Ctx) bool { return c.SelfOK && formOf(c) != "" && !isHome(c) && !inCombat(c) }},
				Do: []Next{{"leave_form", RelNormal + 4}}},
			{Trigger: Trigger{Name: "leave_form_to_rest", Active: leaveToRest}, Do: []Next{{"leave_form", RelNormal + 4}}},
		},
	}
}

// ---- the cat ----

const (
	catRipCP        = 4  // Rip from this many combo points ...
	catRipMinPct    = 40 // ... on a mob above this health: the bleed must run its course
	catBiteCP       = 5  // Ferocious Bite from this many combo points ...
	catBiteFinishCP = 3  // ... or from this many on a mob that is nearly dead
	catBiteFinish   = 35
	catRakeMinPct   = 50 // Rake (a bleed) on a mob above this health
	catFuryEnergy   = 60 // Tiger's Fury with this much energy to spare
	catFuryMinPct   = 40
)

func catAction(name, spell string, possible, useful func(*Ctx) bool) *Action {
	return &Action{Name: name, Spell: spell, Harmful: true, Threat: ThreatSingle, Target: fightTarget, Pre: []string{"face_target"},
		Possible: func(c *Ctx) bool {
			return inForm(c, druidAbilityForms[spell]...) && mobInReach(c) && (possible == nil || possible(c))
		},
		Useful: useful}
}

// druidCat is the cat's rotation in Cat Form (energy): Faerie Fire (Feral) on a new mob (it costs nothing), Tiger's Fury with energy to
// spare, Rake while the mob lives, Shred from behind (Claw otherwise) to build combo points, and Rip or Ferocious Bite to finish with
// them. Shred is cast only from behind the mob, where melee_position stands the cat; Prowl and Pounce are not used. The abilities work
// in Cat Form only, so none is possible in any other form.
func druidCat() *Strategy {
	mobPct := func(c *Ctx) int { return mobHealthPct(fightMob(c)) }
	cp := func(c *Ctx) int { return comboPoints(c, fightMob(c)) }
	energy := func(c *Ctx) int { return PowerPct(c) }
	faerie := catAction("faerie_fire_feral", "Faerie Fire (Feral)", nil, func(c *Ctx) bool {
		return !HasAura(fightMob(c), "Faerie Fire (Feral)") && mobPct(c) > catRakeMinPct
	})
	fury := &Action{Name: "tigers_fury", Spell: "Tiger's Fury",
		Possible: func(c *Ctx) bool {
			return inForm(c, druidAbilityForms["Tiger's Fury"]...) && mobInReach(c) && energy(c) >= catFuryEnergy && mobPct(c) > catFuryMinPct
		},
		Useful: func(c *Ctx) bool { return !HasAura(&c.Self, "Tiger's Fury") }}
	rake := catAction("rake", "Rake", nil, func(c *Ctx) bool {
		return !HasAura(fightMob(c), "Rake") && mobPct(c) > catRakeMinPct && cp(c) < catBiteCP
	})
	shred := catAction("shred", "Shred", func(c *Ctx) bool { return behindMob(c, fightMob(c)) }, func(c *Ctx) bool { return cp(c) < catBiteCP })
	shred.Alt = []string{"claw"}
	claw := catAction("claw", "Claw", nil, func(c *Ctx) bool { return cp(c) < catBiteCP })
	rip := catAction("rip", "Rip", func(c *Ctx) bool { return cp(c) >= catRipCP }, func(c *Ctx) bool {
		return !HasAura(fightMob(c), "Rip") && mobPct(c) > catRipMinPct
	})
	bite := catAction("ferocious_bite", "Ferocious Bite", func(c *Ctx) bool {
		return cp(c) >= catBiteCP || (cp(c) >= catBiteFinishCP && mobPct(c) <= catBiteFinish)
	}, nil)
	return &Strategy{
		Name:    "druid_cat",
		Actions: []*Action{swingAction("auto_attack", fightMob), faerie, fury, rake, shred, claw, rip, bite},
		Rules: []Rule{
			{Trigger: Trigger{Name: "mob_in_reach", Active: mobInReach}, Do: []Next{{"auto_attack", RelNormal + 9}}},
			{Trigger: Trigger{Name: "cat_fight", Active: func(c *Ctx) bool { return inForm(c, formCat) && mobInReach(c) }},
				Do: []Next{{"rip", RelNormal + 8}, {"ferocious_bite", RelNormal + 7}, {"faerie_fire_feral", RelNormal + 6},
					{"tigers_fury", RelNormal + 5}, {"rake", RelNormal + 4}, {"shred", RelNormal + 3}, {"claw", RelNormal + 2}}},
		},
	}
}

// ---- the bear ----

const (
	bearMaulRage    = 400 // Maul with the spare rage, in tenths (it costs 150)
	bearEnrageRage  = 100 // Enrage below this rage ...
	bearEnrageHP    = 50  // ... above this health
	bearRegenHP     = 35  // Frenzied Regeneration below this health ...
	bearRegenRage   = 100 // ... with this much rage to turn into health
	bearRoarMinMobs = 2
)

func bearAction(name, spell string, threat ThreatKind, possible, useful func(*Ctx) bool) *Action {
	return &Action{Name: name, Spell: spell, Harmful: true, Threat: threat, Target: fightTarget, Pre: []string{"face_target"},
		Possible: func(c *Ctx) bool {
			return inForm(c, druidAbilityForms[spell]...) && (possible == nil || possible(c))
		},
		Useful: useful}
}

// druidBear is the bear tank's kit in (Dire) Bear Form (rage): Swipe and Demoralizing Roar with two or more mobs on it, Maul with the spare
// rage, Enrage when the rage is gone at the start of a fight, and Frenzied Regeneration as the emergency. The taunt (Growl) and the choice
// of the mob are role_tank's, the facing of the mob melee_position's.
func druidBear() *Strategy {
	maul := nextSwing("maul", "Maul", ThreatSingle)
	maulPossible := maul.Possible
	maul.Possible = func(c *Ctx) bool {
		return inForm(c, druidAbilityForms["Maul"]...) && (maulPossible == nil || maulPossible(c))
	}
	swipe := bearAction("swipe", "Swipe", ThreatAoE, func(c *Ctx) bool { return ValMeleeMobs.Get(c) >= 2 }, nil)
	roar := bearAction("demoralizing_roar", "Demoralizing Roar", ThreatAoE, func(c *Ctx) bool {
		return ValMeleeMobs.Get(c) >= bearRoarMinMobs && mobsLacking(c, "Demoralizing Roar") > 0
	}, nil)
	enrage := &Action{Name: "enrage", Spell: "Enrage",
		Possible: func(c *Ctx) bool {
			return inForm(c, druidAbilityForms["Enrage"]...) && mobInReach(c) && int(rageNow(c)) < bearEnrageRage && healthPct(c) > bearEnrageHP
		},
		Useful: func(c *Ctx) bool { return !HasAura(&c.Self, "Enrage") }}
	regen := &Action{Name: "frenzied_regeneration", Spell: "Frenzied Regeneration",
		Possible: func(c *Ctx) bool {
			return inForm(c, druidAbilityForms["Frenzied Regeneration"]...) && int(rageNow(c)) >= bearRegenRage
		},
		Useful: func(c *Ctx) bool { return !HasAura(&c.Self, "Frenzied Regeneration") }}
	return &Strategy{
		Name:    "druid_bear",
		Actions: []*Action{swingAction("auto_attack", fightMob), maul, swipe, roar, enrage, regen},
		Rules: []Rule{
			{Trigger: Trigger{Name: "mob_in_reach", Active: mobInReach}, Do: []Next{{"auto_attack", RelNormal + 9}}},
			{Trigger: Trigger{Name: "rage_dump", Active: func(c *Ctx) bool { return mobInReach(c) && int(rageNow(c)) >= bearMaulRage }},
				Do: []Next{{"maul", RelNormal}}},
			{Trigger: Trigger{Name: "pack_on_bear", Active: func(c *Ctx) bool { return mobInReach(c) && ValMeleeMobs.Get(c) >= 2 }},
				Do: []Next{{"swipe", RelNormal + 5}, {"demoralizing_roar", RelNormal + 3}}},
			{Trigger: Trigger{Name: "no_rage", Active: func(c *Ctx) bool { return mobInReach(c) }}, Do: []Next{{"enrage", RelNormal + 2}}},
			{Trigger: Trigger{Name: "bear_hurt", Active: func(c *Ctx) bool { return inCombat(c) && healthPct(c) < bearRegenHP }},
				Do: []Next{{"frenzied_regeneration", relLifeSaver}}},
		},
	}
}
