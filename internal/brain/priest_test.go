package brain

import (
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/spells"
)

var shadowKnown = []uint32{prSWP, prMindBlast, prMindFlay, prSmite, prVampiric, prShadowform, prSilence, prShield1, prInnerFire, prFort1, spShoot}

func shadowRig(t *testing.T, known ...uint32) (*rig, *fakeGame) {
	t.Helper()
	if len(known) == 0 {
		known = shadowKnown
	}
	g := priestFake(known...)
	return newRig(t, g, Params{Strategies: []string{"shadow_priest"}}), g
}

// ---- Power Word: Shield ----

// pull puts the tank next to the priest with a mob that is not in combat yet as its target.
func pull(g *fakeGame) {
	g.party = append(g.party, Member{Name: "Tankard", GUID: tankG, Online: true})
	g.addUnit(objects.Unit{GUID: tankG, Type: "player", Name: "Tankard", Class: 1, InParty: true, Health: 1000, MaxHealth: 1000, Target: mobA,
		Auras: []objects.Aura{{Name: "Power Word: Fortitude", SpellID: prFort1}}}, 6, 0)
	g.addUnit(objects.Unit{GUID: mobA, Type: "creature", Name: "Mob", Hostile: true, Health: 500, MaxHealth: 500}, 30, 0)
}

func TestShieldTheTankAtThePull(t *testing.T) {
	r, g := holyRig(t)
	pull(g)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "shield" || d.Spell != prShield1 || d.Target != tankG {
		t.Fatalf("%+v", d)
	}
	// Not again while the aura is on its way (6 s), then again if it never came.
	if d := r.tick(2 * time.Second); d != nil && d.Action == "shield" {
		t.Fatalf("%+v", d)
	}
	if d := r.tick(5 * time.Second); d == nil || d.Action != "shield" {
		t.Fatalf("%+v", d)
	}
}

func TestShieldNeedsNoWeakenedSoulAndMana(t *testing.T) {
	r, g := holyRig(t)
	pull(g)
	g.addAura(tankG, objects.Aura{Name: "Weakened Soul", SpellID: 6788, Harmful: true})
	if d := r.tick(100 * time.Millisecond); d != nil {
		t.Fatalf("a second shield would be refused: %+v", d)
	}
	r2, g2 := holyRig(t)
	pull(g2)
	g2.addAura(tankG, objects.Aura{Name: "Power Word: Shield", SpellID: prShield1})
	if d := r2.tick(100 * time.Millisecond); d != nil {
		t.Fatalf("%+v", d)
	}
	r3, g3 := holyRig(t)
	pull(g3)
	g3.setMana(40)
	if d := r3.tick(100 * time.Millisecond); d != nil {
		t.Fatalf("the pull shield needs half the mana: %+v", d)
	}
}

func TestShieldAMemberTakingBurst(t *testing.T) {
	r, g := holyRig(t)
	g.member(dpsG1, "Mage", 8, 1000, 1000, 5, 0)
	if d := r.tick(100 * time.Millisecond); d != nil {
		t.Fatalf("%+v", d)
	}
	g.setHP(dpsG1, 650) // 35% of the health gone within a second
	d := r.tick(time.Second)
	if d == nil || d.Action != "shield" || d.Target != dpsG1 || d.Rel <= RelHigh {
		t.Fatalf("%+v", d)
	}
	// The shield is not sent twice (the aura is awaited); the heal follows.
	d = r.tick(2 * time.Second)
	if d == nil || d.Action != "heal" || d.Target != dpsG1 {
		t.Fatalf("%+v", d)
	}
	// A slow loss is not burst: the shield stays out of it.
	r2, g2 := holyRig(t)
	g2.member(dpsG1, "Mage", 8, 1000, 1000, 5, 0)
	r2.tick(100 * time.Millisecond)
	g2.setHP(dpsG1, 900)
	r2.tick(2 * time.Second)
	g2.setHP(dpsG1, 800)
	r2.tick(2 * time.Second)
	g2.setHP(dpsG1, 650)
	if d := r2.tick(2 * time.Second); d == nil || d.Action != "heal" {
		t.Fatalf("%+v", d)
	}
}

// ---- Fade and Psychic Scream ----

func meleeMobsOn(g *fakeGame, n int) {
	for i := 0; i < n; i++ {
		g.addUnit(mob(mobA+packet.GUID(i), selfG), float32(3+i), 0)
	}
}

func TestFadeWhenAMobHasThePriest(t *testing.T) {
	r, g := holyRig(t)
	g.addUnit(mob(mobA, selfG), 4, 0)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "fade" || d.Spell != prFade {
		t.Fatalf("%+v", d)
	}
	g.setSelf(func(u *objects.Unit) { u.Auras = append(u.Auras, objects.Aura{Name: "Fade", SpellID: prFade}) })
	if d := r.tick(2 * time.Second); d != nil {
		t.Fatalf("Fade is on: %+v", d)
	}
}

func TestPsychicScreamIsAnEmergencyWithNoLooseMobs(t *testing.T) {
	r, g := holyRig(t)
	meleeMobsOn(g, 2)
	g.setSelf(func(u *objects.Unit) { u.Health = 500 })
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "psychic_scream" || d.Spell != prScream || d.Rel < RelInterrupt {
		t.Fatalf("%+v", d)
	}
	// A pack that sleeps within 30 yd would be pulled by the scattering mobs: Fade instead.
	r2, g2 := holyRig(t)
	meleeMobsOn(g2, 2)
	g2.setSelf(func(u *objects.Unit) { u.Health = 500 })
	sleeper := mob(mobB+5, 0)
	sleeper.InCombat = false
	g2.addUnit(sleeper, 22, 0)
	if d := r2.tick(100 * time.Millisecond); d == nil || d.Action != "fade" {
		t.Fatalf("%+v", d)
	}
	// One mob, or a priest in good health, does not scream.
	r3, g3 := holyRig(t)
	meleeMobsOn(g3, 1)
	g3.setSelf(func(u *objects.Unit) { u.Health = 500 })
	if d := r3.tick(100 * time.Millisecond); d == nil || d.Action == "psychic_scream" {
		t.Fatalf("%+v", d)
	}
	r4, g4 := holyRig(t)
	meleeMobsOn(g4, 3)
	if d := r4.tick(100 * time.Millisecond); d == nil || d.Action == "psychic_scream" {
		t.Fatalf("%+v", d)
	}
	// A mob fighting someone outside the party counts as loose; one fighting the tank does not.
	r5, g5 := holyRig(t)
	meleeMobsOn(g5, 2)
	g5.setSelf(func(u *objects.Unit) { u.Health = 500 })
	g5.member(tankG, "Tankard", 1, 1000, 1000, 8, 0)
	g5.addUnit(mob(mobB+6, tankG), 12, 0)
	if d := r5.tick(100 * time.Millisecond); d == nil || d.Action != "psychic_scream" {
		t.Fatalf("%+v", d)
	}
	r6, g6 := holyRig(t)
	meleeMobsOn(g6, 2)
	g6.setSelf(func(u *objects.Unit) { u.Health = 500 })
	g6.addUnit(mob(mobB+6, 999), 12, 0)
	if d := r6.tick(100 * time.Millisecond); d == nil || d.Action == "psychic_scream" {
		t.Fatalf("%+v", d)
	}
}

// ---- buffs ----

func TestPriestBuffsOutOfCombatAtTheGate(t *testing.T) {
	r, g := holyRig(t)
	g.setSelf(func(u *objects.Unit) { u.InCombat = false; u.Auras = []objects.Aura{} })
	g.member(dpsG1, "Mage", 8, 1000, 1000, 5, 0)
	g.mutate(dpsG1, func(u *objects.Unit) { u.InCombat = false; u.Auras = []objects.Aura{} })
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "inner_fire" || d.Spell != prInnerFire {
		t.Fatalf("%+v", d)
	}
	g.setSelf(func(u *objects.Unit) {
		u.Auras = append(u.Auras, objects.Aura{Name: "Inner Fire", SpellID: prInnerFire})
	})
	if d = r.tick(2 * time.Second); d == nil || d.Action != "fortitude" || d.Target != selfG {
		t.Fatalf("%+v", d)
	}
	g.setSelf(func(u *objects.Unit) {
		u.Auras = append(u.Auras, objects.Aura{Name: "Power Word: Fortitude", SpellID: prFort1})
	})
	if d = r.tick(2 * time.Second); d == nil || d.Action != "fortitude" || d.Target != dpsG1 {
		t.Fatalf("%+v", d)
	}
	g.addAura(dpsG1, objects.Aura{Name: "Prayer of Fortitude", SpellID: 21562})
	if d = r.tick(2 * time.Second); d != nil {
		t.Fatalf("%+v", d)
	}
	// With the walk going on (no rest gate) or in combat nothing is buffed; nor in Shadowform, which forbids Holy spells.
	r.b.restOK = func() bool { return false }
	g.setSelf(func(u *objects.Unit) { u.Auras = []objects.Aura{} })
	if d = r.tick(2 * time.Second); d != nil {
		t.Fatalf("%+v", d)
	}
	r.b.restOK = func() bool { return true }
	g.setSelf(func(u *objects.Unit) { u.Auras = []objects.Aura{{Name: "Shadowform", SpellID: prShadowform}} })
	if d = r.tick(2 * time.Second); d != nil {
		t.Fatalf("%+v", d)
	}
}

// ---- the shadow priest ----

func TestShadowRotation(t *testing.T) {
	r, g := shadowRig(t)
	tankFights(g, 25, 18, 0)
	if d := r.tick(100 * time.Millisecond); d == nil || d.Spell != prShadowform {
		t.Fatalf("Shadowform first: %+v", d)
	}
	g.setSelf(func(u *objects.Unit) {
		u.Auras = append(u.Auras, objects.Aura{Name: "Shadowform", SpellID: prShadowform})
	})
	if d := r.tick(2200 * time.Millisecond); d == nil || d.Spell != prVampiric {
		t.Fatalf("%+v", d)
	}
	g.addAura(mobA, objects.Aura{Name: "Vampiric Embrace", SpellID: prVampiric, Harmful: true})
	if d := r.tick(2 * time.Second); d == nil || d.Spell != prSWP {
		t.Fatalf("%+v", d)
	}
	g.addAura(mobA, objects.Aura{Name: "Shadow Word: Pain", SpellID: prSWP, Harmful: true})
	if d := r.tick(2 * time.Second); d == nil || d.Spell != prMindBlast {
		t.Fatalf("%+v", d)
	}
	// Mind Blast is on cooldown for 8 s: Mind Flay fills.
	if d := r.tick(2 * time.Second); d == nil || d.Spell != prMindFlay {
		t.Fatalf("%+v", d)
	}
	if d := r.tick(2 * time.Second); d == nil || d.Spell != prMindFlay {
		t.Fatalf("%+v", d)
	}
	// 8 s after the Blast it is next.
	if d := r.tick(2200 * time.Millisecond); d == nil || d.Spell != prMindFlay {
		t.Fatalf("%+v", d)
	}
	if d := r.tick(2500 * time.Millisecond); d == nil || d.Spell != prMindBlast {
		t.Fatalf("%+v", d)
	}
}

func TestShadowDoesNotPainANearlyDeadMob(t *testing.T) {
	r, g := shadowRig(t, prSWP, prMindFlay, spShoot)
	tankFights(g, 25, 18, 0)
	g.mutate(mobA, func(u *objects.Unit) { u.Health = 100 }) // 20%
	r.tick(100 * time.Millisecond)
	if d := r.tick(2200 * time.Millisecond); d == nil || d.Spell != prMindFlay {
		t.Fatalf("%+v", d)
	}
}

func TestShadowSmiteWhenMindFlayCannotReach(t *testing.T) {
	r, g := shadowRig(t, prSmite, prMindFlay, spShoot)
	tankFights(g, 30, 25, 0) // Mind Flay reaches 20 yd, Smite 30
	r.tick(100 * time.Millisecond)
	if d := r.tick(2200 * time.Millisecond); d == nil || d.Spell != prSmite {
		t.Fatalf("%+v", d)
	}
	// In Shadowform the Holy spell is not cast.
	r2, g2 := shadowRig(t, prSmite, prMindFlay, prShadowform, spShoot)
	g2.setSelf(func(u *objects.Unit) {
		u.Auras = append(u.Auras, objects.Aura{Name: "Shadowform", SpellID: prShadowform})
	})
	tankFights(g2, 30, 25, 0)
	r2.tick(100 * time.Millisecond)
	if d := r2.tick(2200 * time.Millisecond); d != nil {
		t.Fatalf("%+v", d)
	}
}

func TestShadowSilenceInterruptsAHeal(t *testing.T) {
	r, g := shadowRig(t)
	tankFights(g, 25, 15, 0)
	g.mutate(mobA, func(u *objects.Unit) {
		u.Casting = &objects.Cast{SpellID: spLesserHeal, Target: mobA, EndsInMS: ms(1500)}
	})
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "interrupt" || d.Spell != prSilence || d.Rel < RelInterrupt {
		t.Fatalf("%+v", d)
	}
	// The healer does not interrupt.
	r2, g2 := holyRig(t, append([]uint32{prSilence}, holyKnown...)...)
	tankFights(g2, 25, 15, 0)
	g2.mutate(mobA, func(u *objects.Unit) {
		u.Casting = &objects.Cast{SpellID: spLesserHeal, Target: mobA, EndsInMS: ms(1500)}
	})
	if d := r2.tick(100 * time.Millisecond); d != nil && d.Action == "interrupt" {
		t.Fatalf("%+v", d)
	}
}

func TestShadowWandsWhenDry(t *testing.T) {
	r, g := shadowRig(t, prMindFlay, spShoot)
	tankFights(g, 25, 15, 0)
	g.setMana(3)
	r.tick(100 * time.Millisecond)
	if d := r.tick(2200 * time.Millisecond); d == nil || d.Spell != spShoot {
		t.Fatalf("%+v", d)
	}
}

func TestShadowShieldsItselfWhenAMobHasIt(t *testing.T) {
	r, g := shadowRig(t, prShield1, prSWP)
	g.addUnit(mob(mobA, selfG), 4, 0)
	g.setSelf(func(u *objects.Unit) { u.Health = 600 })
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "shield_self" || d.Spell != prShield1 {
		t.Fatalf("%+v", d)
	}
	if last := g.lastCast(t); last.target != selfG {
		t.Fatalf("%+v", last)
	}
	r2, g2 := shadowRig(t, prShield1, prSWP, prShadowform)
	g2.addUnit(mob(mobA, selfG), 4, 0)
	g2.setSelf(func(u *objects.Unit) {
		u.Health = 600
		u.Auras = append(u.Auras, objects.Aura{Name: "Shadowform", SpellID: prShadowform})
	})
	if d := r2.tick(100 * time.Millisecond); d != nil && d.Spell == prShield1 {
		t.Fatalf("a Holy spell in Shadowform: %+v", d)
	}
}

func TestShadowPriestStandsNearerThanAMage(t *testing.T) {
	r, pf := newPosRig(t, Params{Strategies: []string{"shadow_priest"}}, shadowKnown...)
	pf.setSelf(func(u *objects.Unit) { u.Class, u.Level = 5, 30 })
	tankFights(pf.fakeGame, 36, 30, 0) // 30 yd from the mob: too far for Mind Flay
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "reposition" {
		t.Fatalf("%+v", d)
	}
	w := pf.walkList()
	if len(w) != 1 {
		t.Fatalf("%v", w)
	}
	if got := dist2(w[0], Vec3{30, 0, 0}); got < 14 || got > 19 {
		t.Fatalf("spot %v is %.1f yd from the mob, want 14-19", w[0], got)
	}
}

func TestShadowDrinksBelowSixtyFiveNotEighty(t *testing.T) {
	r, g := shadowRig(t)
	g.setSelf(func(u *objects.Unit) {
		u.InCombat = false
		u.Auras = append(u.Auras, objects.Aura{Name: "Shadowform", SpellID: prShadowform})
	})
	g.items = []objects.Item{{Entry: 159}}
	g.setMana(70)
	if d := r.tick(100 * time.Millisecond); d != nil {
		t.Fatalf("%+v", d)
	}
	g.setMana(60)
	if d := r.tick(2 * time.Second); d == nil || d.Action != "drink" {
		t.Fatalf("%+v", d)
	}
}

// ---- data ----

func TestPriestPresetsListTheirStrategies(t *testing.T) {
	for preset, want := range map[string]string{
		"holy_priest":   "base,role_healer,priest_holy,priest_defense,priest_buffs,healer_position,avoid_ground,combat_consumables,wand,rest_eat,rest_drink",
		"shadow_priest": "base,combat_dps,priest_shadow,priest_defense,priest_buffs,ranged_position,avoid_ground,class_interrupt,combat_consumables,wand,rest_eat,rest_drink",
	} {
		b, err := New(newFake(), DefaultConfig(), Params{Strategies: []string{preset}})
		if err != nil || strings.Join(b.Strategies(), ",") != want {
			t.Errorf("%s: %v %v", preset, b.Strategies(), err)
		}
	}
	if b, _ := New(newFake(), DefaultConfig(), Params{Strategies: []string{"holy_priest"}}); b.p.Role != RoleHealer {
		t.Errorf("role %q", b.p.Role)
	}
	if b, _ := New(newFake(), DefaultConfig(), Params{Strategies: []string{"shadow_priest"}}); b.p.Role != RoleDPS {
		t.Errorf("role %q", b.p.Role)
	}
}

func TestPriestClassRow(t *testing.T) {
	ci := ClassOf(ClassPriest)
	if ci.Heals == nil || !ci.Drinks || !ci.ManaPotions || ci.ConjuredDrinks || len(ci.Interrupts) != 1 || ci.Interrupts[0].Spell != "Silence" {
		t.Fatalf("%+v", ci)
	}
	if lo, hi := rangedBand(&Ctx{Self: objects.Unit{Class: ClassPriest}}); lo != 14 || hi != 19 {
		t.Fatalf("band %v-%v", lo, hi)
	}
	if lo, hi := rangedBand(&Ctx{Self: objects.Unit{Class: ClassMage}}); lo != rangedMin || hi != rangedMax {
		t.Fatalf("band %v-%v", lo, hi)
	}
	// Every spell of the kit exists under that name.
	names := map[string]bool{}
	for _, n := range ci.Heals.Direct {
		names[n] = true
	}
	names[ci.Heals.HoT] = true
	for _, d := range ci.Heals.Dispels {
		names[d.Spell] = true
	}
	for n := range names {
		found := false
		for id := uint32(1); id < 30000 && !found; id++ {
			found = spells.Name(id) == n
		}
		if !found {
			t.Errorf("no spell named %q", n)
		}
	}
}

func TestPriestSpellData(t *testing.T) {
	// id, name, level, flat cost (mana), range max, cast ms: read off the core's spell_template when the table was made.
	for _, c := range []struct {
		id    uint32
		name  string
		level uint8
		cost  uint32
		rng   float32
		cast  uint32
	}{
		{prLesser1, "Lesser Heal", 1, 30, 40, 1500}, {prLesser2, "Lesser Heal", 4, 45, 40, 2000}, {prLesser3, "Lesser Heal", 10, 75, 40, 2500},
		{prHeal1, "Heal", 16, 155, 40, 3000}, {prHeal2, "Heal", 22, 205, 40, 3000}, {prHeal3, "Heal", 28, 255, 40, 3000}, {6064, "Heal", 34, 305, 40, 3000},
		{prGreater1, "Greater Heal", 40, 370, 40, 3000}, {25314, "Greater Heal", 60, 710, 40, 3000},
		{prFlash1, "Flash Heal", 20, 125, 40, 1500}, {prFlash2, "Flash Heal", 26, 155, 40, 1500}, {10917, "Flash Heal", 56, 380, 40, 1500},
		{prRenew1, "Renew", 8, 30, 40, 0}, {25315, "Renew", 60, 410, 40, 0},
		{prShield1, "Power Word: Shield", 6, 45, 40, 0}, {10901, "Power Word: Shield", 60, 500, 40, 0},
		{prInnerFire, "Inner Fire", 12, 30, 0, 0},
		{prFort1, "Power Word: Fortitude", 1, 50, 30, 0}, {10938, "Power Word: Fortitude", 60, 445, 30, 0},
		{prFade, "Fade", 8, 40, 0, 0}, {prScream, "Psychic Scream", 14, 100, 0, 0},
		{prSWP, "Shadow Word: Pain", 4, 25, 30, 0}, {prMindBlast, "Mind Blast", 10, 50, 30, 1500}, {prMindFlay, "Mind Flay", 20, 45, 20, 0},
		{prSmite, "Smite", 1, 20, 30, 1500}, {prVampiric, "Vampiric Embrace", 1, 40, 30, 0}, {prSilence, "Silence", 0, 225, 20, 0},
		{spShoot, "Shoot", 1, 0, 30, 0},
	} {
		if got := spells.Name(c.id); got != c.name {
			t.Errorf("%d is %q, want %q", c.id, got, c.name)
			continue
		}
		m, ok := spells.Lookup(c.id)
		if !ok || m.Level != c.level || m.Cost != c.cost || m.RangeMax != c.rng || m.CastMS != c.cast || (c.cost > 0 && m.Power != spells.PowerMana) {
			t.Errorf("%s %d: %+v", c.name, c.id, m)
		}
	}
	// The percent-of-base-mana spells: the dispels.
	for _, c := range []struct {
		id    uint32
		name  string
		level uint8
		pct   uint32
	}{{prDispelMagic, "Dispel Magic", 18, 18}, {988, "Dispel Magic", 36, 18}, {prCureDisease, "Cure Disease", 14, 15}, {prAbolishDisease, "Abolish Disease", 32, 15}, {prShadowform, "Shadowform", 0, 40}} {
		m, ok := spells.Lookup(c.id)
		if !ok || spells.Name(c.id) != c.name || m.Level != c.level || m.CostPct != c.pct {
			t.Errorf("%s %d: %+v", c.name, c.id, m)
		}
	}
	// What the role relies on: the heals are heals, the dispels dispel (at 30 yd), Renew heals over 15 s, the shield absorbs, the
	// scream fears, Mind Blast has its 8 s category cooldown, Mind Flay is a channel, Silence is a 45 s cooldown, the shield leaves
	// Weakened Soul, and the do-not-dispel aura is a disease.
	for _, id := range []uint32{prLesser1, prHeal1, prGreater1, prFlash1} {
		if m, _ := spells.Lookup(id); !m.Flags.Has(spells.FlagHeal) || !m.Flags.Has(spells.FlagInterruptible) || m.School != 1 {
			t.Errorf("%s: %+v", spells.Name(id), m)
		}
	}
	for _, id := range []uint32{prDispelMagic, prCureDisease, prAbolishDisease} {
		if m, _ := spells.Lookup(id); !m.Flags.Has(spells.FlagDispel) || m.RangeMax != 30 {
			t.Errorf("%s: %+v", spells.Name(id), m)
		}
	}
	if m, _ := spells.Lookup(prAbolishDisease); !m.Flags.Has(spells.FlagAura) || m.DurationMS != 20000 {
		t.Errorf("Abolish Disease leaves an aura that cures over time: %+v", m)
	}
	if m, _ := spells.Lookup(prRenew1); m.Effects[0].Aura != 8 || m.DurationMS != 15000 || !m.Flags.Has(spells.FlagHeal) {
		t.Errorf("Renew: %+v", m)
	}
	if got := amountOf(prRenew1, 30); got != 45 { // 9 per tick, 5 ticks
		t.Errorf("Renew 1 heals %v", got)
	}
	if m, _ := spells.Lookup(prShield1); m.Effects[0].Aura != 69 || m.DurationMS != 30000 {
		t.Errorf("Power Word: Shield: %+v", m)
	}
	if m, _ := spells.Lookup(prScream); m.Mechanic != 5 {
		t.Errorf("Psychic Scream fears: %+v", m)
	}
	if m, _ := spells.Lookup(prMindBlast); m.CategoryCDMS != 8000 {
		t.Errorf("Mind Blast: %+v", m)
	}
	if m, _ := spells.Lookup(prMindFlay); !m.Flags.Has(spells.FlagChanneled) {
		t.Errorf("Mind Flay: %+v", m)
	}
	if m, _ := spells.Lookup(prSilence); m.CooldownMS != 45000 {
		t.Errorf("Silence: %+v", m)
	}
	if spells.Name(6788) != "Weakened Soul" {
		t.Errorf("Weakened Soul is %q", spells.Name(6788))
	}
	// Shadowform: the spellbook holds its passives under the same name; the brain casts 15473.
	if spells.Name(spellShadowform) != "Shadowform" || spellShadowform != prShadowform {
		t.Errorf("Shadowform %d", spellShadowform)
	}
	// Debuffs of the dispel tests, by their dispel type: 1 magic, 2 curse, 3 disease, 4 poison.
	for id, want := range map[uint32]uint8{auraPolymorph: 1, auraFrostbolt: 1, auraRabies: 3, auraPoison: 4, auraCurse: 2, auraMutating: 3} {
		if m, _ := spells.Lookup(id); m.Dispel != want {
			t.Errorf("%s: dispel type %d, want %d", spells.Name(id), m.Dispel, want)
		}
	}
	if m, _ := spells.Lookup(auraPolymorph); !ccMechanics[m.Mechanic] {
		t.Errorf("Polymorph is crowd control: %+v", m)
	}
	if spells.Name(auraMutating) != "Mutating Injection" || len(noDispel) != 1 || noDispel[0] != "Mutating Injection" {
		t.Errorf("do-not-dispel list %v", noDispel)
	}
}
