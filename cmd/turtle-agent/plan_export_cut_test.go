package main

import (
	"math"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/navmesh/navfixture"
	"github.com/pjerra/turtle-agent/internal/planexport"
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

// fakeSeamPlanner answers the cutter's plan with a polyline through one corner, and the server's query as scripted: the hop to
// a goal exactly on the corner is NOPATH (the server's answer to a goal on an mmaps cell line), or to every goal.
type fakeSeamPlanner struct {
	path      []navmesh.Vec3
	corner    navmesh.Vec3
	refuseOne bool
	refuseAll bool
	asked     int
}

func (f *fakeSeamPlanner) Plan(_ uint32, _ navmesh.Vec3, _ navmesh.Vec3) (navmesh.Path, error) {
	return navmesh.Path{Status: navmesh.Complete, Points: f.path, Length: 100}, nil
}

func (f *fakeSeamPlanner) ServerPath(_ uint32, from, to navmesh.Vec3) (navmesh.ServerPath, error) {
	f.asked++
	typ := uint32(navmesh.PathNormal)
	if f.refuseAll || (f.refuseOne && math.Abs(float64(f.corner[0]-to[0])) < 1e-3 && math.Abs(float64(f.corner[1]-to[1])) < 1e-3) {
		typ = navmesh.PathNoPath
	}
	return navmesh.ServerPath{Type: typ, Points: []navmesh.Vec3{from, to}, ActualEnd: to}, nil
}

// A cut point whose x or y is an exact multiple of the server's 5.3333 yd mmaps cell stands on a tile seam, and the server
// answers the hop to it NOPATH; the same point 0.5 yd back along the hop is reached (route audit 2026-11-12, finding 3:
// plan-probe cleared 13 of the 14 listed rows with this move, naxxramas step 637 stayed: a steep ledge). The cutter moves such
// a point, and only such a point: a seam the server walks keeps its place, and so does one whose move it refuses too.
func TestCutPointsTheServerRefusesAreNudgedOffTheCellLine(t *testing.T) {
	seam := navmesh.Vec3{106.66667, 16, 0}
	from, to := navmesh.Vec3{20, 0, 0}, navmesh.Vec3{120, 20, 0}
	if !planexport.OnSeam(seam[0]) || !planexport.OnSeam(seam[1]) {
		t.Fatalf("the fixture corner %v is not on a cell line", seam)
	}
	for _, c := range []struct {
		name  string
		pl    *fakeSeamPlanner
		moved bool
	}{
		{"the server refuses the seam and reaches the moved point", &fakeSeamPlanner{refuseOne: true}, true},
		{"the server walks the seam", &fakeSeamPlanner{}, false},
		{"the server refuses the move too", &fakeSeamPlanner{refuseAll: true}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.pl.path = []navmesh.Vec3{from, seam, to}
			c.pl.corner = seam
			via, notes, err := cutWith(c.pl, false)(309, dungeon.Point{X: from[0], Y: from[1], Z: from[2]}, dungeon.Point{X: to[0], Y: to[1], Z: to[2]})
			if err != nil {
				t.Fatal(err)
			}
			if len(via) != 2 {
				t.Fatalf("rows %+v, want the 50 yd split and the corner", via)
			}
			last := via[len(via)-1]
			if c.pl.asked == 0 {
				t.Fatalf("the cutter did not ask the server about a seam point")
			}
			if !c.moved {
				if last.X != seam[0] || last.Y != seam[1] {
					t.Fatalf("the cut row moved to %+.4f %.4f: %+v", last.X, last.Y, notes)
				}
				return
			}
			if planexport.OnSeam(last.X) || planexport.OnSeam(last.Y) {
				t.Fatalf("the cut row %+.6f %.6f is still on a cell line", last.X, last.Y)
			}
			gx, gy := float64(last.X-seam[0]), float64(last.Y-seam[1])
			if d := math.Hypot(gx, gy); math.Abs(d-planexport.SeamNudgeYd) > 1e-3 {
				t.Fatalf("the cut row moved %.4f yd, want %g", d, planexport.SeamNudgeYd)
			}
			if gx*float64(from[0]-seam[0])+gy*float64(from[1]-seam[1]) <= 0 {
				t.Fatalf("the cut row moved away from the hop's start: %.4f %.4f", gx, gy)
			}
			if len(notes) != 1 || !strings.Contains(notes[0].Reason, "cell line") {
				t.Fatalf("notes %+v, want one on the seam", notes)
			}
		})
	}
}
