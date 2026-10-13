package brain

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/spells"
)

// The spells of the druid tests, with their template numbers (internal/spells/meta.tsv.gz; TestDruidSpellData checks the facts the brain
// relies on).
const (
	drCat, drBear, drDire, drTree, drTravel, drAquatic, drMoonkinForm = 768, 5487, 9634, 775, 783, 1066, 24858
	drClaw, drShred, drRake, drRip, drBite, drFury, drFFF             = 1082, 5221, 1822, 1079, 22557, 5217, 16857
	drMaul, drSwipe, drRoar, drGrowl, drEnrage, drFrenzied, drBash    = 6807, 779, 99, 6795, 5229, 22842, 5211
	drHT1, drHT4, drHT5, drHT6, drHT7                                 = 5185, 5188, 5189, 6778, 8903
	drRegrowth1, drRegrowth5                                          = 8936, 8941
	drRejuv1, drRejuv7                                                = 774, 8910
	drRemoveCurse, drAbolish, drCurePoison, drInnervate               = 2782, 2893, 8946, 29166
	drMotw1, drMotw5, drThorns4                                       = 1126, 8907, 8914
	drMoonfire, drWrath, drStarfire, drSwarm, drRoots, drHibernate    = 8921, 5176, 2912, 5570, 339, 2637
	drWrath6                                                          = 6780
	itemSpringWater                                                   = 159
)

var (
	drCasterForms = []uint32{drCat, drBear, drDire, drTree, drTravel, drAquatic}
	drRestoKnown  = []uint32{drHT1, 5186, 5187, drHT4, drHT5, drHT6, drHT7, drRegrowth1, 8938, 8939, 8940, drRegrowth5, drRejuv1, 1058, 1430, 2090, 2091, 3627, drRejuv7,
		drRemoveCurse, drAbolish, drCurePoison, drInnervate, drMotw1, 5232, 6756, 5234, drMotw5, 467, 782, 1075, drThorns4}
	drBalanceKnown = []uint32{drMoonfire, drWrath, 5177, 5178, 5179, 5180, 6780, drStarfire, 8949, 8950, drSwarm, drRoots, drHibernate, drMotw5, drThorns4, drInnervate}
	drCatKnown     = []uint32{drCat, drClaw, 3029, drShred, drRake, drRip, drBite, drFury, drFFF, drMotw5, drBear, drDire}
	drBearKnown    = []uint32{drBear, drDire, drMaul, drSwipe, drRoar, drGrowl, drEnrage, drFrenzied, drBash, drMotw5}
)

// standaloneClass are the strategies of the druid and the shaman that build without another strategy (the others use face_target or
// step_away of the role and the positioning).
var standaloneClass = map[string]bool{"druid_buffs": true, "druid_buffs_feral": true, "druid_caster": true, "druid_form_bear": true, "druid_form_cat": true,
	"druid_innervate": true, "shaman_imbue": true, "shaman_shield": true, "shaman_totems_resto": true, "shaman_totems_elemental": true,
	"shaman_totems_enhancement": true}

// classOps are the optional halves of the Game that the druid and the shaman use, for the fakes: the cancel of an aura (a form goes at
// once, as the server's update would show) and the cast at the weapon in the main hand.
type classOps struct {
	mu        sync.Mutex
	g         *fakeGame
	cancels   []uint32
	imbues    []uint32
	cancelErr error
	imbueErr  error
	keep      bool // the aura stays: a server slow to answer
}

func (o *classOps) CancelAura(ctx context.Context, spell uint32) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.cancelErr != nil {
		return o.cancelErr
	}
	o.cancels = append(o.cancels, spell)
	if o.keep {
		return nil
	}
	o.g.mu.Lock()
	defer o.g.mu.Unlock()
	var keep []objects.Aura
	for _, a := range o.g.self.Auras {
		if a.SpellID == spell {
			// Leaving a form puts the mana back in the power.
			if o.g.self.MaxMana > 0 {
				o.g.self.PowerType, o.g.self.Power, o.g.self.MaxPower = 0, o.g.self.Mana, o.g.self.MaxMana
				o.g.self.Mana, o.g.self.MaxMana = 0, 0
			}
			continue
		}
		keep = append(keep, a)
	}
	o.g.self.Auras = keep
	return nil
}

func (o *classOps) CastOnMainHand(ctx context.Context, spell uint32) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.imbueErr != nil {
		return o.imbueErr
	}
	o.imbues = append(o.imbues, spell)
	return nil
}

func (o *classOps) cancelList() []uint32 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]uint32{}, o.cancels...)
}

func (o *classOps) imbueList() []uint32 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]uint32{}, o.imbues...)
}

// casterGame is a fake that cannot walk: the healer and the caster stand where they are.
type casterGame struct {
	*fakeGame
	*classOps
}

// meleeGame is a fake that walks, sees and follows the swings.
type meleeGame struct {
	*meleeFake
	*classOps
}

func newCasterGame(known ...uint32) *casterGame {
	g := newFake(known...)
	return &casterGame{fakeGame: g, classOps: &classOps{g: g}}
}

func newMeleeGame(g *fakeGame) *meleeGame {
	mf := &meleeFake{posFake: &posFake{fakeGame: g}, dodged: map[packet.GUID]time.Time{}}
	return &meleeGame{meleeFake: mf, classOps: &classOps{g: g}}
}

func rigOn(t *testing.T, game Game, g *fakeGame, p Params) *rig {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Now = g.clk.Now
	b, err := New(game, cfg, p)
	if err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, g: g, b: b}
}

// druidSelf makes the fake's player a level 40 druid in caster form with a full pool of 1000 mana and 1200 health, buffed.
func druidSelf(g *fakeGame) {
	g.self.Class, g.self.Level = ClassDruid, 40
	g.self.Health, g.self.MaxHealth = 1200, 1200
	g.self.PowerType, g.self.Power, g.self.MaxPower = 0, 1000, 1000
	g.self.Auras = []objects.Aura{{Name: "Mark of the Wild", SpellID: drMotw5}}
}

// formAura is the aura of a form.
func formAura(name string, id uint32) objects.Aura { return objects.Aura{Name: name, SpellID: id} }

// shiftTo puts the druid into a form: the form's aura, the form's power (energy 100 or rage 0), the mana kept aside.
func shiftTo(g *fakeGame, name string, id uint32) {
	g.setSelf(func(u *objects.Unit) {
		mana, max := u.Power, u.MaxPower
		if u.PowerType != 0 {
			mana, max = u.Mana, u.MaxMana
		}
		u.Mana, u.MaxMana = mana, max
		u.Auras = append(u.Auras, formAura(name, id))
		switch name {
		case formCat:
			u.PowerType, u.Power, u.MaxPower = 3, 100, 100
		case formBear, formDireBear:
			u.PowerType, u.Power, u.MaxPower = 1, 0, 1000
		default: // travel, aquatic, tree: the mana is still the power
			u.PowerType, u.Power, u.MaxPower = 0, mana, max
			u.Mana, u.MaxMana = 0, 0
		}
	})
}

func setManaTrue(g *fakeGame, pct uint32) {
	g.setSelf(func(u *objects.Unit) {
		if u.PowerType == 0 {
			u.Power = u.MaxPower * pct / 100
		} else {
			u.Mana = u.MaxMana * pct / 100
		}
	})
}

func setCP(g *fakeGame, n uint8, target packet.GUID) {
	g.setSelf(func(u *objects.Unit) { u.ComboPoints, u.ComboTarget = n, target })
}

func mobAura(g *fakeGame, name string, id uint32) {
	g.mutate(mobA, func(u *objects.Unit) { u.Auras = append(u.Auras, objects.Aura{Name: name, SpellID: id, Harmful: true}) })
}

func selfAura(g *fakeGame, name string, id uint32) {
	g.setSelf(func(u *objects.Unit) { u.Auras = append(u.Auras, objects.Aura{Name: name, SpellID: id}) })
}

// ---- the spell data the druid relies on ----

func TestDruidSpellData(t *testing.T) {
	// id, name, level, cost (energy and flat mana in units, rage in tenths), range max: read off the core's spell_template when the
	// table was made. A cost of 0 with a percent cost is a cost of a percent of the base mana (the forms, the cures, Innervate).
	type row struct {
		id    uint32
		name  string
		level uint8
		power int8
		cost  uint32
		pct   uint32
		rng   float32
	}
	for _, c := range []row{
		{drCat, "Cat Form", 20, 0, 0, 35, 0}, {drBear, "Bear Form", 10, 0, 0, 35, 0}, {drDire, "Dire Bear Form", 40, 0, 0, 35, 0},
		{drTree, "Tree Form", 44, 0, 0, 20, 0}, {drTravel, "Travel Form", 30, 0, 0, 13, 0}, {drAquatic, "Aquatic Form", 16, 0, 0, 13, 0},
		{drMoonkinForm, "Moonkin Form", 30, 0, 0, 22, 0},
		{drClaw, "Claw", 20, spells.PowerEnergy, 45, 0, 5}, {drShred, "Shred", 22, spells.PowerEnergy, 60, 0, 5},
		{drRake, "Rake", 24, spells.PowerEnergy, 40, 0, 5}, {drRip, "Rip", 20, spells.PowerEnergy, 30, 0, 5},
		{drBite, "Ferocious Bite", 24, spells.PowerEnergy, 35, 0, 5}, {drFury, "Tiger's Fury", 24, spells.PowerEnergy, 30, 0, 0},
		{drFFF, "Faerie Fire (Feral)", 25, spells.PowerEnergy, 0, 0, 30},
		{drMaul, "Maul", 10, spells.PowerRage, 150, 0, 5}, {drSwipe, "Swipe", 16, spells.PowerRage, 200, 0, 5},
		{drRoar, "Demoralizing Roar", 10, spells.PowerRage, 100, 0, 0}, {drGrowl, "Growl", 10, spells.PowerRage, 0, 0, 5},
		{drEnrage, "Enrage", 12, spells.PowerRage, 0, 0, 0}, {drFrenzied, "Frenzied Regeneration", 36, spells.PowerRage, 0, 0, 0},
		{drBash, "Bash", 14, spells.PowerRage, 100, 0, 5},
		{drHT1, "Healing Touch", 1, 0, 25, 0, 40}, {drRegrowth1, "Regrowth", 12, 0, 96, 0, 40}, {drRejuv1, "Rejuvenation", 4, 0, 25, 0, 40},
		{drRemoveCurse, "Remove Curse", 24, 0, 0, 10, 30}, {drAbolish, "Abolish Poison", 26, 0, 0, 16, 30}, {drCurePoison, "Cure Poison", 14, 0, 0, 16, 30},
		{drInnervate, "Innervate", 40, 0, 0, 5, 30}, {drMotw1, "Mark of the Wild", 1, 0, 20, 0, 30}, {467, "Thorns", 6, 0, 35, 0, 30},
		{drMoonfire, "Moonfire", 4, 0, 25, 0, 30}, {drWrath, "Wrath", 1, 0, 20, 0, 30}, {drStarfire, "Starfire", 20, 0, 95, 0, 30},
		{drSwarm, "Insect Swarm", 20, 0, 45, 0, 30}, {drRoots, "Entangling Roots", 8, 0, 50, 0, 30}, {drHibernate, "Hibernate", 18, 0, 90, 0, 30},
	} {
		if got := spells.Name(c.id); got != c.name {
			t.Errorf("%d is %q, want %q", c.id, got, c.name)
			continue
		}
		m, ok := spells.Lookup(c.id)
		if !ok || m.Level != c.level || m.Cost != c.cost || m.CostPct != c.pct || m.RangeMax != c.rng {
			t.Errorf("%s: %+v", c.name, m)
		}
		if (c.cost > 0 || c.pct > 0) && m.Power != c.power {
			t.Errorf("%s is paid in power %d, want %d: %+v", c.name, m.Power, c.power, m)
		}
	}
	// The cast times the rotation relies on.
	for id, ms := range map[uint32]uint32{drStarfire: 3500, drWrath: 1500, drHT1: 1500, 9889: 3500, drRegrowth1: 2000, drRoots: 1500} {
		if m, _ := spells.Lookup(id); m.CastMS != ms {
			t.Errorf("%s casts in %d ms, want %d", spells.Name(id), m.CastMS, ms)
		}
	}
	// The combo-point finishers are flagged so; the next-swing Maul is; Growl taunts.
	for _, id := range []uint32{drRip, drBite} {
		if m, _ := spells.Lookup(id); !m.Flags.Has(spells.FlagComboPoints) {
			t.Errorf("%s is not a finisher: %v", spells.Name(id), m.Flags.Names())
		}
	}
	if m, _ := spells.Lookup(drMaul); !m.Flags.Has(spells.FlagNextSwing) {
		t.Errorf("Maul is not a next-swing ability: %v", m.Flags.Names())
	}
	if m, _ := spells.Lookup(drGrowl); !m.Flags.Has(spells.FlagTaunt) {
		t.Errorf("Growl does not taunt: %v", m.Flags.Names())
	}
	// Regrowth is a direct heal with a heal over time; Rejuvenation only the latter; the cures dispel.
	if m, _ := spells.Lookup(drRegrowth1); healDirectAmount(m, 40) == 0 || healAmount(m, 40) <= healDirectAmount(m, 40) {
		t.Errorf("Regrowth: direct %v all %v", healDirectAmount(m, 40), healAmount(m, 40))
	}
	if m, _ := spells.Lookup(drRejuv1); healDirectAmount(m, 40) != 0 || healAmount(m, 40) == 0 {
		t.Errorf("Rejuvenation: direct %v all %v", healDirectAmount(m, 40), healAmount(m, 40))
	}
	// The cures dispel.
	for _, id := range []uint32{drRemoveCurse, drAbolish, drCurePoison} {
		if m, _ := spells.Lookup(id); !m.Flags.Has(spells.FlagDispel) {
			t.Errorf("%s does not dispel: %v", spells.Name(id), m.Flags.Names())
		}
	}
	// The form spells the table holds are the casting ones, whatever else is called by their name.
	for _, f := range druidForms {
		if spells.Name(f.Spell) != f.Name {
			t.Errorf("form %q: id %d is %q", f.Name, f.Spell, spells.Name(f.Spell))
		}
	}
}

// formLetters names a stances bitmask by the forms of druidForms: C Cat, T Tree, V Travel, A Aquatic, B Bear, D Dire Bear, S Swift Travel,
// M Moonkin.
func formLetters(forms []string) string {
	out := ""
	for _, c := range []struct{ name, letter string }{{formCat, "C"}, {formTree, "T"}, {formTravel, "V"}, {formAquatic, "A"}, {formBear, "B"},
		{formDireBear, "D"}, {formSwiftTrav, "S"}, {formMoonkin, "M"}} {
		for _, f := range forms {
			if f == c.name {
				out += c.letter
			}
		}
	}
	return out
}

// coreForms is what the core's spell_template says about the shapeshift forms every player rank of the gated feral abilities work in: the
// `stances` bitmask (1 << (form - 1): Cat 0x1, Bear 0x10, Dire Bear 0x80) of the rows in tortoise-wow 5d79486,
// sql/base/tw_world_spell_template.sql; no file of sql/database_updates/world sets `stances` for these spells. C Cat, B Bear, D Dire Bear.
var coreForms = map[string]struct {
	forms string
	ids   []uint32
}{
	"Claw":                  {"C", []uint32{1082, 3029, 5201, 9849, 9850}},
	"Shred":                 {"C", []uint32{5221, 6800, 8992, 9829, 9830, 45969}},
	"Rake":                  {"C", []uint32{1822, 1823, 1824, 9904}},
	"Rip":                   {"C", []uint32{1079, 9492, 9493, 9752, 9894, 9896}},
	"Ferocious Bite":        {"C", []uint32{22557, 22568, 22827, 22828, 22829, 31018}},
	"Tiger's Fury":          {"C", []uint32{5217, 6793, 9845, 9846}},
	"Faerie Fire (Feral)":   {"CBD", []uint32{16857, 17390, 17391, 17392}},
	"Maul":                  {"BD", []uint32{6807, 6808, 6809, 8972, 9745, 9880, 9881}},
	"Swipe":                 {"BD", []uint32{779, 780, 769, 9754, 9908}},
	"Demoralizing Roar":     {"BD", []uint32{99, 1735, 9490, 9747, 9898}},
	"Growl":                 {"BD", []uint32{6795}},
	"Enrage":                {"BD", []uint32{5229}},
	"Frenzied Regeneration": {"BD", []uint32{22842, 22895, 22896}},
	"Bash":                  {"BD", []uint32{5211, 6798, 8983}},
}

// coreFormMasks is the bit of each form's spell in the `stances` masks (the shapeshift form id minus one: the effect's misc value of the
// form spell in spell_template is 1 for Cat Form, 2 Tree, 3 Travel, 4 Aquatic, 5 Bear, 8 Dire Bear, 11 Swift Travel, 31 Moonkin).
var coreFormMasks = map[uint32]uint32{768: 1 << 0, 775: 1 << 1, 783: 1 << 2, 1066: 1 << 3, 5487: 1 << 4, 9634: 1 << 7, 51398: 1 << 10, 24858: 1 << 30}

func TestFormGatesFollowTheCoreData(t *testing.T) {
	for name, c := range coreForms {
		for _, id := range c.ids {
			if spells.Name(id) != name {
				t.Errorf("%d is %q, want %q", id, spells.Name(id), name)
			}
		}
		if got := formLetters(druidAbilityForms[name]); got != c.forms {
			t.Errorf("%s: the brain gates it to %q, the core's data says %q", name, got, c.forms)
		}
	}
	for _, f := range druidForms {
		if coreFormMasks[f.Spell] != f.Mask {
			t.Errorf("%s: the table has bit %#x, the core's data says %#x", f.Name, f.Mask, coreFormMasks[f.Spell])
		}
	}
	// The abilities the table does not gate work in every form or none the brain uses.
	for n := range druidAbilityForms {
		if _, ok := coreForms[n]; !ok {
			t.Errorf("%s is gated by the brain and pinned by no test", n)
		}
	}
}

// ---- druid_caster: caster form only ----

func TestPresetsPlayOneRole(t *testing.T) {
	for preset, role := range map[string]string{"resto_druid": RoleHealer, "balance_druid": RoleDPS, "feral_cat_druid": RoleDPS, "bear_tank_druid": RoleTank,
		"resto_shaman": RoleHealer, "elemental_shaman": RoleDPS, "enhancement_shaman": RoleDPS} {
		b, err := New(newFake(), DefaultConfig(), Params{Strategies: []string{preset}})
		if err != nil {
			t.Fatalf("%s: %v", preset, err)
		}
		if b.p.Role != role {
			t.Errorf("%s plays %q, want %q", preset, b.p.Role, role)
		}
	}
	if _, err := New(newFake(), DefaultConfig(), Params{Strategies: []string{"resto_druid"}, Role: RoleTank}); err == nil {
		t.Fatal("a healer preset must not take the tank role")
	}
}

// No preset of the step casts a spell it was told never to: Hibernate (a sleeping mob that wakes pulls the pack), Prowl (never in combat,
// and no stealth play), Ghost Wolf (never in a dungeon), Pounce and Ravage (stealth openers).
func TestNoPresetUsesTheSpellsItMustNot(t *testing.T) {
	never := map[string]bool{"Hibernate": true, "Prowl": true, "Ghost Wolf": true, "Pounce": true, "Ravage": true, "Moonkin Form": true, "Tree Form": true,
		"Travel Form": true, "Aquatic Form": true}
	for _, preset := range []string{"resto_druid", "balance_druid", "feral_cat_druid", "bear_tank_druid", "resto_shaman", "elemental_shaman", "enhancement_shaman"} {
		strats, _, err := Resolve([]string{preset})
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range strats {
			for _, a := range s.Actions {
				if never[a.Spell] {
					t.Errorf("%s: action %s casts %s", preset, a.Name, a.Spell)
				}
			}
		}
	}
}

func restoRig(t *testing.T, known ...uint32) (*rig, *casterGame) {
	t.Helper()
	if len(known) == 0 {
		known = drRestoKnown
	}
	g := newCasterGame(known...)
	druidSelf(g.fakeGame)
	g.self.InCombat = true
	return rigOn(t, g, g.fakeGame, Params{Strategies: []string{"resto_druid"}}), g
}

func TestRestoHealsWithHealingTouchAndRegrowthInAnEmergency(t *testing.T) {
	r, g := restoRig(t)
	g.member(dpsG1, "Mage", 8, 500, 1000, 5, 0) // 50%: the big slow heal covers it
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "heal" || d.Target != dpsG1 || spells.Name(d.Spell) != "Healing Touch" {
		t.Fatalf("%+v", d)
	}
	r2, g2 := restoRig(t)
	g2.member(dpsG1, "Mage", 8, 150, 1000, 5, 0) // 15%: the quick one, by what it heals at once
	d = r2.tick(100 * time.Millisecond)
	if d == nil || d.Action != "heal" || spells.Name(d.Spell) != "Regrowth" || d.Rel < RelInterrupt {
		t.Fatalf("%+v", d)
	}
	// Regrowth is chosen by its direct part, not by the heal over time that comes with it: the biggest rank goes.
	if d.Spell != drRegrowth5 {
		t.Fatalf("rank %d, want the highest known %d", d.Spell, drRegrowth5)
	}
}

func TestRestoKeepsRejuvenationOnTheTank(t *testing.T) {
	r, g := restoRig(t)
	g.member(tankG, "Tankard", 1, 960, 1000, 6, 0) // 96%: no direct heal, the HoT is for the tank in a fight
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "hot_tank" || d.Target != tankG || spells.Name(d.Spell) != "Rejuvenation" || d.Spell != drRejuv7 {
		t.Fatalf("%+v", d)
	}
	g.addAura(tankG, objects.Aura{Name: "Rejuvenation", SpellID: drRejuv7})
	if d := r.tick(2 * time.Second); d != nil {
		t.Fatalf("the HoT is on: %+v", d)
	}
	// A member that is not the tank gets no HoT.
	r2, g2 := restoRig(t)
	g2.member(dpsG1, "Mage", 8, 960, 1000, 5, 0)
	if d := r2.tick(100 * time.Millisecond); d != nil {
		t.Fatalf("%+v", d)
	}
}

func TestRestoDispelsPoisonAndCurse(t *testing.T) {
	r, g := restoRig(t)
	g.member(dpsG1, "Mage", 8, 1000, 1000, 5, 0)
	g.addAura(dpsG1, objects.Aura{Name: "Deadly Poison", SpellID: auraPoison, Harmful: true})
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "dispel" || d.Spell != drAbolish || d.Target != dpsG1 {
		t.Fatalf("%+v", d)
	}
	// The cure is working on it: left alone. Without Abolish Poison the plain cure goes.
	g.addAura(dpsG1, objects.Aura{Name: "Abolish Poison", SpellID: drAbolish})
	if d := r.tick(5 * time.Second); d != nil {
		t.Fatalf("%+v", d)
	}
	var noAbolish []uint32
	for _, id := range drRestoKnown {
		if id != drAbolish {
			noAbolish = append(noAbolish, id)
		}
	}
	r2, g2 := restoRig(t, noAbolish...)
	g2.member(dpsG1, "Mage", 8, 1000, 1000, 5, 0)
	g2.addAura(dpsG1, objects.Aura{Name: "Deadly Poison", SpellID: auraPoison, Harmful: true})
	if d := r2.tick(100 * time.Millisecond); d == nil || d.Spell != drCurePoison {
		t.Fatalf("%+v", d)
	}
	r3, g3 := restoRig(t)
	g3.member(dpsG1, "Mage", 8, 1000, 1000, 5, 0)
	g3.addAura(dpsG1, objects.Aura{Name: "Curse of Agony", SpellID: auraCurse, Harmful: true})
	if d := r3.tick(100 * time.Millisecond); d == nil || d.Spell != drRemoveCurse || d.Target != dpsG1 {
		t.Fatalf("%+v", d)
	}
}

func TestRestoLeavesAFormBeforeItHeals(t *testing.T) {
	r, g := restoRig(t)
	g.member(dpsG1, "Mage", 8, 300, 1000, 5, 0)
	shiftTo(g.fakeGame, formDireBear, drDire)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "leave_form" || d.Rel < RelInterrupt+2 || d.Result != ResultOK {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if got := g.cancelList(); len(got) != 1 || got[0] != drDire {
		t.Fatalf("cancelled %v", got)
	}
	if len(g.castList()) != 0 {
		t.Fatalf("casts %v", spellSeq(g.fakeGame))
	}
	// The aura went with the server's update: the heal follows, no second cancel.
	d = r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "heal" || len(g.cancelList()) != 1 {
		t.Fatalf("%+v cancels %v", d, g.cancelList())
	}
}

func TestRestoWaitsForTheFormToGoAndDoesNotCancelTwice(t *testing.T) {
	r, g := restoRig(t)
	g.member(dpsG1, "Mage", 8, 300, 1000, 5, 0)
	shiftTo(g.fakeGame, formCat, drCat)
	g.keep = true // the server has not answered: the aura is still there
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "leave_form" {
		t.Fatalf("%+v", d)
	}
	for i := 0; i < 3; i++ {
		d := r.tick(300 * time.Millisecond)
		if d != nil && (d.Action == "leave_form" || d.Action == "heal") {
			t.Fatalf("tick %d: %+v", i, d)
		}
	}
	if len(g.cancelList()) != 1 || len(g.castList()) != 0 {
		t.Fatalf("cancels %v casts %v", g.cancelList(), spellSeq(g.fakeGame))
	}
	// A second cancel goes after the hold if the aura is still there.
	if d := r.tick(2 * time.Second); d == nil || d.Action != "leave_form" || len(g.cancelList()) != 2 {
		t.Fatalf("%+v", d)
	}
}

func TestACasterWithoutAuraCancellingStaysPut(t *testing.T) {
	g := newCasterGame(drRestoKnown...)
	druidSelf(g.fakeGame)
	g.self.InCombat = true
	g.member(dpsG1, "Mage", 8, 300, 1000, 5, 0)
	shiftTo(g.fakeGame, formTravel, drTravel)
	r := rigOn(t, struct{ Game }{g}, g.fakeGame, Params{Strategies: []string{"resto_druid"}})
	d := r.tick(100 * time.Millisecond)
	if d != nil || len(g.cancelList()) != 0 || len(g.castList()) != 0 {
		t.Fatalf("%+v cancels %v casts %v", d, g.cancelList(), spellSeq(g.fakeGame))
	}
}

func TestInnervateOnTheDruidWhenDry(t *testing.T) {
	r, g := restoRig(t)
	g.setMana(10)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "innervate" || d.Spell != drInnervate || d.Target != selfG || d.Rel <= RelHigh {
		t.Fatalf("%+v", d)
	}
	selfAura(g.fakeGame, "Innervate", drInnervate)
	if d := r.tick(2 * time.Second); d != nil {
		t.Fatalf("it is on: %+v", d)
	}
	// Not with the mana in good order, and not out of a fight.
	r2, g2 := restoRig(t)
	g2.setMana(30)
	if d := r2.tick(100 * time.Millisecond); d != nil {
		t.Fatalf("%+v", d)
	}
	r3, g3 := restoRig(t)
	g3.self.InCombat = false
	g3.setMana(10)
	if d := r3.tick(100 * time.Millisecond); d != nil && d.Action == "innervate" {
		t.Fatalf("%+v", d)
	}
}

func TestDruidBuffsOutOfCombatInCasterFormOnly(t *testing.T) {
	r, g := restoRig(t)
	g.self.InCombat = false
	g.setSelf(func(u *objects.Unit) { u.Auras = []objects.Aura{} })
	g.member(tankG, "Tankard", 1, 1000, 1000, 6, 0)
	g.mutate(tankG, func(u *objects.Unit) { u.InCombat = false; u.Auras = []objects.Aura{} })
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "mark_of_the_wild" || d.Target != selfG || d.Spell != drMotw5 {
		t.Fatalf("%+v", d)
	}
	selfAura(g.fakeGame, "Mark of the Wild", drMotw5)
	if d = r.tick(2 * time.Second); d == nil || d.Action != "mark_of_the_wild" || d.Target != tankG {
		t.Fatalf("%+v", d)
	}
	g.addAura(tankG, objects.Aura{Name: "Gift of the Wild", SpellID: 21849})
	d = r.tick(2 * time.Second)
	if d == nil || d.Action != "thorns" || d.Target != tankG || d.Spell != drThorns4 {
		t.Fatalf("%+v", d)
	}
	g.addAura(tankG, objects.Aura{Name: "Thorns", SpellID: drThorns4})
	if d = r.tick(2 * time.Second); d != nil {
		t.Fatalf("%+v", d)
	}
	// Nothing in combat, nothing at a walk (no rest gate), nothing with little mana.
	g.setSelf(func(u *objects.Unit) { u.Auras = []objects.Aura{}; u.InCombat = true })
	if d = r.tick(2 * time.Second); d != nil && d.Action == "mark_of_the_wild" {
		t.Fatalf("buffed in combat: %+v", d)
	}
	g.setSelf(func(u *objects.Unit) { u.InCombat = false })
	r.b.restOK = func() bool { return false }
	if d = r.tick(2 * time.Second); d != nil {
		t.Fatalf("buffed on the walk: %+v", d)
	}
	r.b.restOK = func() bool { return true }
	g.setMana(20)
	if d = r.tick(2 * time.Second); d != nil && d.Action == "mark_of_the_wild" {
		t.Fatalf("buffed on low mana: %+v", d)
	}
}

// ---- balance ----

func balanceRig(t *testing.T, known ...uint32) (*rig, *casterGame) {
	t.Helper()
	if len(known) == 0 {
		known = drBalanceKnown
	}
	g := newCasterGame(known...)
	druidSelf(g.fakeGame)
	tankFights(g.fakeGame, 25, 30, 0)
	return rigOn(t, g, g.fakeGame, Params{Strategies: []string{"balance_druid"}}), g
}

func TestBalanceRotation(t *testing.T) {
	r, g := balanceRig(t)
	r.tick(100 * time.Millisecond)
	if d := open(r); d == nil || d.Action != "moonfire" || d.Spell != drMoonfire || d.Target != mobA {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	mobAura(g.fakeGame, "Moonfire", drMoonfire)
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "insect_swarm" || d.Spell != drSwarm {
		t.Fatalf("%+v", d)
	}
	mobAura(g.fakeGame, "Insect Swarm", drSwarm)
	d := r.tick(1600 * time.Millisecond)
	if d == nil || d.Action != "starfire" || spells.Name(d.Spell) != "Starfire" {
		t.Fatalf("%+v", d)
	}
	// Starfire is dear and slow: with the mana under 35% or the mob nearly dead the filler is Wrath.
	g.setMana(30)
	if d := r.tick(2500 * time.Millisecond); d == nil || d.Action != "wrath" || spells.Name(d.Spell) != "Wrath" {
		t.Fatalf("low mana: %+v idle %q", d, r.idle())
	}
	g.setMana(100)
	setMobHP(g.fakeGame, mobA, 25)
	if d := r.tick(2 * time.Second); d == nil || d.Action != "wrath" {
		t.Fatalf("a mob nearly dead: %+v", d)
	}
	// The damage over time is not put on a mob that will not live to see it.
	r2, g2 := balanceRig(t)
	r2.tick(100 * time.Millisecond)
	setMobHP(g2.fakeGame, mobA, 30)
	if d := open(r2); d == nil || d.Action != "wrath" {
		t.Fatalf("%+v", d)
	}
}

func TestBalanceMovesOnlyWithInstants(t *testing.T) {
	r, g := balanceRig(t)
	r.tick(100 * time.Millisecond)
	g.setSelf(func(u *objects.Unit) { u.Moving = true })
	if d := open(r); d == nil || d.Action != "moonfire" {
		t.Fatalf("an instant goes on the move: %+v", d)
	}
	mobAura(g.fakeGame, "Moonfire", drMoonfire)
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "insect_swarm" {
		t.Fatalf("%+v", d)
	}
	mobAura(g.fakeGame, "Insect Swarm", drSwarm)
	if d := r.tick(1600 * time.Millisecond); d != nil {
		t.Fatalf("a cast with a cast time on the move: %+v", d)
	}
}

func TestBalanceKnowsNoInsectSwarmBeforeLevel20(t *testing.T) {
	r, g := balanceRig(t, drMoonfire, drWrath)
	g.self.Level = 12
	r.tick(100 * time.Millisecond)
	if d := open(r); d == nil || d.Action != "moonfire" {
		t.Fatalf("%+v", d)
	}
	mobAura(g.fakeGame, "Moonfire", drMoonfire)
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "wrath" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
}

func TestBalanceRootsAMobThatHasPickedIt(t *testing.T) {
	r, g := balanceRig(t)
	g.addUnit(mob(mobB, selfG), 4, 0)
	d := r.tick(100 * time.Millisecond)
	if d != nil && d.Action == "entangling_roots" {
		t.Fatalf("the druid is healthy: %+v", d)
	}
	g.setSelf(func(u *objects.Unit) { u.Health = 800 }) // 67%
	d = r.tick(1600 * time.Millisecond)
	if d == nil || d.Action != "entangling_roots" || d.Target != mobB || d.Spell != drRoots {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// Rooted, it is not rooted again.
	g.mutate(mobB, func(u *objects.Unit) {
		u.Auras = append(u.Auras, objects.Aura{Name: "Entangling Roots", SpellID: drRoots, Harmful: true})
	})
	if d := r.tick(2 * time.Second); d != nil && d.Action == "entangling_roots" {
		t.Fatalf("%+v", d)
	}
	// Hibernate is never cast.
	for _, c := range g.castList() {
		if c.spell == drHibernate {
			t.Fatal("Hibernate was cast")
		}
	}
}

func TestBalanceStepsAwayFromTheRootedMob(t *testing.T) {
	g := newFake(drBalanceKnown...)
	druidSelf(g)
	g.self.InCombat = true
	cf := newMeleeGame(g)
	r := rigOn(t, cf, g, Params{Strategies: []string{"balance_druid"}})
	rooted := mob(mobB, selfG)
	rooted.Auras = []objects.Aura{{Name: "Entangling Roots", SpellID: drRoots, Harmful: true}}
	g.addUnit(rooted, 4, 0)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "step_away" || len(cf.walkList()) != 1 {
		t.Fatalf("%+v walks %v idle %q", d, cf.walkList(), r.idle())
	}
}

// ---- the cat ----

func catRig(t *testing.T, known ...uint32) (*rig, *meleeGame) {
	t.Helper()
	if len(known) == 0 {
		known = drCatKnown
	}
	g := newFake(known...)
	druidSelf(g)
	shiftTo(g, formCat, drCat)
	mg := newMeleeGame(g)
	return rigOn(t, mg, g, Params{Strategies: []string{"feral_cat_druid"}}), mg
}

// catFight is the standard fight: the tank holds a mob 4 yards east of the cat, the mob faces east (away from the cat, which is behind it).
func catFight(r *rig, mg *meleeGame) {
	tankFights(r.g, 7, 4, 0)
	mg.setSwing(mobA)
	r.tick(100 * time.Millisecond)
}

func TestCatShiftsWhenTheFightStarts(t *testing.T) {
	g := newFake(drCatKnown...)
	druidSelf(g)
	mg := newMeleeGame(g)
	r := rigOn(t, mg, g, Params{Strategies: []string{"feral_cat_druid"}})
	tankFights(g, 7, 4, 0)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "enter_form" || d.Spell != drCat || d.Target != selfG {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if got := spellSeq(g); !eq(got, []uint32{drCat}) {
		t.Fatalf("casts %v", got)
	}
	// Not twice while the aura is on its way.
	if d := r.tick(300 * time.Millisecond); d != nil && d.Action == "enter_form" {
		t.Fatalf("%+v", d)
	}
	if len(mg.cancelList()) != 0 {
		t.Fatalf("cancelled %v", mg.cancelList())
	}
}

func TestCatRotationBuildsAndFinishes(t *testing.T) {
	r, mg := catRig(t)
	catFight(r, mg)
	d := open(r)
	if d == nil || d.Action != "faerie_fire_feral" || d.Spell != drFFF || d.Target != mobA {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	mobAura(r.g, "Faerie Fire (Feral)", drFFF)
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "tigers_fury" || d.Spell != drFury {
		t.Fatalf("%+v", d)
	}
	selfAura(r.g, "Tiger's Fury", drFury)
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "rake" || d.Spell != drRake {
		t.Fatalf("%+v", d)
	}
	mobAura(r.g, "Rake", drRake)
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "shred" || d.Spell != drShred {
		t.Fatalf("behind the mob: %+v", d)
	}
	// Combo points decide the finisher: Rip first on a healthy mob, Ferocious Bite once the Rip runs, or at 3 on a mob nearly dead.
	setCP(r.g, 4, mobA)
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "rip" || d.Spell != drRip {
		t.Fatalf("4 points: %+v", d)
	}
	mobAura(r.g, "Rip", drRip)
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "shred" {
		t.Fatalf("4 points under a Rip: %+v", d)
	}
	setCP(r.g, 5, mobA)
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "ferocious_bite" || d.Spell != drBite {
		t.Fatalf("5 points: %+v", d)
	}
	setCP(r.g, 3, mobA)
	setMobHP(r.g, mobA, 30)
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "ferocious_bite" {
		t.Fatalf("3 points on a mob nearly dead: %+v", d)
	}
	// Combo points on another mob are none.
	setCP(r.g, 5, mobB)
	setMobHP(r.g, mobA, 100)
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action == "ferocious_bite" || d.Action == "rip" {
		t.Fatalf("points on another mob: %+v", d)
	}
}

func TestCatSavesRipForAMobThatLives(t *testing.T) {
	r, mg := catRig(t)
	catFight(r, mg)
	setCP(r.g, 5, mobA)
	setMobHP(r.g, mobA, 38)
	d := open(r)
	// Under 40% the bleed would not see out: the bite.
	if d == nil || d.Action != "ferocious_bite" {
		t.Fatalf("%+v", d)
	}
}

func TestCatShredsOnlyFromBehind(t *testing.T) {
	g := newFake(drCatKnown...)
	druidSelf(g)
	shiftTo(g, formCat, drCat)
	mg := newMeleeGame(g)
	r := rigOn(t, mg, g, Params{Strategies: []string{"feral_cat_druid"}, NoMove: true}) // no stepping round the mob
	tankFights(g, 7, 4, math.Pi)                                                        // the mob faces west, the cat stands in front of it
	mg.setSwing(mobA)
	r.tick(100 * time.Millisecond)
	mobAura(g, "Faerie Fire (Feral)", drFFF)
	mobAura(g, "Rake", drRake)
	selfAura(g, "Tiger's Fury", drFury)
	if d := open(r); d == nil || d.Action != "claw" || spells.Name(d.Spell) != "Claw" {
		t.Fatalf("Claw is the builder from the front: %+v idle %q", d, r.idle())
	}
	// Walked round behind the mob (the melee position's work), the same cat shreds.
	g.setSelf(func(u *objects.Unit) { u.X = 8 })
	g.mutate(tankG, func(u *objects.Unit) {})
	g.mutate(mobA, func(u *objects.Unit) {})
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "shred" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
}

func TestCatWithoutShredBuildsWithClaw(t *testing.T) {
	var noShred []uint32
	for _, id := range drCatKnown {
		if id != drShred {
			noShred = append(noShred, id)
		}
	}
	r, mg := catRig(t, noShred...)
	catFight(r, mg)
	mobAura(r.g, "Faerie Fire (Feral)", drFFF)
	mobAura(r.g, "Rake", drRake)
	selfAura(r.g, "Tiger's Fury", drFury)
	if d := open(r); d == nil || d.Action != "claw" || spells.Name(d.Spell) != "Claw" {
		t.Fatalf("%+v", d)
	}
}

func TestCatEnergyDecidesWhatItCanDo(t *testing.T) {
	r, mg := catRig(t)
	catFight(r, mg)
	mobAura(r.g, "Faerie Fire (Feral)", drFFF)
	mobAura(r.g, "Rake", drRake)
	selfAura(r.g, "Tiger's Fury", drFury)
	setEnergy(r.g, 50) // Claw (45) goes, Shred (60) does not
	if d := open(r); d == nil || d.Action != "claw" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	setEnergy(r.g, 20)
	if d := r.tick(1600 * time.Millisecond); d != nil {
		t.Fatalf("with 20 energy: %+v", d)
	}
}

func TestCatNeverLeavesItsFormInCombat(t *testing.T) {
	r, mg := catRig(t)
	catFight(r, mg)
	r.g.items = []objects.Item{{Entry: itemSpringWater, Count: 5}, {Entry: itemBread, Count: 5}, {Entry: 929, Count: 2}}
	setManaTrue(r.g, 5)
	r.g.setSelf(func(u *objects.Unit) { u.Health = 200; u.InCombat = true })
	for i := 0; i < 4; i++ {
		d := r.tick(1600 * time.Millisecond)
		if d != nil && (d.Action == "leave_form" || d.Action == "drink" || d.Action == "eat" || d.Action == "health_potion") {
			t.Fatalf("tick %d: %+v", i, d)
		}
	}
	if len(mg.cancelList()) != 0 || len(r.g.used) != 0 {
		t.Fatalf("cancelled %v used %v: an item cannot be used in a form", mg.cancelList(), r.g.used)
	}
}

func TestCatLeavesItsFormToDrinkAndShiftsBack(t *testing.T) {
	r, mg := catRig(t)
	r.g.items = []objects.Item{{Entry: itemSpringWater, Count: 5}}
	setManaTrue(r.g, 20)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "leave_form" || d.Result != ResultOK {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if got := mg.cancelList(); len(got) != 1 || got[0] != drCat {
		t.Fatalf("cancelled %v", got)
	}
	// In caster form the mana is the power again, and drinking is rest_drink's.
	if r.g.self.PowerType != 0 || PowerPctOf(r.g.self) != 20 {
		t.Fatalf("self %+v", r.g.self)
	}
	d = r.tick(2 * time.Second)
	if d == nil || d.Action != "drink" || len(r.g.used) != 1 || r.g.used[0] != itemSpringWater {
		t.Fatalf("%+v used %v", d, r.g.used)
	}
	// While it drinks it does not shift (that would break the meal) ...
	r.g.setSelf(func(u *objects.Unit) {
		u.Auras = append(u.Auras, objects.Aura{Name: "Drink", SpellID: 430})
		u.StandState = "sit_chair"
	})
	if d := r.tick(2 * time.Second); d != nil && d.Action == "enter_form" {
		t.Fatalf("%+v", d)
	}
	// ... when it has drunk, it shifts back.
	setManaTrue(r.g, 100)
	r.g.setSelf(func(u *objects.Unit) {
		u.Auras = []objects.Aura{{Name: "Mark of the Wild", SpellID: drMotw5}}
		u.StandState = "stand"
	})
	d = r.tick(2 * time.Second)
	if d == nil || d.Action != "enter_form" || d.Spell != drCat {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
}

// PowerPctOf is the power of a unit row in percent, for the tests.
func PowerPctOf(u objects.Unit) int { return int(uint64(u.Power) * 100 / uint64(u.MaxPower)) }

func TestCatLeavesItsFormToEat(t *testing.T) {
	r, mg := catRig(t)
	r.g.items = []objects.Item{{Entry: itemBread, Count: 5}}
	r.g.setSelf(func(u *objects.Unit) { u.Health = 600 })
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "leave_form" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if d = r.tick(2 * time.Second); d == nil || d.Action != "eat" {
		t.Fatalf("%+v", d)
	}
	if len(mg.cancelList()) != 1 {
		t.Fatalf("cancels %v", mg.cancelList())
	}
	// With nothing to eat it stays in its form: the meal would not be there.
	r2, mg2 := catRig(t)
	r2.g.setSelf(func(u *objects.Unit) { u.Health = 600 })
	if d := r2.tick(100 * time.Millisecond); d != nil || len(mg2.cancelList()) != 0 {
		t.Fatalf("%+v cancels %v", d, mg2.cancelList())
	}
}

func TestCatLeavesItsFormToBuffWhenTheManaIsGood(t *testing.T) {
	r, mg := catRig(t)
	r.g.setSelf(func(u *objects.Unit) { u.Auras = []objects.Aura{formAura(formCat, drCat)} }) // no Mark of the Wild
	setManaTrue(r.g, 90)
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "leave_form" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if d := r.tick(2 * time.Second); d == nil || d.Action != "mark_of_the_wild" || d.Target != selfG {
		t.Fatalf("%+v", d)
	}
	selfAura(r.g, "Mark of the Wild", drMotw5)
	if d := r.tick(2 * time.Second); d == nil || d.Action != "enter_form" {
		t.Fatalf("%+v", d)
	}
	if len(mg.cancelList()) != 1 {
		t.Fatalf("cancels %v", mg.cancelList())
	}
	// With the mana low the shift back would not be affordable: it stays in the form unbuffed.
	r2, mg2 := catRig(t)
	r2.g.setSelf(func(u *objects.Unit) { u.Auras = []objects.Aura{formAura(formCat, drCat)} })
	setManaTrue(r2.g, 40)
	if d := r2.tick(100 * time.Millisecond); d != nil || len(mg2.cancelList()) != 0 {
		t.Fatalf("%+v cancels %v", d, mg2.cancelList())
	}
}

func TestCatLeavesAFormThatIsNotItsOwnOutOfCombatOnly(t *testing.T) {
	r, mg := catRig(t)
	r.g.setSelf(func(u *objects.Unit) {
		u.Auras = []objects.Aura{formAura(formTravel, drTravel), {Name: "Mark of the Wild", SpellID: drMotw5}}
	})
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "leave_form" || mg.cancelList()[0] != drTravel {
		t.Fatalf("%+v cancels %v", d, mg.cancelList())
	}
	r2, mg2 := catRig(t)
	r2.g.setSelf(func(u *objects.Unit) {
		u.Auras = []objects.Aura{formAura(formBear, drBear)}
		u.InCombat = true
	})
	tankFights(r2.g, 7, 4, 0)
	for i := 0; i < 3; i++ {
		if d := r2.tick(1600 * time.Millisecond); d != nil && d.Action == "leave_form" {
			t.Fatalf("in combat: %+v", d)
		}
	}
	if len(mg2.cancelList()) != 0 {
		t.Fatalf("cancels %v", mg2.cancelList())
	}
}

// ---- the bear ----

func bearRig(t *testing.T, known ...uint32) (*rig, *meleeGame) {
	t.Helper()
	if len(known) == 0 {
		known = drBearKnown
	}
	g := newFake(known...)
	druidSelf(g)
	g.self.Health, g.self.MaxHealth = 2000, 2000
	shiftTo(g, formDireBear, drDire)
	mg := newMeleeGame(g)
	return rigOn(t, mg, g, Params{Strategies: []string{"bear_tank_druid"}}), mg
}

func setRageB(g *fakeGame, tenths uint32) { g.setSelf(func(u *objects.Unit) { u.Power = tenths }) }

// bearFight puts a mob on the bear, 3 yards east, the bear swinging at it. The bear has 30 rage and Enrage up, so that a test starts from
// a quiet bear.
func bearFight(r *rig, mg *meleeGame) {
	m := mob(mobA, selfG)
	m.O = math.Pi // the mob faces the bear: the bear is the tank
	r.g.addUnit(m, 3, 0)
	r.g.setSelf(func(u *objects.Unit) {
		u.InCombat = true
		u.Power = 300
		u.Auras = append(u.Auras, objects.Aura{Name: "Enrage", SpellID: drEnrage})
	})
	mg.setSwing(mobA)
}

func TestBearShiftsIntoDireBearFormWhenItKnowsIt(t *testing.T) {
	g := newFake(drBearKnown...)
	druidSelf(g)
	mg := newMeleeGame(g)
	r := rigOn(t, mg, g, Params{Strategies: []string{"bear_tank_druid"}})
	g.addUnit(mob(mobA, selfG), 3, 0)
	g.setSelf(func(u *objects.Unit) { u.InCombat = true })
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "enter_form" || d.Spell != drDire {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// Before level 40 it is the plain Bear Form.
	g2 := newFake(drBear, drMaul, drGrowl)
	druidSelf(g2)
	mg2 := newMeleeGame(g2)
	r2 := rigOn(t, mg2, g2, Params{Strategies: []string{"bear_tank_druid"}})
	g2.addUnit(mob(mobA, selfG), 3, 0)
	g2.setSelf(func(u *objects.Unit) { u.InCombat = true })
	if d := r2.tick(100 * time.Millisecond); d == nil || d.Action != "enter_form" || d.Spell != drBear {
		t.Fatalf("%+v", d)
	}
}

func TestBearRotation(t *testing.T) {
	r, mg := bearRig(t)
	bearFight(r, mg)
	r.g.setSelf(func(u *objects.Unit) { u.Auras = u.Auras[:len(u.Auras)-1]; u.Power = 50 }) // 5 rage and no Enrage: the start of a fight
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "enrage" || d.Spell != drEnrage {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	selfAura(r.g, "Enrage", drEnrage)
	setRageB(r.g, 300)
	if d := r.tick(1600 * time.Millisecond); d != nil {
		t.Fatalf("30 rage and one mob: %+v", d)
	}
	setRageB(r.g, 400)
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "maul" || d.Spell != drMaul {
		t.Fatalf("40 rage: %+v", d)
	}
	if d := r.tick(400 * time.Millisecond); d != nil {
		t.Fatalf("the queued Maul was sent twice: %+v", d)
	}
}

func TestBearSwipesAndRoarsWithTwoMobsOnIt(t *testing.T) {
	r, mg := bearRig(t)
	bearFight(r, mg)
	r.g.addUnit(mob(mobB, selfG), 4, 1)
	d := r.tick(1600 * time.Millisecond)
	if d == nil || d.Action != "swipe" || d.Spell != drSwipe || d.Rel != RelNormal+5 {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	setRageB(r.g, 100) // the Swipe took its 20 rage: 10 are left, enough for the Roar
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "demoralizing_roar" || d.Spell != drRoar {
		t.Fatalf("%+v", d)
	}
	setRageB(r.g, 300)
	for _, id := range []packet.GUID{mobA, mobB} {
		r.g.mutate(id, func(u *objects.Unit) {
			u.Auras = append(u.Auras, objects.Aura{Name: "Demoralizing Roar", SpellID: drRoar, Harmful: true})
		})
	}
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "swipe" {
		t.Fatalf("%+v", d)
	}
}

func TestBearGrowlsAtALooseAddOnlyInBearForm(t *testing.T) {
	r, mg := bearRig(t)
	bearFight(r, mg)
	r.g.party = []Member{{Name: "Mage", GUID: dpsG1, Online: true}}
	r.g.addUnit(objects.Unit{GUID: dpsG1, Type: "player", Name: "Mage", Class: 8, InParty: true, Health: 500, MaxHealth: 500}, 6, 3)
	r.g.addUnit(mob(mobB, dpsG1), 4, 3) // 5 yards: Growl reaches 5
	d := r.tick(1600 * time.Millisecond)
	if d == nil || d.Action != "taunt" || d.Spell != drGrowl || d.Target != mobB {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// In another form the taunt is not possible (it would be refused): the druid cannot growl as a cat.
	r2, mg2 := bearRig(t)
	bearFight(r2, mg2)
	r2.g.setSelf(func(u *objects.Unit) {
		u.Auras = []objects.Aura{formAura(formCat, drCat), {Name: "Enrage", SpellID: drEnrage}}
		u.PowerType, u.Power, u.MaxPower = 3, 100, 100
	})
	r2.g.party = []Member{{Name: "Mage", GUID: dpsG1, Online: true}}
	r2.g.addUnit(objects.Unit{GUID: dpsG1, Type: "player", Name: "Mage", Class: 8, InParty: true, Health: 500, MaxHealth: 500}, 6, 3)
	r2.g.addUnit(mob(mobB, dpsG1), 4, 3)
	if d := r2.tick(1600 * time.Millisecond); d != nil && d.Action == "taunt" {
		t.Fatalf("%+v", d)
	}
}

func TestBearFrenziedRegenerationIsTheEmergency(t *testing.T) {
	r, mg := bearRig(t)
	bearFight(r, mg)
	r.g.setSelf(func(u *objects.Unit) { u.Health = 600; u.Power = 400 }) // 30%, 40 rage
	d := r.tick(1600 * time.Millisecond)
	if d == nil || d.Action != "frenzied_regeneration" || d.Spell != drFrenzied || d.Rel < RelInterrupt {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// Without the rage to turn into health it is no help, and at 40% health it is not yet the hour.
	r2, mg2 := bearRig(t)
	bearFight(r2, mg2)
	r2.g.setSelf(func(u *objects.Unit) { u.Health = 600; u.Power = 50 })
	if d := r2.tick(1600 * time.Millisecond); d != nil && d.Action == "frenzied_regeneration" {
		t.Fatalf("%+v", d)
	}
	r3, mg3 := bearRig(t)
	bearFight(r3, mg3)
	r3.g.setSelf(func(u *objects.Unit) { u.Health = 800; u.Power = 300 })
	if d := r3.tick(1600 * time.Millisecond); d != nil && d.Action == "frenzied_regeneration" {
		t.Fatalf("%+v", d)
	}
}

func TestBearNeverLeavesItsFormInCombat(t *testing.T) {
	r, mg := bearRig(t)
	bearFight(r, mg)
	r.g.items = []objects.Item{{Entry: itemSpringWater, Count: 5}, {Entry: itemBread, Count: 5}}
	setManaTrue(r.g, 5)
	r.g.setSelf(func(u *objects.Unit) { u.Health = 700 })
	for i := 0; i < 3; i++ {
		if d := r.tick(1600 * time.Millisecond); d != nil && (d.Action == "leave_form" || d.Action == "eat" || d.Action == "drink") {
			t.Fatalf("%+v", d)
		}
	}
	if len(mg.cancelList()) != 0 || len(r.g.used) != 0 {
		t.Fatalf("cancelled %v used %v", mg.cancelList(), r.g.used)
	}
}

// ---- the unit's mana outside caster form ----

func TestUnitManaWhileShapeshifted(t *testing.T) {
	g := newFake()
	druidSelf(g)
	shiftTo(g, formCat, drCat)
	g.setSelf(func(u *objects.Unit) { u.Mana, u.MaxMana = 300, 1000 })
	r := rigOn(t, g, g, Params{Strategies: []string{"frost_mage"}})
	c := r.b.newCtx(g.clk.Now())
	if pct, ok := manaPct(c); !ok || pct != 30 {
		t.Fatalf("%d %v", pct, ok)
	}
	g.setSelf(func(u *objects.Unit) { u.Mana, u.MaxMana = 0, 0 })
	c = r.b.newCtx(g.clk.Now())
	if _, ok := manaPct(c); ok {
		t.Fatal("a game that does not say has no answer")
	}
	g.setSelf(func(u *objects.Unit) { u.PowerType, u.Power, u.MaxPower = 0, 250, 1000 })
	c = r.b.newCtx(g.clk.Now())
	if pct, ok := manaPct(c); !ok || pct != 25 {
		t.Fatalf("%d %v", pct, ok)
	}
}
