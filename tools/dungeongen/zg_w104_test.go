// Walk-104 fix (TortoiseBots-audit briefs-omp/zg-route-T29.md; tests/raid-zg-walk104 fz6d/fz5c): the Jin'do hall's
// west packs — the 49373-49382 cluster (group 5), the 49363 camp (group 6) and the 1173082 four (group 7) — were a
// plain pull at the camp itself (groups 5 and 7: their only approach point WAS the pack's centre) or a stand inside
// the camp's detection (group 6's stand -11581 -1334.8 stood 18.6 yd from its nearest unit 49370 and 2.6 yd above
// it), so the walking party woke them with no CC orders out. fz6d-1: at step 471 the tank engaged a Mistress "on
// arrival (0.0 yd)", the 24 bodies of the three camps plus three summoned Corrupted Infernals were in by 22:47:05
// and 20 bots died in 88 s; fz6d-3 474/475 repeated it ("engaged 10 over 6", 5 dead). All three are stand pulls now,
// each from a walked floor point out of every other hall pack's reach, and every walk in stays out of the detection
// of the pack it approaches. On origin/dev the test fails at the first group ("is not a stand pull").
//
// Round 2 (briefs-omp/zg-route-T29-r2.md) is the hall's east side, one-based groups 9 (the honour guard) and 10 (the
// north camp): the same method finds NO stand that makes either a one-at-a-time pull, and the plan note says so
// ("WALK 104 REVIEW R2") instead of claiming them. The guard's 19.5-28.5 yd own window is a 0.4 x 1.9 yd patch whose
// every point stands inside the reach of the escorts 49402/49403, which the pack's own units ring in (2.7-5.9 yd) and
// which stand 8.0/8.6 yd from Jin'do, so every such pull wakes him; the north camp spans 22.8 yd and its whole
// walkable window sits 11-19 yd from the pit's ten Sacrificed Trolls, so every such stand drags their ten in. The
// tests below pin both the body counts of the rows that are one-at-a-time pulls (one-based 6-8, under their ceilings)
// and the two no-stand proofs; the east rows keep the smallest counts any stand reaches (15 and 11) and stay
// gm-cleared.
//
// Round 3 (briefs-omp/zg-route-T29-r3.md) is one-based group 6's stand again: it kept 23 yd of neighbour clearance
// while the raid model is the unit's own reach (detection adjusted for the party's level, 22 yd here, plus the raid's
// 10 yd spread), so the stand sat 31.9 yd from group 4's 49397-49400 camp and 32.2 from the later 1173082 four. It
// moves 9.5 yd west, to (-11619.5 -1306 79.17): 21.2-27.6 yd from its own pack, 41.2 from the 1173082 four (its 32
// yd reach plus 8), at least 41 from every other later pack, the pit 61.7 and Jin'do past his reach; the row's live
// estimate is its own ten. The pull-size estimate still reads 14 because it counts every spawn alive: its four extra
// bodies are group 4's camp, which row "pull group 4" kills before this row. The test below now holds the west stands
// to the real reach of every pack pulled later and proves no reachable stand can clear both four-mob camps (the
// farthest from group 4's camp among the 300 valid stands is 28.4 yd, inside its 32 yd reach), so the 14 has no live
// effect and the plan note says so ("WALK 104 REVIEW R3").
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

// zgW104OtherYd is the clearance a stand keeps from every pack it does not pull: 20 yd detection plus the 3 yd
// spread the walk-38 stands use (zgLaterYd + zgPatrolSpreadYd), the rule the Jeklik ramp fix was held to.
const zgW104OtherYd = 23.0

// zgW104OwnWalkYd is the clearance the walk to a stand keeps from its own pack: past the 20 yd detection, so the
// party does not wake the pack before it stands and the row's CC orders can go out.
const zgW104OwnWalkYd = 21.0

// zgW104CampYd is the margin group 6's stand (one-based; zero-based pull group 5) keeps past the later four-mob
// camp's stand reach: its 22 yd detection plus the raid's 10 yd spread, and 8 yd more (review round 3). The stand is
// where the fight happens, so the camp it must not bring is the one pulled later (the 1173082 four), not the
// 49397-49400 camp group 4 pulls and kills first.
const zgW104CampYd = 8.0

// TestZgW104JindoHallWestPacksOneAtATime pins the walk-104 fix: the hall's three west packs are stand pulls whose
// stands keep 19.5-28 yd from their own pack (in the tank's shot, out of its detection), >= 23 yd from every pack the
// party has already killed and the level-adjusted detection plus the raid's 10 yd spread from every pack still alive
// (review round 3; one-based group 6 adds 8 yd against the later 1173082 four), the same reach from the hall's
// unowned spawns (the pit's Sacrificed Trolls, the north-west cluster) and Jin'do's route reach; the walks in keep
// >= 21 yd from the pack they approach and the same reach from every pack pulled later. With the mmaps it also
// checks line of sight from every stand to every unit of its pack and a complete navmesh path in.
func TestZgW104JindoHallWestPacksOneAtATime(t *testing.T) {
	p, cs := zgLoadPlanAndExport(t)
	leg, legNo := zgW37LegByName(t, p, "jindo_the_hexxer")
	packs := zgPacks(t, *leg, cs)
	owned := map[uint64]bool{}
	for gi := range packs {
		for _, c := range packs[gi] {
			owned[c.GUID] = true
		}
	}
	var jindo *creature
	for i := range cs {
		if cs[i].Entry == 11380 {
			jindo = &cs[i]
		}
	}
	if jindo == nil {
		t.Fatal("no Jin'do spawn in the map-309 export")
	}
	bossReach := routeReach(p, *jindo)

	want := []struct {
		group int
		guids []uint64
	}{
		{5, []uint64{49373, 49374, 49375, 49376, 49377, 49378, 49379, 49380, 49381, 49382}}, // the 49373-49382 cluster
		{6, []uint64{49363, 49364, 49365, 49366, 49367, 49368, 49369, 49370, 49371, 49372}}, // the 49363 camp
		{7, []uint64{1173082, 1173083, 1173084, 1173085}},                                   // the 1173082 four
	}
	stands := map[int]dungeon.Point{}
	for _, w := range want {
		where := fmt.Sprintf("leg 6 pull group %d", w.group)
		if w.group >= len(leg.PullGroups) {
			t.Errorf("%s: the leg has only %d pull groups", where, len(leg.PullGroups))
			continue
		}
		g := leg.PullGroups[w.group]
		got := map[uint64]bool{}
		for _, c := range packs[w.group] {
			got[c.GUID] = true
		}
		for _, guid := range w.guids {
			if !got[guid] {
				t.Errorf("%s does not hold %d: the hall's west packs must stay whole and in their own group", where, guid)
			}
		}
		if len(got) != len(w.guids) {
			t.Errorf("%s holds %d units, want its own %d", where, len(got), len(w.guids))
		}
		if !g.StandPull {
			t.Errorf("%s (%d units) is not a stand pull: the party walks to the camp and wakes it on the way (walk 104 step 471: the tank engaged a Mistress at 0.0 yd, 24 bodies, 20 dead)", where, len(got))
			continue
		}
		if len(g.Approach) == 0 {
			t.Errorf("%s is a stand pull with no approach", where)
			continue
		}
		stand := g.Approach[len(g.Approach)-1]
		stands[w.group] = stand
		// Its own pack: out of its detection on the walk in, inside the tank's shot so PatrolStandPullGate pulls
		// from the stand instead of falling back to a plain pull (zgW37TankShotYd).
		if own, who := zgMin3(stand, packs[w.group]); own < zgOwnMinYd {
			t.Errorf("%s: its stand %.1f %.1f %.1f is %.1f yd from %s %d, want >= %g: the party wakes the pack on the walk to the stand", where, stand.X, stand.Y, stand.Z, own, who.Name, who.Entry, zgOwnMinYd)
		}
		far := 0.0
		for _, c := range packs[w.group] {
			if d := dist(stand, c.Pos); d > far {
				far = d
			}
		}
		if far > zgOwnMaxYd {
			t.Errorf("%s: its stand is %.1f yd from the pack's farthest unit, past the %g yd shot: the row plain-pulls to the camp", where, far, zgOwnMaxYd)
		}
		// Every pack the party has already killed is its own ground: the 23 yd line. Every pack still alive when the
		// row is pulled keeps its own reach (detection adjusted for the party's level, plus the raid's 10 yd spread):
		// the round-1 stands were accepted on 23 yd alone, which let group 6's stand sit 31.9 yd from group 4's
		// camp and 32.2 from the 1173082 four (review round 3).
		for gi := range packs {
			if gi == w.group {
				continue
			}
			if gi < w.group {
				if d, who := zgMin3(stand, packs[gi]); d < zgW104OtherYd {
					t.Errorf("%s: its stand %.1f %.1f %.1f is %.1f yd from pull group %d's %s %d, want >= %g: the fight there brings that pack in", where, stand.X, stand.Y, stand.Z, d, gi, who.Name, who.Entry, zgW104OtherYd)
				}
				continue
			}
			for _, c := range packs[gi] {
				if d := dist(stand, c.Pos); d < zgW104Reach(c) {
					t.Errorf("%s: its stand %.1f %.1f %.1f is %.1f yd from %s %d of pull group %d (pulled later), want >= %.1f (its %.1f yd reach at party level %d): the fight there brings that pack in", where, stand.X, stand.Y, stand.Z, d, c.Name, c.GUID, gi, zgW104Reach(c), zgW104Reach(c)-aggroest.StandSpreadRaidYd, zgW104PartyLevel)
				}
			}
		}
		// One-based group 6 and the later four-mob camp: its reach plus 8 yd (review round 3). The camp is pulled
		// at group 7, so it is alive and must stay out of the fight's own stand reach with a margin.
		if w.group == 5 {
			for _, c := range packs[7] {
				if want := zgW104Reach(c) + zgW104CampYd; dist(stand, c.Pos) < want {
					t.Errorf("%s: its stand %.1f %.1f %.1f is %.1f yd from the later four-mob camp's %s %d, want >= %.1f (its %.1f yd reach plus %g): the pull brings the camp in", where, stand.X, stand.Y, stand.Z, dist(stand, c.Pos), c.Name, c.GUID, want, zgW104Reach(c), zgW104CampYd)
				}
			}
		}
		// The hall's hostiles no row pulls: the pit's ten Sacrificed Trolls (10 s respawn) and the north-west
		// cluster 49387-49396, at their own reach, plus Jin'do himself at his own route reach.
		for _, c := range cs {
			if !zgHostile(c) || owned[c.GUID] {
				continue
			}
			yd := zgW104Reach(c)
			if c.Entry == 11380 {
				yd = bossReach
			}
			if d := dist(stand, c.Pos); d < yd {
				t.Errorf("%s: its stand %.1f %.1f %.1f is %.1f yd from %s %d, which no row pulls, want >= %g", where, stand.X, stand.Y, stand.Z, d, c.Name, c.GUID, yd)
			}
		}
	}

	// The walks in: the party's own party points of the exported leg, so the segments are the ones the run leader
	// walks (a stand pull's party point is its stand; a plain pull's is its approach point).
	parties := zgParties(t, p, legNo)
	for _, w := range want {
		stand, ok := stands[w.group]
		if !ok {
			continue
		}
		at := -1
		for i := range parties {
			if parties[i].Turn && parties[i].Group == w.group {
				at = i
			}
		}
		if at < 0 {
			t.Errorf("leg 6 pull group %d: no exported pull row", w.group)
			continue
		}
		// The walk in starts where the party stood for the previous pull row (a stand pull's party point is its
		// stand, a plain pull's its approach point): the party's own points, so the segment is the one it walks.
		from, ok := dungeon.Point{}, false
		for i := at - 1; i >= 0; i-- {
			if parties[i].Turn {
				from, ok = parties[i].P, true
				break
			}
		}
		if !ok {
			t.Errorf("leg 6 pull group %d: no row before it to walk in from", w.group)
			continue
		}
		walk := zgSamplesAlong(from, stand, zgSampleStep)
		label := fmt.Sprintf("leg 6 pull group %d: the walk from %.1f %.1f to its stand", w.group, from.X, from.Y)
		if d, q, who := zgSampleMin(walk, packs[w.group]); d < zgW104OwnWalkYd {
			t.Errorf("%s passes %.1f yd from its own %s %d at %.1f %.1f, want >= %g (the pack wakes before the pull)", label, d, who.Name, who.Entry, q.X, q.Y, zgW104OwnWalkYd)
		}
		// The packs pulled later are still alive on this walk (the ones before it are the party's own ground: group
		// 4's camp is where it stood), so each keeps its own reach along the walk (review round 3), not the 23 yd
		// line the round-1 stands were held to.
		for _, c := range zgLater(packs, w.group) {
			if d, q, _ := zgSampleMin(walk, []creature{c}); d < zgW104Reach(c) {
				t.Errorf("%s passes %.1f yd from %s %d of a later group at %.1f %.1f, want >= %.1f (its %.1f yd reach at party level %d)", label, d, c.Name, c.GUID, q.X, q.Y, zgW104Reach(c), zgW104Reach(c)-aggroest.StandSpreadRaidYd, zgW104PartyLevel)
				break
			}
		}
	}

	// With the server's mmaps: line of sight from every stand to every unit of its pack (what the pull needs) and a
	// complete navmesh path to the stand (what the walk needs).
	pl := zgPlannerOrNil(t)
	if pl == nil {
		t.Log("no map 309 navmesh files (set TURTLE_AGENT_MMAPS to a folder holding 309.mmap and its tiles): the line-of-sight and path checks are skipped")
		return
	}
	defer pl.Close()
	for _, w := range want {
		stand, ok := stands[w.group]
		if !ok {
			continue
		}
		sv := navmesh.Vec3{stand.X, stand.Y, stand.Z}
		for _, c := range packs[w.group] {
			ray, err := pl.Raycast(p.Map, sv, navmesh.Vec3{c.Pos.X, c.Pos.Y, c.Pos.Z})
			if err != nil {
				t.Fatalf("leg 6 pull group %d: raycast to %s %d: %v", w.group, c.Name, c.GUID, err)
			}
			if !ray.Known || !ray.Clear {
				t.Errorf("leg 6 pull group %d: no line of sight from its stand %.1f %.1f %.1f to %s %d at %.1f %.1f: the pull never goes out", w.group, stand.X, stand.Y, stand.Z, c.Name, c.GUID, c.Pos.X, c.Pos.Y)
			}
		}
		for i := range parties {
			if !parties[i].Turn || parties[i].Group != w.group || i == 0 {
				continue
			}
			a, has := dungeon.Point{}, false
			for k := i - 1; k >= 0; k-- {
				if parties[k].Turn {
					a, has = parties[k].P, true
					break
				}
			}
			if !has {
				continue
			}
			path, err := pl.Plan(p.Map, navmesh.Vec3{a.X, a.Y, a.Z}, sv)
			if err != nil {
				t.Fatalf("leg 6 pull group %d: navmesh path: %v", w.group, err)
			}
			if path.Status != navmesh.Complete {
				t.Errorf("leg 6 pull group %d: no complete navmesh path from %.1f %.1f %.1f to its stand %.1f %.1f %.1f (%s %s): the walk pauses there", w.group, a.X, a.Y, a.Z, stand.X, stand.Y, stand.Z, path.Status, path.Reason)
			}
		}
	}
}

// zgW104RowBodies is the assist-model body estimate of leg 6's pull row i, the walk-38 review's own model
// (aggroest with the run leader's real family assist: RingYd 10 = the core's CreatureFamilyAssistanceRadius, one hop;
// the party level 58, the raid's 10 yd stand spread and the 3 yd walk spread). The rows before i have been pulled and
// are dead; the walk is the party's own points since the previous pull row (the run leader reads the walk it is on,
// its last 60 yd), and the stand is where zgW38Party stands. It returns the count, whether Jin'do joined, and each
// member with its reason, so a failure names the bodies.
func zgW104RowBodies(t *testing.T, p *dungeon.Plan, cs []creature, w *worldspawn.World, groups map[uint64]uint64, rows *zgW37Rows, parties map[int]dungeon.Point, i int, boss creature) (int, bool, []string) {
	t.Helper()
	alive := map[uint64]bool{}
	for _, c := range cs {
		if o, ok := rows.owner[c.GUID]; ok && o < i {
			continue
		}
		alive[c.GUID] = true
	}
	keep := func(sp worldspawn.Spawn) bool {
		return alive[sp.GUID] && zgHostile(creature{GUID: sp.GUID, LevelMax: sp.LevelMax, Faction: int(sp.Faction)})
	}
	mobs, entries := planlint.PullMobs(w.Spawns, nil, groups, keep)
	party := parties[i]
	rank := map[uint32]int{}
	for k, e := range p.KillFirst {
		if _, ok := rank[e]; !ok {
			rank[e] = k
		}
	}
	target, bestRank, bestDist := -1, 1<<30, math.Inf(1)
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
		t.Fatalf("leg 6 row %d (%s): no target among its owned spawns", i, rows.rows[i].Comment)
	}
	prevPull := -1
	for k := i - 1; k >= 0; k-- {
		if rows.rows[k].Kind == planexport.KindPull || rows.rows[k].Kind == planexport.KindBoss {
			prevPull = k
			break
		}
	}
	var walk []dungeon.Point
	start := 0
	if prevPull >= 0 {
		start = prevPull + 1
	}
	for k := start; k < i; k++ {
		walk = append(walk, parties[k])
	}
	walk = zgW38Tail(append(walk, party), 60)
	res := aggroest.Estimate(mobs, target, aggroest.Geometry{
		Walk: walk, WalkSpreadYd: aggroest.WalkSpreadYd,
		Stand: party, StandSpreadYd: aggroest.StandSpreadRaidYd,
	}, aggroest.Params{PartyLevel: 58, Rate: 1, RingYd: aggroest.AssistYd, RingHops: 1, FloorDz: aggroest.FloorDz})
	brought := false
	var why []string
	for _, m := range res.Members {
		c := zgW38Creature(cs, uint64(m.Guid))
		if c == nil {
			continue
		}
		why = append(why, fmt.Sprintf("%s/%d[%s]", c.Name, c.GUID, m.Why))
		if m.Guid == uint32(boss.GUID) {
			brought = true
		}
	}
	return res.Bodies, brought, why
}

// zgW104Yd2 is a horizontal (x, y) distance in yards: the stand windows below are read on the floor plan.
func zgW104Yd2(a, b dungeon.Point) float64 {
	dx, dy := float64(a.X-b.X), float64(a.Y-b.Y)
	return math.Sqrt(dx*dx + dy*dy)
}

// zgW104MinYd2 is the horizontal distance from p to the nearest of cs.
func zgW104MinYd2(p dungeon.Point, cs []creature) float64 {
	best := math.Inf(1)
	for _, c := range cs {
		if d := zgW104Yd2(p, c.Pos); d < best {
			best = d
		}
	}
	return best
}

// zgW104StandWindow lists the stands a one-at-a-time pull of the pack could use: every point on a 0.1 yd lattice over
// the pack's box plus the tank's shot, 19.5-28.5 yd from every unit of the pack (zgOwnMinYd keeps the party out of the
// pack's own detection on the walk in; zgW37TankShotYd keeps every unit in the tank's shot, so
// PatrolStandPullGate pulls from the stand instead of falling back to a plain pull). A pack with no point left has no
// one-at-a-time stand at all.
func zgW104StandWindow(pack []creature) []dungeon.Point {
	minx, maxx, miny, maxy := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
	for _, c := range pack {
		minx = math.Min(minx, float64(c.Pos.X))
		maxx = math.Max(maxx, float64(c.Pos.X))
		miny = math.Min(miny, float64(c.Pos.Y))
		maxy = math.Max(maxy, float64(c.Pos.Y))
	}
	pad := float64(zgW37TankShotYd)
	var out []dungeon.Point
	for xi := 0; ; xi++ {
		x := float32(minx - pad + float64(xi)*0.1)
		if float64(x) > maxx+pad {
			break
		}
		for yi := 0; ; yi++ {
			y := float32(miny - pad + float64(yi)*0.1)
			if float64(y) > maxy+pad {
				break
			}
			st := dungeon.Point{X: x, Y: y}
			lo, hi := math.Inf(1), math.Inf(-1)
			for _, c := range pack {
				d := zgW104Yd2(st, c.Pos)
				lo = math.Min(lo, d)
				hi = math.Max(hi, d)
			}
			if lo >= zgOwnMinYd && hi <= zgW37TankShotYd {
				out = append(out, st)
			}
		}
	}
	return out
}

// zgW104Reach is the stand aggro reach one of the hall's spawns has on the plan's party (level 58) in the run
// leader's model: creature_template detection_range minus the party-minus-mob level difference (at least min(det, 5)),
// plus the raid's 10 yd stand spread (aggroest.StandSpreadRaidYd). A level 60 20 yd spawn reaches 32 yd.
func zgW104Reach(c creature) float64 {
	det := c.DetectionRange
	if det < 1 {
		det = 20
	}
	reach := det - float64(zgW104PartyLevel-c.LevelMax)
	if floor := math.Min(det, 5); reach < floor {
		reach = floor
	}
	return reach + aggroest.StandSpreadRaidYd
}

const zgW104PartyLevel = 58

// TestZgW104JindoHallPullSizesUnderTheirCeilings is round 2 of the walk-104 fix: the hall's west packs (one-based
// groups 6-8) are one-at-a-time pulls under the assist model, and the east side (one-based 9-10) is not, as the plan's
// WALK 104 REVIEW R2 note says. fz6d-1's 24 bodies and 20 dead at step 471 came from rows whose stand was the pack
// itself; on origin/dev this test fails on the first group below ("brings 14 bodies over its ceiling 10" on the walk-104
// side and higher on the old stands), while the east rows keep their measured 15 and 11 until a stand stages them.
func TestZgW104JindoHallPullSizesUnderTheirCeilings(t *testing.T) {
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
	groups := planlint.PackIDs(w)
	rows := zgW37BuildRows(t, p, cs, 6)
	leg, legNo := zgW37LegByName(t, p, "jindo_the_hexxer")
	packs := zgPacks(t, *leg, cs)
	var boss creature
	for _, c := range cs {
		if c.Entry == 11380 {
			boss = c
		}
	}
	if boss.GUID == 0 {
		t.Fatal("no Jin'do spawn in the map-309 export")
	}
	parties := map[int]dungeon.Point{}
	rowOf := map[int]int{}
	for i := range rows.rows {
		parties[i] = zgW38Party(t, pl, rows, i, cs)
		s := rows.rows[i]
		if s.Kind != planexport.KindPull {
			continue
		}
		if g := zgW38Group(s.Comment); g >= 0 {
			if _, seen := rowOf[g]; !seen {
				rowOf[g] = i
			}
		}
	}
	// One-based 6-8: the walk-104 fix's stand pulls stay at or under their max_bodies and bring no Jin'do. The party
	// point is checked first: a plain pull at the camp counts the pack once either way, so the body count only means
	// one-at-a-time if the party stands outside the pack's own wake.
	for _, g := range []int{5, 6, 7} {
		i, ok := rowOf[g]
		if !ok {
			t.Errorf("leg 6 pull group %d (one-based %d): no exported pull row", g, g+1)
			continue
		}
		if rows.rows[i].Flags&planexport.FlagStandPull == 0 {
			t.Errorf("leg 6 pull group %d (one-based %d, row %d): the row is not a stand pull, so the party walks to the pack and fights it all at once (walk 104 step 471: 24 bodies, 20 dead)", g, g+1, i)
			continue
		}
		if own, who := zgMin3(parties[i], packs[g]); own < zgOwnMinYd {
			t.Errorf("leg 6 pull group %d (one-based %d, row %d): the party stands at %.1f %.1f, %.1f yd from its own %s %d, want >= %g: the pack wakes on the walk in and fights with no CC orders out", g, g+1, i, parties[i].X, parties[i].Y, own, who.Name, who.Entry, zgOwnMinYd)
		}
		bodies, jin, why := zgW104RowBodies(t, p, cs, w, groups, rows, parties, i, boss)
		ceiling := int(rows.rows[i].Item)
		if ceiling == 0 {
			ceiling = zgW38PullCeiling
		}
		if bodies > ceiling {
			t.Errorf("leg 6 pull group %d (one-based %d, row %d): the pull brings %d bodies over its ceiling %d: %v", g, g+1, i, bodies, ceiling, why)
		}
		// Review round 3's rule: one-at-a-time means the row's own pack, nothing else. A count over the pack is a
		// neighbour the stand's reach brings in, whatever the ceiling says.
		if bodies != len(packs[g]) {
			t.Errorf("leg 6 pull group %d (one-based %d, row %d): the pull brings %d bodies, its pack is %d: the stand is inside a neighbour's reach: %v", g, g+1, i, bodies, len(packs[g]), why)
		}
		if jin {
			t.Errorf("leg 6 pull group %d (one-based %d, row %d) brings Jin'do 11380 before his boss row: %v", g, g+1, i, why)
		}
	}
	// One-based 9-10: the plan note pins them as unsupported (no stand stages them) at the smallest counts measured;
	// a stand that ever brings them under the ceiling must turn them into one-at-a-time pulls, note included.
	notes := strings.Join(p.Legs[legNo].Notes, "\n")
	if !strings.Contains(notes, "WALK 104 REVIEW R2") || !strings.Contains(notes, "are NOT one-at-a-time pulls") {
		t.Errorf("leg 6's plan note does not document one-based groups 9 and 10 as unsupported (WALK 104 REVIEW R2): the plan must not claim them")
	}
	if !strings.Contains(notes, "WALK 104 REVIEW R3") || !strings.Contains(notes, "stays the fallback") {
		t.Errorf("leg 6's plan note does not document group 6's round-3 stand, the dead camp's four bodies and the +8 margin's limit (WALK 104 REVIEW R3)")
	}
	for _, c := range []struct{ group, bodies int }{{8, 15}, {9, 11}} {
		i, ok := rowOf[c.group]
		if !ok {
			t.Errorf("leg 6 pull group %d (one-based %d): no exported pull row", c.group, c.group+1)
			continue
		}
		bodies, jin, why := zgW104RowBodies(t, p, cs, w, groups, rows, parties, i, boss)
		ceiling := int(rows.rows[i].Item)
		if bodies != c.bodies {
			t.Errorf("leg 6 pull group %d (one-based %d, row %d): the plan note pins %d bodies, the assist model reads %d: %v", c.group, c.group+1, i, c.bodies, bodies, why)
		}
		if bodies <= ceiling {
			t.Errorf("leg 6 pull group %d (one-based %d, row %d): documented as unsupported by any stand, it brings %d bodies at or under its ceiling %d: a stand now stages it -- make it a one-at-a-time pull and update the plan note", c.group, c.group+1, i, bodies, ceiling)
		}
		if !jin {
			t.Errorf("leg 6 pull group %d (one-based %d, row %d): the plan note says the pull wakes Jin'do, it does not: %v", c.group, c.group+1, i, why)
		}
	}
}

// TestZgW104JindoHallEastPacksHaveNoIsolatingStand is the STOP half of round 2: it proves on the floor plan that no
// stand can make one-based group 9 (the honour guard) or 10 (the north camp) a one-at-a-time pull.
//   - The guard's one-at-a-time window exists, but every point of it stands inside the stand reach of the escorts
//     49402/49403, and both escorts stand inside the 10 yd family-assist ring of Jin'do (the run leader's model:
//     RingYd = CreatureFamilyAssistanceRadius, one hop): any guard pull from any of those stands wakes the escorts and
//     the boss. The pack's own units stand 2.7-5.9 yd from the escorts, so no other stand helps either.
//   - The north camp's window is entirely west of the camp, and every point of it is inside the stand reach of the
//     pit's ten Sacrificed Trolls (49078-49087, 10 s respawns) or of the escorts: a stand there brings ten trolls or
//     the boss. The camp spans 22.8 yd, so its window is small and sits between the pit and Jin'do's escorts.
func TestZgW104JindoHallEastPacksHaveNoIsolatingStand(t *testing.T) {
	p, cs := zgLoadPlanAndExport(t)
	leg, _ := zgW37LegByName(t, p, "jindo_the_hexxer")
	packs := zgPacks(t, *leg, cs)
	if len(packs) < 10 {
		t.Fatalf("leg 6 has %d pull groups, want at least the hall's 0-9", len(packs))
	}
	guard, north := packs[8], packs[9]
	if len(guard) != 8 || len(north) != 8 {
		t.Fatalf("leg 6 pull group 8 holds %d units and group 9 %d, want the honour guard's 8 and the north camp's 8", len(guard), len(north))
	}
	one := func(guid uint64) creature {
		for _, c := range cs {
			if c.GUID == guid {
				return c
			}
		}
		t.Fatalf("the map-309 export has no spawn %d", guid)
		return creature{}
	}
	escortA, escortB, jindo := one(49402), one(49403), one(49655)
	escorts := []creature{escortA, escortB}
	pit := make([]creature, 0, 10)
	for guid := uint64(49078); guid <= 49087; guid++ {
		pit = append(pit, one(guid))
	}
	// The guard: every one-at-a-time stand wakes an escort, and each escort calls Jin'do (one 10 yd hop).
	window := zgW104StandWindow(guard)
	if len(window) == 0 {
		t.Fatalf("leg 6 pull group 8 (one-based 9) has no stand 19.5-28.5 yd from its whole pack: the plan note's no-stand claim is stale, re-measure it")
	}
	escortReach := zgW104Reach(escortA)
	if want := zgW104Reach(escortB); want > escortReach {
		escortReach = want
	}
	for _, st := range window {
		if d := zgW104MinYd2(st, escorts); d > escortReach {
			t.Errorf("leg 6 pull group 8 (one-based 9): the stand %.1f %.1f is %.1f yd from an escort, past its %.1f yd stand reach: the pack can be staged clear of 49402/49403 there (re-measure the plan note)", st.X, st.Y, d, escortReach)
		}
	}
	if d := zgW104MinYd2(guard[0].Pos, escorts); d > aggroest.AssistYd {
		t.Errorf("leg 6 pull group 8 (one-based 9): its nearest unit to an escort is %.1f yd, past the %g yd family ring the plan note reasons from", d, aggroest.AssistYd)
	}
	for _, e := range escorts {
		if d := zgW104Yd2(e.Pos, jindo.Pos); d > aggroest.AssistYd {
			t.Errorf("leg 6 pull group 8 (one-based 9): the escort %d is %.1f yd from Jin'do 11380, past the %g yd family ring: waking it need not wake him, re-measure the note", e.GUID, d, aggroest.AssistYd)
		}
	}
	// The north camp: every one-at-a-time stand wakes the pit's ten or the escorts.
	window = zgW104StandWindow(north)
	if len(window) == 0 {
		t.Fatalf("leg 6 pull group 9 (one-based 10) has no stand 19.5-28.5 yd from its whole pack: the plan note's no-stand claim is stale, re-measure it")
	}
	pitReach := zgW104Reach(pit[0])
	for _, st := range window {
		pitYd, escortYd := zgW104MinYd2(st, pit), zgW104MinYd2(st, escorts)
		if pitYd > pitReach && escortYd > escortReach {
			t.Errorf("leg 6 pull group 9 (one-based 10): the stand %.1f %.1f is %.1f yd from the pit's trolls and %.1f from an escort, past both reaches: the camp can be staged there (re-measure the plan note)", st.X, st.Y, pitYd, escortYd)
		}
	}
}

// TestZgW104JindoHallGroup6CampMarginIsTheWindowLimit is the second half of review round 3 (briefs-omp/
// zg-route-T29-r3.md): group 6's stand clears the later four-mob camp (the 1173082 four, pulled at group 7) by the
// camp's reach plus 8 yd, while the pull-size estimate still reads 14 bodies for the row. Those four extra are group
// 4's camp (49397-49400): row "pull group 4" pulls them and kills them before group 6, so the row's live estimate is
// its own ten (TestZgW104JindoHallPullSizesUnderTheirCeilings), and no stand removes the four from the all-alive
// estimate: of every navmesh floor point the walk reaches, in the pack's 21-28.5 yd window, with line of sight to all
// ten, a complete path in and 40+ yd from the 1173082 four, the farthest from group 4's camp is 28.4 yd, inside its
// 32 yd reach. The only points 32+ yd from both camps are a lip at the old stand (-11610 -1306 79.2), which has
// neither the +8 margin nor a clear walk. This pins the limit the plan note states, so the row cannot be "fixed" by
// another stand and the pull-size 14 is the dead camp.
func TestZgW104JindoHallGroup6CampMarginIsTheWindowLimit(t *testing.T) {
	p, cs := zgLoadPlanAndExport(t)
	pl := zgPlannerOrNil(t)
	if pl == nil {
		t.Skip("needs the map-309 mmaps (the stand window, sight and path)")
	}
	defer pl.Close()
	leg, legNo := zgW37LegByName(t, p, "jindo_the_hexxer")
	packs := zgPacks(t, *leg, cs)
	own, dead, camp := packs[5], packs[4], packs[7]
	// The dead camp is pulled and killed before group 6: its own row comes first in the export.
	rows := zgW37BuildRows(t, p, cs, 6)
	deadRow, ok := rows.owner[dead[0].GUID]
	if !ok || rows.rows[deadRow].Kind != planexport.KindPull || rows.rows[deadRow].Comment != "pull group 4" {
		t.Fatalf("the 49397-49400 camp is not row pull group 4 (owner %d): the four bodies the estimate adds have no earlier row to kill them", deadRow)
	}
	groupRow, ok := rows.owner[own[0].GUID]
	if !ok || groupRow <= deadRow {
		t.Fatalf("group 6's row (%d) does not come after group 4's (%d): the walk does not reach the camp first", groupRow, deadRow)
	}
	for _, c := range dead[1:] {
		if rows.owner[c.GUID] != deadRow {
			t.Errorf("%d is owned by row %d, not the camp's row %d: the camp must be one pack", c.GUID, rows.owner[c.GUID], deadRow)
		}
	}
	// The walk in starts where the party stood at the previous pull row, as the run leader walks it.
	parties := zgParties(t, p, legNo)
	at := -1
	for i := range parties {
		if parties[i].Turn && parties[i].Group == 5 {
			at = i
		}
	}
	if at < 0 {
		t.Fatal("no exported pull row for pull group 5")
	}
	from, ok := dungeon.Point{}, false
	for i := at - 1; i >= 0; i-- {
		if parties[i].Turn {
			from, ok = parties[i].P, true
			break
		}
	}
	if !ok {
		t.Fatal("no row before group 6 to walk in from")
	}
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
	// Every unit alive when the row is pulled: the packs after it and the hall's unowned hostiles.
	owned := map[uint64]bool{}
	for gi := range packs {
		for _, c := range packs[gi] {
			owned[c.GUID] = true
		}
	}
	var alive []creature
	for gi := 6; gi < len(packs); gi++ {
		alive = append(alive, packs[gi]...)
	}
	for _, c := range cs {
		if zgHostile(c) && !owned[c.GUID] {
			alive = append(alive, c)
		}
	}
	minx, maxx, miny, maxy := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
	for _, c := range own {
		minx, maxx = math.Min(minx, float64(c.Pos.X)), math.Max(maxx, float64(c.Pos.X))
		miny, maxy = math.Min(miny, float64(c.Pos.Y)), math.Max(maxy, float64(c.Pos.Y))
	}
	pad := float64(zgW37TankShotYd)
	valid := 0
	maxDead := 0.0
	maxAt := dungeon.Point{}
	var both []dungeon.Point
	for xi := 0; ; xi++ {
		x := float32(minx - pad + float64(xi)*0.1)
		if float64(x) > maxx+pad {
			break
		}
		for yi := 0; ; yi++ {
			y := float32(miny - pad + float64(yi)*0.1)
			if float64(y) > maxy+pad {
				break
			}
			// The 3D window is inside the 2D one, so the 2D ring is a cheap superset filter.
			lo2, hi2 := math.Inf(1), math.Inf(-1)
			for _, c := range own {
				d := zgW104Yd2(dungeon.Point{X: x, Y: y}, c.Pos)
				lo2, hi2 = math.Min(lo2, d), math.Max(hi2, d)
			}
			if lo2 < zgOwnMinYd || hi2 > zgOwnMaxYd {
				continue
			}
			floors, err := pl.Floors(309, x, y, 75, 82)
			if err != nil {
				t.Fatalf("floors at %.1f %.1f: %v", x, y, err)
			}
			for _, f := range floors {
				st := dungeon.Point{X: x, Y: y, Z: f.Z}
				lo, hi := math.Inf(1), math.Inf(-1)
				for _, c := range own {
					d := dist(st, c.Pos)
					lo, hi = math.Min(lo, d), math.Max(hi, d)
				}
				if lo < zgOwnMinYd || hi > zgOwnMaxYd {
					continue
				}
				walk := zgSamplesAlong(from, st, zgSampleStep)
				if d, _, _ := zgSampleMin(walk, own); d < zgW104OwnWalkYd {
					continue
				}
				clear := true
				for _, c := range alive {
					yd := zgW104Reach(c)
					if c.Entry == 11380 {
						yd = bossReach
					}
					if dist(st, c.Pos) < yd {
						clear = false
						break
					}
				}
				if !clear {
					continue
				}
				los := true
				for _, c := range own {
					ray, err := pl.Raycast(309, navmesh.Vec3{st.X, st.Y, st.Z}, navmesh.Vec3{c.Pos.X, c.Pos.Y, c.Pos.Z})
					if err != nil || !ray.Known || !ray.Clear {
						los = false
						break
					}
				}
				if !los {
					continue
				}
				path, err := pl.Plan(309, navmesh.Vec3{from.X, from.Y, from.Z}, navmesh.Vec3{st.X, st.Y, st.Z})
				if err != nil || path.Status != navmesh.Complete {
					continue
				}
				campYd, _ := zgMin3(st, camp)
				deadYd, _ := zgMin3(st, dead)
				if campYd >= zgW104Reach(camp[0]) && deadYd >= zgW104Reach(dead[0]) {
					both = append(both, st)
				}
				if campYd < zgW104Reach(camp[0])+zgW104CampYd {
					continue
				}
				valid++
				if deadYd > maxDead {
					maxDead, maxAt = deadYd, st
				}
			}
		}
	}
	if valid == 0 {
		t.Fatalf("no stand in the window keeps %g yd past the 1173082 four's reach: the plan's stand is not one, re-measure it", zgW104CampYd)
	}
	t.Logf("%d valid stands, farthest from group 4's camp %.2f yd", valid, maxDead)
	if maxDead >= zgW104Reach(dead[0]) {
		t.Errorf("a stand %.1f %.1f %.1f is %.1f yd from group 4's camp and still %g yd past the 1173082 four's reach: the two four-mob camps no longer force the estimate's four extra bodies -- re-measure the plan note and the pull-size pin", maxAt.X, maxAt.Y, maxAt.Z, maxDead, zgW104CampYd)
	}
	oldStand := dungeon.Point{X: -11610, Y: -1306}
	for _, st := range both {
		if zgW104Yd2(st, oldStand) > 1.5 {
			t.Errorf("the stand %.1f %.1f %.1f is %g yd from both four-mob camps' reaches and %.1f yd from the old stand: a stand outside both now exists, re-measure the plan note and the pull-size pin", st.X, st.Y, st.Z, zgW104Reach(dead[0]), zgW104Yd2(st, oldStand))
		}
	}
}
