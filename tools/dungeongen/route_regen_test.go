package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/navmesh/navfixture"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

func regenUphillEdges(l [][]float64, order []int) []int {
	var out []int
	for i := 0; i+1 < len(order); i++ {
		if uphill(l, order[i], order[i+1]) {
			out = append(out, i)
		}
	}
	return out
}

// TestRegenOrderDownhill: a one-way drop (the causeway to the pool) whose nearest-neighbour order walks back up it; the
// regenerated order walks downhill only. The failing case is pinned first: the naive order must contain that uphill hop.
func TestRegenOrderDownhill(t *testing.T) {
	// 0: upper near the start, 1: the pool (below, only reachable downhill), 2: upper far.
	l := [][]float64{
		{0, 10, 20},
		{300, 0, 300},
		{20, 10, 0},
	}
	from := []float64{5, 400, 22}
	naive := regenNaiveOrder(l, from)
	if got := regenUphillEdges(l, naive); len(got) == 0 {
		t.Fatalf("the naive order %v has no uphill hop: the test does not reproduce the defect", naive)
	}
	order, oneWays := regenOrder(l, from, nil)
	if len(order) != 3 {
		t.Fatalf("order %v: want all 3 stops", order)
	}
	if got := regenUphillEdges(l, order); len(got) > 0 {
		t.Errorf("order %v walks uphill at %v (the drop is one-way): want upper first, then the pool", order, got)
	}
	if order[2] != 1 {
		t.Errorf("order %v: the pool (1) is last", order)
	}
	if len(oneWays) == 0 {
		t.Error("no one-way pair flagged: the 10 vs 300 yd drop is one")
	}
}

// writeRegenFixture writes a map's mmap and one tile with per-polygon flags (navfixture.WriteDir paints every polygon ground).
func writeRegenFixture(t *testing.T, mapID uint32, polys []navfixture.Poly, flags []uint16) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(fmt.Sprintf("%03d.mmap", mapID), navfixture.MmapParams())
	tx := float64(32) - 12/navfixture.Tile // the tile holding (12, 12), as navfixture.WriteDir names it
	gy := int(tx)
	write(fmt.Sprintf("%03d%02d%02d.mmtile", mapID, gy, gy), navfixture.BuildTile(polys, flags, 0, 0))
	return dir
}

// regenRoom is a 4-yd cell room x 10..150, y 10..90 at z 0, with the cells of the given rectangles painted water (flag
// navmesh.NavWater) instead of ground.
func regenRoom(water [][4]float32) (polys []navfixture.Poly, flags []uint16) {
	for x := float32(10); x < 150; x += 4 {
		for y := float32(10); y < 90; y += 4 {
			wet := false
			for _, r := range water {
				if x >= r[0] && x+4 <= r[2] && y >= r[1] && y+4 <= r[3] {
					wet = true
				}
			}
			polys = append(polys, navfixture.Square(x, y, x+4, y+4, 0))
			if wet {
				flags = append(flags, navmesh.NavWater)
			} else {
				flags = append(flags, navmesh.NavGround)
			}
		}
	}
	return polys, flags
}

func regenPlanner(t *testing.T, mapID uint32, polys []navfixture.Poly, flags []uint16) (*navmesh.Planner, string) {
	t.Helper()
	dir := writeRegenFixture(t, mapID, polys, flags)
	pl := navmesh.NewPlanner(dir)
	t.Cleanup(pl.Close)
	return pl, dir
}

// TestRegenMovesAnchorOutOfWater: an anchor standing in water is moved to dry ground within 25 yd.
func TestRegenMovesAnchorOutOfWater(t *testing.T) {
	const mapID = 1
	polys, flags := regenRoom([][4]float32{{50, 30, 90, 70}}) // a 40x40 yd pool in the room
	pl, dir := regenPlanner(t, mapID, polys, flags)
	r := &regen{mapID: mapID, nav: pl, mmaps: dir, plan: &dungeon.Plan{Map: mapID}}
	anchor := dungeon.Point{X: 70, Y: 50, Z: 0}
	if _, ok, err := r.plane(anchor); err != nil || ok {
		t.Fatalf("the anchor at %.0f %.0f is not in water (ok %v, err %v): the test does not reproduce the defect", anchor.X, anchor.Y, ok, err)
	}
	moved, note, err := r.placeAnchor("stand", anchor, nil, nil, map[int]bool{}, -1, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if moved == anchor {
		t.Fatalf("the water anchor was not moved (%s)", note)
	}
	if d := regenDist3(anchor, moved); d > regenMaxMoveYd {
		t.Errorf("moved %.1f yd (at most %.0f)", d, regenMaxMoveYd)
	}
	if _, ok, err := r.plane(moved); err != nil || !ok {
		t.Errorf("the new anchor %.1f %.1f %.1f is not on plain ground (ok %v, err %v)", moved.X, moved.Y, moved.Z, ok, err)
	}
	if note == "" {
		t.Error("no note for the move")
	}
}

// TestRegenRouteAvoidsUnpulledAggro: a straight walk from one end of a room to the other crosses an unpulled pack's aggro; the
// regenerated route detours round it.
func TestRegenRouteAvoidsUnpulledAggro(t *testing.T) {
	const mapID = 1
	polys, flags := regenRoom(nil)
	pl, dir := regenPlanner(t, mapID, polys, flags)
	r := &regen{mapID: mapID, nav: pl, mmaps: dir, plan: &dungeon.Plan{Map: mapID}}
	pack := []dungeon.Point{{X: 80, Y: 48, Z: 0}, {X: 80, Y: 52, Z: 0}}
	var spawns []regenSpawn
	for i, p := range pack {
		spawns = append(spawns, regenSpawn{
			sp:    worldspawn.Spawn{GUID: uint64(i + 1), Entry: uint32(1000 + i), Faction: 16, LevelMax: 60, Pos: p, DetectionYd: 20},
			reach: 20,
			owner: -1,
		})
	}
	from := dungeon.Point{X: 20, Y: 50, Z: 0}
	to := dungeon.Point{X: 140, Y: 50, Z: 0}
	// The failing case: the straight line runs through the pack (0 of the 25 yd wanted).
	for _, p := range pack {
		if d := regenSegDist(p, from, to); d >= 20+regenAggroSegYd {
			t.Fatalf("the straight walk is %.1f yd from a pack member: it does not cross the aggro", d)
		}
	}
	pts, err := r.route(from, to, map[int]bool{}, -1, 0, nil, spawns)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) == 0 || pts[len(pts)-1] != to {
		t.Fatalf("the route does not end at the goal: %v", pts)
	}
	prev := from
	for _, p := range pts {
		n := int(math.Ceil(regenDist3(prev, p) / regenMaxSegSamples))
		if n < 1 {
			n = 1
		}
		for k := 0; k <= n; k++ {
			f := float32(k) / float32(n)
			q := dungeon.Point{X: prev.X + (p.X-prev.X)*f, Y: prev.Y + (p.Y-prev.Y)*f, Z: prev.Z + (p.Z-prev.Z)*f}
			for _, s := range spawns {
				if d := regenDist3(q, s.sp.Pos); d < s.reach+regenAggroSegYd-0.01 {
					t.Errorf("the route comes %.1f yd from the pack at %.1f %.1f (want over %.0f)", d, q.X, q.Y, s.reach+regenAggroSegYd)
				}
			}
		}
		prev = p
	}
}
