package dungeon

import (
	"strings"
	"testing"
	"time"
)

// walkRun walks the lead bot 400 yd (four legs of 120 yd) with a move that takes 2 s to take effect, looking every 0.5 s.
func walkRun(t *testing.T, o RemoteOptions) (*fakeRemote, Report) {
	t.Helper()
	g := newFakeRemote(t)
	g.speed, g.moveDelay = 6.6, 2*time.Second
	plan := remotePlan(Leg{Name: "a", To: Point{400, 0, 0}})
	o.CarefulHop = 1000 // the legs of MoveLeg, not the careful walk's hops (remote_gather_test.go)
	rep, err := runRemote(t, g, plan, o, func(c *Config) { c.Poll = 500 * time.Millisecond })
	if err != nil {
		t.Fatalf("%v\n%v", err, rep.Notes)
	}
	return g, rep
}

// The next waypoint's order goes out while the lead bot is still running, so it does not come to a stop between waypoints; with the
// look-ahead off it waits for each (the bot stands about a second at every one, with this much delay on an order).
func TestRemoteWalkLooksAhead(t *testing.T) {
	g, rep := walkRun(t, opts())
	if r := rep.Remote; r.WalkStops != 0 || r.WalkStoppedS != 0 || r.LookAheadMoves < 3 {
		t.Fatalf("%+v", r)
	}
	if n := len(g.ordersOf("cmd move")); n < 4 || n > 6 {
		t.Fatalf("%d moves: %v", n, g.orders)
	}
	// Orders at least a second apart (the fake clock moves only in ticks, so count them by the position they were sent from).
	for i := 1; i < len(g.moveOrderPos); i++ {
		if g.moveOrderPos[i].X-g.moveOrderPos[i-1].X < 6 {
			t.Fatalf("two moves less than a second apart: %v", g.moveOrderPos)
		}
	}
	// Each order after the first was sent from within the look-ahead of the point before it.
	for i := 1; i < len(g.moveOrderPos); i++ {
		if left := dist3(g.moveOrderPos[i], g.moveOrderDest[i-1]); left > 14 || left < 1 {
			t.Fatalf("order %d sent %.1f yd from the last waypoint: %v -> %v", i, left, g.moveOrderPos, g.moveOrderDest)
		}
	}

	o := opts()
	o.LookAhead = -1
	_, rep = walkRun(t, o)
	if r := rep.Remote; r.WalkStops == 0 || r.WalkStoppedS <= 0 || r.LookAheadMoves != 0 {
		t.Fatalf("without the look-ahead: %+v", r)
	}
}

// A refused look-ahead move is handled like any refused move: a shorter leg, and the walk goes on.
func TestRemoteWalkLookAheadRefusal(t *testing.T) {
	g := newFakeRemote(t)
	plan := remotePlan(Leg{Name: "a", To: Point{400, 0, 0}})
	n := 0
	g.onTick = func(g *fakeRemote) {
		if n == 0 && len(g.orders) > 3 && g.lead().X > 100 {
			n++
			g.moveErrs = []string{"unreachable"}
		}
	}
	o := opts()
	o.CarefulHop = 1000 // the legs of MoveLeg, not the careful walk's hops
	rep, err := runRemote(t, g, plan, o, nil)
	if err != nil || rep.Remote.OrdersRefused != 1 || g.lead().X < 399 {
		t.Fatalf("%v %+v at %v", err, rep.Remote, g.lead().X)
	}
}

// The gate at a leg's start does not wait for followers that lag behind: they catch up on the walk. The gate before a pull does.
func TestRemoteGatherOnlyBeforePulls(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{30, 0, 0})
	g.onTick = func(g *fakeRemote) { g.party[2].X = g.lead().X - 80 } // Magey is 80 yd behind
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{30, 0, 0}}}}})
	rep, err := runRemote(t, g, plan, opts(), func(c *Config) { c.RestTimeout = 10 * time.Second })
	if err != nil || len(rep.RestGates) < 2 {
		t.Fatalf("%v %+v", err, rep.RestGates)
	}
	first, second := rep.RestGates[0], rep.RestGates[1]
	if first.Result != "rested" || first.Seconds > 1 || len(first.Short) != 0 {
		t.Fatalf("the leg's first gate waited for the follower: %+v", first)
	}
	if second.Result != "gave_up" || len(second.Short) != 1 || !strings.Contains(second.Short[0], "Magey not near the lead bot") {
		t.Fatalf("the gate before the pull should gather: %+v", second)
	}
}

// After a fight the lead bot's stay spot is where it stands, so "return to stay position" does not walk it back to the last move point.
func TestRemoteHoldsWhereTheFightEnded(t *testing.T) {
	g := newFakeRemote(t)
	g.returnToStay, g.returnDelay = true, 6*time.Second // the module walks the bot back some seconds after the fight; the hold comes before
	m := g.addMob(299, Point{100, 0, 0})
	// The lead bot is carried 15 yd forward while it fights.
	g.onTick = func(g *fakeRemote) {
		if m.engaged && g.lead().X < 85 {
			g.lead().X += 5
		}
	}
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{100, 0, 0}}}}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil || rep.Remote.FightHolds < 1 {
		t.Fatalf("%v %+v", err, rep.Remote)
	}
	// Held where it ended: the stay spot is the lead bot's own position, so nothing walked it back.
	if x := g.lead().X; x < 80 {
		t.Fatalf("the lead bot walked back to %.0f", x)
	}
	last := g.moveOrderDest[len(g.moveOrderDest)-1]
	if dist3(last, Point{g.lead().X, 0, 0}) > 1 {
		t.Fatalf("the last move went to %v, the lead bot stands at %v", last, g.lead().X)
	}
}

// A run started inside a leg does not walk back to the leg's first point when resume is on: the walk to `to` is skipped. It is off
// by default.
func TestRemoteResume(t *testing.T) {
	plan := remotePlan(Leg{Name: "a", To: Point{100, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{150, 0, 0}}}}},
		Leg{Name: "b", To: Point{300, 0, 0}})
	start := func(resume bool) (*fakeRemote, Report) {
		g := newFakeRemote(t)
		for _, p := range g.party {
			p.X = 120
		}
		rep, err := runRemote(t, g, plan, opts(), func(c *Config) { c.Resume = resume })
		if err != nil {
			t.Fatalf("%v %v", err, rep.Notes)
		}
		return g, rep
	}
	g, rep := start(true)
	if rep.Resumed == nil || !rep.Resumed.SkippedWalk || rep.Resumed.SkippedGroups != 0 || !strings.Contains(strings.Join(rep.Notes, "|"), "resumed leg 0") {
		t.Fatalf("%+v %v", rep.Resumed, rep.Notes)
	}
	for _, d := range g.moveOrderDest {
		if d.X < 119 {
			t.Fatalf("the lead bot was sent back: %v", g.moveOrderDest)
		}
	}
	g, rep = start(false)
	if rep.Resumed != nil || len(g.moveOrderDest) == 0 || g.moveOrderDest[0].X != 100 {
		t.Fatalf("resume off (the default): %+v %v", rep.Resumed, g.moveOrderDest)
	}
}

// resumeWorld is a fakeWorld that also says whether the party stands near a point.
type resumeWorld struct {
	fakeWorld
	near func(Point) bool
}

func (w *resumeWorld) PartyNear(c Point, r float32) bool { return w.near(c) }

// The stepper's own rules: what is behind the leader is skipped, a pack that is behind but alive is not, a pack nobody has looked at
// (no party member near its positions, so an empty view proves nothing) is not, a leader before the leg's first point or far from
// its route resumes nothing.
func TestStepperResumeAt(t *testing.T) {
	plan := mustPlan(t, `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
	 {"to":{"x":100,"y":0,"z":0},"pull_groups":[
	   {"entries":[1],"positions":[{"x":150,"y":0,"z":0}]},
	   {"entries":[2],"positions":[{"x":200,"y":0,"z":0}]},
	   {"entries":[3],"positions":[{"x":250,"y":0,"z":0}]}]}]}`)
	// The party stands at x and sees the packs within 40 yd of it.
	at := func(x, y float32, hostiles int, sees bool) (Resumed, bool, *Stepper) {
		w := &resumeWorld{fakeWorld: fakeWorld{pos: Point{x, y, 0}, hostiles: hostiles}}
		w.near = func(c Point) bool { return sees && dist3(Point{x, y, 0}, c) <= ResumeSeeRadius }
		s := NewStepperAt(plan, w, nil, 0)
		r, ok := s.ResumeAt()
		return r, ok, s
	}
	if _, ok, _ := at(50, 0, 0, true); ok { // before `to`
		t.Fatal("resumed before the leg's first point")
	}
	if _, ok, _ := at(0, 500, 0, true); ok { // nowhere near
		t.Fatal("resumed far from the route")
	}
	r, ok, s := at(120, 0, 0, true) // between `to` and group 1
	if !ok || !r.SkippedWalk || r.SkippedGroups != 0 {
		t.Fatalf("%+v %v", r, ok)
	}
	if a := s.Next(); a.Kind != ActPull || a.Entries[0] != 1 {
		t.Fatalf("%+v", a)
	}
	// Past group 2 (at 200), standing at 210: group 2 is within 40 yd and empty, so it is skipped; group 1 (at 150) is 60 yd away,
	// nobody looked at it, so it is kept, and with it everything after it.
	r, ok, s = at(210, 0, 0, true)
	if !ok || r.SkippedGroups != 0 {
		t.Fatalf("%+v", r)
	}
	if a := s.Next(); a.Kind != ActPull || a.Entries[0] != 1 {
		t.Fatalf("%+v", a)
	}
	// Standing at 170 (past group 1 at 150, 20 yd from it, in sight): group 1 is empty and skipped; group 2 is ahead.
	r, ok, s = at(170, 0, 0, true)
	if !ok || r.SkippedGroups != 1 {
		t.Fatalf("%+v", r)
	}
	if a := s.Next(); a.Kind != ActPull || a.Entries[0] != 2 {
		t.Fatalf("%+v", a)
	}
	// The same spot with hostiles listed at the groups: the pack behind is alive and is pulled.
	r, ok, s = at(170, 0, 2, true)
	if !ok || r.SkippedGroups != 0 {
		t.Fatalf("%+v", r)
	}
	// A pack the feed cannot see: nobody stands near it (the party view reaches nothing), the list is empty, and it is not skipped.
	r, ok, s = at(170, 0, 0, false)
	if !ok || r.SkippedGroups != 0 {
		t.Fatalf("a pack nobody looked at was skipped: %+v", r)
	}
	if a := s.Next(); a.Kind != ActPull || a.Entries[0] != 1 {
		t.Fatalf("%+v", a)
	}
	// A World that cannot tell where the party is (no PartyWorld) skips no group either.
	w := &fakeWorld{pos: Point{170, 0, 0}}
	s = NewStepperAt(plan, w, nil, 0)
	if r, ok := s.ResumeAt(); !ok || r.SkippedGroups != 0 {
		t.Fatalf("%+v %v", r, ok)
	}
}

// The "stay here" move after a fight waits for the loot wait and for the adds: a move sent earlier would reset the lead bot's
// movement while loose adds hit the party. Here an add joins as the first mob dies; the hold comes after the add is dead, and not
// before PostFight has passed.
func TestRemoteHoldWaitsForTheAdds(t *testing.T) {
	g := newFakeRemote(t)
	g.killRate = 50
	g.addMob(299, Point{30, 0, 0})
	spawned := false
	g.onTick = func(g *fakeRemote) {
		if g.mobsKilled == 1 && !spawned {
			spawned = true
			m := g.addMob(300, Point{20, 0, 0})
			m.pulled, m.pullAt, m.engaged, m.target = true, g.now, true, gTank
		}
	}
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{30, 0, 0}}}}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil || len(g.killTimes) != 2 || rep.Remote.FightHolds != 1 {
		t.Fatalf("%v kills %v %+v", err, g.killTimes, rep.Remote)
	}
	hold := g.moveOrderAt[len(g.moveOrderAt)-1]
	if hold.Before(g.killTimes[1]) {
		t.Fatalf("the hold went out at %v, before the add died at %v", hold, g.killTimes[1])
	}
	if hold.Sub(g.killTimes[0]) < 2*time.Second {
		t.Fatalf("the hold went out %v after the first kill, inside the loot wait", hold.Sub(g.killTimes[0]))
	}
	// No hold while something still hits the party: the adds never die here, so the wait ends at AddsWait and no move goes out.
	g = newFakeRemote(t)
	g.addMob(299, Point{30, 0, 0})
	spawned = false
	g.onTick = func(g *fakeRemote) {
		if g.mobsKilled == 1 && !spawned {
			spawned = true
			m := g.addMob(300, Point{20, 0, 0})
			m.pulled, m.pullAt, m.engaged, m.target, m.hp = true, g.now, true, gTank, 1e9
		}
	}
	rep, err = runRemote(t, g, plan, opts(), func(c *Config) { c.FightTimeout = 30 * time.Second })
	if rep.Remote != nil && rep.Remote.FightHolds != 0 {
		t.Fatalf("a hold went out with an add still fighting: %v %+v", err, rep.Remote)
	}
}

func TestPlanLegStart(t *testing.T) {
	plan := mustPlan(t, `{"version":1,"name":"t","map":70,"entrance":{"x":1,"y":2,"z":3},"legs":[
	 {"to":{"x":10,"y":0,"z":0},"boss":{"entry":9,"position":{"x":40,"y":0,"z":0}}},
	 {"to":{"x":50,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":60,"y":0,"z":0}]}]}]}`)
	if p := legStart(plan, 0); p != (Point{1, 2, 3}) {
		t.Fatal(p)
	}
	if p := legStart(plan, 1); p != (Point{40, 0, 0}) {
		t.Fatal(p)
	}
}
