// Walk-37 fixes (TortoiseBots-audit tests/raid-zg-walk37): the Jin'do hall's honour guard, the Jeklik
// plateau's empty bat row, and the five patrol rows the exporter cut through the pack fields (Venoxis
// groups 10/18/19, Thekal group 3, Arlokk group 21). The rows are built with the live export flags
// (plan-export --approach-hops --floor-aware), so the approach-path rows the run leader actually walks
// are the ones checked: zgParties' model (the party stands at the last approach point) cannot see them.
package main

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// zgW37TankShotYd is the run leader's shot minus its stand margin (TortoiseBots PatrolStandPullGate:
// inShot = leadShotYd - kStandPullMarginYd, 29.5 - 1): a stand-pull row whose pack is not all inside it
// falls back to a plain pull and walks the party to the pack (walk 37 e2 step 552).
const zgW37TankShotYd = 28.5

// zgW37LegByName finds a leg by name.
func zgW37LegByName(t *testing.T, p *dungeon.Plan, name string) (*dungeon.Leg, int) {
	t.Helper()
	for i := range p.Legs {
		if p.Legs[i].Name == name {
			return &p.Legs[i], i
		}
	}
	t.Fatalf("no %s leg", name)
	return nil, -1
}

// zgW37ExportCut is cmd/turtle-agent plan_export_cmd.go's floor-aware cutter (floorAwareCut and its helpers,
// kept in step with it): the path's corners are merged (under FloorAwareMergeYd), dropped (a turn under
// FloorAwareTurnDeg, when canDrop says the walk may skip it) or pushed FloorAwarePushYd off the nearest wall,
// then joined in hops of at most FloorAwareHopYd, each split point taking its z from the plain ground there
// (dropped when there is none within FloorAwareFloorYd of the straight line's z).
func zgW37ExportCut(pl *navmesh.Planner) planexport.CutFunc {
	return func(mapID uint32, from, to dungeon.Point) ([]dungeon.Point, []planexport.CutNote, error) {
		a := navmesh.Vec3{from.X, from.Y, from.Z}
		b := navmesh.Vec3{to.X, to.Y, to.Z}
		path, err := pl.Plan(mapID, a, b)
		if err != nil {
			return nil, nil, err
		}
		if path.Status != navmesh.Complete || len(path.Points) < 2 {
			return nil, nil, fmt.Errorf("no complete path (%s %s)", path.Status, path.Reason)
		}
		return zgW37FloorAwareCut(pl, mapID, path)
	}
}

func zgW37FloorAwareCut(pl *navmesh.Planner, mapID uint32, path navmesh.Path) ([]dungeon.Point, []planexport.CutNote, error) {
	var notes []planexport.CutNote
	kept := []navmesh.Vec3{path.Points[0]}
	for i := 1; i < len(path.Points)-1; i++ {
		p := path.Points[i]
		prev := kept[len(kept)-1]
		next := path.Points[i+1]
		if turn := zgW37TurnDeg(path.Points[i-1], p, next); turn < planexport.FloorAwareTurnDeg && zgW37CanDrop(pl, mapID, prev, next) {
			notes = append(notes, planexport.CutNote{Old: zgW37CutPoint(p), Dropped: true, Reason: fmt.Sprintf("the turn is %.0f degrees (under %g)", turn, planexport.FloorAwareTurnDeg)})
			continue
		}
		if d := zgW37CutDist(prev, p); d < planexport.FloorAwareMergeYd && zgW37CanDrop(pl, mapID, prev, next) {
			notes = append(notes, planexport.CutNote{Old: zgW37CutPoint(p), Dropped: true, Reason: fmt.Sprintf("%.1f yd from the point before it (under %g)", d, planexport.FloorAwareMergeYd)})
			continue
		}
		q, _, _ := zgW37PushOffWall(pl, mapID, p)
		kept = append(kept, q)
	}
	kept = append(kept, path.Points[len(path.Points)-1])
	var via []dungeon.Point
	for i := 1; i < len(kept); i++ {
		a, b := kept[i-1], kept[i]
		d := zgW37CutDist(a, b)
		if d > planexport.FloorAwareHopYd {
			n := int(math.Ceil(float64(d) / planexport.FloorAwareHopYd))
			for k := 1; k < n; k++ {
				f := float64(k) / float64(n)
				x, y := a[0]+(b[0]-a[0])*float32(f), a[1]+(b[1]-a[1])*float32(f)
				meanZ := a[2] + (b[2]-a[2])*float32(f)
				floor, ok, err := zgW37SplitFloor(pl, mapID, x, y, meanZ)
				if err != nil {
					return nil, notes, err
				}
				if !ok {
					notes = append(notes, planexport.CutNote{Old: dungeon.Point{X: x, Y: y, Z: meanZ}, Dropped: true,
						Reason: fmt.Sprintf("no ground within %g yd of %.1f yd", planexport.FloorAwareFloorYd, meanZ)})
					continue
				}
				via = append(via, dungeon.Point{X: x, Y: y, Z: floor.Z})
			}
		}
		if i < len(kept)-1 {
			via = append(via, zgW37CutPoint(kept[i]))
		}
	}
	return via, notes, nil
}

func zgW37PushOffWall(pl *navmesh.Planner, mapID uint32, p navmesh.Vec3) (navmesh.Vec3, bool, string) {
	dirs := 16
	best, bestR := -1, float32(-1)
	for k := range dirs {
		a := 2 * math.Pi * float64(k) / float64(dirs)
		q := navmesh.Vec3{p[0] + planexport.FloorAwareEdgeScanYd*float32(math.Cos(a)), p[1] + planexport.FloorAwareEdgeScanYd*float32(math.Sin(a)), p[2]}
		ray, err := pl.Raycast(mapID, p, q)
		if err != nil || !ray.Known {
			return p, false, "the mesh cannot measure the wall at the corner"
		}
		if !ray.Clear && (bestR < 0 || ray.Hit < bestR) {
			best, bestR = k, ray.Hit
		}
	}
	if best < 0 {
		return p, false, ""
	}
	a := 2 * math.Pi * float64(best) / float64(dirs)
	q := navmesh.Vec3{p[0] - planexport.FloorAwarePushYd*float32(math.Cos(a)), p[1] - planexport.FloorAwarePushYd*float32(math.Sin(a)), p[2]}
	n, ok, err := pl.Nearest(mapID, q)
	if err != nil || !ok {
		return p, false, "no walkable mesh within 4 yd of the pushed point"
	}
	floor, ok, err := zgW37GroundFloor(pl, mapID, n)
	if err != nil || !ok || math.Abs(float64(floor.Z-n[2])) > planexport.FloorAwareFloorYd ||
		floor.Flags&navmesh.NavGround == 0 || floor.Flags&(navmesh.NavWater|navmesh.NavSteep|navmesh.NavMagma|navmesh.NavSlime) != 0 {
		return p, false, "the point off the wall is not plain ground"
	}
	return n, true, ""
}

func zgW37SplitFloor(pl *navmesh.Planner, mapID uint32, x, y, meanZ float32) (navmesh.Floor, bool, error) {
	fl, err := pl.Floors(mapID, x, y, meanZ-planexport.FloorAwareFloorYd, meanZ+planexport.FloorAwareFloorYd)
	if err != nil {
		return navmesh.Floor{}, false, err
	}
	best, ok := navmesh.Floor{}, false
	for _, f := range fl {
		if f.Flags&navmesh.NavGround == 0 || f.Flags&(navmesh.NavWater|navmesh.NavSteep|navmesh.NavMagma|navmesh.NavSlime) != 0 {
			continue
		}
		if d := math.Abs(float64(f.Z - meanZ)); d <= planexport.FloorAwareFloorYd && (!ok || d < math.Abs(float64(best.Z-meanZ))) {
			best, ok = f, true
		}
	}
	return best, ok, nil
}

func zgW37GroundFloor(pl *navmesh.Planner, mapID uint32, p navmesh.Vec3) (navmesh.Floor, bool, error) {
	fl, err := pl.Floors(mapID, p[0], p[1], p[2]-6, p[2]+6)
	if err != nil || len(fl) == 0 {
		return navmesh.Floor{}, false, err
	}
	best := fl[0]
	for _, f := range fl {
		if math.Abs(float64(f.Z-p[2])) < math.Abs(float64(best.Z-p[2])) {
			best = f
		}
	}
	return best, true, nil
}

func zgW37CanDrop(pl *navmesh.Planner, mapID uint32, a, b navmesh.Vec3) bool {
	d := zgW37CutDist(a, b)
	if d > planexport.FloorAwareHopYd {
		return false
	}
	ray, err := pl.Raycast(mapID, a, b)
	if err != nil || !ray.Known || !ray.Clear && float64(ray.Hit) < d-1 {
		return false
	}
	_, ok, err := zgW37SplitFloor(pl, mapID, (a[0]+b[0])/2, (a[1]+b[1])/2, (a[2]+b[2])/2)
	return err == nil && ok
}

func zgW37TurnDeg(a, b, c navmesh.Vec3) float64 {
	u := navmesh.Vec3{b[0] - a[0], b[1] - a[1], b[2] - a[2]}
	v := navmesh.Vec3{c[0] - b[0], c[1] - b[1], c[2] - b[2]}
	lu, lv := zgW37CutDist(a, b), zgW37CutDist(b, c)
	if lu == 0 || lv == 0 {
		return 180
	}
	dot := float64(u[0])*float64(v[0]) + float64(u[1])*float64(v[1]) + float64(u[2])*float64(v[2])
	cos := math.Max(-1, math.Min(1, dot/(lu*lv)))
	return math.Acos(cos) * 180 / math.Pi
}

func zgW37CutDist(a, b navmesh.Vec3) float64 {
	dx, dy, dz := float64(b[0]-a[0]), float64(b[1]-a[1]), float64(b[2]-a[2])
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

func zgW37CutPoint(v navmesh.Vec3) dungeon.Point { return dungeon.Point{X: v[0], Y: v[1], Z: v[2]} }

// zgW37Rows is one leg's live rows and the model both checks below share: which row pulls a spawn (the
// first pull/boss row whose entries hold its entry and whose radius reaches it from the row's point,
// expanded to the spawn's whole formation: routeOwnerOf's rule, and TortoiseBots' own read -- a step's
// units are its entries' spawns, and a stand-pull pack is the formations of those entries whose DB spawn
// lies inside the row's radius) and where the party stands for a row (a stand pull's stand is the move
// row right before it; every other row's point is where the party goes).
type zgW37Rows struct {
	rows   []planexport.StepRow
	owner  map[uint64]int // spawn guid -> index in rows, -1 when no row pulls it
	leg    int
	byLead map[uint64][]creature
}

func zgW37BuildRows(t *testing.T, p *dungeon.Plan, cs []creature, leg int) *zgW37Rows {
	t.Helper()
	pl := zgPlannerOrNil(t)
	if pl == nil {
		t.Skip("no map 309 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 309.mmap and its tiles")
	}
	defer pl.Close()
	res, err := planexport.Build(p, planexport.Options{ApproachHops: true, Cut: zgW37ExportCut(pl), GMCrossDirect: true})
	if err != nil {
		t.Fatal(err)
	}
	out := &zgW37Rows{leg: leg, byLead: map[uint64][]creature{}}
	for _, s := range res.Rows.Steps {
		if s.Leg == leg {
			out.rows = append(out.rows, s)
		}
	}
	for _, c := range cs {
		if c.GroupLeader != 0 {
			out.byLead[c.GroupLeader] = append(out.byLead[c.GroupLeader], c)
		}
	}
	out.owner = map[uint64]int{}
	for gi := range out.rows {
		s := out.rows[gi]
		if s.Kind != planexport.KindPull && s.Kind != planexport.KindBoss && s.Kind != planexport.KindHold {
			continue
		}
		for _, c := range cs {
			if !zgHostile(c) || !slices.Contains(s.Entries, c.Entry) {
				continue
			}
			group := out.byLead[c.GroupLeader]
			if len(group) == 0 {
				group = []creature{c}
			}
			r := float64(s.Radius)
			if r == 0 {
				r = 25
			}
			for _, m := range group {
				dx, dy, dz := float64(s.X-m.Pos.X), float64(s.Y-m.Pos.Y), float64(s.Z-m.Pos.Z)
				if math.Sqrt(dx*dx+dy*dy+dz*dz) <= r {
					if _, ok := out.owner[c.GUID]; !ok {
						out.owner[c.GUID] = gi
					}
					break
				}
			}
		}
	}
	return out
}

// party is where the party stands for row i: a stand pull's stand (the move row right before it in the
// same leg), everything else the row's own point. It mirrors TortoiseBots (the party fights a stand pull
// at the stand) and route_aggro.go's routePoint.px.
func (r *zgW37Rows) party(i int) dungeon.Point {
	s := r.rows[i]
	if s.Kind == planexport.KindPull || s.Kind == planexport.KindBoss {
		if s.Flags&planexport.FlagStandPull != 0 && i > 0 {
			prev := r.rows[i-1]
			if prev.Leg == s.Leg && (prev.Kind == planexport.KindMove || prev.Kind == planexport.KindMoveDirect || prev.Kind == planexport.KindMoveHopExact) {
				return dungeon.Point{X: prev.X, Y: prev.Y, Z: prev.Z}
			}
		}
	}
	return dungeon.Point{X: s.X, Y: s.Y, Z: s.Z}
}

// standInShot says whether a stand-pull row's stand is inside the tank's shot of every unit of its pack,
// so PatrolStandPullGate pulls from the stand: a static unit out of the shot is left to the plain pull,
// which walks the party to the pack (DungeonRunPolicy.h standPullStatic).
func (r *zgW37Rows) standInShot(i int, cs []creature) (bool, string) {
	s := r.rows[i]
	if s.Flags&planexport.FlagStandPull == 0 {
		return false, "not a stand pull"
	}
	stand := r.party(i)
	worst, name := 0.0, ""
	for _, c := range cs {
		if !zgHostile(c) || r.owner[c.GUID] != i {
			continue
		}
		d := dist(stand, c.Pos)
		if d > worst {
			worst, name = d, c.Name
		}
	}
	if worst > zgW37TankShotYd {
		return false, fmt.Sprintf("its farthest pack unit (%s) is %.1f yd from the stand, past the %g yd shot: the row plain-pulls to the pack", name, worst, zgW37TankShotYd)
	}
	return true, ""
}

// zgW37Later returns the units a row i has not pulled yet: owned by a later row or by no row.
func (r *zgW37Rows) later(i int, cs []creature) []creature {
	var out []creature
	for _, c := range cs {
		if !zgHostile(c) {
			continue
		}
		if o, ok := r.owner[c.GUID]; ok && o <= i {
			continue
		}
		out = append(out, c)
	}
	return out
}

// TestZgW37JindoHonourGuardKeepsTheBossReach: walk 37 e/e2 (step 552, three wipes) and walk 36 f/f3/c3
// had the honour-guard row engage Jin'do: the stand pull's stand sat 35+ yd from its own pack, past the
// tank's 28.5 yd shot, so the row plain-pulled and walked the party to the pack 13 yd from him. The fix
// splits the camp (the 8-body formation 49421 and the two lone mistresses 49402/49403) so each row's
// stand is inside its pack's shot and out of Jin'do's reach (routeReach: 25 + the raid's 10 yd spread).
// The rows are the live export's: every move row of the leg before the boss approach and every pull
// or boss row keeps that reach from 11380.
func TestZgW37JindoHonourGuardKeepsTheBossReach(t *testing.T) {
	p, cs := zgLoadPlanAndExport(t)
	leg, legNo := zgW37LegByName(t, p, "jindo_the_hexxer")
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
	if math.Abs(bossReach-35) > 0.01 {
		t.Fatalf("Jin'do's reach is %.1f yd, want 35 (25 aggro at party level 58 + 10 raid spread)", bossReach)
	}
	rows := zgW37BuildRows(t, p, cs, legNo)
	// The boss row's own approach is exempt (it walks to the boss): stop at its first approach row.
	stop := len(rows.rows)
	for i, s := range rows.rows {
		if s.Kind == planexport.KindBoss && s.Entry == 11380 {
			stop = i
			for stop > 0 && rows.rows[stop-1].Kind == planexport.KindMove {
				stop--
			}
			break
		}
	}
	if stop == len(rows.rows) {
		t.Fatal("no boss row for 11380 in the exported leg 6")
	}
	for i := 0; i < stop; i++ {
		s := rows.rows[i]
		if s.Kind != planexport.KindPull && s.Kind != planexport.KindMove {
			continue
		}
		at := rows.party(i)
		if s.Kind == planexport.KindPull {
			// A stand pull whose stand cannot shoot the whole pack plain-pulls: the party walks to
			// the row's point (the pack's centroid) instead of standing at the stand.
			if ok, _ := rows.standInShot(i, cs); !ok {
				at = dungeon.Point{X: s.X, Y: s.Y, Z: s.Z}
			}
		}
		if d := dist(at, jindo.Pos); d < bossReach {
			t.Errorf("exported leg 6 row %d (%s %.1f %.1f %.1f) is %.1f yd from Jin'do 11380, want >= %g: the fight would wake him",
				i, s.Comment, at.X, at.Y, at.Z, d, bossReach)
		}
	}
	// The three guard rows are real stand pulls: the stand is inside the tank's shot of the whole pack
	// (else PatrolStandPullGate plain-pulls to the pack, which is what walked into Jin'do) and the fight
	// is 35+ yd from him.
	for _, want := range []struct {
		guid uint64
		name string
	}{{49421, "49421's guard formation"}, {49402, "the lone 49402"}, {49403, "the lone 49403"}} {
		owner := -1
		if o, ok := rows.owner[want.guid]; ok {
			owner = o
		}
		if owner < 0 {
			t.Errorf("no exported leg 6 pull row owns %s (guid %d): the pack is pulled by no step", want.name, want.guid)
			continue
		}
		if ok, why := rows.standInShot(owner, cs); !ok {
			t.Errorf("exported leg 6 row %d (%s) owns %s: %s", owner, rows.rows[owner].Comment, want.name, why)
		}
		if d := dist(rows.party(owner), jindo.Pos); d < bossReach {
			t.Errorf("exported leg 6 row %d (%s) owns %s and fights %.1f yd from Jin'do, want >= %g", owner, rows.rows[owner].Comment, want.name, d, bossReach)
		}
	}
	_ = leg
}

// TestZgW37PatrolPullsClearOfLaterPacks is the MANDOKIR-440 method on the five ZG patrol rows that were a
// plain pull at a roaming Berserker 11352's DB spawn 68-174 yd behind the row's stand (research/zg-w36/
// MANDOKIR-440.md "Also found": leg 1 groups 10/18/19, leg 3 group 3, leg 4 group 21): the exporter cut the
// hop into approach-path rows through the pack field. Each row is a stand pull now, scoped from a centre the
// run leader sees (<= dungeon.RunLeaderSeeYd), with a radius that holds the patroller's DB spawn (2D, as the
// run leader scopes it), and its stand inside the tank's shot of the patroller's loop: the party holds and the
// run leader shoots it when the loop comes round. The live export has no approach-path row for them, and the
// walk from the previous stand to theirs keeps 20 yd from every pack pulled later.
func TestZgW37PatrolPullsClearOfLaterPacks(t *testing.T) {
	p, cs := zgLoadPlanAndExport(t)
	for _, want := range []struct {
		leg   string
		group int
	}{
		{"high_priest_venoxis", 10}, {"high_priest_venoxis", 18}, {"high_priest_venoxis", 19},
		{"high_priest_thekal", 3}, {"high_priestess_arlokk", 21},
	} {
		leg, legNo := zgW37LegByName(t, p, want.leg)
		rows := zgW37BuildRows(t, p, cs, legNo)
		where := fmt.Sprintf("%s pull group %d", want.leg, want.group)
		g := &leg.PullGroups[want.group]
		if !g.StandPull {
			t.Errorf("%s: a patrol row is not a stand pull: the party walks to its DB spawn and the exporter cuts approach paths through the pack field", where)
			continue
		}
		if g.Patrol == nil || len(g.Patrol.Path) == 0 {
			t.Errorf("%s: no patrol block: the gate cannot wait for its loop", where)
			continue
		}
		if len(g.Approach) == 0 {
			t.Errorf("%s: a stand pull with no approach", where)
			continue
		}
		stand := g.Approach[len(g.Approach)-1]
		n := float32(len(g.Positions))
		var c dungeon.Point
		for _, q := range g.Positions {
			c.X += q.X
			c.Y += q.Y
			c.Z += q.Z
		}
		c.X, c.Y, c.Z = c.X/n, c.Y/n, c.Z/n
		if d := dist(c, stand); d > dungeon.RunLeaderSeeYd {
			t.Errorf("%s: the centre %.1f %.1f is %.1f yd from the stand, want <= %d (the run leader's sight)", where, c.X, c.Y, d, dungeon.RunLeaderSeeYd)
		}
		// The row's own patroller (the movement file its patrol block was read from names its guid) must lie
		// inside the radius in 2D, as TB's ScopeStandPullFacts scopes it -- not just some nearer 11352.
		var guid uint64
		if m := regexp.MustCompile(`creature_movement (\d+)`).FindStringSubmatch(g.Patrol.Source); m != nil {
			guid, _ = strconv.ParseUint(m[1], 10, 64)
		}
		if guid == 0 {
			t.Fatalf("%s: the patrol block does not name its movement file", where)
		}
		dir := -1
		for i := range cs {
			if cs[i].GUID == guid {
				dir = i
			}
		}
		if dir < 0 {
			t.Fatalf("%s: patroller guid %d is not in the map export", where, guid)
		}
		if d := math.Hypot(float64(cs[dir].Pos.X-c.X), float64(cs[dir].Pos.Y-c.Y)); d > float64(g.Radius) {
			t.Errorf("%s: its patroller %d is %.1f yd from the centre, past radius %g: the row owns nothing", where, guid, d, g.Radius)
		}
		loop := math.Inf(1)
		for _, q := range g.Patrol.Path {
			if d := dist(stand, q); d < loop {
				loop = d
			}
		}
		if loop > zgW37TankShotYd {
			t.Errorf("%s: the stand is %.1f yd from the nearest waypoint of its patroller's loop, past the %g yd shot: the gate never fires", where, loop, zgW37TankShotYd)
		}
		// The live export: the row right before the pull is the stand (no approach-path row was cut), and the
		// walk from the previous stand keeps 20 yd from every pack pulled later.
		found := -1
		for i := range rows.rows {
			s := rows.rows[i]
			if s.Kind == planexport.KindPull && strings.HasPrefix(s.Comment, fmt.Sprintf("pull group %d", want.group)) {
				found = i
			}
		}
		if found < 0 {
			t.Errorf("%s: no exported pull row", where)
			continue
		}
		if prev := found - 1; prev < 0 || dist(dungeon.Point{X: rows.rows[prev].X, Y: rows.rows[prev].Y, Z: rows.rows[prev].Z}, stand) > 0.01 {
			t.Errorf("%s: the exported row right before the pull is not the stand (an approach path was cut)", where)
		}
		if found > 0 {
			from := rows.party(found - 1)
			later := rows.later(found, cs)
			for _, q := range zgSamplesAlong(from, stand, zgSampleStep) {
				if d, who := zgMin3(q, later); d < zgLaterYd {
					t.Errorf("%s: the walk from the previous stand passes %.1f yd from %s %d at %.1f %.1f, want >= %g from every pack pulled later", where, d, who.Name, who.Entry, q.X, q.Y, zgLaterYd)
					break
				}
			}
		}
	}
}
