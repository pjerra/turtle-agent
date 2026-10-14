package dungeon

import (
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The kill ledger (remote_ledger.go): a boss that dies in a fight the step does not track still counts as killed.

// A boss that comes into a pull group's fight (it is not an entry of that group) and dies there: the boss step does not fail with
// "no living [777] seen" (remote Uldaman run 1, Revelosh in a 4852 pull).
func TestRemoteBossKilledInAnotherGroupsFight(t *testing.T) {
	g := newFakeRemote(t)
	grp := g.addMob(299, Point{60, 0, 0})
	boss := g.addMob(777, Point{80, 0, 0})
	g.onTick = func(g *fakeRemote) {
		if grp.pulled && !boss.pulled {
			boss.pulled, boss.pullAt = true, g.now // the boss joins the pack's fight
		}
	}
	plan := remotePlan(Leg{Name: "b", To: Point{20, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{60, 0, 0}}}},
		Boss: &Boss{Entry: 777, Position: Point{80, 0, 0}}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil || g.mobsKilled != 2 || len(g.ordersOf("cmd pullback")) != 1 {
		t.Fatalf("%v killed %d orders %v\n%s", err, g.mobsKilled, g.orders, notesOf(rep))
	}
	if !strings.Contains(notesOf(rep), "[777] is dead") {
		t.Fatalf("%s", notesOf(rep))
	}
	_ = boss
}

// A boss that dies in a fight on the way (walk aggro, which tracks nothing): the ledger saw it fight and go.
func TestRemoteBossKilledInAWalkFight(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(777, Point{130, 0, 0})
	inner := g.onTick
	g.onTick = func(g *fakeRemote) {
		if g.lead().X > 90 {
			for _, m := range g.mobs {
				if !m.engaged {
					m.pulled, m.pullAt, m.engaged = true, g.now, true
				}
			}
		}
		if inner != nil {
			inner(g)
		}
	}
	plan := remotePlan(Leg{Name: "b", To: Point{300, 0, 0}, Boss: &Boss{Entry: 777, Position: Point{250, 0, 0}}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil || g.mobsKilled != 1 || rep.Remote.WalkAggros != 1 {
		t.Fatalf("%v killed %d %+v\n%s", err, g.mobsKilled, rep.Remote, notesOf(rep))
	}
	if !strings.Contains(notesOf(rep), "[777] is dead (killed in this run, in another fight") {
		t.Fatalf("%s", notesOf(rep))
	}
}

// A boss that is never fought and then leaves the view is not counted dead.
func TestRemoteBossThatLeavesViewUnfoughtIsNotDead(t *testing.T) {
	g := newFakeRemote(t)
	g.sight = 45
	boss := g.addMob(777, Point{50, 0, 0})
	boss.inert = true
	g.onTick = func(g *fakeRemote) {
		if g.lead().X > 10 {
			boss.pos = Point{50, 300, 0} // out of sight, alive
		}
	}
	plan := remotePlan(Leg{Name: "b", To: Point{20, 0, 0}, Boss: &Boss{Entry: 777, Position: Point{50, 0, 0}}})
	_, err := runRemote(t, g, plan, opts(), nil)
	if err == nil || !strings.Contains(err.Error(), "777") {
		t.Fatalf("a boss that was never fought was counted dead: %v", err)
	}
}

func ledgerDriver(t *testing.T) *RemoteDriver {
	t.Helper()
	g := newFakeRemote(t)
	return NewRemoteDriver(remotePlan(Leg{Name: "a", To: Point{1, 0, 0}}), g, DefaultConfig(), opts(), g.clock())
}

// frameOf puts a frame in the driver as look() does.
func frameOf(d *RemoteDriver, truncated bool, hs ...tbm.HostileRec) {
	d.fr = tbm.ViewFrame{Party: []tbm.PartyRec{{GUID: gTank}}, Hostiles: hs, Truncated: truncated}
	d.noteLedger()
}

func TestLedgerNeedsTwoWholeFramesAndAFight(t *testing.T) {
	boss := tbm.HostileRec{GUID: 0x77, Entry: 777, HPPct: 40, Target: gTank, Flags: "-"}
	pick := []uint32{777}

	d := ledgerDriver(t)
	frameOf(d, false, boss)
	frameOf(d, false)
	if d.ledgerDead(pick) {
		t.Fatal("one frame without it is not death")
	}
	frameOf(d, true) // cut short: says nothing
	if d.ledgerDead(pick) {
		t.Fatal("a truncated frame counted")
	}
	frameOf(d, false)
	if !d.ledgerDead(pick) {
		t.Fatal("fought, then absent from two whole frames: dead")
	}
	frameOf(d, false, boss) // seen alive again
	if d.ledgerDead(pick) {
		t.Fatal("a mob seen alive again is alive")
	}

	d = ledgerDriver(t)
	idle := boss
	idle.HPPct, idle.Target = 100, 0
	frameOf(d, false, idle)
	frameOf(d, false)
	frameOf(d, false)
	if d.ledgerDead(pick) {
		t.Fatal("a boss that was never fought and left view is not dead")
	}

	d = ledgerDriver(t)
	home := boss
	home.Flags = "e"
	frameOf(d, false, home)
	frameOf(d, false)
	frameOf(d, false)
	if d.ledgerDead(pick) {
		t.Fatal("a boss walking home is not dead")
	}

	d = ledgerDriver(t)
	dead := boss
	dead.HPPct = 0
	frameOf(d, false, boss)
	frameOf(d, false, dead)
	if !d.ledgerDead(pick) || d.ledgerDead([]uint32{778}) {
		t.Fatal("listed dead after the fight: dead, and only for its entry")
	}
}
