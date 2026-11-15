// The Gnomeregan short route's order: the walk visits each pack on the boss-to-boss road it belongs to instead of
// zig-zagging between the dormitory, the Fallout caves and the Launch Bay's two rings (issue #1542, route-order audit
// 2026-11-12). The pins are the walk length, the packs and the dropped set, so a later edit that adds a detour or a pack
// back is a failure and not just a print.
package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// gnomerDevWalkYd is the walk before the re-order: origin/dev's plan, measured here the same way (the sum of the navmesh
// path between consecutive party points, entrance -> first row included). 16,669.2 yd, because the order switches decks
// 20 times; the floor-aware export of the same plan measures about the same. The re-order cuts it to the bound below.
const gnomerDevWalkYd = 16669.2

// gnomerMaxWalkYd caps the re-ordered walk: 4,234.2 yd measured here against a 2,999 yd straight route (4,352.2 yd on
// the floor-aware export's rows), 11% of headroom for a cut point that moves.
const gnomerMaxWalkYd = 4700.0

// gnomerPlan is the plan under test.
const gnomerPlan = "../../dungeons/gnomeregan.json"

// gnomerLegs is the re-ordered plan: the four bosses in boss order, and thermaplugg_route as the corridor leg that walks
// from Crowd Pummeler's hall to Mekgineer Thermaplugg's door with no boss of its own.
var gnomerLegs = []struct {
	name  string
	boss  uint32
	packs int
	units int
}{
	{"viscous_fallout", 7079, 26, 37},
	{"electrocutioner_6000", 6235, 28, 63},
	{"crowd_pummeler_9-60", 6229, 6, 13},
	{"thermaplugg_route", 0, 15, 38},
	{"mekgineer_thermaplugg", 7800, 4, 12},
}

// gnomerDropped are the 89 packs the short route leaves out (218 units), by the pack centre the audit lists: the
// dormitory rings' far side, the Fallout caves' side pockets, the west wing, the lower halls west of the tunnel, the
// Launch Bay's lower floor and east ring, the Engineering Labs' lower floor and south wing, and the final corridors'
// lower-deck platform. None of them stands within 18 yd of a kept pack, so 6 yd tells a re-added pack from a kept one.
var gnomerDropped = [][3]float64{
	{-532, 165, -155}, {-532, 161, -155}, {-554, 169, -155},
	{-564, 184, -194}, {-492, 196, -194}, {-478, 165, -208},
	{-498, 131, -209}, {-408, 159, -210}, {-398, 137, -211},
	{-374, 132, -209}, {-381, 98, -210}, {-409, 108, -210},
	{-430, 94, -209}, {-368, 79, -155}, {-368, 85, -155},
	{-367, 102, -155}, {-367, 127, -155}, {-425, 111, -156},
	{-436, 123, -156}, {-463, 111, -151}, {-435, 85, -152},
	{-546, 72, -201}, {-557, 97, -201}, {-571, 115, -202},
	{-582, 137, -202}, {-580, 98, -202}, {-594, 96, -199},
	{-574, 77, -204}, {-575, 47, -200}, {-591, 25, -180},
	{-560, 22, -180}, {-540, 34, -197}, {-524, 18, -156},
	{-620, 58, -200}, {-635, 73, -195}, {-641, 61, -193},
	{-643, 77, -183}, {-636, 89, -183}, {-652, 88, -179},
	{-642, 102, -192}, {-632, 126, -184}, {-606, 125, -188},
	{-613, 144, -200}, {-607, 194, -172}, {-570, 231, -159},
	{-546, 217, -157}, {-528, 206, -155}, {-468, 177, -155},
	{-429, 180, -155}, {-539, 450, -273}, {-532, 482, -273},
	{-505, 483, -273}, {-503, 528, -273}, {-521, 538, -273},
	{-540, 554, -273}, {-585, 548, -273}, {-484, 561, -231},
	{-472, 527, -231}, {-465, 487, -231}, {-475, 453, -231},
	{-475, 443, -231}, {-844, 432, -312}, {-823, 402, -316},
	{-797, 385, -316}, {-859, 391, -316}, {-877, 365, -316},
	{-867, 334, -316}, {-860, 292, -273}, {-795, 283, -273},
	{-768, 346, -273}, {-733, 241, -273}, {-780, 216, -273},
	{-798, 193, -273}, {-812, 180, -273}, {-811, 104, -265},
	{-764, 56, -257}, {-798, 538, -296}, {-755, 577, -304},
	{-744, 581, -291}, {-741, 558, -304}, {-707, 549, -296},
	{-677, 542, -286}, {-751, 600, -304}, {-740, 656, -322},
	{-727, 652, -309}, {-745, 626, -313}, {-727, 693, -331},
	{-709, 693, -318}, {-688, 705, -331},
}

// gnomerPlannerOrNil is the map-90 navmesh when the machine has the server's mmaps, nil otherwise.
func gnomerPlannerOrNil(t *testing.T) *navmesh.Planner {
	t.Helper()
	home, _ := os.UserHomeDir()
	for _, dir := range []string{os.Getenv("TURTLE_AGENT_MMAPS"), filepath.Join(home, "turtle-agent-mmaps")} {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "090.mmap")); err != nil {
			continue
		}
		if tiles, _ := filepath.Glob(filepath.Join(dir, "090*.mmtile")); len(tiles) == 0 {
			continue
		}
		return navmesh.NewPlanner(dir)
	}
	return nil
}

// gnomerPartyRows are the rows that put the party somewhere: an interaction row (open, loot) is not a waypoint.
func gnomerPartyRows(kind string) bool {
	switch kind {
	case planexport.KindMove, planexport.KindMoveDirect, planexport.KindMoveHopExact, planexport.KindPull, planexport.KindBoss, planexport.KindHold:
		return true
	}
	return false
}

// TestGnomereganRouteOrder pins the bosses, the packs and the dropped set of the short route, and the walk its rows take.
// It fails on origin/dev's plan (168 packs, 16,669.2 yd) and on any later edit that walks a deck twice or brings a
// dropped pack back.
func TestGnomereganRouteOrder(t *testing.T) {
	p, err := dungeon.Load(gnomerPlan)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Legs) != len(gnomerLegs) {
		t.Fatalf("%d legs, want %d", len(p.Legs), len(gnomerLegs))
	}
	var kept []dungeon.Point
	packs, units := 0, 0
	for i, want := range gnomerLegs {
		leg := p.Legs[i]
		if leg.Name != want.name {
			t.Errorf("leg %d is %q, want %q: the leg order is the boss order", i, leg.Name, want.name)
		}
		switch {
		case want.boss == 0 && leg.Boss != nil:
			t.Errorf("leg %d (%s) carries boss %d, want none: it is the corridor leg", i, leg.Name, leg.Boss.Entry)
		case want.boss != 0 && leg.Boss == nil:
			t.Errorf("leg %d (%s) has no boss, want %d", i, leg.Name, want.boss)
		case want.boss != 0 && leg.Boss.Entry != want.boss:
			t.Errorf("leg %d (%s) boss is %d, want %d", i, leg.Name, leg.Boss.Entry, want.boss)
		}
		legUnits := 0
		for _, g := range leg.PullGroups {
			legUnits += len(g.Entries)
			kept = append(kept, g.Positions...)
		}
		packs += len(leg.PullGroups)
		units += legUnits
		if len(leg.PullGroups) != want.packs || legUnits != want.units {
			t.Errorf("leg %d (%s): %d packs / %d units, want %d / %d", i, leg.Name, len(leg.PullGroups), legUnits, want.packs, want.units)
		}
	}
	if packs != 79 || units != 163 {
		t.Errorf("%d packs / %d units, want 79 / 163: the short route keeps the packs on the four boss walks", packs, units)
	}
	for _, dropped := range gnomerDropped {
		at := dungeon.Point{X: float32(dropped[0]), Y: float32(dropped[1]), Z: float32(dropped[2])}
		for _, keep := range kept {
			if d := gnomerDist(at, keep); d < 6 {
				t.Errorf("the dropped pack at %.0f %.0f %.0f is back: a pull group stands %.1f yd from it", dropped[0], dropped[1], dropped[2], d)
			}
		}
	}

	pl := gnomerPlannerOrNil(t)
	if pl == nil {
		t.Skip("no map 90 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 090.mmap and its tiles")
	}
	defer pl.Close()

	res, err := planexport.Build(p, planexport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var stops []dungeon.Point
	for _, s := range res.Rows.Steps {
		if gnomerPartyRows(s.Kind) {
			stops = append(stops, dungeon.Point{X: s.X, Y: s.Y, Z: s.Z})
		}
	}
	if len(stops) < 2 {
		t.Fatalf("%d party rows, want the walk", len(stops))
	}
	total := gnomerPath(t, pl, p.Map, p.Entrance, stops[0], "entrance")
	for i := 1; i < len(stops); i++ {
		total += gnomerPath(t, pl, p.Map, stops[i-1], stops[i], fmt.Sprintf("row %d", i+1))
	}
	t.Logf("gnomeregan walk %.1f yd (dev %.1f, straight about 2999, %d packs)", total, gnomerDevWalkYd, packs)
	if total > gnomerMaxWalkYd {
		t.Errorf("the walk is %.1f yd (dev %.1f, cap %.0f): the order switches decks again, put each pack in the leg whose walk passes it",
			total, gnomerDevWalkYd, gnomerMaxWalkYd)
	}
}

// gnomerPath is the navmesh path a->b; a missing or partial path fails the test, it is not zero.
func gnomerPath(t *testing.T, pl *navmesh.Planner, mapID uint32, a, b dungeon.Point, what string) float64 {
	t.Helper()
	path, err := pl.Plan(mapID, navmesh.Vec3{a.X, a.Y, a.Z}, navmesh.Vec3{b.X, b.Y, b.Z})
	if err != nil || path.Status != navmesh.Complete {
		t.Errorf("%s: no complete navmesh path %.1f %.1f %.1f -> %.1f %.1f %.1f: %v (%s %s)",
			what, a.X, a.Y, a.Z, b.X, b.Y, b.Z, err, path.Status, path.Reason)
		return 0
	}
	return float64(path.Length)
}

// gnomerDist is the 3D distance between two points.
func gnomerDist(a, b dungeon.Point) float64 {
	dx, dy, dz := float64(a.X-b.X), float64(a.Y-b.Y), float64(a.Z-b.Z)
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}
