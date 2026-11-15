// Route audit "LIVE" rows (TortoiseBots-audit tests/map-gps-B, map-gps-B 2026-11-15): the rows the GPS mapping run
// measured off their floor, plus the dire_maul_east T27 stand the first pull needs. Each test pins one plan's answer
// from floor-points.tsv (the FloorZ column, the server's own floor under the requested point), so a later edit that
// drops the fix fails here. The rows the run measured on their floor are not pinned: they did not move.
//
// What is NOT fixed here, and why (the measurement says a z change cannot do it):
//   - sunken_temple 137 (leg 0 group 6 approach[0]): the only floor under (-448.0 53.87) is the pool at -189.63.
//   - sunken_temple 655/717 (the two statue stands at y 133.4/133.5): the statue ring floor (-148.73) starts 1.6 yd
//     north of them (probed on a grid); below them is the -173 ring, 25 yd down. They need a 2 yd XY move.
//   - sunken_temple 1156: the floor under it is -148.45 (the shelf the row before it walks) 17 yd below the row, and the
//     pull after it stands 17 yd above that shelf: flooring the row hands the walk to the pull a 16.9 yd climb_anchor and
//     the joiners estimate 1 -> 7 bodies, so the leg's order owns it.
//   - sunken_temple 1338/1414/1838/2216/2225: the floor under each is a different level 31-79 yd from the row's
//     neighbours (see the columns in the report); flooring them drops the leg onto another storey.
//   - hateforge_quarry 441 (leg 5 group 6's pull row): the centroid (-7980.7 -3611.5) has no navmesh floor at the
//     route's level (155 and 351 above it only); the layer the route walks (111.4) is only on the boss's own XY.
//   - scholomance 497: the surface measured at (-174.0 70.0) is the 143.76 roof over the whole wing; the leg walks
//     at 102.8-104.8 and the row before it (-165.0 74.0 -102.96) already has the floor.
package main

import (
	"math"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// liveRowsPullYd is the run's shot range: a stand farther than this from the pack cannot open the pull.
const liveRowsPullYd = 28.5

// liveRowsLaneYd is how far the T27 stand has to stay off the 11464/11465 patrol lanes (T27: the level-57 party died
// to the chains those lanes bring).
const liveRowsLaneYd = 10

// legs 1/3/4 rows of blackrock_depths stood 0.5-1.0 yd above the -84.49/-60.03/-60.02 floor (map-gps-B steps 206,
// 409, 413): no floor within 5 yd at any of them, and the hop into the first was server NOPATH.
func TestBlackrockDepthsLiveRowsFloored(t *testing.T) {
	p := b6aLoad(t, "blackrock_depths")
	a := b6aApproach(p, 1, 37)
	if len(a) != 4 {
		t.Fatalf("leg 1 group 37: %d approach points, want 4", len(a))
	}
	b6aNear(t, "leg 1 group 37 approach[3]", a[3], 582.8, -164.4, -84.49)
	b6aNear(t, "leg 3 group 10 approach[0]", b6aApproach(p, 3, 10)[0], 663.2, 49.3, -60.03)
	b6aNear(t, "leg 4 to", p.Legs[4].To, 676, 30, -60.02)
}

// sunken_temple leg 4 group 1's first approach stood 1.2 yd above the -53.95 floor (map-gps-B step 2490): the pool
// shelves, the row's neighbours walk the -53 shelf.
func TestSunkenTempleLiveRowsFloored(t *testing.T) {
	p := b6aLoad(t, "sunken_temple")
	b6aNear(t, "leg 4 group 1 approach[0]", b6aApproach(p, 4, 1)[0], -544.53, 47.2, -53.95)
}

// hateforge_quarry leg 0 group 13's approach stood 0.6 yd above the 132.59 floor (map-gps-B step 28), on the same XY
// as group 12's pack spawn.
func TestHateforgeQuarryLiveRowsFloored(t *testing.T) {
	p := b6aLoad(t, "hateforge_quarry")
	b6aNear(t, "leg 0 group 13 approach[0]", b6aApproach(p, 0, 13)[0], -8234.23, -3371.84, 132.59)
}

// dire_maul_east T27: the first pull (leg 0 group 0, 13196) was server NOPATH and its fight is the patrol chain that
// wipes a level-57 party. It is a stand pull from cand1 (-175 -275 -4.03): 7.6 yd from the pack, clear line of sight
// (path-check ray), and 16.6/18.0 yd off the 11464/11465 lanes. cand2 has no line of sight (blocked at 2.2 yd),
// cand3 is off the navmesh, cand5 is 41.1 yd out (past the shot) and blocked too.
func TestDireMaulEastT27StandPull(t *testing.T) {
	p := b6aLoad(t, "dire_maul_east")
	g := p.Legs[0].PullGroups[0]
	if !g.StandPull {
		t.Fatalf("leg 0 group 0 is not a stand pull: the first pull needs a stand west of the 11464/11465 lanes (T27)")
	}
	if len(g.Approach) != 1 {
		t.Fatalf("leg 0 group 0: %d approach points, want 1 (the stand)", len(g.Approach))
	}
	stand := g.Approach[0]
	b6aNear(t, "leg 0 group 0 stand", stand, -175, -275, -4.03)
	if d := b6aDist(stand, g.Positions[0]); d > liveRowsPullYd {
		t.Errorf("the stand is %.1f yd from the pack: over the %g yd shot", d, liveRowsPullYd)
	}
	for leg := range p.Legs {
		for group := range p.Legs[leg].PullGroups {
			pat := p.Legs[leg].PullGroups[group].Patrol
			if pat == nil || !liveRowsLane(pat.Entries) {
				continue
			}
			for i := 0; i+1 < len(pat.Path); i++ {
				if d := liveRowsSegDist(stand, pat.Path[i], pat.Path[i+1]); d < liveRowsLaneYd {
					t.Errorf("the stand is %.1f yd from the %v lane (leg %d group %d, %.1f %.1f -> %.1f %.1f): under %g yd, it stands in it",
						d, pat.Entries, leg, group, pat.Path[i].X, pat.Path[i].Y, pat.Path[i+1].X, pat.Path[i+1].Y, float64(liveRowsLaneYd))
				}
			}
		}
	}
}

// liveRowsLane says the patrol path is one of the two lanes T27 names.
func liveRowsLane(entries []uint32) bool {
	for _, e := range entries {
		if e == 11464 || e == 11465 {
			return true
		}
	}
	return false
}

// liveRowsSegDist is the distance from a point to a path segment, in the XY plane (a patrol lane is a floor line).
func liveRowsSegDist(p, a, b dungeon.Point) float64 {
	dx, dy := float64(b.X-a.X), float64(b.Y-a.Y)
	l2 := dx*dx + dy*dy
	t := 0.0
	if l2 > 0 {
		t = math.Max(0, math.Min(1, (float64(p.X-a.X)*dx+float64(p.Y-a.Y)*dy)/l2))
	}
	return math.Hypot(float64(p.X)-(float64(a.X)+t*dx), float64(p.Y)-(float64(a.Y)+t*dy))
}
