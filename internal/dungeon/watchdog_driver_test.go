package dungeon

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The watchdog in both drivers, on their fake clocks (watchdog_test.go has the watchdog alone).

// stuckRemote is a one-leg plan whose rest gate may wait ten minutes, and a party whose mage is hurt and never heals: the gate waits for
// nothing.
func stuckRemote(t *testing.T) (*fakeRemote, *Plan) {
	g := newFakeRemote(t)
	g.party[2].HP = 600
	g.healRate = 0
	return g, remotePlan(Leg{Name: "a", To: Point{20, 0, 0}, Rest: &RestSpec{MaxS: 600}})
}

// Remote: a rest gate has limits of its own (here 600 s): the watchdog's clock waits for it, and the gate ends by its own limit.
func TestRemoteWatchdogHoldsDuringARestGate(t *testing.T) {
	g, plan := stuckRemote(t)
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	if gate := rep.RestGates[0]; gate.Result != "gave_up" || gate.Seconds < 600 {
		t.Fatalf("%+v", gate)
	}
	if w := rep.Watchdog; w.Warnings != 0 || w.Recoveries != 0 || strings.Contains(notesOf(rep), "stall") {
		t.Fatalf("%+v\n%s", w, notesOf(rep))
	}
}

// Remote: a pull whose mob is never worn down stalls; its recovery sends cmd follow and picks the target again, and the second fight
// goes through: the run is done, one stall recovered.
func TestRemoteWatchdogRecoversAStalledPull(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{30, 5, 0})
	g.killRate = 0
	g.onTick = func(g *fakeRemote) {
		if g.indexOf("cmd follow") >= 0 {
			g.killRate = 25
		}
	}
	plan := remotePlan(Leg{Name: "a", To: Point{20, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{30, 5, 0}}}}})
	rep, err := runRemote(t, g, plan, opts(), func(c *Config) { c.FightTimeout = 3000 * time.Second })
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, notesOf(rep))
	}
	if rep.Watchdog.Recoveries != 1 || !strings.Contains(notesOf(rep), "sent cmd follow and ended the fight to pick the target again") {
		t.Fatalf("%+v\n%s", rep.Watchdog, notesOf(rep))
	}
}

// Remote: a fight that is long but wears its mob down never stalls (a boss fight of eight minutes).
func TestRemoteWatchdogLongProgressingFightNeverStalls(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{30, 5, 0})
	g.killRate = 0.2 // 500 s for 100 percent
	plan := remotePlan(Leg{Name: "a", To: Point{20, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{30, 5, 0}}}}})
	rep, err := runRemote(t, g, plan, opts(), func(c *Config) { c.FightTimeout = 3000 * time.Second })
	if err != nil || rep.State != "done" || rep.Seconds < 450 {
		t.Fatalf("%v %s %.0f s %s", err, rep.State, rep.Seconds, notesOf(rep))
	}
	if w := rep.Watchdog; w.Warnings != 0 || w.Recoveries != 0 || w.LongestStall >= 120 || strings.Contains(notesOf(rep), "stall") {
		t.Fatalf("%+v\n%s", w, notesOf(rep))
	}
}

// Remote: a boss stuck in combat at full health. The pull is recovered once (cmd follow, pick again); the run of the tank to the
// commander and back to the boss, the re-pull, the rest gate it goes through and the orders it ACKs are not progress, so the next
// interval without real progress goes to the failure: fail_s is a ceiling.
func TestRemoteWatchdogFailsAPullThatRecoveryDoesNotFix(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{30, 5, 0})
	g.killRate = 0
	g.followTo = &Point{-30, 0, 0} // 60 yd from the boss: the tank runs there on cmd follow and back on the re-pull
	plan := remotePlan(Leg{Name: "a", To: Point{20, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{30, 5, 0}}}}})
	rep, err := runRemote(t, g, plan, opts(), func(c *Config) { c.FightTimeout = 3000 * time.Second; c.BossTimeout = 3000 * time.Second })
	if err == nil || rep.State != "failed" || !strings.HasPrefix(rep.Error, "stalled: ") || !strings.Contains(rep.Error, "leg 0 (a): pull group 1 of 1 for 4") {
		t.Fatalf("%v %q", err, rep.Error)
	}
	if w := rep.Watchdog; w.Recoveries != 1 || w.StallLevel != "fail" || w.StalledForS > 430 {
		t.Fatalf("recovery on, fail at the ceiling: %+v", w)
	}
	if n := len(g.ordersOf("cmd follow")); n < 1 {
		t.Fatalf("the recovery sent cmd follow: %v", g.orders)
	}
	// The fake did run the tank away and back: the lead went near the commander after the follow, then near the boss again.
	away, back := -1, -1
	for i, p := range g.trace {
		if away < 0 && dist3(p, *g.followTo) < 5 {
			away = i
		} else if away >= 0 && dist3(p, Point{30, 5, 0}) < 10 {
			back = i
			break
		}
	}
	if away < 0 || back < 0 {
		t.Fatalf("the tank did not run to the commander and back: away %d back %d", away, back)
	}
	// fail_s from the pull's start plus the 90 s the closing fight may keep the binding, not a cycle of recover_s per re-pull (before the
	// fix: 783 s and two recoveries).
	if rep.Seconds > 530 {
		t.Fatalf("the run took %.0f s: fail_s is not a ceiling\n%s", rep.Seconds, notesOf(rep))
	}
	if g.unbinds != 1 {
		t.Fatalf("unbinds %d", g.unbinds)
	}
}

// Both drivers: a request a recovery left behind (it set the flag, the loop it was for had ended) is not the next step's or fight's.
func TestWatchdogStaleRequestsAreCleared(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{30, 5, 0})
	plan := remotePlan(Leg{Name: "a", To: Point{20, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{30, 5, 0}}}}})
	d := NewRemoteDriver(plan, g, DefaultConfig(), opts(), g.clock())
	d.wdRepick.set()
	d.wdObject.set()
	if err := d.Run(t.Context(), 0); err != nil {
		t.Fatalf("%v", err)
	}
	rep := d.Report()
	if rep.Legs[0].Pulls != 1 || strings.Contains(notesOf(rep), "watchdog") || d.wdRepick.take() || d.wdObject.take() {
		t.Fatalf("%+v\n%s", rep.Legs[0], notesOf(rep))
	}

	gg, p := runFixture(t)
	gd := NewDriver(p, gg, Config{}, gg.clock())
	gd.wdRepick.set()
	gd.wdObject.set()
	if err := gd.Run(t.Context(), 0); err != nil {
		t.Fatalf("%v\n%s", err, notes(gd))
	}
	if strings.Contains(notes(gd), "watchdog") || gd.wdRepick.take() || gd.wdObject.take() {
		t.Fatalf("%s", notes(gd))
	}
}

// Remote: the same fight with a mob that does not lose health fails the run at fail_s, naming the pull.
func TestRemoteWatchdogFailsAFightThatGoesNowhere(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{30, 5, 0})
	g.killRate = 0
	plan := remotePlan(Leg{Name: "a", To: Point{20, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{30, 5, 0}}}}})
	rep, err := runRemote(t, g, plan, opts(), func(c *Config) {
		c.FightTimeout = 3000 * time.Second
		c.Watchdog = WatchdogConfig{WarnS: 60, RecoverS: -1, FailS: 200}
	})
	if err == nil || !strings.HasPrefix(rep.Error, "stalled: fight leg 0 (a): pull group 1 of 1 for 20") || g.unbinds != 1 {
		t.Fatalf("%v unbinds %d", err, g.unbinds)
	}
	if !strings.Contains(rep.Error, "last progress pull started") && !strings.Contains(rep.Error, "last progress order acked") {
		t.Fatalf("%q", rep.Error)
	}
}

// Remote: a walk whose move was ignored is sent again from where the lead bot stands, with a shorter leg, at the recovery.
func TestRemoteWatchdogReplansAStalledWalk(t *testing.T) {
	g := newFakeRemote(t)
	g.ignoreMoves = 1
	plan := remotePlan(Leg{Name: "a", To: Point{300, 0, 0}})
	o := opts()
	o.MoveStall = 1000 * time.Second
	rep, err := runRemote(t, g, plan, o, func(c *Config) { c.Watchdog = WatchdogConfig{WarnS: 20, RecoverS: 30, FailS: 600} })
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, notesOf(rep))
	}
	if !strings.Contains(notesOf(rep), "planned the walk again from 0 0 0: cmd move 40 0 0 (a leg of at most 40 yd)") {
		t.Fatalf("%s", notesOf(rep))
	}
	if rep.Watchdog.Recoveries < 1 {
		t.Fatalf("%+v", rep.Watchdog)
	}
}

// Remote: a silent feed is recovered with `commander view on` again at the recovery level, and fails the run at fail_s when it stays
// silent (FeedLost is long here, so that it does not fail the run first).
func TestRemoteWatchdogResendsTheViewOnASilentFeed(t *testing.T) {
	g := newFakeRemote(t)
	g.cutAt = g.now.Add(30 * time.Second)
	plan := remotePlan(Leg{Name: "a", To: Point{3000, 0, 0}})
	o := opts()
	o.FeedLost = 3000 * time.Second
	rep, err := runRemote(t, g, plan, o, nil)
	if err == nil || !strings.HasPrefix(rep.Error, "stalled: walk leg 0 (a): walk to 3000.0 0.0 0.0 for 420 s") || rep.State != "failed" {
		t.Fatalf("%v %q", err, rep.Error)
	}
	if n := len(g.ordersOf("commander view on")); n != 2 {
		t.Fatalf("view on: bind and one recovery, got %d: %v", n, g.ordersOf("commander"))
	}
	if !strings.Contains(notesOf(rep), "the feed is silent: sent commander view on again") || g.unbinds != 1 {
		t.Fatalf("%s", notesOf(rep))
	}
}

// Remote: before the feed is declared lost, the view is switched on again once (half of FeedLost).
func TestRemoteResendsTheViewBeforeTheFeedIsLost(t *testing.T) {
	g := newFakeRemote(t)
	g.cutAt = g.now.Add(30 * time.Second)
	_, err := runRemote(t, g, remotePlan(Leg{Name: "a", To: Point{3000, 0, 0}}), opts(), nil)
	if err == nil || !strings.Contains(err.Error(), "view feed stopped") {
		t.Fatalf("%v", err)
	}
	if n := len(g.ordersOf("commander view on")); n != 2 {
		t.Fatalf("%d: %v", n, g.ordersOf("commander"))
	}
}

// Remote: a plan's wait_for has its own timeout_s and is not a stall: a 500 s wait past fail_s ends by its own timeout.
func TestRemoteWatchdogDoesNotCountAPlanWait(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{25, 0, 0}) // never pulled, never leaves
	plan := remotePlan(Leg{Name: "a", To: Point{20, 0, 0}, Steps: []Step{{WaitFor: &WaitFor{HostilesGone: &HostilesGone{Radius: 40}, TimeoutS: 500}}}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err == nil || strings.Contains(err.Error(), "stalled") || rep.Seconds < 500 || rep.Watchdog.Warnings != 0 {
		t.Fatalf("%v %.0f s %+v", err, rep.Seconds, rep.Watchdog)
	}
}

// ---- group mode ----

const stuckRestPlan = `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"to":{"x":0,"y":0,"z":0},"rest":{"max_s":600},"pull_groups":[{"entries":[1],"positions":[{"x":20,"y":0,"z":0}]}]}]}`

// stuckGroup is a party whose healer is low on mana and never drinks: the rest gate waits for nothing.
func stuckGroup() *fakeGame {
	g := restFake()
	g.party[1].Power = 116 // 29%, below HealerManaMin: waited for
	return g
}

// Group: a rest gate has limits of its own (here 600 s): the watchdog's clock waits for it.
func TestDriverWatchdogHoldsDuringARestGate(t *testing.T) {
	g := stuckGroup()
	d := runRest(t, g, stuckRestPlan, Config{})
	rep := d.Report()
	if rep.State != "done" || rep.Watchdog.Warnings != 0 || rep.Watchdog.Recoveries != 0 || strings.Contains(notes(d), "stall") {
		t.Fatalf("%s %+v\n%s", rep.State, rep.Watchdog, notes(d))
	}
	for _, gate := range rep.RestGates {
		if gate.Result != "gave_up" || gate.Seconds < 600 {
			t.Fatalf("%+v", gate)
		}
	}
}

// stuckFight is a pack whose mob fights for ever at the same health.
func stuckFight() *fakeGame {
	g := newFake()
	g.add(201, 1, Point{20, 0, 0})
	g.dieIn = 100_000 * time.Second
	return g
}

// Group: a stuck fight is recovered once (follow, pick again); the mob is still stuck, so the run fails at the plan's fail_s (the ceiling
// holds with the recovery on). The run parameter wins over the plan's levels: with the recovery turned off it fails at the same time.
func TestDriverWatchdogRecoversOrFailsAStuckFight(t *testing.T) {
	plan := strings.Replace(restPlan, `"legs"`, `"watchdog":{"warn_s":50,"recover_s":100,"fail_s":300},"legs"`, 1)
	g := stuckFight()
	d := NewDriver(mustPlan(t, plan), g, Config{FightTimeout: 100_000 * time.Second}, g.clock())
	err := d.Run(t.Context(), 0)
	rep := d.Report()
	if err == nil || !strings.HasPrefix(rep.Error, "stalled: ") || !strings.Contains(rep.Error, "pull group 1 of 1 for 3") {
		t.Fatalf("%v %q\n%s", err, rep.Error, notes(d))
	}
	if w := rep.Watchdog; w.Recoveries != 1 || w.StallLevel != "fail" || w.StalledForS > 310 || g.count(".bot action follow") < 1 || !strings.Contains(notes(d), "told the party to follow and ended the fight to pick the target again") {
		t.Fatalf("%+v\n%s", w, notes(d))
	}

	g = stuckFight()
	d = NewDriver(mustPlan(t, plan), g, Config{FightTimeout: 100_000 * time.Second, Watchdog: WatchdogConfig{RecoverS: -1}}, g.clock())
	err = d.Run(t.Context(), 0)
	rep = d.Report()
	if err == nil || rep.State != "failed" || !strings.HasPrefix(rep.Error, "stalled: ") || !strings.Contains(rep.Error, "pull group 1 of 1 for 3") {
		t.Fatalf("%v %q\n%s", err, rep.Error, notes(d))
	}
	if w := rep.Watchdog; w.WarnS != 50 || w.RecoverS != 0 || w.FailS != 300 || w.StallLevel != "fail" || w.Recoveries != 0 {
		t.Fatalf("%+v", w)
	}

	g = stuckFight()
	d = NewDriver(mustPlan(t, plan), g, Config{FightTimeout: 100_000 * time.Second, Watchdog: WatchdogConfig{RecoverS: -1, FailS: 200}}, g.clock())
	_ = d.Run(t.Context(), 0)
	if rep := d.Report(); rep.Watchdog.FailS != 200 || !strings.Contains(rep.Error, " for 2") {
		t.Fatalf("%+v %q", rep.Watchdog, rep.Error)
	}
}

// Group: a long fight that wears its mobs down never stalls.
func TestDriverWatchdogLongProgressingFightNeverStalls(t *testing.T) {
	g := newFake()
	m := g.add(201, 1, Point{20, 0, 0})
	g.dieIn = 600 * time.Second
	started := false
	g.onTick = func(g *fakeGame) {
		switch {
		case m.InCombat && !m.Dead && !started: // the pull has set it fighting at half health: give it a bigger pool
			started, m.MaxHealth, m.Health = true, 100_000, 100_000
		case started && !m.Dead && m.Health > 1000:
			m.Health -= 150 // 0.15 percent a second: the mob dies by its own timer at the end
		}
	}
	d := NewDriver(mustPlan(t, restPlan), g, Config{FightTimeout: 3000 * time.Second}, g.clock())
	if err := d.Run(t.Context(), 0); err != nil {
		t.Fatalf("%v\n%s", err, notes(d))
	}
	if w := d.Report().Watchdog; w.Warnings != 0 || strings.Contains(notes(d), "stall") {
		t.Fatalf("%+v\n%s", w, notes(d))
	}
}

// Remote: an object step whose carrier order is refused in-combat again and again would wait a minute; at the recovery level the
// order's retries are cancelled and the step ends with the refusal, long before the wait's own end.
func TestRemoteWatchdogCancelsTheCarrierRetries(t *testing.T) {
	g := carrierGame(t)
	for i := 0; i < 100; i++ {
		g.script["open"] = append(g.script["open"], errOrder("cmd open", "in-combat"))
	}
	plan := remotePlan(Leg{Name: "c", To: Point{20, 0, 0}, Steps: []Step{lootChestStep()}})
	cfg := DefaultConfig()
	cfg.Watchdog = WatchdogConfig{WarnS: 8, RecoverS: 16, FailS: 600}
	d := NewRemoteDriver(plan, g, cfg, objOpts(), g.clock())
	err := d.Run(t.Context(), 0)
	rep := d.Report()
	if err == nil || !strings.Contains(err.Error(), "refused: in-combat") || rep.State != "failed" {
		t.Fatalf("%v", err)
	}
	if n := len(g.ordersOf("cmd open")); n >= 12 {
		t.Fatalf("%d orders: the retries ran their whole minute", n)
	}
	if rep.Watchdog.Recoveries != 1 || !strings.Contains(notesOf(rep), "cancelled the carrier order's retries: the step retries it once, then takes the fallback") {
		t.Fatalf("%+v\n%s", rep.Watchdog, notesOf(rep))
	}
}

// ---- round 3: a fight is judged by each mob's low; a walk in a pull step is a walk; fights at a gate or on the way are watched ----

// Remote: a mob whose health flips between 100 % and 90 % (it evades and comes back, or is healed) is progress once: the pull is
// recovered once and the run fails at fail_s, not by the fight's own 300 s limit after each re-pull.
func TestRemoteWatchdogFailsAFightWhoseHealthComesBack(t *testing.T) {
	g := newFakeRemote(t)
	m := g.addMob(299, Point{30, 5, 0})
	g.killRate = 0
	var flipAt time.Time
	g.onTick = func(g *fakeRemote) {
		if !m.engaged {
			return
		}
		if flipAt.IsZero() {
			flipAt = g.now
		}
		m.hp = 100
		if int(g.now.Sub(flipAt)/(10*time.Second))%2 == 1 {
			m.hp = 90
		}
	}
	plan := remotePlan(Leg{Name: "a", To: Point{20, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{30, 5, 0}}}}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err == nil || !strings.HasPrefix(rep.Error, "stalled: ") {
		t.Fatalf("%v %q\n%s", err, rep.Error, notesOf(rep))
	}
	if w := rep.Watchdog; w.Recoveries != 1 || w.StallLevel != "fail" || w.StalledForS > 430 || rep.Seconds > 560 {
		t.Fatalf("%.0f s %+v\n%s", rep.Seconds, w, notesOf(rep))
	}
}

// Group: the same with the leader's party.
func TestDriverWatchdogFailsAFightWhoseHealthComesBack(t *testing.T) {
	g := stuckFight()
	m := g.units[0]
	var flipAt time.Time
	g.onTick = func(g *fakeGame) {
		if !m.InCombat || m.Dead {
			return
		}
		if flipAt.IsZero() {
			flipAt = g.now
		}
		m.Health = 50
		if int(g.now.Sub(flipAt)/(10*time.Second))%2 == 1 {
			m.Health = 40
		}
	}
	d := NewDriver(mustPlan(t, restPlan), g, Config{}, g.clock())
	err := d.Run(t.Context(), 0)
	rep := d.Report()
	if err == nil || !strings.HasPrefix(rep.Error, "stalled: ") {
		t.Fatalf("%v %q\n%s", err, rep.Error, notes(d))
	}
	if w := rep.Watchdog; w.Recoveries != 1 || w.StallLevel != "fail" || w.StalledForS > 430 {
		t.Fatalf("%+v\n%s", w, notes(d))
	}
}

// Remote: a walk inside a pull step (here the walk to a group's position to look at it) that gets on after the walk's recovery is
// progress: the lead bot's first move is ignored, the recovery plans the walk again, and the 1500 yd walk after it does not fail the run.
func TestRemoteWatchdogCountsAWalkInAPullStepAfterTheRecovery(t *testing.T) {
	g := newFakeRemote(t)
	g.ignoreMoves = 1
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{1500, 0, 0}}}}})
	o := opts()
	o.MoveStall = 1000 * time.Second
	rep, err := runRemote(t, g, plan, o, func(c *Config) { c.Watchdog = WatchdogConfig{WarnS: 20, RecoverS: 30, FailS: 150} })
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %q\n%s", err, rep.State, rep.Error, notesOf(rep))
	}
	if rep.Watchdog.Recoveries < 1 || rep.Seconds < 150 {
		t.Fatalf("%.0f s %+v\n%s", rep.Seconds, rep.Watchdog, notesOf(rep))
	}
}

// Group: the walk to a group's standoff point inside its pull step. The first try stands still for 250 s (the stall recovers), the
// second gets there after 200 s: that is progress, so the pull goes on and the run is done.
func TestDriverWatchdogCountsAWalkInAPullStepAfterTheRecovery(t *testing.T) {
	plan := `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":1000,"y":0,"z":0}]}]}]}`
	g := newFake()
	g.add(201, 1, Point{1000, 0, 0})
	tries := 0
	g.walkHook = func(ctx context.Context, g *fakeGame, to Point) (WalkResult, error) {
		if dist3(to, Point{}) < 1 {
			g.pos = to
			return WalkResult{Result: "arrived"}, nil
		}
		if tries++; tries == 1 {
			g.now = g.now.Add(250 * time.Second)
			return WalkResult{Result: "failed", Reason: "stuck"}, nil
		}
		g.now = g.now.Add(200 * time.Second)
		g.pos = to
		return WalkResult{Result: "arrived"}, nil
	}
	d := NewDriver(mustPlan(t, plan), g, Config{}, g.clock())
	if err := d.Run(t.Context(), 0); err != nil {
		t.Fatalf("%v\n%s", err, notes(d))
	}
	if w := d.Report().Watchdog; w.Recoveries != 1 || tries != 2 {
		t.Fatalf("tries %d %+v\n%s", tries, w, notes(d))
	}
}

// wayMob makes a mob aggro on the party once the lead bot is 50 yd into its walk; engagedAt is when it joined the fight.
func wayMob(g *fakeRemote, engagedAt *time.Time) {
	var m *rmob
	g.onTick = func(g *fakeRemote) {
		if m == nil && g.lead().X >= 50 {
			m = g.addMob(300, Point{g.lead().X + 3, 0, 0})
			m.pulled, m.pullAt = true, g.now
		}
		if m != nil && m.engaged && engagedAt.IsZero() {
			*engagedAt = g.now
		}
	}
}

// Remote: a fight on the way that goes nowhere stalls inside the walk; the recovery sends no `cmd move` while the party fights (the
// walk would drag the lead bot off its fight), and the run ends by the fight's own limit or at fail_s.
func TestRemoteWatchdogSendsNoMoveWhileTheWayFightGoesOn(t *testing.T) {
	g := newFakeRemote(t)
	g.killRate = 0
	var engagedAt time.Time
	wayMob(g, &engagedAt)
	rep, err := runRemote(t, g, remotePlan(Leg{Name: "a", To: Point{300, 0, 0}}), opts(), nil)
	if err == nil || engagedAt.IsZero() {
		t.Fatalf("%v engaged %v", err, engagedAt)
	}
	for i, at := range g.moveOrderAt {
		if !at.Before(engagedAt) {
			t.Fatalf("cmd move to %v sent %.0f s into the fight\n%s", g.moveOrderDest[i], at.Sub(engagedAt).Seconds(), notesOf(rep))
		}
	}
	if rep.Watchdog.Recoveries < 1 || rep.Watchdog.StalledForS > 430 {
		t.Fatalf("%+v\n%s", rep.Watchdog, notesOf(rep))
	}
}

// Remote: a fight on the way of 280 s that wears its mob down never stalls: it has the same health signal as a pull.
func TestRemoteWatchdogWayFightThatWearsDownNeverStalls(t *testing.T) {
	g := newFakeRemote(t)
	g.killRate = 100.0 / 280
	var engagedAt time.Time
	wayMob(g, &engagedAt)
	rep, err := runRemote(t, g, remotePlan(Leg{Name: "a", To: Point{300, 0, 0}}), opts(), nil)
	if err != nil || rep.State != "done" || engagedAt.IsZero() || g.mobsKilled != 1 {
		t.Fatalf("%v %s killed %d\n%s", err, rep.State, g.mobsKilled, notesOf(rep))
	}
	if w := rep.Watchdog; w.Warnings != 0 || w.LongestStall >= 120 {
		t.Fatalf("%+v\n%s", w, notesOf(rep))
	}
}

// Remote: a fight at a rest gate of 280 s that wears its mob down never stalls.
func TestRemoteWatchdogGateFightThatWearsDownNeverStalls(t *testing.T) {
	g := newFakeRemote(t)
	g.party[2].HP = 600 // the gate before the pull rests the mage; a pack comes at the party there
	g.killRate = 100.0 / 280
	var m *rmob
	g.onTick = func(g *fakeRemote) {
		if m == nil && g.indexOf("cmd rest") >= 0 {
			m = g.addMob(300, Point{g.lead().X + 3, 0, 0})
			m.pulled, m.pullAt = true, g.now
		}
	}
	g.addMob(301, Point{25, 0, 0})
	plan := remotePlan(Leg{Name: "a", To: Point{20, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{301}, Positions: []Point{{25, 0, 0}}}}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil || rep.State != "done" || m == nil || g.mobsKilled != 2 {
		t.Fatalf("%v %s killed %d\n%s", err, rep.State, g.mobsKilled, notesOf(rep))
	}
	if w := rep.Watchdog; w.Warnings != 0 || w.LongestStall >= 120 {
		t.Fatalf("%+v\n%s", w, notesOf(rep))
	}
}

// Group: a fight at a rest gate of 285 s that wears its mob down never stalls.
func TestDriverWatchdogGateFightThatWearsDownNeverStalls(t *testing.T) {
	g := newFake()
	g.add(201, 1, Point{20, 0, 0})
	m := g.add(99, 99, Point{10, 0, 0})
	m.InCombat, m.Target, m.MaxHealth, m.Health = true, leader, 1000, 1000
	start := g.now
	m.dieAt = start.Add(285 * time.Second)
	g.onTick = func(g *fakeGame) {
		if !m.Dead {
			m.Health = uint32(max(1000-3.5*g.now.Sub(start).Seconds(), 1))
		}
	}
	d := NewDriver(mustPlan(t, restPlan), g, Config{}, g.clock())
	if err := d.Run(t.Context(), 0); err != nil {
		t.Fatalf("%v\n%s", err, notes(d))
	}
	if w := d.Report().Watchdog; w.Warnings != 0 || w.LongestStall >= 120 || !m.Dead {
		t.Fatalf("%+v\n%s", w, notes(d))
	}
}
