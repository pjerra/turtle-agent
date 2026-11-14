// The route order of maraudon_purple (Sagiroth/TortoiseBots#1542, 2026-11-14): each pack sits on the leg whose walk
// passes it, in the order along that walk, and the packs no leg passes are gone. The walk here is the plan's own hop
// sum (entrance, leg `to`, every pack point, the boss: the rows the exporter writes without a cutter), which needs no
// navmesh, so every run of the suite measures it. The navmesh route of the plan this pins is 8,633 yd against the
// deployed rows' 36,333 (route-audit-1112/order/maraudon_purple-FIX.md).
package main

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// maraudonWalkYd caps the plan's own hop sum: the 2026-11-14 order comes in at 7,007 yd where the deployed order's
// plan points sum to 10,585, so a later edit that adds a detour or puts a pack back off its leg fails here.
const maraudonWalkYd = 7200.0

// maraudonPacks are the pull groups and units per leg of that order.
var maraudonPacks = []struct{ packs, units int }{
	{29, 91}, // lord_vyletongue
	{25, 59}, // celebras_the_cursed
	{23, 57}, // landslide
	{7, 34},  // tinkerer_gizlock
	{12, 48}, // rotgrip
	{5, 12},  // princess_theradras
}

func maraudonDist(a, b dungeon.Point) float64 {
	dx, dy, dz := float64(b.X-a.X), float64(b.Y-a.Y), float64(b.Z-a.Z)
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

func TestMaraudonPurpleRouteOrder(t *testing.T) {
	p, err := dungeon.Load(filepath.Join("..", "..", "dungeons", "maraudon_purple.json"))
	if err != nil {
		t.Fatal(err)
	}
	// The rows without a cutter: the plan's own points, in the order the run leader walks them.
	res, err := planexport.Build(p, planexport.Options{})
	if err != nil {
		t.Fatalf("maraudon_purple does not export without a cutter: %v", err)
	}
	prev, next := p.Entrance, dungeon.Point{}
	walk := 0.0
	for _, s := range res.Rows.Steps {
		next = dungeon.Point{X: s.X, Y: s.Y, Z: s.Z}
		walk += maraudonDist(prev, next)
		prev = next
	}
	t.Logf("maraudon_purple: %d pull groups, %d units, %d rows, %.1f yd of plan hops (cap %.0f)",
		countMaraudonPacks(p), countMaraudonUnits(p), len(res.Rows.Steps), walk, maraudonWalkYd)
	if walk > maraudonWalkYd {
		t.Errorf("the plan's own hop sum is %.1f yd, want <= %.0f (the route order of 2026-11-14: 7,007 yd here, 8,711 on the navmesh; the order before it was 10,585)", walk, maraudonWalkYd)
	}
	if len(p.Legs) != len(maraudonPacks) {
		t.Fatalf("%d legs, want %d", len(p.Legs), len(maraudonPacks))
	}
	for i, want := range maraudonPacks {
		got, units := 0, 0
		for _, g := range p.Legs[i].PullGroups {
			got++
			units += len(g.Entries)
		}
		if got != want.packs || units != want.units {
			t.Errorf("leg %d (%s): %d pull groups and %d units, want %d and %d (the route order of 2026-11-14)",
				i, p.Legs[i].Name, got, units, want.packs, want.units)
		}
	}
}

func countMaraudonPacks(p *dungeon.Plan) int {
	n := 0
	for _, l := range p.Legs {
		n += len(l.PullGroups)
	}
	return n
}

func countMaraudonUnits(p *dungeon.Plan) int {
	n := 0
	for _, l := range p.Legs {
		for _, g := range l.PullGroups {
			n += len(g.Entries)
		}
	}
	return n
}
