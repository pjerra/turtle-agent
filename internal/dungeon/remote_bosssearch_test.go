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
	if !strings.Contains(notesOf(rep), "the way passes pull group 0 of leg 1 (next), not cleared") {
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
