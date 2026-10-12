//go:build cgo

package navmesh

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/navmesh/navfixture"
)

const fixtureMap = 1

func planner(t *testing.T, polys []navfixture.Poly) *Planner {
	t.Helper()
	p := NewPlanner(navfixture.WriteDir(t, fixtureMap, polys))
	t.Cleanup(p.Close)
	return p
}

func near(a, b Vec3, tol float64) bool {
	return math.Hypot(float64(a[0]-b[0]), float64(a[1]-b[1])) <= tol && math.Abs(float64(a[2]-b[2])) <= tol
}

func onMesh(polys []navfixture.Poly, x, y float32) bool {
	for _, p := range polys {
		x0, y0, x1, y1 := p[0][0], p[0][1], p[2][0], p[2][1]
		if x >= x0-1e-3 && x <= x1+1e-3 && y >= y0-1e-3 && y <= y1+1e-3 {
			return true
		}
	}
	return false
}

// segmentOnMesh samples a straight walk every quarter yard.
func segmentOnMesh(polys []navfixture.Poly, a, b Vec3) bool {
	n := max(1, int(dist2D(a, b)/0.25))
	for k := 0; k <= n; k++ {
		f := float32(k) / float32(n)
		if !onMesh(polys, a[0]+(b[0]-a[0])*f, a[1]+(b[1]-a[1])*f) {
			return false
		}
	}
	return true
}

func TestPathTurnsExactlyAtTheInnerCorner(t *testing.T) {
	p := planner(t, navfixture.LCorridor(0))
	path, err := p.Plan(fixtureMap, Vec3{12, 12, 0}, Vec3{48, 48, 0})
	if err != nil {
		t.Fatal(err)
	}
	if path.Status != Complete || path.Engine != "detour" || len(path.Points) != 3 {
		t.Fatalf("%+v", path)
	}
	// Detour's straight path: start, the portal vertex at the inner corner, goal.
	want := []Vec3{{12, 12, 0}, {46, 14, 0}, {48, 48, 0}}
	for i, w := range want {
		if !near(path.Points[i], w, 0.01) {
			t.Fatalf("point %d = %v, want %v (%v)", i, path.Points[i], w, path.Points)
		}
	}
	wantLen := math.Hypot(34, 2) + math.Hypot(2, 34)
	if math.Abs(float64(path.Length)-wantLen) > 0.01 {
		t.Fatalf("length %.3f, want %.3f", path.Length, wantLen)
	}
	for i := 1; i < len(path.Points); i++ {
		if !segmentOnMesh(navfixture.LCorridor(0), path.Points[i-1], path.Points[i]) {
			t.Fatalf("segment %d leaves the mesh: %v", i, path.Points)
		}
	}
}

func TestPathTakesTheShortWayRoundTheRing(t *testing.T) {
	p := planner(t, navfixture.Ring(0))
	path, err := p.Plan(fixtureMap, Vec3{12, 12, 0}, Vec3{30, 48, 0})
	if err != nil {
		t.Fatal(err)
	}
	if path.Status != Complete || path.Length > 60 {
		t.Fatalf("%+v", path)
	}
	for _, q := range path.Points {
		if q[0] > 30.01 {
			t.Fatalf("went round by the right: %v", path.Points)
		}
	}
}

func TestGoalOffTheMeshIsNoPath(t *testing.T) {
	p := planner(t, navfixture.LCorridor(0))
	path, err := p.Plan(fixtureMap, Vec3{12, 12, 0}, Vec3{200, 200, 0})
	if err != nil {
		t.Fatal(err)
	}
	if path.Status != NoPath || !strings.Contains(path.Reason, "goal") || len(path.Points) != 0 {
		t.Fatalf("%+v", path)
	}
	path, _ = p.Plan(fixtureMap, Vec3{200, 200, 0}, Vec3{12, 12, 0})
	if path.Status != NoPath || !strings.Contains(path.Reason, "start") {
		t.Fatalf("%+v", path)
	}
}

// PathInfo::FindWalkPoly never snaps to a floor more than 3 yd above the point.
func TestGoalUnderTheFloorIsNoPath(t *testing.T) {
	p := planner(t, navfixture.LCorridor(0))
	if path, _ := p.Plan(fixtureMap, Vec3{12, 12, 0}, Vec3{48, 48, -5}); path.Status != NoPath {
		t.Fatalf("5 yd under: %+v", path)
	}
	if path, _ := p.Plan(fixtureMap, Vec3{12, 12, 0}, Vec3{48, 48, 5}); path.Status != Complete {
		t.Fatalf("5 yd over: %+v", path)
	}
}

func TestIslandGoalIsPartialToTheNearestReachablePoint(t *testing.T) {
	polys := append(navfixture.LCorridor(0), navfixture.Square(60, 60, 64, 64, 0))
	p := planner(t, polys)
	path, err := p.Plan(fixtureMap, Vec3{12, 12, 0}, Vec3{62, 62, 0})
	if err != nil {
		t.Fatal(err)
	}
	if path.Status != Partial || !strings.Contains(path.Reason, "yd") {
		t.Fatalf("%+v", path)
	}
	if end := path.Points[len(path.Points)-1]; !near(end, Vec3{50, 50, 0}, 0.01) {
		t.Fatalf("ends at %v, want the corridor's corner nearest the island", end)
	}
}

// Detour's corners only know the polygon heights at the corners; Ground follows the surface
// in between, so a walk over a hill does not cut through it.
func TestGroundFollowsTheSurfaceBetweenCorners(t *testing.T) {
	p := planner(t, navfixture.Ramp())
	h := func(x float32) float32 { return 20 - float32(math.Abs(float64(x-50)))/2 }
	path, err := p.Plan(fixtureMap, Vec3{12, 12, h(12)}, Vec3{88, 12, h(88)})
	if err != nil {
		t.Fatal(err)
	}
	if path.Status != Complete || len(path.Points) != 2 || len(path.Ground) != 1 {
		t.Fatalf("%+v", path)
	}
	g := path.Ground[0]
	if len(g) < 10 {
		t.Fatalf("%d ground points over 76 yd", len(g))
	}
	top := float32(0)
	for _, q := range g {
		if math.Abs(float64(q[2]-h(q[0]))) > 0.05 || q[1] != 12 {
			t.Fatalf("ground point %v is off the ramp (z %.2f)", q, h(q[0]))
		}
		top = max(top, q[2])
	}
	if top < 18 {
		t.Fatalf("highest ground point %.1f, the hill is 20", top)
	}
	if z := GroundZ(Vec3{12, 12, h(12)}, path.Points[1], g, 50, 12); math.Abs(float64(z-19)) > 0.01 {
		t.Fatalf("GroundZ at the top = %.2f", z)
	}
}

func TestGroundZInterpolatesAndClamps(t *testing.T) {
	a, b := Vec3{0, 0, 0}, Vec3{12, 0, 0}
	g := []Vec3{{4, 0, 4}, {8, 0, 4}}
	for _, c := range []struct{ x, want float32 }{{0, 0}, {2, 2}, {6, 4}, {10, 2}, {12, 0}, {-3, 0}, {20, 0}} {
		if z := GroundZ(a, b, g, c.x, 0); math.Abs(float64(z-c.want)) > 1e-4 {
			t.Errorf("x %.0f: z %.3f, want %.3f", c.x, z, c.want)
		}
	}
	if z := GroundZ(a, Vec3{12, 0, 6}, nil, 6, 0); z != 3 {
		t.Errorf("no ground: z %.3f, want the straight line's 3", z)
	}
}

func TestMissingMapAndBadTilesAreErrors(t *testing.T) {
	dir := navfixture.WriteDir(t, fixtureMap, navfixture.LCorridor(0))
	p := NewPlanner(dir)
	defer p.Close()
	if _, err := p.Plan(70, Vec3{}, Vec3{1, 1, 1}); err == nil || !strings.Contains(err.Error(), "070.mmap") ||
		!strings.Contains(err.Error(), dir) {
		t.Fatalf("missing map: %v", err)
	}
	tile := filepath.Join(dir, "0013131.mmtile")
	data, err := os.ReadFile(tile)
	if err != nil {
		t.Fatal(err)
	}
	data[8] = 5 // mmapVersion
	if err := os.WriteFile(tile, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Plan(fixtureMap, Vec3{12, 12, 0}, Vec3{48, 48, 0}); err == nil || !strings.Contains(err.Error(), "0013131.mmtile") ||
		!strings.Contains(err.Error(), "version") {
		t.Fatalf("bad tile: %v", err)
	}
	if _, err := NewPlanner("").Plan(fixtureMap, Vec3{}, Vec3{}); err == nil || !strings.Contains(err.Error(), "mmaps_dir") {
		t.Fatalf("no dir: %v", err)
	}
}

func TestTilesLoadOnceAndOnlyNearThePath(t *testing.T) {
	p := planner(t, navfixture.LCorridor(0))
	for range 2 {
		if _, err := p.Plan(fixtureMap, Vec3{12, 12, 0}, Vec3{48, 48, 0}); err != nil {
			t.Fatal(err)
		}
	}
	if got := p.Tiles(fixtureMap); len(got) != 1 || got[0] != "0013131.mmtile" {
		t.Fatalf("tiles %v", got)
	}
}

func TestWantTilesPicksTheBoxRoundThePointsOnBigMaps(t *testing.T) {
	var names []string
	for r := 40; r < 60; r++ {
		for c := 20; c < 40; c++ {
			names = append(names, tileFile(0, float32(32-r)*navfixture.Tile-1, float32(32-c)*navfixture.Tile-1))
		}
	}
	got := wantTiles(names, 0, []Vec3{{-8913, 554, 94}, {-8900, 560, 94}}, 1)
	if len(got) != 9 || !contains(got, tileFile(0, -8913, 554)) {
		t.Fatalf("%d tiles %v", len(got), got)
	}
	small := []string{"0703131.mmtile", "0703231.mmtile", "0703232.mmtile"}
	if got := wantTiles(small, 70, []Vec3{{0, 0, 0}}, 1); len(got) != 3 {
		t.Fatalf("a small map loads whole: %v", got)
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// TestRealTiles runs on real server tiles when TURTLE_AGENT_MMAPS points at a folder
// holding Uldaman (map 70): 070.mmap, 0703131/0703231/0703232.mmtile. The tiles are built
// from the game's data, so none are committed here.
func TestRealTiles(t *testing.T) {
	dir := os.Getenv("TURTLE_AGENT_MMAPS")
	if dir == "" {
		t.Skip("TURTLE_AGENT_MMAPS not set")
	}
	p := NewPlanner(dir)
	defer p.Close()
	// The harness's in-game call for testers: entrance to the north room, about 131 yd
	// through the west room (WALKTO-TRY.md).
	path, err := p.Plan(70, Vec3{-226.8, 49.1, -46.0}, Vec3{-226, 112, -46.6})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s, %d points, %.1f yd: %v", path.Status, len(path.Points), path.Length, path.Points)
	if path.Status != Complete || path.Length < 120 || path.Length > 140 {
		t.Fatalf("%+v", path)
	}
}
