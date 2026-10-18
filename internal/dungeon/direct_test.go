package dungeon

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/tbm"
)

// A leg with a via list: a plain waypoint, then a direct step to the leg's `to`.
const viaPlan = `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","via":[{"x":40,"y":0,"z":0},{"x":55,"y":0,"z":4,"direct":true}],"to":{"x":55,"y":0,"z":4},
  "pull_groups":[{"entries":[1],"positions":[{"x":60,"y":0,"z":4}]}]}]}`

// viaPlanExact is viaPlan with the launch point marked exact.
const viaPlanExact = `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","via":[{"x":40,"y":0,"z":0,"exact":true},{"x":55,"y":0,"z":4,"direct":true}],"to":{"x":55,"y":0,"z":4},
  "pull_groups":[{"entries":[1],"positions":[{"x":60,"y":0,"z":4}]}]}]}`

func TestParseViaAndDirect(t *testing.T) {
	p := mustPlan(t, viaPlan)
	l := p.Legs[0]
	if len(l.Via) != 2 || l.Via[0].Direct || !l.Via[1].Direct || l.Via[1].Point != (Point{55, 0, 4}) {
		t.Fatalf("%+v", l.Via)
	}
	out, err := Marshal(p)
	if err != nil || !strings.Contains(string(out), `"direct": true`) {
		t.Fatalf("%v %s", err, out)
	}
	if again, err := Parse("t.json", out); err != nil || len(again.Legs[0].Via) != 2 || !again.Legs[0].Via[1].Direct {
		t.Fatalf("the plan does not survive Marshal: %v", err)
	}
}

func TestParseDirectLimits(t *testing.T) {
	plan := func(via string) string {
		return `{"version":1,"name":"t","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[{"to":{"x":0,"y":0,"z":0},"via":[` + via + `],"boss":{"entry":1,"position":{"x":0,"y":0,"z":0}}}]}`
	}
	for name, c := range map[string]struct{ via, want string }{
		"first via direct": {`{"x":1,"y":0,"z":0,"direct":true}`, "needs a via point before it"},
		"too far":          {`{"x":0,"y":0,"z":0},{"x":25.5,"y":0,"z":0,"direct":true}`, "limits are 25 and 8"},
		"too high":         {`{"x":0,"y":0,"z":0},{"x":5,"y":0,"z":8.5,"direct":true}`, "limits are 25 and 8"},
		"too deep":         {`{"x":0,"y":0,"z":0},{"x":5,"y":0,"z":-9,"direct":true}`, "limits are 25 and 8"},
		"unknown field":    {`{"x":0,"y":0,"z":0,"straight":true}`, "unknown field"},
		"no z":             {`{"x":0,"y":0}`, "via[0].z"},
	} {
		if _, err := Parse("t.json", []byte(plan(c.via))); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// On the limits it is fine, up or down, and a plain far waypoint is not a direct step.
	for _, via := range []string{`{"x":0,"y":0,"z":0},{"x":25,"y":0,"z":8,"direct":true}`, `{"x":0,"y":0,"z":0},{"x":0,"y":-25,"z":-8,"direct":true}`, `{"x":0,"y":0,"z":0},{"x":300,"y":0,"z":0}`} {
		if _, err := Parse("t.json", []byte(plan(via))); err != nil {
			t.Errorf("%s: %v", via, err)
		}
	}
}

// The stepper walks the via points in order, the direct one marked, then goes on to the pulls; a via point the player stands on is passed.
func TestStepperWalksViaPoints(t *testing.T) {
	w := &fakeWorld{}
	s := NewStepper(mustPlan(t, viaPlan), w, (&fakeClock{t: time.Unix(1000, 0)}).now)
	a := s.Next()
	if a.Kind != ActWalk || a.Direct || a.To != (Point{40, 0, 0}) {
		t.Fatalf("%+v", a)
	}
	if b := s.Next(); b.To != a.To {
		t.Fatalf("Next repeats its action until it is completed: %+v", b)
	}
	w.pos = Point{40, 0, 0}
	s.Complete()
	a = s.Next()
	if a.Kind != ActWalk || !a.Direct || a.To != (Point{55, 0, 4}) || !strings.Contains(a.Label, "direct step") {
		t.Fatalf("%+v", a)
	}
	w.pos = Point{55, 0, 4}
	s.Complete()
	// `to` is the point the direct step ended on: no walk to it, straight on to the pull.
	if a = s.Next(); a.Kind != ActPull {
		t.Fatalf("%+v", a)
	}

	// Standing on the first via point already: it is passed without a walk; the direct step is still taken.
	w = &fakeWorld{pos: Point{40, 0, 0}}
	s = NewStepper(mustPlan(t, viaPlan), w, (&fakeClock{t: time.Unix(1000, 0)}).now)
	if a = s.Next(); a.Kind != ActWalk || !a.Direct {
		t.Fatalf("%+v", a)
	}
}

// A resumed run that stands past the crossing skips the via points with the walk.
func TestStepperResumePastTheViaPoints(t *testing.T) {
	w := &fakeWorld{pos: Point{58, 0, 4}}
	s := NewStepper(mustPlan(t, viaPlan), w, (&fakeClock{t: time.Unix(1000, 0)}).now)
	if _, ok := s.ResumeAt(); !ok {
		t.Fatal("a leader past `to` resumes in the pulls")
	}
	if a := s.Next(); a.Kind != ActPull {
		t.Fatalf("%+v", a)
	}
}

func viaRemotePlan() *Plan {
	return remotePlan(Leg{Name: "gap", Via: []Waypoint{{Point: Point{40, 0, 0}}, {Point: Point{55, 0, 4}, Direct: true}}, To: Point{55, 0, 4},
		Steps: []Step{{WaitFor: &WaitFor{HostilesGone: &HostilesGone{Radius: 30}, TimeoutS: 5}}}})
}

// The remote driver walks to the lip with plain moves and sends the crossing as `cmd move x y z direct`.
func TestRemoteDirectStepIsSentWithTheFlag(t *testing.T) {
	g := newFakeRemote(t)
	rep, err := runRemote(t, g, viaRemotePlan(), opts(), nil)
	if err != nil {
		t.Fatalf("%v %+v", err, rep.Notes)
	}
	moves := g.ordersOf("cmd move")
	if len(moves) < 2 || moves[len(moves)-1] != "cmd move 55.00 0.00 4.00 direct" {
		t.Fatalf("moves %v", moves)
	}
	for _, m := range moves[:len(moves)-1] {
		if strings.HasSuffix(m, "direct") {
			t.Fatalf("a plain walk sent the flag: %v", moves)
		}
	}
	if math.Abs(float64(g.lead().X-55)) > 2 {
		t.Fatalf("the lead bot is at %v", g.lead().X)
	}
}

// A follower still catching up makes the module answer too-far; the step is sent again (not as a shorter plain leg).
func TestRemoteDirectStepRetriesTooFar(t *testing.T) {
	g := newFakeRemote(t)
	g.directErrs = []string{"too-far", "too-far"}
	rep, err := runRemote(t, g, viaRemotePlan(), opts(), nil)
	if err != nil {
		t.Fatalf("%v %+v", err, rep.Notes)
	}
	var direct int
	for _, m := range g.ordersOf("cmd move") {
		if strings.HasSuffix(m, "direct") {
			direct++
		}
	}
	if direct != 3 || rep.Remote.Refused["move:too-far"] != 2 {
		t.Fatalf("direct moves %d, refused %v", direct, rep.Remote.Refused)
	}
}

// A server without the flag, or a point with no ground: the walk fails at once with the reason, and sends no plain move in its place.
func TestRemoteDirectStepFailsClearly(t *testing.T) {
	g := newFakeRemote(t)
	g.noDirect = true
	_, err := runRemote(t, g, viaRemotePlan(), opts(), nil)
	if err == nil || !strings.Contains(err.Error(), "direct step to 55.0 0.0 4.0") || !strings.Contains(err.Error(), "needs a TortoiseBots with the direct flag") {
		t.Fatalf("%v", err)
	}
	if n := len(g.ordersOf("cmd move 55.00 0.00 4.00")); n != 1 {
		t.Fatalf("the point was ordered %d times: %v", n, g.ordersOf("cmd move"))
	}

	for _, code := range []string{"no-ground", "no-los"} {
		g = newFakeRemote(t)
		g.directErrs = []string{code}
		_, err = runRemote(t, g, viaRemotePlan(), opts(), nil)
		if err == nil || !strings.Contains(err.Error(), code) {
			t.Fatalf("%s: %v", code, err)
		}
		if n := len(g.ordersOf("cmd move 55.00 0.00 4.00")); n != 1 {
			t.Fatalf("a %s refusal was retried: %v", code, g.ordersOf("cmd move"))
		}
	}
}

// holdHere sends no move to the lead bot's own position while the bot stands within 3 yd of the point it was last sent to: the stay
// spot is already here, and the server cannot path to where a bot stands (`unreachable`).
func TestRemoteHoldSkippedWhereTheStaySpotIs(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{30, 0, 0})
	plan := remotePlan(Leg{Name: "a", To: Point{10, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{30, 0, 0}}}}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil || len(g.killTimes) != 1 || rep.Remote.FightHolds != 0 {
		t.Fatalf("%v kills %v %+v", err, g.killTimes, rep.Remote)
	}
	// Every move that went out was to a point other than where the lead bot stood when it was sent (a hold would be to its own position).
	for i, dest := range g.moveOrderDest {
		if dist3(dest, g.moveOrderPos[i]) < 0.5 {
			t.Fatalf("move %d went to the lead bot's own position %v", i, dest)
		}
	}
}

func TestRemoteHoldStillSentWhenTheLeadBotWasCarriedAway(t *testing.T) {
	g := newFakeRemote(t)
	g.returnToStay, g.returnDelay = true, 6*time.Second
	m := g.addMob(299, Point{100, 0, 0})
	g.onTick = func(g *fakeRemote) {
		if m.engaged && g.lead().X < 85 {
			g.lead().X += 5
		}
	}
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{100, 0, 0}}}}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil || rep.Remote.FightHolds < 1 {
		t.Fatalf("%v %+v", err, rep.Remote)
	}
}

// A direct hop shorter than the usual 5 yd "arrived" distance is still sent (live, uldaman hires-2: the lip was 3.97 yd from the first
// hop under the overhang; the walk counted it as reached and sent only the long hop, which the module refused no-los).
func TestRemoteShortDirectHopIsStillSent(t *testing.T) {
	g := newFakeRemote(t)
	plan := remotePlan(Leg{Name: "gap", Via: []Waypoint{{Point: Point{40, 0, 0}}, {Point: Point{43.5, 0, 1}, Direct: true}, {Point: Point{55, 0, 4}, Direct: true}}, To: Point{55, 0, 4},
		Steps: []Step{{WaitFor: &WaitFor{HostilesGone: &HostilesGone{Radius: 30}, TimeoutS: 5}}}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil {
		t.Fatalf("%v %+v", err, rep.Notes)
	}
	var direct []string
	for _, m := range g.ordersOf("cmd move") {
		if strings.HasSuffix(m, "direct") {
			direct = append(direct, m)
		}
	}
	if len(direct) != 2 || direct[0] != "cmd move 43.50 0.00 1.00 direct" || direct[1] != "cmd move 55.00 0.00 4.00 direct" {
		t.Fatalf("direct steps sent: %v", direct)
	}
}

// The loader takes "exact" only on a plain waypoint whose next via is a real direct step: every other use is a plan error (a point a GM
// crosses, the direct step itself, a last via, a hop that no direct step follows).
func TestParseExactWaypoint(t *testing.T) {
	plan := func(via string) string {
		return `{"version":1,"name":"t","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[{"to":{"x":0,"y":0,"z":0},"via":[` + via + `],"boss":{"entry":1,"position":{"x":0,"y":0,"z":0}}}]}`
	}
	for name, c := range map[string]struct{ via, want string }{
		"on the direct step":  {`{"x":0,"y":0,"z":0},{"x":5,"y":0,"z":0,"direct":true,"exact":true}`, "not for the direct step itself"},
		"on the last via":     {`{"x":0,"y":0,"z":0,"exact":true}`, "the last via of the leg"},
		"no direct step next": {`{"x":0,"y":0,"z":0,"exact":true},{"x":5,"y":0,"z":0}`, "via[1] is not a direct step"},
		"before a gm_cross":   {`{"x":0,"y":0,"z":0,"exact":true},{"x":5,"y":0,"z":0,"direct":true,"gm_cross":true}`, "is a gm_cross point"},
		"on the gm_cross":     {`{"x":0,"y":0,"z":0},{"x":5,"y":0,"z":0,"direct":true,"gm_cross":true,"exact":true}`, "reached by the GM move"},
	} {
		if _, err := Parse("t.json", []byte(plan(c.via))); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// On the launch point of a direct step it is fine, and it survives Marshal.
	p := mustPlan(t, viaPlanExact)
	if !p.Legs[0].Via[0].Exact || p.Legs[0].Via[1].Exact {
		t.Fatalf("%+v", p.Legs[0].Via)
	}
	out, err := Marshal(p)
	if err != nil || !strings.Contains(string(out), `"exact": true`) {
		t.Fatalf("%v %s", err, out)
	}
	if again, err := Parse("t.json", out); err != nil || !again.Legs[0].Via[0].Exact {
		t.Fatalf("the plan does not survive Marshal: %v", err)
	}
}

// stopShortOf hangs a hook on the fake that makes the lead bot's move to p stop stopYd short of it, once: the walk's own arrival (MoveArrive,
// 5 yd) then counts the point as reached while the lead bot stands there and does not move on (live, wc-live-3-a3-r6: `cmd move -293.30
// 0.50 -58.00` ACKed, walk ended 1 s later with the lead bot 2.4 yd from the foot, and it stayed). It reports whether the bot was stopped.
func stopShortOf(g *fakeRemote, p Point, stopYd float32) func() bool {
	stopped := false
	g.onTick = func(g *fakeRemote) {
		l := g.lead()
		if stopped || dist3(Point{l.X, l.Y, l.Z}, p) > float64(stopYd)+1.5 {
			return
		}
		l.X, l.Y, l.Z = p.X-stopYd, p.Y, p.Z
		g.moveTo, stopped = nil, true
	}
	return func() bool { return stopped }
}

// holdShortOf is stopShortOf for a lead bot that comes no nearer however often it is sent: the hook puts it back stopYd short of p every time
// its move to p gets closer (a point the module will not or cannot walk to). A move to any other point is left to run, so the direct step that
// follows the launch point lands as it does live.
func holdShortOf(g *fakeRemote, p Point, stopYd float32) {
	g.onTick = func(g *fakeRemote) {
		if g.moveTo == nil || *g.moveTo != p {
			return
		}
		l := g.lead()
		if dist3(Point{l.X, l.Y, l.Z}, p) > float64(stopYd)+1.5 {
			return
		}
		l.X, l.Y, l.Z = p.X-stopYd, p.Y, p.Z
		g.moveTo = nil
	}
}

// exactViaPlan is viaRemotePlan with the launch point marked exact: the remote driver must bring the lead bot onto it (within directArrive) and
// prove it stands there before the direct step goes out.
func exactViaPlan() *Plan {
	return remotePlan(Leg{Name: "gap", Via: []Waypoint{{Point: Point{40, 0, 0}, Exact: true}, {Point: Point{55, 0, 4}, Direct: true}}, To: Point{55, 0, 4},
		Steps: []Step{{WaitFor: &WaitFor{HostilesGone: &HostilesGone{Radius: 30}, TimeoutS: 5}}}})
}

// rampPlan is a launch point (exact when exact is set) and a direct step from it, for the ramp and cap tests.
func rampPlan(launch, landing Point, exact bool) *Plan {
	return remotePlan(Leg{Name: "gap", Via: []Waypoint{{Point: launch, Exact: exact}, {Point: landing, Direct: true}}, To: landing,
		Steps: []Step{{WaitFor: &WaitFor{HostilesGone: &HostilesGone{Radius: 30}, TimeoutS: 5}}}})
}

// The waypoint an exact plan marks must be reached tightly: the module sends the direct step only from the lead bot's own place (it checks the
// line's sight from there: the WC serpentis route's run B was refused no-los with the lead bot 4 yd short of the foot), it carries out no plain
// move shorter than MinMove (6 yd, on the ground plane), so a lead bot left short by the walk is walked back along the path it came and then
// onto the point, and the step goes out only once the lead bot has been seen standing on it. Tested at the 2.4, 4 and 5 yd short the live runs
// left it (minMove on, as the module has it).
func TestRemoteExactWaypointIsWalkedTightlyBeforeADirectStep(t *testing.T) {
	launch := Point{40, 0, 0}
	for _, short := range []float32{2.4, 4, 5} {
		t.Run(fmt.Sprintf("%.1f yd short", short), func(t *testing.T) {
			g := newFakeRemote(t)
			stopped := stopShortOf(g, launch, short)
			g.minMove = MinMove
			rep, err := runRemote(t, g, exactViaPlan(), opts(), nil)
			if err != nil {
				t.Fatalf("%v %+v", err, rep.Notes)
			}
			if !stopped() {
				t.Fatal("the lead bot never came near the launch point: the test proves nothing")
			}
			// The walk left the lead bot short and the tighten walked it back along the path it came (the walk's own hop to 25, then the
			// tighten's) and then onto the point (the walk's own move, then the tighten's).
			if n := len(g.ordersOf("cmd move 25.00 0.00 0.00")); n != 2 {
				t.Fatalf("moves to the path point: %d: %v", n, g.ordersOf("cmd move"))
			}
			if n := len(g.ordersOf("cmd move 40.00 0.00 0.00")); n != 2 {
				t.Fatalf("moves to the launch point: %d: %v", n, g.ordersOf("cmd move"))
			}
			if notes := strings.Join(rep.Notes, " | "); !strings.Contains(notes, "it is walked back to 25.0 0.0 0.0 on the path it came, then onto the point") {
				t.Fatalf("the tighten did not note the walk back: %v", rep.Notes)
			}
			// Every order to the direct step's point went out with the lead bot within directArrive yards of the launch point.
			sent := 0
			for i, dest := range g.moveOrderDest {
				if dest != (Point{55, 0, 4}) {
					continue
				}
				sent++
				if d := dist3(g.moveOrderPos[i], launch); d > directArrive {
					t.Fatalf("the direct order went out with the lead bot %.1f yd from the launch point (%v): %v", d, g.moveOrderPos[i], g.ordersOf("cmd move"))
				}
			}
			if sent != 1 {
				t.Fatalf("direct orders to the point: %d: %v", sent, g.ordersOf("cmd move"))
			}
			if mv := g.ordersOf("cmd move"); mv[len(mv)-1] != "cmd move 55.00 0.00 4.00 direct" {
				t.Fatalf("moves %v", mv)
			}
		})
	}
}

// No order goes out over a lead bot that is still walking in: the walk ends within MoveArrive yards, so the last stretch is often still being
// walked when the tighten starts (the fake lead bot runs 10 yd a tick, and the walk's arrival counts at 5 yd out). Here it walks the launch
// point itself: nothing is sent again, the lead bot is seen standing on it for the proof, and the step goes out from the point.
func TestRemoteExactWaypointIsNotOrderedOverALeadBotStillWalking(t *testing.T) {
	g := newFakeRemote(t)
	launch := Point{40, 0, 0}
	g.speed = 2 // 2 yd a tick: the walk's arrival counts at 5 yd out while the bot is still walking the last stretch
	g.minMove = MinMove
	rep, err := runRemote(t, g, exactViaPlan(), opts(), nil)
	if err != nil {
		t.Fatalf("%v %+v", err, rep.Notes)
	}
	if n := len(g.ordersOf("cmd move 25.00 0.00 0.00")); n != 1 {
		t.Fatalf("the walk's own path point was ordered %d times: %v", n, g.ordersOf("cmd move"))
	}
	if n := len(g.ordersOf("cmd move 40.00 0.00 0.00")); n != 1 {
		t.Fatalf("the launch point was ordered %d times: %v", n, g.ordersOf("cmd move"))
	}
	sent := 0
	for i, dest := range g.moveOrderDest {
		if dest != (Point{55, 0, 4}) {
			continue
		}
		sent++
		if d := dist3(g.moveOrderPos[i], launch); d > directArrive {
			t.Fatalf("the direct order went out with the lead bot %.1f yd from the launch point: %v", d, g.ordersOf("cmd move"))
		}
	}
	if sent != 1 {
		t.Fatalf("direct orders to the point: %d: %v", sent, g.ordersOf("cmd move"))
	}
}

// An exact waypoint is always visited, even when the leader already stands within directArrive of it: the tighten is the only place that wants
// the proof (fresh frames, 2 s of standing at one anchor) before the direct step after it goes out. So a lead bot within 1.5 yd that keeps
// moving, that is seen only in a frozen frame, or that the feed does not list at all fails the step with no direct order — and one that stands
// still in fresh frames sends no move order at all and lets the step out only after the proof.
func TestRemoteExactWaypointStoodOnIsProvenBeforeTheStep(t *testing.T) {
	launch, landing := Point{40, 0, 0}, Point{42.8, 0, 3.2}
	for _, pos := range []struct {
		name string
		off  float32
	}{{"1 yd off", 1}, {"on the point", 0}} {
		for _, resume := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/resume=%v/moving", pos.name, resume), func(t *testing.T) {
				g := newFakeRemote(t)
				g.lead().X = launch.X - pos.off
				g.onTick = func(g *fakeRemote) {
					// It moves 1.4 yd a poll around the launch point and stays inside directArrive.
					if l := g.lead(); l.X > launch.X {
						l.X = launch.X - 0.7
					} else {
						l.X = launch.X + 0.7
					}
				}
				rep, err := runRemote(t, g, rampPlan(launch, landing, true), opts(), func(c *Config) { c.Resume = resume })
				if err == nil || !strings.Contains(err.Error(), "launch waypoint 40.0 0.0 0.0 of the next direct step:") || !strings.Contains(err.Error(), "the lead bot was still moving after") {
					t.Fatalf("%v %+v", err, rep.Notes)
				}
				if n := len(g.ordersOf("cmd move 42.80 0.00 3.20 direct")); n != 0 {
					t.Fatalf("a direct order went out on a moving lead bot: %v", g.ordersOf("cmd move"))
				}
			})
			t.Run(fmt.Sprintf("%s/resume=%v/stale feed", pos.name, resume), func(t *testing.T) {
				g := newFakeRemote(t)
				g.lead().X = launch.X - pos.off
				g.onTick = func(g *fakeRemote) {
					if g.cutAt.IsZero() && g.n >= 1 { // the first frame is out (the bind reads it): the frames stop
						g.cutAt = g.now
					}
				}
				rep, err := runRemote(t, g, rampPlan(launch, landing, true), opts(), func(c *Config) { c.Resume = resume })
				if err == nil || !strings.Contains(err.Error(), "launch waypoint 40.0 0.0 0.0 of the next direct step:") || !strings.Contains(err.Error(), "the feed went quiet after") {
					t.Fatalf("%v %+v", err, rep.Notes)
				}
				if n := len(g.ordersOf("cmd move 42.80 0.00 3.20 direct")); n != 0 {
					t.Fatalf("a direct order went out on a stale feed: %v", g.ordersOf("cmd move"))
				}
			})
			t.Run(fmt.Sprintf("%s/resume=%v/no lead record", pos.name, resume), func(t *testing.T) {
				g := newFakeRemote(t)
				g.lead().X = launch.X - pos.off
				hide := false
				g.onTick = func(g *fakeRemote) {
					if g.n >= 1 { // the first frame is out (the bind reads it): the lead record stops being listed
						hide = true
					}
				}
				game := hideLeadGame{g, func() bool { return hide }}
				rep, err := runRemote(t, game, rampPlan(launch, landing, true), opts(), func(c *Config) { c.Resume = resume })
				if err == nil || !strings.Contains(err.Error(), "launch waypoint 40.0 0.0 0.0 of the next direct step: the feed listed no lead bot for") {
					t.Fatalf("%v %+v", err, rep.Notes)
				}
				if n := len(g.ordersOf("cmd move 42.80 0.00 3.20 direct")); n != 0 {
					t.Fatalf("a direct order went out with no lead bot in the feed: %v", g.ordersOf("cmd move"))
				}
			})
			t.Run(fmt.Sprintf("%s/resume=%v/still", pos.name, resume), func(t *testing.T) {
				g := newFakeRemote(t)
				g.lead().X = launch.X - pos.off
				var began time.Time
				g.onTick = func(g *fakeRemote) {
					if began.IsZero() {
						began = g.now
					}
				}
				rep, err := runRemote(t, g, rampPlan(launch, landing, true), opts(), func(c *Config) { c.Resume = resume })
				if err != nil {
					t.Fatalf("%v %+v", err, rep.Notes)
				}
				// Nothing to move: the single cmd move is the direct step, and it waits for the proof.
				if mv := g.ordersOf("cmd move"); len(mv) != 1 || !strings.HasSuffix(mv[0], " direct") {
					t.Fatalf("moves %v", mv)
				}
				if el := g.moveOrderAt[0].Sub(began); el < tightenProofHold {
					t.Fatalf("the direct step went out %v after the run began, before the %v proof", el, tightenProofHold)
				}
			})
		}
	}
}

// No order goes out over a lead bot that is still moving, even when it is well away from the exact point (the walk's arrival is wider than
// the last stretch): the tighten waits for it to stand, and only then walks it back and onto the point.
func TestRemoteExactWaypointNoOrderOverAMovingLeadBot(t *testing.T) {
	g := newFakeRemote(t)
	launch, landing := Point{40, 0, 0}, Point{42.8, 0, 3.2}
	g.speed = 2     // the walk ends 4 yd out with the lead bot still walking
	const polls = 4 // enough to catch an order over a moving bot, and short enough that the cap leaves room for the proof
	var stoppedAt time.Time
	pollsMoved := 0
	started := false
	g.onTick = func(g *fakeRemote) {
		if !started {
			if len(g.ordersOf("cmd move")) < 2 {
				return // before the walk's own two orders
			}
			started = true
		}
		if pollsMoved >= polls {
			return
		}
		pollsMoved++
		if l := g.lead(); pollsMoved%2 == 0 {
			l.X = launch.X - 6
		} else {
			l.X = launch.X - 4
		}
		g.moveTo = nil
		if pollsMoved == polls {
			stoppedAt = g.now // the last poll it moved: from here it stands
		}
	}
	rep, err := runRemote(t, g, rampPlan(launch, landing, true), opts(), nil)
	if err != nil {
		t.Fatalf("%v %+v", err, rep.Notes)
	}
	if pollsMoved != polls {
		t.Fatalf("the lead bot only moved for %d polls: the test proves nothing", pollsMoved)
	}
	early := 0
	for _, at := range g.moveOrderAt {
		if !at.After(stoppedAt) {
			early++
		}
	}
	if early != 2 {
		t.Fatalf("%d moves went out while the lead bot was still moving (only the walk's two may): %v", early-2, g.ordersOf("cmd move"))
	}
	if n := len(g.ordersOf("cmd move 42.80 0.00 3.20 direct")); n != 1 {
		t.Fatalf("direct steps: %v", g.ordersOf("cmd move"))
	}
}

// The walk back is triggered on the module's own measure, the ground plane: a lead bot 2 yd from the exact point on the ground but 7 yd below
// it in 3D is walked back (a 3D trigger would call that far enough and send the short move the module ignores, so the step would fail).
func TestRemoteExactWaypointTightensOnTheGroundPlane(t *testing.T) {
	g := newFakeRemote(t)
	launch, landing := Point{40, 0, 0}, Point{42.8, 0, 3.2}
	g.speed = 2
	// The walk comes down the ramp from (30, 0, -10) and stops on it 2 yd from the launch point on the ground: the walk's arrival counts it
	// there, and the next poll drops it 7 yd below the point (the 3D distance is then 7.3 yd, the ground distance 2).
	g.lead().X, g.lead().Y, g.lead().Z = 30, 0, -10
	stages := 0
	g.onTick = func(g *fakeRemote) {
		l := g.lead()
		switch {
		case stages == 0 && hdist(Point{l.X, l.Y, l.Z}, launch) <= 2.5:
			l.X, l.Y, l.Z = launch.X-2, 0, launch.Z-2
			g.moveTo, stages = nil, 1
		case stages == 1:
			l.X, l.Y, l.Z = launch.X-2, 0, launch.Z-7
			g.moveTo, stages = nil, 2
		}
	}
	rep, err := runRemote(t, g, rampPlan(launch, landing, true), opts(), nil)
	if err != nil {
		t.Fatalf("%v %+v", err, rep.Notes)
	}
	if n := len(g.ordersOf("cmd move 30.00 0.00 -10.00")); n != 1 {
		t.Fatalf("the walk back on the ground plane was not ordered (%d times): %v", n, g.ordersOf("cmd move"))
	}
	if n := len(g.ordersOf("cmd move 42.80 0.00 3.20 direct")); n != 1 {
		t.Fatalf("direct steps: %v", g.ordersOf("cmd move"))
	}
}

// The tighten sends at most tightenTries rounds (each the walk back and the move onto the point), however long the cap: a lead bot whose moves
// the module ignores never comes closer, and after three rounds of orders the step fails at the cap — it does not keep ordering.
func TestRemoteExactWaypointOrdersAtMostThreeRounds(t *testing.T) {
	g := newFakeRemote(t)
	launch, landing := Point{40, 0, 0}, Point{42.8, 0, 3.2}
	g.speed = 2
	stopped := stopShortOf(g, launch, 4)
	inner := g.onTick
	g.onTick = func(g *fakeRemote) {
		inner(g)
		if stopped() {
			g.ignoreMoves = 1000 // every cmd move of the tighten is ACKed and the bot does not move
		}
	}
	o := opts()
	o.MoveStall = 2 * time.Second // each wait ends on its own stall quickly, so the rounds, not the waits, meet the cap
	rep, err := runRemote(t, g, rampPlan(launch, landing, true), o, nil)
	if err == nil || !strings.Contains(err.Error(), "launch waypoint 40.0 0.0 0.0 of the next direct step:") {
		t.Fatalf("%v %+v", err, rep.Notes)
	}
	tighten := len(g.ordersOf("cmd move")) - 2 // the walk's own two orders
	if tighten < 2 {
		t.Fatalf("the tighten sent only %d orders: the test proves nothing: %v", tighten, g.ordersOf("cmd move"))
	}
	if tighten > 2*tightenTries {
		t.Fatalf("the tighten sent %d orders, more than %d rounds: %v", tighten, tightenTries, g.ordersOf("cmd move"))
	}
	if n := len(g.ordersOf("cmd move 42.80 0.00 3.20 direct")); n != 0 {
		t.Fatalf("a direct order went out: %v", g.ordersOf("cmd move"))
	}
}

// A fight during the tighten is fought (as on a walk) and its time is given back to the cap: the fight here outlasts the 25 s on its own, and
// the step still goes out once the lead bot can be proved to stand on the exact point.
func TestRemoteExactWaypointFightGivesTheCapItsTimeBack(t *testing.T) {
	g := newFakeRemote(t)
	launch, landing := Point{40, 0, 0}, Point{42.8, 0, 3.2}
	g.speed = 2
	stopped := stopShortOf(g, launch, 4)
	inner := g.onTick
	added := false
	g.onTick = func(g *fakeRemote) {
		inner(g)
		// A pack stands where the tighten's walk back goes: it comes on the party and takes a slow kill.
		if !added && stopped() {
			m := g.addMob(299, Point{launch.X - 10, 0, 0})
			m.hp = 200
			g.aggro, g.killRate = 10, 10
			added = true
		}
	}
	rep, err := runRemote(t, g, rampPlan(launch, landing, true), opts(), nil)
	if err != nil {
		t.Fatalf("%v %+v", err, rep.Notes)
	}
	if rep.Remote.WalkAggros == 0 {
		t.Fatalf("no fight happened in the tighten: the test proves nothing (walk aggros %d, orders %v)", rep.Remote.WalkAggros, g.ordersOf("cmd attack"))
	}
	sent := 0
	for i, dest := range g.moveOrderDest {
		if dest != landing {
			continue
		}
		sent++
		if d := dist3(g.moveOrderPos[i], launch); d > directArrive {
			t.Fatalf("the direct order went out with the lead bot %.1f yd from the launch point: %v", d, g.ordersOf("cmd move"))
		}
	}
	if sent != 1 {
		t.Fatalf("direct orders to the point: %d: %v", sent, g.ordersOf("cmd move"))
	}
}

// A plan hop with no exact on the launch point keeps the walk's own arrival exactly as before: no tightening, no extra move, and the direct
// step goes out from where the walk left the lead bot — Uldaman leg 5's launch point at the scorpid pit lip (2.8 yd across from its landing)
// ran live that way.
func TestRemoteUldamanLikeHopWithoutExactIsNotTightened(t *testing.T) {
	g := newFakeRemote(t)
	launch, landing := Point{40, 0, 0}, Point{42.8, 0, 3.2}
	stopped := stopShortOf(g, launch, 4)
	rep, err := runRemote(t, g, rampPlan(launch, landing, false), opts(), nil)
	if err != nil {
		t.Fatalf("%v %+v", err, rep.Notes)
	}
	if !stopped() {
		t.Fatal("the lead bot never came near the launch point: the test proves nothing")
	}
	// The walk's own two moves and the direct step: nothing tightened.
	if n := len(g.ordersOf("cmd move")); n != 3 {
		t.Fatalf("moves %v", g.ordersOf("cmd move"))
	}
	if n := len(g.ordersOf("cmd move 40.00 0.00 0.00")); n != 1 {
		t.Fatalf("the launch point was ordered %d times: %v", n, g.ordersOf("cmd move"))
	}
	if notes := strings.Join(rep.Notes, " | "); strings.Contains(notes, "launch waypoint") {
		t.Fatalf("a waypoint with no exact was tightened: %v", rep.Notes)
	}
	if n := len(g.ordersOf("cmd move 42.80 0.00 3.20 direct")); n != 1 {
		t.Fatalf("direct steps: %v", g.ordersOf("cmd move"))
	}
}

// runRemoteBounded is runRemote for a tighten that has to end at its cap: the run is driven in its own goroutine and the test fails if it has
// not ended within d — a tighten that loops past its cap spins the fake clock for ever (the fake does not wait on the wall clock).
func runRemoteBounded(t *testing.T, d time.Duration, g interface {
	RemoteGame
	clock() Clock
}, plan *Plan, o RemoteOptions) (Report, error) {
	t.Helper()
	type out struct {
		rep Report
		err error
	}
	ch := make(chan out, 1)
	go func() {
		cfg := DefaultConfig()
		cfg.PostFight, cfg.AddsWait = 2*time.Second, 4*time.Second
		dr := NewRemoteDriver(plan, g, cfg, o, g.clock())
		err := dr.Run(context.Background(), 0)
		rep, _ := dr.Status()
		ch <- out{rep, err}
	}()
	select {
	case r := <-ch:
		return r.rep, r.err
	case <-time.After(d):
		t.Fatalf("the tighten did not end at its cap within %v: it went on past it", d)
		return Report{}, nil
	}
}

// stopTheWalkShort hangs the hook that ends the walk 4 yd short of the launch point, so that the tighten runs from there: the walk's own move
// onto the point is counted by the walk's arrival, and the hook leaves every later move to the launch point alone.
func stopTheWalkShort(g *fakeRemote, launch Point) {
	g.onTick = func(g *fakeRemote) {
		if g.moveTo == nil || *g.moveTo != launch || len(g.ordersOf("cmd move 40.00 0.00 0.00")) >= 2 {
			return
		}
		l := g.lead()
		if hdist(Point{l.X, l.Y, l.Z}, launch) <= 5 {
			l.X, l.Y, l.Z = launch.X-4, launch.Y, launch.Z
			g.moveTo = nil
		}
	}
}

// The tighten's cap bounds its waits and its looks: here the lead bot can never be proved to stand on the launch point (a slow bot that stops
// 4 yd short however often it is sent), so the step fails at the cap, naming the waypoint and the distance, and no direct order goes out.
func TestRemoteExactWaypointNotReachedFailsAtTheCap(t *testing.T) {
	g := newFakeRemote(t)
	launch, landing := Point{40, 0, 0}, Point{42.8, 0, 3.2}
	g.speed = 1 // a slow bot: a round of the tighten walks several seconds, so the waits, not the looks, decide the end
	holdShortOf(g, launch, 4)
	o := opts()
	o.MoveStall = 30 * time.Second // the walk's own stall bound is wide: only the tighten's cap can bound these waits
	rep, err := runRemote(t, g, rampPlan(launch, landing, true), o, nil)
	if err == nil || !strings.Contains(err.Error(), "launch waypoint 40.0 0.0 0.0 of the next direct step:") || !strings.Contains(err.Error(), "the direct step is not sent") {
		t.Fatalf("%v %+v", err, rep.Notes)
	}
	for _, o := range g.orders {
		if strings.HasSuffix(o, " direct") {
			t.Fatalf("a direct order went out with the lead bot short of the launch point: %v", g.ordersOf("cmd move"))
		}
	}
	// The tighten's own orders stay inside the cap.
	direct := -1
	last := -1
	first := -1
	for i, dest := range g.moveOrderDest {
		if dest == landing {
			direct = i
		}
	}
	_ = direct
	for i, dest := range g.moveOrderDest {
		if dest == launch {
			if first < 0 {
				first = i + 1 // the order after the walk's own move onto the launch point is the tighten's first
			}
			last = i
		}
	}
	if first < 0 || last < first {
		t.Fatalf("cannot tell the tighten's moves apart: %v", g.moveOrderDest)
	}
	if span := g.moveOrderAt[last].Sub(g.moveOrderAt[first]); span > tightenWait {
		t.Fatalf("a tighten order went out %v after the first (cap %v): %v", span, tightenWait, g.moveOrderAt)
	}
	// The cap ends the step: not the wait's own bounds (MoveStall is 30 s here), which would have run well past it.
	if el := g.now.Sub(g.moveOrderAt[first]); el > tightenWait+5*time.Second {
		t.Fatalf("the tighten ended %v after its first order (cap %v): the cap did not end its waits", el, tightenWait)
	}
}

// The cap can run out while a move of the tighten is still in flight, with the lead bot still walking it: the step then fails, saying the lead
// bot was still moving, and no direct order goes out (a jump started mid-motion near a ledge is unsafe).
func TestRemoteExactWaypointStillMovingAtTheCapFailsTheStep(t *testing.T) {
	g := newFakeRemote(t)
	launch, landing := Point{40, 0, 0}, Point{42.8, 0, 3.2}
	g.speed = 1
	stopTheWalkShort(g, launch)
	inner := g.onTick
	g.onTick = func(g *fakeRemote) {
		inner(g)
		// The tighten's own move onto the launch point (its order is the second one): the bot walks at it and never arrives.
		if g.moveTo == nil || *g.moveTo != launch || len(g.ordersOf("cmd move 40.00 0.00 0.00")) < 2 {
			return
		}
		if l := g.lead(); hdist(Point{l.X, l.Y, l.Z}, launch) <= 4 {
			l.X = launch.X - 6
		}
	}
	rep, err := runRemote(t, g, rampPlan(launch, landing, true), opts(), nil)
	if err == nil || !strings.Contains(err.Error(), "launch waypoint 40.0 0.0 0.0 of the next direct step:") || !strings.Contains(err.Error(), "the lead bot was still moving after") {
		t.Fatalf("%v %+v", err, rep.Notes)
	}
	for _, o := range g.orders {
		if strings.HasSuffix(o, " direct") {
			t.Fatalf("a direct order went out with the lead bot still walking at the launch point: %v", g.ordersOf("cmd move"))
		}
	}
}

// A lead bot that crawls across the launch point is not proof of standing: the anchor is one fixed place, and 0.4 yd a poll accumulates to far
// more than tightenStillYd within the hold, so the proof is never obtained and the step fails (a per-poll comparison would have called each of
// those polls "did not move" and let the step go out).
func TestRemoteExactWaypointCrawlingOverThePointIsNotProof(t *testing.T) {
	g := newFakeRemote(t)
	launch, landing := Point{40, 0, 0}, Point{42.8, 0, 3.2}
	g.lead().X = launch.X - 2.5 // outside directArrive, so the tight walk and the tighten are asked for; the walk moves nobody
	ticks := 0
	g.onTick = func(g *fakeRemote) {
		ticks++
		if ticks < 4 { // the run's own start-up: the leg (and the tighten) must be under way before the crawl begins
			return
		}
		g.lead().X += 0.4 // and then crawls past the launch point, 0.4 yd a poll
		g.moveTo = nil
	}
	rep, err := runRemote(t, g, rampPlan(launch, landing, true), opts(), nil)
	if err == nil || !strings.Contains(err.Error(), "launch waypoint 40.0 0.0 0.0 of the next direct step:") || !strings.Contains(err.Error(), "never stood still on it after") {
		t.Fatalf("%v %+v", err, rep.Notes)
	}
	for _, o := range g.orders {
		if strings.HasSuffix(o, " direct") {
			t.Fatalf("a direct order went out on a crawling lead bot: %v", g.ordersOf("cmd move"))
		}
	}
}

// A feed that has gone quiet after an issued move never counts as proof, and the tighten still ends at its cap: the lead bot is left 4 yd short
// (outside directArrive) by the walk and the feed dies, so the tighten's own move is in flight with nothing to see. The step fails, saying the
// feed went quiet, and no direct order goes out.
func TestRemoteExactWaypointWithAStaleFeedFailsTheStep(t *testing.T) {
	g := newFakeRemote(t)
	launch, landing := Point{40, 0, 0}, Point{42.8, 0, 3.2}
	g.speed = 1 // a slow bot: the walk's own move stops it short of the launch point, where the hook can catch it
	stopTheWalkShort(g, launch)
	inner := g.onTick
	var cutAt time.Time
	g.onTick = func(g *fakeRemote) {
		inner(g)
		// The walk stopped the lead bot 4 yd short of the launch point: a second later the feed goes quiet and stays so.
		l := g.lead()
		if cutAt.IsZero() && g.moveTo == nil && l.X == launch.X-4 {
			cutAt = g.now.Add(time.Second)
		}
		if !cutAt.IsZero() {
			g.cutAt = cutAt
		}
	}
	rep, err := runRemoteBounded(t, 20*time.Second, g, rampPlan(launch, landing, true), opts())
	if err == nil || !strings.Contains(err.Error(), "launch waypoint 40.0 0.0 0.0 of the next direct step:") || !strings.Contains(err.Error(), "the feed went quiet after") {
		t.Fatalf("%v %+v", err, rep.Notes)
	}
	if n := len(g.ordersOf("cmd move 42.80 0.00 3.20 direct")); n != 0 {
		t.Fatalf("a direct order went out on a stale feed: %v", g.ordersOf("cmd move"))
	}
}

// hideLeadGame is a RemoteGame whose frames carry no lead record while hide says so (the party list itself stays whole): a tighten that cannot
// see where the lead bot stands must end at its cap.
type hideLeadGame struct {
	*fakeRemote
	hide func() bool
}

func (g hideLeadGame) Frame() (tbm.ViewFrame, uint64, bool) {
	f, n, ok := g.fakeRemote.Frame()
	if ok && g.hide() {
		f.Lead = 0
	}
	return f, n, ok
}

// A feed that does not list the lead bot at all must not hold the tighten past its cap either, and never counts as proof: the step fails,
// saying no lead bot was listed, and no direct order goes out.
func TestRemoteExactWaypointWithoutALeadRecordFailsTheStep(t *testing.T) {
	g := newFakeRemote(t)
	launch, landing := Point{40, 0, 0}, Point{42.8, 0, 3.2}
	var hideAt time.Time
	g.lead().X = launch.X - 2.5 // outside directArrive: the walk and the tighten are asked for, and the walk moves nobody
	g.onTick = func(g *fakeRemote) {
		if hideAt.IsZero() {
			hideAt = g.now.Add(time.Second) // the feed loses the lead record as the tighten starts
		}
	}
	game := hideLeadGame{g, func() bool { return !hideAt.IsZero() && !g.now.Before(hideAt) }}
	rep, err := runRemoteBounded(t, 20*time.Second, game, rampPlan(launch, landing, true), opts())
	if err == nil || !strings.Contains(err.Error(), "launch waypoint 40.0 0.0 0.0 of the next direct step:") || !strings.Contains(err.Error(), "the feed listed no lead bot for") {
		t.Fatalf("%v %+v", err, rep.Notes)
	}
	if n := len(g.ordersOf("cmd move 42.80 0.00 3.20 direct")); n != 0 {
		t.Fatalf("a direct order went out with no lead bot in the feed: %v", g.ordersOf("cmd move"))
	}
}

// stopShortOfGround is stopShortOf for a ramp: the lead bot is put back at at, once, when its ground distance to p comes within trigger yards
// (its 3D distance is then whatever the slope makes it). It reports whether the bot was stopped.
func stopShortOfGround(g *fakeRemote, p, at Point, trigger float64) func() bool {
	stopped := false
	g.onTick = func(g *fakeRemote) {
		l := g.lead()
		if stopped || hdist(Point{l.X, l.Y, l.Z}, p) > trigger {
			return
		}
		l.X, l.Y, l.Z = at.X, at.Y, at.Z
		g.moveTo, stopped = nil, true
	}
	return func() bool { return stopped }
}

// The walk back keeps to the module's own measure of a move: GetDistance2d, the ground plane. A trail point far enough in 3D but nearer than
// MinMove on the ground (a steep ramp) would be ACKed and ignored, and so would the move onto the launch point from where the lead bot stands:
// it is not used (the step fails at the cap, with no direct order), while a ramp point far enough on the ground is used and the tighten works
// with minMove on.
func TestRemoteExactWaypointBacktrackKeepsToTheGroundPlane(t *testing.T) {
	launch, landing := Point{40, 0, 0}, Point{55, 0, 4}
	t.Run("a ramp point too near on the ground is not walked back to", func(t *testing.T) {
		g := newFakeRemote(t)
		g.speed = 2
		g.minMove = MinMove
		g.lead().X, g.lead().Y, g.lead().Z = 34, 0, -9 // the walk came up the ramp from here
		// Stopped on the ramp 2 yd short of the launch point: 8.1 yd from where the walk started in 3D, but only 4 yd on the ground.
		stopped := stopShortOfGround(g, launch, Point{38, 0, -2}, 2.5)
		rep, err := runRemote(t, g, rampPlan(launch, landing, true), opts(), nil)
		if err == nil || !strings.Contains(err.Error(), "launch waypoint 40.0 0.0 0.0 of the next direct step:") {
			t.Fatalf("%v %+v", err, rep.Notes)
		}
		if !stopped() {
			t.Fatal("the lead bot never came near the launch point: the test proves nothing")
		}
		if n := len(g.ordersOf("cmd move 34.00 0.00 -9.00")); n != 0 {
			t.Fatalf("the ramp point was walked back to %d times although the module would ignore that move: %v", n, g.ordersOf("cmd move"))
		}
		for _, o := range g.orders {
			if strings.HasSuffix(o, " direct") {
				t.Fatalf("a direct order went out with the lead bot on the ramp, off the launch point: %v", g.ordersOf("cmd move"))
			}
		}
	})
	t.Run("a ramp point far enough on the ground is walked back to", func(t *testing.T) {
		g := newFakeRemote(t)
		g.speed = 2
		g.minMove = MinMove
		g.lead().X, g.lead().Y, g.lead().Z = 30, 0, -4 // 10 yd from the launch point on the ground, 10.8 in 3D
		stopped := stopShortOfGround(g, launch, Point{38, 0, -0.8}, 2.5)
		rep, err := runRemote(t, g, rampPlan(launch, landing, true), opts(), nil)
		if err != nil {
			t.Fatalf("%v %+v", err, rep.Notes)
		}
		if !stopped() {
			t.Fatal("the lead bot never came near the launch point: the test proves nothing")
		}
		if n := len(g.ordersOf("cmd move 30.00 0.00 -4.00")); n != 1 {
			t.Fatalf("the ramp point was walked back to %d times: %v", n, g.ordersOf("cmd move"))
		}
		if n := len(g.ordersOf("cmd move 40.00 0.00 0.00")); n != 2 {
			t.Fatalf("moves to the launch point: %d: %v", n, g.ordersOf("cmd move"))
		}
		sent := 0
		for i, dest := range g.moveOrderDest {
			if dest != landing {
				continue
			}
			sent++
			if d := dist3(g.moveOrderPos[i], launch); d > directArrive {
				t.Fatalf("the direct order went out with the lead bot %.1f yd from the launch point: %v", d, g.ordersOf("cmd move"))
			}
		}
		if sent != 1 {
			t.Fatalf("direct orders to the point: %d: %v", sent, g.ordersOf("cmd move"))
		}
	})
}

// A lead bot that starts (or resumes) 1.5-3 yd from an exact waypoint: the walk to it is asked for (the Stepper skips a via point only within
// directArrive there), and with nothing walked behind it there is no path point to back up to, so the step fails at the cap, naming the
// waypoint and the distance, and no direct order goes out.
func TestRemoteExactWaypointStoodNearIsStillWalkedAndFailsShort(t *testing.T) {
	for _, resume := range []bool{false, true} {
		g := newFakeRemote(t)
		launch, landing := Point{40, 0, 0}, Point{42.8, 0, 3.2}
		g.lead().X = 38 // 2 yd from the launch point: within the old ArriveRadius (3 yd) skip
		rep, err := runRemote(t, g, rampPlan(launch, landing, true), opts(), func(c *Config) { c.Resume = resume })
		if err == nil || !strings.Contains(err.Error(), "launch waypoint 40.0 0.0 0.0 of the next direct step: 2.0 yd away and") {
			t.Fatalf("resume %v: %v %+v", resume, err, rep.Notes)
		}
		if n := len(g.ordersOf("cmd move 42.80 0.00 3.20 direct")); n != 0 {
			t.Fatalf("resume %v: a direct order went out from 2 yd short: %v", resume, g.ordersOf("cmd move"))
		}
	}
}

// A waypoint no direct step follows (and no exact) keeps the walk's own arrival: a lead bot that stops 4 yd short of it is left there, with no
// further move.
func TestRemoteWaypointWithoutADirectStepKeepsTheWalkArrival(t *testing.T) {
	g := newFakeRemote(t)
	launch := Point{40, 0, 0}
	stopped := stopShortOf(g, launch, 4)
	plan := remotePlan(Leg{Name: "gap", Via: []Waypoint{{Point: launch}}, To: Point{55, 0, 4},
		Steps: []Step{{WaitFor: &WaitFor{HostilesGone: &HostilesGone{Radius: 30}, TimeoutS: 5}}}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil {
		t.Fatalf("%v %+v", err, rep.Notes)
	}
	if !stopped() {
		t.Fatal("the lead bot never came near the waypoint: the test proves nothing")
	}
	if n := len(g.ordersOf("cmd move 40.00 0.00 0.00")); n != 1 {
		t.Fatalf("the waypoint was re-driven %d times: %v", n, g.ordersOf("cmd move"))
	}
}

// The Stepper marks only the waypoints the plan itself calls exact: the flag is carried into the walk action, a plain waypoint stays a plain
// walk, and an exact waypoint the leader already stands within ArriveRadius of is still walked (only directArrive is standing on it).
func TestStepperWalksExactWaypointsTightly(t *testing.T) {
	at := Point{40, 0, 0}
	plan := func(exact bool) *Plan {
		return remotePlan(Leg{Name: "gap", Via: []Waypoint{{Point: at, Exact: exact}, {Point: Point{55, 0, 4}, Direct: true}}, To: Point{55, 0, 4}})
	}
	w := &fakeWorld{}
	s := NewStepper(plan(true), w, (&fakeClock{t: time.Unix(1000, 0)}).now)
	a := s.Next()
	if a.Kind != ActWalk || a.To != at || !a.Exact {
		t.Fatalf("an exact waypoint is walked plainly: %+v", a)
	}
	w.pos = at // the first via point is reached: the next action is the leg's direct step
	s.Complete()
	if b := s.Next(); b.Kind != ActWalk || !b.Direct || b.Exact {
		t.Fatalf("the direct step is marked exact: %+v", b)
	}
	// A plain waypoint (no exact) is not marked, and one within ArriveRadius is passed.
	s = NewStepper(plan(false), &fakeWorld{}, (&fakeClock{t: time.Unix(1000, 0)}).now)
	if a := s.Next(); a.Kind != ActWalk || a.Exact {
		t.Fatalf("a plain waypoint is marked exact: %+v", a)
	}
	s = NewStepper(plan(false), &fakeWorld{pos: Point{38, 0, 0}}, (&fakeClock{t: time.Unix(1000, 0)}).now)
	if a := s.Next(); a.Kind != ActWalk || a.Exact || a.To != (Point{55, 0, 4}) {
		t.Fatalf("a plain waypoint within ArriveRadius is not passed: %+v", a)
	}
	// An exact waypoint is returned whatever the distance, even standing on it (the remote driver's proof runs from the walk action), and
	// again after a resume.
	for _, pos := range []Point{{38, 0, 0}, at} {
		for _, resume := range []bool{false, true} {
			s = NewStepper(plan(true), &fakeWorld{pos: pos}, (&fakeClock{t: time.Unix(1000, 0)}).now)
			if resume {
				s.ResumeAt()
			}
			if a := s.Next(); a.Kind != ActWalk || !a.Exact || a.To != at {
				t.Fatalf("an exact waypoint the leader stands %v from is not walked (resume %v): %+v", pos, resume, a)
			}
		}
	}
}

// A hold at the lead bot's own spot sends no move (the server cannot path a zero-length move: `unreachable`), whether or not a move was sent
// before; and a hold for a lead bot that a fight carried away is a point holdStepYd yards from it toward its last move point, never its own
// position.
func TestHoldHereSendsNoMoveToTheLeadBotsOwnSpot(t *testing.T) {
	g := newFakeRemote(t)
	g.advance(time.Second) // the first frame
	d := NewRemoteDriver(remotePlan(Leg{Name: "a", To: Point{10, 0, 0}}), g, DefaultConfig(), opts(), g.clock())
	d.look()
	d.bound = true
	here, ok := d.leadPos()
	if !ok {
		t.Fatal("no lead position in the feed")
	}
	// No move was sent yet: nothing is known to anchor, nothing is sent.
	d.holdHere(context.Background())
	if len(g.ordersOf("cmd move")) != 0 {
		t.Fatalf("a hold with no last move: %v", g.orders)
	}
	// The last move point is the lead bot's own spot: nothing is sent.
	d.moveTo, d.haveMoveTo = here, true
	d.holdHere(context.Background())
	if len(g.ordersOf("cmd move")) != 0 {
		t.Fatalf("a hold at the lead bot's own spot: %v", g.orders)
	}
	// The last move point is farther than holdSpotYd away: the stay spot is 2 yd from the lead bot toward it.
	d.moveTo = Point{here.X - 12, here.Y, here.Z}
	d.holdHere(context.Background())
	mv := g.ordersOf("cmd move")
	if len(mv) != 1 {
		t.Fatalf("moves %v here %v moveTo %v have %v stale %v", mv, here, d.moveTo, d.haveMoveTo, d.stale())
	}
	if dest := g.moveOrderDest[len(g.moveOrderDest)-1]; dist3(dest, here) < holdMinYd || dist3(dest, here) > holdStepYd+0.1 || dest.X >= here.X {
		t.Fatalf("hold to %v from %v", dest, here)
	}
}
