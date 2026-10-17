package dungeon

import (
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

// tracePlan is a plan of two legs: leg 0 has a via point, a stage point, a pack 100 yd on and a boss 100 yd past the pack; leg 1 is a stage point
// away from the boss.
func tracePlan() *Plan {
	return &Plan{Name: "t", Map: 1, Legs: []Leg{
		{Name: "a", To: Point{50, 0, 0}, Via: []Waypoint{{Point: Point{20, 0, 0}}},
			PullGroups: []PullGroup{pg(50, 100)}, Boss: &Boss{Entry: 9, Position: Point{50, 200, 0}}},
		{Name: "b", To: Point{150, 200, 0}},
	}}
}

func TestTraceNumbersTheStopsInRunOrder(t *testing.T) {
	tr, err := TracePlan(tracePlan(), fakeNav{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for i, s := range tr.Stops {
		if s.Seq != i+1 {
			t.Errorf("stop %d has seq %d", i, s.Seq)
		}
		got = append(got, s.Kind)
	}
	if want := "via stage pack boss stage"; strings.Join(got, " ") != want {
		t.Errorf("stops %q, want %q", strings.Join(got, " "), want)
	}
	if s := tr.Stops[4]; s.Leg != 1 || s.Seq != 5 {
		t.Errorf("the last stop is the second leg's stage point, numbered on from the first leg: %+v", s)
	}
	// every segment walks to a stop that exists, in the order of the stops
	prev := 0
	for _, sg := range tr.Segs {
		if sg.Stop < prev || sg.Stop > len(tr.Stops) {
			t.Errorf("segment %q walks to stop %d after stop %d", sg.Part, sg.Stop, prev)
		}
		prev = sg.Stop
	}
}

func TestTraceKeepsTheWalkBetweenLegs(t *testing.T) {
	tr, err := TracePlan(tracePlan(), fakeNav{})
	if err != nil {
		t.Fatal(err)
	}
	var firstOf [2]*RouteSeg
	var last0 RouteSeg
	for i := range tr.Segs {
		sg := &tr.Segs[i]
		if firstOf[sg.Leg] == nil {
			firstOf[sg.Leg] = sg
		}
		if sg.Leg == 0 {
			last0 = *sg
		}
	}
	if firstOf[0] == nil || !firstOf[0].Link || firstOf[0].From != (Point{}) {
		t.Errorf("the first walk of the plan starts at the entrance: %+v", firstOf[0])
	}
	l1 := firstOf[1]
	if l1 == nil || !l1.Link || l1.Part != "to" {
		t.Fatalf("leg 1 has no walk from the end of leg 0: %+v", l1)
	}
	if l1.From != last0.To {
		t.Errorf("the walk between legs starts at %v, where leg 0 ended: %v", l1.From, last0.To)
	}
	if len(l1.Path) < 2 || l1.Length < 50 {
		t.Errorf("the walk between legs has no path: %+v", l1)
	}
}

func TestTraceFlagsASegmentThePlannerCannotPath(t *testing.T) {
	p := tracePlan()
	nav := fakeNav{block: func(from, to Point) bool { return to == (Point{150, 200, 0}) }}
	tr, err := TracePlan(p, nav)
	if err != nil {
		t.Fatal(err)
	}
	var red []RouteSeg
	for _, sg := range tr.Segs {
		if !sg.OK {
			red = append(red, sg)
		}
	}
	if len(red) != 1 {
		t.Fatalf("%d failed segments, want 1: %+v", len(red), red)
	}
	r := red[0]
	if r.Reason != ReasonUnreachable || r.Leg != 1 || !r.Link || !strings.Contains(r.Detail, "Detour found no path") {
		t.Errorf("failed segment: %+v", r)
	}
	if len(r.Path) != 0 || r.To != (Point{150, 200, 0}) {
		t.Errorf("a segment with no path keeps its two ends to draw a chord from: %+v", r)
	}
	// the trace and the plan check say the same thing
	if len(tr.Check.Issues) != 1 || tr.Check.Issues[0].Reason != ReasonUnreachable {
		t.Errorf("check issues %q", issueReasons(tr.Check.Issues))
	}
	if st := tr.Stops[4]; st.Seq != 5 || st.Kind != "stage" {
		t.Errorf("stop 5: %+v", st)
	}
}

func TestTraceAgreesWithThePlanCheck(t *testing.T) {
	nav := fakeNav{off: map[Point]bool{{50, 200, 0}: true}, partial: map[Point]Point{{50, 100, 0}: {50, 40, 0}}}
	p := tracePlan()
	tr, err := TracePlan(p, nav)
	if err != nil {
		t.Fatal(err)
	}
	res, err := CheckPlan(p, nav, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := issueReasons(tr.Check.Issues), issueReasons(res.Issues); got != want {
		t.Errorf("trace issues %q, check issues %q", got, want)
	}
	failed := 0
	for _, sg := range tr.Segs {
		if !sg.OK {
			failed++
		}
	}
	if failed != len(res.Issues) {
		t.Errorf("%d failed segments for %d issues", failed, len(res.Issues))
	}
}

// bendNav walks every path through a corner 30 yd off the line, so that a path is not a straight line.
type bendNav struct{ fakeNav }

func (b bendNav) Plan(m uint32, from, to navmesh.Vec3) (navmesh.Path, error) {
	mid := navmesh.Vec3{(from[0] + to[0]) / 2, (from[1]+to[1])/2 + 30, from[2]}
	return navmesh.Path{Status: navmesh.Complete, Points: []navmesh.Vec3{from, mid, to}, Length: dist3D(from, mid) + dist3D(mid, to)}, nil
}

func TestTraceStopsAPackWalkAtTheLookPoint(t *testing.T) {
	p := &Plan{Name: "t", Map: 1, Legs: []Leg{{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{pg(100, 0)}}}}
	tr, err := TracePlan(p, bendNav{})
	if err != nil {
		t.Fatal(err)
	}
	var look *RouteSeg
	for i := range tr.Segs {
		if tr.Segs[i].Kind == "pack" {
			look = &tr.Segs[i]
		}
	}
	if look == nil || !look.OK {
		t.Fatalf("no pack walk: %+v", tr.Segs)
	}
	// the lead bot looks from LookStandoff short of the pack, not from on it
	end := look.Path[len(look.Path)-1]
	if end != look.To || dist3(end, Point{100, 0, 0}) < float64(LookStandoff)-1 {
		t.Errorf("the walk ends at %v, the look point is %v: %v yd from the pack", end, look.To, dist3(end, Point{100, 0, 0}))
	}
	if look.Path[0] != look.From {
		t.Errorf("the walk starts at %v, not where the party stands %v", look.Path[0], look.From)
	}
	if full := float32(dist3(look.From, Point{50, 30, 0}) + dist3(Point{50, 30, 0}, Point{100, 0, 0})); look.Length >= full {
		t.Errorf("the walk is %v yd, as long as the whole path to the pack (%v)", look.Length, full)
	}
}

func TestTraceMarksDirectAndExemptHops(t *testing.T) {
	p := &Plan{Name: "t", Map: 1, Legs: []Leg{{Name: "a", To: Point{30, 0, 0},
		Via: []Waypoint{{Point: Point{10, 0, 0}, Direct: true}, {Point: Point{20, 0, 0}, CheckSkip: "a scorpid pit"}}}}}
	tr, err := TracePlan(p, fakeNav{detour: map[Point]float32{{10, 0, 0}: 25}})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, sg := range tr.Segs {
		kinds = append(kinds, sg.Kind)
		if sg.Kind == "skip" && (!sg.Exempt || !strings.Contains(sg.Detail, "scorpid") || !sg.OK) {
			t.Errorf("exempt hop: %+v", sg)
		}
	}
	if got := strings.Join(kinds, " "); got != "direct skip stage" {
		t.Errorf("segment kinds %q", got)
	}
}

// A walk the planner cannot path does not turn the walks after it red: each is judged from where the plan means the party to stand, while the
// plain check goes on from the last point it reached.
func TestTraceJudgesEachSegmentFromThePlansOwnPoint(t *testing.T) {
	p := &Plan{Name: "t", Map: 1, Legs: []Leg{{Name: "a", To: Point{60, 0, 0}, Via: []Waypoint{{Point: Point{20, 0, 0}}, {Point: Point{40, 0, 0}}}}}}
	nav := fakeNav{block: func(from, to Point) bool { return to == (Point{20, 0, 0}) }}
	tr, err := TracePlan(p, nav)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Segs) != 3 || tr.Segs[0].OK || !tr.Segs[1].OK || !tr.Segs[2].OK {
		t.Fatalf("segments: %+v", tr.Segs)
	}
	if tr.Segs[1].From != (Point{20, 0, 0}) {
		t.Errorf("the walk after the unreachable point starts at %v, not at the point it follows", tr.Segs[1].From)
	}
	res, err := CheckPlan(p, nav, 0)
	if err != nil || len(res.Issues) != 1 {
		t.Errorf("the plain check: %v %q", err, issueReasons(res.Issues))
	}
}
