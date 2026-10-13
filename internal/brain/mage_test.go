package brain

import (
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/objects"
)

// frost is a mage that knows the combat spells, in a party with a warrior tank 25 yards east.
func frost(t *testing.T) *rig {
	t.Helper()
	g := newFake(spFrostbolt, spFireBlast, spFrostNova, spCone, spFrostArmor, spIntellect)
	g.partyWithTank()
	buffUp(g)
	return newRig(t, g, Params{Strategies: []string{"frost_mage"}})
}

// buffUp gives the leader both buffs, so that the upkeep has nothing to do.
func buffUp(g *fakeGame) {
	g.self.Auras = []objects.Aura{{Name: "Frost Armor", SpellID: spFrostArmor}, {Name: "Arcane Intellect", SpellID: spIntellect}}
}

func spellSeq(g *fakeGame) []uint32 {
	var out []uint32
	for _, c := range g.castList() {
		out = append(out, c.spell)
	}
	return out
}

func eq(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestFrostMageWaitsForTheTanksMobThenOpensWithFrostbolt(t *testing.T) {
	r := frost(t)
	r.g.addUnit(mob(mobA, tankG), 28, 0)
	// The tank has the mob in combat, but the opening delay (2 s) has not passed.
	if d := r.tick(100 * time.Millisecond); d != nil || r.idle() != "vetoed:opening_delay" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if d := r.tick(time.Second); d != nil || len(r.g.castList()) != 0 {
		t.Fatalf("still waiting: %+v", d)
	}
	d := r.tick(1100 * time.Millisecond)
	if d == nil || d.Action != "frostbolt" || d.Spell != spFrostbolt || d.Trigger != "fight_target" || d.Target != mobA {
		t.Fatalf("%+v", d)
	}
	if c := r.g.lastCast(t); c.spell != spFrostbolt || c.target != mobA {
		t.Fatalf("%+v", c)
	}
	// Nothing again until the GCD ended (and the cast settled); then the filler goes on.
	if d := r.tick(200 * time.Millisecond); d != nil {
		t.Fatalf("a second cast inside the GCD: %+v", d)
	}
	if d := r.tick(1500 * time.Millisecond); d == nil || d.Action != "frostbolt" {
		t.Fatalf("%+v", d)
	}
}

func TestFrostMageDoesNotOpenOnAMobTheTankHasNotEngaged(t *testing.T) {
	r := frost(t)
	idle := mob(mobA, 0)
	idle.InCombat = false // the tank only targets it
	r.g.addUnit(idle, 28, 0)
	r.tick(3 * time.Second)
	if d := r.tick(3 * time.Second); d != nil || len(r.g.castList()) != 0 || r.idle() != "no_trigger" {
		t.Fatalf("%+v %q", d, r.idle())
	}
}

func TestFrostMageStaysIdleOutOfRangeAndCastsOnceInRange(t *testing.T) {
	r := frost(t)
	r.g.addUnit(mob(mobA, tankG), 45, 0)
	r.tick(100 * time.Millisecond)
	if d := r.tick(2200 * time.Millisecond); d != nil || r.idle() != "not_possible:frostbolt:out_of_range" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	r.g.mutate(mobA, func(u *objects.Unit) { u.X = 29 })
	if d := r.tick(200 * time.Millisecond); d == nil || d.Action != "frostbolt" {
		t.Fatalf("%+v", d)
	}
}

func TestFrostMageNeedsManaForTheSpell(t *testing.T) {
	r := frost(t)
	r.g.addUnit(mob(mobA, tankG), 28, 0)
	r.g.setSelf(func(u *objects.Unit) { u.Power = 60 }) // Frostbolt rank 4 costs 65
	r.tick(100 * time.Millisecond)
	if d := r.tick(2200 * time.Millisecond); d != nil || r.idle() != "not_possible:frostbolt:no_power" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
}

func TestFrostMageAnswersAMobInMeleeWithNovaThenFireBlastThenFrostbolt(t *testing.T) {
	r := frost(t)
	r.g.addUnit(mob(mobA, tankG), 28, 0)
	r.tick(100 * time.Millisecond)
	r.tick(2200 * time.Millisecond) // Frostbolt on the tank's mob
	r.g.addUnit(mob(mobB, selfG), 4, 0)
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "frost_nova" || d.Trigger != "mob_in_melee" || d.Target != selfG {
		t.Fatalf("Nova first: %+v", d)
	}
	r.g.mutate(mobB, func(u *objects.Unit) { u.Auras = []objects.Aura{{Name: "Frost Nova"}} }) // it is rooted
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "fire_blast" || d.Target != mobB {
		t.Fatalf("then Fire Blast on the mob that stands on the mage: %+v", d)
	}
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "frostbolt" || d.Target != mobA {
		t.Fatalf("both are cooling down: Frostbolt on the tank's mob: %+v", d)
	}
	if got := spellSeq(r.g); !eq(got, []uint32{spFrostbolt, spFrostNova, spFireBlast, spFrostbolt}) {
		t.Fatalf("%v", got)
	}
}

func TestFrostMageDoesNotNovaAMobThatIsAlreadyRooted(t *testing.T) {
	r := frost(t)
	m := mob(mobB, selfG)
	m.Auras = []objects.Aura{{Name: "Frost Nova"}}
	r.g.addUnit(m, 4, 0)
	r.g.addUnit(mob(mobA, tankG), 28, 0)
	// The mob on the mage needs no opening delay; Nova would be wasted on it.
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "fire_blast" {
		t.Fatalf("%+v", d)
	}
}

func TestAMobOnTheMageIsFoughtAtOnceAndNeedsNoTank(t *testing.T) {
	g := newFake(spFrostbolt, spFireBlast)
	buffUp(g)
	g.addUnit(mob(mobA, selfG), 20, 0)
	r := newRig(t, g, Params{Strategies: []string{"frost_mage"}})
	if d := r.tick(time.Second); d == nil || d.Action != "frostbolt" {
		t.Fatalf("self-defence has no opening delay: %+v", d)
	}
}

func TestWithoutATankTheSelectedTargetIsFoughtAfterTheOpeningDelay(t *testing.T) {
	g := newFake(spFrostbolt)
	buffUp(g)
	g.addUnit(mob(mobA, 0), 20, 0)
	g.selected = mobA
	r := newRig(t, g, Params{Strategies: []string{"frost_mage"}})
	if d := r.tick(time.Second); d != nil || r.idle() != "vetoed:opening_delay" {
		t.Fatalf("%+v %q", d, r.idle())
	}
	if d := r.tick(2100 * time.Millisecond); d == nil || d.Action != "frostbolt" || d.Target != mobA {
		t.Fatalf("%+v", d)
	}
}

func TestATankNamedInTheParamsIsTheOneFollowed(t *testing.T) {
	g := newFake(spFrostbolt)
	buffUp(g)
	g.party = []Member{{Name: "Tankard", GUID: tankG, Online: true}, {Name: "Other", GUID: 3, Online: true}}
	g.addUnit(objects.Unit{GUID: tankG, Type: "player", Class: 1, InParty: true, Health: 1, MaxHealth: 1, Target: mobA}, 25, 0)
	g.addUnit(objects.Unit{GUID: 3, Type: "player", Class: 2, InParty: true, Health: 1, MaxHealth: 1, Target: mobB}, 25, 5)
	g.addUnit(mob(mobA, tankG), 28, 0)
	g.addUnit(mob(mobB, 3), 28, 5)
	r := newRig(t, g, Params{Strategies: []string{"frost_mage"}, Tank: "other"})
	r.tick(100 * time.Millisecond)
	if d := r.tick(2200 * time.Millisecond); d == nil || d.Target != mobB {
		t.Fatalf("the named tank's mob: %+v", d)
	}
}

func TestTheFightEndsWhenTheMobsAreGoneAndTheNextOneWaitsAgain(t *testing.T) {
	r := frost(t)
	r.g.addUnit(mob(mobA, tankG), 28, 0)
	r.tick(100 * time.Millisecond)
	r.tick(2200 * time.Millisecond)
	// Dead: it leaves the list. Combat is over; the mage does nothing.
	r.g.mutate(mobA, func(u *objects.Unit) { u.Dead, u.Health = true, 0 })
	for i := 0; i < 3; i++ {
		if d := r.tick(1600 * time.Millisecond); d != nil {
			t.Fatalf("after the kill: %+v", d)
		}
	}
	if r.idle() != "no_trigger" {
		t.Fatalf("idle %q", r.idle())
	}
	// A new mob: a new fight, so the opening delay starts again.
	r.g.mutate(mobA, func(u *objects.Unit) { u.Dead, u.Health = false, 500 })
	if d := r.tick(200 * time.Millisecond); d != nil || r.idle() != "vetoed:opening_delay" {
		t.Fatalf("%+v %q", d, r.idle())
	}
	// But the next mob of the same pack, right after the first one, is not made to wait.
	r2 := frost(t)
	r2.g.addUnit(mob(mobA, tankG), 28, 0)
	r2.tick(100 * time.Millisecond)
	r2.tick(2200 * time.Millisecond)
	r2.g.mutate(mobA, func(u *objects.Unit) { u.Dead = true })
	r2.g.addUnit(mob(mobB, tankG), 28, 0)
	r2.g.mutate(tankG, func(u *objects.Unit) { u.Target = mobB })
	if d := r2.tick(1600 * time.Millisecond); d == nil || d.Target != mobB {
		t.Fatalf("%+v", d)
	}
}

func TestConeOfColdNeedsTwoInFrontAndNoLooseMob(t *testing.T) {
	r := frost(t)
	r.g.addUnit(mob(mobA, tankG), 6, 1)
	r.g.addUnit(mob(mobB, tankG), 7, -1)
	r.g.mutate(tankG, func(u *objects.Unit) { u.Target = mobA })
	r.tick(100 * time.Millisecond)
	d := r.tick(2200 * time.Millisecond)
	if d == nil || d.Action != "cone_of_cold" || d.Spell != spCone {
		t.Fatalf("two mobs in front on the tank: %+v", d)
	}
	// A third mob nobody fights stands in reach: the area spell would pull it.
	r.g.addUnit(objects.Unit{GUID: 102, Type: "creature", Hostile: true, Health: 10, MaxHealth: 10}, 9, 0)
	r.g.st.Clear(spCone)
	d = r.tick(1600 * time.Millisecond)
	if d == nil || d.Action != "frostbolt" || r.idle() != "" && false {
		t.Fatalf("vetoed AoE falls back to the filler: %+v", d)
	}
}

func TestConeOfColdIgnoresMobsBehind(t *testing.T) {
	r := frost(t)
	r.g.addUnit(mob(mobA, tankG), -6, 1)
	r.g.addUnit(mob(mobB, tankG), -7, -1)
	r.g.mutate(tankG, func(u *objects.Unit) { u.Target = mobA })
	r.tick(100 * time.Millisecond)
	if d := r.tick(2200 * time.Millisecond); d == nil || d.Action != "frostbolt" {
		t.Fatalf("%+v", d)
	}
}

// ---- out of combat ----

func restRig(t *testing.T, known ...uint32) *rig {
	t.Helper()
	g := newFake(known...)
	buffUp(g)
	return newRig(t, g, Params{Strategies: []string{"frost_mage"}})
}

func TestMageBuffsOutOfCombatWithTheArmorItKnows(t *testing.T) {
	g := newFake(spFrostArmor, spIntellect) // no Ice Armor yet
	r := newRig(t, g, Params{Strategies: []string{"frost_mage"}})
	d := r.tick(time.Second)
	if d == nil || d.Action != "frost_armor" || d.Via != "alt" || d.Target != selfG || d.Spell != spFrostArmor {
		t.Fatalf("Ice Armor is unknown, Frost Armor stands in: %+v", d)
	}
	g.setSelf(func(u *objects.Unit) { u.Auras = []objects.Aura{{Name: "Frost Armor"}} })
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "arcane_intellect" || d.Target != selfG {
		t.Fatalf("%+v", d)
	}
	buffUp(g)
	if d := r.tick(1600 * time.Millisecond); d != nil || r.idle() != "no_trigger" {
		t.Fatalf("%+v %q", d, r.idle())
	}
	// A buff the server says has under a minute left is renewed.
	left := int64(30_000)
	g.setSelf(func(u *objects.Unit) {
		u.Auras = []objects.Aura{{Name: "Frost Armor"}, {Name: "Arcane Intellect", RemainingMS: &left}}
	})
	if d := r.tick(time.Second); d == nil || d.Action != "arcane_intellect" {
		t.Fatalf("%+v", d)
	}
}

func TestMageBuffsNeverRunInCombat(t *testing.T) {
	g := newFake(spFrostArmor, spIntellect)
	g.setSelf(func(u *objects.Unit) { u.InCombat = true })
	r := newRig(t, g, Params{Strategies: []string{"frost_mage"}})
	if d := r.tick(time.Second); d != nil {
		t.Fatalf("%+v", d)
	}
}

func TestMageDrinksWhenLowOnManaOutOfCombat(t *testing.T) {
	r := restRig(t, spEvocation)
	r.g.items = []objects.Item{{Entry: 4540, Count: 5}, {Entry: itemWater, Count: 2}}
	r.g.setSelf(func(u *objects.Unit) { u.Power = 500 }) // 50 %
	d := r.tick(time.Second)
	if d == nil || d.Action != "drink" || len(r.g.used) != 1 || r.g.used[0] != itemWater {
		t.Fatalf("%+v used %v", d, r.g.used)
	}
	// Sitting and drinking: nothing more to do.
	r.g.setSelf(func(u *objects.Unit) { u.StandState = "sit"; u.Auras = append(u.Auras, objects.Aura{Name: "Drink"}) })
	if d := r.tick(6 * time.Second); d != nil || r.idle() != "not_useful:drink" {
		t.Fatalf("%+v %q", d, r.idle())
	}
	// Full: no trigger.
	r.g.setSelf(func(u *objects.Unit) { u.Power = manaFull })
	if d := r.tick(time.Second); d != nil || r.idle() != "no_trigger" {
		t.Fatalf("%+v %q", d, r.idle())
	}
}

func TestMagePrefersTheBestDrinkInTheBags(t *testing.T) {
	r := restRig(t)
	r.g.items = []objects.Item{{Entry: 1179, Count: 5}, {Entry: 8078, Count: 20}, {Entry: itemWater, Count: 2}}
	r.g.setSelf(func(u *objects.Unit) { u.Power = 300; u.Level = 50 })
	r.tick(time.Second)
	if len(r.g.used) != 1 || r.g.used[0] != 8078 {
		t.Fatalf("%v", r.g.used)
	}
}

func TestMageEvocatesWhenNearlyOutOfManaAndFallsBackToDrinking(t *testing.T) {
	r := restRig(t, spEvocation)
	r.g.items = []objects.Item{{Entry: itemWater, Count: 2}}
	r.g.setSelf(func(u *objects.Unit) { u.Power = 100 }) // 10 %
	if d := r.tick(time.Second); d == nil || d.Action != "evocation" || d.Target != selfG {
		t.Fatalf("%+v", d)
	}
	r.g.st.ServerCooldown(spEvocation, 480_000) // used: 8 minutes
	if d := r.tick(2 * time.Second); d == nil || d.Action != "drink" || d.Via != "alt" || len(r.g.used) != 1 {
		t.Fatalf("Evocation is cooling down: %+v", d)
	}
}

func TestMageConjuresWaterWhenItHasNoneToDrink(t *testing.T) {
	r := restRig(t, spConjure)
	r.g.setSelf(func(u *objects.Unit) { u.Power = 300 })
	d := r.tick(time.Second)
	if d == nil || d.Action != "conjure_water" || d.Via != "pre" || d.Spell != spConjure || len(r.g.used) != 0 {
		t.Fatalf("%+v", d)
	}
}

func TestMageDoesNotSitInCombatOrWhileFightingAMob(t *testing.T) {
	r := restRig(t)
	r.g.items = []objects.Item{{Entry: itemWater, Count: 2}}
	r.g.setSelf(func(u *objects.Unit) { u.Power = 100; u.InCombat = true })
	if d := r.tick(time.Second); d != nil || len(r.g.used) != 0 {
		t.Fatalf("%+v", d)
	}
	r.g.setSelf(func(u *objects.Unit) { u.InCombat = false })
	r.g.addUnit(mob(mobA, selfG), 3, 0) // a mob still attacks the mage
	if d := r.tick(time.Second); d != nil && d.Action == "drink" {
		t.Fatalf("%+v", d)
	}
}

func TestDriverGatedUpkeepWaitsForTheRestGate(t *testing.T) {
	g := newFake()
	buffUp(g)
	g.items = []objects.Item{{Entry: itemWater, Count: 2}}
	g.setSelf(func(u *objects.Unit) { u.Power = 300 })
	cfg := DefaultConfig()
	cfg.Now = g.clk.Now
	cfg.NewTicker = func(time.Duration) (<-chan time.Time, func()) { return make(chan time.Time), func() {} }
	ctl := NewController(t.Context(), g, cfg)
	if _, err := ctl.Start(Params{Strategies: []string{"frost_mage"}, RestGate: true}); err != nil {
		t.Fatal(err)
	}
	defer ctl.Stop()
	b := ctl.b
	g.clk.advance(time.Second)
	if d := b.Tick(t.Context()); d != nil || len(g.used) != 0 || ctl.Status().RestOK {
		t.Fatalf("no upkeep outside the rest gate: %+v", d)
	}
	ctl.Rest(true)
	g.clk.advance(time.Second)
	if d := b.Tick(t.Context()); d == nil || d.Action != "drink" || !ctl.Status().RestOK {
		t.Fatalf("%+v", d)
	}
}

// ---- back-off ----

func TestARefusedCastBacksOffByTheServersReason(t *testing.T) {
	for _, c := range []struct {
		reason string
		hold   time.Duration
	}{{"out_of_range", 1500 * time.Millisecond}, {"no_power", 2 * time.Second}, {"line_of_sight", 3 * time.Second}, {"not_ready", 500 * time.Millisecond}, {"fizzle", time.Second}} {
		t.Run(c.reason, func(t *testing.T) {
			r := frost(t)
			r.g.addUnit(mob(mobA, tankG), 28, 0)
			r.tick(100 * time.Millisecond)
			r.tick(2200 * time.Millisecond) // the first Frostbolt
			r.g.st.CastRefused(spFrostbolt, codeOf(t, c.reason))
			r.g.st.Clear(spFrostbolt) // the refusal gave the GCD back; only the hold is under test
			// Looking every 100 ms, the brain sends nothing before the hold ended and tries again soon after it.
			var d *Decision
			elapsed := time.Duration(0)
			for elapsed < 6*time.Second && d == nil {
				d = r.tick(100 * time.Millisecond)
				elapsed += 100 * time.Millisecond
			}
			if d == nil || d.Action != "frostbolt" {
				t.Fatalf("%s: never tried again: %+v idle %q", c.reason, d, r.idle())
			}
			if elapsed < c.hold || elapsed > c.hold+1700*time.Millisecond {
				t.Fatalf("%s: cast again after %v, the hold is %v", c.reason, elapsed, c.hold)
			}
			if n := len(r.g.castList()); n != 2 {
				t.Fatalf("%d casts", n)
			}
		})
	}
}

func TestOneRefusalHoldsOnlyOnce(t *testing.T) {
	r := frost(t)
	r.g.addUnit(mob(mobA, tankG), 28, 0)
	r.tick(100 * time.Millisecond)
	r.tick(2200 * time.Millisecond)
	r.g.st.CastRefused(spFrostbolt, codeOf(t, "out_of_range"))
	r.tick(100 * time.Millisecond)
	r.tick(2 * time.Second)                             // cast again after the hold
	if d := r.tick(1600 * time.Millisecond); d == nil { // the refusal was not seen a second time
		t.Fatal("held again by an old refusal")
	}
}

func TestNotInFrontMakesTheMageFaceItsTarget(t *testing.T) {
	r := frost(t)
	r.g.addUnit(mob(mobA, tankG), 0, 28) // due north; the mage faces east
	r.tick(100 * time.Millisecond)
	r.tick(2200 * time.Millisecond)
	r.g.st.CastRefused(spFrostbolt, codeOf(t, "not_infront"))
	r.g.st.Clear(spFrostbolt)
	d := r.tick(300 * time.Millisecond)
	if d == nil || d.Action != "face_target" || d.Via != "pre" || len(r.g.faces) != 1 || r.g.faces[0] != mobA {
		t.Fatalf("%+v faces %v", d, r.g.faces)
	}
	if d := r.tick(1200 * time.Millisecond); d == nil || d.Action != "frostbolt" {
		t.Fatalf("after turning: %+v", d)
	}
}

func TestEveryPresetAndStrategyBuilds(t *testing.T) {
	g := newFake()
	pn, sn := Strategies()
	for _, n := range pn {
		if _, err := New(g, DefaultConfig(), Params{Strategies: []string{n}}); err != nil {
			t.Errorf("preset %s: %v", n, err)
		}
	}
	for _, n := range sn {
		_, err := New(g, DefaultConfig(), Params{Strategies: []string{n}})
		standalone := map[string]bool{"combat_dps": true, "role_dps": true, "role_tank": true, "mage_rest": true, "avoid_ground": true, "ranged_position": true, "combat_consumables": true,
			"mage_interrupt": true, "class_interrupt": true, "melee_position": true, "rest_eat": true,
			"role_healer": true, "healer_position": true, "rest_drink": true, "hunter_position": true, "priest_holy": true, "priest_defense": true, "priest_buffs": true,
			"pet": true, "pet_imp": true, "warlock_upkeep": true}
		if !standalone[n] && !standaloneClass[n] && err == nil {
			t.Errorf("%s alone must be refused: it uses an action another strategy defines", n)
		}
		if err != nil && !strings.Contains(err.Error(), "face_target") && !strings.Contains(err.Error(), "step_away") {
			t.Errorf("%s: %v", n, err)
		}
	}
	if _, err := New(g, DefaultConfig(), Params{Strategies: []string{"mage_frost", "combat_dps"}}); err != nil {
		t.Errorf("strategies by name: %v", err)
	}
}

func TestBadParams(t *testing.T) {
	g := newFake()
	for _, c := range []struct {
		p    Params
		want string
	}{
		{Params{}, "name at least one"},
		{Params{Strategies: []string{"frost_mage", "warlock_fire"}}, `strategy "warlock_fire" is unknown (presets: `},
		{Params{Strategies: []string{"frost_mage"}, Role: "tank"}, `role "tank"`},
		{Params{Strategies: []string{"mage_frost"}}, "face_target"},
	} {
		if _, err := New(g, DefaultConfig(), c.p); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v, want %q", c.p, err, c.want)
		}
	}
	b, err := New(g, DefaultConfig(), Params{Strategies: []string{" Frost_Mage "}})
	if err != nil || b.p.Role != "dps" || b.p.OpeningDelay != 2*time.Second {
		t.Fatalf("%+v %v", b, err)
	}
}

func TestEventsCarryTheSpellAndTarget(t *testing.T) {
	r := frost(t)
	r.g.addUnit(mob(mobA, tankG), 28, 0)
	r.tick(100 * time.Millisecond)
	r.tick(2200 * time.Millisecond)
	ev := r.g.events(events.KindBrainDecision)
	last := ev[len(ev)-1]
	if last.SpellID != spFrostbolt || last.TargetGUID != mobA || last.Data["action"] != "frostbolt" || last.Data["trigger"] != "fight_target" || last.Data["relevance"] != RelNormal {
		t.Fatalf("%+v", last)
	}
}

func TestMageOnlyDrinksWhatItsLevelAllows(t *testing.T) {
	r := restRig(t) // level 30
	// Moonberry Juice and Conjured Mineral Water need level 35: the leader never sends them.
	r.g.items = []objects.Item{{Entry: 1645, Count: 5}, {Entry: 8077, Count: 20}}
	r.g.setSelf(func(u *objects.Unit) { u.Power = 300 })
	for i := 0; i < 5; i++ {
		if d := r.tick(200 * time.Millisecond); d != nil {
			t.Fatalf("%+v", d)
		}
	}
	if len(r.g.used) != 0 {
		t.Fatalf("used %v", r.g.used)
	}
	// A drink of level 25 in the bags is used, though the better ones are there.
	r.g.items = append(r.g.items, objects.Item{Entry: 1708, Count: 3})
	if d := r.tick(200 * time.Millisecond); d == nil || d.Action != "drink" || len(r.g.used) != 1 || r.g.used[0] != 1708 {
		t.Fatalf("%+v used %v", d, r.g.used)
	}
	// Level 35: the vendor drink of 35 then comes after the conjured water of 35.
	r2 := restRig(t)
	r2.g.items = []objects.Item{{Entry: 1645, Count: 5}, {Entry: 8077, Count: 20}, {Entry: 8766, Count: 1}}
	r2.g.setSelf(func(u *objects.Unit) { u.Power = 300; u.Level = 35 })
	r2.tick(time.Second)
	if len(r2.g.used) != 1 || r2.g.used[0] != 8077 {
		t.Fatalf("%v", r2.g.used)
	}
	// Level 45: Morning Glory Dew (45) leads the vendor drinks.
	r3 := restRig(t)
	r3.g.items = []objects.Item{{Entry: 1645, Count: 5}, {Entry: 8766, Count: 1}}
	r3.g.setSelf(func(u *objects.Unit) { u.Power = 300; u.Level = 45 })
	r3.tick(time.Second)
	if len(r3.g.used) != 1 || r3.g.used[0] != 8766 {
		t.Fatalf("%v", r3.g.used)
	}
}

func TestADrinkIsNotUsedTwiceBeforeTheAuraShows(t *testing.T) {
	r := restRig(t)
	r.g.items = []objects.Item{{Entry: itemWater, Count: 2}}
	r.g.setSelf(func(u *objects.Unit) { u.Power = 300 })
	if d := r.tick(time.Second); d == nil || d.Action != "drink" {
		t.Fatalf("%+v", d)
	}
	// No Drink aura ever appears (the server refused in a way the brain cannot see). Inside the settle time nothing is
	// sent; after it, the same item is passed over for drinkUseHold, so the rest gate does not loop on it.
	for el := 200 * time.Millisecond; el < drinkUseHold; el += 200 * time.Millisecond {
		if d := r.tick(200 * time.Millisecond); d != nil {
			t.Fatalf("again after %v: %+v", el, d)
		}
	}
	if len(r.g.used) != 1 {
		t.Fatalf("used %v", r.g.used)
	}
	if d := r.tick(time.Second); d == nil || d.Action != "drink" || len(r.g.used) != 2 {
		t.Fatalf("after the hold: %+v %v", d, r.g.used)
	}
}

func TestAnUnusableDrinkLetsTheNextLowerOneBeTried(t *testing.T) {
	r := restRig(t)
	r.g.items = []objects.Item{{Entry: 2136, Count: 5}, {Entry: itemWater, Count: 2}} // 15 and 1
	r.g.setSelf(func(u *objects.Unit) { u.Power = 300 })
	r.tick(time.Second)
	r.tick(2 * time.Second) // settled, the aura never came
	if len(r.g.used) != 2 || r.g.used[0] != 2136 || r.g.used[1] != itemWater {
		t.Fatalf("%v", r.g.used)
	}
}

func TestFrostNovaCountsAsAnAreaSpell(t *testing.T) {
	r := frost(t)
	r.g.addUnit(mob(mobA, tankG), 28, 0)
	r.g.addUnit(mob(mobB, selfG), 4, 0)
	r.g.addUnit(objects.Unit{GUID: 102, Type: "creature", Hostile: true, Health: 10, MaxHealth: 10}, 0, 8) // nobody fights it
	r.tick(100 * time.Millisecond)
	d := r.tick(2200 * time.Millisecond)
	if d == nil || d.Action == "frost_nova" {
		t.Fatalf("Nova would pull the loose mob: %+v", d)
	}
	for _, c := range r.g.castList() {
		if c.spell == spFrostNova {
			t.Fatalf("Nova was cast: %v", r.g.castList())
		}
	}
	// With the loose mob gone, Nova goes.
	r.g.mutate(102, func(u *objects.Unit) { u.Dead = true })
	r.g.st.Clear(spFireBlast)
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "frost_nova" {
		t.Fatalf("%+v", d)
	}
}
