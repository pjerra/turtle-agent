//go:build cgo

package navmesh

import (
	"math"
	"testing"

	"github.com/pjerra/turtle-agent/internal/navmesh/navfixture"
)

// An avoid box over the ring's left column (the short way from (12, 12) to (30, 48)) marks its polygons as lava: the path goes the long way
// round by the right, no point of it or of its ground samples lies in the box, the nearest walkable point to the box's middle is outside it,
// and the floors there are still listed, with the lava flag. A box of the floor band below the ring changes nothing, and clearing the
// boxes brings the short way back. Ragefire Chasm's lava basin is ground in the tiles (rfc-live-1e).
func TestAvoidBoxIsKeptOutOf(t *testing.T) {
	p := planner(t, navfixture.Ring(0))
	from, to := Vec3{12, 12, 0}, Vec3{30, 48, 0}
	short, err := p.Plan(fixtureMap, from, to)
	if err != nil || short.Status != Complete || short.Length > 60 {
		t.Fatalf("without a box: %+v %v (want the short way, about 54 yd)", short, err)
	}

	box := Box{Min: Vec3{9, 22, -1}, Max: Vec3{15, 38, 0.5}}
	p.SetAvoid(fixtureMap, []Box{box})
	path, err := p.Plan(fixtureMap, from, to)
	if err != nil || path.Status != Complete {
		t.Fatalf("with the box: %+v %v", path, err)
	}
	if path.Length < 80 {
		t.Fatalf("with the box the path is %.0f yd: it did not go round by the right (%v)", path.Length, path.Points)
	}
	for _, q := range path.Points {
		if box.Contains(q) {
			t.Fatalf("path point %v lies in the avoid box", q)
		}
	}
	for _, seg := range path.Ground {
		for _, q := range seg {
			if box.Contains(q) {
				t.Fatalf("ground sample %v lies in the avoid box", q)
			}
		}
	}
	if n, ok, err := p.Nearest(fixtureMap, Vec3{12, 30, 0}); err != nil || (ok && box.Contains(n)) {
		t.Fatalf("nearest to the box's middle: %v %v %v", n, ok, err)
	}
	fl, err := p.Floors(fixtureMap, 12, 30, -5, 5)
	if err != nil || len(fl) != 1 || fl[0].Flags&NavMagma == 0 || fl[0].Flags&NavGround == 0 {
		t.Fatalf("floors in the box: %+v %v (want the ground with the lava flag added)", fl, err)
	}
	if got := p.Avoid(fixtureMap); len(got) != 1 || got[0] != box {
		t.Fatalf("Avoid = %v", got)
	}

	// A box under the floor (the floor band of another level) leaves the ring alone.
	p.SetAvoid(fixtureMap, []Box{{Min: Vec3{9, 22, -10}, Max: Vec3{15, 38, -5}}})
	if path, err := p.Plan(fixtureMap, from, to); err != nil || path.Length > 60 {
		t.Fatalf("a box below the floor changed the path: %+v %v", path, err)
	}
	p.SetAvoid(fixtureMap, nil)
	if path, err := p.Plan(fixtureMap, from, to); err != nil || path.Length > 60 {
		t.Fatalf("cleared boxes: %+v %v (want the short way again)", path, err)
	}
}

// A scratch planner keeps out of the same boxes.
func TestScratchKeepsTheAvoidBoxes(t *testing.T) {
	p := planner(t, navfixture.Ring(0))
	p.SetAvoid(fixtureMap, []Box{{Min: Vec3{9, 22, -1}, Max: Vec3{15, 38, 0.5}}})
	s := p.Scratch()
	defer s.Close()
	path, err := s.Plan(fixtureMap, Vec3{12, 12, 0}, Vec3{30, 48, 0})
	if err != nil || path.Status != Complete || path.Length < 80 {
		t.Fatalf("scratch planner: %+v %v (want the long way round)", path, err)
	}
}

// A box that crosses a polygon without holding any of its corners marks it when it covers enough of it: the polygon is clipped by the
// box, not tested by its corners (Codex review of #211).
func TestAvoidBoxCrossingAnEdgeMarksThePolygon(t *testing.T) {
	p := planner(t, []navfixture.Poly{navfixture.Square(0, 0, 40, 10, 0)})
	// x 10 to 30 of 0 to 40: half the polygon, and no corner of it inside.
	p.SetAvoid(fixtureMap, []Box{{Min: Vec3{10, -2, -1}, Max: Vec3{30, 12, 1}}})
	fl, err := p.Floors(fixtureMap, 5, 5, -5, 5)
	if err != nil || len(fl) != 1 || fl[0].Flags&NavMagma == 0 {
		t.Fatalf("floors %+v %v: the polygon the box crosses is not marked", fl, err)
	}
	// The same box a floor band above the polygon leaves it alone.
	p.SetAvoid(fixtureMap, []Box{{Min: Vec3{10, -2, 3}, Max: Vec3{30, 12, 5}}})
	if fl, err := p.Floors(fixtureMap, 5, 5, -5, 5); err != nil || len(fl) != 1 || fl[0].Flags&NavMagma != 0 {
		t.Fatalf("floors %+v %v: a box above the floor marked it", fl, err)
	}
}

// A box that only touches a corner of a one-cell-wide corridor (a ramp, a doorway) does not cut it: the cells it clips are mostly outside.
func TestAvoidBoxTouchingACornerKeepsTheCorridor(t *testing.T) {
	p := planner(t, navfixture.LCorridor(0))
	from, to := Vec3{12, 12, 0}, Vec3{48, 48, 0}
	before, err := p.Plan(fixtureMap, from, to)
	if err != nil || before.Status != Complete {
		t.Fatalf("%+v %v", before, err)
	}
	p.SetAvoid(fixtureMap, []Box{{Min: Vec3{45, 13, -1}, Max: Vec3{47, 15, 1}}}) // round the inner corner (46, 14)
	after, err := p.Plan(fixtureMap, from, to)
	if err != nil || after.Status != Complete || math.Abs(float64(after.Length-before.Length)) > 0.01 {
		t.Fatalf("the corridor was cut: %+v %v (before %.1f yd)", after, err, before.Length)
	}
}
