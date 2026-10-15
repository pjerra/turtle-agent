package routeedit

import (
	"errors"
	"testing"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

// holeNav is a flat mesh at height 5 with holes: a point in a hole is off the mesh.
type holeNav struct {
	holes [][4]float32 // x0, y0, x1, y1
	err   error
}

func (n holeNav) in(x, y float32) bool {
	for _, h := range n.holes {
		if x >= h[0] && x <= h[2] && y >= h[1] && y <= h[3] {
			return true
		}
	}
	return false
}

func (n holeNav) Plan(_ uint32, from, to navmesh.Vec3) (navmesh.Path, error) {
	if n.err != nil {
		return navmesh.Path{}, n.err
	}
	return navmesh.Path{Status: navmesh.Complete, Points: []navmesh.Vec3{from, to}, Length: 1}, nil
}

func (n holeNav) Raycast(uint32, navmesh.Vec3, navmesh.Vec3) (navmesh.Ray, error) {
	return navmesh.Ray{Known: true, Clear: true}, nil
}

func (n holeNav) Nearest(_ uint32, p navmesh.Vec3) (navmesh.Vec3, bool, error) {
	if n.err != nil {
		return navmesh.Vec3{}, false, n.err
	}
	if n.in(p[0], p[1]) {
		return navmesh.Vec3{}, false, nil
	}
	return navmesh.Vec3{p[0], p[1], 5}, true, nil
}

func TestEvaluateRefusesTheIssuesAnEditMakesNotTheOnesThePlanHad(t *testing.T) {
	base := load(t)
	clean := holeNav{}
	if res := Evaluate(base, base, clean); !res.Checked || !res.OK || res.BaseIssues != 0 || res.NewIssues != 0 || res.Points == 0 {
		t.Fatalf("the plan as it is: %+v", res)
	}
	// an edit that moves a via point into a hole makes an issue: not OK, and the issue is marked new
	edited, _, err := Apply(base, []Op{{Leg: 0, Kind: KindMove, Index: 0, X: 20, Y: 50}}, flat)
	if err != nil {
		t.Fatal(err)
	}
	holey := holeNav{holes: [][4]float32{{10, 40, 30, 60}}}
	res := Evaluate(base, edited, holey)
	if !res.Checked || res.OK || res.NewIssues != 1 || len(res.Issues) != 1 || !res.Issues[0].New || res.Issues[0].Reason != "off the mesh" || res.Issues[0].Text == "" {
		t.Fatalf("%+v", res)
	}
	if res.Refusal() == "" {
		t.Error("a refusal has words")
	}
	// the same hole under a point the base already had there is an old issue: another, harmless edit is not blamed for it
	oldHole := holeNav{holes: [][4]float32{{35, -5, 45, 5}}} // swallows via 1 (40, 0)
	if res := Evaluate(base, base, oldHole); res.BaseIssues == 0 {
		t.Fatal("the fixture should fail the base")
	}
	harmless, _, err := Apply(base, []Op{{Leg: 1, Kind: KindMove, Index: 0, X: 201, Y: 1}}, flat)
	if err != nil {
		t.Fatal(err)
	}
	res = Evaluate(base, harmless, oldHole)
	if !res.OK || res.NewIssues != 0 || res.BaseIssues == 0 || len(res.Issues) != res.BaseIssues || res.Issues[0].New {
		t.Fatalf("%+v", res)
	}
	// inserting a via point before the failing one renumbers it ("via 1" becomes "via 2") and it is still not new
	inserted, _, err := Apply(base, []Op{{Leg: 0, Kind: KindInsert, Index: 0, X: 10, Y: 0}}, flat)
	if err != nil {
		t.Fatal(err)
	}
	if res := Evaluate(base, inserted, oldHole); !res.OK || res.NewIssues != 0 {
		t.Fatalf("%+v", res)
	}
	// an edit that fixes the point passes with fewer issues than the plan had
	fixed, _, err := Apply(base, []Op{{Leg: 0, Kind: KindMove, Index: 1, X: 40, Y: 8}}, flat)
	if err != nil {
		t.Fatal(err)
	}
	if res := Evaluate(base, fixed, oldHole); !res.OK || len(res.Issues) >= res.BaseIssues {
		t.Fatalf("%+v", res)
	}
}

func TestEvaluateCannotProveWhatTheNavmeshCannotAnswer(t *testing.T) {
	base := load(t)
	res := Evaluate(base, base, holeNav{err: errors.New("no tiles for map 209")})
	if res.Checked || res.OK || res.Skipped == "" || res.Refusal() == "" {
		t.Fatalf("%+v", res)
	}
}
