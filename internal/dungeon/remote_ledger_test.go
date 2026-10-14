package dungeon

import (
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The kill ledger (remote_ledger.go): a boss that dies in a fight the step does not track still counts as killed.

// A boss that comes into a pull group's fight (it is not an entry of that group) and dies there: the boss step does not fail with
// "no living [777] seen" (remote Uldaman run 1, Revelosh in a 4852 pull).
func TestRemoteBossKilledInAnotherGroupsFight(t *testing.T) {
	g := newFakeRemote(t)
	grp := g.addMob(299, Point{60, 0, 0})
	boss := g.addMob(777, Point{40, 0, 0})
	g.killRate = 90 // the boss is last seen nearly dead
	g.onTick = func(g *fakeRemote) {
		if grp.pulled && !boss.pulled {
			boss.pulled, boss.pullAt = true, g.now // the boss joins the pack's fight
		}
	}
	plan := remotePlan(Leg{Name: "b", To: Point{20, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{60, 0, 0}}}},
		Boss: &Boss{Entry: 777, Position: Point{40, 0, 0}}})
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
	g.killRate = 90 // the boss is last seen nearly dead
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
	frameWith(d, []tbm.PartyRec{{GUID: gTank}}, truncated, hs...)
}

func frameWith(d *RemoteDriver, party []tbm.PartyRec, truncated bool, hs ...tbm.HostileRec) {
	d.fr = tbm.ViewFrame{Party: party, Hostiles: hs, Truncated: truncated}
	d.noteLedger()
}

func TestLedgerNeedsTwoWholeFramesAndAFight(t *testing.T) {
	boss := tbm.HostileRec{GUID: 0x77, Entry: 777, HPPct: 15, Target: gTank, Flags: "-"}
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

// A live boss must not be counted dead for leaving the feed.
func TestLedgerDoesNotKillALiveBoss(t *testing.T) {
	pick := []uint32{777}
	boss := tbm.HostileRec{GUID: 0x77, Entry: 777, HPPct: 15, Target: gTank, Flags: "-", X: 50}
	near := []tbm.PartyRec{{GUID: gTank, X: 50}}
	far := []tbm.PartyRec{{GUID: gTank, X: 200}} // the bots were chased out of range

	// Chased out of view: the only bot is 150 yd from where the boss was last seen.
	d := ledgerDriver(t)
	frameWith(d, near, false, boss)
	for i := 0; i < 4; i++ {
		frameWith(d, far, false)
	}
	if d.ledgerDead(pick) {
		t.Fatal("a boss no bot was near was counted dead")
	}

	// A healthy boss that goes out of view with a bot beside it is not dead either.
	d = ledgerDriver(t)
	healthy := boss
	healthy.HPPct = 60
	frameWith(d, near, false, healthy)
	frameWith(d, near, false)
	frameWith(d, near, false)
	if d.ledgerDead(pick) {
		t.Fatal("a boss at 60% was counted dead for leaving the feed")
	}

	// A wipe: one absence before it and one after are not two in a row.
	d = ledgerDriver(t)
	frameWith(d, near, false, boss)
	frameWith(d, near, false)
	frameWith(d, []tbm.PartyRec{{GUID: gTank, X: 50, Dead: true}}, false)
	frameWith(d, near, false)
	if d.ledgerDead(pick) {
		t.Fatal("the absence counts survived a wipe")
	}
	d.ledgerClearAbsences() // what a recovered wipe does
	frameWith(d, near, false)
	if d.ledgerDead(pick) {
		t.Fatal("one absence after the clear")
	}

	// The same boss, a bot by it and two whole frames: dead.
	frameWith(d, near, false)
	if !d.ledgerDead(pick) {
		t.Fatal("nearly dead, a bot by, two frames: dead")
	}
}

// A pack the pulls passed over that walked up by itself and died in an untracked fight counts as killed (ZF-2); a mob that is not
// known dead, or one the bots refused, does not.
func TestPassedDeadInLedger(t *testing.T) {
	d := &RemoteDriver{ledger: map[packet.GUID]*ledgerMob{1: {dead: true}, 2: {dead: true}, 3: {}}}
	pass := func(gs ...packet.GUID) fightSpec {
		sp := fightSpec{passed: map[packet.GUID]bool{}}
		for _, g := range gs {
			sp.passed[g] = true
		}
		return sp
	}
	two := []passedOver{{text: "a"}, {text: "b"}}
	for _, c := range []struct {
		name string
		sp   fightSpec
		gave []passedOver
		want int
	}{
		{"both dead", pass(1, 2), two, 2},
		{"one alive in the ledger", pass(1, 3), two, 0},
		{"one never fought", pass(1, 9), two, 0},
		{"one refused", pass(1, 2), []passedOver{{text: "a"}, {text: "b", refused: true}}, 0},
		{"nothing passed", pass(), two, 0},
		{"passed and given up disagree", pass(1), two, 0},
	} {
		if got := d.passedDeadInLedger(c.sp, c.gave); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
}
