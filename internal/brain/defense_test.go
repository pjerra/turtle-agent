package brain

import (
	"math"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
)

// ---- aggro: a mob that has picked the leader ----

// interruptTarget finds the enemy cast to interrupt with the named spell: in the spell's range, the highest priority, the soonest
// to end among those.
func interruptTarget(c *Ctx, spell string) *objects.Unit {
	id, ok := c.Spells.Highest(spell)
	if !ok {
		return nil
	}
	return interruptTargetID(c, id)
}

func TestRootedMobOnTheMageMakesItStepBackOutOfReach(t *testing.T) {
	r, pf := newPosRig(t, Params{})
	r.g.addUnit(mob(mobA, selfG), 5, 0)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "frost_nova" {
		t.Fatalf("a mob in melee is answered with Frost Nova: %+v", d)
	}
	r.g.mutate(mobA, func(u *objects.Unit) { u.Auras = []objects.Aura{{Name: "Frost Nova", SpellID: spFrostNova}} })
	d = r.tick(1600 * time.Millisecond)
	if d == nil || d.Action != "step_away" || d.Trigger != "rooted_mob_close" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	w := pf.walkList()
	if len(w) != 1 || dist2(w[0], Vec3{5, 0, 0}) < rangedMin {
		t.Fatalf("walked to %v", w)
	}
	// Out of the root's reach and in the band: the mage goes back to bolting the mob.
	d = r.tick(1600 * time.Millisecond)
	if d == nil || d.Action != "frostbolt" {
		t.Fatalf("%+v", d)
	}
}

func TestBlinkOutOfMeleeWhenHurtAndFrostNovaIsNotThere(t *testing.T) {
	novaDown := func(t *testing.T) (*rig, *posFake) {
		r, pf := newPosRig(t, Params{})
		r.g.st.StartCooldown(spFrostNova, "cast")
		r.g.addUnit(mob(mobA, selfG), 5, 0)
		r.g.setSelf(func(u *objects.Unit) { u.Health = 350; u.O = math.Pi }) // 44%, facing away from the mob
		return r, pf
	}
	r, _ := novaDown(t)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "blink" || d.Rel != RelInterrupt+5 || d.Spell != spBlink {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if c := r.g.lastCast(t); c.spell != spBlink || c.target != selfG {
		t.Fatalf("%+v", c)
	}
	// Facing the mob, Blink would land in it.
	r, _ = novaDown(t)
	r.g.setSelf(func(u *objects.Unit) { u.O = 0 })
	if d := r.tick(100 * time.Millisecond); d != nil && d.Action == "blink" {
		t.Fatalf("blinked into the mob: %+v", d)
	}
	// Not hurt: no Blink.
	r, _ = novaDown(t)
	r.g.setSelf(func(u *objects.Unit) { u.Health = 700 })
	if d := r.tick(100 * time.Millisecond); d != nil && d.Action == "blink" {
		t.Fatalf("blinked at 87%% health: %+v", d)
	}
	// Frost Nova ready: it comes first.
	r, _ = newPosRig(t, Params{})
	r.g.addUnit(mob(mobA, selfG), 5, 0)
	r.g.setSelf(func(u *objects.Unit) { u.Health = 350; u.O = math.Pi })
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "frost_nova" {
		t.Fatalf("%+v", d)
	}
	// A wall ahead: no Blink.
	r, pf := novaDown(t)
	pf.blocked = wallRect(-9, -3, -6, 3)
	if d := r.tick(100 * time.Millisecond); d != nil && d.Action == "blink" {
		t.Fatalf("blinked into a wall: %+v", d)
	}
}

func TestIceBlockAtCriticalHealthWithAMobOnTheMage(t *testing.T) {
	r, _ := newPosRig(t, Params{})
	r.g.addUnit(mob(mobA, selfG), 5, 0)
	r.g.setSelf(func(u *objects.Unit) { u.Health = 120; u.InCombat = true }) // 15%
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "ice_block" || d.Rel != RelDispel || d.Spell != spIceBlock {
		t.Fatalf("%+v", d)
	}
	// Frozen in the block, the potion and the healthstone stay in the bags.
	r.g.items = []objects.Item{{Entry: 5509, Count: 1}, {Entry: 929, Count: 1}}
	r.g.setSelf(func(u *objects.Unit) { u.Auras = append(u.Auras, objects.Aura{Name: "Ice Block"}) })
	if d := r.tick(2 * time.Second); d != nil && (d.Action == "healthstone" || d.Action == "health_potion" || d.Action == "ice_block") {
		t.Fatalf("%+v", d)
	}
	// Not at 30%, and not without a mob on the mage.
	r, _ = newPosRig(t, Params{})
	r.g.addUnit(mob(mobA, selfG), 5, 0)
	r.g.setSelf(func(u *objects.Unit) { u.Health = 240 })
	if d := r.tick(100 * time.Millisecond); d != nil && d.Action == "ice_block" {
		t.Fatalf("%+v", d)
	}
	r, _ = newPosRig(t, Params{})
	r.g.addUnit(mob(mobA, tankG), 20, 0)
	r.g.setSelf(func(u *objects.Unit) { u.Health = 120 })
	if d := r.tick(100 * time.Millisecond); d != nil && d.Action == "ice_block" {
		t.Fatalf("%+v", d)
	}
	// A mage without the talent has no Ice Block to cast.
	r, _ = newPosRig(t, Params{}, spFrostbolt, spFrostNova, spBlink)
	r.g.addUnit(mob(mobA, selfG), 5, 0)
	r.g.setSelf(func(u *objects.Unit) { u.Health = 120 })
	if d := r.tick(100 * time.Millisecond); d != nil && d.Action == "ice_block" {
		t.Fatalf("%+v", d)
	}
}

func TestRunsTowardTheTankWithAMobOnTheLeaderThatIsStillComing(t *testing.T) {
	r, pf := newPosRig(t, Params{})
	r.g.party = []Member{{Name: "Tankard", GUID: tankG, Online: true}}
	r.g.addUnit(objects.Unit{GUID: tankG, Type: "player", Name: "Tankard", Class: 1, InParty: true, Health: 900, MaxHealth: 900}, 30, 6)
	r.g.addUnit(mob(mobA, selfG), 15, 0)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "run_to_tank" || d.Trigger != "mob_on_me_far" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	w := pf.walkList()
	if len(w) != 1 || math.Abs(dist2(w[0], Vec3{30, 6, 0})-tankMeet) > 0.01 {
		t.Fatalf("ran to %v", w)
	}
	// The mob is in melee: Frost Nova, not a run with the mob on the leader's back.
	r, pf = newPosRig(t, Params{})
	r.g.party = []Member{{Name: "Tankard", GUID: tankG, Online: true}}
	r.g.addUnit(objects.Unit{GUID: tankG, Type: "player", Name: "Tankard", Class: 1, InParty: true, Health: 900, MaxHealth: 900}, 30, 6)
	r.g.addUnit(mob(mobA, selfG), 6, 0)
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "frost_nova" || len(pf.walkList()) != 0 {
		t.Fatalf("%+v", d)
	}
	// The tank is close already: nothing to run to.
	r, pf = newPosRig(t, Params{})
	r.g.party = []Member{{Name: "Tankard", GUID: tankG, Online: true}}
	r.g.addUnit(objects.Unit{GUID: tankG, Type: "player", Name: "Tankard", Class: 1, InParty: true, Health: 900, MaxHealth: 900}, 4, 4)
	r.g.addUnit(mob(mobA, selfG), 15, 0)
	r.tick(100 * time.Millisecond)
	if len(pf.walkList()) != 0 {
		t.Fatalf("ran %v", pf.walkList())
	}
	// A mob that is not on the leader: no run.
	r, pf = newPosRig(t, Params{})
	r.g.party = []Member{{Name: "Tankard", GUID: tankG, Online: true}}
	r.g.addUnit(objects.Unit{GUID: tankG, Type: "player", Name: "Tankard", Class: 1, InParty: true, Health: 900, MaxHealth: 900, Target: mobA}, 30, 6)
	r.g.addUnit(mob(mobA, tankG), 15, 0)
	r.tick(100 * time.Millisecond)
	for _, w := range pf.walkList() {
		if dist2(w, Vec3{30, 6, 0}) < 5 {
			t.Fatalf("ran to the tank without a mob on the leader: %v", w)
		}
	}
}

func TestAreaSpellStaysVetoedWhileALooseMobIsNear(t *testing.T) {
	r, _ := newPosRig(t, Params{})
	r.g.partyWithTank()
	r.g.addUnit(mob(mobA, tankG), 6, 1)
	r.g.addUnit(mob(mobB, tankG), 7, -1)
	r.g.addUnit(objects.Unit{GUID: 102, Type: "creature", Hostile: true, Health: 10, MaxHealth: 10}, 9, 0)
	r.tick(100 * time.Millisecond)
	d := r.tick(2200 * time.Millisecond)
	if d != nil && d.Action == "cone_of_cold" {
		t.Fatalf("an area spell with a loose mob near: %+v", d)
	}
}

// ---- interrupts ----

func TestCounterspellsAHealAndBreaksTheMagesOwnCast(t *testing.T) {
	r, _ := newPosRig(t, Params{})
	r.g.addUnit(mob(mobA, tankG), 20, 0)
	r.g.mutate(mobA, func(u *objects.Unit) {
		u.Casting = &objects.Cast{SpellID: spLesserHeal, EndsInMS: ms(1500), Target: mobB}
	})
	r.g.st.CastStarted(spFrostbolt, 2600, mobA) // the mage's own Frostbolt is in progress
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "interrupt" || d.Trigger != "enemy_casting" || d.Rel != RelInterrupt || d.Spell != spCounterspell || d.Target != mobA {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if c := r.g.lastCast(t); c.spell != spCounterspell || c.target != mobA {
		t.Fatalf("%+v", c)
	}
}

func TestInterruptChoosesByWhatTheCastDoesAndWhoItIsAimedAt(t *testing.T) {
	for _, c := range []struct {
		name   string
		spell  uint32
		ends   int64
		onTank bool
		dist   float32
		want   bool
	}{
		{"damage on the tank", spShadowBolt, 1500, true, 20, true},
		{"damage on nobody we know", spShadowBolt, 1500, false, 20, false},
		{"a heal on anyone", spLesserHeal, 1500, false, 20, true},
		{"crowd control on anyone", spPolymorph, 1500, false, 20, true},
		{"too late: 200 ms left", spLesserHeal, 200, false, 20, false},
		{"out of Counterspell's range", spLesserHeal, 1500, false, 35, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, _ := newPosRig(t, Params{})
			r.g.partyWithTank()
			r.g.addUnit(mob(mobA, tankG), c.dist, 0)
			var tgt = mobB
			if c.onTank {
				tgt = tankG
			}
			r.g.mutate(mobA, func(u *objects.Unit) { u.Casting = &objects.Cast{SpellID: c.spell, EndsInMS: ms(c.ends), Target: tgt} })
			d := r.tick(100 * time.Millisecond)
			got := d != nil && d.Action == "interrupt"
			if got != c.want {
				t.Fatalf("interrupted %v, want %v: %+v idle %q", got, c.want, d, r.idle())
			}
		})
	}
}

func TestInterruptPrefersCrowdControlOverDamageAndTheSoonestCast(t *testing.T) {
	r, _ := newPosRig(t, Params{})
	r.g.partyWithTank()
	r.g.addUnit(mob(mobA, tankG), 18, 0)
	r.g.addUnit(mob(mobB, tankG), 22, 3)
	r.g.mutate(mobA, func(u *objects.Unit) {
		u.Casting = &objects.Cast{SpellID: spShadowBolt, EndsInMS: ms(1000), Target: tankG}
	})
	r.g.mutate(mobB, func(u *objects.Unit) {
		u.Casting = &objects.Cast{SpellID: spPolymorph, EndsInMS: ms(1800), Target: tankG}
	})
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "interrupt" || d.Target != mobB {
		t.Fatalf("%+v", d)
	}
}

func TestAnInterruptedCastIsNotInterruptedAgainAndAFailedCallIsBackedOff(t *testing.T) {
	r, _ := newPosRig(t, Params{})
	r.g.partyWithTank()
	r.g.addUnit(mob(mobA, tankG), 20, 0)
	r.g.mutate(mobA, func(u *objects.Unit) {
		u.Casting = &objects.Cast{SpellID: spLesserHeal, EndsInMS: ms(5000), Target: tankG}
	})
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "interrupt" {
		t.Fatalf("%+v", d)
	}
	at := func(d time.Duration) *objects.Unit {
		r.g.clk.advance(d)
		return interruptTarget(r.b.newCtx(r.g.clk.Now()), "Counterspell")
	}
	first, second := at(time.Second), at(time.Second)
	if first != nil || second != nil {
		t.Fatal("the same cast was tried again within 3 s")
	}
	if at(1500*time.Millisecond) == nil {
		t.Fatal("the cast is still running after 3 s and was not tried again")
	}
	// A refused call holds the spell back for a second.
	r2, _ := newPosRig(t, Params{})
	r2.g.partyWithTank()
	r2.g.addUnit(mob(mobA, tankG), 20, 0)
	r2.g.mutate(mobA, func(u *objects.Unit) {
		u.Casting = &objects.Cast{SpellID: spLesserHeal, EndsInMS: ms(5000), Target: tankG}
	})
	r2.g.castErr = errTest
	if d := r2.tick(100 * time.Millisecond); d == nil || d.Result != ResultError {
		t.Fatalf("%+v", d)
	}
	r2.g.castErr = nil
	if d := r2.tick(300 * time.Millisecond); d != nil && d.Action == "interrupt" {
		t.Fatalf("tried again inside the back-off: %+v", d)
	}
	if d := r2.tick(1500 * time.Millisecond); d == nil || d.Action != "interrupt" {
		t.Fatalf("not tried again after it: %+v", d)
	}
}

func TestNoInterruptWithoutTheSpell(t *testing.T) {
	r, _ := newPosRig(t, Params{}, spFrostbolt)
	r.g.addUnit(mob(mobA, tankG), 20, 0)
	r.g.mutate(mobA, func(u *objects.Unit) { u.Casting = &objects.Cast{SpellID: spLesserHeal, EndsInMS: ms(1500)} })
	if d := r.tick(100 * time.Millisecond); d != nil {
		t.Fatalf("%+v", d)
	}
}

// ---- consumables ----

func TestHealthstoneThenPotionBelowCriticalHealthAndNothingTwice(t *testing.T) {
	r, _ := newPosRig(t, Params{})
	// Major Healthstone needs level 48; the leader is 30: Healthstone (24) is the best in the bags, and Healing Potion (12) the
	// best potion (Major, 45, is out of reach).
	r.g.items = []objects.Item{{Entry: 9421, Count: 1}, {Entry: 5509, Count: 1}, {Entry: 13446, Count: 1}, {Entry: 929, Count: 1}}
	r.g.setSelf(func(u *objects.Unit) { u.Health = 150; u.InCombat = true })
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "healthstone" || d.Rel != RelDispel-3 {
		t.Fatalf("%+v", d)
	}
	d = r.tick(300 * time.Millisecond)
	if d == nil || d.Action != "health_potion" {
		t.Fatalf("%+v", d)
	}
	if got := r.g.used; len(got) != 2 || got[0] != 5509 || got[1] != 929 {
		t.Fatalf("used %v", got)
	}
	for i := 0; i < 5; i++ {
		if d := r.tick(300 * time.Millisecond); d != nil && (d.Action == "healthstone" || d.Action == "health_potion") {
			t.Fatalf("used again inside the 2 minute cooldown: %+v", d)
		}
	}
	// Two minutes on, a new fight: they are used again.
	if d := r.tick(2 * time.Minute); d == nil || d.Action != "healthstone" {
		t.Fatalf("%+v", d)
	}
	if d := r.tick(300 * time.Millisecond); d == nil || d.Action != "health_potion" {
		t.Fatalf("%+v", d)
	}
}

func TestNoPotionAboveCriticalHealthOrOutOfCombat(t *testing.T) {
	r, _ := newPosRig(t, Params{})
	r.g.items = []objects.Item{{Entry: 5509, Count: 1}, {Entry: 929, Count: 1}}
	r.g.setSelf(func(u *objects.Unit) { u.Health = 300; u.InCombat = true }) // 37%: bandage territory, not potion
	if d := r.tick(100 * time.Millisecond); d != nil && (d.Action == "healthstone" || d.Action == "health_potion") {
		t.Fatalf("%+v", d)
	}
	r.g.setSelf(func(u *objects.Unit) { u.Health = 100; u.InCombat = false })
	if d := r.tick(100 * time.Millisecond); d != nil && (d.Action == "healthstone" || d.Action == "health_potion") {
		t.Fatalf("out of combat: %+v", d)
	}
}

func TestBandageOnlyOutsideTheCombatDamageWindowAndNotAgainWhenItStartedNoChannel(t *testing.T) {
	r, _ := newPosRig(t, Params{})
	r.g.setSelf(func(u *objects.Unit) { u.Health = 300; u.InCombat = true })
	r.tick(100 * time.Millisecond) // the baseline
	r.g.items = []objects.Item{{Entry: 1251, Count: 5}}
	r.g.setSelf(func(u *objects.Unit) { u.Health = 250 }) // hurt just now
	if d := r.tick(100 * time.Millisecond); d != nil && d.Action == "bandage" {
		t.Fatalf("bandaged inside the damage window: %+v", d)
	}
	if d := r.tick(4500 * time.Millisecond); d == nil || d.Action != "bandage" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if got := r.g.used; len(got) != 1 || got[0] != 1251 {
		t.Fatalf("used %v", got)
	}
	// No First Aid channel started: the bandage is not tried again for minutes.
	r.tick(1600 * time.Millisecond)
	r.tick(13 * time.Second)
	if d := r.tick(time.Second); d != nil && d.Action == "bandage" {
		t.Fatalf("bandaged again: %+v", d)
	}
	if len(r.g.used) != 1 {
		t.Fatalf("used %v", r.g.used)
	}
}

func TestBandageNeedsNoMobOnTheLeaderAndNoRecentBandage(t *testing.T) {
	r, _ := newPosRig(t, Params{})
	r.g.items = []objects.Item{{Entry: 1251, Count: 5}}
	r.g.addUnit(mob(mobA, selfG), 15, 0)
	r.g.setSelf(func(u *objects.Unit) { u.Health = 300; u.InCombat = true })
	r.tick(5 * time.Second)
	for _, u := range r.g.used {
		if u == 1251 {
			t.Fatal("bandaged with a mob on the leader")
		}
	}
	r2, _ := newPosRig(t, Params{})
	r2.g.items = []objects.Item{{Entry: 1251, Count: 5}}
	r2.g.setSelf(func(u *objects.Unit) {
		u.Health = 300
		u.InCombat = true
		u.Auras = append(u.Auras, objects.Aura{Name: "Recently Bandaged"})
	})
	r2.tick(5 * time.Second)
	if len(r2.g.used) != 0 {
		t.Fatalf("used %v", r2.g.used)
	}
}

func TestManaPotionWhenABossFightLeavesTheMageDry(t *testing.T) {
	r, _ := newPosRig(t, Params{})
	r.g.partyWithTank()
	boss := mob(mobA, tankG)
	boss.Level, boss.MaxHealth, boss.Health = 36, 20000, 20000
	r.g.addUnit(boss, 25, 0)
	r.g.items = []objects.Item{{Entry: 6149, Count: 1}, {Entry: 3827, Count: 1}} // Greater (31) is out of reach at level 30
	r.g.setSelf(func(u *objects.Unit) { u.Power = 50; u.InCombat = true })       // 5%
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "mana_potion" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if len(r.g.used) != 1 || r.g.used[0] != 3827 {
		t.Fatalf("used %v", r.g.used)
	}
	// An ordinary mob: the wand takes over instead, once the opening delay is over.
	r, _ = newPosRig(t, Params{})
	r.g.partyWithTank()
	r.g.addUnit(mob(mobA, tankG), 25, 0)
	r.g.items = []objects.Item{{Entry: 3827, Count: 1}}
	r.g.setSelf(func(u *objects.Unit) { u.Power = 50; u.InCombat = true })
	r.tick(100 * time.Millisecond)
	if d := r.tick(2200 * time.Millisecond); d == nil || d.Action != "wand" || len(r.g.used) != 0 {
		t.Fatalf("%+v used %v", d, r.g.used)
	}
}

// ---- the wand ----

func TestWandFillsInWhenTheMageCannotPayForFrostbolt(t *testing.T) {
	r, _ := newPosRig(t, Params{})
	r.g.partyWithTank()
	r.g.addUnit(mob(mobA, tankG), 24, 0)
	r.g.setSelf(func(u *objects.Unit) { u.Power = 40 }) // 4%: Frostbolt costs 65
	r.tick(100 * time.Millisecond)
	d := r.tick(2200 * time.Millisecond)
	if d == nil || d.Action != "wand" || d.Spell != spShoot || d.Target != mobA || d.Rel != RelNormal-1 {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// Shoot repeats by itself: not sent again while it runs.
	for i := 0; i < 4; i++ {
		if d := r.tick(500 * time.Millisecond); d != nil && d.Action == "wand" {
			t.Fatalf("Shoot again after %d ms: %+v", (i+1)*500, d)
		}
	}
	// With the mana back, Frostbolt is the filler again.
	r.g.setSelf(func(u *objects.Unit) { u.Power = 600 })
	if d := r.tick(1500 * time.Millisecond); d == nil || d.Action != "frostbolt" {
		t.Fatalf("%+v", d)
	}
}

func TestWandIsLeftAloneWhenTheTargetIsOutOfWandRangeOrNoWandIsEquipped(t *testing.T) {
	r, _ := newPosRig(t, Params{NoMove: true}) // standing still, so the mob stays out of range
	r.g.partyWithTank()
	r.g.addUnit(mob(mobA, tankG), 40, 0)
	r.g.setSelf(func(u *objects.Unit) { u.Power = 40 })
	r.tick(100 * time.Millisecond)
	if d := r.tick(2200 * time.Millisecond); d != nil && d.Action == "wand" {
		t.Fatalf("out of range: %+v", d)
	}
	// In range, but the server says no wand is equipped: a long back-off, not a Shoot every second.
	r2, _ := newPosRig(t, Params{NoMove: true})
	r2.g.partyWithTank()
	r2.g.addUnit(mob(mobA, tankG), 24, 0)
	r2.g.setSelf(func(u *objects.Unit) { u.Power = 40 })
	r2.tick(100 * time.Millisecond)
	if d := r2.tick(2200 * time.Millisecond); d == nil || d.Action != "wand" {
		t.Fatalf("%+v", d)
	}
	r2.g.st.CastRefused(spShoot, codeOf(t, "equipped_item_class"))
	for i := 0; i < 28; i++ { // one tick a second keeps the fight (and its opening delay) running
		if d := r2.tick(time.Second); d != nil && d.Action == "wand" {
			t.Fatalf("Shoot again after %d s without a wand: %+v", i+1, d)
		}
	}
	tried := false
	for i := 0; i < 6 && !tried; i++ {
		if d := r2.tick(time.Second); d != nil && d.Action == "wand" {
			tried = true
		}
	}
	if !tried {
		t.Fatal("not tried again after the long back-off")
	}
}
