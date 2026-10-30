package dungeon

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

// fakeNav is a flat world: every point has floor, a path is the straight line, except where the test says otherwise.
type fakeNav struct {
	off     map[Point]bool            // points with no floor
	partial map[Point]Point           // goal -> where its path ends instead (status partial)
	cut     func(a, b Point) bool     // the ray from a to b leaves the mesh
	block   func(from, to Point) bool // no path from from to to
	detour  map[Point]float32         // goal -> path length (a path longer than the line)
}

func (f fakeNav) Plan(_ uint32, from, to navmesh.Vec3) (navmesh.Path, error) {
	g := Point{to[0], to[1], to[2]}
	if f.off[g] {
		return navmesh.Path{Status: navmesh.NoPath, Reason: "goal is not on the navmesh (no walkable polygon within 5 yd)", Points: []navmesh.Vec3{}}, nil
	}
	if f.block != nil && f.block(Point{from[0], from[1], from[2]}, g) {
		return navmesh.Path{Status: navmesh.NoPath, Reason: "Detour found no path", Points: []navmesh.Vec3{}}, nil
	}
	end := to
	st := navmesh.Complete
	if e, ok := f.partial[g]; ok {
		end, st = navmesh.Vec3{e.X, e.Y, e.Z}, navmesh.Partial
	}
	p := navmesh.Path{Status: st, Points: []navmesh.Vec3{from, end}, Length: dist3D(from, end)}
	if l, ok := f.detour[g]; ok {
		p.Length = l
	}
	return p, nil
}

func dist3D(a, b navmesh.Vec3) float32 {
	return float32(math.Sqrt(float64((a[0]-b[0])*(a[0]-b[0]) + (a[1]-b[1])*(a[1]-b[1]) + (a[2]-b[2])*(a[2]-b[2]))))
}

func (f fakeNav) Raycast(_ uint32, from, to navmesh.Vec3) (navmesh.Ray, error) {
	a, b := Point{from[0], from[1], from[2]}, Point{to[0], to[1], to[2]}
	if f.cut != nil && f.cut(a, b) {
		return navmesh.Ray{Known: true, Hit: 1}, nil
	}
	return navmesh.Ray{Known: true, Clear: true}, nil
}

func (f fakeNav) Nearest(_ uint32, p navmesh.Vec3) (navmesh.Vec3, bool, error) {
	if f.off[Point{p[0], p[1], p[2]}] {
		return navmesh.Vec3{}, false, nil
	}
	return p, true, nil
}

// checkLegs runs the check on a plan made of the given legs (built by hand: Parse would refuse the faults under test).
func checkLegs(t *testing.T, nav Navmesh, from int, legs ...Leg) *PlanCheck {
	t.Helper()
	res, err := CheckPlan(&Plan{Name: "t", Map: 1, Legs: legs}, nav, from)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func issueReasons(is []PlanIssue) string {
	var out []string
	for _, i := range is {
		out = append(out, fmt.Sprintf("%d/%s/%d/%s", i.Leg, i.Part, i.PullGroup, i.Reason))
	}
	return strings.Join(out, " ")
}

func pg(x, y float32) PullGroup {
	return PullGroup{Entries: []uint32{1}, Positions: []Point{{x, y, 0}}}
}

func TestPlanCheckReasons(t *testing.T) {
	nav := fakeNav{
		off:     map[Point]bool{{10, 200, 0}: true},
		partial: map[Point]Point{{10, 150, 0}: {10, 110, 0}}, // 40 yd short: past the look view
	}
	res := checkLegs(t, nav, 0, Leg{
		Name: "a", To: Point{10, 0, 0},
		Via:        []Waypoint{{Point: Point{5, 0, 0}}, {Point: Point{12, 0, 0}, Direct: true}, {Point: Point{40, 0, 0}, Direct: true}},
		PullGroups: []PullGroup{pg(10, 30), pg(10, 90), pg(10, 150)},
		Boss:       &Boss{Entry: 9, Position: Point{10, 200, 0}},
	})
	// 12,0 is a needless direct hop (a straight path of the same length), 40,0 is 28 yd across: too far. Group 3's path ends 40 yd short of
	// it. The boss stands off the mesh.
	if got, want := issueReasons(res.Issues), "0/direct via 2/0/too far 0/pull group look/3/too far 0/boss look/0/off the mesh"; got != want {
		t.Errorf("issues %q, want %q", got, want)
	}
	if got, want := issueReasons(res.Warnings), "0/direct via 1/0/needless direct"; got != want {
		t.Errorf("warnings %q, want %q", got, want)
	}
	if res.OK() || res.Points != 8 {
		t.Errorf("ok %v points %d", res.OK(), res.Points)
	}
	if s := res.Issues[1].String(); !strings.Contains(s, "leg 0 (a) pull group 3 of 3 (from_pull_group 2)") {
		t.Errorf("numbered like dungeon_status: %s", s)
	}
}

func TestPlanCheckDirectHops(t *testing.T) {
	leg := Leg{To: Point{20, 0, 0}, Via: []Waypoint{{Point: Point{10, 0, 0}, Direct: true}, {Point: Point{20, 0, 0}, Direct: true}}}
	// A wall between the two hops: the ray from 10,0 to 20,0 leaves the mesh.
	cut := func(a, b Point) bool { return a.X >= 10 && b.X >= 20 }
	res := checkLegs(t, fakeNav{cut: cut}, 0, leg)
	if got, want := issueReasons(res.Issues), "0/direct via 1/0/no line of sight"; got != want {
		t.Errorf("issues %q, want %q", got, want)
	}
	// A path that winds (twice the line) is a hop the navmesh does not join straight: no warning.
	res = checkLegs(t, fakeNav{detour: map[Point]float32{{10, 0, 0}: 25, {20, 0, 0}: 25}}, 0, leg)
	if !res.OK() || len(res.Warnings) != 0 {
		t.Errorf("issues %q warnings %q", issueReasons(res.Issues), issueReasons(res.Warnings))
	}
}

func TestPlanCheckFromLeg(t *testing.T) {
	nav := fakeNav{off: map[Point]bool{{10, 0, 0}: true, {20, 0, 0}: true}}
	res := checkLegs(t, nav, 1, Leg{Name: "a", To: Point{10, 0, 0}}, Leg{Name: "b", To: Point{20, 0, 0}})
	if got := issueReasons(res.Issues); got != "1/to/0/off the mesh" {
		t.Errorf("%q", got)
	}
}

func TestPlanCheckExemptHop(t *testing.T) {
	leg := Leg{To: Point{40, 0, 0}, Via: []Waypoint{{Point: Point{10, 0, 0}}, {Point: Point{40, 0, 0}, Direct: true, CheckSkip: "a hole in the mesh"}}}
	res := checkLegs(t, fakeNav{}, 0, leg)
	if !res.OK() || len(res.Exempt) != 1 || res.Exempt[0].Reason != ReasonSkipped || res.Exempt[0].Detail != "a hole in the mesh" || len(res.Warnings) != 0 {
		t.Errorf("%+v", res)
	}
	if res.Points != 2 { // via 0 and `to`
		t.Errorf("points %d", res.Points)
	}
}

// The run starts at from_leg / from_pull_group: what comes before is not checked, the pull groups after it are. An explicit group 0 starts
// at the leg's first group (the leg's own walk is skipped); none given checks the walk too.
func TestPlanCheckFromPullGroup(t *testing.T) {
	nav := fakeNav{off: map[Point]bool{{5, 0, 0}: true, {10, 30, 0}: true, {10, 90, 0}: true}}
	leg := Leg{Name: "a", To: Point{5, 0, 0}, PullGroups: []PullGroup{pg(10, 30), pg(10, 90)}}
	zero, one := 0, 1
	for _, c := range []struct {
		from *int
		want string
	}{
		{nil, "0/to/0/off the mesh 0/pull group look/1/off the mesh 0/pull group look/2/off the mesh"},
		{&zero, "0/pull group look/1/off the mesh 0/pull group look/2/off the mesh"},
		{&one, "0/pull group look/2/off the mesh"},
	} {
		res, err := CheckPlanWith(&Plan{Name: "t", Map: 1, Legs: []Leg{leg}}, nav, CheckOptions{FromPullGroup: c.from})
		if err != nil {
			t.Fatal(err)
		}
		if got := issueReasons(res.Issues); got != c.want {
			t.Errorf("from group %v: %q, want %q", c.from, got, c.want)
		}
	}
}

// A start the plan does not have is an error, not an empty pass.
func TestPlanCheckRefusesABadStart(t *testing.T) {
	plan := &Plan{Name: "t", Map: 1, Legs: []Leg{{Name: "a", To: Point{5, 0, 0}, PullGroups: []PullGroup{pg(10, 30)}}, {Name: "b", To: Point{9, 0, 0}}}}
	two, neg, one := 2, -1, 1
	for _, opt := range []CheckOptions{{FromLeg: 2}, {FromLeg: -1}, {FromPullGroup: &two}, {FromPullGroup: &neg}, {FromLeg: 1, FromPullGroup: &one}, {FromLeg: 1, FromPullGroup: new(int)}} {
		if _, err := CheckPlanWith(plan, fakeNav{}, opt); !errors.Is(err, ErrCheckStart) {
			t.Errorf("%+v: %v", opt, err)
		}
	}
	if _, err := CheckPlanWith(plan, fakeNav{}, CheckOptions{FromLeg: 1}); err != nil {
		t.Error(err)
	}
}

// A run that starts at a pull group is checked from the gather spot the driver uses (Plan.GatherSpot), not from the look point of the
// group before it: where the two routes disagree, the gather spot's wins.
func TestPlanCheckStartsAtTheGatherSpot(t *testing.T) {
	plan := &Plan{Name: "t", Map: 1, Legs: []Leg{{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{pg(0, 100), pg(0, 200)}}}}
	one := 1
	spot, _ := plan.GatherSpot(0, 1, nil)
	if spot.Y != 145 { // 55 yd short of the group on the straight line
		t.Fatalf("gather spot %v", spot)
	}
	// The way to group 2 is open from the gather spot, closed from group 1's look point: a run from the plan's start fails there, a run
	// at group 2 does not.
	fromSpotOpen := fakeNav{block: func(from, to Point) bool { return to.Y == 200 && from.Y < 120 }}
	for _, c := range []struct {
		from *int
		want string
	}{{nil, "0/pull group look/2/unreachable"}, {&one, ""}} {
		res, err := CheckPlanWith(plan, fromSpotOpen, CheckOptions{FromPullGroup: c.from})
		if err != nil {
			t.Fatal(err)
		}
		if got := issueReasons(res.Issues); got != c.want {
			t.Errorf("from group %v, route open from the gather spot only: %q, want %q", c.from, got, c.want)
		}
	}
	// And the other way round: closed from the gather spot, open from group 1's look point.
	fromSpotClosed := fakeNav{block: func(from, to Point) bool { return to.Y == 200 && from.Y > 120 }}
	for _, c := range []struct {
		from *int
		want string
	}{{nil, ""}, {&one, "0/pull group look/2/unreachable"}} {
		res, err := CheckPlanWith(plan, fromSpotClosed, CheckOptions{FromPullGroup: c.from})
		if err != nil {
			t.Fatal(err)
		}
		if got := issueReasons(res.Issues); got != c.want {
			t.Errorf("from group %v, route closed from the gather spot: %q, want %q", c.from, got, c.want)
		}
	}
}

// A run from a leg starts where the leg before ended (Plan.LegStart: its boss), not at the simulated look point of its last pack.
func TestPlanCheckStartsAtTheLegStart(t *testing.T) {
	plan := &Plan{Name: "t", Map: 1, Legs: []Leg{
		{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{pg(0, 100)}, Boss: &Boss{Entry: 9, Position: Point{0, 300, 0}}},
		{Name: "b", To: Point{0, 400, 0}},
	}}
	nav := fakeNav{block: func(from, to Point) bool { return to.Y == 400 && from.Y != 300 }}
	res, err := CheckPlanWith(plan, nav, CheckOptions{FromLeg: 1})
	if err != nil || !res.OK() {
		t.Errorf("%v %q", err, issueReasons(res.Issues))
	}
}

// A pull group or boss gated to a team is checked like the rest: the remote driver looks at a part of the other team (gatedLook) before it
// skips the fight, so its look must work too. The issues carry the team.
func TestPlanCheckTeamGatedPartsAreLookedAt(t *testing.T) {
	nav := fakeNav{off: map[Point]bool{{10, 30, 0}: true, {10, 200, 0}: true}}
	leg := Leg{Name: "a", To: Point{0, 0, 0},
		PullGroups: []PullGroup{{Entries: []uint32{1}, Team: TeamHorde, Positions: []Point{{10, 30, 0}}}},
		Boss:       &Boss{Entry: 9, Team: TeamHorde, Position: Point{10, 200, 0}}}
	res := checkLegs(t, nav, 0, leg)
	if got, want := issueReasons(res.Issues), "0/pull group look/1/off the mesh 0/boss look/0/off the mesh"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
	for _, is := range res.Issues {
		if is.Team != TeamHorde || !strings.Contains(is.String(), "[horde only") {
			t.Errorf("not labelled: %s", is)
		}
	}
}

// The stand points of the steps are walked before the boss: an unreachable stand is reported, and the boss is looked at from the last one.
func TestPlanCheckWalksTheStepStands(t *testing.T) {
	stand, bad, chest := Point{0, 150, 0}, Point{0, 160, 0}, Point{0, 400, 0}
	leg := Leg{Name: "a", To: Point{0, 0, 0},
		Steps: []Step{
			{UseObject: &UseObject{ObjectRef: ObjectRef{Entry: 1}, Stand: &stand}},
			{LootObject: &LootObject{ObjectRef: ObjectRef{Entry: 2}}}, // no stand: nothing to walk
			{Ritual: &Ritual{Entry: 3, Participants: 3, Stand: &bad}},
		},
		Boss:      &Boss{Entry: 9, Position: Point{0, 250, 0}},
		AfterBoss: []Step{{LootObject: &LootObject{ObjectRef: ObjectRef{Entry: 4}, Stand: &chest}}}}
	nav := fakeNav{off: map[Point]bool{bad: true, chest: true}}
	res := checkLegs(t, nav, 0, leg)
	if got, want := issueReasons(res.Issues), "0/steps[2].ritual.stand/0/off the mesh 0/after_boss[0].loot_object.stand/0/off the mesh"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
	// The boss is looked at from the last stand the party reached (0,150): the path to it is open only from there.
	nav = fakeNav{block: func(from, to Point) bool { return to.Y == 250 && from.Y != 150 }}
	leg.AfterBoss = nil
	leg.Steps = leg.Steps[:1]
	if res := checkLegs(t, nav, 0, leg); !res.OK() {
		t.Errorf("the boss was not looked at from the step's stand: %s", res.Summary())
	}
}

// A point the plan marks needs_walk was never snapped to the navmesh: it is a failing point (reached by the check), not a silent skip.
func TestPlanCheckFailsNeedsWalkPoints(t *testing.T) {
	leg := Leg{Name: "a", To: Point{5, 0, 0}, NeedsWalk: true,
		PullGroups: []PullGroup{{Entries: []uint32{1}, Positions: []Point{{10, 30, 0}}, NeedsWalk: true}, pg(10, 60)},
		Boss:       &Boss{Entry: 9, Position: Point{10, 200, 0}, NeedsWalk: true}}
	res := checkLegs(t, fakeNav{}, 0, leg)
	if got, want := issueReasons(res.Issues), "0/to/0/needs a mapping walk 0/pull group look/1/needs a mapping walk 0/boss look/0/needs a mapping walk"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
	// A run that starts past them does not reach them.
	one := 1
	res, err := CheckPlanWith(&Plan{Name: "t", Map: 1, Legs: []Leg{leg}}, fakeNav{}, CheckOptions{FromPullGroup: &one})
	if err != nil || issueReasons(res.Issues) != "0/boss look/0/needs a mapping walk" {
		t.Errorf("%v %q", err, issueReasons(res.Issues))
	}
}

// The positions of a group are looked at in order, each look starting where the one before ended (confirmGone), and the next group goes on
// from the last: a position can be open from the look point of the one before it but closed from the point before the group, and a group
// open from the last look point but closed from the first.
func TestPlanCheckLooksAtPositionsInSequence(t *testing.T) {
	plan := &Plan{Name: "t", Map: 1, Legs: []Leg{{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{
		{Entries: []uint32{1, 1}, Positions: []Point{{0, 100, 0}, {0, 200, 0}}}, pg(0, 300)}}}}
	// Position 1's look point is 21 yd short of it, at y 79; position 2's at y 179.
	nav := fakeNav{block: func(from, to Point) bool {
		return (to.Y == 200 && from.Y < 20) || (to.Y == 300 && from.Y > 70 && from.Y < 90)
	}}
	res, err := CheckPlanWith(plan, nav, CheckOptions{})
	if err != nil || !res.OK() {
		t.Errorf("%v: position 2 was looked at from the point before the group, or group 2 from the first look point: %q", err, issueReasons(res.Issues))
	}
	// And the other way round, each fails when its own start is closed.
	nav = fakeNav{block: func(from, to Point) bool {
		return (to.Y == 200 && from.Y > 70 && from.Y < 90) || (to.Y == 300 && from.Y > 170 && from.Y < 190)
	}}
	res, err = CheckPlanWith(plan, nav, CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := issueReasons(res.Issues), "0/pull group look/1/unreachable"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
	// A position within the view of the party needs no look (a bot that far from it that sees none is the evidence).
	near := &Plan{Name: "t", Map: 1, Legs: []Leg{{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{1, 1}, Positions: []Point{{0, 100, 0}, {0, 90, 0}}}}}}}
	res, err = CheckPlanWith(near, fakeNav{block: func(from, to Point) bool { return to.Y == 90 }}, CheckOptions{})
	if err != nil || !res.OK() {
		t.Errorf("%v %q", err, issueReasons(res.Issues))
	}
}

// The tuned look counts: a pack off the mesh whose path ends 17 yd short of it is in view from the end with the default look (view 21), not
// with look_arrive 10 (the lead bot may stop 10 yd short of the look point: view 14) or look_standoff 12 (view 12).
func TestPlanCheckTunedLook(t *testing.T) {
	nav := fakeNav{partial: map[Point]Point{{0, 60, 0}: {0, 43, 0}}}
	plan := &Plan{Name: "t", Map: 1, Legs: []Leg{{To: Point{0, 0, 0}, PullGroups: []PullGroup{pg(0, 60)}}}}
	for _, c := range []struct {
		opt  CheckOptions
		want string
	}{{CheckOptions{}, ""}, {CheckOptions{LookArrive: 10}, "0/pull group look/1/too far"}, {CheckOptions{LookStandoff: 12}, "0/pull group look/1/too far"}} {
		res, err := CheckPlanWith(plan, nav, c.opt)
		if err != nil {
			t.Fatal(err)
		}
		if got := issueReasons(res.Issues); got != c.want {
			t.Errorf("%+v: %q, want %q", c.opt, got, c.want)
		}
	}
}

// Velratha's pull group 9 (ZF batch 2, Haiku s2): the pack at 1636 1105 stands off the mesh, the path ends at 1625.6 1109.3, and the look
// point 21 yd back from there was 31 yd from the pack (the live move ended at 1605.47 1104.17, the run stopped on "could not get a view").
// LookPoint moves it up the path to the first point within the view.
func TestLookPointStaysWithinView(t *testing.T) {
	pack := Point{1636, 1105, 9}
	path := []Point{{1591.76, 1085.92, 9.2}, {1606.93, 1105.33, 8.9}, {1625.6, 1109.33, 9.6}}
	old, ok := StandoffPoint(path, LookStandoff)
	if !ok || math.Abs(float64(old.X-1605.475)) > 1 || math.Abs(float64(old.Y-1104.167)) > 1 || dist3(old, pack) < 29 {
		t.Fatalf("the old look point %v (ok %v) should be the live 1605.47 1104.17, 30 yd from the pack", old, ok)
	}
	view := lookView(LookStandoff, lookArrive)
	if view != 21 {
		t.Fatalf("view %v", view)
	}
	got, ok := LookPoint(path, pack, LookStandoff, view, nil)
	// The lead bot may stop lookArrive short of the point and lookAt drops frames beyond ClearSeeRadius: the point leaves room for both.
	if !ok || dist3(got, pack) > ClearSeeRadius-lookArrive || dist3(got, pack) < float64(view)-1 {
		t.Errorf("the look point %v is %.1f yd from the pack (ok %v): want %v, at most %d", got, dist3(got, pack), ok, view, ClearSeeRadius-lookArrive)
	}
	if got.X < old.X { // on the path and nearer its end than the old point
		t.Errorf("%v is not past %v", got, old)
	}
	// A pack on a complete path keeps the old standoff point; a look point already in view is left alone.
	onPath := Point{1625.6, 1109.33, 9.6}
	if want, _ := StandoffPoint(path, LookStandoff); func() Point { p, _ := LookPoint(path, onPath, LookStandoff, view, nil); return p }() != want {
		t.Errorf("a pack at the path's end moved the look point")
	}
	near := Point{1622, 1108, 9.6}
	if want, _ := StandoffPoint(path, LookStandoff); func() Point { p, _ := LookPoint(path, near, LookStandoff, view, nil); return p }() != want {
		t.Errorf("a look point in view moved")
	}
	// A path that never comes within view gains nothing by moving: the old result, the standoff back from the path's end, with its ok.
	far := Point{1700, 1300, 9}
	wantP, wantOK := StandoffPoint(path, LookStandoff)
	if p, ok := LookPoint(path, far, LookStandoff, view, nil); ok != wantOK || p != wantP {
		t.Errorf("out of view everywhere: %v %v, want the standoff point %v %v", p, ok, wantP, wantOK)
	}
	// A path shorter than the standoff leaves the lead bot where it is when the pack is in view from its start.
	if _, ok := LookPoint(path[1:], Point{1622, 1108, 9.6}, 40, view, nil); ok {
		t.Errorf("a path shorter than the standoff moved a lead bot that is in view")
	}
	// The tuned look: a longer arrival slack pulls the point nearer, a standoff under the margin stays the limit.
	if v := lookView(LookStandoff, 8); v != 16 {
		t.Errorf("look_arrive 8: view %v", v)
	}
	if v := lookView(15, lookArrive); v != 15 {
		t.Errorf("look_standoff 15: view %v", v)
	}
}

// Velratha's group 9, positions 4 and 5 (1644.0 1105.0 and 1644.2 1107.4), looked at from the last stand of the positions before them
// (1615.2 1107.3): the path to the pack is 14 yd, shorter than the 21 yd standoff, so StandoffPoint gives none and the lead bot stayed 29 yd
// away ("could not get a view of the pack"). It now moves up the path into view.
func TestLookPointMovesWhenTheShortPathLeavesTheLeadOutOfView(t *testing.T) {
	path := []Point{{1615.2, 1107.3, 8.9}, {1629.0, 1119.2, 9.6}}
	view := lookView(LookStandoff, lookArrive)
	for _, pack := range []Point{{1644.0, 1105.0, 9.2}, {1644.2, 1107.4, 9.0}} {
		if _, ok := StandoffPoint(path, LookStandoff); ok {
			t.Fatal("the path is meant to be shorter than the standoff")
		}
		if d := dist3(path[0], pack); d < 28 {
			t.Fatalf("the lead bot is %.1f yd from %v", d, pack)
		}
		got, ok := LookPoint(path, pack, LookStandoff, view, nil)
		if !ok || dist3(got, pack) > float64(view) {
			t.Errorf("pack %v: look point %v (ok %v) is %.1f yd away, want within %v", pack, got, ok, dist3(got, pack), view)
		}
		if dist3(got, path[0]) == 0 || dist3(got, path[1]) > dist3(path[0], path[1]) {
			t.Errorf("pack %v: %v is not on the path past its start", pack, got)
		}
	}
}

// The look point is sought every half yard along the path and at each segment's end: a path that comes into view only in the last
// fraction of a segment (a length that is no multiple of 0.5) still gets its end as the look point, instead of the old standoff.
func TestLookPointSeesTheLastFractionOfASegment(t *testing.T) {
	// A 10.3 yd segment ending exactly view yd from the pack (21): the samples at 0, 0.5, ... 10.0 are all farther, only the end is in.
	pack := Point{0, 31.3, 0}
	path := []Point{{0, 0, 0}, {0, 10.3, 0}}
	view := float32(21)
	if d := dist3(path[1], pack); d > float64(view) {
		t.Fatalf("the end is %.2f yd from the pack", d)
	}
	if d := dist3(Point{0, 10, 0}, pack); d <= float64(view) {
		t.Fatalf("the sample before the end is in view (%.2f yd): the test needs it out", d)
	}
	got, ok := LookPoint(path, pack, 40, view, nil)
	if !ok || got != path[1] {
		t.Errorf("look point %v (ok %v), want the segment's end %v", got, ok, path[1])
	}
	// Several segments, the entry on the end of the second.
	path = []Point{{0, 0, 0}, {0, 5.2, 0}, {0, 10.3, 0}}
	if got, ok := LookPoint(path, pack, 40, view, nil); !ok || got != path[2] {
		t.Errorf("look point %v (ok %v), want %v", got, ok, path[2])
	}
}

// oldLook is the look point before LookPoint: the standoff counted back from the path's end.
func oldLook(path []Point, _ Point, standoff, _ float32, _ func(Point) Point) (Point, bool) {
	return StandoffPoint(path, standoff)
}

func zfNav(t *testing.T) *navmesh.Planner {
	t.Helper()
	dir := zulfarrakMmaps()
	if dir == "" {
		t.Skip("no map 209 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 209.mmap and the 209*.mmtile tiles")
	}
	pl := navmesh.NewPlanner(dir)
	t.Cleanup(pl.Close)
	return pl
}

func skipNoEngine(t *testing.T, err error) {
	t.Helper()
	if errors.Is(err, navmesh.ErrNoEngine) {
		t.Skip("built without cgo: no navmesh engine")
	}
	if err != nil {
		t.Fatal(err)
	}
}

// The shipped Zul'Farrak plan passes the plan check with nothing to warn of (issue #187).
func TestZulfarrakPlanCheck(t *testing.T) {
	pl := zfNav(t)
	for _, f := range []string{zulfarrakPlan} {
		p := loadPlan(t, f)
		res, err := CheckPlan(p, pl, 0)
		skipNoEngine(t, err)
		if !res.OK() || len(res.Warnings) != 0 {
			t.Errorf("%s:\n%s\n%s", p.Name, res.Summary(), res.WarningSummary())
		}
		if p.Name == "zulfarrak" && res.Points < 100 {
			t.Errorf("only %d points checked", res.Points)
		}
		// A run from a pull group starts at its gather spot: the same plan passes from the groups of the Velratha legs (legs 7 and 8, split at
		// the pack of five at 1636 1105 by the route edits of 2026-10-16) and from a leg start.
		if p.Name == "zulfarrak" {
			for _, from := range []struct{ leg, group int }{{7, 0}, {7, 5}, {8, 0}, {8, 2}} {
				g := from.group
				res, err := CheckPlanWith(p, pl, CheckOptions{FromLeg: from.leg, FromPullGroup: &g})
				if err != nil || !res.OK() {
					t.Errorf("from leg %d pull group %d: %v\n%s", from.leg, g, err, res.Summary())
				}
			}
			if res, err := CheckPlanWith(p, pl, CheckOptions{FromLeg: 3}); err != nil || !res.OK() {
				t.Errorf("from leg 3: %v\n%s", err, res.Summary())
			}
		}
	}
}

// The plan as it shipped before the check (testdata, sha256 c86c1e62ee5a...) fails it where the batch-2 runs stopped, and warns of the direct hops
// that the navmesh joins. With the look point of that time (oldLook) Velratha's pull group 9 is "too far"; LookPoint passes it.
func TestZulfarrakOldPlanFailsCheck(t *testing.T) {
	pl := zfNav(t)
	old := loadPlan(t, filepath.Join("testdata", "zulfarrak-c86c1e62ee5a.json"))
	res, err := checkPlan(old, pl, CheckOptions{}, oldLook)
	skipNoEngine(t, err)
	var gp9 int
	for _, is := range res.Issues {
		if is.Leg == 8 && is.PullGroup == 9 && is.PullGroups == 12 && is.Reason == ReasonTooFar {
			gp9++
		}
	}
	if gp9 != 5 { // its five positions, each looked at from the same stand
		t.Errorf("pull group 9 of 12 fails %d times as too far, want 5:\n%s", gp9, res.Summary())
	}
	if !strings.Contains(res.Summary(), "1605.5 1104.2") {
		t.Errorf("the look point should be the live 1605.5 1104.2:\n%s", res.Summary())
	}
	// The hop to 1433.9 804.7 needed a ray the mesh cannot give (it bends round the ledge), and the plain walk to it is complete.
	if !strings.Contains(res.Summary(), "leg 2 (antusul_a3) direct via 1 1433.9 804.7 10.3: no line of sight") {
		t.Errorf("leg 2's first direct hop should fail:\n%s", res.Summary())
	}
	wantWarn := map[string]bool{"1/direct via 1": true, "1/direct via 2": true, "2/direct via 2": true, "2/direct via 3": true, "4/direct via 1": true, "4/direct via 2": true}
	for _, w := range res.Warnings {
		delete(wantWarn, fmt.Sprintf("%d/%s", w.Leg, w.Part))
	}
	if len(wantWarn) != 0 {
		t.Errorf("no needless-direct warning for %v:\n%s", wantWarn, res.WarningSummary())
	}
	// With LookPoint the group passes; the rest of the old plan's failure is the leg 2 hop alone.
	res, err = CheckPlan(old, pl, 0)
	skipNoEngine(t, err)
	if len(res.Issues) != 1 || res.Issues[0].Leg != 2 {
		t.Errorf("after the look point fix:\n%s", res.Summary())
	}
}

// planCheckKnownFailing lists the shipped plans that fail the plan check today, with the failing points counted when the check came in. Issue #187
// follows them up one by one (most are drafts from the world export, whose points were never walked; blackfathom_deeps, razorfen_downs, scarlet_graveyard, stratholme_undead and temple_of_ahnqiraj fail only on their needs_walk points; Uldaman's two direct hops over the scorpid pit are exempt in the plan itself (check_skip)). Take a plan off the list when it is fixed;
// Zul'Farrak is not on it. wailing_caverns-extra is the Naralex event draft (#276): the offline walk from the foot to the gossip stand is
// complete and pinned (TestNaralexReturnHop), but the server's PathFinder stops 205 yd short on the lower mesh, and the draft is not
// exported to the run leader.
var planCheckKnownFailing = map[string]bool{
	"temple_of_ahnqiraj":    true,
	"stratholme_undead":     true,
	"scarlet_graveyard":     true,
	"razorfen_downs":        true,
	"blackfathom_deeps":     true,
	"black_morass":          true,
	"blackrock_depths":      true,
	"crescent_grove":        true,
	"dire_maul_east":        true,
	"dire_maul_west":        true,
	"dragonmaw_retreat":     true,
	"gilneas_city":          true,
	"hateforge_quarry":      true,
	"karazhan_crypt":        true,
	"lower_blackrock_spire": true,
	"maraudon_orange":       true,
	"maraudon_purple":       true,
	"molten_core":           true,
	"naxxramas":             true,
	"shadowfang_keep":       true,
	"stormwrought_ruins":    true,
	"sunken_temple":         true,
	"upper_blackrock_spire": true,
	"upper_karazhan_halls":  true,
	"wailing_caverns-extra": true,
	"windhorn_canyon":       true,
	"zul_gurub":             true,
}

// Every shipped plan whose map has navmesh files here passes the plan check, except the ones listed in planCheckKnownFailing.
func TestShippedPlansPassPlanCheck(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(plansDir, "*.json"))
	var missing, failing []string
	checked := 0
	for _, f := range files {
		p, err := Load(f)
		if err != nil {
			t.Fatal(err)
		}
		dir := mapMmaps(p.Map)
		if dir == "" {
			missing = append(missing, p.Name)
			continue
		}
		pl := navmesh.NewPlanner(dir)
		res, err := CheckPlan(p, pl, 0)
		pl.Close()
		skipNoEngine(t, err)
		checked++
		switch {
		case res.OK() && planCheckKnownFailing[p.Name]:
			t.Errorf("%s passes the plan check now: remove %s from planCheckKnownFailing", p.Name, p.Name)
		case res.OK():
		case planCheckKnownFailing[p.Name]:
			failing = append(failing, fmt.Sprintf("%s (%d of %d)", p.Name, len(res.Issues), res.Points))
		default:
			t.Errorf("%s: %d of %d points fail:\n%s", p.Name, len(res.Issues), res.Points, res.Summary())
		}
	}
	if checked == 0 {
		t.Skip("no navmesh files for any shipped plan's map here")
	}
	t.Logf("%d plans checked; known failing (planCheckKnownFailing, #187): %s; no navmesh files for %s", checked, strings.Join(failing, ", "), strings.Join(missing, ", "))
}

// shapeNav is a navmesh whose paths and floor the test shapes: path gives the corners from a point to a pack, floor the mesh floor under a
// point (the way a path over stairs or a drop leaves the interpolated point floating); a goal in off has no path.
type shapeNav struct {
	path  func(from, to Point) []Point
	floor func(Point) Point
	off   map[Point]bool
}

func (s shapeNav) Plan(_ uint32, from, to navmesh.Vec3) (navmesh.Path, error) {
	f, g := Point{from[0], from[1], from[2]}, Point{to[0], to[1], to[2]}
	if s.off[g] {
		return navmesh.Path{Status: navmesh.NoPath, Reason: "goal is not on the navmesh", Points: []navmesh.Vec3{}}, nil
	}
	pts := s.path(f, g)
	out := make([]navmesh.Vec3, len(pts))
	for i, p := range pts {
		out[i] = navmesh.Vec3{p.X, p.Y, p.Z}
	}
	return navmesh.Path{Status: navmesh.Complete, Points: out, Length: 1}, nil
}

func (s shapeNav) Raycast(uint32, navmesh.Vec3, navmesh.Vec3) (navmesh.Ray, error) {
	return navmesh.Ray{Known: true, Clear: true}, nil
}

func (s shapeNav) Nearest(_ uint32, p navmesh.Vec3) (navmesh.Vec3, bool, error) {
	q := s.floor(Point{p[0], p[1], p[2]})
	return navmesh.Vec3{q.X, q.Y, q.Z}, true, nil
}

// lookFake is a fakeRemote whose game also plans paths and snaps to the floor of a shapeNav.
type lookFake struct {
	*fakeRemote
	nav shapeNav
}

func (g lookFake) PlanPath(_ context.Context, _ uint32, from, to Point) ([]Point, error) {
	return g.nav.path(from, to), nil
}

func (g lookFake) SnapToFloor(_ context.Context, _ uint32, p Point) (Point, bool) {
	return g.nav.floor(p), true
}

func lookDriver(t *testing.T, nav shapeNav, anchors ...Point) (*RemoteDriver, lookFake, fightSpec) {
	t.Helper()
	g := lookFake{newFakeRemote(t), nav}
	g.sight = 5
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: anchors}}})
	d := NewRemoteDriver(plan, g, DefaultConfig(), opts(), g.clock())
	if err := d.begin(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	d.curLeg = 0
	return d, g, fightSpec{pick: []uint32{299}, track: []uint32{299}, anchors: anchors}
}

// The driver walks the look point the plan check checks, snapped to the mesh floor by the same LookPoint: a path over stairs whose
// interpolated points float 6 yd above the floor. The runtime target equals the From point of the check's next look.
func TestLookTargetIsTheSameAtRuntimeAndInThePlanCheck(t *testing.T) {
	packA, packB := Point{0, 30, 0}, Point{0, 500, 0}
	nav := shapeNav{
		floor: func(p Point) Point { return Point{p.X, p.Y, 0} },
		off:   map[Point]bool{packB: true},
		path: func(from, to Point) []Point {
			if to == packA { // stairs: the straight path climbs 12 yd over 16 yd, the floor under it is flat
				return []Point{from, {from.X, from.Y + 10, from.Z + 6}, {from.X, from.Y + 16, from.Z + 12}}
			}
			return []Point{from, to}
		},
	}
	d, g, sp := lookDriver(t, nav, packA)
	if _, _, err := d.confirmGone(context.Background(), sp, newClearing()); err != nil {
		t.Logf("confirmGone: %v", err) // the fake lead bot sees nothing from there; the walk is what is under test
	}
	var run Point
	for _, to := range g.moveOrderDest {
		if to.Y > 1 {
			run = to
		}
	}
	if run == (Point{}) || run.Z != 0 {
		t.Fatalf("the lead bot was sent to %v (all: %v): the look point should be on the floor (z 0)", run, g.moveOrderDest)
	}
	plan := &Plan{Name: "t", Map: 1, Legs: []Leg{{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299, 299}, Positions: []Point{packA, packB}}}}}}
	res, err := CheckPlanWith(plan, nav, CheckOptions{})
	if err != nil || len(res.Issues) != 1 || res.Issues[0].Reason != ReasonOffMesh {
		t.Fatalf("%v %v", err, res.Issues)
	}
	if from := res.Issues[0].From; dist3(from, run) > 1 {
		t.Errorf("the check's look point %v is not the runtime target %v", from, run)
	}
}

// A pack no point of the path gets within view of: the approach itself does not chase the path's end (the look point is the old one, the standoff
// back from the path's end, or none when the path is shorter, and the check reports the pack as too far); the run then walks the lead bot
// straight toward the pack (confirmGone, lookWalkTries) instead of failing "could not get a view" with no order sent (zf-r4-s4, Velratha group
// 11), so the lead bot ends within view of the pack.
func TestLookDoesNotChaseAPathThatNeverGetsInView(t *testing.T) {
	pack := Point{0, 100, 0}
	for _, c := range []struct {
		name string
		end  float32
		want []Point // the moves the approach alone gives the lead bot (the first moves)
	}{{"long path", 40, []Point{{0, 19, 0}}}, {"short path", 10, nil}} {
		nav := shapeNav{floor: func(p Point) Point { return p }, path: func(from, to Point) []Point {
			if to == pack {
				return []Point{from, {from.X, from.Y + c.end, from.Z}}
			}
			return []Point{from, to}
		}}
		d, g, sp := lookDriver(t, nav, pack)
		_, _, _ = d.confirmGone(context.Background(), sp, newClearing())
		var got []Point
		for _, to := range g.moveOrderDest {
			if to.Y > 1 {
				got = append(got, to)
			}
		}
		if len(got) < len(c.want) || (len(c.want) == 1 && dist3(got[0], c.want[0]) > 0.5) {
			t.Errorf("%s: the lead bot was sent to %v, want first %v", c.name, got, c.want)
		}
		if l := g.lead(); dist3(Point{l.X, l.Y, l.Z}, pack) > float64(ClearSeeRadius) {
			t.Errorf("%s: the lead bot ends %.0f yd from the pack (moves %v)", c.name, dist3(Point{l.X, l.Y, l.Z}, pack), got)
		}
		plan := &Plan{Name: "t", Map: 1, Legs: []Leg{{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{pack}}}}}}
		res, err := CheckPlanWith(plan, nav, CheckOptions{})
		if err != nil || issueReasons(res.Issues) != "0/pull group look/1/too far" {
			t.Errorf("%s: %v %q", c.name, err, issueReasons(res.Issues))
		}
	}
}
