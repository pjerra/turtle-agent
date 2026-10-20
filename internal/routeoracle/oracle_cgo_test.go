//go:build cgo

package routeoracle

import (
	"encoding/json"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/navmesh/navfixture"
)

// The oracle on a synthetic mesh (navfixture's L corridor): a row along x from 10 to 50 at
// y 10..14, then a column up y to 50 at x 46..50. Start (12, 12), goal (48, 48); the live
// walk is 68 yd, the straight stored line 50.9 yd. No real mmaps are needed.
const corridorPlan = `{"version":1,"name":"corridor","map":0,"entrance":{"x":12,"y":12,"z":0},"legs":[
 {"name":"hall","to":{"x":46,"y":12,"z":0},"via":[{"x":22,"y":12,"z":0},{"x":30,"y":12,"z":0,"direct":true}],
  "pull_groups":[{"entries":[100,101],"positions":[{"x":30,"y":12,"z":0}]},
                 {"entries":[102],"positions":[{"x":200,"y":200,"z":0}]}]},
 {"name":"boss","to":{"x":48,"y":48,"z":0},"boss":{"entry":200,"position":{"x":48,"y":48,"z":0}}}]}`

func corridorFixture(t *testing.T) (*navmesh.Planner, string, *dungeon.Plan) {
	t.Helper()
	dir := navfixture.WriteDir(t, 0, navfixture.LCorridor(0))
	p := navmesh.NewPlanner(dir)
	t.Cleanup(p.Close)
	plan, err := dungeon.Parse("corridor.json", []byte(corridorPlan))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return p, dir, plan
}

func TestOracleOnTheCorridorFixture(t *testing.T) {
	p, dir, plan := corridorFixture(t)
	rep, err := Oracle(p, dir, plan)
	if err != nil {
		t.Fatalf("Oracle: %v", err)
	}
	if rep.Plan != "corridor" || rep.Map != 0 || len(rep.Legs) != 2 {
		t.Fatalf("report header: %+v", rep)
	}
	// Both legs plan complete; the second starts where the first ended.
	for i, want := range []string{"to", "boss"} {
		lr := rep.Legs[i]
		if lr.Objective != want {
			t.Errorf("leg %d: objective %q, want %q", i, lr.Objective, want)
		}
		if lr.Avoid.Status != string(navmesh.Complete) || lr.Plain.Status != string(navmesh.Complete) {
			t.Errorf("leg %d: statuses plain %s avoid %s, want complete", i, lr.Plain.Status, lr.Avoid.Status)
		}
		if lr.Plain.EndGapYd > 0.5 || lr.Avoid.EndGapYd > 0.5 {
			t.Errorf("leg %d: end gaps %v/%v, want ~0 on a complete path", i, lr.Plain.EndGapYd, lr.Avoid.EndGapYd)
		}
	}
	if got := rep.Legs[1].From; got != (dungeon.Point{X: 46, Y: 12}) {
		t.Errorf("leg 1 from %v, want the first leg's objective (46, 12)", got)
	}
	// The corridor membership of the two pull groups: the first's position is on the path,
	// the second's is in the void far from it.
	pulls := rep.Legs[0].Pulls
	if len(pulls) != 2 || !pulls[0].OnPath || pulls[1].OnPath {
		t.Errorf("pull groups on path: %+v", pulls)
	}
	if pulls[0].NearYd > 1 || pulls[1].NearYd < 100 {
		t.Errorf("pull group distances: %v %v", pulls[0].NearYd, pulls[1].NearYd)
	}
	// The direct via lies on the path: touched. The leg's stored chain runs along the
	// corridor but detours to the second group's pull point in the void (200, 200) and back,
	// so the stored length dwarfs the live one: a plan's stored points really are walked in
	// the order they are stored.
	lr := rep.Legs[0]
	if lr.BreaksTouched != 1 || len(lr.Breaks) != 1 || lr.Breaks[0].Kind != "direct" {
		t.Errorf("leg 0 breaks: %+v", lr.Breaks)
	}
	if lr.Ratio < 5 {
		t.Errorf("leg 0 ratio %v, want far above 1 (the void pull point is on the stored chain)", lr.Ratio)
	}
	for _, s := range lr.Stored {
		want := s.Kind != "pull" || s.Seq == 4 // the first group's pull point is on the path
		if s.Redundant != want {
			t.Errorf("leg 0 stored point %d (%s): redundant %v, want %v (%.1f yd)", s.Seq, s.Kind, s.Redundant, want, s.NearYd)
		}
	}
	// The boss leg's stored points: its to and its boss, both the same point at the path's end.
	if got := rep.Legs[1].Stored; len(got) != 2 || got[0].Kind != "to" || got[1].Kind != "boss" {
		t.Errorf("leg 1 stored points: %+v", got)
	}
	// No jumps on flat ground, and the tile file is named and hashed.
	if len(rep.Legs[0].ZJumps) != 0 {
		t.Errorf("leg 0 z jumps: %+v", rep.Legs[0].ZJumps)
	}
	if len(rep.Tiles) != 2 || rep.Tiles[0].Name != "000.mmap" || len(rep.Tiles[0].Sha256) != 64 {
		t.Errorf("tiles: %+v", rep.Tiles)
	}
}

// The corridor membership and the break proximity of points against a really planned path.
func TestCorridorAndBreaksAgainstAPlannedPath(t *testing.T) {
	p, _, _ := corridorFixture(t)
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
		{"in the notch, over the slack of a small radius", Vec3{30, 30, 0}, -5, false},
		{"a floor above", Vec3{12, 30, 20}, 25, false},
		{"in the void", Vec3{200, 200, 0}, 25, false},
	}
	for _, c := range cases {
		near := NearestPolyline(aware.Points, c.p)
		if got := OnCorridor(near, c.radius); got != c.on {
			t.Errorf("%s: on corridor %v, want %v (near %+v)", c.name, got, c.on, near)
		}
	}
	// A break within BreakNearYd of the path is touched; one in the void is not.
	for _, c := range []struct {
		p       Vec3
		touched bool
	}{
		{Vec3{30, 12, 0}, true},
		{Vec3{200, 200, 0}, false},
	} {
		l := dungeon.Leg{Via: []dungeon.Waypoint{{Point: dungeon.Point{X: c.p[0], Y: c.p[1], Z: c.p[2]}, Direct: true}}}
		got := breaks(&l, aware)
		if len(got) != 1 || got[0].Touched != c.touched {
			t.Errorf("break at %v: %+v, want touched %v", c.p, got, c.touched)
		}
	}
}

// The ratio of the stored route to the live path on the L corridor: the plan's own points cut
// the corner the walk goes round, so the stored line is shorter.
func TestRatioCutsTheCorner(t *testing.T) {
	p, _, _ := corridorFixture(t)
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

// A Partial leg whose end gap ends near a break's foot touches it (the second break rule).
func TestBreakTouchedByAPartialEndGap(t *testing.T) {
	// A path that ends short of the goal, 7 yd from the break's foot: within BreakGapYd.
	aware := navmesh.Path{Status: navmesh.Partial, Points: []Vec3{{0, 0, 0}, {10, 0, 0}}}
	l := dungeon.Leg{Via: []dungeon.Waypoint{
		{Point: dungeon.Point{X: 17, Y: 0, Z: 0}, CheckSkip: "the mesh does not link it"},
		{Point: dungeon.Point{X: 40, Y: 0, Z: 0}, CheckSkip: "far"},
	}}
	got := breaks(&l, aware)
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
	p, dir, plan := corridorFixture(t)
	rep, err := Oracle(p, dir, plan)
	if err != nil {
		t.Fatalf("Oracle: %v", err)
	}
	b, err := json.MarshalIndent(rep, "", " ")
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if len(b) < 100 {
		t.Fatalf("report json is %d bytes", len(b))
	}
}
