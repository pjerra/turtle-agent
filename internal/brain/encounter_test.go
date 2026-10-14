package brain

import (
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tactics"
)

// Entries of the tests: a boss, a shard that must die first, a ward that is never attacked.
const (
	entBoss  uint32 = 7023
	entShard uint32 = 7209
	entWard  uint32 = 9001
	shardG          = packet.GUID(110)
	wardG           = packet.GUID(111)
)

func withEntry(u objects.Unit, entry uint32) objects.Unit {
	u.Entry = entry
	return u
}

func oneEncounter(t tactics.Tactics) []Encounter {
	return []Encounter{{Name: "test/boss", Entries: []uint32{entBoss}, Tactics: t}}
}

// bossFight is the standard mage fight with the tank on the boss and a shard next to it.
func bossFight(t *testing.T, enc []Encounter) (*rig, *fakeGame) {
	t.Helper()
	g := newFake(spFrostbolt, spFireBlast, spFrostNova, spCone, spFrostArmor, spIntellect, spCounterspell)
	g.partyWithTank()
	buffUp(g)
	g.addUnit(withEntry(mob(mobA, tankG), entBoss), 28, 0)
	g.addUnit(withEntry(mob(shardG, tankG), entShard), 27, 2)
	return newRig(t, g, Params{Strategies: []string{"frost_mage"}, Encounters: enc}), g
}

func TestKillFirstTakesTheShardBeforeTheBoss(t *testing.T) {
	r, g := bossFight(t, oneEncounter(tactics.Tactics{KillFirst: []uint32{entShard}}))
	r.tick(100 * time.Millisecond)
	d := r.tick(2200 * time.Millisecond)
	if d == nil || d.Action != "frostbolt" || d.Target != shardG {
		t.Fatalf("the shard is first: %+v", d)
	}
	// The shard dies: the boss is the target again.
	g.mutate(shardG, func(u *objects.Unit) { u.Dead = true })
	if d := r.tick(2 * time.Second); d == nil || d.Target != mobA {
		t.Fatalf("%+v", d)
	}
}

func TestWithoutTacticsTheTanksMobIsFought(t *testing.T) {
	r, _ := bossFight(t, nil)
	r.tick(100 * time.Millisecond)
	if d := r.tick(2200 * time.Millisecond); d == nil || d.Target != mobA {
		t.Fatalf("%+v", d)
	}
}

func TestKillFirstWaitsForTheEncounterToBeOn(t *testing.T) {
	// The boss is not in view: the encounter is off and the shard is just a mob.
	r, g := bossFight(t, oneEncounter(tactics.Tactics{KillFirst: []uint32{entShard}}))
	g.mutate(mobA, func(u *objects.Unit) { u.Dead = true })
	g.mutate(tankG, func(u *objects.Unit) { u.Target = shardG })
	r.tick(100 * time.Millisecond)
	if d := r.tick(2200 * time.Millisecond); d == nil || d.Target != shardG {
		t.Fatalf("%+v", d)
	}
	if evs := g.events(events.KindBrainState); len(evs) != 0 {
		t.Fatalf("an encounter that never started was reported: %+v", evs)
	}
}

func TestNeverTargetIsLeftAloneUnlessItAttacksTheLeader(t *testing.T) {
	enc := oneEncounter(tactics.Tactics{NeverTarget: []uint32{entWard}})
	g := newFake(spFrostbolt, spFireBlast, spFrostNova, spCone, spFrostArmor, spIntellect)
	g.partyWithTank()
	buffUp(g)
	g.addUnit(withEntry(mob(mobB, tankG), entBoss), 28, 3)
	g.addUnit(withEntry(mob(wardG, tankG), entWard), 28, 0)
	g.mutate(tankG, func(u *objects.Unit) { u.Target = wardG })
	r := newRig(t, g, Params{Strategies: []string{"frost_mage"}, Encounters: enc})
	r.tick(100 * time.Millisecond)
	if d := r.tick(2200 * time.Millisecond); d != nil && d.Target == wardG {
		t.Fatalf("the ward was attacked: %+v", d)
	}
	// It attacks the leader: it is fought like any mob that has the player.
	g.mutate(wardG, func(u *objects.Unit) { u.Target = selfG })
	if d := r.tick(2200 * time.Millisecond); d == nil || d.Target != wardG {
		t.Fatalf("%+v", d)
	}
}

func TestEncounterIsReportedWhenItStartsAndEnds(t *testing.T) {
	r, g := bossFight(t, oneEncounter(tactics.Tactics{KillFirst: []uint32{entShard}}))
	r.tick(100 * time.Millisecond)
	r.tick(100 * time.Millisecond)
	evs := g.events(events.KindBrainState)
	if len(evs) != 1 || evs[0].Data["state"] != "encounter" || evs[0].Data["encounter"] != "test/boss" {
		t.Fatalf("%+v", evs)
	}
	g.mutate(mobA, func(u *objects.Unit) { u.Dead = true })
	r.tick(100 * time.Millisecond)
	evs = g.events(events.KindBrainState)
	if len(evs) != 2 || evs[1].Data["state"] != "encounter_end" {
		t.Fatalf("%+v", evs)
	}
}

// ---- interrupts ----

func TestPlannedCastIsInterruptedBeforeAHeal(t *testing.T) {
	scene := func(enc []Encounter) *Decision {
		g := newFake(spFrostbolt, spFireBlast, spFrostNova, spCone, spFrostArmor, spIntellect, spCounterspell)
		g.partyWithTank()
		buffUp(g)
		g.addUnit(withEntry(mob(mobA, tankG), entBoss), 28, 0)
		healer := withEntry(mob(mobB, 0), 5000)
		healer.Casting = &objects.Cast{SpellID: spLesserHeal, Target: mobA, EndsInMS: ms(2000)}
		g.addUnit(healer, 20, 4)
		caster := withEntry(mob(shardG, 0), 5001)
		caster.Casting = &objects.Cast{SpellID: spShadowBolt, Target: 0, EndsInMS: ms(2000)}
		g.addUnit(caster, 20, -4)
		r := newRig(t, g, Params{Strategies: []string{"frost_mage"}, Encounters: enc})
		return r.tick(100 * time.Millisecond)
	}
	if d := scene(nil); d == nil || d.Action != "interrupt" || d.Target != mobB {
		t.Fatalf("by default the heal is first: %+v", d)
	}
	byName := oneEncounter(tactics.Tactics{Interrupt: []tactics.SpellRef{{Name: "Shadow Bolt"}}})
	if d := scene(byName); d == nil || d.Action != "interrupt" || d.Target != shardG {
		t.Fatalf("the plan names Shadow Bolt: %+v", d)
	}
	byID := oneEncounter(tactics.Tactics{Interrupt: []tactics.SpellRef{{ID: spShadowBolt}}})
	if d := scene(byID); d == nil || d.Target != shardG {
		t.Fatalf("by id: %+v", d)
	}
}

// ---- dispels ----

func TestHealerDispelsThePlannedAuraAndSparesTheOneThePlanForbids(t *testing.T) {
	boss := func(g *fakeGame) { g.addUnit(withEntry(mob(mobA, tankG), entBoss), 20, 0) }
	run := func(enc []Encounter, aura objects.Aura, mana uint32, bossIn bool) *Decision {
		g := priestFake(holyKnown...)
		if bossIn {
			boss(g)
		}
		r := newRig(t, g, Params{Strategies: []string{"holy_priest"}, Encounters: enc})
		g.member(dpsG1, "Mage", 8, 1000, 1000, 5, 0)
		g.setMana(mana)
		aura.Harmful = true
		g.addAura(dpsG1, aura)
		return r.tick(100 * time.Millisecond)
	}
	slow := objects.Aura{SpellID: auraFrostbolt, Name: "Frostbolt"}
	poly := objects.Aura{SpellID: auraPolymorph, Name: "Polymorph"}
	planned := oneEncounter(tactics.Tactics{Dispel: []tactics.SpellRef{{Name: "Frostbolt"}}})
	if d := run(nil, slow, 30, true); d != nil {
		t.Fatalf("a slow is not worth the mana at 30%%: %+v", d)
	}
	if d := run(planned, slow, 30, true); d == nil || d.Action != "dispel" || d.Target != dpsG1 {
		t.Fatalf("the plan wants it gone: %+v", d)
	}
	if d := run(planned, slow, 30, false); d != nil {
		t.Fatalf("the boss is not in view, the encounter is off: %+v", d)
	}
	forbid := oneEncounter(tactics.Tactics{NoDispel: []tactics.SpellRef{{ID: auraPolymorph}}})
	if d := run(nil, poly, 100, true); d == nil || d.Action != "dispel" {
		t.Fatalf("by default a polymorph is dispelled: %+v", d)
	}
	if d := run(forbid, poly, 100, true); d != nil {
		t.Fatalf("the plan forbids it: %+v", d)
	}
}

// ---- move out ----

func TestLeavesTheCircleOfAPlannedCast(t *testing.T) {
	enc := oneEncounter(tactics.Tactics{MoveOut: []tactics.MoveOut{{Cast: &tactics.SpellRef{Name: "Shadow Bolt"}, Yards: 12}}})
	r, pf := newPosRig(t, Params{Encounters: enc})
	boss := withEntry(mob(mobA, tankG), entBoss)
	boss.Casting = &objects.Cast{SpellID: spShadowBolt, EndsInMS: ms(2000)}
	r.g.addUnit(boss, 6, 0)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "leave_ground_aoe" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if w := pf.walkList()[0]; dist2(w, Vec3{6, 0, 0}) < 12 {
		t.Fatalf("walked to %v, %.1f yd from the caster", w, dist2(w, Vec3{6, 0, 0}))
	}
}

func TestStaysWhenTheCastIsAnotherSpellOrFarEnough(t *testing.T) {
	enc := oneEncounter(tactics.Tactics{MoveOut: []tactics.MoveOut{{Cast: &tactics.SpellRef{Name: "Shadow Bolt"}, Yards: 12}}})
	r, pf := newPosRig(t, Params{Encounters: enc})
	boss := withEntry(mob(mobA, tankG), entBoss)
	boss.Casting = &objects.Cast{SpellID: spLesserHeal, EndsInMS: ms(2000)}
	r.g.addUnit(boss, 6, 0)
	if d := r.tick(100 * time.Millisecond); d != nil && d.Action == "leave_ground_aoe" {
		t.Fatalf("another spell: %+v", d)
	}
	r.g.mutate(mobA, func(u *objects.Unit) { u.Casting = &objects.Cast{SpellID: spShadowBolt, EndsInMS: ms(2000)}; u.X = 20 })
	if d := r.tick(1500 * time.Millisecond); d != nil && d.Action == "leave_ground_aoe" {
		t.Fatalf("20 yd away: %+v", d)
	}
	if len(pf.walkList()) != 0 {
		t.Fatalf("walked: %v", pf.walkList())
	}
}

func TestATankHoldsItsPlaceInACastCircle(t *testing.T) {
	enc := oneEncounter(tactics.Tactics{MoveOut: []tactics.MoveOut{{Cast: &tactics.SpellRef{Name: "Shadow Bolt"}}}})
	r, _ := newPosRig(t, Params{Encounters: enc})
	boss := withEntry(mob(mobA, tankG), entBoss)
	boss.Casting = &objects.Cast{SpellID: spShadowBolt, EndsInMS: ms(2000)}
	r.g.addUnit(boss, 4, 0)
	c := r.b.newCtx(r.g.clk.Now())
	if hs := tacticCastHazards(c); len(hs) != 1 || hs[0].Radius != tactics.DefaultYard {
		t.Fatalf("%+v", hs)
	}
	c.P.Role = RoleTank
	if hs := tacticCastHazards(c); len(hs) != 0 {
		t.Fatalf("a tank leaves its place: %+v", hs)
	}
}

func TestLeavesAGroundEffectOfAPlannedSpellWhoeverCastIt(t *testing.T) {
	// A Blizzard of the tank's is the party's own and is ignored; the plan names it, so it is a hazard.
	area := objects.DynamicObject{GUID: 0xF100000000000001, Spell: spBlizzard, Caster: tankG, Radius: 8, X: 1, Y: 0}
	plain, pfPlain := newPosRig(t, Params{})
	plain.g.partyWithTank()
	pfPlain.areas = []objects.DynamicObject{area}
	if d := plain.tick(100 * time.Millisecond); d != nil && d.Action == "leave_ground_aoe" {
		t.Fatalf("the party's own area: %+v", d)
	}
	enc := oneEncounter(tactics.Tactics{MoveOut: []tactics.MoveOut{{Ground: &tactics.SpellRef{ID: spBlizzard}}}})
	r, pf := newPosRig(t, Params{Encounters: enc})
	r.g.partyWithTank()
	r.g.addUnit(withEntry(mob(mobA, tankG), entBoss), 28, 0)
	pf.areas = []objects.DynamicObject{area}
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "leave_ground_aoe" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
}
