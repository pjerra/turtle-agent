// Walk-107/108 fix, round 3 (TortoiseBots-audit briefs-omp/zg-route-T35-r3.md; the round-2 review codex-adv-zg-route-t35-r2).
// Leg 0's step 36 (plan pull group 10) is the F6 bat formation 49155: all six of its units are OPTION_FORMATION_MOVE
// followers of 49155 (creatures.tsv group_flags 3), so the pack walks 49155's 8-point 74 s loop in one column; the DB
// spawn slots (49150/49154 at the camp, 49156/49159/49161 129-134 yd west) are only where the followers start.
//
// Round 1 put the stand at -12130 -1495.7 133, inside formation 49189's wake (the next pull group's bats 49151/49153
// stand 24.8 and 27.3 yd from it, 49189's loop 30.9 yd). Round 2 moved it to -12114 -1491 131.63: 25.3 yd from F6's
// loop with 19.9 yd in the tank's shot, and 39.9-64.6 yd from 49189's five spawns. Review round 2 held the branch anyway:
// 49189 is level 61 and its loop's first waypoint -12131.4 -1464.8 130.6 is 31.5 yd from that stand, inside 49189's
// routeReach (detection 20 - (58 - 61) = 23 yd, plus the raid's 10 yd spread = 33), so its live patrol could wake and
// walk the party while the row waits.
//
// Round 3's option (b), pulling 49189 first from its own stand, cannot be built here: a stand with the >= 18 yd shot
// window of 49189's loop that this row's gate needs is at most 35.4 yd from 49155's nearest waypoint (a 0.5 yd grid
// over the plateau; at most 28.4 yd with the stand also 23 yd clear of 49189's own lane), under 49155's 38 yd
// routeReach (detection 20 - (58 - 61) + 10 spread + its 5 yd wander), because the two lanes cross: 49155's waypoints
// stand 5.4-20.2 yd from 49189's loop. The row is therefore the merged pull (option c): radius 6.5 scopes both
// formations by their camp spawns (49154 at 4.5, 49155 at 4.4, 49153 at 6.3 yd), 11 bodies (6 + 5) under max_bodies 11,
// and no other formation (91549, the lone bat 29.8 yd above the plateau, is 33.9 yd out; the next row's 49137 camp is
// 69 yd out).
//
// The stand is -12114.5 -1446.8 131.32 (the navmesh floor; a complete 16 yd path from step 32's -12099 -1448):
// 23.95 yd from 49155's east waypoint -12125.1 -1468.27 with 27.7 yd of its loop in the 28.5 yd shot (11 s at
// 2.5 yd/s), 24.70 yd from 49189's east waypoint -12131.4 -1464.8 with 10.4 yd of its loop in shot (4 s), both outside
// the 23 yd rider detection at the plan's party level 58 and inside the tank's shot, clear line of sight to all 8 and
// all 6 waypoints, 50.8 yd from the row point (the run leader sees 60), and a walk that stays north of 49155's loop so
// neither patrol sees the party on the way in. The plan carries one patrol block per row, so 49189's 6-point loop is
// read from movement.tsv here: both loops' shot windows, the merged 11 bodies, the ownership and the merged timing are
// pinned, and every later patrol waypoint must keep its patroller's routeReach + 3 yd from the stand and the walk.
//
// Fails on origin/dev (no merged row, no patrol block). Fails on round 2's plan (quoted by the round-3 review): the
// stand -12114 -1491 is 31.5 yd from 49189's loop, whose row (pull group 11) is after it, want >= 36.
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

const (
	// zgT35F6 is the patroller whose loop the row carries and whose pack it pulls.
	zgT35F6 = 49155
	// zgT35Merged is the formation merged into the row in round 3: its loop had to be shot from the same stand.
	zgT35Merged = 49189
	// zgT35Bodies is the merged pack (F6's 6 + 49189's 5) and the row's max_bodies ceiling.
	zgT35Bodies = 11
	// zgT35OwnYd is how near its own patrol the party may stand: the pack's aggro at the plan's party level (20 yd for a
	// level-60 bat, 23 for the level-61 rider) plus 3 yd of head room, so the stand is outside detection and the tank
	// still reaches it (28.5 yd).
	zgT35OwnYd = 23.0
	// zgT35F6ShotYd is the loop length F6's loop must have inside the tank's 28.5 yd shot: a corner only the leader
	// touches would fire the gate for a second or two a circuit, which is how a stand pull hangs.
	zgT35F6ShotYd = 18.0
	// zgT35MergedShotYd is the same window for the merged formation's loop: it is narrower (the stand is 24.7 yd from
	// the lane), but its 10.4 yd at 2.5 yd/s is a 4 s firing window each round, which the gate reads.
	zgT35MergedShotYd = 8.0
	// zgT35WindowS is the shortest firing window, at 2.5 yd/s, either loop may give the gate.
	zgT35WindowS = 4.0
)

func TestZgJeklikF6And49189MergedPull(t *testing.T) {
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
	f6Loop, ok := movement[zgT35F6]
	if !ok || len(f6Loop) < 3 {
		t.Fatalf("movement.tsv: %d has no loop", zgT35F6)
	}
	mergedLoop, ok := movement[zgT35Merged]
	if !ok || len(mergedLoop) < 3 {
		t.Fatalf("movement.tsv: %d has no loop", zgT35Merged)
	}
	flags, err := zgT35GroupFlags()
	if err != nil {
		t.Fatal(err)
	}
	packs := zgPacks(t, *leg, cs)
	byGUID := map[uint64]creature{}
	for _, c := range cs {
		byGUID[c.GUID] = c
	}
	byLeader := map[uint64][]creature{}
	for _, c := range cs {
		if c.GroupLeader != 0 {
			byLeader[c.GroupLeader] = append(byLeader[c.GroupLeader], c)
		}
	}
	// The row that pulls F6's pack: the one whose pack holds its leader.
	gi := -1
	for i := range packs {
		for _, c := range packs[i] {
			if c.GUID == zgT35F6 {
				gi = i
				break
			}
		}
		if gi >= 0 {
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
	// The merged pack: F6's six and 49189's five, every unit a follower of one of the two leaders.
	leads := map[uint64]bool{}
	for _, c := range packs[gi] {
		lead := c.GroupLeader
		if lead == 0 {
			lead = c.GUID
		}
		leads[lead] = true
		if lead != zgT35F6 && lead != zgT35Merged {
			t.Errorf("pull group %d holds %s %d, whose formation is %d, not F6 %d or the merged %d: the row must scope these two packs only", gi, c.Name, c.GUID, lead, zgT35F6, zgT35Merged)
		}
		if flags[c.GUID]&1 == 0 {
			t.Errorf("pull group %d: %s %d (group_flags %d) is no formation follower (OPTION_FORMATION_MOVE, 0x1), so it does not walk the loop", gi, c.Name, c.GUID, flags[c.GUID])
		}
	}
	if len(packs[gi]) != zgT35Bodies {
		t.Errorf("pull group %d pulls %d bodies, want the merged %d (F6's 6 + %d's 5): %v", gi, len(packs[gi]), zgT35Bodies, zgT35Merged, leads)
	}
	if !leads[zgT35F6] || !leads[zgT35Merged] {
		t.Errorf("pull group %d does not hold both formations: leaders %v", gi, leads)
	}
	// The row's bodies ceiling: the merged pull is 11 bodies, so the row must say so (max_bodies -> the pull row's item).
	if g.MaxBodies != zgT35Bodies {
		t.Errorf("pull group %d max_bodies is %d, want %d: the merged pull's body count", gi, g.MaxBodies, zgT35Bodies)
	}
	// The patrol block: F6's own loop, point for point, with a period the driver can read. The plan carries one block
	// per row, so the merged loop is read from movement.tsv below.
	if g.Patrol == nil {
		t.Errorf("pull group %d carries no patrol block: the gate waits 600 s for a formation whose loop it cannot read", gi)
	} else {
		if !strings.Contains(g.Patrol.Source, fmt.Sprintf("creature_movement %d", zgT35F6)) {
			t.Errorf("pull group %d patrol block source %q does not name creature_movement %d", gi, g.Patrol.Source, zgT35F6)
		}
		if g.Patrol.PeriodS <= 0 {
			t.Errorf("pull group %d patrol period is %.1f s", gi, g.Patrol.PeriodS)
		}
		if len(g.Patrol.Path) != len(f6Loop) {
			t.Errorf("pull group %d patrol path has %d points, want %d (%d's loop)", gi, len(g.Patrol.Path), len(f6Loop), zgT35F6)
		} else {
			for k := range f6Loop {
				if d := dist(g.Patrol.Path[k], f6Loop[k]); d > 0.5 {
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
	// Both loops the row must reach: F6's (the block) and the merged row's (read from movement.tsv). Each must be
	// outside its own pack's detection (the party holds there without a body pull) and inside the tank's shot with a
	// real window, or the gate waits 600 s again.
	for _, want := range []struct {
		guid uint64
		loop []dungeon.Point
		shot float64
		what string
	}{
		{zgT35F6, f6Loop, zgT35F6ShotYd, "F6's loop (the row's patrol block)"},
		{zgT35Merged, mergedLoop, zgT35MergedShotYd, "the merged formation's loop"},
	} {
		near := math.Inf(1)
		for _, q := range want.loop {
			if d := dist(stand, q); d < near {
				near = d
			}
		}
		if near > zgW37TankShotYd {
			t.Errorf("pull group %d stand (%.1f %.1f %.1f) is %.1f yd from the nearest waypoint of %s, past the %g yd shot: the gate never fires on it",
				gi, stand.X, stand.Y, stand.Z, near, want.what, zgW37TankShotYd)
		}
		if near < zgT35OwnYd {
			t.Errorf("pull group %d stand (%.1f %.1f %.1f) is %.1f yd from %s, inside its pack's %g yd detection plus the party's spread: the patrol walks onto the party at the stand",
				gi, stand.X, stand.Y, stand.Z, near, want.what, zgT35OwnYd)
		}
		if shot := zgT35LoopInShot(stand, want.loop, zgW37TankShotYd); shot < want.shot {
			t.Errorf("pull group %d stand (%.1f %.1f %.1f) has %.1f yd of %s inside the %g yd shot, want >= %g: the gate only blinks at a corner instead of firing every circuit",
				gi, stand.X, stand.Y, stand.Z, shot, want.what, zgW37TankShotYd, want.shot)
		} else if secs := shot / 2.5; secs < zgT35WindowS {
			t.Errorf("pull group %d stand (%.1f %.1f %.1f) has %.1f yd (%.1f s at 2.5 yd/s) of %s in shot, want >= %g s: the merged pull's timing window",
				gi, stand.X, stand.Y, stand.Z, shot, secs, want.what, zgT35WindowS)
		}
	}
	// Line of sight from the stand to both loops, and the floor: a stand on the plate's north slope is fine, a stand in
	// air or behind the ridge is not.
	if pl != nil {
		for _, want := range []struct {
			guid uint64
			loop []dungeon.Point
		}{{zgT35F6, f6Loop}, {zgT35Merged, mergedLoop}} {
			clear := 0
			for _, w := range want.loop {
				r, err := pl.Raycast(309, navmesh.Vec3{stand.X, stand.Y, stand.Z + 1.5}, navmesh.Vec3{w.X, w.Y, w.Z + 1})
				if err == nil && r.Known && r.Clear {
					clear++
				}
			}
			if clear != len(want.loop) {
				t.Errorf("pull group %d: line of sight from the stand (%.1f %.1f %.1f) to %d of %d's %d waypoints", gi, stand.X, stand.Y, stand.Z, clear, want.guid, len(want.loop))
			}
		}
		if fl, err := pl.Floors(309, stand.X, stand.Y, stand.Z-3, stand.Z+3); err != nil || len(fl) == 0 {
			t.Errorf("pull group %d: no navmesh floor under the stand (%.1f %.1f %.1f): %v", gi, stand.X, stand.Y, stand.Z, err)
		} else if d := math.Abs(float64(fl[0].Z - stand.Z)); d > 0.5 {
			t.Errorf("pull group %d: the stand (%.1f %.1f %.1f) stands %.2f yd off the floor (%.2f): the run leader snaps", gi, stand.X, stand.Y, stand.Z, d, fl[0].Z)
		}
	}
	// Ownership through the route (first row wins): every unit of both formations belongs to this row, so no later row
	// waits for a pack it does not pull and the merged pull is the row the party fights.
	pts, err := routePoints(p)
	if err != nil {
		t.Fatal(err)
	}
	var owners []routePoint
	for _, q := range pts {
		if q.ownerIdx >= 0 {
			owners = append(owners, q)
		}
	}
	rowOf := func(c creature) (routePoint, bool) { return routeOwnerOf(owners, byLeader, c) }
	me, ok := rowOf(byGUID[zgT35F6])
	if !ok {
		t.Fatalf("no route row pulls F6 %d: the plan rows and the plan's pull groups disagree", zgT35F6)
	}
	if me.item != zgT35Bodies {
		t.Errorf("the merged row's item is %d, want the %d bodies ceiling", me.item, zgT35Bodies)
	}
	for _, c := range packs[gi] {
		o, pulled := rowOf(c)
		if !pulled || o.idx != me.idx {
			t.Errorf("%s %d is pulled by %s, not by the merged row (index %d): the row's radius must scope both formations", c.Name, c.GUID, pulledBy(pulled, o), me.idx)
		}
	}
	// The stand and every 2 yd of the walk-in keep every later pack's wake reach off its spawns and its patroller's
	// routeReach + the patrol spread off every waypoint of its loop. The merged row's own packs are the ones the row
	// walks out to pull, so they are skipped. On round 2's plan 49189 is the next row and its loop's 31.5 yd from the
	// stand fails this rule (reach 33 + 3 margin = 36): the review's finding.
	check := func(what string, q dungeon.Point) {
		for _, c := range cs {
			if !hostileMob(c, nil, nil) {
				continue
			}
			o, pulled := rowOf(c)
			if pulled && o.idx <= me.idx {
				continue // pulled and dead by now, this row's own pack included
			}
			if d, reach := dist(q, c.Pos), routeReach(p, c); d < reach+1 {
				t.Errorf("pull group %d %s (%.1f %.1f %.1f) is %.1f yd from %s %d (entry %d), inside its %.1f yd wake reach: %s",
					gi, what, q.X, q.Y, q.Z, d, c.Name, c.GUID, c.Entry, reach, pulledBy(pulled, o))
			}
		}
		for guid, wp := range movement {
			c, known := byGUID[guid]
			var o routePoint
			pulled := false
			lim := zgLaterYd
			if known {
				o, pulled = rowOf(c)
				lim = routeReach(p, c)
			}
			if pulled && o.idx <= me.idx {
				continue // pulled and dead by now, this row's own merged patrols included
			}
			lim += zgPatrolSpreadYd
			for _, w := range wp {
				if d := math.Abs(float64(w.Z - q.Z)); d > aggroest.FloorDz {
					continue // a flying loop over the party (the run leader's ring band is 12 yd)
				}
				if d := dist(q, w); d < lim {
					who := fmt.Sprintf("%d", guid)
					if known {
						who = fmt.Sprintf("%s %d", c.Name, c.Entry)
					}
					t.Errorf("pull group %d %s (%.1f %.1f %.1f) is %.1f yd from waypoint %.1f %.1f %.1f of %s's loop (%s), want >= %g (its routeReach %g + %g): the patrol walks the party",
						gi, what, q.X, q.Y, q.Z, d, w.X, w.Y, w.Z, who, pulledBy(pulled, o), lim, lim-zgPatrolSpreadYd, zgPatrolSpreadYd)
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
	if pl != nil {
		for k := 0; k+1 < len(stops); k++ {
			pa, err := pl.Plan(309, navmesh.Vec3{stops[k].X, stops[k].Y, stops[k].Z}, navmesh.Vec3{stops[k+1].X, stops[k+1].Y, stops[k+1].Z})
			if err != nil || pa.Status != navmesh.Complete {
				t.Errorf("pull group %d: the walk-in leg %.1f %.1f %.1f -> %.1f %.1f %.1f is no complete navmesh path (%s %v): the run leader cannot walk to its stand",
					gi, stops[k].X, stops[k].Y, stops[k].Z, stops[k+1].X, stops[k+1].Y, stops[k+1].Z, pa.Status, err)
			}
		}
	}
	for k := 0; k+1 < len(stops); k++ {
		for _, q := range zgSamplesAlong(stops[k], stops[k+1], zgSampleStep) {
			check("walk-in", q)
		}
	}
}

// zgT35GroupFlags reads creature_groups' flags per spawn (creatures.tsv group_flags): OPTION_FORMATION_MOVE (0x1) is
// what makes a follower walk its leader's loop, and without it the stand's shot at the loop would pull only the leader.
func zgT35GroupFlags() (map[uint64]uint32, error) {
	rs, err := rows("testdata/map309/creatures.tsv", "guid", "group_flags")
	if err != nil {
		return nil, err
	}
	r := &reader{path: "testdata/map309/creatures.tsv"}
	out := map[uint64]uint32{}
	for _, m := range rs {
		out[uint64(r.num(m, "guid"))] = uint32(r.num(m, "group_flags"))
	}
	return out, r.err
}

// pulledBy names the route row a spawn belongs to, for a failure message.
func pulledBy(pulled bool, o routePoint) string {
	if !pulled {
		return "no row pulls it"
	}
	return fmt.Sprintf("the row at leg %d step %d", o.leg, o.seq)
}

// zgT35LoopInShot is the length of a closed waypoint loop inside the tank's shot of a stand (2D: the loop and the
// stand are on the plateau, and the z band the run leader reads is 12 yd).
func zgT35LoopInShot(stand dungeon.Point, loop []dungeon.Point, shotYd float64) float64 {
	var sum float64
	for i := range loop {
		a, b := loop[i], loop[(i+1)%len(loop)]
		ax, ay := float64(a.X)-float64(stand.X), float64(a.Y)-float64(stand.Y)
		dx, dy := float64(b.X)-float64(a.X), float64(b.Y)-float64(a.Y)
		alen := math.Hypot(dx, dy)
		if alen == 0 {
			continue
		}
		c2 := ax*ax + ay*ay - shotYd*shotYd
		bq := 2 * (ax*dx + ay*dy)
		disc := bq*bq - 4*alen*alen*c2
		if disc <= 0 {
			if c2 <= 0 {
				sum += alen
			}
			continue
		}
		sq := math.Sqrt(disc)
		lo := math.Max(0, (-bq-sq)/(2*alen*alen))
		hi := math.Min(1, (-bq+sq)/(2*alen*alen))
		if hi > lo {
			sum += (hi - lo) * alen
		}
	}
	return sum
}
