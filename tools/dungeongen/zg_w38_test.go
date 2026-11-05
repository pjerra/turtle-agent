// Walk-38 fixes (TortoiseBots-audit tests/raid-zg-walk38) and the review round that followed them: the
// Jin'do hall (the review's H1/H2: the run leader's real family-assist model, RingYd 10 and one hop, so the
// hall's east side is pulled with Jin'do by the boss row), the Hakkar outer leg's step-601 wall (M4, on the
// real export in cmd/turtle-agent), the Jeklik step-43 patrol pull and its camp stands against every loop
// (M3), and the corridor's stand coverage (L2). The hall test below is the rule: a pull row before the boss
// row must not bring Jin'do, and it must not bring more bodies than its own ceiling, by that assist model
// on the live-flag export.
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
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/planlint"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

// zgW38PullCeiling is the ceiling a pull row without max_bodies gets in the live run: the run leader's
// joiner ceiling 6 plus the target (PullCeilingBodies), as walk 38's "ceiling 7" messages read.
const zgW38PullCeiling = 7

// zgW38Party is where the party stands for a row: a stand pull's stand when the stand can shoot its whole
// pack with line of sight (PatrolStandPullGate's 28.5 yd shot), else the row's own point -- the plain pull
// the run leader falls back to.
func zgW38Party(t *testing.T, pl *navmesh.Planner, rows *zgW37Rows, i int, cs []creature) dungeon.Point {
	t.Helper()
	s := rows.rows[i]
	if s.Kind != planexport.KindPull || s.Flags&planexport.FlagStandPull == 0 {
		return dungeon.Point{X: s.X, Y: s.Y, Z: s.Z}
	}
	stand := rows.party(i)
	if ok, _ := rows.standInShot(i, cs); !ok {
		return dungeon.Point{X: s.X, Y: s.Y, Z: s.Z}
	}
	for _, c := range cs {
		if !zgHostile(c) || rows.owner[c.GUID] != i {
			continue
		}
		r, err := pl.Raycast(309, navmesh.Vec3{stand.X, stand.Y, stand.Z + 1.5}, navmesh.Vec3{c.Pos.X, c.Pos.Y, c.Pos.Z + 1})
		if err != nil || !r.Known || !r.Clear {
			return dungeon.Point{X: s.X, Y: s.Y, Z: s.Z}
		}
	}
	return stand
}

// TestZgW38JindosHallPullsStayUnderTheirCeilings: from the leg start to the boss row, no pull row brings
// Jin'do and no pull row brings more bodies than its ceiling (its max_bodies, else 7), and no party point
// or walked step is inside Jin'do's own route-aggro reach (35 yd: 25 aggro at the plan's party level 58
// plus the raid's 10 yd spread). The bodies are aggroest's estimate over the spawns still alive when the
// row is pulled (earlier rows' packs are dead): proximity along the rows the exporter walks plus every
// formation (AGGRO_TOGETHER/link) a woken unit closes. The call-for-help ring is not used: the core's
// creature_template call_for_help_range is 5 yd (the run leader's 20 yd kAggroRingYd is the high-side
// estimate that names the whole hall one pull, in which no plan can keep the boss out), and the walk-38
// fights show the formations, not the ring, are what came.
func TestZgW38JindosHallPullsStayUnderTheirCeilings(t *testing.T) {
	p, cs := zgLoadPlanAndExport(t)
	pl := zgPlannerOrNil(t)
	if pl == nil {
		t.Skip("needs the map-309 mmaps (the live-flag export and the stand shot)")
	}
	defer pl.Close()
	w, _, err := worldspawn.Load("testdata/map309", 309)
	if err != nil {
		t.Fatal(err)
	}
	packs := planlint.PackIDs(w)
	rows := zgW37BuildRows(t, p, cs, 6)
	var boss creature
	for _, c := range cs {
		if c.Entry == 11380 {
			boss = c
		}
	}
	if boss.GUID == 0 {
		t.Fatal("no Jin'do spawn in the map-309 export")
	}
	bossReach := routeReach(p, boss)
	bossRow := -1
	stop := -1 // the first boss-approach row: the walk to the boss is meant to enter his reach
	parties := map[int]dungeon.Point{}
	for i := range rows.rows {
		parties[i] = zgW38Party(t, pl, rows, i, cs)
		if stop < 0 && strings.HasPrefix(rows.rows[i].Comment, "boss approach") {
			stop = i
		}
		if rows.rows[i].Kind == planexport.KindBoss {
			bossRow = i
			break
		}
	}
	if bossRow < 0 {
		t.Fatal("no boss row in the exported leg 6")
	}
	if stop < 0 {
		stop = bossRow
	}
	// The boss's own reach: every party point and every walked step before the boss row.
	prev := dungeon.Point{}
	for i := 0; i < stop; i++ {
		s := rows.rows[i]
		if s.Kind != planexport.KindMove && s.Kind != planexport.KindMoveDirect && s.Kind != planexport.KindMoveHopExact &&
			s.Kind != planexport.KindPull && s.Kind != planexport.KindBoss {
			continue
		}
		at := parties[i]
		if d := dist(at, boss.Pos); d < bossReach {
			t.Errorf("leg 6 row %d (%s) stands %.1f yd from Jin'do 11380 at %.1f %.1f, want >= %g", i, s.Comment, d, at.X, at.Y, bossReach)
		}
		if prev != (dungeon.Point{}) {
			walk := zgSamplesAlong(prev, at, zgSampleStep)
			if path, err := pl.Plan(309, navmesh.Vec3{prev.X, prev.Y, prev.Z}, navmesh.Vec3{at.X, at.Y, at.Z}); err == nil && path.Status == navmesh.Complete && len(path.Points) > 1 {
				walk = walk[:0]
				for k := 0; k+1 < len(path.Points); k++ {
					a, b := path.Points[k], path.Points[k+1]
					walk = append(walk, zgSamplesAlong(dungeon.Point{X: a[0], Y: a[1], Z: a[2]}, dungeon.Point{X: b[0], Y: b[1], Z: b[2]}, zgSampleStep)...)
				}
			}
			for _, q := range walk {
				if d := dist(q, boss.Pos); d < bossReach {
					t.Errorf("leg 6 rows %d->%d (%s -> %s) walk %.1f yd from Jin'do 11380 at %.1f %.1f, want >= %g", i-1, i, rows.rows[i-1].Comment, s.Comment, d, q.X, q.Y, bossReach)
					break
				}
			}
		}
		prev = at
	}
	// The bodies: order-aware, proximity (walk + stand) and formations.
	prevPull := -1
	for i := 0; i < stop; i++ {
		s := rows.rows[i]
		if s.Kind != planexport.KindPull {
			continue
		}
		alive := map[uint64]bool{}
		for _, c := range cs {
			if o, ok := rows.owner[c.GUID]; ok && o < i {
				continue // pulled and dead by now
			}
			alive[c.GUID] = true
		}
		keep := func(sp worldspawn.Spawn) bool {
			return alive[sp.GUID] && zgHostile(creature{GUID: sp.GUID, LevelMax: sp.LevelMax, Faction: int(sp.Faction)})
		}
		mobs, entries := planlint.PullMobs(w.Spawns, nil, packs, keep)
		party := parties[i]
		target := -1
		bestRank, bestDist := 1<<30, math.Inf(1)
		rank := map[uint32]int{}
		for k, e := range p.KillFirst {
			if _, ok := rank[e]; !ok {
				rank[e] = k
			}
		}
		for mi, m := range mobs {
			c := zgW38Creature(cs, uint64(m.Guid))
			if c == nil || rows.owner[c.GUID] != i {
				continue
			}
			r, ok := rank[entries[m.Guid]]
			if !ok {
				r = 1 << 20
			}
			d := dist(party, m.Pos)
			if r < bestRank || (r == bestRank && d < bestDist) {
				target, bestRank, bestDist = mi, r, d
			}
		}
		if target < 0 {
			t.Errorf("leg 6 row %d (%s): no target among its owned spawns", i, s.Comment)
			continue
		}
		var walk []dungeon.Point
		start := 0
		if prevPull >= 0 {
			start = prevPull + 1
		}
		for k := start; k < i; k++ {
			walk = append(walk, parties[k])
		}
		walk = append(walk, party)
		// The run leader reads the walk it is on, not the leg behind it: keep the last 60 yd (its sight).
		walk = zgW38Tail(walk, 60)
		res := aggroest.Estimate(mobs, target, aggroest.Geometry{
			Walk: walk, WalkSpreadYd: aggroest.WalkSpreadYd,
			Stand: party, StandSpreadYd: aggroest.StandSpreadRaidYd,
		}, aggroest.Params{PartyLevel: 58, Rate: 1, RingYd: aggroest.AssistYd, RingHops: 1, FloorDz: aggroest.FloorDz})
		ceiling := int(s.Item)
		if ceiling == 0 {
			ceiling = zgW38PullCeiling
		}
		if res.Bodies > ceiling {
			var why []string
			for _, m := range res.Members {
				if c := zgW38Creature(cs, uint64(m.Guid)); c != nil {
					why = append(why, fmt.Sprintf("%s/%d[%s]", c.Name, c.GUID, m.Why))
				}
			}
			t.Errorf("leg 6 row %d (%s) brings %d bodies over its ceiling %d: %v", i, s.Comment, res.Bodies, ceiling, why)
		}
		for _, m := range res.Members {
			if m.Guid == uint32(boss.GUID) {
				t.Errorf("leg 6 row %d (%s) brings Jin'do 11380 before its boss row: the pull's bodies are %d over the ceiling %d", i, s.Comment, res.Bodies, ceiling)
			}
		}
		prevPull = i
	}
	// H1/H2 of the walk-38 review: the hall's east side is pulled WITH Jin'do, never before him. The
	// lone mistresses 49402/49403 stand 7.2/8.6 yd from him and the guard's eight 11-17 yd; the guard's
	// nearest unit is 5.9 yd from 49402, so the 10 yd family assist of any guard pull wakes the
	// mistresses, and a party point inside their own reach (22 aggro + the raid's 10 yd spread) has
	// them call him on the same hop. No stand keeps the party 32 yd from them and in the tank's 28.5 yd
	// shot of the guard (the guard spans 9.5 yd: a stand in shot of its farthest is at most 28.5+9.5 yd
	// from the nearest), and the north camp can only be shot from inside the Sacrificed Trolls'
	// respawning pit (18.4 yd at best). So the boss row owns the whole east side and pulls it as its
	// adds: entries 14825/14882/14883 within its radius.
	wantOwned := map[uint64]string{
		49402: "the lone mistress", 49403: "the lone mistress",
		1173078: "the north camp's east four", 1173079: "the north camp's east four",
		1173080: "the north camp's east four", 1173081: "the north camp's east four",
		49421: "the honour guard", 49573: "the honour guard", 49574: "the honour guard",
		49575: "the honour guard", 49576: "the honour guard", 49577: "the honour guard",
		49609: "the honour guard", 49650: "the honour guard",
		49383: "the north camp", 49384: "the north camp", 49385: "the north camp", 49386: "the north camp",
	}
	for guid, what := range wantOwned {
		o, ok := rows.owner[guid]
		if !ok || o != bossRow {
			t.Errorf("%s %d is pulled by row %d (owned %v), want the Jin'do boss row %d: it is pulled with him, never before him", what, guid, o, ok, bossRow)
		}
	}
	br := rows.rows[bossRow]
	if !slices.Contains(br.Entries, 14825) || !slices.Contains(br.Entries, 14883) {
		t.Errorf("the boss row's entries are %v, want 14825 (the hall's mistresses and guard) and 14883 (their Voodoo Slaves) so the adds are scoped to his row", br.Entries)
	}
	if br.Radius < 47 {
		t.Errorf("the boss row's radius is %g, want >= 47 (the north camp's 49385 stands 45.8 yd from him)", br.Radius)
	}
}

// zgW38Tail keeps the last yd yards of a polyline (the run leader reads the walk it is on).
func zgW38Tail(walk []dungeon.Point, yd float64) []dungeon.Point {
	if len(walk) < 2 {
		return walk
	}
	rem := 0.0
	k := len(walk) - 1
	for k > 0 {
		d := dist(walk[k-1], walk[k])
		if rem+d > yd {
			break
		}
		rem += d
		k--
	}
	if k == len(walk)-1 {
		// The last segment is longer than yd: take its final yd yards (do not drop the walk).
		prev, last := walk[len(walk)-2], walk[len(walk)-1]
		if rem >= yd {
			return walk[len(walk)-1:]
		}
		f := (yd - rem) / dist(prev, last)
		start := dungeon.Point{
			X: last.X - (last.X-prev.X)*float32(f),
			Y: last.Y - (last.Y-prev.Y)*float32(f),
			Z: last.Z - (last.Z-prev.Z)*float32(f),
		}
		return []dungeon.Point{start, last}
	}
	return walk[k:]
}

// zgW38Creature finds a spawn by guid.
func zgW38Creature(cs []creature, guid uint64) *creature {
	for i := range cs {
		if cs[i].GUID == guid {
			return &cs[i]
		}
	}
	return nil
}

// TestZgW38HakkarOuterRowsWalkable: every consecutive pair of the hakkar_outer leg's live-flag rows has a complete
// navmesh path, and every row stands on the mesh. Walk 38 f2 paused at step 601 "no walkable path toward
// -11752.0 -1741.9 14.3": the approach-hop cutter had put a point 4.1 yd under the floor on the straight line
// between the group-5 and group-6 pulls (the floor-aware cut's kept point was the only one, and the last-approach
// interpolation crossed the gap), so the run leader could not path to it. The floor-aware cut leaves the 114 yd hop
// to the run leader, whose own path around the lake is complete.
func TestZgW38HakkarOuterRowsWalkable(t *testing.T) {
	p, cs := zgLoadPlanAndExport(t)
	pl := zgPlannerOrNil(t)
	if pl == nil {
		t.Skip("needs the map-309 mmaps")
	}
	defer pl.Close()
	rows := zgW37BuildRows(t, p, cs, 7)
	var pts []struct {
		at   dungeon.Point
		what string
	}
	for _, s := range rows.rows {
		switch s.Kind {
		case planexport.KindMove, planexport.KindMoveDirect, planexport.KindMoveHopExact, planexport.KindPull, planexport.KindBoss:
			pts = append(pts, struct {
				at   dungeon.Point
				what string
			}{dungeon.Point{X: s.X, Y: s.Y, Z: s.Z}, fmt.Sprintf("%s %q", s.Kind, s.Comment)})
		}
	}
	if len(pts) < 10 {
		t.Fatalf("%d rows in the hakkar_outer leg", len(pts))
	}
	for i := 0; i+1 < len(pts); i++ {
		a, b := pts[i].at, pts[i+1].at
		path, err := pl.Plan(309, navmesh.Vec3{a.X, a.Y, a.Z}, navmesh.Vec3{b.X, b.Y, b.Z})
		if err != nil {
			t.Errorf("%s -> %s: %v", pts[i].what, pts[i+1].what, err)
			continue
		}
		if path.Status != navmesh.Complete {
			t.Errorf("%s -> %s (%.1f %.1f %.1f -> %.1f %.1f %.1f): no complete navmesh path (%s %s)", pts[i].what, pts[i+1].what,
				a.X, a.Y, a.Z, b.X, b.Y, b.Z, path.Status, path.Reason)
		}
	}
}

// TestZgW38JeklikPatrolStands is the MANDOKIR-440 method on Jeklik's two patrol camps: each patroller's own row
// carries its movement.tsv loop (the run leader waits at the stand for a waypoint to come into its 28.5 yd shot),
// the stand is inside the shot of the whole pack, and it is within the run leader's 60 yd sight of the row's centre.
// Walk 38 d/h2/a/h3 stalled at leg-0 step 43: the combined row at -12218 -1440 held the 49131 and 49135 camps
// (54 yd apart) with no patrol block, so the gate waited on the stand for units that walk a patrol it could not
// read. Dev has neither loop, so this fails on dev.
func TestZgW38JeklikPatrolStands(t *testing.T) {
	p, cs := zgLoadPlanAndExport(t)
	// The structural checks need no navmesh (so dev fails without it); the shot's line of sight does, and is
	// skipped alone when the machine has no map-309 mmaps.
	pl := zgPlannerOrNil(t)
	if pl != nil {
		defer pl.Close()
	}
	leg, _ := zgW37LegByName(t, p, "high_priestess_jeklik")
	movement, err := zgReadMovementPaths("testdata/map309/movement.tsv")
	if err != nil {
		t.Fatal(err)
	}
	packs := zgPacks(t, p.Legs[0], cs)
	for _, want := range []struct {
		guid uint64
		size int
	}{
		{49135, 3},  // the south headhunter camp
		{49131, 10}, // the north witch-doctor camp and the bat camp 8 yd away (one row)
	} {
		loop, ok := movement[want.guid]
		if !ok || len(loop) < 3 {
			t.Fatalf("movement.tsv: %d has no loop", want.guid)
		}
		gi := -1
		for i := range leg.PullGroups {
			if pt := leg.PullGroups[i].Patrol; pt != nil && strings.Contains(pt.Source, fmt.Sprintf("creature_movement %d", want.guid)) {
				gi = i
			}
		}
		if gi < 0 {
			t.Errorf("no pull group carries the %d patroller's loop: the stand pull waits on units whose patrol the run leader cannot read", want.guid)
			continue
		}
		g := leg.PullGroups[gi]
		if !g.StandPull {
			t.Errorf("pull group %d (patroller %d) is not a stand pull", gi, want.guid)
		}
		if len(packs[gi]) != want.size {
			t.Errorf("pull group %d carries %d bodies, want %d (its own camp; the north camp also owns the bat camp)", gi, len(packs[gi]), want.size)
		}
		if len(g.Patrol.Path) != len(loop) {
			t.Errorf("pull group %d patrol path has %d points, want %d", gi, len(g.Patrol.Path), len(loop))
		} else {
			for k := range loop {
				if d := dist(g.Patrol.Path[k], loop[k]); d > 0.5 {
					t.Errorf("pull group %d patrol path[%d] is %.1f yd from %d's waypoint", gi, k, d, want.guid)
					break
				}
			}
		}
		if g.Patrol.PeriodS <= 0 {
			t.Errorf("pull group %d patrol period is %.1f s", gi, g.Patrol.PeriodS)
		}
		if len(g.Approach) == 0 {
			t.Errorf("pull group %d has no stand", gi)
			continue
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
			t.Errorf("pull group %d stand is %.1f yd from its centre, want <= %d", gi, d, dungeon.RunLeaderSeeYd)
		}
		nearest := math.Inf(1)
		for _, q := range loop {
			if d := dist(stand, q); d < nearest {
				nearest = d
			}
		}
		if nearest > zgW37TankShotYd {
			t.Errorf("pull group %d stand is %.1f yd from the nearest waypoint of %d's loop, past the %g yd shot: the gate never fires", gi, nearest, want.guid, zgW37TankShotYd)
		}
		for _, c := range packs[gi] {
			if d := dist(stand, c.Pos); d > zgW37TankShotYd {
				t.Errorf("pull group %d: its unit %s %d is %.1f yd from the stand, past the %g yd shot: the row plain-pulls", gi, c.Name, c.GUID, d, zgW37TankShotYd)
			}
			if pl != nil {
				r, err := pl.Raycast(309, navmesh.Vec3{stand.X, stand.Y, stand.Z + 1.5}, navmesh.Vec3{c.Pos.X, c.Pos.Y, c.Pos.Z + 1})
				if err != nil || !r.Known || !r.Clear {
					t.Errorf("pull group %d: no line of sight from the stand to %s %d", gi, c.Name, c.GUID)
				}
			}
		}
	}
}

// TestZgW38JeklikCampsClearEveryLoop is the walk-38 review's M3: the two Jeklik camp stands (the south
// headhunter camp 49135 and the north camp that holds the witch doctor 49131 and the bat formation 49130)
// are tested against ALL of the leg's patrol loops, not only the one their row carries. A plan group holds
// one patrol block, so the loops come from movement.tsv: 49131's 112.6 s loop (21 points) and 49130's
// 7-point loop were never read from the south stand, which sat 21.5 yd from 49131's loop (20 yd detection
// + 3 yd spread is the minimum). The rule is zgCheckStands': a camp stand keeps 23 yd from every waypoint of
// every unpulled patrol of a LATER group. It fails on dev (the south stand -12219 -1491 stands 21.5 yd from
// 49131's loop); the plan pulls the 49185 bat formation before the north camp and stages the south camp
// outside the other loops.
func TestZgW38JeklikCampsClearEveryLoop(t *testing.T) {
	p, cs := zgLoadPlanAndExport(t)
	leg := p.Legs[0]
	packs := zgPacks(t, leg, cs)
	movement, err := zgReadMovementPaths("testdata/map309/movement.tsv")
	if err != nil {
		t.Fatal(err)
	}
	loops := map[int][]struct {
		c    creature
		loop []dungeon.Point
	}{}
	for gi, pk := range packs {
		seen := map[uint64]bool{}
		for _, c := range pk {
			if !c.Patrol || seen[c.GUID] {
				continue
			}
			seen[c.GUID] = true
			if lp, ok := movement[c.GUID]; ok && len(lp) > 0 {
				loops[gi] = append(loops[gi], struct {
					c    creature
					loop []dungeon.Point
				}{c, lp})
			}
		}
	}
	var camps []int
	for gi := range leg.PullGroups {
		holds := map[uint64]bool{}
		for _, c := range packs[gi] {
			if c.GUID == 49131 || c.GUID == 49135 || c.GroupLeader == 49131 || c.GroupLeader == 49135 {
				holds[c.GUID] = true
			}
		}
		if len(holds) > 0 {
			camps = append(camps, gi)
		}
	}
	if len(camps) < 2 {
		t.Fatalf("the leg's camp rows are %v, want the south (49135) and north (49131 + 49130) camps", camps)
	}
	for _, gi := range camps {
		g := leg.PullGroups[gi]
		if len(g.Approach) == 0 {
			t.Errorf("pull group %d (a Jeklik camp) has no stand", gi)
			continue
		}
		stand := g.Approach[len(g.Approach)-1]
		for gj := gi + 1; gj < len(leg.PullGroups); gj++ {
			for _, l := range loops[gj] {
				nearest := math.Inf(1)
				for _, q := range l.loop {
					if d := dist(stand, q); d < nearest {
						nearest = d
					}
				}
				if nearest < zgLaterYd+zgPatrolSpreadYd {
					t.Errorf("pull group %d stand (%.1f %.1f %.1f) is %.1f yd from a waypoint of %s %d's loop (pull group %d), want >= %g: the patrol walks the party",
						gi, stand.X, stand.Y, stand.Z, nearest, l.c.Name, l.c.GUID, gj, zgLaterYd+zgPatrolSpreadYd)
				}
			}
		}
	}
}
