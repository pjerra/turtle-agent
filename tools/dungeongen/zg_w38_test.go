// Walk-38 fixes (TortoiseBots-audit tests/raid-zg-walk38): the Jin'do hall's honour guard and the three
// packs beside it, the Hakkar outer leg's step-601 wall, and the Jeklik step-43 patrol pull. The hall test
// below is the brief's rule: a pull row before the boss row must not bring Jin'do, and it must not bring
// more bodies than its own ceiling, by the route-aggro model (proximity with each formation's social
// link) on the live-flag export.
package main

import (
	"fmt"
	"math"
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
			for _, q := range zgSamplesAlong(prev, at, zgSampleStep) {
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
		}, aggroest.Params{PartyLevel: 58, Rate: 1, RingYd: aggroest.RingYd, RingHops: 0, FloorDz: aggroest.FloorDz})
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
		return walk[len(walk)-1:]
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

