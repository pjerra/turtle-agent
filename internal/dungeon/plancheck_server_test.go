package dungeon

import (
	"testing"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

// planServerMmaps finds the navmesh files like mapMmaps, for the plan-check
// server tests (maps 808, 818, 820).
func planServerMmaps(m uint32) string { return mapMmaps(m) }

func mustHaveMmaps(t *testing.T, maps ...uint32) {
	t.Helper()
	for _, m := range maps {
		if planServerMmaps(m) == "" {
			t.Skipf("no map %d navmesh files: set TURTLE_AGENT_MMAPS to a folder holding %03d.mmap and the tiles", m, m)
		}
	}
}

// TestPlanCheckFlagsWindhornLedgeRow is case 1 at plan level: a one-leg plan
// on map 820 whose via is the ledge row. The offline query has a complete
// path, but the server runs out of nodes, so the row fails server
// unreachable. Today (without the server query) it passes with 0 issues.
func TestPlanCheckFlagsWindhornLedgeRow(t *testing.T) {
	mustHaveMmaps(t, 820)
	pl := navmesh.NewPlanner(planServerMmaps(820))
	defer pl.Close()
	plan := &Plan{Name: "t", Map: 820, Entrance: Point{X: -7640.0, Y: -3636.0, Z: 197.3},
		Legs: []Leg{{Name: "a", Via: []Waypoint{{Point: Point{X: -7640.0, Y: -3636.0, Z: 197.3}}},
			To: Point{X: -7639.95, Y: -3636.02, Z: 224.08}}}}
	res, err := CheckPlan(plan, pl, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Issues) != 1 || res.Issues[0].Reason != ReasonServerUnreachable || res.Issues[0].Part != "to" {
		t.Fatalf("issues %+v, want one server-unreachable on to", res.Issues)
	}
}

// TestPlanCheckFlagsHateforgeSwallowedHop is case 2 at plan level: the leg
// starts on the ramp-foot point, with the direct check_skip hop onto the
// terrace 1.75 yd above it and the terrace `to`. The direct hop is inside the
// arrival radius (never sent: the party stays on the lower poly), and the
// terrace `to` is then unreachable on the offline mesh from the lower poly
// (the spec's table row: offline partial 1 poly vs server INCOMPLETE into
// the pocket from the same lower start).
func TestPlanCheckFlagsHateforgeSwallowedHop(t *testing.T) {
	mustHaveMmaps(t, 808)
	pl := navmesh.NewPlanner(planServerMmaps(808))
	defer pl.Close()
	plan := &Plan{Name: "t", Map: 808, Entrance: Point{X: -8320, Y: -3447.5, Z: 180.1},
		Legs: []Leg{{Name: "bargul_terrace",
			Via: []Waypoint{
				{Point: Point{X: -8320, Y: -3447.5, Z: 180.1}},
				{Point: Point{X: -8321.5, Y: -3447.5, Z: 181}, Direct: true, CheckSkip: "terrace hop"},
			},
			To: Point{X: -8321.5, Y: -3447.5, Z: 181},
			Boss: &Boss{Entry: 1, Position: Point{X: -8309.45, Y: -3460.42, Z: 194.43},
				Approach: []Point{{X: -8309.45, Y: -3460.42, Z: 194.43}}},
		}}}
	res, err := CheckPlan(plan, pl, 0)
	if err != nil {
		t.Fatal(err)
	}
	var swallowed, unreachable bool
	for _, is := range res.Issues {
		if is.Reason == ReasonArriveSwallowed && is.Part == "direct via 1" {
			swallowed = true
		}
		if is.Reason == ReasonServerUnreachable {
			unreachable = true
		}
	}
	if !swallowed {
		t.Errorf("no arrival-radius issue on direct via 1: %+v", res.Issues)
	}
	if !unreachable {
		t.Errorf("no server-unreachable issue on the boss approach or the to row (the server walks into the pocket, then refuses): %+v", res.Issues)
	}
}

// TestPlanCheckStillPassesStormwroughtLedgeFromMeshPoints is the documented
// limit of section 3.6: from mesh-height points the server's query takes the
// ledge, so plan-check passes it. The live failure (case 3) needs the bot's
// real z on the game floor, which is 2.8-3.6 yd below the mesh there
// (RUN-LESSONS 'r30 stormwrought_ruins slotD').
func TestPlanCheckStillPassesStormwroughtLedgeFromMeshPoints(t *testing.T) {
	mustHaveMmaps(t, 818)
	pl := navmesh.NewPlanner(planServerMmaps(818))
	defer pl.Close()
	plan := &Plan{Name: "t", Map: 818, Entrance: Point{X: -6317.6, Y: -3945.9, Z: 218.58},
		Legs: []Leg{{Name: "a", To: Point{X: -6333.1, Y: -3947.2, Z: 219.3}}}}
	res, err := CheckPlan(plan, pl, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK() {
		t.Errorf("issues %+v, want none (documented limit: mesh-height points pass)", res.Issues)
	}
}

// serverFromNav is a Navmesh fake that also answers the server query, and
// records the from points the check's server walk starts from.
type serverFromNav struct {
	fakeNav
	froms []Point
}

func (s *serverFromNav) ServerPath(_ uint32, from, to navmesh.Vec3) (navmesh.ServerPath, error) {
	s.froms = append(s.froms, Point{from[0], from[1], from[2]})
	return navmesh.ServerPath{Type: navmesh.PathNormal, Points: []navmesh.Vec3{from, to}, ActualEnd: to}, nil
}

// TestPlanCheckArriveRadiusKeepsTheParty is the arrival gate on a synthetic
// plan (no real tiles): a plain via 2 yd from the previous point is counted
// reached, so the walk to `to` starts from the earlier point.
func TestPlanCheckArriveRadiusKeepsTheParty(t *testing.T) {
	nav := &serverFromNav{}
	plan := &Plan{Name: "t", Map: 1, Entrance: Point{X: 0, Y: 0, Z: 0},
		Legs: []Leg{{Name: "a",
			Via: []Waypoint{{Point: Point{X: 10, Y: 0, Z: 0}}, {Point: Point{X: 12, Y: 0, Z: 0}}},
			To:  Point{X: 30, Y: 0, Z: 0}}}}
	res, err := CheckPlan(plan, nav, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK() {
		t.Fatalf("issues %+v, want none", res.Issues)
	}
	for _, from := range nav.froms {
		if from.X == 12 {
			t.Errorf("server walk started from the swallowed point %v: %+v", from, nav.froms)
		}
	}
	if len(nav.froms) == 0 {
		t.Errorf("no server walk recorded")
	}
}
