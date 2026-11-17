// The Hakkar outer leg's lake basin (ZG walk 104 T28): the party reached the north shore's stand, then the run leader had
// to cross the lake for the two 15043 packs on the islet and the lead bot stopped in water every time ("lead in water:
// yes; cmd move path type 0x81; its own move failing: stay walk resting after 3 failed walks"). No dry floor reaches that
// islet from the north shore, the west rim, the lower ring or the east shore (plan-probe water 40-160 yd on every hop), so
// the packs are dropped and the lower-ring pack (old pull group 4) is walked to around the lake's west rim instead.
package main

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// zgHakkarOuterLeg is the lake-basin leg's index in dungeons/zul_gurub.json.
const zgHakkarOuterLeg = 7

// zgHakkarOuterIslet is the box of the lake islet the two dropped 15043 packs stood on (x, y, z bounds): the packs'
// positions sit on the islet's dry terrace at z 10.6-14.6, above the water the lead bot cannot cross.
var zgHakkarOuterIslet = [6]float64{-11885, -11860, -1710, -1610, 9, 16}

// zgHakkarOuterRingApproach is the dry approach the lower-ring pack takes around the lake's west rim: the walk-104 frames
// (fz5d-1, fz6a-1, fz6d-1) stood on the first three, the fourth is the leg's own lower-ring row. Keep the order: each hop
// is a path-check server 0x1 ground path and the last point stands 22 yd from the pack.
var zgHakkarOuterRingApproach = []dungeon.Point{
	{X: -11910, Y: -1523.8, Z: 26.2},
	{X: -11944.9, Y: -1532.6, Z: 40.2},
	{X: -11946.7, Y: -1600, Z: 37.3},
	{X: -11886.388, Y: -1751.4547, Z: 12.036596},
}

// TestZulGurubHakkarOuterDropsWaterLockedPacks: no pull group of the leg stands on the lake islet. The islet's two 15043
// packs (walk 104's pull groups 2 and 3, at -11873 -1634 and -11875 -1695) are only reachable by swimming: the run leader
// crossed in one water hop and the playerbot lead stalled in the water on every attempt (fz5c-1, fz5d-1, fz6e-2 all
// paused at step 553). The walk-29 fix dropped the water-only 11374 fish groups the same way.
func TestZulGurubHakkarOuterDropsWaterLockedPacks(t *testing.T) {
	p, err := dungeon.Load(filepath.Join(plansDirT, "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Legs) <= zgHakkarOuterLeg {
		t.Fatalf("the plan has %d legs, want more than %d", len(p.Legs), zgHakkarOuterLeg)
	}
	leg := p.Legs[zgHakkarOuterLeg]
	if len(leg.PullGroups) != 21 {
		t.Errorf("leg %s: %d pull groups, want 21 (the two islet packs dropped)", leg.Name, len(leg.PullGroups))
	}
	b := zgHakkarOuterIslet
	for gi, g := range leg.PullGroups {
		for _, q := range g.Positions {
			if q.X >= float32(b[0]) && q.X <= float32(b[1]) && q.Y >= float32(b[2]) && q.Y <= float32(b[3]) &&
				q.Z >= float32(b[4]) && q.Z <= float32(b[5]) {
				t.Errorf("leg %s pull group %d (%v): position %.1f %.1f %.1f stands on the unreachable lake islet",
					leg.Name, gi, g.Entries, q.X, q.Y, q.Z)
			}
		}
	}
}

// TestZulGurubHakkarOuterRingApproach: the lower-ring pack (the 15043s at -11901.3 -1733.0) carries the dry west-rim
// approach, in order, and the last point stands in the run leader's sight of the pack (its 60 yd see range, at most 50
// wanted) so the pull goes out from there instead of the run leader cutting its own legs over the lake.
func TestZulGurubHakkarOuterRingApproach(t *testing.T) {
	p, err := dungeon.Load(filepath.Join(plansDirT, "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	leg := p.Legs[zgHakkarOuterLeg]
	var ring *dungeon.PullGroup
	for gi := range leg.PullGroups {
		for _, q := range leg.PullGroups[gi].Positions {
			if math.Abs(float64(q.X)-(-11901.3)) < 0.01 && math.Abs(float64(q.Y)-(-1733.02)) < 0.01 {
				ring = &leg.PullGroups[gi]
			}
		}
	}
	if ring == nil {
		t.Fatalf("leg %s has no pull group at -11901.3 -1733.02", leg.Name)
	}
	if len(ring.Approach) != len(zgHakkarOuterRingApproach) {
		t.Fatalf("the ring pack's approach: %d points, want %d", len(ring.Approach), len(zgHakkarOuterRingApproach))
	}
	for i, want := range zgHakkarOuterRingApproach {
		got := ring.Approach[i]
		if math.Abs(float64(got.X-want.X)) > 1e-4 || math.Abs(float64(got.Y-want.Y)) > 1e-4 ||
			math.Abs(float64(got.Z-want.Z)) > 1e-4 {
			t.Errorf("approach[%d]: %.3f %.3f %.3f, want %.3f %.3f %.3f", i, got.X, got.Y, got.Z, want.X, want.Y, want.Z)
		}
	}
	dist := func(a, b dungeon.Point) float64 {
		dx := float64(a.X - b.X)
		dy := float64(a.Y - b.Y)
		dz := float64(a.Z - b.Z)
		return math.Sqrt(dx*dx + dy*dy + dz*dz)
	}
	// The hops between the points are 38.6, 67.5 and 165.0 yd: the exporter cuts the two over 50 yd into move rows along
	// the rim's own path (leg 7's rows 552-575 in the migration), so only a hop far past the leg cap would be wrong.
	for i := 1; i < len(ring.Approach); i++ {
		if d := dist(ring.Approach[i-1], ring.Approach[i]); d > 200 {
			t.Errorf("approach[%d] -> approach[%d]: %.1f yd, want at most 200", i-1, i, d)
		}
	}
	var cx, cy, cz float64
	for _, q := range ring.Positions {
		cx += float64(q.X)
		cy += float64(q.Y)
		cz += float64(q.Z)
	}
	n := float64(len(ring.Positions))
	centre := dungeon.Point{X: float32(cx / n), Y: float32(cy / n), Z: float32(cz / n)}
	if d := dist(ring.Approach[len(ring.Approach)-1], centre); d > 50 {
		t.Errorf("the stand is %.1f yd from the pack centre, want at most 50 (RunLeaderReachYd)", d)
	}
}

// zgHakkarOuterGroup4Approach is the dry rim walk that reaches the plateau pack's edge in pull group 4 (the 15043 four at
// -11723.0 -1734.6): the first point is the lead bot Darin's own position in walk 116 st16a-1 (17:07:48 UTC, z 10.5, the
// floor 11.06), the next two are its party cells (17:08:02), and the stand is the plateau's edge (-11742.4 -1735.9, floor
// 17.53). The first hop is the swim the raid itself made; the three rim hops are dry.
var zgHakkarOuterGroup4Approach = []dungeon.Point{
	{X: -11788.2, Y: -1735.1, Z: 11.06},
	{X: -11774.3, Y: -1734.8, Z: 15.35},
	{X: -11764.5, Y: -1733.2, Z: 17.19},
	{X: -11742.4, Y: -1735.9, Z: 17.53},
}

// TestZGHakkarOuterGroup4DryStand: the plateau pack (15043 x4 at -11723.0 -1734.6) is pulled from a dry stand on its rim,
// not walked to at its centre. Walk 116 step 581 (TortoiseBots-audit tests/raid-zg-walk116) stopped in the gather with the
// anchor on the lake bed at -11774.7 -1776.5 -15.3, 11.6 yd under the lead: the plan's pull group 4 had no approach, so the
// run leader crossed the lake to the pack and the step's gather anchor was taken in the water. With stand_pull the party
// holds at the rim stand and the pack comes up to it, and the gather anchor is the stand's own dry row.
func TestZGHakkarOuterGroup4DryStand(t *testing.T) {
	p, err := dungeon.Load(filepath.Join(plansDirT, "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	leg := p.Legs[zgHakkarOuterLeg]
	var pack *dungeon.PullGroup
	for gi := range leg.PullGroups {
		g := &leg.PullGroups[gi]
		if len(g.Positions) != 4 {
			continue
		}
		var cx, cy float64
		for _, q := range g.Positions {
			cx += float64(q.X)
			cy += float64(q.Y)
		}
		if math.Abs(cx/4+11723.0) < 5 && math.Abs(cy/4+1734.6) < 5 {
			pack = g
		}
	}
	if pack == nil {
		t.Fatalf("leg %s has no 4-body pack within 5 yd of -11723.0 -1734.6", leg.Name)
	}
	if !pack.StandPull {
		t.Error("the plateau pack is not a stand pull: the run leader walks to the pack's centre and gathers on the lake bed")
	}
	if len(pack.Approach) == 0 {
		t.Fatal("approach is empty: the run walks the lake to the pack and the gather stalls on the rim")
	}
	if len(pack.Approach) != len(zgHakkarOuterGroup4Approach) {
		t.Fatalf("the plateau pack's approach: %d points, want %d", len(pack.Approach), len(zgHakkarOuterGroup4Approach))
	}
	for i, want := range zgHakkarOuterGroup4Approach {
		got := pack.Approach[i]
		if math.Abs(float64(got.X-want.X)) > 1e-4 || math.Abs(float64(got.Y-want.Y)) > 1e-4 ||
			math.Abs(float64(got.Z-want.Z)) > 1e-4 {
			t.Errorf("approach[%d]: %.3f %.3f %.3f, want %.3f %.3f %.3f", i, got.X, got.Y, got.Z, want.X, want.Y, want.Z)
		}
	}
	dist := func(a, b dungeon.Point) float64 {
		dx := float64(a.X - b.X)
		dy := float64(a.Y - b.Y)
		dz := float64(a.Z - b.Z)
		return math.Sqrt(dx*dx + dy*dy + dz*dz)
	}
	var cx, cy, cz float64
	for _, q := range pack.Positions {
		cx += float64(q.X)
		cy += float64(q.Y)
		cz += float64(q.Z)
	}
	n := float64(len(pack.Positions))
	centre := dungeon.Point{X: float32(cx / n), Y: float32(cy / n), Z: float32(cz / n)}
	stand := pack.Approach[len(pack.Approach)-1]
	dx, dy := float64(stand.X-centre.X), float64(stand.Y-centre.Y)
	if d := math.Sqrt(dx*dx + dy*dy); d < 15 || d > 30 {
		t.Errorf("the stand is %.1f yd from the pack centre (2D), want 15-30: the run leader's shot reaches it and the pack has to walk to the rim", d)
	}
	if d := dist(stand, centre); d > 50 {
		t.Errorf("the stand is %.1f yd from the pack centre, want at most 50 (RunLeaderReachYd)", d)
	}
	// Every approach point is on the walked rim's floor (z 11.1-17.5); the lake bed under it is z -16..-12, so a point
	// below z 5 is a point taken in the water (the trap this fix removes).
	for i, q := range pack.Approach {
		if q.Z < 5 {
			t.Errorf("approach[%d] (%.1f %.1f %.1f) stands on the lake bed, not the rim", i, q.X, q.Y, q.Z)
		}
	}
	// The rim chain keeps the swim to its first hop: the three hops above the rim are short and walkable in one go.
	for i := 1; i < len(pack.Approach); i++ {
		if d := dist(pack.Approach[i-1], pack.Approach[i]); d > 50 {
			t.Errorf("approach[%d] -> approach[%d]: %.1f yd, want at most 50 (one run leader hop)", i-1, i, d)
		}
	}
}
