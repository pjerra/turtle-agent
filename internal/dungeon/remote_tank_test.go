package dungeon

import (
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/tbm"
)

// Remote mode, tank first. The walk aggro path is the fight here: the mobs engage the party when the lead bot passes x 90, and the
// party is sent at them with `cmd attack`. Mob A (entry 299) stands nearer to the lead bot than mob B (entry 300).

func aggroFight(t *testing.T, mod func(g *fakeRemote, a, b *rmob), o RemoteOptions) (*fakeRemote, *rmob, *rmob, Report) {
	t.Helper()
	g := newFakeRemote(t)
	a := g.addMob(299, Point{130, 0, 0})
	b := g.addMob(300, Point{130, 40, 0})
	mod(g, a, b)
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
	o.Tank = "Tanky"
	plan := remotePlan(Leg{Name: "a", To: Point{300, 0, 0}})
	rep, err := runRemote(t, g, plan, o, nil)
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	return g, a, b, rep
}

// attackGUIDs are the targets of the `cmd attack` orders, in order, without repeats of the same mob in a row.
func attackGUIDs(g *fakeRemote) []string {
	var out []string
	for _, o := range g.ordersOf("cmd attack") {
		f := strings.Fields(o)
		if len(f) > 3 {
			continue // bot-scoped: the tank alone at a loose add
		}
		if len(out) == 0 || out[len(out)-1] != f[2] {
			out = append(out, f[2])
		}
	}
	return out
}

func TestRemotePartyTakesTheMobTheTankHolds(t *testing.T) {
	g, a, b, rep := aggroFight(t, func(g *fakeRemote, a, b *rmob) {
		a.hold, a.tankPct = gMage, 20 // the nearer mob is on the mage, the tank has 20 % of its top
		g.ignorePull = true
	}, opts())
	got := attackGUIDs(g)
	if len(got) < 2 || got[0] != tbm.GUIDArg(b.guid) || got[1] != tbm.GUIDArg(a.guid) {
		t.Fatalf("attack orders %v (a %s b %s)", got, tbm.GUIDArg(a.guid), tbm.GUIDArg(b.guid))
	}
	r := rep.Remote
	// Shares were sampled while both fought (A 20, B 100: mean 60), then A alone.
	if r.TankThreatSamples < 2 || r.TankThreatShare <= 0 || r.TankThreatShare >= 100 {
		t.Fatalf("%+v", r)
	}
	if r.TargetSwitches != 0 || r.CCRespected != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestRemoteTankIsSentAtALooseAdd(t *testing.T) {
	var engagedAt time.Time
	g, a, _, rep := aggroFight(t, func(g *fakeRemote, a, b *rmob) {
		a.hold = gMage // on the mage; the fake tank takes it when sent
		g.onTick = func(g *fakeRemote) {
			if engagedAt.IsZero() && a.engaged {
				engagedAt = g.now
			}
		}
	}, opts())
	// The tank goes alone (`cmd attack <guid> <tank>`, #928), never as a `cmd pull` (refused tank-busy while it melees).
	sent := g.ordersOf("cmd attack " + tbm.GUIDArg(a.guid) + " ")
	if len(sent) != 1 || sent[0] != "cmd attack "+tbm.GUIDArg(a.guid)+" Tanky" || rep.Remote.LooseAddOrders != 1 || len(g.ordersOf("cmd pull ")) != 0 {
		t.Fatalf("orders %v %+v", g.orders, rep.Remote)
	}
	// It came after the mob had hit the mage for looseGrace, not at once.
	if at := g.orderAt[g.indexOf("cmd attack "+tbm.GUIDArg(a.guid)+" Tanky")]; at.Sub(engagedAt) < looseGrace {
		t.Fatalf("the tank was sent %v after the mob engaged", at.Sub(engagedAt))
	}
}

func TestRemoteLooseAddOrdersHaveACooldown(t *testing.T) {
	g, _, _, rep := aggroFight(t, func(g *fakeRemote, a, b *rmob) {
		a.hold = gMage
		g.ignorePull = true // the tank never takes it
		g.killRate = 2      // the fight lasts a minute
	}, opts())
	var at []time.Time
	for i, o := range g.orders {
		if strings.HasSuffix(o, " Tanky") && strings.HasPrefix(o, "cmd attack ") {
			at = append(at, g.orderAt[i])
		}
	}
	if len(at) != maxAddOrders || rep.Remote.LooseAddOrders != maxAddOrders {
		t.Fatalf("orders at %v: %+v", at, rep.Remote)
	}
	for i := 1; i < len(at); i++ {
		if at[i].Sub(at[i-1]) < DefaultRemoteOptions().AddCooldown {
			t.Fatalf("orders %v apart", at[i].Sub(at[i-1]))
		}
	}
}

func TestRemoteStunnedMobStaysTheFocus(t *testing.T) {
	// The feed's `c` flag covers stuns and roots too. A mob the party is fighting that gets stunned (no cc order for it) is not
	// abandoned: the party does not turn to the other mob and back.
	g, a, b, rep := aggroFight(t, func(g *fakeRemote, a, b *rmob) {
		g.killRate = 10
		g.onTick = func(g *fakeRemote) {
			if g.indexOf("cmd attack") >= 0 {
				a.flags = "c"
			}
		}
	}, opts())
	got := attackGUIDs(g)
	if len(got) != 2 || got[0] != tbm.GUIDArg(a.guid) || got[1] != tbm.GUIDArg(b.guid) || rep.Remote.CCRespected != 0 || rep.Remote.TargetSwitches != 0 {
		t.Fatalf("attack orders %v %+v", got, rep.Remote)
	}
}

func TestRemoteFlagWithoutACCOrderIsNoSheep(t *testing.T) {
	// A stunned mob nobody ordered crowd control for is a target like any other.
	g, a, _, rep := aggroFight(t, func(g *fakeRemote, a, b *rmob) { a.flags = "c" }, opts())
	if got := attackGUIDs(g); len(got) == 0 || got[0] != tbm.GUIDArg(a.guid) || rep.Remote.CCRespected != 0 {
		t.Fatalf("attack orders %v %+v", got, rep.Remote)
	}
}

func TestRemoteLeavesAMobWithACCOrderAlone(t *testing.T) {
	for _, flag := range []string{"-", "c"} {
		t.Run("flag "+flag, func(t *testing.T) { leavesACCMobAlone(t, flag) })
	}
}

// leavesACCMobAlone: a cc order was accepted for entry 300; the feed has not flagged it yet (the cast is on its way) or has: the party
// leaves it be until it is the last mob.
func leavesACCMobAlone(t *testing.T, flag string) {
	g := newFakeRemote(t)
	g.addMob(299, Point{30, 0, 0})
	sheep := g.addMob(300, Point{32, 0, 0})
	sheep.noTarget, sheep.flags = true, flag
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299, 300}, Positions: []Point{{30, 0, 0}}}}})
	o := opts()
	o.CC = []CCRule{{Entry: 300, Mark: "moon", Bot: "Magey"}}
	g.onTick = func(g *fakeRemote) {
		if g.indexOf("cmd pullback") >= 0 && !sheep.engaged {
			sheep.pulled, sheep.pullAt, sheep.engaged = true, g.now, true
		}
	}
	rep, err := runRemote(t, g, plan, o, nil)
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	got := attackGUIDs(g)
	if len(got) != 2 || got[1] != tbm.GUIDArg(sheep.guid) || rep.Remote.CCRespected != 1 {
		t.Fatalf("attack orders %v %+v", got, rep.Remote)
	}
}

func TestRemoteOpenerDelay(t *testing.T) {
	// One mob on the tank from the start: the party is sent OpenerDelay after the tank first holds it, not at once.
	run := func(o RemoteOptions) time.Duration {
		g := newFakeRemote(t)
		m := g.addMob(299, Point{30, 0, 0})
		plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{30, 0, 0}}}}})
		rep, err := runRemote(t, g, plan, o, nil)
		if err != nil {
			t.Fatalf("%v %v", err, rep.Notes)
		}
		engaged := m.pullAt.Add(g.engageDelay)
		first := g.orderAt[g.indexOf("cmd attack")]
		if rep.Remote.TankThreatShare != 100 {
			t.Fatalf("a mob on the tank all along: share %v", rep.Remote.TankThreatShare)
		}
		return first.Sub(engaged)
	}
	if d := run(opts()); d < DefaultRemoteOptions().OpenerDelay {
		t.Fatalf("the party went in %v after the mob was on the tank", d)
	}
	o := opts()
	o.OpenerDelay = -1
	if d := run(o); d >= DefaultRemoteOptions().OpenerDelay {
		t.Fatalf("without the delay the party still waited %v", d)
	}
}

func TestRemoteTargetSwitchIsCounted(t *testing.T) {
	// A is on the tank, B on the mage. Five seconds after the first attack order the mobs trade places: the party moves to the mob the
	// tank now holds, once.
	g, a, b, rep := aggroFight(t, func(g *fakeRemote, a, b *rmob) {
		b.hold = gMage
		g.ignorePull = true
		g.killRate = 3
		var flipAt time.Time
		g.onTick = func(g *fakeRemote) {
			if flipAt.IsZero() && g.indexOf("cmd attack") >= 0 {
				flipAt = g.now.Add(5 * time.Second)
			}
			if !flipAt.IsZero() && !g.now.Before(flipAt) && a.hold == 0 {
				a.hold, a.tankPct = gMage, 10
				b.hold = 0
			}
		}
	}, opts())
	got := attackGUIDs(g)
	if len(got) < 2 || got[0] != tbm.GUIDArg(a.guid) || got[1] != tbm.GUIDArg(b.guid) || rep.Remote.TargetSwitches != 1 {
		t.Fatalf("attack orders %v %+v", got, rep.Remote)
	}
}

// switchRun fights two mobs on the mage with the tank holding 45 % of A and 30 % of B; three seconds after the first attack order
// the shares become a and b. It reports the target switches.
func switchRun(t *testing.T, a2, b2 int) int {
	t.Helper()
	_, _, _, rep := aggroFight(t, func(g *fakeRemote, a, b *rmob) {
		a.hold, a.tankPct, b.hold, b.tankPct = gMage, 45, gMage, 30
		g.ignorePull = true
		g.killRate = 3
		var at time.Time
		g.onTick = func(g *fakeRemote) {
			if at.IsZero() && g.indexOf("cmd attack") >= 0 {
				at = g.now.Add(3 * time.Second)
			}
			if !at.IsZero() && !g.now.Before(at) {
				a.tankPct, b.tankPct = a2, b2
			}
		}
	}, opts())
	return rep.Remote.TargetSwitches
}

func TestRemoteSwitchNeedsALead(t *testing.T) {
	// Both mobs under 50 % of the tank's hold: a lead of 15 points does not move the party, a lead of 40 does.
	if n := switchRun(t, 40, 55); n != 0 {
		t.Fatalf("the party switched %d times for a lead of 15", n)
	}
	if n := switchRun(t, 30, 70); n != 1 {
		t.Fatalf("the party switched %d times for a lead of 40", n)
	}
}

func TestRemoteLooseGraceCountsFromTheMoveOffTheTank(t *testing.T) {
	// The mob has been on the tank for a long time and then goes to the mage: the tank is sent 3 s after that, not at once.
	var movedAt time.Time
	g, a, _, rep := aggroFight(t, func(g *fakeRemote, a, b *rmob) {
		g.ignorePull = true
		g.killRate = 2
		start := time.Time{}
		g.onTick = func(g *fakeRemote) {
			if start.IsZero() && a.engaged {
				start = g.now
			}
			if movedAt.IsZero() && !start.IsZero() && g.now.Sub(start) > 12*time.Second {
				movedAt, a.hold = g.now, gMage
			}
		}
	}, opts())
	i := g.indexOf("cmd attack " + tbm.GUIDArg(a.guid) + " Tanky")
	if i < 0 || !strings.Contains(g.orders[i], tbm.GUIDArg(a.guid)) || rep.Remote.LooseAddOrders < 1 {
		t.Fatalf("pulls %v %+v", g.ordersOf("cmd attack "), rep.Remote)
	}
	if d := g.orderAt[i].Sub(movedAt); d < looseGrace {
		t.Fatalf("the tank was sent %v after the mob left it", d)
	}
}

// A module that ignores the bot name answers party-scoped: the loose-add orders stop for the run.
func TestRemoteLooseAddOrdersStopOnAPartyScopedAnswer(t *testing.T) {
	g, _, _, rep := aggroFight(t, func(g *fakeRemote, a, b *rmob) {
		a.hold = gMage
		g.ignorePull = true
		g.killRate = 2
		g.oldModule = true
	}, opts())
	n := 0
	for _, o := range g.ordersOf("cmd attack ") {
		if strings.HasSuffix(o, " Tanky") {
			n++
		}
	}
	if n != 1 || rep.Remote.LooseAddOrders != 1 || !strings.Contains(notesOf(rep), "no more loose-add orders this run") {
		t.Fatalf("orders %d %+v\n%s", n, rep.Remote, notesOf(rep))
	}
}
