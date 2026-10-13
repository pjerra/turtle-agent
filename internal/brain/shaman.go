package brain

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/spells"
)

// The shaman, the sixth class of the brain (issue #80 step 4c): a healer (resto_shaman), a caster (elemental_shaman) and a melee fighter
// (enhancement_shaman). The rotations are the harvest of mod-playerbots' shaman behaviour (docs: PLAYERBOTS-HARVEST, Shaman table; ideas
// and numbers only, GPL-2.0+), with the spell names, levels, costs, ranges and totem slots checked against the core's spell data in
// TestShamanSpellData. Spells are named and the brain casts the highest known rank.
//
// Totems. A totem is a self cast that summons a creature into the slot of its element (fire, earth, water, air: the spell's effects
// 87 to 90, SPELL_EFFECT_SUMMON_TOTEM_SLOT1 to 4) and unsummons the one that stood there; the spell needs the element's totem tool in the
// bags (the core's Spell::CheckItems: Earth Totem 5175, Flame Totem 5176, Water Totem 5177, Air Totem 5178). The 1.12 shaman has no
// Totemic Recall, so "recalling" a totem is dropping a new one. The agent cannot see whose totem is whose, so the totem manager keeps its
// own record of each slot: the spell, the place and the time of the drop, and the lifetime the spell's data gives. A totem is dropped
// when a fight is on near the tank and the slot is empty, expired, farther than totemFar from the player (the party has moved on), or
// its creature is gone from view; never twice in totemGap, and never for a mob that is nearly dead.
//
// The shaman never turns into a Ghost Wolf: the form slows the casts, bars the heals and an agent in a dungeon has no use for it.

// totemFar is how far from the player a totem may stand and still count: the auras of the totems reach about 20 yd, and the group
// stands within 30 of the player.
const (
	totemFar      = 30.0
	totemGap      = 10 * time.Second // least time between two drops into one slot
	totemGrace    = 3 * time.Second  // a new totem is given this long to show in view
	totemSight    = 35.0             // a place this near the player is in view: a totem that is not there is gone
	totemAt       = 8.0              // a totem creature within this many yards of the drop is ours
	totemMinMana  = 25               // no totems with less mana (percent)
	totemMinMob   = 25               // no totems for a mob under this health (percent)
	totemDefLife  = 60 * time.Second // lifetime when the spell data has none
	imbueRefresh  = 5 * time.Minute  // an imbue is renewed this long before it ends
	shamanShieldM = 50               // Lightning Shield in combat above this mana (percent)
)

// Elements, in the order of the memory array.
const (
	elFire = iota
	elEarth
	elWater
	elAir
	elements
)

var elementNames = [elements]string{"fire", "earth", "water", "air"}

// totemTools are the totem items each element's spells need (Spell.cpp CheckItems; item_template 5175 to 5178).
var totemTools = [elements]uint32{5176, 5175, 5177, 5178}

// totemNoTool are the totem spells that need no totem item (their totem1 and totem2 are empty in spell_template; pinned by
// TestTotemSpellsFollowTheCoreData): the spellbook of a player who knows only these drops them without the tool.
var totemNoTool = map[uint32]bool{24854: true, 27621: true}

// totemOrder is the order slots are filled in: the damage first, then the buffs, the mana last.
var totemOrder = [elements]int{elFire, elAir, elWater, elEarth}

// totemElement is the slot of the totem a spell summons: effects 87 (fire), 88 (earth), 89 (water) and 90 (air) are the slot effects.
func totemElement(m spells.Meta) (int, bool) {
	for _, e := range m.Effects {
		switch e.Effect {
		case 87:
			return elFire, true
		case 88:
			return elEarth, true
		case 89:
			return elWater, true
		case 90:
			return elAir, true
		}
	}
	return 0, false
}

// totemPrefs are the totems each preset drops, per element, best first: the first of a list that the spellbook has is the one used.
type totemPrefs [elements][]string

var (
	// A healer drops the water totem that feeds its mana (Mana Spring, else Healing Stream, which heals the group a little) and
	// Stoneskin for the group's armor.
	restoTotems = totemPrefs{elWater: {"Mana Spring Totem", "Healing Stream Totem"}, elEarth: {"Stoneskin Totem"}}
	// An elemental shaman drops Searing Totem (it fights too) and Mana Spring Totem.
	elementalTotems = totemPrefs{elFire: {"Searing Totem"}, elWater: {"Mana Spring Totem"}}
	// An enhancement shaman drops Searing (it fights too), Windfury (or Grace of Air) and Strength of Earth for the melee.
	enhancementTotems = totemPrefs{elFire: {"Searing Totem"}, elAir: {"Windfury Totem", "Grace of Air Totem"}, elEarth: {"Strength of Earth Totem"}}
)

// totemRec is what the manager remembers of a slot.
type totemRec struct {
	spell uint32
	name  string // the spell's name; the totem creature's name holds it (totemNamed)
	at    time.Time
	pos   Vec3
	life  time.Duration
}

// shamMem is the shaman's memory between ticks.
type shamMem struct {
	mu         sync.Mutex
	totems     [elements]totemRec
	imbueAt    time.Time // the last imbue that went out
	imbueSpell uint32
	imbueTry   time.Time // an imbue that was refused: the next try waits for imbueRetry
	failSeen   uint64
	failInit   bool
}

const imbueRetry = 60 * time.Second

func (s *shamMem) totem(el int) totemRec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.totems[el]
}

func (s *shamMem) setTotem(el int, r totemRec) {
	s.mu.Lock()
	s.totems[el] = r
	s.mu.Unlock()
}

// ---- the heal kit ----

// shamanHeals is what a shaman heals and cures with. Healing Wave (10 ranks, level 1 to 60) is the main heal and Lesser Healing Wave (6,
// level 20 to 60) the quick and dear one; Chain Heal (3, level 40 to 54) is used while two or more members are hurt. A shaman has no heal
// over time: the tank gets the direct heals only. Cure Poison and Cure Disease are the dispels.
var shamanHeals = HealKit{
	Direct: []string{"Healing Wave", "Lesser Healing Wave"},
	Fast:   []string{"Lesser Healing Wave"},
	Multi:  []string{"Chain Heal"},
	Dispels: []DispelSpell{
		{Spell: "Cure Poison", Types: []uint8{dispelPoison}},
		{Spell: "Cure Disease", Types: []uint8{dispelDisease}},
	},
}

func init() {
	strategyBuilders["shaman_shield"] = shamanShield
	strategyBuilders["shaman_imbue"] = shamanImbue
	strategyBuilders["shaman_totems_resto"] = func() *Strategy { return shamanTotems("shaman_totems_resto", restoTotems, RelNormal-3) }
	strategyBuilders["shaman_totems_elemental"] = func() *Strategy { return shamanTotems("shaman_totems_elemental", elementalTotems, RelNormal+1) }
	strategyBuilders["shaman_totems_enhancement"] = func() *Strategy {
		return shamanTotems("shaman_totems_enhancement", enhancementTotems, RelNormal+1)
	}
	strategyBuilders["shaman_elemental"] = shamanElemental
	strategyBuilders["shaman_enhancement"] = shamanEnhancement
	presets["resto_shaman"] = []string{"role_healer", "shaman_shield", "shaman_totems_resto", "healer_position", "avoid_ground",
		"combat_consumables", "rest_eat", "rest_drink"}
	presets["elemental_shaman"] = []string{"combat_dps", "shaman_elemental", "shaman_shield", "shaman_totems_elemental", "ranged_position",
		"avoid_ground", "class_interrupt", "combat_consumables", "rest_eat", "rest_drink"}
	presets["enhancement_shaman"] = []string{"combat_dps", "shaman_enhancement", "shaman_shield", "shaman_imbue", "shaman_totems_enhancement",
		"melee_position", "avoid_ground", "class_interrupt", "combat_consumables", "rest_eat", "rest_drink"}
	// Refusals the shaman and the druid meet that back a spell off for longer than the default second: a totem tool or a weapon that is not
	// there stays missing, a shapeshift form does not change by itself, and Entangling Roots stays barred indoors.
	for k, v := range map[string]time.Duration{
		"item_gone":       60 * time.Second,
		"totems":          60 * time.Second,
		"not_shapeshift":  3 * time.Second,
		"only_shapeshift": 3 * time.Second,
		"only_outdoors":   30 * time.Second,
		"outdoors_only":   30 * time.Second,
	} {
		holdFor[k] = v
	}
}

// ---- Lightning Shield and Water Shield ----

// shieldFor is the shield the shaman keeps: a healer takes Water Shield (mana back from every hit) when it has it, anyone else Lightning
// Shield (the damage to whoever hits it).
func shieldFor(c *Ctx) string {
	if c.P.Role == RoleHealer {
		if _, ok := c.Spells.Highest("Water Shield"); ok {
			return "Water Shield"
		}
	}
	if _, ok := c.Spells.Highest("Lightning Shield"); ok {
		return "Lightning Shield"
	}
	return ""
}

// shamanShield keeps the shield up: out of combat at a rest gate with a quarter of the mana, and in a fight when it has gone and the mana
// is good. The aura goes when its charges are used up.
func shamanShield() *Strategy {
	missing := func(c *Ctx) bool {
		n := shieldFor(c)
		return n != "" && auraMissing(&c.Self, n)
	}
	return &Strategy{
		Name: "shaman_shield",
		Actions: []*Action{{Name: "shield",
			Resolve: spellByName(shieldFor),
			Useful:  missing}},
		Rules: []Rule{
			{Trigger: Trigger{Name: "shield_missing_ooc", Active: func(c *Ctx) bool {
				return restNow(c) && c.Self.PowerType == powerMana && PowerPct(c) >= 25 && missing(c)
			}}, Do: []Next{{"shield", RelNormal + 3}}},
			{Trigger: Trigger{Name: "shield_missing_in_combat", Active: func(c *Ctx) bool {
				return c.SelfOK && c.Self.PowerType == powerMana && inCombat(c) && PowerPct(c) >= shamanShieldM && missing(c)
			}}, Do: []Next{{"shield", RelNormal - 2}}},
		},
	}
}

// ---- totems ----

// haveItem says the bags hold the item.
func haveItem(c *Ctx, entry uint32) bool {
	for _, it := range c.G.Items() {
		if it.Entry == entry {
			return true
		}
	}
	return false
}

// totemSpellFor is the highest rank of the named totem that the spellbook has and that summons a totem into the slot (the spellbook of
// some totems also holds the aura spells that the totem casts, under the same name).
func totemSpellFor(c *Ctx, name string, el int) (uint32, bool) {
	ranks := c.Spells.Ranks(name) // lowest first
	for i := len(ranks) - 1; i >= 0; i-- {
		if m, ok := spells.Lookup(ranks[i]); ok {
			if got, isTotem := totemElement(m); isTotem && got == el {
				return ranks[i], true
			}
		}
	}
	return 0, false
}

// totemChoice is the totem the preset wants in the slot: the first name of the preference list that the spellbook has.
func totemChoice(c *Ctx, prefs totemPrefs, el int) (id uint32, name string) {
	for _, n := range prefs[el] {
		if id, ok := totemSpellFor(c, n, el); ok {
			return id, n
		}
	}
	return 0, ""
}

// totemLive says the slot holds a totem that still counts: not expired, not left farther than totemFar behind, and, when its place is in
// view, with a creature of its name standing there.
func totemLive(c *Ctx, el int) bool {
	r := c.mem.sham.totem(el)
	if r.spell == 0 || c.Now.Sub(r.at) >= r.life || dist2(r.pos, selfPos(c)) > totemFar {
		return false
	}
	if c.Now.Sub(r.at) < totemGrace || dist2(r.pos, selfPos(c)) > totemSight {
		return true
	}
	for i := range c.Units {
		u := &c.Units[i]
		if u.Type == "creature" && !u.Hostile && !u.Dead && dist2(unitPos(u), r.pos) <= totemAt && totemNamed(u.Name, r.name) {
			return true
		}
	}
	return false
}

// totemNamed says a creature's name may be that of the totem a spell summons: it holds the spell's name without "Totem" (the creature is
// "Searing Totem V" or the like) and "Totem" itself, or it is not known yet (the name query has not been answered): an unknown name is
// given the benefit of the doubt, since a wrong "it is gone" drops the totem again and again.
func totemNamed(creature, spell string) bool {
	if creature == "" {
		return true
	}
	stem := strings.TrimSpace(strings.TrimSuffix(spell, "Totem"))
	return strings.Contains(creature, "Totem") && strings.Contains(creature, stem)
}

// totemPlan is the totem to drop now.
type totemPlan struct {
	Element int
	Spell   uint32
	Name    string
}

// totemWanted finds the slot to fill now, if any: a fight is on near the tank (or the shaman is alone), the mob is not nearly dead, the mana
// is not short, the slot is empty, expired, left behind or its totem is gone, and its last drop is no recent. The totem tool is in the bags.
func totemWanted(c *Ctx, prefs totemPrefs) (totemPlan, bool) {
	if !c.SelfOK || c.Self.PowerType != powerMana || !(inCombat(c) || ValFight.Get(c).Mob != nil) || PowerPct(c) < totemMinMana {
		return totemPlan{}, false
	}
	if m := fightMob(c); m != nil && mobHealthPct(m) < totemMinMob {
		return totemPlan{}, false
	}
	if t := findTank(c); t != nil && float64(t.Distance2D) > totemFar {
		return totemPlan{}, false
	}
	for _, el := range totemOrder {
		id, name := totemChoice(c, prefs, el)
		if id == 0 || !(totemNoTool[id] || haveItem(c, totemTools[el])) {
			continue
		}
		if r := c.mem.sham.totem(el); r.spell != 0 && c.Now.Sub(r.at) < totemGap {
			continue
		}
		if totemLive(c, el) {
			continue
		}
		return totemPlan{Element: el, Spell: id, Name: name}, true
	}
	return totemPlan{}, false
}

// shamanTotems is the totem manager of a preset: it drops the preset's totems (see the package comment) at the relevance rel.
func shamanTotems(name string, prefs totemPrefs, rel float32) *Strategy {
	plan := func(c *Ctx) (totemPlan, bool) { return totemWanted(c, prefs) }
	return &Strategy{
		Name: name,
		Actions: []*Action{{Name: "drop_totem",
			Resolve: func(c *Ctx) (uint32, bool) { p, ok := plan(c); return p.Spell, ok },
			Target:  func(c *Ctx) packet.GUID { return c.Self.GUID },
			Do: func(ctx context.Context, c *Ctx) error {
				p, ok := plan(c)
				if !ok {
					return nil
				}
				life := totemDefLife
				if m, found := spells.Lookup(p.Spell); found && m.DurationMS > 0 {
					life = time.Duration(m.DurationMS) * time.Millisecond
				}
				c.mem.sham.setTotem(p.Element, totemRec{spell: p.Spell, name: p.Name, at: c.Now, pos: selfPos(c), life: life})
				return c.cast(ctx, p.Spell, c.Self.GUID)
			}}},
		Rules: []Rule{{Trigger: Trigger{Name: "totem_wanted", Active: func(c *Ctx) bool { _, ok := plan(c); return ok }},
			Do: []Next{{"drop_totem", rel}}}},
	}
}

// ---- weapon imbue ----

// WeaponImbuer is the optional half of the Game that casts an enchanting spell at the weapon in the main hand (CMSG_CAST_SPELL with an
// item target): a shaman's weapon imbue. A game without it never imbues.
type WeaponImbuer interface {
	CastOnMainHand(ctx context.Context, spell uint32) error
}

// imbueNames are the weapon imbues of the enhancement preset, best first: Windfury Weapon (level 30) for the extra attacks, Flametongue
// Weapon (level 10) for the damage on a slow weapon, Rockbiter Weapon (level 1) before that.
var imbueNames = []string{"Windfury Weapon", "Flametongue Weapon", "Rockbiter Weapon"}

// imbueSpellNow is the highest rank of the best imbue the spellbook has, with the time it lasts (the enchant effect's base points plus
// one, in seconds: 3600 in this data).
func imbueSpellNow(c *Ctx) (id uint32, life time.Duration, ok bool) {
	for _, n := range imbueNames {
		ranks := c.Spells.Ranks(n)
		for i := len(ranks) - 1; i >= 0; i-- {
			m, found := spells.Lookup(ranks[i])
			if !found {
				continue
			}
			for _, e := range m.Effects {
				if e.Effect == 54 { // SPELL_EFFECT_ENCHANT_ITEM_TEMPORARY
					return ranks[i], time.Duration(e.Base+1) * time.Second, true
				}
			}
		}
	}
	return 0, 0, false
}

// imbueDue says the weapon has no imbue the brain knows of, or it is about to end.
func imbueDue(c *Ctx) bool {
	_, life, ok := imbueSpellNow(c)
	if !ok {
		return false
	}
	s := &c.mem.sham
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.Now.Before(s.imbueTry) {
		return false
	}
	return s.imbueAt.IsZero() || c.Now.Sub(s.imbueAt) >= life-imbueRefresh
}

// noteImbueRefusal forgets an imbue the server refused (no weapon in the hand), so that it is tried again after imbueRetry.
func noteImbueRefusal(c *Ctx) {
	cs := c.Spells.CastState()
	s := &c.mem.sham
	s.mu.Lock()
	defer s.mu.Unlock()
	first := !s.failInit
	s.failInit = true
	fresh := cs.Failures != s.failSeen
	s.failSeen = cs.Failures
	if first || !fresh || cs.LastFailure == nil || s.imbueSpell == 0 || cs.LastFailure.SpellID != s.imbueSpell {
		return
	}
	s.imbueAt = time.Time{}
	s.imbueTry = c.Now.Add(imbueRetry)
}

// shamanImbue puts a weapon imbue on the main-hand weapon out of combat, at a rest gate: the brain cannot read the enchantment off the
// weapon, so it remembers when it imbued and renews the imbue five minutes before it ends (an hour in this data), once at the start of a
// run. The off hand is left alone.
func shamanImbue() *Strategy {
	return &Strategy{
		Name:   "shaman_imbue",
		Before: noteImbueRefusal,
		Actions: []*Action{{Name: "imbue",
			Resolve: func(c *Ctx) (uint32, bool) { id, _, ok := imbueSpellNow(c); return id, ok },
			Possible: func(c *Ctx) bool {
				_, ok := c.G.(WeaponImbuer)
				return ok
			},
			Useful: imbueDue,
			Do: func(ctx context.Context, c *Ctx) error {
				id, _, ok := imbueSpellNow(c)
				w, can := c.G.(WeaponImbuer)
				if !ok || !can {
					return errors.New("no weapon imbue to cast")
				}
				s := &c.mem.sham
				s.mu.Lock()
				s.imbueSpell, s.imbueAt = id, c.Now
				s.mu.Unlock()
				c.mem.mu.Lock()
				c.mem.lastCastTarget = 0
				c.mem.mu.Unlock()
				if err := w.CastOnMainHand(ctx, id); err != nil {
					s.mu.Lock()
					s.imbueAt, s.imbueTry = time.Time{}, c.Now.Add(imbueRetry)
					s.mu.Unlock()
					return err
				}
				return nil
			}}},
		Rules: []Rule{{Trigger: Trigger{Name: "weapon_unimbued", Active: func(c *Ctx) bool {
			return restNow(c) && c.Self.PowerType == powerMana && PowerPct(c) >= 30 && imbueDue(c)
		}}, Do: []Next{{"imbue", RelNormal + 2}}}},
	}
}

// ---- the elemental shaman ----

const (
	flameShockMinPct = 40 // Flame Shock on a mob that will live to see it tick
	flameShockMana   = 30
	chainMobs        = 2 // Chain Lightning with this many mobs round the fight's mob
	chainRadius      = 12.0
)

// shamanElemental is the damage rotation of an elemental shaman: Flame Shock on the fight's mob, Chain Lightning when two or more mobs
// stand together (an area spell, so combat_dps's veto holds it back while a mob not yet on the tank is near), Lightning Bolt as the filler;
// the bolts are not cast on the move, Flame Shock is. Earth Shock is the interrupt (class_interrupt), the totems are the manager's.
func shamanElemental() *Strategy {
	harm := func(name, spell string) *Action {
		return &Action{Name: name, Spell: spell, Harmful: true, Threat: ThreatSingle, Target: fightTarget, Pre: []string{"face_target"}}
	}
	flame := harm("flame_shock", "Flame Shock")
	flame.Useful = func(c *Ctx) bool {
		return !HasAura(fightMob(c), "Flame Shock") && mobHealthPct(fightMob(c)) > flameShockMinPct && PowerPct(c) >= flameShockMana
	}
	bolt := harm("lightning_bolt", "Lightning Bolt")
	bolt.Possible = func(c *Ctx) bool { return !c.Self.Moving }
	chain := harm("chain_lightning", "Chain Lightning")
	chain.Threat = ThreatAoE
	chain.Alt = []string{"lightning_bolt"}
	chain.Possible = func(c *Ctx) bool { return !c.Self.Moving && mobsNear(c, fightMob(c), chainRadius) >= chainMobs }
	return &Strategy{
		Name:    "shaman_elemental",
		Actions: []*Action{flame, bolt, chain},
		Rules: []Rule{
			{Trigger: Trigger{Name: "fight_target", Active: func(c *Ctx) bool { return ValFight.Get(c).Mob != nil }},
				Do: []Next{{"flame_shock", RelNormal + 5}, {"lightning_bolt", RelNormal}}},
			{Trigger: Trigger{Name: "mobs_together", Active: func(c *Ctx) bool { return mobsNear(c, fightMob(c), chainRadius) >= chainMobs }},
				Do: []Next{{"chain_lightning", RelNormal + 3}}},
		},
	}
}

// ---- the enhancement shaman ----

const (
	enhShockMana   = 60 // Earth Shock as a filler only above this mana
	enhFlameMinPct = 50
)

// shamanEnhancement is the melee rotation of an enhancement shaman: the auto attack, Stormstrike (level 30, a talent in other data: only
// when the spellbook has it) on cooldown, Flame Shock on a healthy mob, Earth Shock for the spare mana. The shaman stands behind the mob
// (melee_position), the imbue and the totems are the manager's, Earth Shock as an interrupt is class_interrupt's.
func shamanEnhancement() *Strategy {
	melee := func(name, spell string, possible, useful func(*Ctx) bool) *Action {
		return &Action{Name: name, Spell: spell, Harmful: true, Threat: ThreatSingle, Target: fightTarget, Pre: []string{"face_target"},
			Possible: func(c *Ctx) bool { return mobInReach(c) && (possible == nil || possible(c)) }, Useful: useful}
	}
	return &Strategy{
		Name: "shaman_enhancement",
		Actions: []*Action{
			swingAction("auto_attack", fightMob),
			melee("stormstrike", "Stormstrike", nil, nil),
			melee("flame_shock", "Flame Shock", func(c *Ctx) bool { return PowerPct(c) >= flameShockMana }, func(c *Ctx) bool {
				return !HasAura(fightMob(c), "Flame Shock") && mobHealthPct(fightMob(c)) > enhFlameMinPct
			}),
			melee("earth_shock", "Earth Shock", func(c *Ctx) bool { return PowerPct(c) >= enhShockMana }, nil),
		},
		Rules: []Rule{
			{Trigger: Trigger{Name: "mob_in_reach", Active: mobInReach}, Do: []Next{{"auto_attack", RelNormal + 9}}},
			{Trigger: Trigger{Name: "enh_fight", Active: mobInReach},
				Do: []Next{{"stormstrike", RelNormal + 7}, {"flame_shock", RelNormal + 5}, {"earth_shock", RelNormal + 3}}},
		},
	}
}
