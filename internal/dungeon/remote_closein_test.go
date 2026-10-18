package dungeon

import (
	"strings"
	"testing"
	"time"
)

// The walk-in of a pull that drew no fight is an experiment and is off unless PullCloseIn asks for it (the default 0). With it on
// (pull_close_in 12: clearly nearer than the 20-24 yd floor the tank's own walk-in reaches in live runs), the driver walks the lead bot
// in and sends the pull again from there, and that try does not count.
func TestRemotePullCloseInRetriesFromANearerStand(t *testing.T) {
	for _, on := range []bool{true, false} {
		name := "off (default)"
		if on {
			name = "pull_close_in 12"
		}
		t.Run(name, func(t *testing.T) {
			g := newFakeRemote(t)
			m := g.addMob(299, Point{40, 0, 0})
			m.pullRange = 22 // the module's own walk-in stops at a floor about 22 yd out (wc-live-3 A3: 20.0-23.7 yd)
			var at []float64 // how far the lead bot stood from the mob when each pull went out
			g.onTick = func(g *fakeRemote) {
				for len(at) < len(g.ordersOf("cmd pullback")) {
					at = append(at, g.leadDist(m))
				}
			}
			var mod func(*Config)
			if on {
				mod = func(c *Config) { c.PullCloseIn = 12 }
			}
			rep, err := runRemote(t, g, pullPlan(), opts(), mod)
			if !on {
				if err == nil || !strings.Contains(err.Error(), "3 pulls of [299] failed") {
					t.Fatalf("with the walk-in off: %v", err)
				}
				if pulls := g.ordersOf("cmd pullback"); len(pulls) != 3 || rep.Remote.PullCloseIns != 0 {
					t.Fatalf("with the walk-in off: %d pullbacks, %d walk-ins", len(pulls), rep.Remote.PullCloseIns)
				}
				return
			}
			if err != nil || len(g.mobs) != 0 || g.mobsKilled != 1 {
				t.Fatalf("%v, mobs %d, killed %d\n%s", err, len(g.mobs), g.mobsKilled, notesOf(rep))
			}
			if pulls := g.ordersOf("cmd pullback"); len(pulls) != 2 || rep.Remote.PullCloseIns != 1 {
				t.Fatalf("%d pullbacks, %d walk-ins: %v", len(pulls), rep.Remote.PullCloseIns, pulls)
			}
			if rep.Legs[0].Pulls != 2 || rep.Legs[0].PullRetries != 1 {
				t.Fatalf("the leg counted %+v", rep.Legs[0])
			}
			if len(at) != 2 || at[0] < 35 || at[1] > 17 {
				t.Fatalf("the lead bot stood %v yd from the mob at the pulls: the second must go out from the nearer stand", at)
			}
		})
	}
}

// One walk-in per mob, not one per failed try: the tank runs back to its hold spot after an abandoned pull (the module's return, which
// cmd follow does here), so without the per-mob budget the same mob would be walked in on again at every later try. The mob never comes
// (inert) and the group still runs out of tries.
func TestRemotePullCloseInIsOneWalkInPerMob(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{40, 0, 0}).inert = true
	// `cmd follow` runs the lead bot back to this hold spot. followAt is set to the fake's start time so the fake's run-back-to-the-mob
	// (a pull 20 s after a follow) stays off: what stalls here is the module's own walk-in, not the lead bot's walk.
	g.followTo, g.followAt = &Point{0, 0, 0}, g.now
	rep, err := runRemote(t, g, pullPlan(), opts(), func(c *Config) { c.PullCloseIn = 12 })
	if err == nil || !strings.Contains(err.Error(), "3 pulls of [299] failed") {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if pulls := g.ordersOf("cmd pullback"); len(pulls) > 4 || rep.Remote.PullCloseIns != 1 {
		t.Fatalf("%d pullbacks, %d walk-ins: the walk-in is one per mob, not one per try", len(pulls), rep.Remote.PullCloseIns)
	}
}

// The budget is per mob, not per group: a group of several mobs gives each of them its own walk-in, so the second mob's stalled pull is
// walked in on too.
func TestRemotePullCloseInGivesEachMobOfTheGroupItsOwnWalkIn(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{40, 0, 0}).pullRange = 22
	g.addMob(299, Point{40, 60, 0}).pullRange = 22
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{40, 0, 0}, {40, 60, 0}}}}})
	rep, err := runRemote(t, g, plan, opts(), func(c *Config) { c.PullCloseIn = 12 })
	if err != nil || g.mobsKilled != 2 || len(g.mobs) != 0 {
		t.Fatalf("%v, killed %d, mobs %d\n%s", err, g.mobsKilled, len(g.mobs), notesOf(rep))
	}
	if rep.Remote.PullCloseIns != 2 {
		t.Fatalf("%d walk-ins, want one per mob: %v\n%s", rep.Remote.PullCloseIns, g.ordersOf("cmd pullback"), notesOf(rep))
	}
}

// A mob that is not where its pull was made gets no walk-in: the walk would be about a spot its pull was not.
func TestRemotePullCloseInSkipsAMobThatMoved(t *testing.T) {
	g := newFakeRemote(t)
	m := g.addMob(299, Point{40, 0, 0})
	m.pullRange = 22
	seen := 0
	g.onTick = func(g *fakeRemote) {
		if n := len(g.ordersOf("cmd pullback")); n != seen { // the mob steps aside after every pull order
			if seen++; seen%2 == 1 {
				m.pos.Y = -8
			} else {
				m.pos.Y = 8
			}
		}
	}
	rep, err := runRemote(t, g, pullPlan(), opts(), func(c *Config) { c.PullCloseIn = 12 })
	if err == nil || !strings.Contains(err.Error(), "3 pulls of [299] failed") {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if pulls := g.ordersOf("cmd pullback"); len(pulls) != 3 || rep.Remote.PullCloseIns != 0 {
		t.Fatalf("%d pullbacks, %d walk-ins: a mob that moved must not be walked in on", len(pulls), rep.Remote.PullCloseIns)
	}
}

// A recovery walk must not take the party into a second pack: a hostile the group is not pulling, standing near where the walk-in would
// stand, keeps the pull at its own stand and the try counts as a failed one.
func TestRemotePullCloseInSkipsAnUnsafeStand(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{40, 0, 0}).pullRange = 22
	g.addMob(300, Point{28, 8, 0}) // another entry: not a candidate of this group, 8 yd from the stand (28 0 0)
	rep, err := runRemote(t, g, pullPlan(), opts(), func(c *Config) { c.PullCloseIn = 12 })
	if err == nil || !strings.Contains(err.Error(), "3 pulls of [299] failed") {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if pulls := g.ordersOf("cmd pullback"); len(pulls) != 3 || rep.Remote.PullCloseIns != 0 {
		t.Fatalf("%d pullbacks, %d walk-ins: the stand was not clear", len(pulls), rep.Remote.PullCloseIns)
	}
}

// The tunable's 0 round-trips as off (no hidden sign in the value) and off is the default, in DefaultConfig and in a Config that leaves
// it out.
func TestPullCloseInTuneRoundTripsAndDefaultsOff(t *testing.T) {
	s := tuneIndex["pull_close_in"]
	tg := &tuneTarget{cfg: new(Config), opt: new(RemoteOptions), x: new(tuneExtra)}
	for _, v := range []float64{0, 5, 70} {
		if err := s.check(v); err != nil {
			t.Fatalf("pull_close_in %v: %v", v, err)
		}
		s.set(tg, v)
		if got := s.get(tg); got != v {
			t.Fatalf("pull_close_in set %v, read back %v", v, got)
		}
	}
	if def := TuneDefaults()["pull_close_in"]; def != 0 {
		t.Fatalf("the tunable default is %v, want 0 (off)", def)
	}
	if c := DefaultConfig(); c.PullCloseIn != 0 {
		t.Fatalf("DefaultConfig.PullCloseIn is %v, want 0 (off)", c.PullCloseIn)
	}
	if c := (Config{}).withDefaults(); c.PullCloseIn != 0 {
		t.Fatalf("a Config that leaves the close-in out has %v, want 0 (off)", c.PullCloseIn)
	}
}

// A close-in stand is never farther out than the stand the party already waits at (a value above it would walk the party away from the
// mob), and a value that is off stays off.
func TestPullCloseInTrimsToTheStandoff(t *testing.T) {
	standoff := DefaultConfig().PullStandoff
	if c := (Config{PullCloseIn: 60}).withDefaults(); c.PullCloseIn != standoff {
		t.Fatalf("PullCloseIn 60 with the default standoff %v became %v", standoff, c.PullCloseIn)
	}
	if c := (Config{PullCloseIn: 12}).withDefaults(); c.PullCloseIn != 12 {
		t.Fatalf("PullCloseIn 12 became %v", c.PullCloseIn)
	}
	if c := (Config{PullCloseIn: -1}).withDefaults(); c.PullCloseIn != -1 {
		t.Fatalf("PullCloseIn -1 (off) became %v", c.PullCloseIn)
	}
}

// closeInCase is what closeInWalkTest returns: the run's report and error, the pull orders, the walk attempts and every move order (for a
// failure message).
type closeInCase struct {
	rep   Report
	err   error
	pulls int
	walks int
	moves []string
}

// closeInWalkTest is the A3-shaped case for the checks around the walk: an inert mob 25 yd from the party (the pull draws no fight however
// near the party walks; the walk to its stand is 13 yd, one move), the walk-in at 12 yd, and a change injected as the walk goes out (start) and taken back 5 s later (clear), which
// is after the checks that follow the walk and before the next try. tick runs on every tick for a change that must land at a chosen
// moment (nil: none). A walk attempt is a `cmd move` whose destination is within 20 yd of the mob's spot (the stand is 12 yd from it), so
// the gather and fight holds, which move the lead bot where it already stands, are not counted.
func closeInWalkTest(t *testing.T, start, clear, tick func(*fakeRemote, *rmob)) closeInCase {
	t.Helper()
	g := newFakeRemote(t)
	m := g.addMob(299, Point{25, 0, 0})
	m.inert = true
	home := Point{25, 0, 0}
	walked := 0
	var clearAt time.Time
	g.onTick = func(g *fakeRemote) {
		n := 0
		for _, at := range g.moveOrderDest {
			if dist3(at, home) <= 20 {
				n++
			}
		}
		if n > walked {
			walked = n
			start(g, m)
			if clear != nil {
				clearAt = g.now.Add(5 * time.Second)
			}
		}
		if !clearAt.IsZero() && !g.now.Before(clearAt) {
			clearAt = time.Time{}
			clear(g, m)
		}
		if tick != nil {
			tick(g, m)
		}
	}
	rep, err := runRemote(t, g, pullPlan(), opts(), func(c *Config) { c.PullCloseIn = 12 })
	return closeInCase{rep: rep, err: err, pulls: len(g.ordersOf("cmd pullback")), walks: walked, moves: g.ordersOf("cmd move")}
}

// failed says the group failed the way it does when the walk-in did not retry the pull, and names the counts when it did not.
func (c closeInCase) failed(t *testing.T, what string) {
	t.Helper()
	if c.err == nil || !strings.Contains(c.err.Error(), "3 pulls of [299] failed") {
		t.Fatalf("%s: %v\n%s", what, c.err, notesOf(c.rep))
	}
	if c.walks != 1 || c.pulls != 3 || c.rep.Remote.PullCloseIns != 1 {
		t.Fatalf("%s: %d walk attempts, %d pull orders, %d counted walk-ins, moves %v", what, c.walks, c.pulls, c.rep.Remote.PullCloseIns, c.moves)
	}
}

// The mob's one walk-in is spent when the party walks in, not only when the pull is sent again from there: a walk that ran and whose own
// checks then rejected the retry must not be walked again at the next try. The mob keeps stepping aside as the party walks in, so every
// walk is made and every retry is rejected.
func TestRemotePullCloseInChargesTheWalkBeforeItRuns(t *testing.T) {
	aside := func(g *fakeRemote, m *rmob) {
		if m.pos.Y == 0 {
			m.pos.Y = 8
		} else {
			m.pos.Y = -m.pos.Y
		}
	}
	c := closeInWalkTest(t, aside, nil, nil)
	if c.err == nil || !strings.Contains(c.err.Error(), "3 pulls of [299] failed") {
		t.Fatalf("%v\n%s", c.err, notesOf(c.rep))
	}
	if c.walks != 1 || c.rep.Remote.PullCloseIns != 1 {
		t.Fatalf("%d walk attempts, %d counted walk-ins, moves %v: the walk is charged when it is made, not when it is retried", c.walks, c.rep.Remote.PullCloseIns, c.moves)
	}
	if c.pulls != 3 {
		t.Fatalf("%d pull orders, moves %v: a rejected walk-in must not be sent again", c.pulls, c.moves)
	}
}

// A mob that starts walking home while the party walks in is not pulled again from the nearer stand: the walk ran, the pull counts.
func TestRemotePullCloseInSkipsATargetThatStartsEvading(t *testing.T) {
	c := closeInWalkTest(t,
		func(g *fakeRemote, m *rmob) { m.flags = "e" },
		func(g *fakeRemote, m *rmob) { m.flags = "-" }, nil)
	c.failed(t, "an evading mob is not pulled again")
}

// A mob that joins a fight just as the party arrives at the nearer stand is not pulled again from it. The fight lands with the party
// already there (a tick past the last hop), so the walk is not stopped by its own walk-aggro rule and the check under test decides.
func TestRemotePullCloseInSkipsATargetThatJoinsAFight(t *testing.T) {
	joined := false
	c := closeInWalkTest(t,
		func(g *fakeRemote, m *rmob) {},
		func(g *fakeRemote, m *rmob) { m.engaged, m.target = false, 0 },
		func(g *fakeRemote, m *rmob) {
			if !joined && g.leadDist(m) <= 16 { // the lead bot has reached the stand (12 yd from the mob, arriving within move_arrive)
				joined = true
				m.pulled, m.pullAt, m.engaged, m.target = true, g.now, true, gTank
			}
		})
	if !joined {
		t.Fatalf("the mob never joined a fight: %v\n%s", c.err, notesOf(c.rep))
	}
	// The retry is not sent from the nearer stand: the walk ran once and the pull counts. The mob is in a fight now, so the ordinary
	// machinery sends the party at it and it dies (the group is cleared, the run does not fail): this check is read from the note and the
	// counts, not from the run's outcome.
	if c.walks != 1 || c.rep.Remote.PullCloseIns != 1 || !strings.Contains(notesOf(c.rep), "from a nearer stand: it is already fighting the party") {
		t.Fatalf("%d walk attempts, %d counted walk-ins, moves %v: a mob already in a fight is not pulled again\n%s",
			c.walks, c.rep.Remote.PullCloseIns, c.moves, notesOf(c.rep))
	}
}

// A mob that moves away while the party walks in (8 yd, more than closeInMovedYd) is not pulled again. It steps aside across the party's
// line (not away from it), so the walk still gains ground and the moved check is what stops the retry.
func TestRemotePullCloseInSkipsATargetDisplacedDuringTheWalk(t *testing.T) {
	c := closeInWalkTest(t,
		func(g *fakeRemote, m *rmob) {
			if m.pos.Y == 0 {
				m.pos.Y = 8
			}
		}, nil, nil)
	c.failed(t, "a mob that moved during the walk is not pulled again")
}

// A hostile that comes into the stand's area while the party walks in keeps the pull at its own stand. It is flagged as walking home so
// the careful walk does not pull it ahead of the party (any living hostile the group is not pulling counts, whatever it is doing).
func TestRemotePullCloseInSkipsAStandTakenWhileWalking(t *testing.T) {
	var other *rmob
	c := closeInWalkTest(t,
		func(g *fakeRemote, m *rmob) {
			other = g.addMob(300, Point{13, 8, 0}) // it comes into the stand's area (the stand is 13 0 0)
			other.flags = "e"
		}, nil, nil)
	if other == nil {
		t.Fatalf("the second hostile was never added: %v\n%s", c.err, notesOf(c.rep))
	}
	c.failed(t, "a hostile in the stand's area stops the retry")
}
