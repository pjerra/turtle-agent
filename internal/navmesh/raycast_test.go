//go:build cgo

package navmesh

import (
	"math"
	"testing"

	"github.com/pjerra/turtle-agent/internal/navmesh/navfixture"
)

func TestRaycastAlongTheCorridorIsClearAndRoundTheCornerIsBlocked(t *testing.T) {
	p := planner(t, navfixture.LCorridor(0))
	// Along the row (x 10..50 at y 10..14): clear.
	ray, err := p.Raycast(fixtureMap, Vec3{12, 12, 0}, Vec3{44, 12, 0})
	if err != nil || !ray.Known || !ray.Clear || ray.Hit != 0 {
		t.Fatalf("along the row: %+v %v", ray, err)
	}
	// Up the column (x 46..50, y 14..50): clear.
	ray, err = p.Raycast(fixtureMap, Vec3{48, 16, 0}, Vec3{48, 48, 0})
	if err != nil || !ray.Clear {
		t.Fatalf("up the column: %+v %v", ray, err)
	}
	// From the row's west end to the column's north end the line leaves the mesh: blocked, and where.
	ray, err = p.Raycast(fixtureMap, Vec3{12, 12, 0}, Vec3{48, 48, 0})
	if err != nil || !ray.Known || ray.Clear {
		t.Fatalf("round the corner: %+v %v", ray, err)
	}
	// The line y = x leaves the row (y <= 14) at x = 14, 2 * sqrt(2) yd from the start.
	if want := 2 * math.Sqrt2; math.Abs(float64(ray.Hit)-want) > 0.05 {
		t.Fatalf("hit at %.3f yd, want %.3f", ray.Hit, want)
	}
}

func TestRaycastEndOffTheMeshIsBlockedAndStartOffTheMeshIsUnknown(t *testing.T) {
	p := planner(t, navfixture.LCorridor(0))
	ray, err := p.Raycast(fixtureMap, Vec3{12, 12, 0}, Vec3{30, 30, 0}) // the end is in the hollow of the L
	if err != nil || !ray.Known || ray.Clear {
		t.Fatalf("%+v %v", ray, err)
	}
	ray, err = p.Raycast(fixtureMap, Vec3{30, 30, 0}, Vec3{12, 12, 0}) // the start is over no polygon
	if err != nil || ray.Known {
		t.Fatalf("start off the mesh: %+v %v", ray, err)
	}
}

func TestRaycastRingSeesAcrossTheHollowOnlyAlongTheCells(t *testing.T) {
	p := planner(t, navfixture.Ring(0))
	// Bottom row end to end: clear. Bottom-left corner to top-right corner crosses the hollow: blocked.
	if ray, err := p.Raycast(fixtureMap, Vec3{12, 12, 0}, Vec3{48, 12, 0}); err != nil || !ray.Clear {
		t.Fatalf("%+v %v", ray, err)
	}
	if ray, err := p.Raycast(fixtureMap, Vec3{12, 12, 0}, Vec3{48, 48, 0}); err != nil || !ray.Known || ray.Clear {
		t.Fatalf("%+v %v", ray, err)
	}
}

func TestRaycastNeedsAFolderAndAMap(t *testing.T) {
	if _, err := NewPlanner("").Raycast(fixtureMap, Vec3{}, Vec3{1, 1, 0}); err == nil {
		t.Fatal("no folder, no error")
	}
	p := NewPlanner(t.TempDir())
	defer p.Close()
	if _, err := p.Raycast(fixtureMap, Vec3{}, Vec3{1, 1, 0}); err == nil {
		t.Fatal("no map file, no error")
	}
}

func TestRaycastMissingTileIsUnknown(t *testing.T) {
	p := planner(t, navfixture.LCorridor(0))
	ray, err := p.Raycast(fixtureMap, Vec3{12, 12, 0}, Vec3{-588, 12, 0})
	if err != nil || ray.Known || ray.Clear {
		t.Fatalf("%+v %v", ray, err)
	}
}

func TestNearestSnapsToTheMesh(t *testing.T) {
	p := planner(t, navfixture.LCorridor(0))
	got, ok, err := p.Nearest(fixtureMap, Vec3{12, 16.5, 0}) // 2.5 yd north of the row
	if err != nil || !ok || !near(got, Vec3{12, 14, 0}, 0.05) {
		t.Fatalf("%v %v %v", got, ok, err)
	}
	if _, ok, _ := p.Nearest(fixtureMap, Vec3{30, 40, 0}); ok {
		t.Fatal("a point 20 yd from the mesh snapped")
	}
}
