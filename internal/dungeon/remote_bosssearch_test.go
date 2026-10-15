package dungeon

import (
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

// The patrol takes the boss far from his spot (50 yd and more, beyond the anchor and the 80 yd of the lead bot): the search sees him in
// the feed without the anchor filter, and the step approaches and pulls that exact unit.
func TestRemoteBossSeenFarFromHisSpotIsApproachedAndPulled(t *testing.T) {
	for _, at := range []Point{{60, 50, 0}, {60, 130, 0}, {400, 0, 0}} {
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
