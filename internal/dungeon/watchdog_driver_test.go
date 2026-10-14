package dungeon

import (
	"strings"
	"testing"
	"time"
)

// The watchdog in both drivers, on their fake clocks (watchdog_test.go has the watchdog alone).

// stuckRest is a one-leg plan whose rest gate can wait ten minutes, and a party whose mage is hurt and never heals: the gate waits for
// nothing.
func stuckRemote(t *testing.T) (*fakeRemote, *Plan) {
	g := newFakeRemote(t)
	g.party[2].HP = 300
	g.healRate = 0
	return g, remotePlan(Leg{Name: "a", To: Point{20, 0, 0}, Rest: &RestSpec{MaxS: 600}})
}

// Remote: a rest gate that waits for nothing is warned about at 120 s, ended at 240 s (its one recovery), and the run goes on and is done.
func TestRemoteWatchdogEndsAStuckRestGate(t *testing.T) {
	g, plan := stuckRemote(t)
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	w := rep.Watchdog
	if w == nil || w.Warnings < 1 || w.Recoveries != 1 || w.WarnS != 120 || w.RecoverS != 240 || w.FailS != 420 {
		t.Fatalf("%+v", w)
	}
	n := notesOf(rep)
	for _, want := range []string{"stall warning: no progress for 12", "waiting on rest gate", "stall recovery after 24", "ended the rest gate", "rest gate ended by the watchdog after a stall"} {
		if !strings.Contains(n, want) {
			t.Fatalf("missing %q in\n%s", want, n)
		}
	}
	if gate := rep.RestGates[0]; gate.Result != "gave_up" || gate.Seconds < 240 || gate.Seconds > 250 {
		t.Fatalf("%+v", gate)
	}
	// The gate was ended once per stall: the rest orders went out every 45 s and are not progress.
	if rep.Watchdog.StallLevel != "none" && rep.Watchdog.StallLevel != "warn" {
		t.Fatalf("%+v", rep.Watchdog)
	}
}

// Remote: with the recovery off the same stall fails the run at fail_s, with the reason, after the end-of-run handling (unbind).
func TestRemoteWatchdogFailsAStuckRun(t *testing.T) {
	g, plan := stuckRemote(t)
	rep, err := runRemote(t, g, plan, opts(), func(c *Config) { c.Watchdog = WatchdogConfig{RecoverS: -1, FailS: 300} })
	if err == nil || rep.State != "failed" {
		t.Fatalf("%v %s", err, rep.State)
	}
	if !strings.HasPrefix(rep.Error, "stalled: rest gate leg 0 (a): walk to 20.0 0.0 0.0 for 30") || !strings.Contains(rep.Error, ", last progress ") {
		t.Fatalf("%q", rep.Error)
	}
	if g.unbinds != 1 || g.bound || rep.Remote == nil || !rep.Remote.Unbound {
		t.Fatalf("the run is unbound: unbinds %d bound %v %+v", g.unbinds, g.bound, rep.Remote)
	}
	if w := rep.Watchdog; w.StallLevel != "fail" || w.Recoveries != 0 || w.StalledForS < 300 || w.StalledForS > 310 {
		t.Fatalf("%+v", w)
	}
	if !strings.Contains(notesOf(rep), rep.Error) {
		t.Fatalf("the reason is in the notes: %s", notesOf(rep))
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
	if err == nil || !strings.HasPrefix(rep.Error, "stalled: pull leg 0 (a): pull group 1 of 1 for 20") || g.unbinds != 1 {
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

// Group: the stuck gate is warned about, ended at the recovery, and the run is done; each gate of the leg is its own stall.
func TestDriverWatchdogEndsAStuckRestGate(t *testing.T) {
	g := stuckGroup()
	d := runRest(t, g, stuckRestPlan, Config{})
	rep := d.Report()
	if rep.State != "done" || rep.Watchdog == nil || rep.Watchdog.Recoveries != 2 || rep.Watchdog.Warnings < 2 {
		t.Fatalf("%s %+v", rep.State, rep.Watchdog)
	}
	n := notes(d)
	for _, want := range []string{"stall warning: no progress for 12", "stall recovery after 24", "rest gate ended by the watchdog after a stall"} {
		if !strings.Contains(n, want) {
			t.Fatalf("missing %q in\n%s", want, n)
		}
	}
	for _, gate := range rep.RestGates {
		if gate.Result != "gave_up" || gate.Seconds < 240 || gate.Seconds > 250 {
			t.Fatalf("%+v", gate)
		}
	}
}

// Group: the plan's own watchdog levels apply, and the run parameter wins over them; the failure ends the run with the reason.
func TestDriverWatchdogFailsAStuckRun(t *testing.T) {
	plan := strings.Replace(stuckRestPlan, `"legs"`, `"watchdog":{"warn_s":50,"recover_s":-1,"fail_s":150},"legs"`, 1)
	g := stuckGroup()
	d := NewDriver(mustPlan(t, plan), g, Config{}, g.clock())
	err := d.Run(t.Context(), 0)
	rep := d.Report()
	if err == nil || rep.State != "failed" || !strings.HasPrefix(rep.Error, "stalled: rest gate leg 0") || !strings.Contains(rep.Error, " for 15") {
		t.Fatalf("%v %q", err, rep.Error)
	}
	if w := rep.Watchdog; w.WarnS != 50 || w.RecoverS != 0 || w.FailS != 150 || w.StallLevel != "fail" || w.Recoveries != 0 {
		t.Fatalf("%+v", w)
	}

	g = stuckGroup()
	d = NewDriver(mustPlan(t, plan), g, Config{Watchdog: WatchdogConfig{FailS: 100}}, g.clock())
	_ = d.Run(t.Context(), 0)
	if rep := d.Report(); rep.Watchdog.FailS != 100 || !strings.Contains(rep.Error, " for 10") {
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
