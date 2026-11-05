// Walk-39 fixes (TortoiseBots-audit tests/raid-zg-walk39): the five moving Gurubashi Bat
// Riders (14750) are patrollers. Before walk 39 the leg pulled F1 from its own camp and the
// west camps in an order that let riders walk into another pack's fight: m1-6's step 29
// brought 11 bodies (F1 + F3/F4), and step 43 brought 13 and joined 16 (11368 x10, 11831 x2,
// 14750 x4). These tests pin the MANDOKIR-440 handling the walk-37 patrol rows use: each
// rider's movement.tsv loop is the patrol block of the group that pulls its formation, the
// group is a stand pull, and its stand is in the tank's shot of that loop.
// TestZgW39JeklikMoversOwnedByTheirRows adds the ownership half (fix round 1): the run
// leader's own scoping of a stand pull from the row's centre and radius, 2D, and (fix round 2)
// the order the three west rows are pulled in and the clearances of the walks into them: F6 is
// its own row before F5, and no walk of that part of leg 0 passes within 15 yd of a spawn of a
// pack pulled after it or within 20 yd of a rider loop pulled after it.
package main

import (
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// zgW39Movers are the 14750 spawns with a creature_movement waypoint loop: F1 49147, F3 49155,
// F4 49189, F5 49185 and F6 49130. The other six have none: 49141, 49142 and 49193 stand still
// (movement type 0, wander 0) and 49154, 49167 and 49129 wander (movement type 1, wander 5).
var zgW39Movers = []uint64{49147, 49155, 49189, 49185, 49130}

// TestZgW39JeklikBatRidersArePatrollers: every moving Bat Rider's loop is carried by the
// pull group of its formation, that group is a stand pull, its stand is inside the run
// leader's 28.5 yd shot of the pack (a pack unit is what the gate waits for: the rider's own
// loop point counts too, as it is a unit of the pack walking it) and inside its 60 yd sight
// of the row's point. F6's row is the one row whose stand cannot reach its rider's loop: fix
// round 2 put it on the terrace at -12207 -1408, 32.4 yd from 49130's loop, because the lead's
// rule is >= 30 yd from 49185's loop and from the 49131 camp loop, and no point on the mesh
// meets that and comes within 28.5 yd of 49130's loop (the leg-0 scan in the walk-39 fix-2
// note); 49125, the nearest of its pack, is 23.8 yd from it, so the gate fires on the bats and
// the row scopes the whole formation.
func TestZgW39JeklikBatRidersArePatrollers(t *testing.T) {
	p, err := dungeon.Load(filepath.Join(plansDirT, "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	movement, err := readMovementPaths(filepath.Join("testdata", "map309", "movement.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	cs, err := readCreatures(filepath.Join("testdata", "map309", "creatures.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	forms := zgFormations(cs)
	leg := zgW35Leg(t, p, "high_priestess_jeklik")
	for _, guid := range zgW39Movers {
		loop, ok := movement[guid]
		if !ok || len(loop) < 2 {
			t.Fatalf("movement.tsv: %d has no loop", guid)
		}
		gi := -1
		for i := range leg.PullGroups {
			if pt := leg.PullGroups[i].Patrol; pt != nil && strings.Contains(pt.Source, fmt.Sprintf("creature_movement %d", guid)) {
				gi = i
			}
		}
		if gi < 0 {
			t.Errorf("no leg-0 pull group carries the %d Bat Rider's loop: the rider walks into another pack with no patrol mark", guid)
			continue
		}
		g := leg.PullGroups[gi]
		if !g.StandPull {
			t.Errorf("pull group %d (Bat Rider %d) is not a stand pull", gi, guid)
		}
		if len(g.Patrol.Path) != len(loop) {
			t.Errorf("pull group %d patrol path has %d points, want %d", gi, len(g.Patrol.Path), len(loop))
			continue
		}
		for k := range loop {
			if d := dist(g.Patrol.Path[k], loop[k]); d > 0.5 {
				t.Errorf("pull group %d patrol path[%d] is %.1f yd from %d's waypoint", gi, k, d, guid)
				break
			}
		}
		if len(g.Approach) == 0 {
			t.Errorf("pull group %d has no stand", gi)
			continue
		}
		stand := g.Approach[len(g.Approach)-1]
		if d := closestOnLoop(stand, loop); d > zgW37TankShotYd && guid != 49130 {
			t.Errorf("pull group %d stand is %.1f yd from the nearest waypoint of %d's loop, past the %g yd shot: the gate never fires", gi, d, guid, zgW37TankShotYd)
		}
		if guid == 49130 {
			// The one row whose stand cannot reach its rider's loop: its static units are in shot instead.
			if d := zgW39NearestPackYd(g, stand, cs, forms); d > zgW37TankShotYd {
				t.Errorf("pull group %d stand is %.1f yd from the nearest unit of its pack, past the %g yd shot: the gate never fires", gi, d, zgW37TankShotYd)
			}
		}
		var cen dungeon.Point
		for _, q := range g.Positions {
			cen.X += q.X
			cen.Y += q.Y
			cen.Z += q.Z
		}
		n := float32(len(g.Positions))
		cen.X, cen.Y, cen.Z = cen.X/n, cen.Y/n, cen.Z/n
		if d := dist(cen, stand); d > dungeon.RunLeaderSeeYd {
			t.Errorf("pull group %d stand is %.1f yd from its point, want <= %d", gi, d, dungeon.RunLeaderSeeYd)
		}
	}
}

// TestZgW39JeklikF1StandClearsTheOtherLanes: walk 39's step 29 fight started on the walk to
// F1's camp: the party crossed the 49135/49155/49189 lanes and the east-turned F3/F4 joined
// F1 (11 bodies). F1's stand is the walked point -12080 -1461 now, 15.6-20.6 yd from F1's
// east lane and >= 30 yd from the three formations that walk it next.
func TestZgW39JeklikF1StandClearsTheOtherLanes(t *testing.T) {
	p, err := dungeon.Load(filepath.Join(plansDirT, "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	movement, err := readMovementPaths(filepath.Join("testdata", "map309", "movement.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	leg := zgW35Leg(t, p, "high_priestess_jeklik")
	var g *dungeon.PullGroup
	for i := range leg.PullGroups {
		if pt := leg.PullGroups[i].Patrol; pt != nil && strings.Contains(pt.Source, "creature_movement 49147") {
			g = &leg.PullGroups[i]
			break
		}
	}
	if g == nil || len(g.Approach) == 0 {
		t.Fatal("no F1 stand pull carrying 49147's loop")
	}
	stand := g.Approach[len(g.Approach)-1]
	nearest := func(guid uint64) float64 {
		best := math.Inf(1)
		for _, q := range movement[guid] {
			if d := dist(stand, q); d < best {
				best = d
			}
		}
		return best
	}
	if d := nearest(49147); d > zgW37TankShotYd {
		t.Errorf("F1 stand is %.1f yd from 49147's loop (want <= %g)", d, zgW37TankShotYd)
	}
	for _, guid := range []uint64{49135, 49155, 49189} {
		if d := nearest(guid); d < 30 {
			t.Errorf("F1 stand is %.1f yd from %d's loop, want >= 30: the lane walks the party", d, guid)
		}
	}
}

// TestZgW39JeklikNorthCampIsItsOwnRow: walk 39's step-46 pull was refused with "the tank
// could not start moving to a firing position" while 11368 49123 stood 8.8 yd from the row
// centre and the lead bot 53.1 yd from it (stand -12175.4 -1447.4). Fix round 2 unmixed that
// row: the north camp (49131/49132/49133, 3 bodies) is its own stand pull after F6, from
// -12235 -1394 132.28 (mesh z; 19.6 yd from 49131, in the tank's shot, and 21.5 yd from the
// 49131 loop), and its max_bodies is its real count, not a raised ceiling. 49123 is F6's, in
// the row whose stand is 23.8 yd from the nearest of its pack (49125).
func TestZgW39JeklikNorthCampIsItsOwnRow(t *testing.T) {
	p, err := dungeon.Load(filepath.Join(plansDirT, "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	cs, err := readCreatures(filepath.Join("testdata", "map309", "creatures.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	forms := zgFormations(cs)
	leg := zgW35Leg(t, p, "high_priestess_jeklik")
	gi := -1
	for i := range leg.PullGroups {
		for _, e := range leg.PullGroups[i].Entries {
			if e == 11351 {
				gi = i
			}
		}
	}
	if gi < 0 {
		t.Fatal("no leg-0 pull group has the north camp's Headhunter entry 11351")
	}
	g := leg.PullGroups[gi]
	if !g.StandPull || len(g.Approach) == 0 {
		t.Fatalf("the north camp row is not a stand pull with a stand")
	}
	pack, err := zgPack(g, cs, forms)
	if err != nil {
		t.Fatalf("north camp row: %v", err)
	}
	var guids []uint64
	for _, c := range pack {
		guids = append(guids, c.GUID)
	}
	slices.Sort(guids)
	if want := []uint64{49131, 49132, 49133}; !slices.Equal(guids, want) {
		t.Errorf("the north camp row pulls %v, want %v (the camp alone: F6 and F5 are their own rows)", guids, want)
	}
	if g.MaxBodies != len(pack) {
		t.Errorf("the north camp row's max_bodies is %d, want its real count %d", g.MaxBodies, len(pack))
	}
	stand := g.Approach[len(g.Approach)-1]
	if d := zgW39NearestPackYd(g, stand, cs, forms); d > zgW37TankShotYd {
		t.Errorf("the north camp stand is %.1f yd from the nearest of its pack, past the %g yd shot", d, zgW37TankShotYd)
	}
	if d := dist(zgW39Centre(g), stand); d > dungeon.RunLeaderSeeYd {
		t.Errorf("the north camp stand is %.1f yd from its point, want <= %d", d, dungeon.RunLeaderSeeYd)
	}
}

// zgW39NearestPackYd is the 3D distance from stand to the nearest spawn of the row's pack (zgPack: the formations its
// positions name), the unit the stand-pull gate waits for to be in the tank's shot.
func zgW39NearestPackYd(g dungeon.PullGroup, stand dungeon.Point, cs []creature, forms map[uint64]*zgFormation) float64 {
	pack, err := zgPack(g, cs, forms)
	if err != nil {
		return math.Inf(1)
	}
	best := math.Inf(1)
	for _, c := range pack {
		if d := dist(stand, c.Pos); d < best {
			best = d
		}
	}
	return best
}

// zgW39Pack3D is zgPack's 3D twin: the run leader scopes a stand-pull row by PlanDistance (3D) of the spawns of the
// row's entries from its point, then expands to each scoped spawn's group_leader formation
// (DungeonRunPolicy.h ScopeStandPullFacts). zgPack keeps 2D (the conservative test model); this is what the server
// owns, so max_bodies is checked against it.
func zgW39Pack3D(g dungeon.PullGroup, cs []creature, forms map[uint64]*zgFormation) []creature {
	c := zgW39Centre(g)
	in := map[uint64]bool{}
	var leads []uint64
	for _, x := range cs {
		if !zgHostile(x) || !slices.Contains(g.Entries, x.Entry) {
			continue
		}
		if dist(c, x.Pos) > float64(g.Radius) {
			continue
		}
		in[x.GUID] = true
		lead := x.GroupLeader
		if lead == 0 {
			lead = x.GUID
		}
		if !slices.Contains(leads, lead) {
			leads = append(leads, lead)
		}
	}
	for _, x := range cs {
		if zgHostile(x) && slices.Contains(leads, x.GroupLeader) {
			in[x.GUID] = true
		}
	}
	var out []creature
	for _, x := range cs {
		if in[x.GUID] && zgHostile(x) {
			out = append(out, x)
		}
	}
	return out
}

// zgW39Centre is a pull group's row point: the mean of its positions (planexport writes it, and the run leader scopes a
// stand pull from it).
func zgW39Centre(g dungeon.PullGroup) dungeon.Point {
	var c dungeon.Point
	for _, p := range g.Positions {
		c.X += p.X
		c.Y += p.Y
		c.Z += p.Z
	}
	if n := float32(len(g.Positions)); n > 0 {
		c.X, c.Y, c.Z = c.X/n, c.Y/n, c.Z/n
	}
	return c
}

// zgW39Named is the set of spawns a row means to pull: for every position, the nearest spawn when it is one of the row's
// entries (the plan lists one spawn per member), expanded to that spawn's group_leader formation.
func zgW39Named(g dungeon.PullGroup, cs []creature, forms map[uint64]*zgFormation) map[uint64]bool {
	out := map[uint64]bool{}
	for _, pos := range g.Positions {
		best, bd := -1, math.Inf(1)
		for i := range cs {
			if !zgHostile(cs[i]) {
				continue
			}
			if d := dist(pos, cs[i].Pos); d < bd {
				bd, best = d, i
			}
		}
		if best < 0 || !slices.Contains(g.Entries, cs[best].Entry) {
			continue
		}
		lead := cs[best].GroupLeader
		if lead == 0 {
			lead = cs[best].GUID
		}
		if f := forms[lead]; f != nil {
			for _, m := range f.Member {
				out[m.GUID] = true
			}
		}
	}
	return out
}

// TestZgW39JeklikMoversOwnedByTheirRows is the Jeklik leg's ownership check, the walk-37 patrol rows' half: the run
// leader scopes a stand pull from the row's centre (the positions' mean) and radius, 2D, expanding every scoped spawn to
// its group_leader formation (DungeonRunPolicy.h ScopeStandPullFacts; routeOwnerOf's rule in route_aggro.go). Three
// things must hold or the row does not pull what it says. (a) Every moving rider's DB spawn lies inside its own row's 2D
// radius: F4's row stood at the south bat 49151's spawn with radius 1, 27.7 yd from 49189, so the rider the loop marks
// was outside the row. (b) A row must not scope a spawn that a LATER row's own positions name: F5's radius 40 reached
// F6's seven spawns 39.4-62.6 yd out, so the step-43 pull was 13 bodies (F5 6 + F6 7, the walk-39 wipe). (c) Every pull
// row must own at least one spawn: the south Headhunter camp row at -12219 -1491 named none (49134/35/36 belong to the
// radius-72 row pulled before it), pull_size printed "leg 0 pull 14: no target", and the run leader chased the residue
// (walk-37 FIX 2).
func TestZgW39JeklikMoversOwnedByTheirRows(t *testing.T) {
	p, err := dungeon.Load(filepath.Join(plansDirT, "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	cs, err := readCreatures(filepath.Join("testdata", "map309", "creatures.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	leg := zgW35Leg(t, p, "high_priestess_jeklik")
	forms := zgFormations(cs)
	groups := leg.PullGroups
	packs := make([][]creature, len(groups))
	owner := map[uint64]int{}
	for gi := range groups {
		pack, err := zgPack(groups[gi], cs, forms)
		if err != nil {
			t.Errorf("pull group %d: %v: the run leader has no target for the row", gi, err)
			continue
		}
		packs[gi] = pack
		for _, c := range pack {
			if _, ok := owner[c.GUID]; !ok {
				owner[c.GUID] = gi
			}
		}
	}
	// (a) the five movers' own spawns inside their rows' 2D radius.
	for _, guid := range zgW39Movers {
		gi := -1
		for i := range groups {
			if pt := groups[i].Patrol; pt != nil && strings.Contains(pt.Source, fmt.Sprintf("creature_movement %d", guid)) {
				gi = i
			}
		}
		if gi < 0 {
			t.Errorf("%d has no loop-carrying row", guid)
			continue
		}
		var c *creature
		for i := range cs {
			if cs[i].GUID == guid {
				c = &cs[i]
			}
		}
		if c == nil {
			t.Fatalf("%d is not in the map-309 export", guid)
		}
		cen := zgW39Centre(groups[gi])
		if d := math.Hypot(float64(c.Pos.X-cen.X), float64(c.Pos.Y-cen.Y)); d > float64(groups[gi].Radius) {
			t.Errorf("pull group %d (%d's row): its spawn is %.1f yd from the row centre, past radius %g: the row cannot scope its rider", gi, guid, d, groups[gi].Radius)
		}
	}
	// (a2) max_bodies is the row's real pack (3D, the run leader's scoping, less what an earlier row already pulled
	// and what the rows before it owned): the ceiling is a size, not a silencer. F5's is 6 with the lone ledge bat
	// 91551 out of its radius 14 (the reviewer's 7-vs-6 row); F6's is 7; the north camp's is 3.
	drained := map[uint64]bool{}
	realBodies := make([]int, len(groups))
	for gi := range groups {
		n := 0
		for _, c := range zgW39Pack3D(groups[gi], cs, forms) {
			if !drained[c.GUID] {
				n++
			}
			drained[c.GUID] = true
		}
		realBodies[gi] = n
	}
	for _, guid := range []uint64{49130, 49185} {
		gi := -1
		for i := range groups {
			if pt := groups[i].Patrol; pt != nil && strings.Contains(pt.Source, fmt.Sprintf("creature_movement %d", guid)) {
				gi = i
			}
		}
		if gi < 0 {
			t.Errorf("%d has no loop-carrying row", guid)
			continue
		}
		if groups[gi].MaxBodies != realBodies[gi] {
			t.Errorf("pull group %d (%d's row) has max_bodies %d, want its real pack %d", gi, guid, groups[gi].MaxBodies, realBodies[gi])
		}
	}
	campRow := -1
	for i := range groups {
		if slices.Contains(groups[i].Entries, 11351) {
			campRow = i
		}
	}
	if campRow < 0 {
		t.Fatal("no leg-0 row has the north camp's Headhunter entry 11351")
	}
	if groups[campRow].MaxBodies != realBodies[campRow] {
		t.Errorf("the north camp row has max_bodies %d, want its real pack %d", groups[campRow].MaxBodies, realBodies[campRow])
	}
	// (b) no row scopes a spawn a later row's positions name.
	named := make([]map[uint64]bool, len(groups))
	for gi := range groups {
		named[gi] = zgW39Named(groups[gi], cs, forms)
	}
	for gi := range groups {
		for _, c := range packs[gi] {
			for gj := gi + 1; gj < len(groups); gj++ {
				if named[gj][c.GUID] {
					t.Errorf("pull group %d scopes %s %d, which pull group %d's own positions name: the row pulls another formation (%d and %d together)", gi, c.Name, c.GUID, gj, gi, gj)
					break
				}
			}
		}
	}
	// (c) every pull row owns at least one spawn.
	for gi := range groups {
		n := 0
		for _, c := range packs[gi] {
			if owner[c.GUID] == gi {
				n++
			}
		}
		if n == 0 {
			t.Errorf("pull group %d (%.1f %.1f, radius %g) owns no spawn: the run leader has no target and chases the residue", gi, zgW39Centre(groups[gi]).X, zgW39Centre(groups[gi]).Y, groups[gi].Radius)
		}
	}
	// (d) the order and the walks into it (fix round 2). F6 (the formation interleaved with the north camp) is its
	// own row AFTER the radius-72 camp and BEFORE F5, the north camp between them: the walk that staged F5 first
	// passed F5's static bat 49140 at 2.2 yd (walk 39's step-43 13-body pull) and crossed 49130's loop. Every walk
	// segment from the S5 row to F5's stand must keep 15 yd (3D) off every spawn of a formation pulled after the
	// segment's end row and 20 yd (2D) off every rider loop pulled after it: those are the packs that would wake on
	// the walk (walk 39 fix 1's numbers). Sampling is the straight segment between the export's party points, as
	// route-aggro and pull_size read the rows (the navmesh hop itself is out of scope, L1).
	rowOfLoop := func(guid uint64) int {
		gi := -1
		for i := range groups {
			if pt := groups[i].Patrol; pt != nil && strings.Contains(pt.Source, fmt.Sprintf("creature_movement %d", guid)) {
				gi = i
			}
		}
		return gi
	}
	f6, camp, f5 := rowOfLoop(49130), campRow, rowOfLoop(49185)
	if f6 < 0 || f5 < 0 || camp < 0 {
		t.Fatalf("leg-0 rows: F6 %d, north camp %d, F5 %d", f6, camp, f5)
	}
	if !(f6 < camp && camp < f5) {
		t.Errorf("leg-0 order is F6 row %d, north camp row %d, F5 row %d: want F6 before the camp before F5 (the walk to F5 passes F5's spawns and F6's loop)", f6, camp, f5)
	}
	pts, err := routePoints(p)
	if err != nil {
		t.Fatal(err)
	}
	movement, err := readMovementPaths(filepath.Join("testdata", "map309", "movement.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	own := pullOwners(pts, cs)
	s5Row, f5Row := own[49137], own[49185]
	if s5Row == 0 && f5Row == 0 {
		t.Fatal("no route rows own 49137 (S5) or 49185 (F5)")
	}
	end := f5Row
	if end <= s5Row || end >= len(pts) || pts[s5Row].leg != 0 || pts[end].leg != 0 {
		t.Fatalf("the part is route rows %d..%d (%d route points)", s5Row, end, len(pts))
	}
	// sample takes n+1 points along a-b: the straight segment between two party points.
	sample := func(a, b dungeon.Point, n int) []dungeon.Point {
		var out []dungeon.Point
		for k := 0; k <= n; k++ {
			f := float32(k) / float32(n)
			out = append(out, dungeon.Point{X: a.X + (b.X-a.X)*f, Y: a.Y + (b.Y-a.Y)*f, Z: a.Z + (b.Z-a.Z)*f})
		}
		return out
	}
	for i := s5Row; i+1 <= end; i++ {
		a := dungeon.Point{X: pts[i].px, Y: pts[i].py, Z: pts[i].pz}
		b := dungeon.Point{X: pts[i+1].px, Y: pts[i+1].py, Z: pts[i+1].pz}
		if a == b {
			continue
		}
		endRow := pts[i+1].ownerIdx
		if endRow < 0 {
			endRow = pts[i+1].idx
		}
		// dest is the pull row this walk is heading to: its own rider's LOOP is not a hazard (a stand pull's stand
		// has to be reached, and the ring it walks closes round it), while its pack's spawns are: 15 yd off them
		// holds at every stand (walk 39's fix-2 rows are 19.6-25.1 yd off their own packs' nearest spawns).
		dest := -1
		for k := i + 2; k < len(pts); k++ {
			if pts[k].ownerIdx >= 0 {
				dest = pts[k].idx
				break
			}
		}
		n := int(math.Ceil(dist(a, b)))
		if n < 1 {
			n = 1
		}
		walk := sample(a, b, n)
		worst := math.Inf(1)
		var worstC creature
		for _, c := range cs {
			if !zgHostile(c) {
				continue
			}
			if o, pulled := own[c.GUID]; !pulled || o <= endRow {
				continue
			}
			for _, q := range walk {
				if d := dist(q, c.Pos); d < worst {
					worst, worstC = d, c
				}
			}
		}
		if worst < 15 {
			o := own[worstC.GUID]
			t.Errorf("walk rows %d->%d passes %.1f yd from %s %d (pulled by leg %d row %d): want >= 15 over the whole segment, the pack wakes on the walk", pts[i].seq, pts[i+1].seq, worst, worstC.Name, worstC.GUID, pts[o].leg, pts[o].seq)
		}
		for _, guid := range zgW39Movers {
			o, pulled := own[guid]
			if !pulled || o <= endRow || o == dest {
				continue
			}
			loop := movement[guid]
			nearest := math.Inf(1)
			for _, q := range walk {
				if d := closestOnLoop(q, loop); d < nearest {
					nearest = d
				}
			}
			if nearest < 20 {
				t.Errorf("walk rows %d->%d passes %.1f yd from %d's loop (pulled by leg %d row %d): want >= 20, the rider walks into the party", pts[i].seq, pts[i+1].seq, nearest, guid, pts[o].leg, pts[o].seq)
			}
		}
	}
}
