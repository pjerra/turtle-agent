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
	got := wantTiles(names, 0, Vec3{-8913, 554, 94}, Vec3{-8900, 560, 94}, 1, MaxTiles)
	if len(got) != 9 || got[0] != tileFile(0, -8913, 554) {
		t.Fatalf("%d tiles %v", len(got), got)
	}
	// A long line across the map: the box would be 20 x 20, cut to the tiles nearest it.
	at := func(r, c int) Vec3 {
		return Vec3{float32(32-r)*navfixture.Tile - 1, float32(32-c)*navfixture.Tile - 1, 0}
	}
	from, to := at(41, 21), at(58, 38)
	got = wantTiles(names, 0, from, to, 3, 30)
	mid := at(49, 29)
	if len(got) != 30 || got[0] != tileFile(0, from[0], from[1]) || !contains(got, tileFile(0, to[0], to[1])) ||
		!contains(got, tileFile(0, mid[0], mid[1])) || contains(got, tileFile(0, at(41, 38)[0], at(41, 38)[1])) {
		t.Fatalf("%d tiles %v", len(got), got)
	}
	small := []string{"0703131.mmtile", "0703231.mmtile", "0703232.mmtile"}
	if got := wantTiles(small, 70, Vec3{}, Vec3{}, 1, MaxTiles); len(got) != 3 {
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

// The path runs between the floor points under the asked ones (findNearestPoly's closest
// points), never at the asked height: a walk there would climb or drop yards in a beat.
func TestPathEndsOnTheFloor(t *testing.T) {
	p := planner(t, navfixture.LCorridor(0))
	for _, c := range []struct {
		name     string
		from, to Vec3
		status   Status
		reason   string
	}{
		{"goal 5 yd up", Vec3{12, 12, 0}, Vec3{48, 48, 5}, Complete, ""},
		{"goal 8 yd up", Vec3{12, 12, 0}, Vec3{48, 48, 8}, Partial, "8.0 yd from the navmesh"},
		{"goal 2 yd down", Vec3{12, 12, 0}, Vec3{48, 48, -2}, Complete, ""},
		{"start 4 yd up", Vec3{12, 12, 4}, Vec3{48, 48, 0}, Complete, ""},
	} {
		path, err := p.Plan(fixtureMap, c.from, c.to)
		if err != nil {
			t.Fatal(err)
		}
		first, last := path.Points[0], path.Points[len(path.Points)-1]
		if path.Status != c.status || !strings.Contains(path.Reason, c.reason) || (c.reason == "") != (path.Reason == "") {
			t.Errorf("%s: %s %q", c.name, path.Status, path.Reason)
		}
		if first != (Vec3{12, 12, 0}) || last != (Vec3{48, 48, 0}) {
			t.Errorf("%s: path from %v to %v, not on the floor", c.name, first, last)
		}
	}
}

// Past the tile limit the least recently used tile is unloaded, and comes back when needed.
func TestTilesEvictLeastRecentlyUsed(t *testing.T) {
	dir := navfixture.WriteDir(t, fixtureMap, navfixture.LCorridor(0))
	// A second tile holding a 4-yd cell at x -590..-586, y 10..14 (Detour tile x 0, y -2).
	far := navfixture.BuildTile([]navfixture.Poly{navfixture.Square(-590, 10, -586, 14, 0)}, nil, 0, -2)
	if err := os.WriteFile(filepath.Join(dir, tileFile(fixtureMap, -588, 12)), far, 0o644); err != nil {
		t.Fatal(err)
	}
	p := NewPlanner(dir)
	defer p.Close()
	p.max = 1
	for i, c := range []struct {
		from, to Vec3
		tile     string
	}{
		{Vec3{12, 12, 0}, Vec3{48, 48, 0}, "0013131.mmtile"},
		{Vec3{-589, 12, 0}, Vec3{-587, 13, 0}, tileFile(fixtureMap, -588, 12)},
		{Vec3{12, 12, 0}, Vec3{48, 48, 0}, "0013131.mmtile"},
	} {
		path, err := p.Plan(fixtureMap, c.from, c.to)
		if err != nil || path.Status != Complete {
			t.Fatalf("plan %d: %+v %v", i, path, err)
		}
		if got := p.Tiles(fixtureMap); len(got) != 1 || got[0] != c.tile {
			t.Fatalf("plan %d: tiles %v, want %s", i, got, c.tile)
		}
	}
}

func TestTileErrorNamesUnknownCodes(t *testing.T) {
	if err := tileError(-3); !strings.Contains(err.Error(), "truncated") {
		t.Fatal(err)
	}
	if err := tileError(-9); err.Error() != "unknown tile error -9" {
		t.Fatal(err)
	}
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
