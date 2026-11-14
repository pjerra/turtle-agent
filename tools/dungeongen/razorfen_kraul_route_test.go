package main

import (
	"fmt"
	"math"
	"path/filepath"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// walkedYd is the plan's walked length in the model the route-order audit uses: the exported rows' party points, summed
// hop by hop as straight lines (the navmesh path is longer everywhere; this is the comparable floor). A stand pull is
// fought from its stand, which routePoints already folds in.
func walkedYd(p *dungeon.Plan) (float64, int, int, error) {
	pts, err := routePoints(p)
	if err != nil {
		return 0, 0, 0, err
	}
	var yd float64
	pulls, bosses := 0, 0
	for i, q := range pts {
		switch q.kind {
		case planexport.KindPull:
			pulls++
		case planexport.KindBoss:
			bosses++
		}
		if i == 0 {
			continue
		}
		a := pts[i-1]
		yd += math.Hypot(math.Hypot(float64(q.px-a.px), float64(q.py-a.py)), float64(q.pz-a.pz))
	}
	return yd, pulls, bosses, nil
}

// TestRazorfenKraulRouteIsShort pins the route-order fix of 2026-11-14 (Sagiroth/TortoiseBots#1542, the Gnomeregan
// method of 2026-11-12). origin/dev's plan put each boss's nearest spawn clusters in list order: 682 rows, 122 pulls,
// 7,933 yd on the straight model here, 30 backtracks of 10,545 yd on the navmesh (the audit of 2026-11-12,
// tests/route-audit-1112/order/ORDER.md). The fixed plan moves every pack to the leg whose walk passes it and drops the
// six that stand 58-101 yd off that walk: 336 rows, 116 pulls, 6,080 yd here (7,727 yd on the navmesh), 13 backtracks,
// and 0 packs the walk wakes unplanned (trash-check). The bound and the per-leg pulls both fail on origin/dev's file
// (7,933 > 6,200 and 31/20/8/8/12/10/6/23/4): the pulls pin the assignment itself, so a pack back in a leg its walk does
// not pass fails here even when the straight line barely moves (the pit is a 2,600-yd walk and a 200-yd line).
func TestRazorfenKraulRouteIsShort(t *testing.T) {
	p, err := dungeon.Load(filepath.Join(plansDirT, "razorfen_kraul.json"))
	if err != nil {
		t.Fatal(err)
	}
	yd, pulls, bosses, err := walkedYd(p)
	if err != nil {
		t.Fatal(err)
	}
	const maxYd = 6200
	if yd > maxYd {
		t.Errorf("razorfen_kraul walks %.0f yd (straight hops between the exported rows): the route-order fix brings it under %d (origin/dev: 7,933). A pack back in a leg its walk does not pass, or a leg's anchor moved off the route, shows up here", yd, maxYd)
	}
	if pulls != 116 {
		t.Errorf("razorfen_kraul has %d pull rows, want 116 (the 2026-11-14 route order keeps 116 of the deployed 122 packs)", pulls)
	}
	// The assignment itself: each leg holds the packs its own walk passes, in that order's count. A pack back in a leg
	// its walk does not pass changes one of these numbers, whatever the straight length does.
	pts, err := routePoints(p)
	if err != nil {
		t.Fatal(err)
	}
	perLeg := make([]int, len(p.Legs))
	for _, q := range pts {
		if q.kind == planexport.KindPull {
			perLeg[q.leg]++
		}
	}
	want := []int{12, 31, 2, 6, 17, 10, 11, 27, 0}
	if fmt.Sprint(perLeg) != fmt.Sprint(want) {
		t.Errorf("razorfen_kraul pulls per leg %v, want %v (the route order's assignment: the 2026-11-14 audit's per-leg tables)", perLeg, want)
	}
	if bosses != 7 {
		t.Errorf("razorfen_kraul has %d boss rows, want 7 (the boss order is unchanged: Roogug, Aggem Thorncurse, Death Speaker Jargba, Overlord Ramtusk, Rotthorn, Agathelos the Raging, Charlga Razorflank)", bosses)
	}
	t.Logf("razorfen_kraul: %.0f yd, %d pulls, %d bosses", yd, pulls, bosses)
}
