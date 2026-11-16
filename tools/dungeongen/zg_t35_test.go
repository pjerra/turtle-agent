// Walk-107/108 fix, round 3 plus round 4 (TortoiseBots-audit briefs-omp/zg-route-T35-r3.md and zg-route-T35-final.md;
// the round-2 review codex-adv-zg-route-t35-r2 and the round-3 review codex r3).
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
//
// Round 4 (the lead's decision after review r3): the merged row tracks and times only 49155's patrol, so formation
// 49189's own pull row is back right after it, from round 1/2's own data (stand -12108 -1479 130.9, radius 1 at 49151's
// spawn -12153.9 -1489.43 130.725), with the patrol block matching 49189's 6-point loop. It is a fallback: the merged
// row already owns 49189's formation, so when the merged pull took the pack the fallback's own pack is gone and the run
// leader skips the row. turtle-agent's driver does the same: Driver.engage (internal/dungeon/driver.go:1769) notes
// "no living %v in view; taken as already dead" (driver.go:1799) and returns nil (driver.go:1800) when
// Driver.pickTarget (driver.go:1951) finds no living unit of the row's entries near its anchors (Driver.candidates,
// driver.go:1921, drops every u.Dead). Its stand stands 20.2 yd from 49155's east loop waypoint, inside that pack's
// 38 yd routeReach: by this row 49155 is dead (the merged row pulled it first), so its wake no longer matters, and that
// is the documented limit of the pair.
package main

import (
	"fmt"
	"math"
	"slices"
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
	gi := zgT35MergedRow(t, leg, legNo, packs)
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

// zgT35MergedRow is the leg's row that pulls F6's pack: the one whose pack holds the patroller's leader.
func zgT35MergedRow(t *testing.T, leg *dungeon.Leg, legNo int, packs [][]creature) int {
	t.Helper()
	for i := range packs {
		for _, c := range packs[i] {
			if c.GUID == zgT35F6 {
				return i
			}
		}
	}
	t.Fatalf("leg %d carries no pull group for the F6 bat formation %d: its camp (spawn %d) has no row", legNo, zgT35F6, zgT35F6)
	return -1
}

// TestZgJeklik49189FallbackRow pins round 4: formation 49189's own stand pull right after the merged row, as the
// fallback for a merged pull that leaves its pack standing. The merged row carries one patrol block and it is 49155's
// loop, so nothing in that row tracks or times 49189's patrol. The row back is round 1/2's: 49151's spawn
// -12153.9 -1489.43 130.725 as its point, radius 1, the stand -12108 -1479 130.9, and 49189's own 6-point loop as its
// patrol block (176.8 yd, 70.7 s at 2.5 yd/s). The test asserts it exists right after the merged row, that the run
// leader's spawn scoping gives it 49189's five units and no 49155 unit, that its stand is outside 49155's spawn wake
// (the choice the final brief makes) and 27.4 yd from 49189's loop, and that the row is skipped, not failed, when the
// pack is already dead (Driver.engage, internal/dungeon/driver.go:1799-1800).
//
// The documented limit: the stand is 20.2 yd from 49155's east loop waypoint -12125.1 -1468.27, inside that pack's
// 38 yd routeReach. That is safe only because 49155 is dead by this row: the merged row before it pulls 49155 first.
func TestZgJeklik49189FallbackRow(t *testing.T) {
	p, cs := zgLoadPlanAndExport(t)
	leg, legNo := zgW37LegByName(t, p, "high_priestess_jeklik")
	movement, err := zgReadMovementPaths("testdata/map309/movement.tsv")
	if err != nil {
		t.Fatal(err)
	}
	loop, ok := movement[zgT35Merged]
	if !ok || len(loop) < 3 {
		t.Fatalf("movement.tsv: %d has no loop", zgT35Merged)
	}
	merged := zgT35Formation(cs, zgT35Merged)
	if len(merged) != 5 {
		t.Fatalf("creatures.tsv: formation %d has %d units, want 5", zgT35Merged, len(merged))
	}
	f6 := zgT35Formation(cs, zgT35F6)
	packs := zgPacks(t, *leg, cs)
	gi := zgT35MergedRow(t, leg, legNo, packs)
	if gi+1 >= len(leg.PullGroups) {
		t.Fatalf("pull group %d (the merged %d/%d pull) is the leg's last: 49189's fallback row is missing", gi, zgT35F6, zgT35Merged)
	}
	fb, pack := leg.PullGroups[gi+1], packs[gi+1]
	// The pack: 49189's five, and not one unit of 49155's six (a row that also pulled 49155 would re-pull a pack the
	// merged row already fought).
	want := map[uint64]bool{}
	for _, c := range merged {
		want[c.GUID] = true
	}
	if len(pack) != len(merged) {
		t.Errorf("pull group %d (the fallback %d) pulls %d bodies, want %d: %v", gi+1, zgT35Merged, len(pack), len(merged), pack)
	}
	for _, c := range pack {
		if !want[c.GUID] {
			t.Errorf("pull group %d (the fallback %d) holds %s %d, which is no unit of formation %d", gi+1, zgT35Merged, c.Name, c.GUID, zgT35Merged)
		}
		if c.GroupLeader != zgT35Merged {
			t.Errorf("pull group %d: %s %d (group_leader %d) is not a follower of %d", gi+1, c.Name, c.GUID, c.GroupLeader, zgT35Merged)
		}
	}
	for _, c := range f6 {
		for _, m := range pack {
			if m.GUID == c.GUID {
				t.Errorf("pull group %d (the fallback %d) also pulls %s %d of formation %d: the merged row before it already did", gi+1, zgT35Merged, m.Name, m.GUID, zgT35F6)
			}
		}
	}
	// The run leader's own scoping (plan.PullGroup.StandPull -> TortoiseBots ScopeStandPullFacts): the row's pack is the
	// formations with a unit whose DB spawn lies within Radius of the row point, every other formation's units dropped.
	// That is what keeps the fallback off 49155's pack even though both formations share the entries 11368/14750.
	var cen dungeon.Point
	for _, q := range fb.Positions {
		cen.X, cen.Y, cen.Z = cen.X+q.X, cen.Y+q.Y, cen.Z+q.Z
	}
	n := float32(len(fb.Positions))
	cen.X, cen.Y, cen.Z = cen.X/n, cen.Y/n, cen.Z/n
	scoped := map[uint64]bool{}
	for _, c := range cs {
		if !hostileMob(c, nil, nil) || !slices.Contains(fb.Entries, c.Entry) {
			continue
		}
		if math.Hypot(float64(c.Pos.X-cen.X), float64(c.Pos.Y-cen.Y)) > float64(fb.Radius)+1 {
			continue
		}
		lead := c.GroupLeader
		if lead == 0 {
			lead = c.GUID
		}
		scoped[lead] = true
	}
	if len(scoped) != 1 || !scoped[zgT35Merged] {
		t.Errorf("pull group %d (the fallback %d) scopes formations %v by the spawns inside radius %g of %.1f %.1f: want %d alone", gi+1, zgT35Merged, scoped, fb.Radius, cen.X, cen.Y, zgT35Merged)
	}
	// The row point is 49151's DB spawn, the radius is round 1/2's 1, and the last approach point is their stand.
	if len(fb.Positions) != 1 {
		t.Fatalf("pull group %d carries %d positions, want 1 (49151's spawn)", gi+1, len(fb.Positions))
	}
	fbRow := fb.Positions[0]
	var bat creature
	for _, c := range merged {
		if c.GUID == 49151 {
			bat = c
		}
	}
	if bat.GUID == 0 {
		t.Fatalf("creatures.tsv: no 49151 in formation %d", zgT35Merged)
	}
	if d := dist(fbRow, bat.Pos); d > 0.5 {
		t.Errorf("pull group %d's point %.1f %.1f %.1f is %.2f yd from 49151's spawn: the row is round 1/2's, on 49151's spawn", gi+1, fbRow.X, fbRow.Y, fbRow.Z, d)
	}
	if fb.Radius != 1 {
		t.Errorf("pull group %d radius is %g, want 1: it scopes 49189 by 49151's spawn and must not take 49150 at 2.9 yd", gi+1, fb.Radius)
	}
	if !fb.StandPull {
		t.Errorf("pull group %d is not a stand pull: the formation walks a loop, so the party fights it from a stand", gi+1)
	}
	if len(fb.Approach) == 0 {
		t.Fatalf("pull group %d has no stand", gi+1)
	}
	stand := fb.Approach[len(fb.Approach)-1]
	if d := dist(stand, dungeon.Point{X: -12108, Y: -1479, Z: 130.9}); d > 0.5 {
		t.Errorf("pull group %d's stand is %.2f yd from round 2's -12108 -1479 130.9: the fallback keeps the r2 data", gi+1, d)
	}
	// 49155's spawn wake: the final brief's choice between the r1 and r2 data (they are the same row). By this row
	// 49155 is dead, so only the spawns matter here; the loop's 20.2 yd is the documented limit above.
	for _, c := range f6 {
		if d, reach := dist(stand, c.Pos), routeReach(p, c); d < reach+1 {
			t.Errorf("pull group %d's stand %.1f %.1f %.1f is %.1f yd from %s %d of formation %d, inside its %.1f yd wake reach", gi+1, stand.X, stand.Y, stand.Z, d, c.Name, c.GUID, zgT35F6, reach)
		}
	}
	// Its own pack's loop: outside the rider's detection at the party level (the party is not jumped at the stand) and
	// inside the tank's 28.5 yd shot (the gate fires; the east lane passes 27.4 yd from the stand, 2.6 yd of the loop
	// in shot each 70.7 s circuit, the thin window the r2 data has).
	near := math.Inf(1)
	for _, q := range loop {
		if d := dist(stand, q); d < near {
			near = d
		}
	}
	if near < zgT35OwnYd {
		t.Errorf("pull group %d's stand %.1f %.1f %.1f is %.1f yd from %d's loop, inside the %g yd own detection: the patrol walks the party", gi+1, stand.X, stand.Y, stand.Z, near, zgT35Merged, zgT35OwnYd)
	}
	if near > zgW37TankShotYd {
		t.Errorf("pull group %d's stand %.1f %.1f %.1f is %.1f yd from %d's loop, past the %g yd shot: the gate never fires", gi+1, stand.X, stand.Y, stand.Z, near, zgT35Merged, zgW37TankShotYd)
	}
	// The patrol block matching its loop: without it the row times nothing of the patrol it waits on.
	if fb.Patrol == nil {
		t.Errorf("pull group %d (the fallback %d) carries no patrol block: the merged row times only %d's loop", gi+1, zgT35Merged, zgT35F6)
	} else {
		if !strings.Contains(fb.Patrol.Source, fmt.Sprintf("creature_movement %d", zgT35Merged)) {
			t.Errorf("pull group %d's patrol source %q does not name creature_movement %d", gi+1, fb.Patrol.Source, zgT35Merged)
		}
		if len(fb.Patrol.Path) != len(loop) {
			t.Errorf("pull group %d's patrol path has %d points, want %d (%d's loop)", gi+1, len(fb.Patrol.Path), len(loop), zgT35Merged)
		} else {
			for k := range loop {
				if d := dist(fb.Patrol.Path[k], loop[k]); d > 0.5 {
					t.Errorf("pull group %d's patrol path[%d] is %.1f yd from %d's waypoint", gi+1, k, d, zgT35Merged)
					break
				}
			}
		}
		length := 0.0
		for k := range loop {
			length += dist(loop[k], loop[(k+1)%len(loop)])
		}
		if want := length / 2.5; math.Abs(float64(fb.Patrol.PeriodS)-want) > 1 {
			t.Errorf("pull group %d's patrol period is %.1f s, want %.1f (%d's %0.1f yd loop at 2.5 yd/s)", gi+1, fb.Patrol.PeriodS, want, zgT35Merged, length)
		}
	}
	// The mechanism that makes it a fallback: the merged row owns 49189's spawns (the route's first row wins), so the
	// fallback's gather finds nothing once the merged pull took them, and the run leader skips the row instead of
	// failing (internal/dungeon/driver.go:1799-1800). Here the route row is the assertable half.
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
	byLeader := map[uint64][]creature{}
	for _, c := range cs {
		if c.GroupLeader != 0 {
			byLeader[c.GroupLeader] = append(byLeader[c.GroupLeader], c)
		}
	}
	for _, c := range merged {
		o, pulled := routeOwnerOf(owners, byLeader, c)
		if !pulled || o.leg != 0 || o.item != zgT35Bodies {
			t.Errorf("%s %d is pulled by %s, not by the merged row (leg 0, item %d): the fallback's pack is the merged row's, so it is gone by then",
				c.Name, c.GUID, pulledBy(pulled, o), zgT35Bodies)
		}
	}
}

// zgT35Formation is a formation's units by its leader (creatures.tsv group_leader; the leader itself included).
func zgT35Formation(cs []creature, leader uint64) []creature {
	var out []creature
	for _, c := range cs {
		if c.GroupLeader == leader || c.GUID == leader {
			out = append(out, c)
		}
	}
	return out
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
