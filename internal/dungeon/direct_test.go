package dungeon

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"
)

// A leg with a via list: a plain waypoint, then a direct step to the leg's `to`.
const viaPlan = `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","via":[{"x":40,"y":0,"z":0},{"x":55,"y":0,"z":4,"direct":true}],"to":{"x":55,"y":0,"z":4},
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
