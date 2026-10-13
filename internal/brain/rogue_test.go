package brain

import (
	"math"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/spells"
)

// Spells of the rogue tests (ids from internal/spells/meta.tsv.gz; TestRogueSpellData checks the facts the rotation relies on).
const (
	spSinister = 8621 // rank 6, level 38
	spEvisc    = 8624 // rank 6, level 40
	spSnD      = 5171
	spKick     = 1767 // rank 2, level 26
	spEvasion  = 5277
	spFeint    = 8637 // rank 3, level 40
	spVanish   = 1856
	spSprint   = 8696 // rank 2, level 34
)

var rogueSpells = []uint32{spSinister, spEvisc, spSnD, spKick, spEvasion, spFeint, spVanish, spSprint}

// newClassRig is a brain of the preset on a fake game that can walk, see and follow the swings, with the player as the class, on the
// power with its full pool, in a party of nobody (the test adds a tank). tune adjusts the player's row.
func newClassRig(t *testing.T, preset string, class uint8, level uint32, power uint8, pool uint32, p Params, known []uint32, tune func(*objects.Unit)) (*rig, *meleeFake) {
	t.Helper()
	g := newFake(known...)
	g.self.Class, g.self.Level = class, level
	g.self.PowerType, g.self.Power, g.self.MaxPower = power, pool, pool
	g.self.Health, g.self.MaxHealth = 1000, 1000
	if tune != nil {
		tune(&g.self)
	}
	mf := &meleeFake{posFake: &posFake{fakeGame: g}, dodged: map[packet.GUID]time.Time{}}
	cfg := DefaultConfig()
	cfg.Now = g.clk.Now
	if p.Strategies == nil {
		p.Strategies = []string{preset}
	}
	b, err := New(mf, cfg, p)
	if err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, g: g, b: b}, mf
}

func newRogueRig(t *testing.T, p Params, known ...uint32) (*rig, *meleeFake) {
	if len(known) == 0 {
		known = rogueSpells
	}
	return newClassRig(t, "combat_rogue", ClassRogue, 40, 3, 100, p, known, nil)
}

func setEnergy(g *fakeGame, n uint32) { g.setSelf(func(u *objects.Unit) { u.Power = n }) }

func setHealth(g *fakeGame, pct uint32) {
	g.setSelf(func(u *objects.Unit) { u.Health = u.MaxHealth * pct / 100 })
}

func setCombo(g *fakeGame, n uint8, on packet.GUID) {
	g.setSelf(func(u *objects.Unit) { u.ComboPoints, u.ComboTarget = n, on })
}

func addAura(g *fakeGame, name string, id uint32, left *int64) {
	g.setSelf(func(u *objects.Unit) {
		u.Auras = append(u.Auras, objects.Aura{Name: name, SpellID: id, RemainingMS: left})
	})
}

// onMe puts the mob on the player.
func onMe(g *fakeGame, id packet.GUID) { g.mutate(id, func(u *objects.Unit) { u.Target = selfG }) }

func TestRogueBuildsComboPointsThenFinishes(t *testing.T) {
	r, mf := newRogueRig(t, Params{})
	meleeFight(r, mf)
	setCombo(r.g, 0, 0)
	if d := open(r); d == nil || d.Action != "sinister_strike" || d.Spell != spSinister || d.Target != mobA || d.Rel != RelNormal+5 {
		t.Fatalf("builder first: %+v idle %q", d, r.idle())
	}
	setEnergy(r.g, 100)
	setCombo(r.g, 2, mobA)
	if d := r.tick(1100 * time.Millisecond); d == nil || d.Action != "slice_and_dice" || d.Spell != spSnD || d.Target != mobA || d.Rel != RelNormal+8 {
		t.Fatalf("Slice and Dice before more points: %+v idle %q", d, r.idle())
	}
	addAura(r.g, "Slice and Dice", spSnD, ms(20000))
	setEnergy(r.g, 100)
	setCombo(r.g, 3, mobA)
	if d := r.tick(1100 * time.Millisecond); d == nil || d.Action != "sinister_strike" {
		t.Fatalf("three points: build on: %+v", d)
	}
	setEnergy(r.g, 100)
	setCombo(r.g, 5, mobA)
	if d := r.tick(1100 * time.Millisecond); d == nil || d.Action != "eviscerate" || d.Spell != spEvisc || d.Target != mobA || d.Rel != RelNormal+7 {
		t.Fatalf("five points: %+v idle %q", d, r.idle())
	}
	setEnergy(r.g, 100)
	setCombo(r.g, 0, 0)
	if d := r.tick(1100 * time.Millisecond); d == nil || d.Action != "sinister_strike" {
		t.Fatalf("after the finisher the builder again: %+v", d)
	}
	if got := casts(r.g); !eq(got, []uint32{spSinister, spSnD, spSinister, spEvisc, spSinister}) {
		t.Fatalf("casts %v", got)
	}
}

func TestRogueEnergyDecidesWhatItCanDo(t *testing.T) {
	r, mf := newRogueRig(t, Params{})
	meleeFight(r, mf)
	addAura(r.g, "Slice and Dice", spSnD, ms(20000))
	// 30 energy: no Sinister Strike (40), the finisher (30) goes.
	setEnergy(r.g, 30)
	setCombo(r.g, 5, mobA)
	if d := open(r); d == nil || d.Action != "eviscerate" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// 25 energy: nothing the rogue knows is affordable except Kick, and there is nothing to kick.
	setEnergy(r.g, 25)
	if d := r.tick(1100 * time.Millisecond); d != nil {
		t.Fatalf("with 25 energy and 5 points: %+v", d)
	}
	// Five points: the builder waits for the finisher, it does not overshoot.
	setEnergy(r.g, 100)
	setCombo(r.g, 5, mobA)
	if d := r.tick(1100 * time.Millisecond); d == nil || d.Action == "sinister_strike" {
		t.Fatalf("a sixth point: %+v", d)
	}
}

func TestRogueFinisherNeedsPointsOnTheMobItAttacks(t *testing.T) {
	r, mf := newRogueRig(t, Params{})
	meleeFight(r, mf)
	setCombo(r.g, 5, mobB) // the points are on another mob
	if d := open(r); d == nil || d.Action != "sinister_strike" {
		t.Fatalf("points on another target are no finisher: %+v", d)
	}
}

func TestRogueFinishesWithFourPointsOnlyAMobThatIsLow(t *testing.T) {
	r, mf := newRogueRig(t, Params{})
	meleeFight(r, mf)
	addAura(r.g, "Slice and Dice", spSnD, ms(20000))
	setCombo(r.g, 4, mobA)
	if d := open(r); d == nil || d.Action != "sinister_strike" {
		t.Fatalf("a healthy mob: build to five: %+v", d)
	}
	setEnergy(r.g, 100)
	setMobHP(r.g, mobA, 30)
	if d := r.tick(1100 * time.Millisecond); d == nil || d.Action != "eviscerate" {
		t.Fatalf("a mob at 30%%: %+v", d)
	}
}

func TestSliceAndDiceWantsPointsALivingMobAndAnEndingBuff(t *testing.T) {
	// One point is too little, a mob about to die is not worth it.
	r, mf := newRogueRig(t, Params{})
	meleeFight(r, mf)
	setCombo(r.g, 1, mobA)
	if d := open(r); d == nil || d.Action != "sinister_strike" {
		t.Fatalf("one point: %+v", d)
	}
	r, mf = newRogueRig(t, Params{})
	meleeFight(r, mf)
	setCombo(r.g, 3, mobA)
	setMobHP(r.g, mobA, 30)
	if d := open(r); d == nil || d.Action != "sinister_strike" {
		t.Fatalf("a mob at 30%%: %+v", d)
	}
	// Up with plenty of time left: leave it. Under 1.5 s left: renew it.
	r, mf = newRogueRig(t, Params{})
	meleeFight(r, mf)
	addAura(r.g, "Slice and Dice", spSnD, ms(9000))
	setCombo(r.g, 3, mobA)
	if d := open(r); d == nil || d.Action != "sinister_strike" {
		t.Fatalf("buff up: %+v", d)
	}
	r, mf = newRogueRig(t, Params{})
	meleeFight(r, mf)
	addAura(r.g, "Slice and Dice", spSnD, ms(1000))
	setCombo(r.g, 3, mobA)
	if d := open(r); d == nil || d.Action != "slice_and_dice" {
		t.Fatalf("buff ending: %+v", d)
	}
}

func TestRogueKicksACasterInReachAndNotOneOutOfIt(t *testing.T) {
	r, mf := newRogueRig(t, Params{})
	meleeFight(r, mf)
	caster := mob(mobB, tankG)
	caster.Casting = &objects.Cast{SpellID: spShadowBolt, EndsInMS: ms(1500), Target: tankG}
	r.g.addUnit(caster, 3, 2)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "interrupt" || d.Spell != spKick || d.Target != mobB || d.Rel != RelInterrupt {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// Kick reaches 5 yards.
	r, mf = newRogueRig(t, Params{})
	meleeFight(r, mf)
	far := mob(mobB, tankG)
	far.Casting = &objects.Cast{SpellID: spShadowBolt, EndsInMS: ms(1500), Target: tankG}
	r.g.addUnit(far, 12, 0)
	if d := r.tick(100 * time.Millisecond); d != nil && d.Action == "interrupt" {
		t.Fatalf("a kick from 12 yards: %+v", d)
	}
	// Kick costs 25 energy.
	r, mf = newRogueRig(t, Params{})
	meleeFight(r, mf)
	caster = mob(mobB, tankG)
	caster.Casting = &objects.Cast{SpellID: spShadowBolt, EndsInMS: ms(1500), Target: tankG}
	r.g.addUnit(caster, 3, 2)
	setEnergy(r.g, 20)
	if d := r.tick(100 * time.Millisecond); d != nil && d.Action == "interrupt" {
		t.Fatalf("a kick with 20 energy: %+v", d)
	}
}

func TestEvasionWhenAMobOnTheRogueHurtsIt(t *testing.T) {
	r, mf := newRogueRig(t, Params{})
	meleeFight(r, mf)
	onMe(r.g, mobA)
	setHealth(r.g, 40)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "evasion" || d.Spell != spEvasion || d.Rel != relEmergency {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// Not twice, not at 60% health.
	addAura(r.g, "Evasion", spEvasion, nil)
	if d := r.tick(1100 * time.Millisecond); d != nil && d.Action == "evasion" {
		t.Fatalf("Evasion again: %+v", d)
	}
	r, mf = newRogueRig(t, Params{})
	meleeFight(r, mf)
	onMe(r.g, mobA)
	setHealth(r.g, 60)
	if d := r.tick(100 * time.Millisecond); d != nil && d.Action == "evasion" {
		t.Fatalf("at 60%%: %+v", d)
	}
	// A mob that has the tank is not a reason.
	r, mf = newRogueRig(t, Params{})
	meleeFight(r, mf)
	setHealth(r.g, 30)
	if d := r.tick(100 * time.Millisecond); d != nil && d.Action == "evasion" {
		t.Fatalf("a mob on the tank: %+v", d)
	}
}

func TestFeintOnlyWhenTheMobHasTheRogueAndATankCanTakeIt(t *testing.T) {
	r, mf := newRogueRig(t, Params{})
	meleeFight(r, mf)
	onMe(r.g, mobA)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "feint" || d.Spell != spFeint || d.Target != mobA || d.Rel != RelHigh {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// Not again for 10 s (its cooldown): the tank needs a moment to take the mob.
	setEnergy(r.g, 100)
	if d := r.tick(3 * time.Second); d != nil && d.Action == "feint" {
		t.Fatalf("Feint spammed: %+v", d)
	}
	setEnergy(r.g, 100)
	if d := r.tick(8 * time.Second); d == nil || d.Action != "feint" {
		t.Fatalf("a mob that stays on the rogue: %+v", d)
	}
	// Without a tank there is nobody to give the mob to.
	r, mf = newRogueRig(t, Params{})
	m := mob(mobA, selfG)
	r.g.addUnit(m, 4, 0)
	r.g.setSelf(func(u *objects.Unit) { u.InCombat = true })
	mf.setSwing(mobA)
	r.tick(100 * time.Millisecond)
	for _, id := range casts(r.g) {
		if id == spFeint {
			t.Fatal("Feint with no tank")
		}
	}
}

func TestRogueEmergenciesInOrder(t *testing.T) {
	// A hurt rogue with a mob on it and the tank 20 yards off: Vanish, then Evasion, then Sprint and the run to the tank.
	r, mf := newRogueRig(t, Params{})
	tankFights(r.g, 20, 4, 0)
	mf.setSwing(mobA)
	onMe(r.g, mobA)
	r.tick(100 * time.Millisecond)
	setHealth(r.g, 20)
	setEnergy(r.g, 0)
	steps := []struct {
		action string
		aura   string
		id     uint32
		rel    float32
	}{
		{"vanish", "Vanish", spVanish, relEmergency + 1},
		{"evasion", "Evasion", spEvasion, relEmergency},
		{"sprint", "Sprint", spSprint, relEmergency - 1},
	}
	for _, s := range steps {
		d := r.tick(1100 * time.Millisecond)
		if d == nil || d.Action != s.action || d.Spell != s.id || d.Rel != s.rel {
			t.Fatalf("want %s: %+v idle %q", s.action, d, r.idle())
		}
		addAura(r.g, s.aura, s.id, nil)
	}
	d := r.tick(1100 * time.Millisecond)
	if d == nil || d.Action != "flee_to_tank" || len(mf.walkList()) != 1 {
		t.Fatalf("%+v walks %v idle %q", d, mf.walkList(), r.idle())
	}
	if w := mf.walkList()[0]; math.Abs(float64(w[0])-16) > 1.5 {
		t.Fatalf("ran to %v, want about 4 yards short of the tank at x=20", w)
	}
	// No Vanish above 25%, no run when the tank stands near.
	r, mf = newRogueRig(t, Params{})
	meleeFight(r, mf)
	onMe(r.g, mobA)
	setHealth(r.g, 28)
	setEnergy(r.g, 0)
	r.tick(100 * time.Millisecond)
	for _, id := range casts(r.g) {
		if id == spVanish || id == spSprint {
			t.Fatalf("%v", casts(r.g))
		}
	}
	if len(mf.walkList()) != 0 {
		t.Fatalf("walked to a tank that stands near: %v", mf.walkList())
	}
}

func TestRogueStaysBehindTheMob(t *testing.T) {
	r, mf := newRogueRig(t, Params{})
	tankFights(r.g, 8, 4, math.Pi) // the mob faces the rogue
	mf.setSwing(mobA)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "melee_reposition" || d.Rel != RelMove {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	m := r.g.units[1]
	if m.GUID != mobA {
		m = r.g.units[0]
	}
	if w := mf.walkList(); len(w) != 1 || !behind(m, w[0]) {
		t.Fatalf("walked to %v, not behind the mob at (%.0f,%.0f)", w, m.X, m.Y)
	}
}

func TestRogueWaitsForTheTankLikeAnyDamageDealer(t *testing.T) {
	r, mf := newRogueRig(t, Params{})
	tankFights(r.g, 7, 4, 0)
	mf.setSwing(mobA)
	addAura(r.g, "Slice and Dice", spSnD, ms(20000))
	setCombo(r.g, 5, mobA)
	if d := r.tick(100 * time.Millisecond); d != nil || r.idle() != "vetoed:opening_delay" {
		t.Fatalf("it opened before the tank held the mob: %+v idle %q", d, r.idle())
	}
}

func TestRogueSpellData(t *testing.T) {
	for _, c := range []struct {
		id    uint32
		name  string
		level uint8
		cost  uint32
		rng   float32
	}{
		{spSinister, "Sinister Strike", 38, 40, 5}, {spEvisc, "Eviscerate", 40, 30, 5}, {spSnD, "Slice and Dice", 10, 20, 100},
		{spKick, "Kick", 26, 25, 5}, {spFeint, "Feint", 40, 20, 5}, {spEvasion, "Evasion", 8, 0, 0}, {spVanish, "Vanish", 22, 0, 0},
		{spSprint, "Sprint", 34, 0, 0},
	} {
		if got := spells.Name(c.id); got != c.name {
			t.Errorf("%d is %q, want %q", c.id, got, c.name)
			continue
		}
		m, ok := spells.Lookup(c.id)
		if !ok || m.Level != c.level || m.Cost != c.cost || m.RangeMax != c.rng {
			t.Errorf("%s: %+v", c.name, m)
		}
		if m.Power != spells.PowerEnergy {
			t.Errorf("%s is not an energy spell: power %d", c.name, m.Power)
		}
		// Nothing of the rotation needs Stealth or forbids combat; the finishers and only they take combo points.
		if m.Flags.Has(spells.FlagStealthOnly) || m.Flags.Has(spells.FlagNotInCombat) {
			t.Errorf("%s: %v", c.name, m.Flags.Names())
		}
		if want := c.id == spEvisc || c.id == spSnD; m.Flags.Has(spells.FlagComboPoints) != want {
			t.Errorf("%s: combo flag %v, want %v", c.name, m.Flags.Has(spells.FlagComboPoints), want)
		}
	}
	// Kick interrupts (the interrupt effect and the damage), a melee move with a 10 s category cooldown.
	if m, _ := spells.Lookup(spKick); !m.Flags.Has(spells.FlagInterrupt) || m.CategoryCDMS != 10000 || m.Mechanic != 0 {
		t.Errorf("Kick: %+v", m)
	}
	// The cooldowns the emergencies lean on: Evasion 4 min, Vanish 5 min, Sprint 4 min.
	for id, cd := range map[uint32]uint32{spEvasion: 240000, spVanish: 300000, spSprint: 240000} {
		if m, _ := spells.Lookup(id); m.CategoryCDMS != cd {
			t.Errorf("%s: category cooldown %d, want %d", spells.Name(id), m.CategoryCDMS, cd)
		}
	}
	// Feint is a threat effect (SPELL_EFFECT_THREAT, 63) on the enemy.
	if m, _ := spells.Lookup(spFeint); m.Effects[0].Effect != 63 || m.Effects[0].Base >= 0 {
		t.Errorf("Feint: %+v", m.Effects)
	}
	// Slice and Dice is a buff on the rogue: an aura of haste (SPELL_AURA_MOD_MELEE_HASTE, 138) lasting longer with more points.
	if m, _ := spells.Lookup(spSnD); m.Effects[1].Aura != 138 || m.Effects[1].Effect != 6 {
		t.Errorf("Slice and Dice: %+v", m.Effects)
	}
	// The class table row: energy, and Kick without a stance (the rogue has none).
	ci := ClassOf(ClassRogue)
	if ci.Power != powerEnergy || len(ci.Interrupts) != 1 || ci.Interrupts[0].Spell != "Kick" || len(ci.Interrupts[0].Stances) != 0 || ci.Drinks {
		t.Errorf("%+v", ci)
	}
	assertNoStances(t, "rogue", []uint32{1752, 1757, 1758, 1759, 1760, 8621, 11293, 11294, 5171, 6774, 2098, 6760, 6761, 6762, 8623, 8624, 11299, 11300, 31016,
		1766, 1767, 1768, 1769, 5277, 1966, 6768, 8637, 11303, 25302, 2983, 8696, 11305, 1856, 1857})
}

// coreNoStance are spell ids whose rows in the core's spell_template carry no stance or form (`stances` and `stancesNot` are 0), as
// read off sql/base/tw_world_spell_template.sql of tortoise-wow 5d79486 (no file of sql/database_updates/world sets `stances` for
// them), for the rogue's, the hunter's and the paladin's abilities. A class with a form (warrior, druid) has its own gates
// (warriorStances, TestStanceGatesFollowTheCoreData); these three classes have none, so the brain checks no stance for them and the
// class table gives their interrupts none. The test pins that the gate tables are not given an entry for any of these ids.
func assertNoStances(t *testing.T, class string, ids []uint32) {
	t.Helper()
	for _, id := range ids {
		if spells.Name(id) == "" {
			t.Errorf("%s: spell %d has no name", class, id)
		}
		if st, ok := SpellStances[id]; ok {
			t.Errorf("%s: %s %d has stances %v in the brain, the core's data has none", class, spells.Name(id), id, st)
		}
		if _, ok := warriorStances[spells.Name(id)]; ok {
			t.Errorf("%s: %s is in the warrior's stance table", class, spells.Name(id))
		}
	}
}
