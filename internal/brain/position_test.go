package brain

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
)

func TestLineOfSightIsCachedPerTargetFor250msAndFollowsMovement(t *testing.T) {
	r, pf := newPosRig(t, Params{})
	tankFights(r.g, 22, 24, 0)
	at := func(d time.Duration) *Ctx {
		r.g.clk.advance(d)
		return r.b.newCtx(r.g.clk.Now())
	}
	c := at(0)
	m, _ := c.Unit(mobA)
	if clear, known := c.LineOfSight(m); !clear || !known {
		t.Fatalf("clear %v known %v", clear, known)
	}
	base := pf.rayCount()
	if base != 1 {
		t.Fatalf("%d rays for one question", base)
	}
	c = at(100 * time.Millisecond)
	m, _ = c.Unit(mobA)
	c.LineOfSight(m)
	if pf.rayCount() != base {
		t.Fatal("asked again inside the 250 ms")
	}
	// Another target has its own entry.
	tk, _ := c.Unit(tankG)
	c.LineOfSight(tk)
	if pf.rayCount() != base+1 {
		t.Fatal("the tank shares the mob's entry")
	}
	c = at(200 * time.Millisecond) // 300 ms after the first answer
	m, _ = c.Unit(mobA)
	c.LineOfSight(m)
	if pf.rayCount() != base+2 {
		t.Fatalf("not asked again after the TTL: %d", pf.rayCount())
	}
	// A wall appears and the target moves two yards: asked at once, and blocked now.
	pf.blocked = wallRect(10, -8, 12, 8)
	r.g.mutate(mobA, func(u *objects.Unit) { u.X += 2 })
	c = at(10 * time.Millisecond)
	m, _ = c.Unit(mobA)
	if clear, known := c.LineOfSight(m); clear || !known {
		t.Fatalf("clear %v known %v behind the wall", clear, known)
	}
}

func TestLineOfSightUnknownWithoutSightOrAnswer(t *testing.T) {
	r := frost(t) // a fake without Sight
	r.g.addUnit(mob(mobA, tankG), 24, 0)
	c := r.b.newCtx(r.g.clk.Now())
	m, _ := c.Unit(mobA)
	if clear, known := c.LineOfSight(m); !clear || known {
		t.Fatalf("clear %v known %v", clear, known)
	}
	r2, pf := newPosRig(t, Params{})
	pf.offMesh = func(Vec3) bool { return true } // the mesh knows nothing here
	r2.g.addUnit(mob(mobA, tankG), 24, 0)
	c = r2.b.newCtx(r2.g.clk.Now())
	m, _ = c.Unit(mobA)
	if _, known := c.LineOfSight(m); known {
		t.Fatal("an unknown answer was taken as known")
	}
	if issue := positionIssue(c); issue != "" {
		t.Fatalf("issue %q on an unknown line", issue)
	}
}

func TestRangedBandStepsBackFromTooCloseAndOutOfTheMobsFront(t *testing.T) {
	r, pf := newPosRig(t, Params{})
	tankFights(r.g, 8, 12, math.Pi) // the mob faces the tank, and the leader stands in the line 12 yd from it
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "reposition" || d.Trigger != "bad_position" || d.Result != ResultOK {
		t.Fatalf("%+v", d)
	}
	w := pf.walkList()
	if len(w) != 1 {
		t.Fatalf("walks %v", w)
	}
	mobAt := unitXY(r.g, mobA)
	if got := dist2(w[0], mobAt); got < rangedMin || got > rangedMax {
		t.Fatalf("spot %v is %.1f yd from the mob, want %v-%v", w[0], got, rangedMin, rangedMax)
	}
	if inFront(&objects.Unit{X: 12, Y: 0, O: math.Pi}, w[0]) {
		t.Fatalf("spot %v is in the mob's front", w[0])
	}
	// There now: nothing more to do but wait for the tank's hold (the opening delay holds Frostbolt back).
	if d := r.tick(300 * time.Millisecond); d != nil || len(pf.walkList()) != 1 {
		t.Fatalf("walked again: %+v %v", d, pf.walkList())
	}
	d = r.tick(2 * time.Second)
	if d == nil || d.Action != "frostbolt" {
		t.Fatalf("after the step: %+v", d)
	}
}

func TestRangedBandLeavesAMobsFrontEvenAtTheRightDistance(t *testing.T) {
	r, pf := newPosRig(t, Params{})
	tankFights(r.g, 22, 24, math.Pi) // 24 yd away, but straight in the line of the mob's front
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "reposition" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	w := pf.walkList()
	if inFront(&objects.Unit{X: 24, Y: 0, O: math.Pi}, w[0]) || dist2(w[0], Vec3{24, 0, 0}) > rangedMax {
		t.Fatalf("spot %v", w[0])
	}
}

func TestRangedBandStepsToASpotWithLineOfSight(t *testing.T) {
	r, pf := newPosRig(t, Params{})
	pf.blocked = wallRect(10, -8, 12, 8)
	tankFights(r.g, 22, 24, 0) // facing away from the leader; a wall between them
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "reposition" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	w := pf.walkList()[0]
	if pf.blocked(w, Vec3{24, 0, 0}) {
		t.Fatalf("spot %v has no sight of the mob", w)
	}
	if got := dist2(w, Vec3{24, 0, 0}); got < rangedMin || got > rangedMax {
		t.Fatalf("spot %v is %.1f yd away", w, got)
	}
	// The step itself was walkable: the fake walked from (0,0) to the spot and the line was clear.
	if pf.blocked(Vec3{0, 0, 0}, w) {
		t.Fatalf("the step to %v crosses the wall", w)
	}
}

func TestRangedBandComesCloserWhenTheMobIsTooFar(t *testing.T) {
	r, pf := newPosRig(t, Params{})
	tankFights(r.g, 38, 42, 0)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "reposition" {
		t.Fatalf("%+v", d)
	}
	if got := dist2(pf.walkList()[0], Vec3{42, 0, 0}); got < rangedMin || got > rangedMax {
		t.Fatalf("%.1f yd", got)
	}
}

func TestRangedBandStaysPutInTheBandWithSightAndOutOfTheFront(t *testing.T) {
	r, pf := newPosRig(t, Params{})
	tankFights(r.g, 22, 24, 0)
	for i := 0; i < 5; i++ {
		r.tick(300 * time.Millisecond)
	}
	if len(pf.walkList()) != 0 {
		t.Fatalf("walked %v", pf.walkList())
	}
}

func TestPositioningKeepsToAnotherGroupOfSleepers(t *testing.T) {
	r, pf := newPosRig(t, Params{})
	tankFights(r.g, 8, 12, math.Pi)
	// Sleeping packs where the two nearest spots are: no spot may stand within reach of them.
	r.g.addUnit(objects.Unit{GUID: 300, Type: "creature", Hostile: true, Health: 100, MaxHealth: 100, Level: 30}, 1, 19)
	r.g.addUnit(objects.Unit{GUID: 301, Type: "creature", Hostile: true, Health: 100, MaxHealth: 100, Level: 30}, 1, -19)
	r.tick(100 * time.Millisecond)
	for _, w := range pf.walkList() {
		for _, s := range []Vec3{{1, 19, 0}, {1, -19, 0}} {
			if got := dist2(w, s); got < mobKeepAway {
				t.Fatalf("spot %v is %.1f yd from the sleepers at %v", w, got, s)
			}
		}
	}
}

func TestPositioningIsQuietWithoutAWalkerOrWithMovementOff(t *testing.T) {
	// A game that cannot walk: no reposition, no trace of it either.
	r := frost(t)
	r.g.addUnit(objects.Unit{GUID: tankG, Type: "player", Class: 1, InParty: true, Health: 900, MaxHealth: 900, Target: mobA}, 8, 0)
	m := mob(mobA, tankG)
	m.O = math.Pi
	r.g.addUnit(m, 12, 0)
	r.g.party = []Member{{Name: "Tankard", GUID: tankG, Online: true}}
	r.tick(100 * time.Millisecond)
	if r.idle() != "vetoed:opening_delay" {
		t.Fatalf("idle %q", r.idle())
	}
	// NoMove keeps even a game that can walk on the spot.
	r2, pf := newPosRig(t, Params{NoMove: true})
	tankFights(r2.g, 8, 12, math.Pi)
	r2.tick(100 * time.Millisecond)
	r2.tick(3 * time.Second)
	if len(pf.walkList()) != 0 {
		t.Fatalf("NoMove walked %v", pf.walkList())
	}
}

func TestNoSpotIsReportedOnceNotRetriedEveryTick(t *testing.T) {
	r, pf := newPosRig(t, Params{})
	tankFights(r.g, 8, 12, math.Pi)
	pf.offMesh = func(p Vec3) bool { return p[0] != 0 || p[1] != 0 } // only the player's own spot is on the mesh
	r.tick(100 * time.Millisecond)
	first := pf.snapCount()
	if first == 0 {
		t.Fatal("no search was made")
	}
	r.tick(200 * time.Millisecond)
	r.tick(200 * time.Millisecond)
	if pf.snapCount() != first {
		t.Fatalf("searched again inside the negative memo: %d -> %d", first, pf.snapCount())
	}
	if len(pf.walkList()) != 0 {
		t.Fatal("walked without a spot")
	}
}

func TestBrainNeverWalksWhileTheDriverHoldsTheMoveLock(t *testing.T) {
	r, pf := newPosRig(t, Params{})
	tankFights(r.g, 8, 12, math.Pi)
	release, err := r.b.lock.Hold(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d := r.tick(100 * time.Millisecond); d != nil && d.Action == "reposition" {
		t.Fatalf("walked under the driver: %+v", d)
	}
	r.tick(2 * time.Second)
	if len(pf.walkList()) != 0 {
		t.Fatal("walked under the driver")
	}
	release()
	d := r.tick(500 * time.Millisecond) // past the settle of the Frostbolt cast under the driver's walk
	if d == nil || d.Action != "reposition" {
		t.Fatalf("after the release: %+v idle %q", d, r.idle())
	}
}

func TestDriverHoldStopsAStepThatIsUnderWay(t *testing.T) {
	r, pf := newPosRig(t, Params{})
	pf.hold = make(chan struct{}) // the walk never arrives by itself
	tankFights(r.g, 8, 12, math.Pi)
	done := make(chan *Decision, 1)
	go func() { done <- r.tick(100 * time.Millisecond) }()
	// Wait until the brain's step has begun.
	deadline := time.Now().Add(3 * time.Second)
	for {
		r.b.lock.mu.Lock()
		started := r.b.lock.step != nil
		r.b.lock.mu.Unlock()
		if started {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the brain never began its step")
		}
		time.Sleep(5 * time.Millisecond)
	}
	release, err := r.b.lock.Hold(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	select {
	case d := <-done:
		if d == nil || d.Result != ResultOK {
			t.Fatalf("a cut-short step is not a failure: %+v", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the brain's walk was not stopped")
	}
	if len(pf.walkList()) != 0 {
		t.Fatal("the cancelled walk arrived")
	}
}

func TestAFailedWalkBacksOffTheAction(t *testing.T) {
	r, pf := newPosRig(t, Params{})
	tankFights(r.g, 8, 12, math.Pi)
	pf.walkErr = errTest
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "reposition" || d.Result != ResultError {
		t.Fatalf("%+v", d)
	}
	if d := r.tick(300 * time.Millisecond); d != nil && d.Action == "reposition" {
		t.Fatalf("tried again inside the hold: %+v", d)
	}
}

type testErr string

func (e testErr) Error() string { return string(e) }

const errTest = testErr("no route")

// ---- ground effects ----

func TestLeavesAGroundEffectOfAnEnemyAlongTheMesh(t *testing.T) {
	r, pf := newPosRig(t, Params{})
	r.g.addUnit(mob(mobA, tankG), 20, 0)
	pf.areas = []objects.DynamicObject{{GUID: 0xF100000000000001, Spell: spRainOfFire, Caster: mobA, Radius: 8, X: 1, Y: 0}}
	// A Frostbolt is being cast: leaving the fire outranks it.
	r.g.st.CastStarted(spFrostbolt, 2600, mobA)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "leave_ground_aoe" || d.Trigger != "in_ground_aoe" || d.Rel != RelInterrupt+2 {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	w := pf.walkList()[0]
	if got := dist2(w, Vec3{1, 0, 0}); got < 8+2 {
		t.Fatalf("spot %v is %.1f yd from the fire's centre", w, got)
	}
	// Out of it: nothing more.
	if d := r.tick(1500 * time.Millisecond); d != nil && d.Action == "leave_ground_aoe" {
		t.Fatalf("%+v", d)
	}
}

func TestIgnoresThePartysOwnGroundEffects(t *testing.T) {
	r, pf := newPosRig(t, Params{})
	r.g.addUnit(objects.Unit{GUID: tankG, Type: "player", Class: 2, InParty: true, Health: 900, MaxHealth: 900}, 5, 0)
	r.g.party = []Member{{Name: "Tankard", GUID: tankG, Online: true}}
	pf.areas = []objects.DynamicObject{
		{GUID: 0xF100000000000001, Spell: spRainOfFire, Caster: tankG, Radius: 8, X: 0, Y: 0},  // a party member's
		{GUID: 0xF100000000000002, Spell: spBlizzard, Caster: selfG, Radius: 8, X: 0, Y: 0},    // the leader's own
		{GUID: 0xF100000000000003, Spell: spRainOfFire, Caster: mobA, Radius: 8, X: 40, Y: 40}, // an enemy's, far away
	}
	r.tick(100 * time.Millisecond)
	r.tick(time.Second)
	if len(pf.walkList()) != 0 {
		t.Fatalf("walked %v", pf.walkList())
	}
}

func TestAGroundEffectWithTheCasterOutOfViewCountsByItsSpell(t *testing.T) {
	r, pf := newPosRig(t, Params{})
	pf.areas = []objects.DynamicObject{{GUID: 0xF100000000000001, Spell: spRainOfFire, Caster: 0xF130000000000099, Radius: 8, X: 0, Y: 0}}
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "leave_ground_aoe" {
		t.Fatalf("a harmful spell of an unseen caster: %+v", d)
	}
	// A spell that does nothing harmful, from an unseen caster, is let be.
	r2, pf2 := newPosRig(t, Params{})
	pf2.areas = []objects.DynamicObject{{GUID: 0xF100000000000001, Spell: spFrostArmor, Caster: 0xF130000000000099, Radius: 8, X: 0, Y: 0}}
	r2.tick(100 * time.Millisecond)
	if len(pf2.walkList()) != 0 {
		t.Fatal("left a harmless area")
	}
}

func TestAPeriodicDamageAuraOnTheLeaderMarksItsGroundEffectAsHarmful(t *testing.T) {
	r, pf := newPosRig(t, Params{})
	// The area's caster reads as a party member (a mis-attribution the aura corrects): the aura on the leader wins.
	r.g.addUnit(objects.Unit{GUID: tankG, Type: "player", Class: 1, InParty: true, Health: 900, MaxHealth: 900}, 30, 0)
	r.g.party = []Member{{Name: "Tankard", GUID: tankG, Online: true}}
	r.g.setSelf(func(u *objects.Unit) {
		u.Auras = append(u.Auras, objects.Aura{Slot: 33, SpellID: spRainOfFire, Harmful: true})
	})
	pf.areas = []objects.DynamicObject{{GUID: 0xF100000000000001, Spell: spRainOfFire, Caster: tankG, Radius: 6, X: 0, Y: 0}}
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "leave_ground_aoe" {
		t.Fatalf("%+v", d)
	}
}
