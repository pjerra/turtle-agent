package dungeon

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// fakeWorld is a World the test moves by hand.
type fakeWorld struct {
	pos      Point
	objs     []objects.GameObject
	hostiles int
}

func (f *fakeWorld) Position() Point                   { return f.pos }
func (f *fakeWorld) HostilesWithin(Point, float32) int { return f.hostiles }
func (f *fakeWorld) GameObjects() []objects.GameObject {
	out := make([]objects.GameObject, len(f.objs))
	for i, o := range f.objs {
		o.Distance = float32(dist3(f.pos, Point{o.X, o.Y, o.Z}))
		out[i] = o
	}
	return out
}

func (f *fakeWorld) set(guid packet.GUID, state string) {
	for i := range f.objs {
		if f.objs[i].GUID == guid {
			f.objs[i].State = state
		}
	}
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func mustPlan(t *testing.T, s string) *Plan {
	t.Helper()
	p, err := Parse("t.json", []byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

const (
	keystone packet.GUID = 0xF11001E5D3003839
	seal     packet.GUID = 0xF11001E5D4003856
)

func doors() []objects.GameObject {
	return []objects.GameObject{
		{GUID: keystone, Entry: 124371, X: 20, Y: 0, Z: 0, State: "ready"},
		{GUID: seal, Entry: 124372, X: 30, Y: 0, Z: 0, State: "ready"},
	}
}

const doorPlan = `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","to":{"x":10,"y":0,"z":0},"skip":[9],
  "pull_groups":[{"entries":[1,2],"positions":[{"x":12,"y":0,"z":0}]},{"entries":[3],"positions":[{"x":14,"y":0,"z":0}]}],
  "steps":[{"use_object":{"entry":124371}},{"wait_for":{"gameobject":{"entry":124372,"state":"active"},"timeout_s":20}}],
  "boss":{"entry":7,"position":{"x":40,"y":0,"z":0},"first":[8]}},
 {"to":{"x":50,"y":0,"z":0},"steps":[{"wait_for":{"hostiles_gone":{"radius":30},"timeout_s":10}}]}]}`

func TestStepperWalksALeg(t *testing.T) {
	w := &fakeWorld{objs: doors()}
	clk := &fakeClock{t: time.Unix(1000, 0)}
	s := NewStepper(mustPlan(t, doorPlan), w, clk.now)

	a := s.Next()
	if a.Kind != ActWalk || a.To != (Point{10, 0, 0}) || a.Leg != 0 {
		t.Fatalf("%+v", a)
	}
	if b := s.Next(); b.Kind != ActWalk {
		t.Fatalf("Next repeats its action until it is completed: %+v", b)
	}
	w.pos = Point{10, 0, 0}
	s.Complete()

	a = s.Next()
	if a.Kind != ActPull || len(a.Entries) != 2 || a.Positions[0] != (Point{12, 0, 0}) || a.Skip[0] != 9 {
		t.Fatalf("%+v", a)
	}
	s.Complete()
	if a = s.Next(); a.Kind != ActPull || a.Entries[0] != 3 {
		t.Fatalf("%+v", a)
	}
	s.Complete()

	// The keystone is 10 yards away: walk next to it, then use it; a walk inside a step does not finish the step.
	a = s.Next()
	if a.Kind != ActWalk || a.To != (Point{20, 0, 0}) || a.Step != 0 {
		t.Fatalf("%+v", a)
	}
	w.pos = Point{18, 0, 0}
	s.Complete()
	a = s.Next()
	if a.Kind != ActUse || a.GUID != keystone || a.Object.Entry != 124371 {
		t.Fatalf("a walk inside a step does not end it: %+v", a)
	}
	s.Complete()

	// The seal opens a moment later; until then the stepper waits, and the wait is not completed by the driver.
	a = s.Next()
	if a.Kind != ActWait || a.Remaining != 20*time.Second || a.Step != 1 {
		t.Fatalf("%+v", a)
	}
	clk.t = clk.t.Add(5 * time.Second)
	s.Complete() // no effect on a wait
	if a = s.Next(); a.Kind != ActWait || a.Remaining != 15*time.Second {
		t.Fatalf("%+v", a)
	}
	w.set(keystone, "active")
	w.set(seal, "active")

	a = s.Next()
	if a.Kind != ActKill || a.Boss.Entry != 7 || a.Boss.First[0] != 8 || a.Skip[0] != 9 {
		t.Fatalf("%+v", a)
	}
	s.Complete()

	// Leg 1: the player is far from its To, hostiles stand around; the wait passes at once when they are gone.
	a = s.Next()
	if a.Kind != ActWalk || a.Leg != 1 || a.To != (Point{50, 0, 0}) {
		t.Fatalf("%+v", a)
	}
	w.pos = Point{49, 0, 0}
	w.hostiles = 2
	s.Complete()
	if a = s.Next(); a.Kind != ActWait || a.Leg != 1 {
		t.Fatalf("%+v", a)
	}
	w.hostiles = 0
	if a = s.Next(); a.Kind != ActDone {
		t.Fatalf("%+v", a)
	}
	if a = s.Next(); a.Kind != ActDone {
		t.Fatalf("done stays done: %+v", a)
	}
}

func TestStepperSkipsWhatIsAlreadyTrue(t *testing.T) {
	w := &fakeWorld{objs: doors(), pos: Point{10, 1, 0}} // within 3 yards of leg 0's To
	w.set(keystone, "active")
	w.set(seal, "active")
	w.objs[0].X, w.objs[0].Y = 11, 1 // the keystone is next to the player
	s := NewStepper(mustPlan(t, doorPlan), w, nil)
	if a := s.Next(); a.Kind != ActPull {
		t.Fatalf("no walk when already there: %+v", a)
	}
	s.Complete()
	s.Complete()
	// Already open: the keystone is used (the stepper does not judge it), the wait passes at once.
	a := s.Next()
	if a.Kind != ActUse {
		t.Fatalf("%+v", a)
	}
	s.Complete()
	if a = s.Next(); a.Kind != ActKill {
		t.Fatalf("a wait that already holds is over: %+v", a)
	}
}

// A wait_for gameobject with gone_ok (Shadowfang Keep's Courtyard Door): done when the object is in the state, or missing from the
// agent's view after it was seen, when the agent stands within GoneReadYd of the point and the miss has held for GoneConfirm; an object
// never seen waits out its timeout.
func goneOKPlan(goneOK string) string {
	return `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","to":{"x":10,"y":0,"z":0},
  "steps":[{"wait_for":{"gameobject":{"entry":124372,"near":{"x":30,"y":0,"z":0},"state":"active"` + goneOK + `},"timeout_s":60}}],
  "boss":{"entry":7,"position":{"x":40,"y":0,"z":0}}}]}`
}

func TestStepperWaitGoneOKAfterSeenIsDone(t *testing.T) {
	w := &fakeWorld{objs: doors(), pos: Point{10, 1, 0}}
	clk := &fakeClock{t: time.Unix(1000, 0)}
	s := NewStepper(mustPlan(t, goneOKPlan(`,"gone_ok":true`)), w, clk.now)
	if a := s.Next(); a.Kind != ActWait {
		t.Fatalf("the seal is ready: %+v", a)
	}
	w.objs = w.objs[:1] // the script removed the seal
	clk.t = clk.t.Add(2 * time.Second)
	if a := s.Next(); a.Kind != ActWait {
		t.Fatalf("one miss is no proof: %+v", a)
	}
	clk.t = clk.t.Add(2 * time.Second)
	if a := s.Next(); a.Kind != ActWait {
		t.Fatalf("2 s of misses is not GoneConfirm: %+v", a)
	}
	clk.t = clk.t.Add(1 * time.Second)
	if a := s.Next(); a.Kind != ActKill {
		t.Fatalf("seen, and missing for GoneConfirm: %+v", a)
	}
}

// A miss from where the agent could not see the object (far from the point) never counts, and a blip of the object resets the count.
func TestStepperWaitGoneOKNeedsAReadableMissHeldWithoutABreak(t *testing.T) {
	w := &fakeWorld{objs: doors(), pos: Point{10, 1, 0}}
	clk := &fakeClock{t: time.Unix(1000, 0)}
	s := NewStepper(mustPlan(t, goneOKPlan(`,"gone_ok":true`)), w, clk.now)
	s.Next()
	seal := w.objs[1]
	w.objs = w.objs[:1]
	w.pos = Point{-200, 0, 0} // 230 yd from the point: the store lists nothing there
	for i := 0; i < 8; i++ {
		clk.t = clk.t.Add(2 * time.Second)
		if a := s.Next(); a.Kind != ActWait {
			t.Fatalf("a miss from out of view is not gone: %+v", a)
		}
	}
	w.pos = Point{10, 1, 0}
	clk.t = clk.t.Add(2 * time.Second)
	s.Next() // readable miss number one
	clk.t = clk.t.Add(2 * time.Second)
	w.objs = append(w.objs, seal) // a blip: found again
	if a := s.Next(); a.Kind != ActWait {
		t.Fatalf("%+v", a)
	}
	w.objs = w.objs[:1]
	clk.t = clk.t.Add(2 * time.Second)
	s.Next() // the count starts over
	clk.t = clk.t.Add(2 * time.Second)
	if a := s.Next(); a.Kind != ActWait {
		t.Fatalf("2 s after the break: %+v", a)
	}
	clk.t = clk.t.Add(2 * time.Second)
	if a := s.Next(); a.Kind != ActKill {
		t.Fatalf("4 s after the break: %+v", a)
	}
}

// A wait with no near point cannot be judged gone (nothing says where the agent would have to stand to see it).
func TestStepperWaitGoneOKWithoutNearIsNeverGone(t *testing.T) {
	w := &fakeWorld{objs: doors(), pos: Point{10, 1, 0}}
	clk := &fakeClock{t: time.Unix(1000, 0)}
	p := mustPlan(t, `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","to":{"x":10,"y":0,"z":0},"steps":[{"wait_for":{"gameobject":{"entry":124372,"state":"active","gone_ok":true},"timeout_s":60}}],
  "boss":{"entry":7,"position":{"x":40,"y":0,"z":0}}}]}`)
	s := NewStepper(p, w, clk.now)
	s.Next()
	w.objs = w.objs[:1]
	for i := 0; i < 5; i++ {
		clk.t = clk.t.Add(2 * time.Second)
		if a := s.Next(); a.Kind != ActWait {
			t.Fatalf("%+v", a)
		}
	}
}

func TestStepperWaitGoneOKNeverSeenWaitsOutItsTimeout(t *testing.T) {
	w := &fakeWorld{objs: doors()[:1], pos: Point{10, 1, 0}}
	clk := &fakeClock{t: time.Unix(1000, 0)}
	s := NewStepper(mustPlan(t, goneOKPlan(`,"gone_ok":true`)), w, clk.now)
	if a := s.Next(); a.Kind != ActWait {
		t.Fatalf("%+v", a)
	}
	for i := 0; i < 5; i++ {
		clk.t = clk.t.Add(10 * time.Second)
		if a := s.Next(); i < 5 && clk.t.Sub(time.Unix(1000, 0)) < 60*time.Second && a.Kind != ActWait {
			t.Fatalf("never seen is not gone: %+v", a)
		}
	}
	clk.t = clk.t.Add(20 * time.Second)
	if a := s.Next(); a.Kind != ActFailed || !strings.Contains(a.Err, "waited 60 s for game object 124372") {
		t.Fatalf("%+v", a)
	}
}

func TestStepperWaitGoneWithoutGoneOKKeepsWaiting(t *testing.T) {
	w := &fakeWorld{objs: doors(), pos: Point{10, 1, 0}}
	clk := &fakeClock{t: time.Unix(1000, 0)}
	s := NewStepper(mustPlan(t, goneOKPlan("")), w, clk.now)
	s.Next()
	w.objs = w.objs[:1]
	for i := 0; i < 5; i++ {
		clk.t = clk.t.Add(2 * time.Second)
		if a := s.Next(); a.Kind != ActWait {
			t.Fatalf("%+v", a)
		}
	}
}

// A sighting belongs to its target (entry and point), not to the entry: the second wait is on the same entry 60 yd away, never read
// found, so the first one's sighting does not make it gone.
func TestStepperWaitGoneOKSightingsAreKeyedByTarget(t *testing.T) {
	w := &fakeWorld{objs: doors(), pos: Point{60, 0, 0}}
	w.set(seal, "active")
	clk := &fakeClock{t: time.Unix(1000, 0)}
	p := mustPlan(t, `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","to":{"x":60,"y":0,"z":0},"steps":[
  {"wait_for":{"gameobject":{"entry":124372,"near":{"x":30,"y":0,"z":0},"state":"active","gone_ok":true},"timeout_s":60}},
  {"wait_for":{"gameobject":{"entry":124372,"near":{"x":90,"y":0,"z":0},"state":"active","gone_ok":true},"timeout_s":60}}],
  "boss":{"entry":7,"position":{"x":70,"y":0,"z":0}}}]}`)
	s := NewStepper(p, w, clk.now)
	var a Action
	for i := 0; i < 12; i++ {
		a = s.Next()
		clk.t = clk.t.Add(2 * time.Second)
		if a.Kind == ActKill || a.Kind == ActFailed {
			break
		}
	}
	if a.Kind != ActWait || !strings.Contains(a.Label, "near 90 0 0") {
		t.Fatalf("the second wait must still be waiting: %+v", a)
	}
}

func TestStepperWaitTimesOutAndFails(t *testing.T) {
	w := &fakeWorld{pos: Point{49, 0, 0}, hostiles: 1}
	clk := &fakeClock{t: time.Unix(1000, 0)}
	p := mustPlan(t, `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
	  {"name":"b","to":{"x":50,"y":0,"z":0},"steps":[{"wait_for":{"hostiles_gone":{"radius":30},"timeout_s":10}}]}]}`)
	s := NewStepper(p, w, clk.now)
	if a := s.Next(); a.Kind != ActWait {
		t.Fatalf("%+v", a)
	}
	clk.t = clk.t.Add(11 * time.Second)
	a := s.Next()
	if a.Kind != ActFailed || a.Err != "leg 0 (b): step 0: waited 10 s for no hostiles within 30 yd" {
		t.Fatalf("%+v", a)
	}
	s.Complete() // a failed plan stays failed
	if a = s.Next(); a.Kind != ActFailed {
		t.Fatalf("%+v", a)
	}
}

// A wait's fail_text is added to its failure, so a run that stops there says why.
func TestStepperWaitFailTextIsInTheFailure(t *testing.T) {
	w := &fakeWorld{pos: Point{49, 0, 0}, hostiles: 1}
	clk := &fakeClock{t: time.Unix(1000, 0)}
	p := mustPlan(t, `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
	  {"name":"b","to":{"x":50,"y":0,"z":0},"steps":[{"wait_for":{"hostiles_gone":{"radius":30},"timeout_s":10,"fail_text":"needs three participants"}}]}]}`)
	s := NewStepper(p, w, clk.now)
	s.Next()
	clk.t = clk.t.Add(11 * time.Second)
	if a := s.Next(); a.Kind != ActFailed || a.Err != "leg 0 (b): step 0: waited 10 s for no hostiles within 30 yd: needs three participants" {
		t.Fatalf("%+v", a)
	}
}

func TestStepperFailures(t *testing.T) {
	// An object that is not in view and has no place to look for it ends the plan; the driver can fail it as well.
	w := &fakeWorld{}
	s := NewStepper(mustPlan(t, `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
	  {"to":{"x":0,"y":0,"z":0},"steps":[{"use_object":{"entry":5}}]}]}`), w, nil)
	if a := s.Next(); a.Kind != ActFailed || a.Err != "leg 0: step 0: game object 5 is not in view" {
		t.Fatalf("%+v", a)
	}
	s = NewStepper(mustPlan(t, doorPlan), &fakeWorld{objs: doors()}, nil)
	s.Next()
	s.Fail("path blocked")
	s.Fail("later reason")
	if a := s.Next(); a.Kind != ActFailed || a.Err != "path blocked" {
		t.Fatalf("the first failure stays: %+v", a)
	}
}

// An object not in view is looked for where the plan says it stands (stand, else near) before the plan gives up.
func TestStepperLooksWhereThePlanSays(t *testing.T) {
	p := mustPlan(t, `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
	  {"to":{"x":0,"y":0,"z":0},"steps":[{"use_object":{"entry":124371,"near":{"x":20,"y":0,"z":0},"stand":{"x":17,"y":0,"z":0}}}]}]}`)
	w := &fakeWorld{}
	s := NewStepper(p, w, nil)
	a := s.Next()
	if a.Kind != ActWalk || a.To != (Point{17, 0, 0}) {
		t.Fatalf("%+v", a)
	}
	w.pos = Point{17, 0, 0}
	w.objs = doors()
	s.Complete()
	if a = s.Next(); a.Kind != ActUse || a.GUID != keystone {
		t.Fatalf("%+v", a)
	}
}

// Of two objects with the same entry the one nearest to the given position is used, and one out of range is not.
func TestStepperPicksTheNearestToNear(t *testing.T) {
	near := objects.GameObject{GUID: 1, Entry: 77, X: 101, Y: 0, Z: 0}
	far := objects.GameObject{GUID: 2, Entry: 77, X: 120, Y: 0, Z: 0}
	w := &fakeWorld{pos: Point{100, 0, 0}, objs: []objects.GameObject{far, near}}
	p := mustPlan(t, `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
	  {"to":{"x":100,"y":0,"z":0},"steps":[{"use_object":{"near":{"x":119,"y":0,"z":0},"entry":77,"range":4}}]}]}`)
	s := NewStepper(p, w, nil)
	if a := s.Next(); a.Kind != ActWalk || a.To != (Point{120, 0, 0}) {
		t.Fatalf("the object nearest to near, not to the player: %+v", a)
	}
	p = mustPlan(t, `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
	  {"to":{"x":100,"y":0,"z":0},"steps":[{"use_object":{"near":{"x":50,"y":0,"z":0},"entry":77}}]}]}`)
	s = NewStepper(p, w, nil)
	if a := s.Next(); a.Kind != ActWalk || a.To != (Point{50, 0, 0}) {
		t.Fatalf("nothing within range of near: go and look there: %+v", a)
	}
	w.pos = Point{50, 0, 0}
	if a := s.Next(); a.Kind != ActFailed {
		t.Fatalf("nothing there either: %+v", a)
	}
}

func TestStepperEmptyAndPosition(t *testing.T) {
	w := &fakeWorld{objs: doors()}
	s := NewStepper(mustPlan(t, doorPlan), w, nil)
	if l, ph, i := s.Position(); l != 0 || ph != "walk" || i != 0 {
		t.Fatalf("%d %s %d", l, ph, i)
	}
	s.Next()
	s.Complete()
	if l, ph, i := s.Position(); l != 0 || ph != "pull_groups" || i != 0 {
		t.Fatalf("%d %s %d", l, ph, i)
	}
}

// A walk inside a use_object step that never brings the player next to the object ends the plan after
// MaxStepWalks, and so does a step that has been going for MaxStepSeconds.
func TestStepperBoundsTheWalksOfAStep(t *testing.T) {
	plan := mustPlan(t, `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
	  {"name":"a","to":{"x":0,"y":0,"z":0},"steps":[{"use_object":{"entry":124371}}]}]}`)
	w := &fakeWorld{objs: doors()} // the keystone stays 20 yd away: every walk "succeeds" without getting closer
	clk := &fakeClock{t: time.Unix(1000, 0)}
	s := NewStepper(plan, w, clk.now)
	for i := range MaxStepWalks {
		if a := s.Next(); a.Kind != ActWalk {
			t.Fatalf("walk %d: %+v", i, a)
		}
		s.Complete()
	}
	a := s.Next()
	if a.Kind != ActFailed || a.Err != "leg 0 (a): step 0: game object 124371 is still out of reach after 4 walks" {
		t.Fatalf("%+v", a)
	}

	s = NewStepper(plan, &fakeWorld{objs: doors()}, clk.now)
	if a := s.Next(); a.Kind != ActWalk {
		t.Fatalf("%+v", a)
	}
	clk.t = clk.t.Add((MaxStepSeconds + 1) * time.Second)
	if a := s.Next(); a.Kind != ActFailed || a.Err != "leg 0 (a): step 0: game object 124371 is not used after 121 s" {
		t.Fatalf("%+v", a)
	}

	// Within reach it is used, however long the step has been going.
	w = &fakeWorld{objs: doors(), pos: Point{19, 0, 0}}
	s = NewStepper(plan, w, clk.now)
	if a := s.Next(); a.Kind != ActWalk { // the leg's own walk to "to"
		t.Fatalf("%+v", a)
	}
	s.Complete()
	s.Next()
	clk.t = clk.t.Add((MaxStepSeconds + 1) * time.Second)
	if a := s.Next(); a.Kind != ActUse {
		t.Fatalf("%+v", a)
	}
}

func TestNewStepperAtStartsInTheLeg(t *testing.T) {
	p := mustPlan(t, doorPlan)
	s := NewStepperAt(p, &fakeWorld{objs: doors(), pos: Point{50, 0, 0}}, nil, 1)
	if leg, ph, _ := s.Position(); leg != 1 || ph != "walk" {
		t.Fatalf("%d %s", leg, ph)
	}
	if s = NewStepperAt(p, &fakeWorld{}, nil, 9); s.Next().Kind != ActDone {
		t.Fatal("past the end is done")
	}
	if s = NewStepperAt(p, &fakeWorld{}, nil, -3); s.Next().Leg != 0 {
		t.Fatal("below 0 is the first leg")
	}
}

// teamWorld is a fakeWorld whose leader has a team.
type teamWorld struct {
	*fakeWorld
	team string
}

func (w teamWorld) Team() string { return w.team }

const teamPlan = `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","to":{"x":0,"y":0,"z":0},
  "pull_groups":[{"entries":[1],"positions":[{"x":5,"y":0,"z":0}],"team":"horde"},{"entries":[2],"positions":[{"x":6,"y":0,"z":0}]}],
  "steps":[{"loot_object":{"entry":123329,"near":{"x":30,"y":0,"z":0},"expect_item":{"entry":7740}}},{"use_item":{"entry":7740,"expect_item":{"entry":7733}}}],
  "boss":{"entry":6906,"position":{"x":9,"y":0,"z":0},"team":"horde"}},
 {"name":"b","to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[3],"positions":[{"x":7,"y":0,"z":0}]}]}]}`

// The team gate passes over a pack and a boss of the other team and says so; steps and ungated packs stay.
func TestStepperTeamGate(t *testing.T) {
	var kinds []ActionKind
	run := func(team string) []ActionKind {
		p := mustPlan(t, teamPlan)
		w := teamWorld{&fakeWorld{objs: []objects.GameObject{{GUID: 0x77, Entry: 123329, X: 30, State: "ready"}}}, team}
		s := NewStepper(p, w, nil)
		kinds = nil
		var skipped []string
		for i := 0; i < 40; i++ {
			a := s.Next()
			skipped = append(skipped, s.Skipped()...)
			if a.Kind == ActDone {
				break
			}
			if a.Kind == ActWalk {
				w.pos = a.To
			}
			kinds = append(kinds, a.Kind)
			s.Complete()
		}
		if team == "alliance" && (len(skipped) != 2 || skipped[0] == "" || len(s.Skipped()) != 0) {
			t.Fatalf("skipped %q", skipped)
		}
		if team != "alliance" && len(skipped) != 0 {
			t.Fatalf("%s skipped %q", team, skipped)
		}
		return kinds
	}
	want := func(ks ...ActionKind) string { return fmt.Sprint(ks) }
	// Leg a: pulls [1 horde-only, 2], loot (a walk next to the chest first), use_item, boss (horde-only); leg b: a walk back and its pack.
	if got := run("alliance"); fmt.Sprint(got) != want(ActPull, ActWalk, ActLoot, ActItem, ActWalk, ActPull) {
		t.Fatalf("alliance %v", got)
	}
	if got := run("horde"); fmt.Sprint(got) != want(ActPull, ActPull, ActWalk, ActLoot, ActItem, ActKill, ActWalk, ActPull) {
		t.Fatalf("horde %v", got)
	}
	if got := run(""); len(got) != 8 {
		t.Fatalf("unknown team %v", got)
	}
}

// A loot_object step walks to the chest like a use_object, then asks for the loot with the item it expects.
func TestStepperLootAndItemActions(t *testing.T) {
	p := mustPlan(t, teamPlan)
	w := &fakeWorld{objs: []objects.GameObject{{GUID: 0x77, Entry: 123329, X: 30, State: "ready"}}}
	s := NewStepper(p, w, nil)
	s.Complete() // walk
	s.Complete() // pull 1 (team unknown: not skipped)
	s.Complete() // pull 2
	a := s.Next()
	if a.Kind != ActWalk || a.To != (Point{30, 0, 0}) {
		t.Fatalf("%+v", a)
	}
	w.pos = a.To
	s.Complete()
	a = s.Next()
	if a.Kind != ActLoot || a.GUID != 0x77 || a.Expect == nil || a.Expect.Entry != 7740 || a.Object == nil {
		t.Fatalf("%+v", a)
	}
	s.Complete()
	a = s.Next()
	if a.Kind != ActItem || a.Item.Entry != 7740 || a.Expect.Entry != 7733 {
		t.Fatalf("%+v", a)
	}
	s.Complete()
	if a = s.Next(); a.Kind != ActKill {
		t.Fatalf("%+v", a)
	}
}
