// Walk-107/108 fix, round 2 (TortoiseBots-audit briefs-omp/zg-route-T35-r2.md; the evidence is the frames of
// tests/raid-zg-walk107/st7c-1 and tests/raid-zg-walk108/st8a-1, and the round-1 review codex-adv-zg-route-t35-r1).
// Leg 0's step 36 (plan pull group 10) is the F6 bat formation 49155: all six of its units are OPTION_FORMATION_MOVE
// followers of 49155 (creatures.tsv group_flags 3), so the pack walks 49155's 8-point 74 s loop in one column and the
// DB spawn slots (49150/49154 at the camp, 49156/49159/49161 126-134 yd west in the walk frames) are only where the
// followers start. The pre-fix stand -12093 -1464 stood 32.4 yd from the loop's nearest waypoint, past the run
// leader's 28.5 yd shot: walk 107 paused 600 s ("the pack's formation 0xf13000399e00c003 is alive but did not come
// within sight of the stand in 600 s"), and walk 108's tank walked to the loop point -12125.1 -1468.3 and failed
// ("Helina did not pull it within 45 s") with the pack 60-66 yd from the stand.
//
// Round 1 put the stand at -12130 -1495.7 133, inside the shot of the camp's spawn slots but 24.8 and 27.3 yd from
// 49151/49153, two bats of the NEXT pull group's formation 49189: inside their wake reach (aggro 20 at level 60
// against the plan's party level 58, plus the raid's 10 yd spread) and inside 49189's own patrol loop's reach (30.9
// yd from the loop's east waypoint), so the route-aggro pin counted four new passes into that pack and 49189's patrol
// walks the party while it waits.
//
// The camp (-12150.8 -1484.94, the loop's last waypoint) cannot be shot from outside 49189's reach at all: 49189's
// bats stand 5.5 and 3.9 yd from it, so the most a stand 28.5 yd from it can reach is 29.3 and 32.3 yd (only in the
// narrow cone that clears both), and that point stands 20 yd from 49189's patrol loop. The row therefore shoots the
// loop's east corner -12125.1 -1468.27 from -12114 -1491 131.63 (the navmesh floor, 4.5 yd in from the plateau's
// south edge): 25.3 yd from the waypoint with 19.9 yd of the loop inside the tank's 28.5 yd shot (8 s of each 74 s
// circuit), 39.9, 40.7, 59.5, 64.6 and 61.3 yd from 49189's five spawns (wake reach 30, 30, 30, 30 and 33 yd) and
// 31.5 yd from its loop, and clear line of sight to every waypoint. The whole pack is with the leader, so the pull
// meets all six at the corner. The row adds no route-aggro pass at all: the walk clears even its own pack's spawn
// slots (49155 38.4 and 49154 39.0 yd, reach 38 each).
//
// The rule this pins: the stand is inside the tank's shot of the loop it pulls, outside its own pack's detection
// (23 yd: aggro at the party level plus the spread), and the stand and every 2 yd of the walk-in keep every later
// pack's wake reach off its spawns and the repo's 23 yd off its loop. Fails on origin/dev: no group carries 49155's
// loop, the stand is 32.4 yd from the loop and the walk passes 17.6 yd from 49189's loop. Fails on the round-1 stand:
// 24.8 and 27.3 yd from 49151/49153 and 30.9 yd from 49189's loop.
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

// zgT35F6 is the patroller whose loop the row must carry and whose pack it pulls.
const zgT35F6 = 49155

// zgT35Next is the formation of the pull group after this row: its five spawns must stay out of the stand's and the
// walk-in's wake reach, or the party pulls the next pack early.
const zgT35Next = 49189

// zgT35OwnYd is how near its own patrol the party may stand: the pack's aggro at the plan's party level (20 yd for a
// level-60 bat, 23 for the level-61 rider) plus the raid's 3 yd of head room, so the stand is outside detection and
// the tank still reaches it (28.5 yd).
const zgT35OwnYd = 23.0

// zgT35ShotYd is the loop length the stand must have inside the tank's 28.5 yd shot: a corner only the leader touches
// would fire the gate for a second or two a circuit, which is how a stand pull hangs.
const zgT35ShotYd = 18.0

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
	flags, err := zgT35GroupFlags()
	if err != nil {
		t.Fatal(err)
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
	// The row that pulls F6's pack: the one whose pack holds its leader.
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
	// The pack is the formation, and every unit of it follows the leader (OPTION_FORMATION_MOVE, group_flags 3): the
	// stand is aimed at the patrol, not at the DB spawn slots.
	if len(packs[gi]) != 6 {
		t.Errorf("pull group %d pulls %d units, want F6's six", gi, len(packs[gi]))
	}
	for _, c := range packs[gi] {
		lead := c.GroupLeader
		if lead == 0 {
			lead = c.GUID
		}
		if lead != zgT35F6 {
			t.Errorf("pull group %d holds %s %d, whose formation is %d, not F6 %d: the row must scope one pack", gi, c.Name, c.GUID, lead, zgT35F6)
		}
		if flags[c.GUID]&1 == 0 {
			t.Errorf("pull group %d: %s %d (group_flags %d) is no formation follower (OPTION_FORMATION_MOVE, 0x1), so it does not walk F6's loop: the stand cannot shoot it from the loop", gi, c.Name, c.GUID, flags[c.GUID])
		}
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
	// The gate's shot at the loop: the party holds and the tank fires when the patrol comes round. The corner must be
	// inside the shot with a real window, or the gate waits 600 s again.
	near := math.Inf(1)
	for _, q := range loop {
		if d := dist(stand, q); d < near {
			near = d
		}
	}
	if near > zgW37TankShotYd {
		t.Errorf("pull group %d stand (%.1f %.1f %.1f) is %.1f yd from the nearest waypoint of %d's loop, past the %g yd shot: the gate never fires (walk 107 waited 600 s, walk 108's tank walked to the loop point and failed)",
			gi, stand.X, stand.Y, stand.Z, near, zgT35F6, zgW37TankShotYd)
	}
	if near < zgT35OwnYd {
		t.Errorf("pull group %d stand (%.1f %.1f %.1f) is %.1f yd from %d's patrol, inside its own pack's %g yd detection plus the party's spread: the patrol walks onto the party at the stand",
			gi, stand.X, stand.Y, stand.Z, near, zgT35F6, zgT35OwnYd)
	}
	if shot := zgT35LoopInShot(stand, loop, zgW37TankShotYd); shot < zgT35ShotYd {
		t.Errorf("pull group %d stand (%.1f %.1f %.1f) has %.1f yd of %d's loop inside the %g yd shot, want >= %g: the gate only blinks at a corner instead of firing every circuit",
			gi, stand.X, stand.Y, stand.Z, shot, zgT35F6, zgW37TankShotYd, zgT35ShotYd)
	}
	// Line of sight from the stand to the loop, and the floor: a stand on the plate's south slope is fine, a stand in
	// air or behind the ridge is not.
	if pl != nil {
		clear := false
		for _, w := range loop {
			r, err := pl.Raycast(309, navmesh.Vec3{stand.X, stand.Y, stand.Z + 1.5}, navmesh.Vec3{w.X, w.Y, w.Z + 1})
			if err == nil && r.Known && r.Clear {
				clear = true
				break
			}
		}
		if !clear {
			t.Errorf("pull group %d: no line of sight from the stand (%.1f %.1f %.1f) to any waypoint of %d's loop", gi, stand.X, stand.Y, stand.Z, zgT35F6)
		}
		if fl, err := pl.Floors(309, stand.X, stand.Y, stand.Z-3, stand.Z+3); err != nil || len(fl) == 0 {
			t.Errorf("pull group %d: no navmesh floor under the stand (%.1f %.1f %.1f): %v", gi, stand.X, stand.Y, stand.Z, err)
		} else if d := math.Abs(float64(fl[0].Z - stand.Z)); d > 0.5 {
			t.Errorf("pull group %d: the stand (%.1f %.1f %.1f) stands %.2f yd off the floor (%.2f): the run leader snaps", gi, stand.X, stand.Y, stand.Z, d, fl[0].Z)
		}
	}
	// The next pull group's formation: every one of its spawns is outside the wake reach (aggro at the plan's party
	// level, the raid's spread and the spawn's wander) from the stand. This is the round-1 review's finding: the stand
	// sat 24.8 and 27.3 yd from 49151/49153 (reach 30) and its bats joined the pull by the assist ring.
	byGUID := map[uint64]creature{}
	for _, c := range cs {
		byGUID[c.GUID] = c
	}
	next := []uint64{zgT35Next}
	for _, c := range cs {
		if c.GroupLeader == zgT35Next {
			next = append(next, c.GUID)
		}
	}
	for _, guid := range next {
		c, ok := byGUID[guid]
		if !ok {
			t.Fatalf("creatures.tsv: no spawn %d", guid)
		}
		if d, reach := dist(stand, c.Pos), routeReach(p, c); d < reach+1 {
			t.Errorf("pull group %d stand (%.1f %.1f %.1f) is %.1f yd from %s %d of the next pull group's formation %d, inside its %.1f yd wake reach: the stand wakes the next pack",
				gi, stand.X, stand.Y, stand.Z, d, c.Name, c.GUID, zgT35Next, reach)
		}
	}
	// The clearance: the stand and every 2 yd of the walk-in keep every later pack's wake reach off its spawns (the
	// review's rule) and 23 yd off its patrol loop (the walk-38 rule: a patrol that walks the party while it waits is
	// the multi-formation wipe the stand pulls exist to stop). F6's own pack is the one the row walks out to pull.
	pts, err := routePoints(p)
	if err != nil {
		t.Fatal(err)
	}
	byLeader := map[uint64][]creature{}
	for _, c := range cs {
		if c.GroupLeader != 0 {
			byLeader[c.GroupLeader] = append(byLeader[c.GroupLeader], c)
		}
	}
	var owners []routePoint
	for _, q := range pts {
		if q.ownerIdx >= 0 {
			owners = append(owners, q)
		}
	}
	rowOf := func(c creature) (routePoint, bool) { return routeOwnerOf(owners, byLeader, c) }
	me := byGUID[zgT35F6]
	mine, ok := rowOf(me)
	if !ok {
		t.Fatalf("no route row pulls F6 %d: the plan rows and the plan's pull groups disagree", zgT35F6)
	}
	check := func(what string, q dungeon.Point) {
		for _, c := range cs {
			if !hostileMob(c, nil, nil) {
				continue
			}
			o, pulled := rowOf(c)
			if pulled && o.idx <= mine.idx {
				continue // pulled and dead by now, this row's own pack included
			}
			if d, reach := dist(q, c.Pos), routeReach(p, c); d < reach+1 {
				t.Errorf("pull group %d %s (%.1f %.1f %.1f) is %.1f yd from %s %d (entry %d), inside its %.1f yd wake reach: %s",
					gi, what, q.X, q.Y, q.Z, d, c.Name, c.GUID, c.Entry, reach, pulledBy(pulled, o))
			}
		}
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

// pulledBy names the row a spawn belongs to, for a failure message.
func pulledBy(pulled bool, o routePoint) string {
	if !pulled {
		return "no row pulls it"
	}
	return fmt.Sprintf("pull group %d", o.idx)
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
