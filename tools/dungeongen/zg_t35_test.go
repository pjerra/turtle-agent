// Walk-107/108 fix (TortoiseBots-audit briefs-omp/zg-route-T35.md; the evidence is the frames of
// tests/raid-zg-walk107/st7c-1 and tests/raid-zg-walk108/st8a-1). Leg 0's step 36 (plan pull group 10) is the F6
// bat formation 49155's camp: its leader's DB spawn stands 4.4 yd from the row's point, its followers 49150/49154
// hover there, and the leader walks an 8-point 74 s loop whose last waypoint is that camp (-12150.8 -1484.94). The
// old stand -12093 -1464 stood 59 yd from the row's point and 32.4 yd from the nearest waypoint of that loop, past
// the run leader's 28.5 yd shot, so the stand pull could never fire: walk 107 paused 600 s on "the pack's formation
// 0xf13000399e00c003 is alive but did not come within sight of the stand in 600 s", and walk 108 walked the tank to
// the loop point -12125.1 -1468.3 and failed ("Helina did not pull it within 45 s") with the pack 60-66 yd away.
//
// The rule this pins is the walk-38 camp rule (TestZgW38JeklikPatrolStands, TestZgW38JeklikCampsClearEveryLoop) on
// this row: it carries its patroller's loop (the run leader waits at the stand for a waypoint to come into its shot,
// and only a plan patrol block lets the driver read it), the stand is inside that shot of the loop and of the camp's
// spawns (the whole pack as the party meets it), outside the pack's own 20 yd detection, and 23 yd clear (20
// detection + 3 spread) of every loop not pulled yet. F6's west-spawned followers 49156/49159/49161 are not checked
// as spawns: they live at slots around their leader (126-134 yd from those DB spawns in the walk frames), so they come
// round with the loop. Fails on origin/dev: no group carries 49155's loop, and the stand is 32.4 yd from the nearest
// waypoint and 59.0 from the camp, past the shot.
package main

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/aggroest"
	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
)

// zgT35F6 is the patroller whose loop the row must carry and whose camp it pulls.
const zgT35F6 = 49155

// zgT35CampYd is how near the row's point a pack spawn must stand to count as the camp the party meets (F6's three
// camp spawns are 4.4, 4.5 and 7.4 yd out; its west followers are 126+).
const zgT35CampYd = 15.0

func TestZgJeklikF6CampStandReachesItsPatrol(t *testing.T) {
	p, cs := zgLoadPlanAndExport(t)
	pl := zgPlannerOrNil(t)
	if pl != nil {
		defer pl.Close()
	}
	leg, legNo := zgW37LegByName(t, p, "high_priestess_jeklik")
	movement, err := zgReadMovementPaths("testdata/map309/movement.tsv")
	if err != nil {
		t.Fatal(err)
	}
	loop, ok := movement[zgT35F6]
	if !ok || len(loop) < 3 {
		t.Fatalf("movement.tsv: %d has no loop", zgT35F6)
	}
	packs := zgPacks(t, *leg, cs)
	owner := map[uint64]int{}
	for gi, pk := range packs {
		for _, c := range pk {
			if _, seen := owner[c.GUID]; !seen {
				owner[c.GUID] = gi
			}
		}
	}
	own := func(gi int, guid uint64) bool {
		for _, c := range packs[gi] {
			if c.GUID == guid {
				return true
			}
		}
		return false
	}
	// The row that pulls F6's camp: the one whose pack holds its leader.
	gi := -1
	for i := range packs {
		if own(i, zgT35F6) {
			gi = i
			break
		}
	}
	if gi < 0 {
		t.Fatalf("leg %d carries no pull group for the F6 bat formation %d: its camp (spawn %d) has no row", legNo, zgT35F6, zgT35F6)
	}
	g := leg.PullGroups[gi]
	if !g.StandPull {
		t.Errorf("pull group %d (F6 %d) is not a stand pull", gi, zgT35F6)
	}
	// The patrol block: F6's own loop, point for point, with a period the driver can read.
	if g.Patrol == nil {
		t.Errorf("pull group %d carries no patrol block: the gate waits 600 s for a formation whose loop it cannot read", gi)
	} else {
		if !strings.Contains(g.Patrol.Source, fmt.Sprintf("creature_movement %d", zgT35F6)) {
			t.Errorf("pull group %d patrol block source %q does not name creature_movement %d", gi, g.Patrol.Source, zgT35F6)
		}
		if g.Patrol.PeriodS <= 0 {
			t.Errorf("pull group %d patrol period is %.1f s", gi, g.Patrol.PeriodS)
		}
		if len(g.Patrol.Path) != len(loop) {
			t.Errorf("pull group %d patrol path has %d points, want %d (%d's loop)", gi, len(g.Patrol.Path), len(loop), zgT35F6)
		} else {
			for k := range loop {
				if d := dist(g.Patrol.Path[k], loop[k]); d > 0.5 {
					t.Errorf("pull group %d patrol path[%d] is %.1f yd from %d's waypoint", gi, k, d, zgT35F6)
					break
				}
			}
		}
	}
	if len(g.Approach) == 0 {
		t.Fatalf("pull group %d (F6 %d) has no stand (approach point)", gi, zgT35F6)
	}
	stand := g.Approach[len(g.Approach)-1]
	var cen dungeon.Point
	for _, q := range g.Positions {
		cen.X += q.X
		cen.Y += q.Y
		cen.Z += q.Z
	}
	n := float32(len(g.Positions))
	cen.X, cen.Y, cen.Z = cen.X/n, cen.Y/n, cen.Z/n
	if d := dist(cen, stand); d > dungeon.RunLeaderSeeYd {
		t.Errorf("pull group %d stand is %.1f yd from its centre, want <= %d (the run leader's sight)", gi, d, dungeon.RunLeaderSeeYd)
	}
	// The gate's shot at the loop: the party holds and the tank shoots when the patrol comes round.
	nearest := math.Inf(1)
	for _, q := range loop {
		if d := dist(stand, q); d < nearest {
			nearest = d
		}
	}
	if nearest > zgW37TankShotYd {
		t.Errorf("pull group %d stand (%.1f %.1f %.1f) is %.1f yd from the nearest waypoint of %d's loop, past the %g yd shot: the gate never fires (walk 107 waited 600 s, walk 108's tank walked to the loop point and failed)",
			gi, stand.X, stand.Y, stand.Z, nearest, zgT35F6, zgW37TankShotYd)
	}
	// And at the camp the party meets: every camp spawn in the shot, none inside the pack's own detection.
	var camp []creature
	for _, c := range packs[gi] {
		if dist(c.Pos, cen) <= zgT35CampYd {
			camp = append(camp, c)
		}
	}
	if len(camp) < 3 {
		t.Fatalf("pull group %d: %d of its spawns stand within %g yd of its point, want the camp's three (49150, 49154, %d)", gi, len(camp), zgT35CampYd, zgT35F6)
	}
	for _, c := range camp {
		if d := dist(stand, c.Pos); d > zgW37TankShotYd {
			t.Errorf("pull group %d: its camp unit %s %d is %.1f yd from the stand, past the %g yd shot: the gate cannot pick it either", gi, c.Name, c.GUID, d, zgW37TankShotYd)
		} else if d < zgOwnMinYd {
			t.Errorf("pull group %d: its camp unit %s %d is %.1f yd from the stand, inside the %g yd the party keeps from its own pack (the patrol walks the party)", gi, c.Name, c.GUID, d, zgOwnMinYd)
		}
		if pl == nil {
			continue
		}
		r, err := pl.Raycast(309, navmesh.Vec3{stand.X, stand.Y, stand.Z + 1.5}, navmesh.Vec3{c.Pos.X, c.Pos.Y, c.Pos.Z + 1})
		if err != nil || !r.Known || !r.Clear {
			t.Errorf("pull group %d: no line of sight from the stand to %s %d", gi, c.Name, c.GUID)
		}
	}
	// The clearance: the stand and every 2 yd of the walk-in keep 23 yd (20 detection + 3 spread) from every waypoint
	// of every loop pulled later (or by no row), F6's own excepted: a patrol that walks the party while it waits is the
	// multi-formation wipe the stand pulls exist to stop.
	byGUID := map[uint64]creature{}
	for _, c := range cs {
		byGUID[c.GUID] = c
	}
	check := func(what string, q dungeon.Point) {
		for guid, wp := range movement {
			if o, ok := owner[guid]; ok && o <= gi {
				continue // pulled and dead by now
			}
			if own(gi, guid) || guid == zgT35F6 {
				continue // this row's own patrol: the stand sits inside its loop by design
			}
			for _, w := range wp {
				if d := math.Abs(float64(w.Z - q.Z)); d > aggroest.FloorDz {
					continue // a flying loop over the party (the run leader's ring band is 12 yd)
				}
				if d := dist(q, w); d < zgLaterYd+zgPatrolSpreadYd {
					who := fmt.Sprintf("%d", guid)
					if c, ok := byGUID[guid]; ok {
						who = fmt.Sprintf("%s %d", c.Name, c.Entry)
					}
					pulled := "no row"
					if o, ok := owner[guid]; ok {
						pulled = fmt.Sprintf("pull group %d", o)
					}
					t.Errorf("pull group %d %s (%.1f %.1f %.1f) is %.1f yd from waypoint %.1f %.1f %.1f of %s's loop (%s), want >= %g: the patrol walks the party",
						gi, what, q.X, q.Y, q.Z, d, w.X, w.Y, w.Z, who, pulled, zgLaterYd+zgPatrolSpreadYd)
					return
				}
			}
		}
	}
	check("stand", stand)
	from := leg.To
	if gi > 0 {
		prev := leg.PullGroups[gi-1]
		if len(prev.Approach) > 0 {
			from = prev.Approach[len(prev.Approach)-1]
		} else if len(prev.Positions) > 0 {
			from = prev.Positions[0]
		}
	}
	stops := append([]dungeon.Point{from}, g.Approach...)
	for k := 0; k+1 < len(stops); k++ {
		for _, q := range zgSamplesAlong(stops[k], stops[k+1], zgSampleStep) {
			check("walk-in", q)
		}
	}
}
