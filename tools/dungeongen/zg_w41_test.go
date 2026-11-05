// Walk-41 fix 1 (TortoiseBots-audit research/zg-bosses/JEKLIK-W41.md §1 and §3): Jeklik's last boss approach stands
// on the plateau's floor inside the run leader's reach pull, so the walk-pull gate passes without a move and the run
// is never paused on her boss row with no firing position.
package main

import (
	"fmt"
	"math"
	"testing"

	"github.com/pjerra/turtle-agent/internal/aggroest"
	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// zgW41JeklikHover is Jeklik's hover point in the walk-41 frames: 10.3 yd above the plan's boss position, where she
// stands out of combat (research/zg-bosses/JEKLIK-W41.md §1).
var zgW41JeklikHover = dungeon.Point{X: -12289.7, Y: -1382.2, Z: 144.6}

// TestZgJeklikBossApproachInPullRange is walk 41's fix 1. The last boss approach was -12241.56 -1399.14: 51.0 yd
// 2D and 52.7 yd 3D from Jeklik's hover point, past Faerie Fire (Feral)'s 29.5 yd, so ReachPullAction left the
// pull's firing stand search with nothing in range and the run paused on her boss row (tj-6/7/8 and mj-4..8: 7
// pauses in walk 41; walk-39 j4-2's bear walked to -12269.3 -1389.4 131.5 first and pulled). The plan point is that
// walked floor point now. The rules:
//   - the exported row right before the boss row is the plan's last approach (the part the run leader walks), on
//     the mesh floor of map 309 and reachable from the previous stand;
//   - it stands at most 28 yd 3D from the hover point (inside the pull's check against the firing range) and at
//     least 20 yd 2D (the pull's reach: Faerie Fire (Feral) at ~29.5 yd, the firing stand at 25 yd);
//   - the point and the walk-in samples keep 23 yd from every waypoint of every movement.tsv loop in the run
//     leader's 12 yd vertical band, except a loop the party already pulled (the run leader drops a dead
//     patroller's loop); the flying 91548-91555 bats at z >= 148 are outside the band.
func TestZgJeklikBossApproachInPullRange(t *testing.T) {
	p, cs := zgLoadPlanAndExport(t)
	leg, legIdx := zgW37LegByName(t, p, "high_priestess_jeklik")
	if leg.Boss == nil || len(leg.Boss.Approach) == 0 {
		t.Fatal("the jeklik leg has no boss approach")
	}
	at := leg.Boss.Approach[len(leg.Boss.Approach)-1]
	// (1) The reach: <= 28 yd 3D from her hover point, >= 20 yd 2D.
	if d := dist(at, zgW41JeklikHover); d > 28 {
		t.Errorf("the boss approach %.2f %.2f %.2f is %.1f yd 3D from Jeklik's hover point %.1f %.1f %.1f, want <= 28 (Faerie Fire (Feral) is 29.5 yd): the reach pull has nothing to fire from",
			at.X, at.Y, at.Z, d, zgW41JeklikHover.X, zgW41JeklikHover.Y, zgW41JeklikHover.Z)
	}
	if d := math.Hypot(float64(at.X-zgW41JeklikHover.X), float64(at.Y-zgW41JeklikHover.Y)); d < 20 {
		t.Errorf("the boss approach %.2f %.2f %.2f is %.1f yd 2D from Jeklik's hover point, want >= 20: the pull must not start inside her body",
			at.X, at.Y, at.Z, d)
	}
	// (2) The patrol loops: every waypoint in the floor band, minus the loops the party already pulled.
	movement, err := zgReadMovementPaths("testdata/map309/movement.tsv")
	if err != nil {
		t.Fatal(err)
	}
	// The exported rows of the leg (live flags) name the pulled rows: the boss row and the move row before it.
	rowsTrib := zgW37BuildRows(t, p, cs, legIdx)
	bossAt := -1
	for i, s := range rowsTrib.rows {
		if s.Kind == planexport.KindBoss && s.Entry == 14517 {
			bossAt = i
		}
	}
	if bossAt < 1 {
		t.Fatalf("no exported boss row for 14517 in the leg's %d rows", len(rowsTrib.rows))
	}
	approach := rowsTrib.rows[bossAt-1]
	if approach.Kind != planexport.KindMove || approach.Comment != "boss approach" {
		t.Fatalf("the row before the boss row is %s %q, want the boss approach move", approach.Kind, approach.Comment)
	}
	if d := dist(rowPoint(approach), at); d > 0.01 {
		t.Errorf("the plan's boss approach %.2f %.2f %.2f and the exported row (%.2f %.2f %.2f) differ by %.2f yd",
			at.X, at.Y, at.Z, approach.X, approach.Y, approach.Z, d)
	}
	byGUID := map[uint64]creature{}
	for _, c := range cs {
		byGUID[c.GUID] = c
	}
	check := func(what string, q dungeon.Point) {
		for guid, loop := range movement {
			if o, ok := rowsTrib.owner[guid]; ok && o <= bossAt {
				continue // pulled and dead by the time the party walks to her row
			}
			for _, w := range loop {
				if d := math.Abs(float64(w.Z - q.Z)); d > aggroest.FloorDz {
					continue // a flying loop above the party (the run leader's ring band is 12 yd)
				}
				if d := dist(q, w); d < zgLaterYd+zgPatrolSpreadYd {
					who := "a spawn"
					if c, ok := byGUID[guid]; ok {
						who = fmt.Sprintf("%s %d", c.Name, c.Entry)
					}
					t.Errorf("the boss approach %s (%.1f %.1f %.1f) is %.1f yd from waypoint %.1f %.1f %.1f of %s's loop (%d), want >= %g: the patrol walks the party",
						what, q.X, q.Y, q.Z, d, w.X, w.Y, w.Z, who, guid, zgLaterYd+zgPatrolSpreadYd)
					return
				}
			}
		}
	}
	check("point", at)
	// The walk-in: from the previous row's party point, through the plan's approach points, to the stand.
	from := rowsTrib.party(bossAt - 3) // the group-15 pull row (two move rows before the boss approach)
	if len(leg.Boss.Approach) > 1 {
		for _, q := range zgSamplesAlong(from, leg.Boss.Approach[0], zgSampleStep) {
			check("walk-in", q)
		}
		from = leg.Boss.Approach[0]
	}
	for _, q := range zgSamplesAlong(from, at, zgSampleStep) {
		check("walk-in", q)
	}
	// (3) On the floor, and the walk from the previous stand is complete.
	pl := zgPlannerOrNil(t)
	if pl == nil {
		t.Skip("the mesh floor and the walk-in path need the map-309 mmaps")
	}
	defer pl.Close()
	fl, err := pl.Floors(309, at.X, at.Y, at.Z-8, at.Z+8)
	if err != nil {
		t.Fatalf("floors at the boss approach: %v", err)
	}
	above := 0.0
	for _, f := range fl {
		if d := float64(f.Z) - float64(at.Z); d > above {
			above = d
		}
	}
	if above > 3 {
		t.Errorf("the boss approach %.2f %.2f %.2f stands %.1f yd under the mesh floor: the run leader cannot stand there", at.X, at.Y, at.Z, above)
	}
	start := rowsTrib.party(bossAt - 3)
	path, err := pl.Plan(309, navmesh.Vec3{start.X, start.Y, start.Z}, navmesh.Vec3{at.X, at.Y, at.Z})
	if err != nil {
		t.Fatalf("path to the boss approach: %v", err)
	}
	if path.Status != navmesh.Complete {
		t.Errorf("no complete navmesh path from the previous stand (%.1f %.1f %.1f) to the boss approach (%.1f %.1f %.1f): %s %s",
			start.X, start.Y, start.Z, at.X, at.Y, at.Z, path.Status, path.Reason)
	}
}
