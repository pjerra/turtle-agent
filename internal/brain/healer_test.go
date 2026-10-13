package brain

import (
	"math"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/spells"
)

// The spells of the priest tests, with their template numbers (internal/spells/meta.tsv.gz). Ranks: Lesser Heal 1-3, Heal 1-4,
// Greater Heal 1, Flash Heal 1-2, Renew 1, Power Word: Shield 1.
const (
	prLesser1, prLesser2, prLesser3 = 2050, 2052, 2053
	prHeal1, prHeal2, prHeal3       = 2054, 2055, 6063
	prGreater1                      = 2060
	prFlash1, prFlash2              = 2061, 9472
	prRenew1                        = 139
	prShield1                       = 17
	prDispelMagic                   = 527
	prCureDisease                   = 528
	prAbolishDisease                = 552
	prInnerFire                     = 588
	prFort1                         = 1243
	prFade                          = 586
	prScream                        = 8122
	prSWP                           = 589
	prMindBlast                     = 8092
	prMindFlay                      = 15407
	prSmite                         = 585
	prVampiric                      = 15286
	prSilence                       = 15487
	prShadowform                    = 15473

	dpsG1, dpsG2, dpsG3 = packet.GUID(30), packet.GUID(31), packet.GUID(32)

	// debuffs
	auraPolymorph = 118   // magic, mechanic polymorph
	auraFrostbolt = 116   // magic, a slow
	auraRabies    = 3150  // disease
	auraPoison    = 2818  // poison
	auraCurse     = 980   // curse
	auraMutating  = 28169 // disease, on the do-not-dispel list
)

var holyKnown = []uint32{prLesser1, prLesser2, prLesser3, prHeal1, prHeal2, prHeal3, prFlash1, prFlash2, prRenew1, prShield1, prDispelMagic,
	prCureDisease, prInnerFire, prFort1, prFade, prScream, spShoot}

// priestFake is a level 30 priest in combat, buffed, with a full pool of 1000 mana and 1000 health.
func priestFake(known ...uint32) *fakeGame {
	g := newFake(known...)
	g.self.Class, g.self.Level = 5, 30
	g.self.MaxHealth, g.self.Health = 1000, 1000
	g.self.InCombat = true
	g.self.Auras = []objects.Aura{{Name: "Inner Fire", SpellID: prInnerFire}, {Name: "Power Word: Fortitude", SpellID: prFort1}}
	return g
}

// member adds a party member in combat with the given health at (x, y).
func (g *fakeGame) member(guid packet.GUID, name string, class uint8, hp, max uint32, x, y float32) {
	g.party = append(g.party, Member{Name: name, GUID: guid, Online: true})
	g.addUnit(objects.Unit{GUID: guid, Type: "player", Name: name, Class: class, InParty: true, Health: hp, MaxHealth: max, InCombat: true,
		Auras: []objects.Aura{{Name: "Power Word: Fortitude", SpellID: prFort1}}}, x, y)
}

func (g *fakeGame) setHP(guid packet.GUID, hp uint32) {
	g.mutate(guid, func(u *objects.Unit) { u.Health = hp })
}

func (g *fakeGame) addAura(guid packet.GUID, a objects.Aura) {
	g.mutate(guid, func(u *objects.Unit) { u.Auras = append(u.Auras, a) })
}

func (g *fakeGame) setMana(pct uint32) {
	g.setSelf(func(u *objects.Unit) { u.Power = u.MaxPower * pct / 100 })
}

func holyRig(t *testing.T, known ...uint32) (*rig, *fakeGame) {
	t.Helper()
	if len(known) == 0 {
		known = holyKnown
	}
	g := priestFake(known...)
	return newRig(t, g, Params{Strategies: []string{"holy_priest"}}), g
}

func amountOf(id uint32, level uint32) float64 {
	m, _ := spells.Lookup(id)
	return healAmount(m, level)
}

// ---- triage ----

func TestHealTriageLowestHealthFirst(t *testing.T) {
	r, g := holyRig(t)
	g.member(dpsG1, "Mage", 8, 800, 1000, 5, 0)
	g.member(dpsG2, "Rogue", 4, 400, 1000, 5, 3)
	g.member(dpsG3, "Hunter", 3, 600, 1000, 5, -3)
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "heal" || d.Target != dpsG2 {
		t.Fatalf("%+v", d)
	}
	// With the worst hurt healed, the next worst is the one after.
	g.setHP(dpsG2, 1000)
	if d := r.tick(2 * time.Second); d == nil || d.Action != "heal" || d.Target != dpsG3 {
		t.Fatalf("%+v", d)
	}
	g.setHP(dpsG3, 1000)
	if d := r.tick(2 * time.Second); d == nil || d.Target != dpsG1 {
		t.Fatalf("%+v", d)
	}
	g.setHP(dpsG1, 1000)
	if d := r.tick(2 * time.Second); d != nil {
		t.Fatalf("nobody is hurt: %+v", d)
	}
}

func TestHealTriageIncludesThePlayer(t *testing.T) {
	r, g := holyRig(t)
	g.member(dpsG1, "Mage", 8, 700, 1000, 5, 0)
	g.setSelf(func(u *objects.Unit) { u.Health = 300 })
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "heal" || d.Target != selfG {
		t.Fatalf("the priest heals itself first: %+v", d)
	}
}

func TestHealTriageTankBonus(t *testing.T) {
	r, g := holyRig(t)
	g.member(tankG, "Tankard", 1, 700, 1000, 6, 0) // 70% - 8 = 62 + 0.6
	g.member(dpsG1, "Mage", 8, 650, 1000, 6, 3)    // 65%
	if d := r.tick(100 * time.Millisecond); d == nil || d.Target != tankG {
		t.Fatalf("a tank a little better off than a dps is healed first: %+v", d)
	}
	r2, g2 := holyRig(t)
	g2.member(tankG, "Tankard", 1, 800, 1000, 6, 0) // 80 - 8 = 72
	g2.member(dpsG1, "Mage", 8, 600, 1000, 6, 3)    // 60: the tank bonus is not that big
	if d := r2.tick(100 * time.Millisecond); d == nil || d.Target != dpsG1 {
		t.Fatalf("%+v", d)
	}
}

func TestHealTriageDistancePenalty(t *testing.T) {
	r, g := holyRig(t)
	g.member(dpsG1, "Far", 8, 600, 1000, 30, 0) // 60 + 3 = 63
	g.member(dpsG2, "Near", 8, 620, 1000, 5, 0) // 62 + 0.5 = 62.5
	if d := r.tick(100 * time.Millisecond); d == nil || d.Target != dpsG2 {
		t.Fatalf("%+v", d)
	}
}

func TestHealSkipsTheDeadTheFarAndTheOffline(t *testing.T) {
	r, g := holyRig(t)
	g.member(dpsG1, "Dead", 8, 0, 1000, 5, 0)
	g.mutate(dpsG1, func(u *objects.Unit) { u.Dead = true })
	g.party[0].Dead = true
	g.member(dpsG2, "TooFar", 8, 100, 1000, 45, 0)
	g.member(dpsG3, "Offline", 8, 100, 1000, 6, 0)
	g.party[2].Online = false
	if d := r.tick(100 * time.Millisecond); d != nil {
		t.Fatalf("nobody can be healed: %+v", d)
	}
	g.member(tankG, "Tankard", 1, 500, 1000, 8, 0)
	if d := r.tick(2 * time.Second); d == nil || d.Target != tankG {
		t.Fatalf("%+v", d)
	}
}

func TestHealSkipsAMemberWithoutLineOfSight(t *testing.T) {
	g := priestFake(holyKnown...)
	pf := &posFake{fakeGame: g}
	cfg := DefaultConfig()
	cfg.Now = g.clk.Now
	b, err := New(pf, cfg, Params{Strategies: []string{"holy_priest"}})
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, g: g, b: b}
	pf.blocked = wallRect(10, -50, 11, 50)          // a wall between the player and the east
	g.member(dpsG1, "Behind", 8, 200, 1000, 20, 0)  // the most hurt, behind the wall
	g.member(dpsG2, "Visible", 8, 700, 1000, 0, 20) // in sight
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "heal" || d.Target != dpsG2 {
		t.Fatalf("%+v", d)
	}
}

func TestHealRefusedForLineOfSightLeavesTheMemberAlone(t *testing.T) {
	g := priestFake(holyKnown...)
	pf := &posFake{fakeGame: g}
	cfg := DefaultConfig()
	cfg.Now = g.clk.Now
	b, err := New(pf, cfg, Params{Strategies: []string{"holy_priest"}})
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, g: g, b: b}
	g.member(dpsG1, "Mage", 8, 300, 1000, 10, 0)
	g.member(dpsG2, "Rogue", 4, 650, 1000, 0, 10)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Target != dpsG1 {
		t.Fatalf("%+v", d)
	}
	// The server says the wall is there: the mesh could not see it.
	g.st.CastRefused(d.Spell, codeOf(t, "line_of_sight"))
	g.st.Clear(d.Spell)
	d = r.tick(2 * time.Second)
	if d == nil || d.Action != "heal" || d.Target != dpsG2 {
		t.Fatalf("the member behind the wall waits: %+v", d)
	}
}

// ---- the spell by the deficit ----

func TestHealChoosesTheSmallestHealThatCoversTheDeficit(t *testing.T) {
	for _, c := range []struct {
		name    string
		hp, max uint32
		want    uint32
	}{
		{"70 missing: Lesser Heal 2 (84) covers it", 330, 400, prLesser2},
		{"100 missing: Lesser Heal 3 (155)", 300, 400, prLesser3},
		{"400 missing: Heal 2 (477)", 600, 1000, prHeal2},
		{"800 missing and nothing covers it: the biggest, Heal 3 (613)", 1200, 2000, prHeal3},
	} {
		r, g := holyRig(t)
		g.member(dpsG1, "Mage", 8, c.hp, c.max, 5, 0)
		d := r.tick(100 * time.Millisecond)
		if d == nil || d.Action != "heal" || d.Spell != c.want {
			t.Errorf("%s: %+v, want spell %d", c.name, d, c.want)
		}
	}
	// The amounts the choice rests on come from the spell data and rise with the rank.
	if !(amountOf(prLesser1, 30) < amountOf(prLesser2, 30) && amountOf(prLesser3, 30) < amountOf(prHeal1, 30) && amountOf(prHeal1, 30) < amountOf(prHeal2, 30) && amountOf(prHeal2, 30) < amountOf(prHeal3, 30)) {
		t.Errorf("the ranks must rise")
	}
}

func TestHealRankDownForASmallDeficit(t *testing.T) {
	r, g := holyRig(t)
	g.member(dpsG1, "Mage", 8, 330, 400, 5, 0) // 82%: 70 missing, Lesser Heal 2 (84) covers it
	if d := r.tick(100 * time.Millisecond); d == nil || d.Spell != prLesser2 {
		t.Fatalf("%+v", d)
	}
}

func TestHealDoesNotOverheal(t *testing.T) {
	// A level 60 priest that knows only Greater Heal 1 (920) and the top ranks has nothing small: a 150-health deficit is left alone,
	// the regeneration or a HoT does it.
	g := priestFake(prGreater1, prHeal1, prRenew1, spShoot)
	g.self.Level = 60
	r := newRig(t, g, Params{Strategies: []string{"holy_priest"}})
	g.member(dpsG1, "Mage", 8, 700, 850, 5, 0) // 82%, 150 missing; the smallest heal, Heal 1, is 331 -> half is 165
	if d := r.tick(100 * time.Millisecond); d != nil {
		t.Fatalf("a deficit under half of the smallest heal is not healed: %+v", d)
	}
	g.setHP(dpsG1, 600) // 250 missing
	if d := r.tick(2 * time.Second); d == nil || d.Spell != prHeal1 {
		t.Fatalf("%+v", d)
	}
}

func TestHealLowMemberGetsTheQuickestCoveringHeal(t *testing.T) {
	r, g := holyRig(t)
	g.member(dpsG1, "Mage", 8, 120, 400, 5, 0) // 30%, 280 missing: Flash Heal 2 (295) covers it in 1.5 s; Heal 1 (331) takes 3 s
	if d := r.tick(100 * time.Millisecond); d == nil || d.Spell != prFlash2 {
		t.Fatalf("%+v", d)
	}
}

func TestHealEmergencyTakesTheBiggestFastHeal(t *testing.T) {
	r, g := holyRig(t)
	g.member(dpsG1, "Mage", 8, 150, 1000, 5, 0) // 15%
	if d := r.tick(100 * time.Millisecond); d == nil || d.Spell != prFlash2 || d.Rel < RelInterrupt {
		t.Fatalf("%+v", d)
	}
	// A tank is in an emergency a little earlier.
	r2, g2 := holyRig(t)
	g2.member(tankG, "Tankard", 1, 320, 1000, 5, 0)
	if d := r2.tick(100 * time.Millisecond); d == nil || d.Spell != prFlash2 || d.Rel < RelInterrupt {
		t.Fatalf("%+v", d)
	}
}

func TestHealKeepsFastHealsForTheLow(t *testing.T) {
	r, g := holyRig(t)
	g.member(dpsG1, "Mage", 8, 600, 1000, 5, 0) // 60%: Heal 2 covers 400; Flash Heal is dear
	if d := r.tick(100 * time.Millisecond); d == nil || d.Spell == prFlash1 || d.Spell == prFlash2 {
		t.Fatalf("%+v", d)
	}
}

func TestHealSavesManaBelowForty(t *testing.T) {
	r, g := holyRig(t)
	g.setMana(30)
	g.member(dpsG1, "Mage", 8, 780, 1000, 5, 0) // 78%: a candidate at full mana, not now (threshold 70)
	if d := r.tick(100 * time.Millisecond); d != nil {
		t.Fatalf("mana is short, the top-up waits: %+v", d)
	}
	g.setHP(dpsG1, 500) // 50%; the long wait keeps the drop from reading as burst damage, which would be shielded
	d := r.tick(5 * time.Second)
	if d == nil || d.Action != "heal" || d.Spell == prFlash1 || d.Spell == prFlash2 {
		t.Fatalf("no fast heal for a member who is not low: %+v", d)
	}
	// Under 35% and close to dying the dear heal is allowed even then.
	g.setHP(dpsG1, 200)
	if d := r.tick(5 * time.Second); d == nil || (d.Spell != prFlash1 && d.Spell != prFlash2) {
		t.Fatalf("%+v", d)
	}
	// No HoT on the tank at this mana.
	r2, g2 := holyRig(t)
	g2.setMana(30)
	g2.member(tankG, "Tankard", 1, 950, 1000, 5, 0)
	if d := r2.tick(100 * time.Millisecond); d != nil {
		t.Fatalf("%+v", d)
	}
}

func TestHealCountsHealsInFlight(t *testing.T) {
	r, g := holyRig(t)
	g.member(dpsG1, "Healer", 5, 1000, 1000, 5, 0)
	g.member(dpsG2, "Rogue", 4, 500, 1000, 5, 3)
	g.member(dpsG3, "Mage", 8, 700, 1000, 5, -3)
	// Another healer is casting Greater Heal (920) on the rogue: it will be topped.
	g.mutate(dpsG1, func(u *objects.Unit) {
		u.Casting = &objects.Cast{SpellID: prGreater1, Target: dpsG2, EndsInMS: ms(1500)}
	})
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "heal" || d.Target != dpsG3 {
		t.Fatalf("the rogue is being healed already: %+v", d)
	}
}

// ---- HoT, rest gates, mana ----

func TestHealHoTOnTheTankWhenIdle(t *testing.T) {
	r, g := holyRig(t)
	g.member(tankG, "Tankard", 1, 960, 1000, 5, 0) // in combat, 96%: no heal, but a Renew
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "hot_tank" || d.Spell != prRenew1 || d.Target != tankG {
		t.Fatalf("%+v", d)
	}
	g.addAura(tankG, objects.Aura{Name: "Renew", SpellID: prRenew1})
	if d := r.tick(2 * time.Second); d != nil {
		t.Fatalf("the HoT is on: %+v", d)
	}
	// Out of combat the tank needs none.
	r2, g2 := holyRig(t)
	g2.member(tankG, "Tankard", 1, 960, 1000, 5, 0)
	g2.mutate(tankG, func(u *objects.Unit) { u.InCombat = false })
	g2.setSelf(func(u *objects.Unit) { u.InCombat = false })
	if d := r2.tick(100 * time.Millisecond); d != nil {
		t.Fatalf("%+v", d)
	}
}

func TestHealOutOfCombatOnlyAtRestGates(t *testing.T) {
	r, g := holyRig(t)
	g.setSelf(func(u *objects.Unit) { u.InCombat = false })
	g.member(dpsG1, "Mage", 8, 500, 1000, 5, 0)
	g.mutate(dpsG1, func(u *objects.Unit) { u.InCombat = false })
	r.b.restOK = func() bool { return false } // the driver is walking
	if d := r.tick(100 * time.Millisecond); d != nil {
		t.Fatalf("the priest does not stop the walk to heal: %+v", d)
	}
	r.b.restOK = func() bool { return true }
	if d := r.tick(2 * time.Second); d == nil || d.Action != "heal" || d.Target != dpsG1 {
		t.Fatalf("%+v", d)
	}
	// A member under 90% is healed at the gate (the driver wants 90), one at 92% is not.
	g.setHP(dpsG1, 920)
	if d := r.tick(2 * time.Second); d != nil {
		t.Fatalf("%+v", d)
	}
}

func TestHealerDrinksBeforeTheTopUpAtARestGate(t *testing.T) {
	r, g := holyRig(t)
	g.setSelf(func(u *objects.Unit) { u.InCombat = false })
	g.setMana(60)
	g.items = []objects.Item{{Entry: 159}} // Refreshing Spring Water
	g.member(dpsG1, "Mage", 8, 800, 1000, 5, 0)
	g.mutate(dpsG1, func(u *objects.Unit) { u.InCombat = false })
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "drink" || len(g.used) != 1 || g.used[0] != 159 {
		t.Fatalf("%+v used %v", d, g.used)
	}
	// A healer drinks up to 80%, the shadow priest (a damage dealer) only below 65%.
	g.setMana(85)
	g.setSelf(func(u *objects.Unit) {
		u.StandState = "sit"
		u.Auras = append(u.Auras, objects.Aura{Name: "Drink"})
	})
	if d := r.tick(8 * time.Second); d != nil {
		t.Fatalf("the top-up waits for the end of the drink: %+v", d)
	}
	g.setSelf(func(u *objects.Unit) { u.Auras = u.Auras[:len(u.Auras)-1] }) // the drink ends, the priest still sits
	if d := r.tick(2 * time.Second); d == nil || d.Action != "stand_up" {
		t.Fatalf("%+v", d)
	}
	if d := r.tick(2 * time.Second); d == nil || d.Action != "heal" {
		t.Fatalf("%+v", d)
	}
}

func TestDispelsByTheDebuffsType(t *testing.T) {
	cast := func(known []uint32, aura objects.Aura, mana uint32) *Decision {
		g := priestFake(known...)
		r := newRig(t, g, Params{Strategies: []string{"holy_priest"}})
		g.member(dpsG1, "Mage", 8, 1000, 1000, 5, 0)
		g.setMana(mana)
		aura.Harmful = true
		g.addAura(dpsG1, aura)
		return r.tick(100 * time.Millisecond)
	}
	poly := objects.Aura{SpellID: auraPolymorph, Name: "Polymorph"}
	slow := objects.Aura{SpellID: auraFrostbolt, Name: "Frostbolt"}
	if d := cast(holyKnown, poly, 100); d == nil || d.Action != "dispel" || d.Spell != prDispelMagic || d.Target != dpsG1 {
		t.Errorf("magic: %+v", d)
	}
	if d := cast(holyKnown, objects.Aura{SpellID: auraRabies, Name: "Rabies"}, 100); d == nil || d.Spell != prCureDisease {
		t.Errorf("disease: %+v", d)
	}
	withAbolish := append([]uint32{prAbolishDisease}, holyKnown...)
	if d := cast(withAbolish, objects.Aura{SpellID: auraRabies, Name: "Rabies"}, 100); d == nil || d.Spell != prAbolishDisease {
		t.Errorf("abolish first: %+v", d)
	}
	for _, a := range []objects.Aura{{SpellID: auraPoison, Name: "Deadly Poison"}, {SpellID: auraCurse, Name: "Curse of Agony"}, {SpellID: auraMutating, Name: "Mutating Injection"}} {
		if d := cast(holyKnown, a, 100); d != nil {
			t.Errorf("a priest cannot (or must not) dispel %s: %+v", a.Name, d)
		}
	}
	// A beneficial aura is not dispelled (Dispel Magic on a friend removes harmful magic).
	good := objects.Aura{SpellID: auraPolymorph, Name: "Polymorph"}
	g := priestFake(holyKnown...)
	r := newRig(t, g, Params{Strategies: []string{"holy_priest"}})
	g.member(dpsG1, "Mage", 8, 1000, 1000, 5, 0)
	g.addAura(dpsG1, good) // Harmful false: a buff
	if d := r.tick(100 * time.Millisecond); d != nil {
		t.Errorf("%+v", d)
	}
	// Mana: an urgent dispel needs 20%, any other 40%.
	if d := cast(holyKnown, poly, 25); d == nil || d.Action != "dispel" {
		t.Errorf("urgent dispel at 25%% mana: %+v", d)
	}
	if d := cast(holyKnown, slow, 30); d != nil {
		t.Errorf("a slow is not worth the mana at 30%%: %+v", d)
	}
	if d := cast(holyKnown, slow, 80); d == nil || d.Action != "dispel" {
		t.Errorf("a slow at 80%%: %+v", d)
	}
	// Abolish Disease already on the target: the disease is being cured.
	g = priestFake(withAbolish...)
	r = newRig(t, g, Params{Strategies: []string{"holy_priest"}})
	g.member(dpsG1, "Mage", 8, 1000, 1000, 5, 0)
	g.addAura(dpsG1, objects.Aura{SpellID: auraRabies, Name: "Rabies", Harmful: true})
	g.addAura(dpsG1, objects.Aura{SpellID: prAbolishDisease, Name: "Abolish Disease"})
	if d := r.tick(100 * time.Millisecond); d != nil {
		t.Errorf("%+v", d)
	}
}

func TestDispelPicksCrowdControlFirstAndTriesOncePerAura(t *testing.T) {
	r, g := holyRig(t)
	g.member(dpsG1, "Rogue", 4, 1000, 1000, 5, 0)
	g.member(dpsG2, "Mage", 8, 1000, 1000, 5, 3)
	g.addAura(dpsG1, objects.Aura{SpellID: auraFrostbolt, Name: "Frostbolt", Harmful: true})
	g.addAura(dpsG2, objects.Aura{SpellID: auraPolymorph, Name: "Polymorph", Harmful: true})
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "dispel" || d.Target != dpsG2 {
		t.Fatalf("%+v", d)
	}
	// The aura is still on the unit in the next row (it resisted): the brain tries the other one, then waits.
	if d := r.tick(2 * time.Second); d == nil || d.Target != dpsG1 {
		t.Fatalf("%+v", d)
	}
	if d := r.tick(1500 * time.Millisecond); d != nil {
		t.Fatalf("each aura is tried once per 4 s: %+v", d)
	}
	if d := r.tick(3 * time.Second); d == nil || d.Action != "dispel" {
		t.Fatalf("%+v", d)
	}
}

func TestEmergencyHealDoesNotCutTheCastInProgress(t *testing.T) {
	r, g := holyRig(t)
	g.slow = true // a cast stays in progress
	g.member(dpsG1, "Mage", 8, 600, 1000, 5, 0)
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "heal" {
		t.Fatalf("%+v", d)
	}
	g.member(dpsG2, "Rogue", 4, 100, 1000, 5, 3) // dying
	if d := r.tick(200 * time.Millisecond); d != nil {
		t.Fatalf("the second heal would be refused while the first is cast: %+v", d)
	}
	if n := len(g.castList()); n != 1 {
		t.Fatalf("%d casts", n)
	}
}

// ---- positions ----

func healPosRig(t *testing.T, p Params) (*rig, *posFake) {
	t.Helper()
	// No Power Word: Shield in the book: the tank would be shielded at the start of every fight these tests set up.
	var known []uint32
	for _, id := range holyKnown {
		if id != prShield1 {
			known = append(known, id)
		}
	}
	g := priestFake(known...)
	pf := &posFake{fakeGame: g}
	cfg := DefaultConfig()
	cfg.Now = g.clk.Now
	if p.Strategies == nil {
		p.Strategies = []string{"holy_priest"}
	}
	b, err := New(pf, cfg, p)
	if err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, g: g, b: b}, pf
}

func TestHealerStandsBehindTheTankAtRange(t *testing.T) {
	r, pf := healPosRig(t, Params{})
	tankFights(pf.fakeGame, 10, 12, math.Pi) // the mob faces the tank, and so the player at the origin
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "heal_reposition" {
		t.Fatalf("%+v", d)
	}
	w := pf.walkList()
	if len(w) != 1 {
		t.Fatalf("walks %v", w)
	}
	if got := dist2(w[0], Vec3{10, 0, 0}); got < healerMin || got > healerMax {
		t.Fatalf("spot %v is %.1f yd from the tank, want %v-%v", w[0], got, healerMin, healerMax)
	}
	if inFront(&objects.Unit{X: 12, Y: 0, O: math.Pi}, w[0]) {
		t.Fatalf("spot %v is in the mob's front", w[0])
	}
	// Once there, it stays put.
	if d := r.tick(2 * time.Second); d != nil && d.Action == "heal_reposition" {
		t.Fatalf("%+v", d)
	}
}

func TestHealerStaysWhenWellPlaced(t *testing.T) {
	r, pf := healPosRig(t, Params{})
	tankFights(pf.fakeGame, 24, 26, 0) // the mob faces away from the player, 24 yd from the tank
	if d := r.tick(100 * time.Millisecond); d != nil {
		t.Fatalf("%+v", d)
	}
}

func TestHealerNeedsAFightAndMovementOn(t *testing.T) {
	r, pf := healPosRig(t, Params{NoMove: true})
	tankFights(pf.fakeGame, 10, 12, math.Pi)
	if d := r.tick(100 * time.Millisecond); d != nil {
		t.Fatalf("no_move: %+v", d)
	}
	r2, pf2 := healPosRig(t, Params{})
	pf2.party = []Member{{Name: "Tankard", GUID: tankG, Online: true}}
	pf2.addUnit(objects.Unit{GUID: tankG, Type: "player", Name: "Tankard", Class: 1, InParty: true, Health: 1000, MaxHealth: 1000}, 5, 0)
	if d := r2.tick(100 * time.Millisecond); d != nil {
		t.Fatalf("no fight, no walk: %+v", d)
	}
}

func TestHealerStepsOutOfTheTanksSightLine(t *testing.T) {
	r, pf := healPosRig(t, Params{})
	tankFights(pf.fakeGame, 24, 26, 0)
	pf.blocked = func(a, b Vec3) bool { // a pillar between the player and the tank
		return wallRect(10, -3, 12, 3)(a, b)
	}
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "heal_reposition" {
		t.Fatalf("%+v", d)
	}
	w := pf.walkList()
	if len(w) != 1 || pf.blocked(w[0], Vec3{24, 0, 0}) {
		t.Fatalf("the spot must see the tank: %v", w)
	}
}

func TestHealerPrefersASpotThatSeesTheOtherMembers(t *testing.T) {
	side := func(withMember bool, northWall bool) float32 {
		r, pf := healPosRig(t, Params{})
		tankFights(pf.fakeGame, 10, 12, math.Pi)
		if withMember {
			pf.member(dpsG1, "Mage", 8, 1000, 1000, -30, 0)
			if northWall {
				pf.blocked = wallRect(-26, 0, -24, 40) // hides the member from every spot on the north side
			} else {
				pf.blocked = wallRect(-26, -40, -24, 0)
			}
		}
		if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "heal_reposition" {
			t.Fatalf("%+v", d)
		}
		w := pf.walkList()
		if len(w) != 1 {
			t.Fatalf("%v", w)
		}
		return w[0][1]
	}
	base := side(false, false)
	if math.Abs(float64(base)) < 5 {
		t.Fatalf("base spot %v", base)
	}
	// Hide the member from the side of the base spot: the healer takes the other side.
	north := base > 0
	if got := side(true, north); (got > 0) == north {
		t.Fatalf("the healer stood on the side %v that cannot see the member (base %v)", got, base)
	}
}

func TestHealerWalksTowardAMemberItCannotReach(t *testing.T) {
	r, pf := healPosRig(t, Params{})
	pf.member(dpsG1, "Mage", 8, 300, 1000, 39.5, 0) // 30%, just out of reach (the spot search covers 24 yd)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "heal_approach" {
		t.Fatalf("%+v", d)
	}
	w := pf.walkList()
	if len(w) != 1 || dist2(w[0], Vec3{39.5, 0, 0}) > healReach-5 {
		t.Fatalf("walks %v", w)
	}
}

func TestHealerRunsToTheTankWithAMobThatPickedIt(t *testing.T) {
	r, pf := healPosRig(t, Params{})
	g := pf.fakeGame
	g.party = []Member{{Name: "Tankard", GUID: tankG, Online: true}}
	g.addUnit(objects.Unit{GUID: tankG, Type: "player", Name: "Tankard", Class: 1, InParty: true, Health: 1000, MaxHealth: 1000, Target: mobB, InCombat: true}, 30, 0)
	g.addUnit(mob(mobA, selfG), 14, 0) // far from melee, on the priest
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "run_to_tank" {
		t.Fatalf("%+v", d)
	}
}

func TestHealingOutranksWalking(t *testing.T) {
	r, pf := healPosRig(t, Params{})
	tankFights(pf.fakeGame, 10, 12, math.Pi)
	pf.setHP(tankG, 300) // the tank is low: heal first, then the position
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "heal" || d.Target != tankG {
		t.Fatalf("%+v", d)
	}
	if len(pf.walkList()) != 0 {
		t.Fatalf("walked %v", pf.walkList())
	}
}

func TestHealerUsesAManaPotionInABossFight(t *testing.T) {
	r, pf := healPosRig(t, Params{NoMove: true})
	g := pf.fakeGame
	tankFights(g, 24, 26, 0)
	g.mutate(mobA, func(u *objects.Unit) { u.Level = 40; u.MaxHealth = 20000; u.Health = 20000 })
	g.items = []objects.Item{{Entry: 2455}} // Minor Mana Potion
	g.setMana(9)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "mana_potion" || len(g.used) != 1 || g.used[0] != 2455 {
		t.Fatalf("%+v used %v", d, g.used)
	}
	// Not in an ordinary fight.
	r2, pf2 := healPosRig(t, Params{NoMove: true})
	tankFights(pf2.fakeGame, 24, 26, 0)
	pf2.items = []objects.Item{{Entry: 2455}}
	pf2.setMana(9)
	if d := r2.tick(100 * time.Millisecond); d != nil && d.Action == "mana_potion" {
		t.Fatalf("%+v", d)
	}
}

// ---- review round 1 ----

func TestDispelRangeIsTheSpellsOwn(t *testing.T) {
	poly := objects.Aura{SpellID: auraPolymorph, Name: "Polymorph", Harmful: true}
	r, pf := healPosRig(t, Params{NoMove: true})
	g := pf.fakeGame
	g.member(tankG, "Tankard", 1, 1000, 1000, 32, 0) // inside the heal's 38.5 yd, outside the dispel's 30
	g.addAura(tankG, poly)
	g.member(dpsG1, "Mage", 8, 1000, 1000, 15, 0)
	g.addAura(dpsG1, poly)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "dispel" || d.Target != dpsG1 {
		t.Fatalf("the member in range is dispelled, not the tank out of range: %+v", d)
	}
	for i := 0; i < 3; i++ { // and the tank is not tried again and again
		r.tick(2 * time.Second)
	}
	for _, c := range g.castList() {
		if c.target == tankG {
			t.Fatalf("a dispel was sent at the tank out of range: %v", g.castList())
		}
	}
}

func TestHealerWalksIntoDispelRangeForCrowdControlOnly(t *testing.T) {
	poly := objects.Aura{SpellID: auraPolymorph, Name: "Polymorph", Harmful: true}
	r, pf := healPosRig(t, Params{})
	pf.member(tankG, "Tankard", 1, 1000, 1000, 32, 0)
	pf.addAura(tankG, poly)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "heal_approach" {
		t.Fatalf("%+v", d)
	}
	if w := pf.walkList(); len(w) != 1 || dist2(w[0], Vec3{32, 0, 0}) > 29 {
		t.Fatalf("walks %v", w)
	}
	if d := r.tick(2 * time.Second); d == nil || d.Action != "dispel" || d.Target != tankG {
		t.Fatalf("%+v", d)
	}
	// A slow is not worth a walk.
	r2, pf2 := healPosRig(t, Params{})
	pf2.member(tankG, "Tankard", 1, 1000, 1000, 32, 0)
	pf2.addAura(tankG, objects.Aura{SpellID: auraFrostbolt, Name: "Frostbolt", Harmful: true})
	if d := r2.tick(100 * time.Millisecond); d != nil || len(pf2.walkList()) != 0 {
		t.Fatalf("%+v %v", d, pf2.walkList())
	}
}

func TestHealerDoesNotWalkBackBeforeTheHeal(t *testing.T) {
	r, pf := healPosRig(t, Params{})
	g := pf.fakeGame
	tankFights(g, -25, -27, math.Pi)                 // the tank 25 yd west, its mob facing away from the player
	pf.member(dpsG1, "Kiter", 8, 300, 1000, 39.5, 0) // hurt, just out of reach, 64 yd from the tank
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "heal_approach" {
		t.Fatalf("%+v", d)
	}
	d = r.tick(2 * time.Second)
	if d == nil || d.Action != "heal" || d.Target != dpsG1 {
		t.Fatalf("the heal comes before the walk back to the tank: %+v", d)
	}
	if n := len(pf.walkList()); n != 1 {
		t.Fatalf("walks %v", pf.walkList())
	}
	// Healed: now the way back to the band.
	g.setHP(dpsG1, 1000)
	d = r.tick(2 * time.Second)
	if d == nil || d.Action != "heal_reposition" || len(pf.walkList()) != 2 {
		t.Fatalf("%+v %v", d, pf.walkList())
	}
}

func TestEffectAmountMatchesTheCoresRoll(t *testing.T) {
	// Lesser Heal 1: base 45, 11 sides: the core rolls 46 to 56, average 51 (level scaling is nil at level 1).
	if got := amountOf(prLesser1, 1); got != 51 {
		t.Errorf("Lesser Heal 1 heals %v", got)
	}
	// Renew 1: 8 base points and no die: 9 a tick, 5 ticks.
	if got := amountOf(prRenew1, 1); got != 45 {
		t.Errorf("Renew 1 heals %v", got)
	}
}

// ---- sitting ----

func TestNoCastWhileEatingOutOfCombatAndStandsForAFight(t *testing.T) {
	g := newFake(spFrostbolt, spIntellect, spFrostArmor, spIceArmor)
	g.self.Auras = []objects.Aura{{Name: "Frost Armor", SpellID: spFrostArmor}, {Name: "Drink", SpellID: 430}}
	g.self.StandState = "sit"
	g.setMana(50)
	r := newRig(t, g, Params{Strategies: []string{"frost_mage"}})
	// Arcane Intellect is missing, but the mage sits at its drink: nothing is sent, nobody stands.
	if d := r.tick(100 * time.Millisecond); d != nil || len(g.castList()) != 0 || g.stands != 0 {
		t.Fatalf("%+v casts %v stands %d", d, g.castList(), g.stands)
	}
	// A mob on the mage: the drink is broken, the mage stands (that is the tick), and casts the next.
	g.addUnit(mob(mobA, selfG), 20, 0)
	g.setSelf(func(u *objects.Unit) { u.InCombat = true })
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "stand_up" || g.stands != 1 || len(g.castList()) != 0 {
		t.Fatalf("%+v stands %d", d, g.stands)
	}
	if d = r.tick(2 * time.Second); d == nil || d.Spell != spFrostbolt {
		t.Fatalf("%+v", d)
	}
	if g.stands != 1 {
		t.Fatalf("stood %d times", g.stands)
	}
}

func TestStandsUpAfterAMealForUpkeep(t *testing.T) {
	g := newFake(spIntellect, spFrostArmor)
	g.self.Auras = []objects.Aura{{Name: "Frost Armor", SpellID: spFrostArmor}} // no Drink aura: the meal is over, the mage still sits
	g.self.StandState = "sit"
	r := newRig(t, g, Params{Strategies: []string{"frost_mage"}})
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "stand_up" {
		t.Fatalf("%+v", d)
	}
	if d := r.tick(2 * time.Second); d == nil || d.Spell != spIntellect {
		t.Fatalf("%+v", d)
	}
}

func TestPriestHealsWaitForTheMealUnlessItIsAnEmergency(t *testing.T) {
	r, g := holyRig(t)
	g.setSelf(func(u *objects.Unit) {
		u.InCombat = false
		u.StandState = "sit"
		u.Auras = append(u.Auras, objects.Aura{Name: "Drink"})
	})
	g.member(dpsG1, "Mage", 8, 500, 1000, 5, 0) // 50%: a medium heal, buffs missing, none breaks the meal
	g.mutate(dpsG1, func(u *objects.Unit) { u.InCombat = false; u.Auras = []objects.Aura{} })
	if d := r.tick(100 * time.Millisecond); d != nil || g.stands != 0 {
		t.Fatalf("%+v stands %d", d, g.stands)
	}
	g.setHP(dpsG1, 200) // 20%
	d := r.tick(5 * time.Second)
	if d == nil || d.Action != "stand_up" {
		t.Fatalf("%+v", d)
	}
	if d = r.tick(2 * time.Second); d == nil || d.Action != "heal" || d.Target != dpsG1 {
		t.Fatalf("%+v", d)
	}
}

func TestNotStandingRefusalBacksTheSpellOff(t *testing.T) {
	if holdFor["not_standing"] != time.Second {
		t.Fatalf("%v", holdFor["not_standing"])
	}
	g := newFake(spFrostbolt, spFrostArmor, spIntellect)
	buffUp(g)
	r := newRig(t, g, Params{Strategies: []string{"frost_mage"}})
	tankFights(g, 25, 28, 0)
	r.tick(100 * time.Millisecond)
	d := r.tick(2200 * time.Millisecond)
	if d == nil || d.Spell != spFrostbolt {
		t.Fatalf("%+v", d)
	}
	g.st.CastRefused(spFrostbolt, codeOf(t, "not_standing"))
	g.st.Clear(spFrostbolt)
	if d := r.tick(200 * time.Millisecond); d != nil && d.Spell == spFrostbolt {
		t.Fatalf("sent again at once: %+v", d)
	}
}
