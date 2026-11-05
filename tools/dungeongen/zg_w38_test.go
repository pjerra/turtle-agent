// Walk-38 fixes (TortoiseBots-audit tests/raid-zg-walk38) and the review rounds that followed them: the
// Jin'do hall (the review's H1/H2 and the round-2 A-1/A-6: the run leader's real family-assist model, RingYd
// 10 and one hop; the hall's east side is its own four pull groups again, each over its original ceiling by a
// pinned known limit, and walk 39 GM-clears them), the Hakkar outer leg's step-601 wall (M4, on the real
// export in cmd/turtle-agent), the Jeklik step-43 patrol pull and its camp stands and walks against every
// loop (M3/A-5), and the corridor's stand coverage (L2). The hall test below is the rule: a pull row before
// the boss row must not bring more bodies than its ceiling unless the plan note pins the known limit, it must
// not bring Jin'do unless the note does, and the hall's lone mistresses and stands keep their own groups.
package main

import (
	"fmt"
	"math"
	"strconv"
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

// zgW38HallLimit is one documented known limit of the hall's own rows (the plan note's WALK 38 REVIEW R2):
// Over is how many bodies the pull brings over its original max_bodies under the run leader's real family-assist
// model, and Boss says the pull wakes Jin'do too. The key is the plan's pull group index, which the export's
// "pull group N" comment names.
type zgW38HallLimit struct {
	Over int
	Boss bool
}

// zgW38HallLimits pins each hall pull's overage: no stand stages them under their ceilings (a stand in the
// tank's 28.5 yd shot of the guard or the north camp is inside the lone mistresses' 22+10 yd reach, and a stand
// in a lone mistress's shot is inside the 10 yd family ring of the other and of the guard), so the plan keeps
// the original ceilings and walk 39 GM-clears these groups (leg 6 pull groups 9-12 and 16).
var zgW38HallLimits = map[int]zgW38HallLimit{
	8:  {Over: 1, Boss: true},  // the guard's eight + the north camp's east four + Jin'do: 15 over 14
	9:  {Over: 1, Boss: true},  // the north camp's eight + the two lone mistresses + Jin'do: 11 over 10
	10: {Over: 2, Boss: true},  // the lone 49403 + 49402 + Jin'do: 3 over 1
	11: {Over: 1, Boss: true},  // the lone 49402 + Jin'do: 2 over 1
	15: {Over: 1, Boss: false}, // the corridor's 49714 row + the 51881/51946 pair: 10 over 9 (A-3's 10 is reverted)
}

// zgW38Group is the plan pull group an exported row's comment names ("pull group 8; stand pull" -> 8), or -1.
func zgW38Group(comment string) int {
	f := strings.Fields(comment)
	if len(f) < 3 || f[0] != "pull" || f[1] != "group" {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSuffix(f[2], ";"))
	if err != nil {
		return -1
	}
	return n
}

// TestZgW38JindosHallPullsStayUnderTheirCeilings is the walk-38 review's H1/A-6 on the live-flag export. The
// bodies are aggroest's estimate with the run leader's real family-assist model (aggroest.AssistYd: RingYd 10 =
// the core's CreatureFamilyAssistanceRadius, RingHops 1): over the spawns still alive when the row is pulled
// (earlier rows' packs are dead), proximity along the rows the exporter walks plus every formation a woken unit
// closes, and the one assist hop. A pre-boss pull row must stay at or under its ceiling (max_bodies, else 7),
// and bring no Jin'do, unless the plan note pins its known limit (zgW38HallLimits: the hall's four own groups
// and the corridor's 49714 row, whose stands cannot stage them under their ceilings); those rows are asserted at
// exactly their pinned overage so a change of geometry fails here. A-6: the two lone mistresses 49402/49403 are
// members of no pull but their own (no row owns them, and no other row's radius reaches their formation) and no
// stand a pre-boss pull uses is inside Jin'do's own route-aggro reach (35 yd: 25 aggro at the plan's party level
// 58 plus the raid's 10 yd spread). The hall's four groups are also asserted to own exactly their own units, so
// `raid-walk --gm-clear 6:9-12` clears each whole pack.
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
	groupRow := map[int]int{}
	for i := range rows.rows {
		parties[i] = zgW38Party(t, pl, rows, i, cs)
		if stop < 0 && strings.HasPrefix(rows.rows[i].Comment, "boss approach") {
			stop = i
		}
		if g := zgW38Group(rows.rows[i].Comment); g >= 0 && rows.rows[i].Kind == planexport.KindPull {
			if _, ok := groupRow[g]; !ok {
				groupRow[g] = i
			}
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
	// A-6 (direct): no stand of a pre-boss pull is inside Jin'do's own reach. A stand-pull row's stand is the
	// move row right before it; every other row's point is where the party goes.
	for i := 0; i < stop; i++ {
		s := rows.rows[i]
		if s.Kind != planexport.KindPull && s.Kind != planexport.KindBoss {
			continue
		}
		stand := rows.party(i)
		if d := dist(stand, boss.Pos); d < bossReach {
			t.Errorf("leg 6 pull row %d (%s, pull group %d) has its stand %.1f %.1f %.1f %.1f yd from Jin'do 11380, want >= %g: no pre-boss stand reaches him",
				i, s.Comment, zgW38Group(s.Comment), stand.X, stand.Y, stand.Z, d, bossReach)
		}
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
		group := zgW38Group(s.Comment)
		limit, known := zgW38HallLimits[group]
		if known {
			if res.Bodies != ceiling+limit.Over {
				var why []string
				for _, m := range res.Members {
					if c := zgW38Creature(cs, uint64(m.Guid)); c != nil {
						why = append(why, fmt.Sprintf("%s/%d[%s]", c.Name, c.GUID, m.Why))
					}
				}
				t.Errorf("leg 6 row %d (%s, pull group %d) brings %d bodies; the plan note's WALK 38 REVIEW R2 pins it at its ceiling %d plus %d = %d: %v",
					i, s.Comment, group, res.Bodies, ceiling, limit.Over, ceiling+limit.Over, why)
			}
		} else if res.Bodies > ceiling {
			var why []string
			for _, m := range res.Members {
				if c := zgW38Creature(cs, uint64(m.Guid)); c != nil {
					why = append(why, fmt.Sprintf("%s/%d[%s]", c.Name, c.GUID, m.Why))
				}
			}
			t.Errorf("leg 6 row %d (%s) brings %d bodies over its ceiling %d: %v", i, s.Comment, res.Bodies, ceiling, why)
		}
		brought := false
		for _, m := range res.Members {
			if m.Guid == uint32(boss.GUID) {
				brought = true
			}
		}
		switch {
		case !known && brought:
			t.Errorf("leg 6 row %d (%s) brings Jin'do 11380 before its boss row: the pull's bodies are %d over the ceiling %d", i, s.Comment, res.Bodies, ceiling)
		case known && limit.Boss && !brought:
			t.Errorf("leg 6 row %d (%s, pull group %d): the plan note's known limit says the pull wakes Jin'do, but it does not (bodies %d over ceiling %d): re-measure it", i, s.Comment, group, res.Bodies, ceiling)
		case known && !limit.Boss && brought:
			t.Errorf("leg 6 row %d (%s, pull group %d) brings Jin'do 11380: its pinned limit does not", i, s.Comment, group)
		}
		prevPull = i
	}
	// H1/A-6: the hall's east side is its own pull groups again (the boss-row fold is undone). Each group owns
	// exactly its units, so a GM clear of one group kills one whole pack; 49402/49403 belong to their own row
	// only and the boss row carries no adds.
	wantOwned := map[int][]uint64{
		8:  {49421, 49573, 49574, 49575, 49576, 49577, 49609, 49650},         // the honour guard's eight
		9:  {49383, 49384, 49385, 49386, 1173078, 1173079, 1173080, 1173081}, // the north camp
		10: {49403},                                                          // the lone mistress
		11: {49402},                                                          // the lone mistress
	}
	for group, guids := range wantOwned {
		row, ok := groupRow[group]
		if !ok {
			t.Errorf("no exported pull row for leg 6 pull group %d", group)
			continue
		}
		for _, guid := range guids {
			if o, ok := rows.owner[guid]; !ok || o != row {
				t.Errorf("leg 6 pull group %d: %d is pulled by row %d (owned %v), want its own row %d (%s): the hall's east side is not the boss row's adds",
					group, guid, o, ok, row, rows.rows[row].Comment)
			}
		}
	}
	for _, guid := range []uint64{49402, 49403} {
		o, ok := rows.owner[guid]
		if !ok {
			t.Errorf("the lone mistress %d is pulled by no row: its group is empty", guid)
			continue
		}
		if o == bossRow {
			t.Errorf("the lone mistress %d is owned by the Jin'do boss row: it must be its own pull group", guid)
		}
		if g := zgW38Group(rows.rows[o].Comment); g != 10 && g != 11 {
			t.Errorf("the lone mistress %d is owned by row %d (%s): it is a member of a pull other than its own group", guid, o, rows.rows[o].Comment)
		}
	}
	br := rows.rows[bossRow]
	if len(br.Entries) != 0 || br.Radius != 0 {
		t.Errorf("the Jin'do boss row carries adds (entries %v, radius %g): the hall's east side is its own pull groups", br.Entries, br.Radius)
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
	// Every row stands on the floor. The walk-38 f2 row (-11752.0 -1741.9 14.3) was 4.1 yd under the
	// mesh floor, but the planner re-anchors an off-mesh end point, so the path check above still saw a
	// complete path to it: the row check is the floor under the point itself. Fails on dev, whose cutter
	// writes that row (M4).
	for _, pt := range pts {
		fl, err := pl.Floors(309, pt.at.X, pt.at.Y, pt.at.Z-8, pt.at.Z+8)
		if err != nil {
			t.Errorf("%s (%.1f %.1f %.1f): floors: %v", pt.what, pt.at.X, pt.at.Y, pt.at.Z, err)
			continue
		}
		above := 0.0
		for _, f := range fl {
			if d := float64(f.Z) - float64(pt.at.Z); d > above {
				above = d
			}
		}
		// The mesh's detail error is under 1 yd and a pack's DB spawn z can sit ~2 yd off the floor (the group-22
		// pull's centroid at -11822.3 -1510.4 15.7 is 1.9 yd under its floor); 3 yd under is off the mesh.
		if above > 3 {
			t.Errorf("%s (%.1f %.1f %.1f) stands %.1f yd under the mesh floor: the row is off the mesh", pt.what, pt.at.X, pt.at.Y, pt.at.Z, above)
		}
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
