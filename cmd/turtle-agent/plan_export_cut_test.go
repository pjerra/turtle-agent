package main

import (
	"math"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/navmesh/navfixture"
)

// plane is a flat rectangle of ground at z.
func plane(x0, y0, x1, y1, z float32) navfixture.Poly {
	return navfixture.Poly{{x0, y0, z}, {x1, y0, z}, {x1, y1, z}, {x0, y1, z}}
}

func cutPlanner(t *testing.T, polys []navfixture.Poly) *navmesh.Planner {
	t.Helper()
	pl := navmesh.NewPlanner(navfixture.WriteDir(t, 309, polys))
	t.Cleanup(pl.Close)
	return pl
}

func pathOf(pts ...navmesh.Vec3) navmesh.Path {
	return navmesh.Path{Status: navmesh.Complete, Points: pts, Length: float32(cutDist(pts[0], pts[len(pts)-1]))}
}

// A split over a canal bed 9 yd down gets no row: the hop stays whole and the drop is a note.
func TestFloorAwareCutDropsTheCanalSplit(t *testing.T) {
	pl := cutPlanner(t, []navfixture.Poly{
		plane(0, 0, 20, 20, 0),    // the near bank
		plane(100, 0, 120, 20, 0), // the far bank
		plane(20, 0, 100, 20, -9), // the canal bed, 9 yd down
	})
	via, notes, err := floorAwareCut(pl, 309, pathOf(navmesh.Vec3{0, 10, 0}, navmesh.Vec3{120, 10, 0}))
	if err != nil {
		t.Fatal(err)
	}
	if len(via) != 0 {
		t.Fatalf("a split over the canal got a row: %+v", via)
	}
	if len(notes) != 1 || !notes[0].Dropped || !strings.Contains(notes[0].Reason, "no ground within") {
		t.Fatalf("notes %+v", notes)
	}
}

// The split point's z is the floor's, not the straight line's: a 120 yd hop whose middle floor stands at 1 yd gets a row at 1.
func TestFloorAwareCutTakesTheSplitZFromTheFloor(t *testing.T) {
	pl := cutPlanner(t, []navfixture.Poly{
		plane(0, 0, 55, 20, 0),
		plane(55, 0, 65, 20, 1), // the floor of the split point
		plane(65, 0, 120, 20, 0),
	})
	via, notes, err := floorAwareCut(pl, 309, pathOf(navmesh.Vec3{0, 10, 0}, navmesh.Vec3{120, 10, 0}))
	if err != nil {
		t.Fatal(err)
	}
	if len(via) != 1 || len(notes) != 0 {
		t.Fatalf("via %+v notes %+v", via, notes)
	}
	if math.Abs(float64(via[0].Z-1)) > 0.2 || via[0].Z == 0 {
		t.Fatalf("the split point's z is %.2f, want the floor 1 (a straight-line z is 0)", via[0].Z)
	}
}

// Two corners 1.5 yd apart are one row: the nearer one gets no row.
func TestFloorAwareCutMergesCloseCorners(t *testing.T) {
	pl := cutPlanner(t, []navfixture.Poly{plane(-20, -20, 120, 20, 0)})
	path := pathOf(
		navmesh.Vec3{0, 1, 0},
		navmesh.Vec3{50, 1, 0},   // kept
		navmesh.Vec3{50, 2.5, 0}, // 1.5 yd from the kept corner
		navmesh.Vec3{100, 1, 0},
	)
	via, notes, err := floorAwareCut(pl, 309, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(via) != 1 {
		t.Fatalf("rows %+v, want the one kept corner", via)
	}
	if math.Abs(float64(via[0].Y-1)) > 0.01 {
		t.Fatalf("the near corner is a row too: %+v", via)
	}
	var merged bool
	for _, n := range notes {
		merged = merged || (n.Dropped && math.Abs(float64(n.Old.Y-2.5)) < 0.01 && strings.Contains(n.Reason, "under 8"))
	}
	if !merged {
		t.Fatalf("no note of the merge: %+v", notes)
	}
}

// A corner that sits on a wall is pushed 3 yd into the walkable side.
func TestFloorAwareCutPushesTheCornerOffTheWall(t *testing.T) {
	// A 10 yd wide strip (y 0..10): the mesh edge at y 10 is the wall.
	pl := cutPlanner(t, []navfixture.Poly{plane(0, 0, 100, 10, 0)})
	path := pathOf(
		navmesh.Vec3{10, 1, 0},
		navmesh.Vec3{50, 9.5, 0}, // 0.5 yd from the wall
		navmesh.Vec3{90, 1, 0},
	)
	via, notes, err := floorAwareCut(pl, 309, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(via) != 1 {
		t.Fatalf("rows %+v", via)
	}
	if math.Abs(float64(via[0].Y-6.5)) > 0.3 {
		t.Fatalf("the corner at y 9.5 stayed at %.2f, want it 3 yd off the wall at y 6.5 (notes %+v)", via[0].Y, notes)
	}
	if math.Abs(float64(via[0].Z)) > 0.2 {
		t.Fatalf("the pushed corner left the floor: %+v", via[0])
	}
}

// A 90 yd straight hop stays one hop: no mechanical 50 yd split.
func TestFloorAwareCutKeepsAStraightHopWhole(t *testing.T) {
	pl := cutPlanner(t, []navfixture.Poly{plane(-20, -20, 120, 20, 0)})
	via, notes, err := floorAwareCut(pl, 309, pathOf(navmesh.Vec3{0, 0, 0}, navmesh.Vec3{90, 0, 0}))
	if err != nil || len(via) != 0 || len(notes) != 0 {
		t.Fatalf("via %+v notes %+v err %v", via, notes, err)
	}
}
