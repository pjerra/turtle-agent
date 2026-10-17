package dungeon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/tbm"
)

// runRemoteTx is runRemote with a hook on the driver before the run (its tunables).
func runRemoteTx(t *testing.T, g *fakeRemote, plan *Plan, o RemoteOptions, mod func(d *RemoteDriver)) (Report, error) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PostFight = 2 * time.Second
	cfg.AddsWait = 4 * time.Second
	d := NewRemoteDriver(plan, g, cfg, o, g.clock())
	if mod != nil {
		mod(d)
	}
	err := d.Run(context.Background(), 0)
	rep, ended := d.Status()
	if !ended {
		t.Fatal("the report does not say the run ended")
	}
	return rep, err
}

// walker moves m (while it is not pulled) back and forth on the x line between lo and hi at speed yd/s, starting with dir.
func walker(m *rmob, lo, hi, speed float32, dir float32) func(g *fakeRemote) {
	var last time.Time
	return func(g *fakeRemote) {
		if !last.IsZero() && !m.pulled && !m.engaged {
			m.pos.X += dir * speed * float32(g.now.Sub(last).Seconds())
			if m.pos.X <= lo {
				m.pos.X, dir = lo, 1
			}
			if m.pos.X >= hi {
				m.pos.X, dir = hi, -1
			}
		}
		last = g.now
	}
}

// ZF-1 hires-3 run 4: a Shadowhunter (cc_always) that joins the fight after the pull gets the cc bot's Polymorph at once; it was in no
// planned pack, so the CC before the pull never saw it.
func TestRemotePatrolJoiningMidPullIsSheeped(t *testing.T) {
	g := newFakeRemote(t)
	a := g.addMob(299, Point{30, 0, 0})
	hex := g.addMob(300, Point{90, 0, 0}) // out of the pack's radius and of the walk's way
	g.killRate = 5                        // a long fight
	g.onTick = func(g *fakeRemote) {
		if a.engaged && !hex.pulled {
			hex.pulled, hex.pullAt = true, g.now // it walks in and joins
		}
	}
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{30, 0, 0}}}}})
	plan.CCAlways = []uint32{300}
	o := opts()
	o.CCBot = "Magey"
	rep, err := runRemote(t, g, plan, o, nil)
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	cc, pb := g.indexOf("cmd cc"), g.indexOf("cmd pullback")
	if pb < 0 || cc < pb || g.orders[cc] != "cmd cc moon "+tbm.GUIDArg(hex.guid)+" Magey" {
		t.Fatalf("cc %d pullback %d %v", cc, pb, g.orders)
	}
	if r := rep.Remote; r.ReactiveCC != 1 || r.BigPackCC != 0 {
		t.Fatalf("%+v", r)
	}
}

// A sheep that runs out while the fight goes on is put on again; the tank's own target never gets one.
func TestRemoteReactiveCCIsRenewedOnLapse(t *testing.T) {
	g := newFakeRemote(t)
	a := g.addMob(299, Point{30, 0, 0})
	hex := g.addMob(300, Point{90, 0, 0})
	g.killRate, g.ccLasts = 3, 6*time.Second
	g.onTick = func(g *fakeRemote) {
		if a.engaged && !hex.pulled {
			hex.pulled, hex.pullAt = true, g.now
		}
		g.party[0].Target = a.guid // the tank is on the pulled mob
	}
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{30, 0, 0}}}}})
	plan.CCAlways = []uint32{300, 299}
	o := opts()
	o.CCBot = "Magey"
	rep, err := runRemote(t, g, plan, o, nil)
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	ccs := g.ordersOf("cmd cc")
	if len(ccs) < 2 {
		t.Fatalf("%v", g.orders)
	}
	for _, c := range ccs {
		if strings.Contains(c, tbm.GUIDArg(a.guid)) {
			t.Fatalf("the tank's target got a cc: %v", ccs)
		}
	}
	if r := rep.Remote; r.ReactiveCC != 1 || r.CCRenews < 1 {
		t.Fatalf("%+v %v", r, ccs)
	}
}

// A patrol whose path brings it to the pack is waited for until it has turned and walks away; the pull goes after that.
func TestRemoteWaitsForAPatrolToPass(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{60, 0, 0})
	p := g.addMob(301, Point{200, 0, 0})
	g.onTick = walker(p, 80, 300, 5, -1)
	pat := &Patrol{Entries: []uint32{301}, Path: []Point{{300, 0, 0}, {80, 0, 0}}, PeriodS: 88}
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{60, 0, 0}}, Patrol: pat}}})
	var atPull float32 = -1
	walk := g.onTick
	g.onTick = func(g *fakeRemote) {
		walk(g)
		if atPull < 0 && g.indexOf("cmd pullback") >= 0 {
			atPull = p.pos.X
		}
	}
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	if r := rep.Remote; r.PatrolWaits != 1 || r.PatrolTimeouts != 0 || r.PatrolPulls != 0 || r.PatrolWaitS < 15 {
		t.Fatalf("%+v", r)
	}
	// It turned at 80 and was walking east, out of patrol_aggro of the pack at 60, when the pull went.
	if atPull <= 60+PatrolAggro {
		t.Fatalf("patrol at %.0f at the pull; %v", atPull, g.orders)
	}
}

// Rule 3: a patrol that never walks away (it stands by the pack) costs at most patrol_wait_s per group; then the pack is pulled with it
// counted in.
func TestRemotePatrolWaitIsBounded(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{60, 0, 0})
	p := g.addMob(301, Point{90, 0, 0}) // on its path, 30 yd from the pack: it walks to and fro there and never away
	g.onTick = walker(p, 85, 95, 2, -1)
	pat := &Patrol{Entries: []uint32{301}, Path: []Point{{300, 0, 0}, {80, 0, 0}}, PeriodS: 88}
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{60, 0, 0}}, Patrol: pat}}})
	rep, err := runRemoteTx(t, g, plan, opts(), func(d *RemoteDriver) { d.tx.PatrolWait = 20 * time.Second })
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	if r := rep.Remote; r.PatrolWaits != 1 || r.PatrolTimeouts != 1 || r.PatrolWaitS < 19 || r.PatrolWaitS > 23 {
		t.Fatalf("%+v", r)
	}
	if g.indexOf("cmd pullback") < 0 {
		t.Fatalf("%v", g.orders)
	}
}

// A patrol that passes the party clear of the pack is pulled alone first (with the pack CC on its cc_always member), then the pack.
func TestRemotePatrolPassingThePartyIsPulledAlone(t *testing.T) {
	g := newFakeRemote(t)
	a := g.addMob(299, Point{60, 0, 0})
	p1 := g.addMob(301, Point{15, 120, 0})
	p2 := g.addMob(300, Point{16, 121, 0})
	var last time.Time
	g.onTick = func(g *fakeRemote) {
		if !last.IsZero() {
			for _, m := range []*rmob{p1, p2} {
				if !m.pulled && !m.engaged && m.pos.Y > 20 {
					m.pos.Y -= 5 * float32(g.now.Sub(last).Seconds())
				}
			}
		}
		last = g.now
		if p1.pulled && !p2.pulled {
			p2.pulled, p2.pullAt = true, g.now // the pair is linked: it comes with it
		}
	}
	pat := &Patrol{Entries: []uint32{301, 300}, Path: []Point{{15, 200, 0}, {15, 20, 0}}, PeriodS: 72}
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{60, 0, 0}}, Patrol: pat}}})
	plan.CCAlways = []uint32{300}
	o := opts()
	o.CCBot = "Magey"
	rep, err := runRemote(t, g, plan, o, nil)
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	pbs := g.ordersOf("cmd pullback")
	if len(pbs) < 2 || pbs[0] != "cmd pullback "+tbm.GUIDArg(p1.guid) || pbs[len(pbs)-1] != "cmd pullback "+tbm.GUIDArg(a.guid) {
		t.Fatalf("%v", g.orders)
	}
	if cc := g.indexOf("cmd cc"); cc < 0 || g.orders[cc] != "cmd cc moon "+tbm.GUIDArg(p2.guid)+" Magey" {
		t.Fatalf("%v", g.orders)
	}
	if r := rep.Remote; r.PatrolPulls != 1 {
		t.Fatalf("%+v", r)
	}
}

// A patrol member standing within patrol_aggro of the pull target counts in its pack: three mobs and two patrollers make a big pack.
func TestRemotePatrolNearThePullCountsInThePack(t *testing.T) {
	g := newFakeRemote(t)
	for i := 0; i < 3; i++ {
		g.addMob(299, Point{60 + float32(i), 0, 0})
	}
	g.addMob(301, Point{60, 20, 0})
	g.addMob(301, Point{61, 21, 0})
	pat := &Patrol{Entries: []uint32{301}, Path: []Point{{60, 200, 0}, {60, 20, 0}}, PeriodS: 144}
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299, 299, 299}, Positions: []Point{{60, 0, 0}}, Radius: 10, Patrol: pat}}})
	o := opts()
	o.CCBot = "Magey"
	rep, err := runRemoteTx(t, g, plan, o, func(d *RemoteDriver) { d.tx.PatrolWait = 0 })
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	if r := rep.Remote; r.BigPacks < 1 || r.BigPackCC < 1 {
		t.Fatalf("%+v %v", r, g.orders)
	}
}

// ZF-1 run 4: the module ignores a move under about 6 yd, and a walk whose last stretch was 5.2 yd stalled five times and failed. The last
// stretch goes as a move to min_move+1 yd along the line.
func TestRemoteTinyMoveStepsPast(t *testing.T) {
	g := newFakeRemote(t)
	g.minMove = 6
	g.addMob(299, Point{40, 0, 0})
	plan := remotePlan(Leg{Name: "a", To: Point{5.5, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{40, 0, 0}}}}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	if rep.Remote.TinyMoves < 1 || len(g.moveOrderDest) == 0 || g.moveOrderDest[0].X < 6.9 || g.moveOrderDest[0].X > 7.1 || rep.Remote.MoveStalls != 0 {
		t.Fatalf("%+v %v", rep.Remote, g.moveOrderDest)
	}
}

// rfc-live-2, leg 4: a hop of 6.3 yd in a line but 5.3 yd on the ground (a slope) was acked three times and never walked: the module measures
// a move on the ground plane. The tiny-move rule measures the same way, so the hop goes as a step past.
func TestRemoteTinyMoveOnASlopeIsMeasuredOnTheGround(t *testing.T) {
	g := newFakeRemote(t)
	g.minMove = 6
	plan := remotePlan(Leg{Name: "a", To: Point{5.3, 0, 3.4}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	if rep.Remote.TinyMoves < 1 || len(g.moveOrderDest) == 0 || g.moveOrderDest[0].X < 6.9 || g.moveOrderDest[0].X > 7.1 || rep.Remote.MoveStalls != 0 {
		t.Fatalf("%+v %v", rep.Remote, g.moveOrderDest)
	}
}

func TestPlanPatrolIsChecked(t *testing.T) {
	base := `{"version":1,"name":"p","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[{"name":"a","to":{"x":0,"y":0,"z":0},
"pull_groups":[{"entries":[1],"positions":[{"x":1,"y":1,"z":0}],"patrol":%s}]}]}`
	ok := `{"entries":[2],"path":[{"x":1,"y":1,"z":0},{"x":9,"y":1,"z":0}],"period_s":30,"source":"spawn 1"}`
	p, err := Parse("p.json", []byte(strings.Replace(base, "%s", ok, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if pt := p.Legs[0].PullGroups[0].Patrol; pt == nil || len(pt.Path) != 2 || pt.PeriodS != 30 || pt.Entries[0] != 2 {
		t.Fatalf("%+v", pt)
	}
	for _, bad := range []string{
		`{"entries":[],"path":[{"x":1,"y":1,"z":0},{"x":9,"y":1,"z":0}],"period_s":30}`,
		`{"entries":[2],"path":[{"x":1,"y":1,"z":0}],"period_s":30}`,
		`{"entries":[2],"path":[{"x":1,"y":1,"z":0},{"x":9,"y":1,"z":0}],"period_s":0}`,
	} {
		if _, err := Parse("p.json", []byte(strings.Replace(base, "%s", bad, 1))); err == nil || !strings.Contains(err.Error(), "patrol") {
			t.Fatalf("%s: %v", bad, err)
		}
	}
}

func TestPathAheadWrapsTheRound(t *testing.T) {
	path := []Point{{0, 0, 0}, {10, 0, 0}}
	seg, along, off := onPath(path, Point{8, 1, 0})
	if seg != 0 || along < 7.9 || along > 8.1 || off > 1.01 {
		t.Fatalf("%d %.2f %.2f", seg, along, off)
	}
	pts := pathAhead(path, seg, along, 10)
	if last := pts[len(pts)-1]; last.X > 6 { // 2 yd to the end, then back towards 0
		t.Fatalf("%v", pts)
	}
}

// A unit of the patrol's entries that stands still on the path is no patrol: it is waited for only until it has stood patrolStaticAfter.
func TestRemoteStaticMobOnThePatrolPathIsNoPatrol(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{60, 0, 0})
	g.addMob(301, Point{90, 0, 0})
	pat := &Patrol{Entries: []uint32{301}, Path: []Point{{300, 0, 0}, {80, 0, 0}}, PeriodS: 88}
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{60, 0, 0}}, Patrol: pat}}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	if r := rep.Remote; r.PatrolTimeouts != 0 || r.PatrolWaitS > patrolStaticAfter.Seconds()+2 {
		t.Fatalf("%+v", r)
	}
}

// An estimated period (period_estimate) gives no pace: where the estimate says the patrol is far from the pull in the window, the party
// still waits for it (nothing is predicted from the estimate), and writes a note; a measured period of the same size does predict it safe.
func TestRemoteEstimatedPatrolPeriodIsNeverPredictedSafe(t *testing.T) {
	run := func(estimate bool) (Report, float32) {
		g := newFakeRemote(t)
		g.addMob(299, Point{60, 0, 0})
		p := g.addMob(301, Point{200, 0, 0}) // 140 yd from the pack, walking at 5 yd/s: the estimate below says 0.7 yd/s
		g.onTick = walker(p, 80, 300, 5, -1)
		pat := &Patrol{Entries: []uint32{301}, Path: []Point{{300, 0, 0}, {80, 0, 0}}, PeriodS: 600, PeriodEstimate: estimate}
		plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{60, 0, 0}}, Patrol: pat}}})
		var atPull float32 = -1
		walk := g.onTick
		g.onTick = func(g *fakeRemote) {
			walk(g)
			if atPull < 0 && g.indexOf("cmd pullback") >= 0 {
				atPull = p.pos.X
			}
		}
		rep, err := runRemote(t, g, plan, opts(), nil)
		if err != nil {
			t.Fatalf("%v %v", err, rep.Notes)
		}
		return rep, atPull
	}
	rep, atPull := run(true)
	if r := rep.Remote; r.PatrolWaits < 1 || r.PatrolTimeouts != 0 {
		t.Fatalf("%+v %v", r, rep.Notes)
	}
	if atPull <= 60+PatrolAggro {
		t.Fatalf("pulled with the patrol at %.0f", atPull)
	}
	if n := notesOf(rep); !strings.Contains(n, "period_estimate") || !strings.Contains(n, "phase is not predicted") {
		t.Fatalf("no note about the estimate: %s", n)
	}
	// The same patrol with a measured period is predicted safe (far from the pull in the window): no wait, no note.
	rep, _ = run(false)
	if r := rep.Remote; r.PatrolWaits != 0 || strings.Contains(notesOf(rep), "period_estimate") {
		t.Fatalf("%+v %v", r, rep.Notes)
	}
}

// patrolDanger of an estimated patrol looks a distance ahead, not a time.
func TestPatrolDangerOfAnEstimatedPeriodLooksADistanceAhead(t *testing.T) {
	g := newFakeRemote(t)
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{60, 0, 0}}}}})
	d := NewRemoteDriver(plan, g, DefaultConfig(), opts(), g.clock())
	member := tbm.HostileRec{Entry: 301, X: 200, Y: 0, Z: 0}
	for _, c := range []struct {
		estimate bool
		want     bool
	}{{false, false}, {true, true}} {
		p := &Patrol{Entries: []uint32{301}, Path: []Point{{300, 0, 0}, {0, 0, 0}}, PeriodS: 600, PeriodEstimate: c.estimate}
		if got := d.patrolDanger(p, member, []Point{{60, 0, 0}}); got != c.want {
			t.Errorf("estimate %v: danger %v, want %v", c.estimate, got, c.want)
		}
	}
}

func TestPatrolPeriodEstimateParsesAndWarns(t *testing.T) {
	doc := `{"version":1,"name":"p","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[{"name":"a","to":{"x":0,"y":0,"z":0},
"pull_groups":[{"entries":[1],"positions":[{"x":1,"y":1,"z":0}],"patrol":{"entries":[2],"path":[{"x":1,"y":1,"z":0},{"x":9,"y":1,"z":0}],"period_s":30,"period_estimate":true}}]}]}`
	p, err := Parse("p.json", []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Legs[0].PullGroups[0].Patrol.PeriodEstimate {
		t.Fatal("period_estimate not read")
	}
	b, err := Marshal(p)
	if err != nil || !strings.Contains(string(b), `"period_estimate": true`) {
		t.Fatalf("%v %s", err, b)
	}
	res, err := CheckPlan(p, fakeNav{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range res.Warnings {
		found = found || w.Reason == WarnEstimatedPatrol
	}
	if !found || !res.OK() {
		t.Fatalf("warnings %+v ok %v", res.Warnings, res.OK())
	}
}
