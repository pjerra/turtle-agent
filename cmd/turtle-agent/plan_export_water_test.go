//go:build cgo

package main

import (
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/navmesh/navfixture"
)

// The cutter writes no point in or under a liquid: a straight ground walk over a NavWater pond keeps the water as one hop
// between the dry points either side (the planlint water rule's cut points).
func TestCutDropsWetPoints(t *testing.T) {
	const ground, water = 1, 8 // NAV_GROUND, NAV_WATER
	polys := []navfixture.Poly{
		{{10, 90, 0}, {110, 90, 0}, {110, 110, 0}, {10, 110, 0}},
		{{110, 90, 0}, {210, 90, 0}, {210, 110, 0}, {110, 110, 0}},
		{{210, 90, 0}, {310, 90, 0}, {310, 110, 0}, {210, 110, 0}},
	}
	pl := navmesh.NewPlanner(navfixture.WriteDirFlags(t, 1, polys, []uint16{ground, water, ground}))
	defer pl.Close()
	if liq, err := navmesh.LiquidAt(pl, 1, navmesh.Vec3{160, 100, 0}); err != nil || !liq.Wet {
		t.Fatalf("the pond point is not liquid: %+v, %v", liq, err)
	}
	via, notes, err := cutWith(pl, false)(1, dungeon.Point{X: 20, Y: 100, Z: 0}, dungeon.Point{X: 300, Y: 100, Z: 0})
	if err != nil {
		t.Fatal(err)
	}
	dropped := 0
	for _, p := range via {
		if liq, err := navmesh.LiquidAt(pl, 1, navmesh.Vec3{p.X, p.Y, p.Z}); err != nil || liq.Wet {
			t.Fatalf("cut point %v is in liquid (%+v, %v)", p, liq, err)
		}
	}
	for _, n := range notes {
		if n.Dropped && strings.Contains(n.Reason, "NavWater") {
			dropped++
		}
	}
	if dropped == 0 {
		t.Fatalf("no wet cut point was dropped: via %v, notes %v", via, notes)
	}
}
