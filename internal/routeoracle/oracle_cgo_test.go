//go:build cgo

package routeoracle

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/navmesh/navfixture"
)

// The fixtures are synthetic meshes (navfixture's L corridor: a row along x from 10 to 50 at
// y 10..14, then a column up y to 50 at x 46..50) plus, where a test needs one, a detached
// island of cells at (100..120). No real mmaps are needed.
const walkPlan = `{"version":1,"name":"corridor","map":0,"entrance":{"x":12,"y":12,"z":0},"legs":[
 {"name":"hall","to":{"x":46,"y":12,"z":0},
  "via":[{"x":22,"y":12,"z":0},{"x":30,"y":12,"z":0,"direct":true,"check_skip":"fixture"}],
  "pull_groups":[{"entries":[100],"positions":[{"x":44,"y":12,"z":0}]}]},
 {"name":"boss","to":{"x":48,"y":30,"z":0},
  "pull_groups":[{"entries":[103],"approach":[{"x":48,"y":20,"z":0}],"positions":[{"x":48,"y":40,"z":0}]}],
  "boss":{"entry":200,"position":{"x":48,"y":48,"z":0}}},
 {"name":"voidpull","to":{"x":48,"y":48,"z":0},
  "pull_groups":[{"entries":[102],"positions":[{"x":200,"y":200,"z":0}]}]},
 {"name":"after","to":{"x":46,"y":12,"z":0},
  "pull_groups":[{"entries":[105],"positions":[{"x":46,"y":12,"z":0}]}]}]}`

// A second plan whose third leg walks to a detached island: the hop fails PARTIAL, and the
// hop after it is planned on from the point the partial path reached.
const islandPlan = `{"version":1,"name":"island","map":0,"entrance":{"x":12,"y":12,"z":0},"legs":[
 {"name":"hall","to":{"x":46,"y":12,"z":0},
  "pull_groups":[{"entries":[100],"positions":[{"x":44,"y":12,"z":0}]}]},
 {"name":"island","to":{"x":102,"y":102,"z":0},
  "via":[{"x":48,"y":20,"z":0}],
  "pull_groups":[{"entries":[104],"positions":[{"x":110,"y":110,"z":0}]}]},
 {"name":"after","to":{"x":46,"y":12,"z":0},
  "pull_groups":[{"entries":[105],"positions":[{"x":46,"y":12,"z":0}]}]}]}`

func oracleFixture(t *testing.T, plan string, extra ...navfixture.Poly) (*navmesh.Planner, string, *dungeon.Plan) {
	t.Helper()
	polys := navfixture.LCorridor(0)
	polys = append(polys, extra...)
	dir := navfixture.WriteDir(t, 0, polys)
	p := navmesh.NewPlanner(dir)
	t.Cleanup(p.Close)
	name := "corridor.json"
	if strings.Contains(plan, `"name":"island"`) {
		name = "island.json"
	}
	dp, err := dungeon.Parse(name, []byte(plan))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return p, dir, dp
}

// The walk the oracle plans is the driver's own, hop by hop: the boss leg holds its To, the
// pull group's approach point and pull centre, and the boss, and every hop of it plans
// complete, so the leg's stored length and its live length agree (ratio ~1).
func TestOraclePlansTheDriversWalk(t *testing.T) {
	p, dir, plan := oracleFixture(t, walkPlan)
	rep, err := Oracle(p, dir, plan)
	if err != nil {
		t.Fatalf("Oracle: %v", err)
	}
	if len(rep.Legs) != 4 {
		t.Fatalf("legs: %d", len(rep.Legs))
	}
	// Leg 0: the hall with its via points; hop 2's via is direct with the plan's own
	// check_skip, so the oracle does not walk it on the mesh (skipped) and the hop after it
	// plans on from the skipped point.
	lr := rep.Legs[0]
	if lr.Status != "complete" || lr.Objective != "to" {
		t.Errorf("leg 0: status %s objective %s", lr.Status, lr.Objective)
	}
	if len(lr.Segments) != 4 {
		t.Fatalf("leg 0 hops: %d", len(lr.Segments))
	}
	wantStatus := []string{"complete", "skipped", "complete", "complete"}
	for i := range lr.Segments {
		s := lr.Segments[i]
		if s.NonComparable || s.Avoid.Status != wantStatus[i] {
			t.Errorf("leg 0 hop %d: status %s comparable %v, want %s", i+1, s.Avoid.Status, !s.NonComparable, wantStatus[i])
		}
	}
	// The skipped hop is the leg's break waypoint: listed (not walked on the mesh), and
	// touched — the route brings the party onto its foot, from where the direct step
	// launches.
	if len(lr.Breaks) != 1 || lr.Breaks[0].Kind != "direct,check_skip" || !lr.Breaks[0].Touched {
		t.Errorf("leg 0 breaks: %+v", lr.Breaks)
	}
	// Leg 1: the boss leg with To and pulls — the case the review named. Its hops are the
	// driver's sequence (to, approach, pull, boss), all complete, so the stored hops and the
	// live route agree; a report that planned the boss directly instead would leave the
	// stored chain (which holds the to, the approach and the pull) far longer than the live
	// walk and fail this bound.
	br := rep.Legs[1]
	if br.Status != "complete" || br.Objective != "boss" {
		t.Errorf("leg 1: status %s objective %s", br.Status, br.Objective)
	}
	kinds := []string{}
	for i := range br.Segments {
		kinds = append(kinds, br.Segments[i].Kind)
		if s := br.Segments[i]; s.NonComparable || s.Avoid.Status != "complete" {
			t.Errorf("leg 1 hop %d: %+v", i+1, s)
		}
	}
	if strings.Join(kinds, ",") != "to,approach,pull,boss" {
		t.Errorf("leg 1 hop kinds %v", kinds)
	}
	if br.Ratio < 0.9 || br.Ratio > 1.15 {
		t.Errorf("leg 1 ratio %v, want ~1 (the live route goes through the stored points)", br.Ratio)
	}
	if br.StoredMeanYd > 1 {
		t.Errorf("leg 1 stored mean %.1f yd, want ~0 (the route visits every stored point)", br.StoredMeanYd)
	}
	// Leg 2's single hop to the void pull centre (200, 200) finds no mesh there: no_path,
	// and it fails the leg.
	vp := rep.Legs[2]
	if vp.Status != string(navmesh.NoPath) {
		t.Errorf("leg 2 status %s, want no_path", vp.Status)
	}
	if len(vp.Segments) != 2 || vp.Segments[1].Kind != "pull" || vp.Segments[1].Avoid.Status != string(navmesh.NoPath) || vp.Segments[1].NonComparable {
		t.Errorf("leg 2 hops: %+v", vp.Segments)
	}
	// Leg 3 comes after a failed leg: indeterminate, nothing planned for it — the numbers a
	// report that advanced prev anyway would print are not here.
	af := rep.Legs[3]
	if af.Status != "indeterminate" || len(af.Segments) != 0 || af.Avoid.LiveLenYd != 0 {
		t.Errorf("leg 3: %+v", af)
	}
	if !strings.Contains(af.Reason, "leg 2 (voidpull) failed") {
		t.Errorf("leg 3 reason %q", af.Reason)
	}
	// The aggregates are the complete legs' only: mean ratio and the pack counts of legs 0-1.
	s := rep.Summary()
	if !strings.Contains(s, "2 complete, 1 failed, 1 indeterminate") {
		t.Errorf("summary %q", s)
	}
	// The tile file is named and hashed.
	if len(rep.Tiles) != 2 || rep.Tiles[0].Name != "000.mmap" || len(rep.Tiles[0].Sha256) != 64 {
		t.Errorf("tiles: %+v", rep.Tiles)
	}
}

// A partial hop fails its leg; the hop after it is planned on from the point the partial
// path reached and marked non-comparable, and the legs after the leg are indeterminate.
func TestOracleContinuesPastAPartialHop(t *testing.T) {
	p, dir, plan := oracleFixture(t, islandPlan, navfixture.Square(100, 100, 120, 120, 0))
	rep, err := Oracle(p, dir, plan)
	if err != nil {
		t.Fatalf("Oracle: %v", err)
	}
	if len(rep.Legs) != 3 {
		t.Fatalf("legs: %d", len(rep.Legs))
	}
	if rep.Legs[0].Status != "complete" {
		t.Errorf("leg 0 status %s", rep.Legs[0].Status)
	}
	il := rep.Legs[1]
	if il.Status != string(navmesh.Partial) {
		t.Fatalf("leg 1 status %s, want partial", il.Status)
	}
	if len(il.Segments) != 3 {
		t.Fatalf("leg 1 hops: %d", len(il.Segments))
	}
	if il.Segments[0].NonComparable || il.Segments[0].Avoid.Status != "complete" {
		t.Errorf("leg 1 hop 1: %+v", il.Segments[0])
	}
	f := il.Segments[1] // the failing hop: to the island, partial
	if f.NonComparable || f.Kind != "to" || f.Avoid.Status != string(navmesh.Partial) {
		t.Errorf("leg 1 hop 2: %+v", f)
	}
	c := il.Segments[2] // planned on from the partial path's end
	if !c.NonComparable || c.From != il.Segments[1].Avoid.End {
		t.Errorf("leg 1 hop 3: %+v (want it planned on from hop 2's path end %v)", c, il.Segments[1].Avoid.End)
	}
	if !strings.Contains(il.Reason, "hop 2 (to) is partial") {
		t.Errorf("leg 1 reason %q", il.Reason)
	}
	af := rep.Legs[2]
	if af.Status != "indeterminate" || !strings.Contains(af.Reason, "leg 1 (island) failed") {
		t.Errorf("leg 2: %+v", af)
	}
	// The failed leg scores nothing: the plan's mean ratio is leg 0's alone.
	if !strings.Contains(rep.Summary(), "1 complete, 1 failed, 1 indeterminate") {
		t.Errorf("summary %q", rep.Summary())
	}
}

// The corridor membership and the break proximity of points against a really planned route.
func TestCorridorAndBreaksAgainstAPlannedRoute(t *testing.T) {
	p, _, _ := oracleFixture(t, walkPlan)
	aware, err := p.Plan(0, Vec3{12, 12, 0}, Vec3{48, 48, 0})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if aware.Status != navmesh.Complete {
		t.Fatalf("path %s (%s)", aware.Status, aware.Reason)
	}
	cases := []struct {
		name   string
		p      Vec3
		radius float64
		on     bool
	}{
		{"on the corridor", Vec3{12, 30, 0}, 25, true},
		{"a pack's radius inside it", Vec3{14, 30, 0}, 10, true},
		{"a floor above", Vec3{12, 30, 20}, 25, false},
		{"in the void", Vec3{200, 200, 0}, 25, false},
	}
	for _, c := range cases {
		near := NearestPolyline(aware.Points, c.p)
		if got := OnCorridor(near, c.radius); got != c.on {
			t.Errorf("%s: on corridor %v, want %v (near %+v)", c.name, got, c.on, near)
		}
	}
	// A break within BreakNearYd of the route is touched; one in the void is not.
	for _, c := range []struct {
		p       Vec3
		touched bool
	}{
		{Vec3{30, 12, 0}, true},
		{Vec3{200, 200, 0}, false},
	} {
		l := dungeon.Leg{Via: []dungeon.Waypoint{{Point: dungeon.Point{X: c.p[0], Y: c.p[1], Z: c.p[2]}, Direct: true}}}
		got := breaksOf(&l, aware.Points, Vec3{}, false)
		if len(got) != 1 || got[0].Touched != c.touched {
			t.Errorf("break at %v: %+v, want touched %v", c.p, got, c.touched)
		}
	}
}

// The ratio of the stored route to the live route on the L corridor: the plan's own points
// cut the corner the walk goes round, so the stored line is shorter.
func TestRatioCutsTheCorner(t *testing.T) {
	p, _, _ := oracleFixture(t, walkPlan)
	aware, err := p.Plan(0, Vec3{12, 12, 0}, Vec3{48, 48, 0})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	stored := []Vec3{{12, 12, 0}, {48, 48, 0}}
	ratio := PolylineLength(stored) / PolylineLength(aware.Points)
	if ratio < 0.7 || ratio > 0.8 {
		t.Errorf("ratio %v, want ~0.75 (50.9 yd straight over 68 yd round the corner)", ratio)
	}
}

// A partial hop whose path's end stands near a break's foot touches it (the second break
// rule).
func TestBreakTouchedByAPartialEndGap(t *testing.T) {
	// A path that ends short of the goal, 7 yd from the break's foot: within BreakGapYd.
	route := []Vec3{{0, 0, 0}, {10, 0, 0}}
	l := dungeon.Leg{Via: []dungeon.Waypoint{
		{Point: dungeon.Point{X: 17, Y: 0, Z: 0}, CheckSkip: "the mesh does not link it"},
		{Point: dungeon.Point{X: 40, Y: 0, Z: 0}, CheckSkip: "far"},
	}}
	got := breaksOf(&l, route, Vec3{10, 0, 0}, true)
	if len(got) != 2 {
		t.Fatalf("breaks: %+v", got)
	}
	if !got[0].Touched || got[0].Kind != "check_skip" {
		t.Errorf("near break: %+v, want touched", got[0])
	}
	if got[1].Touched {
		t.Errorf("far break: %+v, want untouched", got[1])
	}
}

// The report marshals: Stage 1 reads these files as fixtures.
func TestReportMarshals(t *testing.T) {
	p, dir, plan := oracleFixture(t, walkPlan)
	rep, err := Oracle(p, dir, plan)
	if err != nil {
		t.Fatalf("Oracle: %v", err)
	}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if len(b) < 100 {
		t.Fatalf("report json is %d bytes", len(b))
	}
}
