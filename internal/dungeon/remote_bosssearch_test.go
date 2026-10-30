package dungeon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/tbm"
)

// A boss that patrols is not at his spot when the step looks (Theka, Velratha: "no living [7795] seen"): the step waits up to 60 s for him
// to walk back into range, then fails with "boss <entry> not found". He is never counted dead for being absent.

func bossSearchPlan() *Plan {
	return remotePlan(Leg{Name: "b", To: Point{20, 0, 0}, Boss: &Boss{Entry: 777, Position: Point{60, 0, 0}}})
}

func TestRemoteBossWalksBackWithinTheWaitAndIsKilled(t *testing.T) {
	g := newFakeRemote(t)
	began := g.now
	added := false
	g.onTick = func(g *fakeRemote) { // out of the bots' sight on his patrol until 40 s in, then back at his spot
		if !added && g.now.Sub(began) > 40*time.Second {
			added = true
			g.addMob(777, Point{60, 0, 0})
		}
	}
	rep, err := runRemote(t, g, bossSearchPlan(), opts(), nil)
	if err != nil || g.mobsKilled != 1 {
		t.Fatalf("%v killed %d\n%s", err, g.mobsKilled, notesOf(rep))
	}
	if n := notesOf(rep); !strings.Contains(n, "boss 777 is not at his spot; waiting up to 60 s") || !strings.Contains(n, "boss 777 seen at") {
		t.Fatalf("%s", n)
	}
	if rep.Remote.BossSearches != 1 {
		t.Fatalf("%+v", rep.Remote)
	}
}

func TestRemoteBossThatNeverComesBackFailsAfterTheWait(t *testing.T) {
	g := newFakeRemote(t) // the boss is on no frame
	began := g.now
	rep, err := runRemote(t, g, bossSearchPlan(), opts(), nil)
	if err == nil || !strings.Contains(err.Error(), "boss 777 not found") {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if waited := g.now.Sub(began); waited < 60*time.Second || waited > 4*time.Minute {
		t.Fatalf("waited %v", waited)
	}
	if strings.Contains(notesOf(rep), "is dead") || g.mobsKilled != 0 {
		t.Fatalf("an absent boss was counted dead: %s", notesOf(rep))
	}
}

// Absence alone is no kill: a boss that was never fought and is gone from the feed is "not found", not "dead".
func TestRemoteAbsentBossIsNeverCountedDead(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(777, Point{60, 0, 0})
	g.mobs[0].inert = true
	g.onTick = func(g *fakeRemote) { g.mobs = nil } // gone from the feed with no fight, no corpse
	rep, err := runRemote(t, g, bossSearchPlan(), opts(), nil)
	if err == nil || !strings.Contains(err.Error(), "boss 777 not found") || strings.Contains(notesOf(rep), "is dead") {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
}

// The patrol takes the boss away from his spot: the search takes him when he is within 120 yd of it and the way to him passes no pull group
// of a later leg; the unit is approached and pulled by its GUID. Farther, or behind an uncleared pack, the wait goes on and the step fails.
func TestRemoteBossSeenAwayFromHisSpotIsApproachedAndPulled(t *testing.T) {
	for _, at := range []Point{{60, 50, 0}, {60, 100, 0}, {60, 119, 0}} {
		g := newFakeRemote(t)
		boss := g.addMob(777, at)
		rep, err := runRemote(t, g, bossSearchPlan(), opts(), nil)
		if err != nil || g.mobsKilled != 1 {
			t.Fatalf("boss at %v: %v killed %d\n%s", at, err, g.mobsKilled, notesOf(rep))
		}
		if got := g.ordersOf("cmd pullback " + tbm.GUIDArg(boss.guid)); len(got) != 1 {
			t.Fatalf("boss at %v: pullbacks %v", at, g.orders)
		}
	}
}

func TestRemoteBossSeenTooFarFromHisSpotIsNotTaken(t *testing.T) {
	for _, at := range []Point{{60, 130, 0}, {400, 0, 0}} {
		g := newFakeRemote(t)
		g.addMob(777, at)
		rep, err := runRemote(t, g, bossSearchPlan(), opts(), nil)
		if err == nil || !strings.Contains(err.Error(), "boss 777 not found") || g.mobsKilled != 0 || len(g.ordersOf("cmd pullback")) != 0 {
			t.Fatalf("boss at %v: %v killed %d\n%s", at, err, g.mobsKilled, notesOf(rep))
		}
		if !strings.Contains(notesOf(rep), "but not taken") || !strings.Contains(notesOf(rep), "more than 120") {
			t.Fatalf("boss at %v: %s", at, notesOf(rep))
		}
	}
}

// 100 yd from his spot, with a pack of the next leg on the way to him: not taken. The same boss with the pack off the way is.
func TestRemoteBossBehindAnUnclearedLaterPackIsNotTaken(t *testing.T) {
	plan := func(pack Point) *Plan {
		p := bossSearchPlan()
		p.Legs = append(p.Legs, Leg{Name: "next", To: Point{500, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{300}, Positions: []Point{pack}}}})
		return p
	}
	g := newFakeRemote(t)
	g.addMob(777, Point{60, 100, 0})
	rep, err := runRemote(t, g, plan(Point{49, 50, 0}), opts(), nil)
	if err == nil || !strings.Contains(err.Error(), "boss 777 not found") || len(g.ordersOf("cmd pullback")) != 0 {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if !strings.Contains(notesOf(rep), "the way passes pull group 1 of 1 (from_pull_group 0) of leg 1 (next), not cleared") {
		t.Fatalf("%s", notesOf(rep))
	}
	g = newFakeRemote(t)
	boss := g.addMob(777, Point{60, 100, 0})
	rep, err = runRemote(t, g, plan(Point{-200, 50, 0}), opts(), nil)
	if err != nil || len(g.ordersOf("cmd pullback "+tbm.GUIDArg(boss.guid))) != 1 {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
}

// plannerFake plans the path a test gives: a partial path stops short of its goal, an error is no path.
type plannerFake struct {
	*fakeRemote
	path func(from, to Point) ([]Point, error)
}

func (g plannerFake) PlanPath(_ context.Context, _ uint32, from, to Point) ([]Point, error) {
	if to.Y < 90 {
		return []Point{from, to}, nil // everything but the way to the boss (60 100 0) is a plain complete path
	}
	return g.path(from, to)
}

// The way to the boss must be a complete navmesh path: one that stops short of him, or none, keeps the wait going.
func TestRemoteBossSearchNeedsACompleteNavmeshPath(t *testing.T) {
	for name, path := range map[string]func(from, to Point) ([]Point, error){
		"partial": func(from, to Point) ([]Point, error) { return []Point{from, lerp(from, to, 0.5)}, nil },
		"none":    func(from, to Point) ([]Point, error) { return nil, errors.New("no path") },
	} {
		g := newFakeRemote(t)
		g.addMob(777, Point{60, 100, 0})
		rep, err := runRemote(t, plannerFake{g, path}, bossSearchPlan(), opts(), nil)
		if err == nil || !strings.Contains(err.Error(), "boss 777 not found") || !strings.Contains(notesOf(rep), "no complete navmesh path") {
			t.Fatalf("%s: %v\n%s", name, err, notesOf(rep))
		}
	}
	g := newFakeRemote(t)
	g.addMob(777, Point{60, 100, 0})
	whole := func(from, to Point) ([]Point, error) { return []Point{from, to}, nil }
	if _, err := runRemote(t, plannerFake{g, whole}, bossSearchPlan(), opts(), nil); err != nil || g.mobsKilled != 1 {
		t.Fatalf("a complete path: %v killed %d", err, g.mobsKilled)
	}
}

// boss_search_s 0 turns the search off: the step fails at once, as it did before the search existed.
func TestRemoteBossSearchCanBeTurnedOff(t *testing.T) {
	spec := tuneIndex["boss_search_s"]
	var cfg Config
	spec.set(&tuneTarget{cfg: &cfg}, 0)
	if cfg.BossSearch >= 0 || spec.get(&tuneTarget{cfg: &cfg}) != 0 {
		t.Fatalf("0 must switch it off and read back as 0: %v", cfg.BossSearch)
	}
	spec.set(&tuneTarget{cfg: &cfg}, 45)
	if cfg.BossSearch != 45*time.Second {
		t.Fatalf("%v", cfg.BossSearch)
	}
	g := newFakeRemote(t)
	began := g.now
	rep, err := runRemoteTx(t, g, bossSearchPlan(), opts(), func(d *RemoteDriver) { d.cfg.BossSearch = -time.Millisecond })
	if err == nil || !strings.Contains(err.Error(), "boss 777 not found") || rep.Remote.BossSearches != 0 || g.now.Sub(began) > 50*time.Second {
		t.Fatalf("%v searches %d waited %v", err, rep.Remote.BossSearches, g.now.Sub(began))
	}
}

// A run that began inside the boss's leg from a later start does not search: he may have been killed before this run.
func TestRemoteBossSearchSkippedAfterAMidPlanStart(t *testing.T) {
	plan := remotePlan(Leg{Name: "first", To: Point{10, 0, 0}, Boss: &Boss{Entry: 778, Position: Point{30, 0, 0}}}, Leg{Name: "b", To: Point{20, 0, 0}, Boss: &Boss{Entry: 777, Position: Point{60, 0, 0}}})
	g := newFakeRemote(t)
	g.lead().X = 30 // where leg 0 ends, the start of leg 1
	began := g.now
	d := NewRemoteDriver(plan, g, DefaultConfig(), opts(), g.clock())
	err := d.Run(context.Background(), 1) // from_leg 1: the boss of leg 1 may have died in the run before
	rep, _ := d.Status()
	if err == nil || !strings.Contains(err.Error(), "boss 777 not found") || rep.Remote.BossSearches != 0 || g.now.Sub(began) > 50*time.Second {
		t.Fatalf("%v searches %d waited %v\n%s", err, rep.Remote.BossSearches, g.now.Sub(began), notesOf(rep))
	}
}

// A group of the current leg that the feed still shows alive blocks the way too; a cleared one does not.
func TestBossWayChecksTheCurrentLegsPacks(t *testing.T) {
	plan := remotePlan(Leg{Name: "b", To: Point{20, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{300}, Positions: []Point{{49, 50, 0}}}}, Boss: &Boss{Entry: 777, Position: Point{60, 0, 0}}})
	g := newFakeRemote(t)
	d := NewRemoteDriver(plan, g, DefaultConfig(), opts(), g.clock())
	d.curLeg = 0
	boss := tbm.HostileRec{GUID: 0x501, Entry: 777, X: 60, Y: 100, HPPct: 100, Flags: "-"}
	pack := tbm.HostileRec{GUID: 0x502, Entry: 300, X: 50, Y: 52, HPPct: 100, Flags: "-"}
	sp := fightSpec{pick: []uint32{777}, anchors: []Point{{60, 0, 0}}, wide: true, required: true}
	look := func(hs ...tbm.HostileRec) string {
		d.fr = tbm.ViewFrame{Lead: gTank, Party: []tbm.PartyRec{{GUID: gTank, Name: "Tanky", X: 39}}, Hostiles: hs}
		d.haveFr = true
		_, why := d.bossWay(context.Background(), sp, boss, 100)
		return why
	}
	if why := look(boss, pack); !strings.Contains(why, "pull group 1 of 1 (from_pull_group 0) of leg 0") {
		t.Fatalf("a living member of this leg's pack is on the way: %q", why)
	}
	if why := look(boss); why != "" {
		t.Fatalf("a cleared group does not block: %q", why)
	}
}

// The way is checked again before every hop: a path that comes to pass an uncleared pack stops the walk and fails the step.
func TestRemoteBossWalkStopsWhenTheWayChanges(t *testing.T) {
	plan := bossSearchPlan()
	plan.Legs = append(plan.Legs, Leg{Name: "next", To: Point{500, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{300}, Positions: []Point{{200, 200, 0}}}}})
	g := newFakeRemote(t)
	g.addMob(777, Point{60, 100, 0})
	calls := 0
	path := func(from, to Point) ([]Point, error) {
		calls++
		if calls >= 2 {
			return []Point{from, {200, 200, 0}, to}, nil // re-planned through the pack of the next leg
		}
		return []Point{from, to}, nil
	}
	rep, err := runRemote(t, plannerFake{g, path}, plan, opts(), nil)
	if err == nil || !strings.Contains(err.Error(), "boss 777 not found") || !strings.Contains(err.Error(), "blocked on the walk") || len(g.ordersOf("cmd pullback")) != 0 {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
}

// walkToBoss succeeds only with the lead bot within PullRange of the boss: every other way out is a failure with a reason, and no pull
// follows (the step fails "blocked on the walk").
func TestRemoteBossWalkFailsWithoutALeadPosition(t *testing.T) {
	g := newFakeRemote(t)
	d := NewRemoteDriver(bossSearchPlan(), g, DefaultConfig(), opts(), g.clock())
	boss := tbm.HostileRec{GUID: 0x601, Entry: 777, X: 60, Y: 100, HPPct: 100, Flags: "-"}
	d.fr = tbm.ViewFrame{Hostiles: []tbm.HostileRec{boss}} // no party, no lead
	d.haveFr = true
	why, err := d.walkToBoss(context.Background(), "boss", fightSpec{pick: []uint32{777}, anchors: []Point{{60, 0, 0}}}, boss.GUID)
	if err != nil || !strings.Contains(why, "place is not known") || len(g.ordersOf("cmd pullback")) != 0 {
		t.Fatalf("why %q err %v orders %v", why, err, g.orders)
	}
}

func TestRemoteBossWalkFailsWithoutAStandPoint(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(777, Point{60, 100, 0})
	// A "complete" path that ends at the boss but is a yard long: it holds no stand point 38 yd short of him.
	short := func(from, to Point) ([]Point, error) { return []Point{{to.X, to.Y - 1, to.Z}, to}, nil }
	rep, err := runRemote(t, plannerFake{g, short}, bossSearchPlan(), opts(), nil)
	if err == nil || !strings.Contains(err.Error(), "blocked on the walk") || !strings.Contains(err.Error(), "no stand point") || len(g.ordersOf("cmd pullback")) != 0 {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
}

func TestRemoteBossWalkFailsWhenTheHopsRunOut(t *testing.T) {
	defer func(n int) { bossWalkHops = n }(bossWalkHops)
	bossWalkHops = 1
	g := newFakeRemote(t)
	g.addMob(777, Point{60, 119, 0})
	rep, err := runRemoteTx(t, g, bossSearchPlan(), opts(), func(d *RemoteDriver) { d.opt.MoveLeg = 10 }) // one hop of 10 yd leaves him far off
	if err == nil || !strings.Contains(err.Error(), "blocked on the walk") || !strings.Contains(err.Error(), "after 1 hops") || len(g.ordersOf("cmd pullback")) != 0 {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
}
