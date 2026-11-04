package brain

import (
	"errors"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/spells"
)

// The spells of the shaman tests, with their template numbers (internal/spells/meta.tsv.gz; TestShamanSpellData checks the facts the
// brain relies on).
const (
	shHW1, shHW7                  = 331, 8005
	shLHW1, shLHW3                = 8004, 8010
	shChainHeal                   = 1064
	shCurePoison, shCureDisease   = 526, 2870
	shBolt1, shBolt7              = 403, 10391
	shChainLightning1, shChainLtn = 421, 930
	shFlame1, shFlame4            = 8050, 10447
	shEarth1, shEarth5            = 8042, 10412
	shLShield5                    = 8134
	shWShield1, shWShield2        = 45527, 51533
	shStormstrike                 = 17364
	shWindfuryW1, shWindfuryW2    = 8232, 8235
	shFlametongueW3, shRockbiter1 = 8030, 8017
	shSearing4, shFlametongueT2   = 6365, 8249
	shManaSpring2, shHealStream3  = 10495, 6377
	shStoneskin4, shStrength3     = 10406, 8161
	shWindfuryT                   = 8512
	toolEarth, toolFire           = 5175, 5176
	toolWater, toolAir            = 5177, 5178
)

var (
	shHealKnown = []uint32{shHW1, 332, 547, 913, 939, 959, shHW7, shLHW1, 8008, shLHW3, shChainHeal, shCurePoison, shCureDisease}
	shShields   = []uint32{324, 325, 905, 945, shLShield5, shWShield1, shWShield2}
	shRestoTot  = []uint32{5675, shManaSpring2, 5394, 6375, shHealStream3, 8071, 8154, 8155, shStoneskin4}
	shElemKnown = []uint32{shBolt1, 529, 548, 915, 943, 6041, shBolt7, shChainLightning1, shChainLtn, shFlame1, 8052, 8053, shFlame4, shEarth1, 8044, 8045, 8046,
		shEarth5, 3599, 6363, 6364, shSearing4, 8227, shFlametongueT2, 5675, shManaSpring2}
	shEnhKnown = []uint32{shStormstrike, shFlame1, 8052, 8053, shFlame4, shEarth1, 8044, 8045, 8046, shEarth5, shWindfuryW1, shWindfuryW2, 8024, 8027, shFlametongueW3,
		16339, shRockbiter1, 8018, 8019, 10399, 3599, 6363, 6364, shSearing4, shWindfuryT, 8075, 8160, shStrength3}
	allTools = []objects.Item{{Entry: toolEarth, Count: 1}, {Entry: toolFire, Count: 1}, {Entry: toolWater, Count: 1}, {Entry: toolAir, Count: 1}}
)

// shamanSelf makes the fake's player a level 40 shaman with a full pool of 1000 mana and 1200 health, with a Lightning Shield up.
func shamanSelf(g *fakeGame) {
	g.self.Class, g.self.Level = ClassShaman, 40
	g.self.Health, g.self.MaxHealth = 1200, 1200
	g.self.PowerType, g.self.Power, g.self.MaxPower = 0, 1000, 1000
	g.self.Auras = []objects.Aura{{Name: "Lightning Shield", SpellID: shLShield5}}
}

// ---- the spell data the shaman relies on ----

func TestShamanSpellData(t *testing.T) {
	// id, name, level, flat mana cost, percent cost, range max: read off the core's spell_template when the table was made.
	for _, c := range []struct {
		id        uint32
		name      string
		level     uint8
		cost, pct uint32
		rng       float32
	}{
		{shHW1, "Healing Wave", 1, 25, 0, 40}, {shHW7, "Healing Wave", 40, 340, 0, 40}, {shLHW1, "Lesser Healing Wave", 20, 105, 0, 40},
		{shChainHeal, "Chain Heal", 40, 260, 0, 40}, {shCurePoison, "Cure Poison", 16, 0, 9, 30}, {shCureDisease, "Cure Disease", 22, 0, 9, 30},
		{shBolt1, "Lightning Bolt", 1, 15, 0, 30}, {shChainLightning1, "Chain Lightning", 32, 280, 0, 30}, {shFlame1, "Flame Shock", 10, 55, 0, 20},
		{shEarth1, "Earth Shock", 4, 30, 0, 20}, {324, "Lightning Shield", 8, 40, 0, 0}, {shWShield1, "Water Shield", 26, 0, 0, 0},
		{shStormstrike, "Stormstrike", 30, 0, 10, 5}, {shRockbiter1, "Rockbiter Weapon", 1, 15, 0, 0}, {8024, "Flametongue Weapon", 10, 30, 0, 0},
		{shWindfuryW1, "Windfury Weapon", 30, 90, 0, 0},
		{3599, "Searing Totem", 10, 25, 0, 0}, {5675, "Mana Spring Totem", 26, 40, 0, 0}, {5394, "Healing Stream Totem", 20, 40, 0, 0},
		{8071, "Stoneskin Totem", 4, 30, 0, 0}, {8075, "Strength of Earth Totem", 10, 25, 0, 0}, {shWindfuryT, "Windfury Totem", 32, 115, 0, 0},
		{8227, "Flametongue Totem", 28, 90, 0, 0}, {8835, "Grace of Air Totem", 42, 155, 0, 0}, {2645, "Ghost Wolf", 20, 100, 0, 0},
	} {
		if got := spells.Name(c.id); got != c.name {
			t.Errorf("%d is %q, want %q", c.id, got, c.name)
			continue
		}
		m, ok := spells.Lookup(c.id)
		if !ok || m.Level != c.level || m.Cost != c.cost || m.CostPct != c.pct || m.RangeMax != c.rng {
			t.Errorf("%s: %+v", c.name, m)
		}
	}
	// The cast times the heals are chosen by: Chain Heal and the Healing Waves are slow, the Lesser Healing Wave is the quick one.
	for id, ms := range map[uint32]uint32{shHW1: 1500, shHW7: 3000, shLHW1: 1500, shChainHeal: 3000, shBolt7: 3000, shChainLtn: 2500} {
		if m, _ := spells.Lookup(id); m.CastMS != ms {
			t.Errorf("%s casts in %d ms, want %d", spells.Name(id), m.CastMS, ms)
		}
	}
	// The shocks share a cooldown, Earth Shock interrupts, Flame Shock is a damage over time, the cures dispel, the heals heal.
	if a, b := mustMeta(shFlame1), mustMeta(shEarth1); a.Category == 0 || a.Category != b.Category || !b.Flags.Has(spells.FlagInterrupt) {
		t.Errorf("Flame Shock %+v Earth Shock %+v", a, b)
	}
	if m := mustMeta(shEarth5); !m.Flags.Has(spells.FlagInterrupt) {
		t.Errorf("Earth Shock %+v", m)
	}
	for _, id := range []uint32{shCurePoison, shCureDisease} {
		if m := mustMeta(id); !m.Flags.Has(spells.FlagDispel) {
			t.Errorf("%s does not dispel: %v", spells.Name(id), m.Flags.Names())
		}
	}
	for _, id := range []uint32{shHW1, shLHW1, shChainHeal} {
		if m := mustMeta(id); !m.Flags.Has(spells.FlagHeal) || healDirectAmount(m, 40) == 0 || healAmount(m, 40) != healDirectAmount(m, 40) {
			t.Errorf("%s: %+v", spells.Name(id), m)
		}
	}
	// The imbues are enchant effects (SPELL_EFFECT_ENCHANT_ITEM_TEMPORARY) that last 3600 seconds: the base points plus one.
	for _, id := range []uint32{shRockbiter1, 8024, shWindfuryW1, shWindfuryW2} {
		m := mustMeta(id)
		if m.Effects[0].Effect != 54 || m.Effects[0].Base != 3599 {
			t.Errorf("%s: %+v", spells.Name(id), m.Effects)
		}
	}
	// Stormstrike needs a weapon in the hand and waits 8 s between two; no other spell of the preset does.
	if m := mustMeta(shStormstrike); m.Category == 0 || m.CategoryCDMS != 8000 {
		t.Errorf("Stormstrike %+v", m)
	}
}

func mustMeta(id uint32) spells.Meta { m, _ := spells.Lookup(id); return m }

// coreTotems is what the core's spell_template says about the totems the presets drop: every player rank, the slot its first effect fills
// (effect 87 fire, 88 earth, 89 water, 90 air: SPELL_EFFECT_SUMMON_TOTEM_SLOT1 to 4, Spell.h) and the totem tool its `totem1` names
// (tortoise-wow 5d79486, sql/base/tw_world_spell_template.sql). Mana Spring Totem 24854 and Windfury Totem 27621 need none.
var coreTotems = []struct {
	name string
	el   int
	tool uint32
	ids  []uint32
}{
	{"Searing Totem", elFire, toolFire, []uint32{3599, 6363, 6364, 6365, 10437, 10438}},
	{"Flametongue Totem", elFire, toolFire, []uint32{8227, 8249, 10526, 16387}},
	{"Mana Spring Totem", elWater, toolWater, []uint32{5675, 10495, 10496, 10497}},
	{"Healing Stream Totem", elWater, toolWater, []uint32{5394, 6375, 6377, 10462, 10463}},
	{"Stoneskin Totem", elEarth, toolEarth, []uint32{8071, 8154, 8155, 10406, 10407, 10408}},
	{"Strength of Earth Totem", elEarth, toolEarth, []uint32{8075, 8160, 8161, 10442, 25361}},
	{"Windfury Totem", elAir, toolAir, []uint32{shWindfuryT}},
	{"Grace of Air Totem", elAir, toolAir, []uint32{8835, 10627, 25359}},
}

func TestTotemSpellsFollowTheCoreData(t *testing.T) {
	for _, c := range coreTotems {
		for _, id := range c.ids {
			if spells.Name(id) != c.name {
				t.Errorf("%d is %q, want %q", id, spells.Name(id), c.name)
				continue
			}
			if el, ok := totemElement(mustMeta(id)); !ok || el != c.el {
				t.Errorf("%s %d fills slot %d (%v), want %s", c.name, id, el, ok, elementNames[c.el])
			}
			if totemTools[c.el] != c.tool {
				t.Errorf("%s: the table has tool %d for %s, the core says %d", c.name, totemTools[c.el], elementNames[c.el], c.tool)
			}
			if m := mustMeta(id); m.DurationMS <= 0 {
				t.Errorf("%s %d lasts %d ms", c.name, id, m.DurationMS)
			}
			if totemNoTool[id] {
				t.Errorf("%d is listed as needing no tool", id)
			}
		}
	}
	for id, el := range map[uint32]int{24854: elWater, 27621: elAir} {
		if got, ok := totemElement(mustMeta(id)); !ok || got != el || !totemNoTool[id] {
			t.Errorf("%d: slot %d %v, no-tool %v", id, got, ok, totemNoTool[id])
		}
	}
	// The lifetimes the manager reads off the spells.
	for id, d := range map[uint32]time.Duration{3599: 30 * time.Second, 5675: time.Minute, 8071: 2 * time.Minute, 8227: 2 * time.Minute} {
		if got := time.Duration(mustMeta(id).DurationMS) * time.Millisecond; got != d {
			t.Errorf("%s lasts %v, want %v", spells.Name(id), got, d)
		}
	}
	// A spell that is not a totem fills no slot; the aura spells that share a totem's name (Windfury Totem ranks 2 and 3 in this data) neither.
	for _, id := range []uint32{shHW1, 10608, 10610, 8516} {
		if _, ok := totemElement(mustMeta(id)); ok {
			t.Errorf("%s %d fills a slot", spells.Name(id), id)
		}
	}
}

// ---- the healer ----

func restoShamanRig(t *testing.T, known ...uint32) (*rig, *casterGame) {
	t.Helper()
	if len(known) == 0 {
		known = append(append(append([]uint32{}, shHealKnown...), shShields...), shRestoTot...)
	}
	g := newCasterGame(known...)
	shamanSelf(g.fakeGame)
	g.self.InCombat = true
	g.self.Auras = []objects.Aura{{Name: "Water Shield", SpellID: shWShield2}}
	g.items = append([]objects.Item{}, allTools...)
	return rigOn(t, g, g.fakeGame, Params{Strategies: []string{"resto_shaman"}}), g
}

func TestRestoShamanHealsWithHealingWaveAndTheQuickOneInAnEmergency(t *testing.T) {
	r, g := restoShamanRig(t)
	g.member(dpsG1, "Mage", 8, 500, 1000, 5, 0)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "heal" || d.Target != dpsG1 || spells.Name(d.Spell) != "Healing Wave" {
		t.Fatalf("%+v", d)
	}
	r2, g2 := restoShamanRig(t)
	g2.member(dpsG1, "Mage", 8, 150, 1000, 5, 0)
	d = r2.tick(100 * time.Millisecond)
	if d == nil || d.Action != "heal" || d.Spell != shLHW3 || d.Rel < RelInterrupt {
		t.Fatalf("%+v", d)
	}
}

func TestRestoShamanChainHealsWhenSeveralAreHurt(t *testing.T) {
	r, g := restoShamanRig(t)
	g.member(dpsG1, "Mage", 8, 700, 1000, 5, 0)
	g.member(dpsG2, "Rogue", 4, 750, 1000, 5, 3)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "heal" || d.Spell != shChainHeal || d.Target != dpsG1 {
		t.Fatalf("%+v", d)
	}
	// One member hurt: the plain heal, Chain Heal costs more for the same health.
	r2, g2 := restoShamanRig(t)
	g2.member(dpsG1, "Mage", 8, 700, 1000, 5, 0)
	g2.member(dpsG2, "Rogue", 4, 1000, 1000, 5, 3)
	if d := r2.tick(100 * time.Millisecond); d == nil || d.Action != "heal" || spells.Name(d.Spell) != "Healing Wave" {
		t.Fatalf("%+v", d)
	}
	// Two hurt but one is in an emergency: the quick heal, not the slow one on three.
	r3, g3 := restoShamanRig(t)
	g3.member(dpsG1, "Mage", 8, 150, 1000, 5, 0)
	g3.member(dpsG2, "Rogue", 4, 750, 1000, 5, 3)
	if d := r3.tick(100 * time.Millisecond); d == nil || d.Spell != shLHW3 {
		t.Fatalf("%+v", d)
	}
	// Before level 40 there is no Chain Heal to choose.
	r4, g4 := restoShamanRig(t, append(append([]uint32{}, shHW1, 332, 547, 913, 939, 959), shWShield1)...)
	g4.member(dpsG1, "Mage", 8, 700, 1000, 5, 0)
	g4.member(dpsG2, "Rogue", 4, 750, 1000, 5, 3)
	if d := r4.tick(100 * time.Millisecond); d == nil || spells.Name(d.Spell) != "Healing Wave" {
		t.Fatalf("%+v", d)
	}
}

func TestRestoShamanHasNoHealOverTimeForTheTank(t *testing.T) {
	r, g := restoShamanRig(t)
	g.member(tankG, "Tankard", 1, 960, 1000, 6, 0)
	if d := r.tick(100 * time.Millisecond); d != nil && d.Action != "drop_totem" {
		t.Fatalf("%+v", d)
	}
}

func TestRestoShamanCuresPoisonAndDisease(t *testing.T) {
	r, g := restoShamanRig(t)
	g.member(dpsG1, "Mage", 8, 1000, 1000, 5, 0)
	g.addAura(dpsG1, objects.Aura{Name: "Deadly Poison", SpellID: auraPoison, Harmful: true})
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "dispel" || d.Spell != shCurePoison || d.Target != dpsG1 {
		t.Fatalf("%+v", d)
	}
	r2, g2 := restoShamanRig(t)
	g2.member(dpsG1, "Mage", 8, 1000, 1000, 5, 0)
	g2.addAura(dpsG1, objects.Aura{Name: "Rabies", SpellID: auraRabies, Harmful: true})
	if d := r2.tick(100 * time.Millisecond); d == nil || d.Action != "dispel" || d.Spell != shCureDisease {
		t.Fatalf("%+v", d)
	}
	// Magic and curses are not for a shaman.
	r3, g3 := restoShamanRig(t)
	g3.member(dpsG1, "Mage", 8, 1000, 1000, 5, 0)
	g3.addAura(dpsG1, objects.Aura{Name: "Curse of Agony", SpellID: auraCurse, Harmful: true})
	if d := r3.tick(100 * time.Millisecond); d != nil && d.Action == "dispel" {
		t.Fatalf("%+v", d)
	}
}

func TestRestoShamanHealsBeforeItDropsATotem(t *testing.T) {
	r, g := restoShamanRig(t)
	g.member(dpsG1, "Mage", 8, 500, 1000, 5, 0)
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "heal" {
		t.Fatalf("%+v", d)
	}
	g.setHP(dpsG1, 1000)
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "drop_totem" || d.Rel != RelNormal-3 {
		t.Fatalf("%+v", d)
	}
}

// ---- the shield ----

func TestShamanShieldPicksWaterShieldForAHealer(t *testing.T) {
	r, g := restoShamanRig(t)
	g.self.InCombat = false
	g.setSelf(func(u *objects.Unit) { u.Auras = []objects.Aura{} })
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "shield" || d.Spell != shWShield2 || d.Target != selfG {
		t.Fatalf("%+v", d)
	}
	selfAura(g.fakeGame, "Water Shield", shWShield2)
	if d := r.tick(2 * time.Second); d != nil {
		t.Fatalf("it is up: %+v", d)
	}
	// A healer who has no Water Shield yet keeps a Lightning Shield; a damage dealer never takes the Water Shield.
	r2, g2 := restoShamanRig(t, append(append([]uint32{}, shHealKnown...), 324, 325)...)
	g2.self.InCombat = false
	g2.setSelf(func(u *objects.Unit) { u.Auras = []objects.Aura{} })
	if d := r2.tick(100 * time.Millisecond); d == nil || d.Action != "shield" || spells.Name(d.Spell) != "Lightning Shield" {
		t.Fatalf("%+v", d)
	}
	g3 := newCasterGame(append(append([]uint32{}, shElemKnown...), shShields...)...)
	shamanSelf(g3.fakeGame)
	g3.setSelf(func(u *objects.Unit) { u.Auras = []objects.Aura{} })
	r3 := rigOn(t, g3, g3.fakeGame, Params{Strategies: []string{"elemental_shaman"}})
	if d := r3.tick(100 * time.Millisecond); d == nil || d.Action != "shield" || d.Spell != shLShield5 {
		t.Fatalf("%+v", d)
	}
}

func TestShamanShieldInCombatNeedsMana(t *testing.T) {
	g := newCasterGame(append(append([]uint32{}, shElemKnown...), shShields...)...)
	shamanSelf(g.fakeGame)
	g.self.InCombat = true
	g.setSelf(func(u *objects.Unit) { u.Auras = []objects.Aura{}; u.Power = 400 })
	r := rigOn(t, g, g.fakeGame, Params{Strategies: []string{"elemental_shaman"}})
	if d := r.tick(100 * time.Millisecond); d != nil {
		t.Fatalf("40%% mana: %+v", d)
	}
	g.setMana(80)
	if d := r.tick(2 * time.Second); d == nil || d.Action != "shield" || d.Rel != RelNormal-2 {
		t.Fatalf("%+v", d)
	}
}

// ---- totems ----

func elementalRig(t *testing.T, known ...uint32) (*rig, *casterGame) {
	t.Helper()
	if len(known) == 0 {
		known = append(append([]uint32{}, shElemKnown...), shShields...)
	}
	g := newCasterGame(known...)
	shamanSelf(g.fakeGame)
	g.items = append([]objects.Item{}, allTools...)
	tankFights(g.fakeGame, 8, 12, 0)
	return rigOn(t, g, g.fakeGame, Params{Strategies: []string{"elemental_shaman"}}), g
}

// totemRig is an elemental shaman that knows the totems and nothing else, so that the rotation stays out of the way.
func totemRig(t *testing.T) (*rig, *casterGame) {
	t.Helper()
	return elementalRig(t, 3599, 6363, 6364, shSearing4, 5675, shManaSpring2, shLShield5)
}

// totemCasts are the totem spells among the casts so far.
func totemCasts(g *fakeGame) []uint32 {
	var out []uint32
	for _, c := range g.castList() {
		if _, ok := totemElement(mustMeta(c.spell)); ok {
			out = append(out, c.spell)
		}
	}
	return out
}

// totemUnit is the creature a totem leaves, standing 2 yards from where the shaman dropped it.
func totemUnit(g *fakeGame, guid packet.GUID, name string) {
	g.addUnit(objects.Unit{GUID: guid, Type: "creature", Name: name, Health: 5, MaxHealth: 5}, 2, 0)
}

const (
	searingG = packet.GUID(0xF1000000A1)
	springG  = packet.GUID(0xF1000000A2)
)

// dropBoth runs the two drops of the elemental preset, the creatures standing after each: Searing Totem (45 s at rank 4), then Mana Spring Totem (60 s).
func dropBoth(r *rig, g *casterGame) {
	r.tick(100 * time.Millisecond)
	totemUnit(g.fakeGame, searingG, "Searing Totem IV")
	r.tick(1600 * time.Millisecond)
	totemUnit(g.fakeGame, springG, "Mana Spring Totem II")
}

func removeUnit(g *casterGame, guid packet.GUID) {
	g.fakeGame.mu.Lock()
	defer g.fakeGame.mu.Unlock()
	var keep []objects.Unit
	for _, u := range g.fakeGame.units {
		if u.GUID != guid {
			keep = append(keep, u)
		}
	}
	g.fakeGame.units = keep
}

func TestTotemsAreDroppedAtTheStartOfAFightOnePerElement(t *testing.T) {
	r, g := totemRig(t)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "drop_totem" || d.Spell != shSearing4 || d.Target != selfG || d.Rel != RelNormal+1 {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	totemUnit(g.fakeGame, searingG, "Searing Totem IV")
	d = r.tick(1600 * time.Millisecond)
	if d == nil || d.Action != "drop_totem" || d.Spell != shManaSpring2 {
		t.Fatalf("%+v", d)
	}
	totemUnit(g.fakeGame, springG, "Mana Spring Totem II")
	// Nothing else is wanted: the elemental has two, and both last longer than this.
	for i := 0; i < 16; i++ {
		if d := r.tick(2500 * time.Millisecond); d != nil {
			t.Fatalf("tick %d: %+v", i, d)
		}
	}
	if got := totemCasts(g.fakeGame); !eq(got, []uint32{shSearing4, shManaSpring2}) {
		t.Fatalf("totems %v", got)
	}
}

func TestTotemsAreNotSpammed(t *testing.T) {
	// The creatures never show (the server refused, or the name is not what the manager looks for): within the grace they are trusted,
	// later they are gone, and each slot is dropped again but not before the gap of ten seconds from its last drop.
	r, g := totemRig(t)
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "drop_totem" || d.Spell != shSearing4 {
		t.Fatalf("%+v", d)
	}
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "drop_totem" || d.Spell != shManaSpring2 {
		t.Fatalf("the water slot comes next: %+v", d)
	}
	for _, at := range []time.Duration{1 * time.Second, 2 * time.Second, 2 * time.Second} { // to 6.7 s
		if d := r.tick(at); d != nil && d.Action == "drop_totem" {
			t.Fatalf("a second drop within the gap: %+v", d)
		}
	}
	if d := r.tick(4 * time.Second); d == nil || d.Action != "drop_totem" || d.Spell != shSearing4 {
		t.Fatalf("%+v", d)
	}
	if got := totemCasts(g.fakeGame); len(got) != 3 {
		t.Fatalf("totems %v", got)
	}
}

func TestTotemIsDroppedAgainWhenTheCreatureIsGone(t *testing.T) {
	r, g := totemRig(t)
	dropBoth(r, g)
	r.tick(5 * time.Second)
	removeUnit(g, searingG) // the fire totem dies: no creature of its name stands at its place any more
	d := r.tick(5 * time.Second)
	if d == nil || d.Action != "drop_totem" || d.Spell != shSearing4 {
		t.Fatalf("%+v", d)
	}
	if got := totemCasts(g.fakeGame); !eq(got, []uint32{shSearing4, shManaSpring2, shSearing4}) {
		t.Fatalf("totems %v", got)
	}
}

func TestTotemIsDroppedAgainWhenItsLifetimeIsOver(t *testing.T) {
	r, g := totemRig(t)
	dropBoth(r, g) // at 0.1 and 1.7 s
	for i := 0; i < 16; i++ {
		r.tick(2500 * time.Millisecond) // to 41.8 s: both stand
	}
	if got := totemCasts(g.fakeGame); len(got) != 2 {
		t.Fatalf("totems %v", got)
	}
	// Searing Totem rank 4 lasts 45 s: with its creature still standing, it is replaced.
	d := r.tick(5 * time.Second)
	if d == nil || d.Action != "drop_totem" || d.Spell != shSearing4 {
		t.Fatalf("%+v", d)
	}
	// Mana Spring Totem lasts 60 s: at 61.8 s, also with its creature standing.
	for i := 0; i < 5; i++ {
		if d := r.tick(2500 * time.Millisecond); d != nil {
			t.Fatalf("tick %d: %+v", i, d)
		}
	}
	d = r.tick(5 * time.Second)
	if d == nil || d.Action != "drop_totem" || d.Spell != shManaSpring2 {
		t.Fatalf("%+v", d)
	}
}

func TestTotemsLeftBehindAreDroppedAgain(t *testing.T) {
	r, g := totemRig(t)
	dropBoth(r, g)
	r.tick(9 * time.Second)
	// The party moves 40 yards on; the totems stand where they were.
	g.setSelf(func(u *objects.Unit) { u.X = 40 })
	g.mutate(tankG, func(u *objects.Unit) { u.X = 48 })
	g.mutate(mobA, func(u *objects.Unit) { u.X = 52 })
	g.mutate(searingG, func(u *objects.Unit) {})
	g.mutate(springG, func(u *objects.Unit) {})
	d := r.tick(2 * time.Second)
	if d == nil || d.Action != "drop_totem" || d.Spell != shSearing4 {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	totemUnit(g.fakeGame, 0xF1000000A3, "Searing Totem IV")
	g.mutate(0xF1000000A3, func(u *objects.Unit) { u.X = 42 })
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "drop_totem" || d.Spell != shManaSpring2 {
		t.Fatalf("%+v", d)
	}
}

func TestTotemCreatureNamesAreRecognisedLoosely(t *testing.T) {
	for _, c := range []struct {
		creature, spell string
		want            bool
	}{
		{"Flametongue Totem II", "Flametongue Totem", true}, {"Mana Spring Totem", "Mana Spring Totem", true}, {"Totem of Searing", "Searing Totem", true},
		{"Strength of Earth Totem III", "Strength of Earth Totem", true}, {"", "Searing Totem", true}, // the name query is not answered yet
		{"Stoneskin Totem IV", "Searing Totem", false}, {"Murloc", "Searing Totem", false}, {"Searing Flame", "Searing Totem", false},
	} {
		if got := totemNamed(c.creature, c.spell); got != c.want {
			t.Errorf("%q for %q: %v, want %v", c.creature, c.spell, got, c.want)
		}
	}
	// A creature whose name is not known yet is taken for the totem: no drop again and again while the name query is out.
	r, g := totemRig(t)
	r.tick(100 * time.Millisecond)
	totemUnit(g.fakeGame, searingG, "")
	r.tick(1600 * time.Millisecond)
	totemUnit(g.fakeGame, springG, "")
	for i := 0; i < 12; i++ {
		r.tick(2500 * time.Millisecond)
	}
	if got := totemCasts(g.fakeGame); !eq(got, []uint32{shSearing4, shManaSpring2}) {
		t.Fatalf("totems %v", got)
	}
	// A hostile creature at the place is not the totem.
	r2, g2 := totemRig(t)
	r2.tick(100 * time.Millisecond)
	g2.addUnit(mob(mobB+9, tankG), 2, 0)
	g2.mutate(mobB+9, func(u *objects.Unit) { u.Name = "Searing Totem IV" })
	r2.tick(1600 * time.Millisecond)
	for i := 0; i < 5; i++ {
		r2.tick(2500 * time.Millisecond)
	}
	if got := totemCasts(g2.fakeGame); len(got) < 3 {
		t.Fatalf("a hostile creature stood in for the totem: %v", got)
	}
}

func TestNoTotemsWithoutTheToolOrTheMana(t *testing.T) {
	r, g := totemRig(t)
	g.items = []objects.Item{{Entry: toolWater, Count: 1}} // only the water tool
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "drop_totem" || d.Spell != shManaSpring2 {
		t.Fatalf("%+v", d)
	}
	r2, g2 := totemRig(t)
	g2.items = nil
	if d := r2.tick(100 * time.Millisecond); d != nil {
		t.Fatalf("no tools: %+v", d)
	}
	r3, g3 := totemRig(t)
	g3.setMana(20)
	if d := r3.tick(100 * time.Millisecond); d != nil {
		t.Fatalf("20%% mana: %+v", d)
	}
	// A totem that needs no tool is dropped without one.
	r4, g4 := elementalRig(t, 24854, shLShield5)
	g4.items = nil
	if d := r4.tick(100 * time.Millisecond); d == nil || d.Action != "drop_totem" || d.Spell != 24854 {
		t.Fatalf("%+v", d)
	}
}

func TestNoTotemsAwayFromTheTankOrForADyingMobOrOutOfAFight(t *testing.T) {
	r, g := totemRig(t)
	g.mutate(tankG, func(u *objects.Unit) { u.X = 35 })
	if d := r.tick(100 * time.Millisecond); d != nil {
		t.Fatalf("the tank is 35 yards off: %+v", d)
	}
	r2, g2 := totemRig(t)
	setMobHP(g2.fakeGame, mobA, 20)
	if d := r2.tick(100 * time.Millisecond); d != nil {
		t.Fatalf("a mob nearly dead: %+v", d)
	}
	g3 := newCasterGame(3599, 6365, shLShield5)
	shamanSelf(g3.fakeGame)
	g3.items = allTools
	r3 := rigOn(t, g3, g3.fakeGame, Params{Strategies: []string{"elemental_shaman"}})
	if d := r3.tick(100 * time.Millisecond); d != nil {
		t.Fatalf("no fight: %+v", d)
	}
	// A shaman with no tank in view (alone) drops its totems when a fight is on.
	g4 := newCasterGame(3599, 6365, shLShield5)
	shamanSelf(g4.fakeGame)
	g4.self.InCombat = true
	g4.items = allTools
	r4 := rigOn(t, g4, g4.fakeGame, Params{Strategies: []string{"elemental_shaman"}})
	g4.addUnit(mob(mobA, selfG), 25, 0)
	if d := r4.tick(100 * time.Millisecond); d == nil || d.Action != "drop_totem" || d.Spell != 6365 {
		t.Fatalf("%+v", d)
	}
}

func TestEnhancementDropsItsTotems(t *testing.T) {
	r, mg := enhRig(t, shStormstrike, 3599, 6365, shWindfuryT, 8161, 8075, shLShield5)
	enhFight(r, mg)
	r.g.items = append([]objects.Item{}, allTools...)
	var got []uint32
	for i := 0; i < 5; i++ {
		if d := r.tick(1600 * time.Millisecond); d != nil && d.Action == "drop_totem" {
			got = append(got, d.Spell)
		}
	}
	// Fire, then air, then earth, as the table orders them; the first of each list the spellbook has.
	if !eq(got, []uint32{6365, shWindfuryT, 8161}) {
		t.Fatalf("totems %v casts %v", got, spellSeq(r.g))
	}
}

// ---- the elemental shaman ----

func TestElementalRotation(t *testing.T) {
	r, g := elementalRig(t)
	g.items = nil // no totems here: the rotation alone
	r.tick(100 * time.Millisecond)
	d := open(r)
	if d == nil || d.Action != "flame_shock" || d.Spell != shFlame4 || d.Target != mobA {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	mobAura(g.fakeGame, "Flame Shock", shFlame4)
	d = r.tick(1600 * time.Millisecond)
	if d == nil || d.Action != "lightning_bolt" || d.Spell != shBolt7 {
		t.Fatalf("%+v", d)
	}
	// The damage over time is not put on a mob nearly dead.
	r2, g2 := elementalRig(t)
	g2.items = nil
	r2.tick(100 * time.Millisecond)
	setMobHP(g2.fakeGame, mobA, 35)
	if d := open(r2); d == nil || d.Action != "lightning_bolt" {
		t.Fatalf("%+v", d)
	}
	// ... and not with the mana gone.
	r3, g3 := elementalRig(t)
	g3.items = nil
	g3.setMana(20)
	r3.tick(100 * time.Millisecond)
	if d := open(r3); d == nil || d.Action != "lightning_bolt" {
		t.Fatalf("%+v", d)
	}
}

func TestElementalChainLightningNeedsTwoMobsAndTheVetoAllows(t *testing.T) {
	r, g := elementalRig(t)
	g.items = nil
	g.addUnit(mob(mobB, tankG), 15, 2) // next to the tank's mob
	mobAura(g.fakeGame, "Flame Shock", shFlame4)
	r.tick(100 * time.Millisecond)
	d := open(r)
	if d == nil || d.Action != "chain_lightning" || d.Spell != shChainLtn {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// One mob: the bolt. A mob 20 yards from the other is not a pack.
	r2, g2 := elementalRig(t)
	g2.items = nil
	mobAura(g2.fakeGame, "Flame Shock", shFlame4)
	g2.addUnit(mob(mobB, tankG), 34, 0)
	r2.tick(100 * time.Millisecond)
	if d := open(r2); d == nil || d.Action != "lightning_bolt" {
		t.Fatalf("%+v", d)
	}
	// A mob that no one has picked, near the shaman: the area spell would pull it, and the veto says no.
	r3, g3 := elementalRig(t)
	g3.items = nil
	g3.addUnit(mob(mobB, tankG), 15, 2)
	mobAura(g3.fakeGame, "Flame Shock", shFlame4)
	loose := mob(mobB+1, 0)
	loose.InCombat = false
	g3.addUnit(loose, 8, 3)
	r3.tick(100 * time.Millisecond)
	d = open(r3)
	if d == nil || d.Action != "lightning_bolt" {
		t.Fatalf("%+v idle %q", d, r3.idle())
	}
}

func TestElementalCastsNoBoltOnTheMove(t *testing.T) {
	r, g := elementalRig(t)
	g.items = nil
	r.tick(100 * time.Millisecond)
	g.setSelf(func(u *objects.Unit) { u.Moving = true })
	d := open(r)
	if d == nil || d.Action != "flame_shock" {
		t.Fatalf("an instant goes on the move: %+v", d)
	}
	mobAura(g.fakeGame, "Flame Shock", shFlame4)
	if d := r.tick(1600 * time.Millisecond); d != nil {
		t.Fatalf("a bolt on the move: %+v", d)
	}
}

func TestElementalInterruptsWithEarthShock(t *testing.T) {
	r, g := elementalRig(t)
	g.items = nil
	r.tick(100 * time.Millisecond)
	caster := mob(mobB, tankG)
	caster.Casting = &objects.Cast{SpellID: 2050, Target: mobA, EndsInMS: ms(1500)} // a Lesser Heal
	g.addUnit(caster, 12, 5)
	d := r.tick(300 * time.Millisecond)
	if d == nil || d.Action != "interrupt" || d.Spell != shEarth5 || d.Target != mobB || d.Rel < RelInterrupt {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// Out of its range of 20 yards it waits.
	r2, g2 := elementalRig(t)
	g2.items = nil
	r2.tick(100 * time.Millisecond)
	far := mob(mobB, tankG)
	far.Casting = &objects.Cast{SpellID: 2050, Target: mobA, EndsInMS: ms(1500)}
	g2.addUnit(far, 28, 0)
	if d := r2.tick(300 * time.Millisecond); d != nil && d.Action == "interrupt" {
		t.Fatalf("%+v", d)
	}
}

// ---- the enhancement shaman ----

func enhRig(t *testing.T, known ...uint32) (*rig, *meleeGame) {
	t.Helper()
	if len(known) == 0 {
		known = append(append([]uint32{}, shEnhKnown...), shShields...)
	}
	g := newFake(known...)
	shamanSelf(g)
	mg := newMeleeGame(g)
	return rigOn(t, mg, g, Params{Strategies: []string{"enhancement_shaman"}}), mg
}

// enhFight is the standard melee fight: the tank holds a mob 4 yards east of the shaman, the mob faces away from it, the shaman swings.
func enhFight(r *rig, mg *meleeGame) {
	tankFights(r.g, 7, 4, 0)
	mg.setSwing(mobA)
	r.g.setSelf(func(u *objects.Unit) { u.InCombat = true })
	r.tick(100 * time.Millisecond)
}

func TestEnhancementRotation(t *testing.T) {
	r, mg := enhRig(t, shStormstrike, shFlame1, 8052, 8053, shFlame4, shEarth1, 8044, 8045, 8046, shEarth5, shLShield5)
	enhFight(r, mg)
	d := open(r)
	if d == nil || d.Action != "stormstrike" || d.Spell != shStormstrike || d.Target != mobA {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if len(mg.walkList()) != 0 {
		t.Fatalf("it stands behind the mob and had no reason to walk: %v", mg.walkList())
	}
	// Stormstrike waits for its cooldown; Flame Shock is next, then (their cooldown shared) Earth Shock after six seconds, once the
	// Stormstrike that is ready again after eight has gone.
	d = r.tick(1600 * time.Millisecond)
	if d == nil || d.Action != "flame_shock" || d.Spell != shFlame4 {
		t.Fatalf("%+v", d)
	}
	mobAura(r.g, "Flame Shock", shFlame4)
	for i := 0; i < 2; i++ {
		if d := r.tick(2900 * time.Millisecond); d != nil {
			t.Fatalf("the shocks share a cooldown: %+v", d)
		}
	}
	if d := r.tick(2900 * time.Millisecond); d == nil || d.Action != "stormstrike" {
		t.Fatalf("eight seconds on: %+v", d)
	}
	d = r.tick(1600 * time.Millisecond)
	if d == nil || d.Action != "earth_shock" || d.Spell != shEarth5 {
		t.Fatalf("%+v", d)
	}
}

func TestEnhancementKeepsItsManaForThePartyBelowSixty(t *testing.T) {
	r, mg := enhRig(t, shFlame4, shEarth5, shLShield5)
	enhFight(r, mg)
	mobAura(r.g, "Flame Shock", shFlame4)
	r.g.setMana(50)
	if d := open(r); d != nil {
		t.Fatalf("50%% mana: %+v", d)
	}
	r.g.setMana(70)
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "earth_shock" {
		t.Fatalf("%+v", d)
	}
}

func TestEnhancementWithoutStormstrikeShocksAndSwings(t *testing.T) {
	r, mg := enhRig(t, shFlame1, shLShield5)
	enhFight(r, mg)
	if d := open(r); d == nil || d.Action != "flame_shock" || d.Spell != shFlame1 {
		t.Fatalf("%+v", d)
	}
	// With no swing under way the first thing is the attack.
	r2, mg2 := enhRig(t, shFlame1, shLShield5)
	tankFights(r2.g, 7, 4, 0)
	r2.g.setSelf(func(u *objects.Unit) { u.InCombat = true })
	r2.tick(100 * time.Millisecond)
	if d := open(r2); d == nil || d.Action != "auto_attack" || len(mg2.targets) != 1 || mg2.attacks != 1 {
		t.Fatalf("%+v", d)
	}
}

// ---- the weapon imbue ----

func imbueRig(t *testing.T, known ...uint32) (*rig, *meleeGame) {
	t.Helper()
	r, mg := enhRig(t, known...)
	return r, mg
}

func TestImbueOnceOutOfCombatAndRenewedBeforeItEnds(t *testing.T) {
	r, mg := imbueRig(t)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "imbue" || d.Spell != shWindfuryW2 {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if got := mg.imbueList(); !eq(got, []uint32{shWindfuryW2}) {
		t.Fatalf("imbues %v", got)
	}
	if len(spellSeq(r.g)) != 0 {
		t.Fatalf("the imbue is not a cast at a unit: %v", spellSeq(r.g))
	}
	// Not again for 55 minutes, then again.
	if d := r.tick(10 * time.Minute); d != nil {
		t.Fatalf("%+v", d)
	}
	if d := r.tick(40 * time.Minute); d != nil {
		t.Fatalf("%+v", d)
	}
	if d := r.tick(6 * time.Minute); d == nil || d.Action != "imbue" || len(mg.imbueList()) != 2 {
		t.Fatalf("%+v imbues %v", d, mg.imbueList())
	}
}

func TestImbueTakesTheBestOneTheSpellbookHas(t *testing.T) {
	r, mg := imbueRig(t, shFlametongueW3, 8024, shRockbiter1, shLShield5)
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "imbue" || d.Spell != shFlametongueW3 {
		t.Fatalf("%+v", d)
	}
	r2, mg2 := imbueRig(t, shRockbiter1, 8018, 8019, 10399, shLShield5)
	if d := r2.tick(100 * time.Millisecond); d == nil || d.Spell != 10399 || len(mg2.imbueList()) != 1 {
		t.Fatalf("%+v", d)
	}
	if len(mg.imbueList()) != 1 {
		t.Fatalf("%v", mg.imbueList())
	}
	r3, mg3 := imbueRig(t, shLShield5)
	if d := r3.tick(100 * time.Millisecond); d != nil || len(mg3.imbueList()) != 0 {
		t.Fatalf("no imbue known: %+v", d)
	}
}

func TestImbueNeitherInCombatNorOnTheWalkNorWithLittleMana(t *testing.T) {
	r, mg := imbueRig(t)
	r.g.setSelf(func(u *objects.Unit) { u.InCombat = true })
	if d := r.tick(100 * time.Millisecond); d != nil && d.Action == "imbue" {
		t.Fatalf("%+v", d)
	}
	r.g.setSelf(func(u *objects.Unit) { u.InCombat = false })
	r.b.restOK = func() bool { return false }
	if d := r.tick(2 * time.Second); d != nil {
		t.Fatalf("%+v", d)
	}
	r.b.restOK = func() bool { return true }
	r.g.setMana(20)
	if d := r.tick(2 * time.Second); d != nil && d.Action == "imbue" {
		t.Fatalf("%+v", d)
	}
	if len(mg.imbueList()) != 0 {
		t.Fatalf("%v", mg.imbueList())
	}
}

func TestImbueRefusedIsTriedAgainAfterAMinute(t *testing.T) {
	r, mg := imbueRig(t)
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "imbue" {
		t.Fatalf("%+v", d)
	}
	// The server says there is no weapon: the spell is held back and the imbue is forgotten.
	r.g.st.CastRefused(shWindfuryW2, codeOf(t, "item_gone"))
	if d := r.tick(2 * time.Second); d != nil {
		t.Fatalf("%+v", d)
	}
	if d := r.tick(30 * time.Second); d != nil {
		t.Fatalf("%+v", d)
	}
	if d := r.tick(40 * time.Second); d == nil || d.Action != "imbue" || len(mg.imbueList()) != 2 {
		t.Fatalf("%+v imbues %v", d, mg.imbueList())
	}
	// The game itself may refuse (no weapon in the hand): the next try is a minute away.
	r2, mg2 := imbueRig(t)
	mg2.imbueErr = errNoWeapon
	if d := r2.tick(100 * time.Millisecond); d == nil || d.Result != ResultError {
		t.Fatalf("%+v", d)
	}
	for i := 0; i < 5; i++ {
		if d := r2.tick(5 * time.Second); d != nil && d.Action == "imbue" {
			t.Fatalf("tick %d: %+v", i, d)
		}
	}
	mg2.imbueErr = nil
	if d := r2.tick(40 * time.Second); d == nil || d.Action != "imbue" || len(mg2.imbueList()) != 1 {
		t.Fatalf("%+v", d)
	}
}

var errNoWeapon = errors.New("no weapon in the main hand")

func TestAGameThatCannotImbueNeverImbues(t *testing.T) {
	g := newFake(append(append([]uint32{}, shEnhKnown...), shShields...)...)
	shamanSelf(g)
	mg := newMeleeGame(g)
	r := rigOn(t, struct{ Game }{mg}, g, Params{Strategies: []string{"enhancement_shaman"}})
	if d := r.tick(100 * time.Millisecond); d != nil {
		t.Fatalf("%+v", d)
	}
	if len(mg.imbueList()) != 0 {
		t.Fatalf("%v", mg.imbueList())
	}
}

func TestImbueDurationIsReadOffTheSpell(t *testing.T) {
	r, _ := imbueRig(t)
	c := r.b.newCtx(r.g.clk.Now())
	id, life, ok := imbueSpellNow(c)
	if !ok || id != shWindfuryW2 || life != time.Hour {
		t.Fatalf("%d %v %v", id, life, ok)
	}
}

// ---- the class tables ----

func TestShamanAndDruidRowsHealThroughTheKit(t *testing.T) {
	for id, kit := range map[uint8]*HealKit{ClassShaman: &shamanHeals, ClassDruid: &druidHeals} {
		ci := ClassOf(id)
		if ci.Heals != kit {
			t.Errorf("%s heals with %+v", ci.Name, ci.Heals)
		}
		for _, n := range append(append(append([]string{}, kit.Direct...), kit.Multi...), kit.HoT) {
			if n == "" {
				continue
			}
			found := false
			for sid := uint32(1); sid < 60000 && !found; sid++ {
				found = spells.Name(sid) == n
			}
			if !found {
				t.Errorf("%s heals with %q, which no spell is called", ci.Name, n)
			}
		}
		for _, n := range kit.Fast {
			if !contains(kit.Direct, n) {
				t.Errorf("%s: %q is fast but not a direct heal", ci.Name, n)
			}
		}
		for _, n := range kit.DirectOnly {
			if !contains(kit.Direct, n) {
				t.Errorf("%s: %q is direct-only but not a direct heal", ci.Name, n)
			}
		}
	}
	if got := ClassOf(ClassDruid).Taunt; got != "Growl" {
		t.Errorf("druid taunt %q", got)
	}
	if got := ClassOf(ClassShaman).Interrupts; len(got) != 1 || got[0].Spell != "Earth Shock" {
		t.Errorf("shaman interrupts %v", got)
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
