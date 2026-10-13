package brain

import (
	"context"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// Follow-ups of the step 3b review (#80).

// ---- A: a refusal for line of sight is remembered as a wall ----

func TestServerLineOfSightRefusalMakesTheMageMoveNotCastIntoTheWall(t *testing.T) {
	r, pf := newPosRig(t, Params{})
	tankFights(r.g, 22, 24, 0) // in the band, the mesh sees nothing in the way
	r.tick(100 * time.Millisecond)
	if d := r.tick(2200 * time.Millisecond); d == nil || d.Action != "frostbolt" {
		t.Fatalf("%+v", d)
	}
	r.g.st.CastRefused(spFrostbolt, codeOf(t, "line_of_sight")) // a wall the mesh does not know
	r.g.st.Clear(spFrostbolt)
	d := r.tick(400 * time.Millisecond) // past the cast's settle
	if d == nil || d.Action != "reposition" || d.Trigger != "bad_position" {
		t.Fatalf("the mage did not move: %+v idle %q", d, r.idle())
	}
	w := pf.walkList()
	if len(w) != 1 || dist2(w[0], Vec3{0, 0, 0}) < losBlockMoved {
		t.Fatalf("walked to %v, which is no other place", w)
	}
	// From the new place the mesh answer counts again: the mage casts once the 3 s hold is over.
	var cast *Decision
	for i := 0; i < 60 && cast == nil; i++ {
		if d := r.tick(100 * time.Millisecond); d != nil && d.Action == "frostbolt" {
			cast = d
		}
	}
	if cast == nil {
		t.Fatalf("never cast again, idle %q", r.idle())
	}
}

func TestLineOfSightBlockHoldsForFiveSecondsWhereTheRefusalHappenedAndIsExtended(t *testing.T) {
	r, _ := newPosRig(t, Params{})
	tankFights(r.g, 22, 24, 0)
	at := func(d time.Duration) *Ctx {
		r.g.clk.advance(d)
		return r.b.newCtx(r.g.clk.Now())
	}
	c := at(0)
	m, _ := c.Unit(mobA)
	markLOSBlocked(c, m)
	for _, d := range []time.Duration{0, time.Second, 3 * time.Second} { // 0, 1 s, 4 s
		c = at(d)
		m, _ = c.Unit(mobA)
		if clear, known := c.LineOfSight(m); clear || !known {
			t.Fatalf("after %v: clear %v known %v, want known blocked", d, clear, known)
		}
	}
	// A second refusal at 4 s runs the block on to 9 s.
	markLOSBlocked(c, m)
	c = at(3 * time.Second) // 7 s
	m, _ = c.Unit(mobA)
	if clear, known := c.LineOfSight(m); clear || !known {
		t.Fatal("the repeated refusal did not extend the block")
	}
	c = at(2100 * time.Millisecond) // 9.1 s
	m, _ = c.Unit(mobA)
	if clear, known := c.LineOfSight(m); !clear || !known {
		t.Fatalf("block outlived its time: clear %v known %v", clear, known)
	}
	// Walking away ends it at once.
	markLOSBlocked(c, m)
	r.g.setSelf(func(u *objects.Unit) { u.X += 5 })
	c = at(10 * time.Millisecond)
	m, _ = c.Unit(mobA)
	if clear, _ := c.LineOfSight(m); !clear {
		t.Fatal("the block followed the player 5 yd away")
	}
}

// ---- B: a walk does not keep the brain from acting ----

// runToTankMage is a mage with a mob on its back 15 yd away and the tank 30 yd off: it runs to the tank, the walker held open.
func runToTankMage(t *testing.T) (*rig, *posFake) {
	r, pf := newPosRig(t, Params{})
	pf.hold = make(chan struct{})
	r.g.party = []Member{{Name: "Tankard", GUID: tankG, Online: true}}
	r.g.addUnit(objects.Unit{GUID: tankG, Type: "player", Name: "Tankard", Class: 1, InParty: true, Health: 900, MaxHealth: 900}, 30, 6)
	r.g.addUnit(mob(mobA, selfG), 15, 0)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "run_to_tank" || d.Result != ResultOK {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if !r.b.lock.stepping() {
		t.Fatal("no step under way")
	}
	return r, pf
}

func TestAWalkDoesNotBlockTheTickAndHoldsBackOrdinaryActions(t *testing.T) {
	r, pf := runToTankMage(t)
	// The tick came back with the walk under way. While it lasts the mage does not cast on the move or start a second walk.
	for i := 0; i < 5; i++ {
		if d := r.tick(200 * time.Millisecond); d != nil {
			t.Fatalf("acted during the walk: %+v", d)
		}
	}
	if len(r.g.castList()) != 0 {
		t.Fatalf("cast on the move: %v", r.g.castList())
	}
	close(pf.hold) // the walker arrives
	r.settleStep(time.Second)
	if r.b.lock.stepping() {
		t.Fatal("the step did not end")
	}
	if len(pf.walkList()) != 1 {
		t.Fatalf("walks %v", pf.walkList())
	}
}

func TestEmergencyDuringAWalkCancelsItAndActsInTheSameTick(t *testing.T) {
	r, pf := runToTankMage(t)
	r.g.setSelf(func(u *objects.Unit) { u.Health = 120 }) // 12%: Ice Block (RelDispel)
	d := r.tick(200 * time.Millisecond)
	if d == nil || d.Action != "ice_block" || d.Result != ResultOK {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if r.b.lock.stepping() {
		t.Fatal("the walk went on under the Ice Block")
	}
	if c := r.g.lastCast(t); c.spell != spIceBlock {
		t.Fatalf("%+v", c)
	}
	if len(pf.walkList()) != 0 {
		t.Fatalf("the cancelled walk arrived: %v", pf.walkList())
	}
	for _, x := range r.recent() {
		if x.Result == ResultError {
			t.Fatalf("a cancelled walk is no failure: %+v", x)
		}
	}
}

func TestInterruptDuringAWalkCancelsIt(t *testing.T) {
	r, _ := runToTankMage(t)
	cast := objects.Cast{SpellID: spLesserHeal, Target: packet.GUID(100), EndsInMS: ms(2000)}
	r.g.mutate(mobA, func(u *objects.Unit) { u.Casting = &cast })
	d := r.tick(200 * time.Millisecond)
	if d == nil || d.Action != "interrupt" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if r.b.lock.stepping() {
		t.Fatal("the walk went on")
	}
}

func TestWalkIsASecondGuardAgainstNoMoveAndTheDriversHold(t *testing.T) {
	r, pf := newPosRig(t, Params{NoMove: true})
	c := r.b.newCtx(r.g.clk.Now())
	c.run = context.Background()
	if err := c.walk(context.Background(), Vec3{1, 2, 3}); err != errNoMove {
		t.Fatalf("NoMove: %v", err)
	}
	r2, pf2 := newPosRig(t, Params{})
	release, _ := r2.b.lock.Hold(context.Background())
	defer release()
	c = r2.b.newCtx(r2.g.clk.Now())
	if err := c.walk(context.Background(), Vec3{1, 2, 3}); err != errMoveLocked {
		t.Fatalf("held: %v", err)
	}
	if r2.b.lock.stepping() || len(pf.walkList())+len(pf2.walkList()) != 0 {
		t.Fatal("walked")
	}
}

// ---- C: the interrupt memory is pruned ----

func TestInterruptedMemoryForgetsOldCasts(t *testing.T) {
	r, _ := newPosRig(t, Params{})
	r.g.party = []Member{{Name: "Tankard", GUID: tankG, Online: true}}
	r.g.addUnit(objects.Unit{GUID: tankG, Type: "player", Name: "Tankard", Class: 1, InParty: true, Health: 900, MaxHealth: 900, Target: mobA}, 22, 0)
	r.g.addUnit(mob(mobA, tankG), 24, 0)
	cast := objects.Cast{SpellID: spLesserHeal, Target: mobA, EndsInMS: ms(2000)}
	r.g.mutate(mobA, func(u *objects.Unit) { u.Casting = &cast })
	e := &r.b.mem.ext
	e.mu.Lock()
	e.interrupted = map[interruptKey]time.Time{{caster: 999, spell: 1}: r.g.clk.Now().Add(-time.Minute)}
	e.mu.Unlock()
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "interrupt" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, old := e.interrupted[interruptKey{999, 1}]; old || len(e.interrupted) != 1 {
		t.Fatalf("interrupted memory %v", e.interrupted)
	}
}
