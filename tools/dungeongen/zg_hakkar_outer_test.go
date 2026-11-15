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
const zgHakkarOuterLeg = 8

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
