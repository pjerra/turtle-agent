package dungeon

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/eluna"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// elunaFake is the fake game with the Eluna test toolkit on top: the agent's own `.go xyz` and the three `.tt` queries the start
// path sends. Every call of the start path is recorded in seq, in the order it was made.
type elunaFake struct {
	*fakeLeader
	seq []string

	goAt  []Point
	goMap []uint32
	goErr error

	floor    eluna.FloorResult
	floorErr error

	quiet    eluna.QuietResult
	quietErr error
	quietAt  []Point
	quietR   []float32
	quietW   []time.Duration

	place    eluna.PlaceResult
	placeErr error
	placeAt  []Point
	spread   []float32
	of       []string

	times []time.Time // when each entry of seq was made (the fake clock)
}

func newElunaFake(t *testing.T) *elunaFake {
	t.Helper()
	g := &elunaFake{fakeLeader: newFakeLeader(t, happyScript()...)}
	// The step-3 start at the tests' step 3 is proven by the run leader's start message.
	g.startExtra = []string{"The run starts at step 3 of 5 (leg first): the party is in the instance."}
	f := float32(0)
	g.floor = eluna.FloorResult{Floor: &f}
	g.quiet = eluna.QuietResult{Quiet: true}
	g.place = eluna.PlaceResult{Placed: true, Members: []eluna.Placed{{GUID: "0x1", OK: true, Dz: 0.2}, {GUID: "0x2", OK: true, Dz: 0.3}}}
	return g
}

func (g *elunaFake) GoXYZ(ctx context.Context, mapID uint32, at Point) error {
	g.seq = append(g.seq, "go xyz")
	g.times = append(g.times, g.now)
	g.goAt = append(g.goAt, at)
	g.goMap = append(g.goMap, mapID)
	return g.goErr
}

func (g *elunaFake) TTFloor(ctx context.Context, p Point) (eluna.FloorResult, error) {
	g.seq = append(g.seq, "tt floor")
	g.times = append(g.times, g.now)
	return g.floor, g.floorErr
}

func (g *elunaFake) TTQuiet(ctx context.Context, p Point, r float32, wait time.Duration) (eluna.QuietResult, error) {
	g.seq = append(g.seq, "tt quiet")
	g.times = append(g.times, g.now)
	g.quietAt = append(g.quietAt, p)
	g.quietR = append(g.quietR, r)
	g.quietW = append(g.quietW, wait)
	return g.quiet, g.quietErr
}

func (g *elunaFake) TTPlace(ctx context.Context, p Point, spread float32, of string) (eluna.PlaceResult, error) {
	g.seq = append(g.seq, "tt place")
	g.times = append(g.times, g.now)
	g.placeAt = append(g.placeAt, p)
	g.spread = append(g.spread, spread)
	g.of = append(g.of, of)
	return g.place, g.placeErr
}

func (g *elunaFake) Command(ctx context.Context, command string) (tbm.Reply, error) {
	g.seq = append(g.seq, command)
	g.times = append(g.times, g.now)
	return g.fakeLeader.Command(ctx, command)
}

// The rest of StartHands (the start path asserts it for the `.go xyz`).
func (g *elunaFake) AgentPlace() (Point, uint32, bool)               { return Point{}, 0, true }
func (g *elunaFake) AccountLevel(context.Context) (int, bool, error) { return 3, true, nil }
func (g *elunaFake) Appear(context.Context, string) error            { return nil }
func (g *elunaFake) WalkSelf(context.Context, Point) (WalkResult, error) {
	return WalkResult{}, nil
}

// elunaOpts is an observed run with the toolkit on and a partial start at p.
func elunaOpts(p Point, havePoint bool) RemoteOptions {
	o := observeOpts(ObserveSpec{})
	o.ElunaPlace = true
	o.StartFrom = func(int) (PartialStart, error) {
		return PartialStart{Words: "from step 3", Step: 3, Leg: -1, Point: p, HavePoint: havePoint}, nil
	}
	return o
}

// placeSeq keeps the start placement's own calls (the `.go xyz`, the `.tt` queries and the two `.bot action` lines) in order.
// indexPrefix is the first index of an order that starts with prefix (-1: none).
func indexPrefix(list []string, prefix string) int {
	for i, s := range list {
		if strings.HasPrefix(s, prefix) {
			return i
		}
	}
	return -1
}

func placeSeq(seq []string) []string {
	var out []string
	for _, s := range seq {
		switch {
		case s == "go xyz", s == "tt floor", s == "tt quiet", s == "tt place", s == "action stay", s == "action follow":
			out = append(out, s)
		}
	}
	return out
}

// Off by default: no `.tt` line and no `.bot action` line is sent, whatever the start says.
func TestElunaPlaceOffByDefault(t *testing.T) {
	g := newElunaFake(t)
	o := observeOpts(ObserveSpec{})
	o.StartFrom = func(int) (PartialStart, error) {
		return PartialStart{Words: "from step 3", Step: 3, Leg: -1, Point: Point{}, HavePoint: true}, nil
	}
	rep, err := runRemote(t, g, observePlan(), o, nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	if s := placeSeq(g.seq); len(s) != 0 {
		t.Fatalf("the toolkit was used with the flag off: %v", s)
	}
}

// The decided order of the start placement: move, floor, quiet, stay, place, follow — and the run starts after it.
func TestElunaPlaceOrder(t *testing.T) {
	g := newElunaFake(t)
	rep, err := runRemote(t, g, observePlan(), elunaOpts(Point{X: 1, Y: 2, Z: 3}, true), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	want := []string{"go xyz", "tt floor", "tt quiet", "action stay", "tt place", "action follow"}
	if got := placeSeq(g.seq); !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	if len(g.goAt) != 1 || g.goAt[0] != (Point{X: 1, Y: 2, Z: 5}) || g.goMap[0] != 0 {
		t.Fatalf(".go xyz %v map %v", g.goAt, g.goMap)
	}
	if len(g.quietAt) != 1 || g.quietR[0] != 50 || g.quietW[0] != 20*time.Second {
		t.Fatalf("quiet %v r %v w %v", g.quietAt, g.quietR, g.quietW)
	}
	if len(g.placeAt) != 1 || g.placeAt[0] != (Point{X: 1, Y: 2, Z: 3}) || g.spread[0] != 3 {
		t.Fatalf("place %v spread %v", g.placeAt, g.spread)
	}
	if i, j := indexIn(g.seq, "action follow"), indexPrefix(g.seq, "run start"); i < 0 || j < 0 || i > j {
		t.Fatalf("follow at %d, run start at %d: %v", i, j, g.seq)
	}
	notes := strings.Join(rep.Notes, "\n")
	if !strings.Contains(notes, "eluna place: quiet, 2 placed at 1.0 2.0 3.0") {
		t.Fatalf("notes %v", rep.Notes)
	}
}

// The bind makes a bots-only group the commander is not in: the place names the lead bot's group with `of=`.
func TestElunaPlacePassesTheLeadBotsGroup(t *testing.T) {
	g := newElunaFake(t)
	rep, err := runRemote(t, g, observePlan(), elunaOpts(Point{X: 1, Y: 2, Z: 3}, true), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	if !slices.Equal(g.of, []string{"Tanky"}) {
		t.Fatalf("place of= %q, want the run's lead bot", g.of)
	}
}

// Nothing waits after the place: bound bots with no run going wander off in about a minute, so the run starts in the same
// instant as the placement (the fake clock only moves on a wait).
func TestElunaPlaceStartsAtOnce(t *testing.T) {
	g := newElunaFake(t)
	rep, err := runRemote(t, g, observePlan(), elunaOpts(Point{X: 1, Y: 2, Z: 3}, true), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	place, start := indexIn(g.seq, "tt place"), indexPrefix(g.seq, "run start")
	if place < 0 || start < 0 || start < place {
		t.Fatalf("place at %d, run start at %d: %v", place, start, g.seq)
	}
	if !g.times[start].Equal(g.times[place]) {
		t.Fatalf("the start waited %v after the place: %v", g.times[start].Sub(g.times[place]), g.seq)
	}
}

// A member that drifted 7 yd from its ring spot but stands 0.3 yd from the floor is placed: the toolkit's own `placed` is
// false, the start goes on and the drift is only a note.
func TestElunaPlaceDriftIsANote(t *testing.T) {
	g := newElunaFake(t)
	g.place = eluna.PlaceResult{Placed: false, Members: []eluna.Placed{
		{GUID: "0x1", Name: "Tanky", Dz: 0.2, Dist: 1.1},
		{GUID: "0x2", Name: "Magey", Dz: 0.3, Dist: 7},
	}}
	rep, err := runRemote(t, g, observePlan(), elunaOpts(Point{}, true), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %s", err, rep.State, rep.Error)
	}
	notes := strings.Join(rep.Notes, "\n")
	if !strings.Contains(notes, "eluna place: quiet, 2 placed at 0.0 0.0 0.0 (drifted: Magey 7.0 yd)") {
		t.Fatalf("notes %v", rep.Notes)
	}
	if indexPrefix(g.orders, "run start") < 0 {
		t.Fatalf("the run was not started: %v", g.orders)
	}
}

// A member 3 yd off the floor is not placed: the run is refused there, with the member and its height named.
func TestElunaPlaceRefusesOffFloor(t *testing.T) {
	g := newElunaFake(t)
	g.place = eluna.PlaceResult{Placed: true, Members: []eluna.Placed{
		{GUID: "0x1", Name: "Tanky", Dz: 0.2, Dist: 1.1},
		{GUID: "0x2", Name: "Magey", Dz: 3, Dist: 1, OK: true},
	}}
	_, err := runRemote(t, g, observePlan(), elunaOpts(Point{}, true), nil)
	if err == nil || !strings.HasPrefix(err.Error(), "observe mode: eluna place:") || !strings.Contains(err.Error(), "Magey") || !strings.Contains(err.Error(), "off the floor") {
		t.Fatalf("%v", err)
	}
	if s := placeSeq(g.seq); slices.Contains(s, "action follow") {
		t.Fatalf("an off-floor party was released: %v", s)
	}
	if indexPrefix(g.orders, "run start") >= 0 {
		t.Fatalf("the run was started: %v", g.orders)
	}
}

// A pack that is not quiet refuses the run there: the near and roaming units are named, nothing is placed, no run starts.
func TestElunaPlaceRefusesNotQuiet(t *testing.T) {
	g := newElunaFake(t)
	g.quiet = eluna.QuietResult{Quiet: false,
		Near:    []eluna.Unit{{GUID: "0xF130000000001622", Entry: 5650, Name: "Witch Doctor Zum'rah", Dist: 8.2}},
		Roamers: []eluna.Roamer{{GUID: "0xF130000000001623", Entry: 5651, Why: "moved", Moved: 12, Dist: 30}}}
	_, err := runRemote(t, g, observePlan(), elunaOpts(Point{}, true), nil)
	if err == nil || !strings.HasPrefix(err.Error(), "observe mode: eluna place:") || !strings.Contains(err.Error(), "quiet") {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(err.Error(), "5650") || !strings.Contains(err.Error(), "Witch Doctor Zum'rah") || !strings.Contains(err.Error(), "5651") {
		t.Fatalf("the refusal does not say what stands there: %v", err)
	}
	if s := placeSeq(g.seq); slices.Contains(s, "tt place") || slices.Contains(s, "action follow") {
		t.Fatalf("a party that is not alone was placed: %v", s)
	}
	if indexPrefix(g.orders, "run start") >= 0 {
		t.Fatalf("the run was started: %v", g.orders)
	}
}

func TestElunaPlaceRefusesFloorMismatch(t *testing.T) {
	g := newElunaFake(t)
	floor := float32(10)
	g.floor = eluna.FloorResult{Floor: &floor}
	_, err := runRemote(t, g, observePlan(), elunaOpts(Point{Z: 0}, true), nil)
	if err == nil || !strings.HasPrefix(err.Error(), "observe mode: eluna place:") || !strings.Contains(err.Error(), "floor") {
		t.Fatalf("%v", err)
	}
	if s := placeSeq(g.seq); slices.Contains(s, "tt quiet") || slices.Contains(s, "action stay") {
		t.Fatalf("a floor 10 yd below the step was placed on: %v", s)
	}
	if indexPrefix(g.orders, "run start") >= 0 {
		t.Fatalf("the run was started: %v", g.orders)
	}

	// No floor at all (indoors, off the mesh) is refused too.
	g = newElunaFake(t)
	g.floor = eluna.FloorResult{}
	_, err = runRemote(t, g, observePlan(), elunaOpts(Point{Z: 0}, true), nil)
	if err == nil || !strings.HasPrefix(err.Error(), "observe mode: eluna place:") || !strings.Contains(err.Error(), "no floor") {
		t.Fatalf("%v", err)
	}
}

// Without the start step's point there is nothing to place at: the placement does nothing and checkStartPlace decides as before.
func TestElunaPlaceNeedsPoint(t *testing.T) {
	g := newElunaFake(t)
	_, err := runRemote(t, g, observePlan(), elunaOpts(Point{}, false), nil)
	if err == nil || !strings.Contains(err.Error(), "start step's point is not known") {
		t.Fatalf("%v", err)
	}
	if s := placeSeq(g.seq); len(s) != 0 {
		t.Fatalf("the placement ran without a point: %v", s)
	}
	if indexPrefix(g.orders, "run start") >= 0 {
		t.Fatalf("the run was started: %v", g.orders)
	}
}

// A game without the toolkit refuses the run rather than starting it untested.
func TestElunaPlaceNeedsClient(t *testing.T) {
	g := newFakeLeader(t, happyScript()...)
	_, err := runRemote(t, g, observePlan(), elunaOpts(Point{}, true), nil)
	if err == nil || !strings.Contains(err.Error(), "eluna_toolkit is on but this game has no Eluna client") {
		t.Fatalf("%v", err)
	}
	if indexPrefix(g.orders, "run start") >= 0 {
		t.Fatalf("the run was started: %v", g.orders)
	}
}

// The place order's error (a pack that woke up, a refused anchor) refuses the run too.
func TestElunaPlaceRefusesPlaceError(t *testing.T) {
	g := newElunaFake(t)
	g.place = eluna.PlaceResult{}
	g.placeErr = &eluna.Error{Code: "in_combat", Msg: "a member is in combat"}
	_, err := runRemote(t, g, observePlan(), elunaOpts(Point{}, true), nil)
	if err == nil || !strings.HasPrefix(err.Error(), "observe mode: eluna place:") || !strings.Contains(err.Error(), "in_combat") {
		t.Fatalf("%v", err)
	}
	if indexPrefix(g.orders, "run start") >= 0 {
		t.Fatalf("the run was started: %v", g.orders)
	}
}
