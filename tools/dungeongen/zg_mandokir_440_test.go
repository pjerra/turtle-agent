// Walk-36 Mandokir wipes at steps 440-454 (research/zg-w36/MANDOKIR-440.md): the
// group-0 patrol's plain pull row stood at the patroller's DB spawn, 118 yd
// from its stand, so the exporter's --approach-hops cut the hop into an
// "approach path" row at -12287.3 -1835.9, 9.0 yd from group 2's priest, and
// the walk to the spawn passed 1.7-3.9 yd from groups 2 and 4; and group 5's
// stand at -12320 -1858 stands 28.9 yd from group 12's Blood Drinkers, so the
// 11-body chain fight pulls them in. The fix is plan data: group 0 is a stand
// pull from its stand, group 1's mid-walk approach point is deleted, and
// group 5's stand moves to -12300 -1868. These tests pin that.
package main

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// zgMandokirLeg is leg 5 (bloodlord_mandokir) of the ZG plan.
func zgMandokirLeg(t *testing.T, p *dungeon.Plan) *dungeon.Leg {
	t.Helper()
	for i := range p.Legs {
		if p.Legs[i].Name == "bloodlord_mandokir" {
			return &p.Legs[i]
		}
	}
	t.Fatal("no bloodlord_mandokir leg")
	return nil
}

// zgMandokirSpawn finds a spawn by guid in the map-309 creature fixture.
func zgMandokirSpawn(t *testing.T, cs []creature, guid uint64) creature {
	t.Helper()
	for _, c := range cs {
		if c.GUID == guid {
			return c
		}
	}
	t.Fatalf("no spawn %d in the creature fixture", guid)
	return creature{}
}

// TestZulGurubMandokirPatrollerIsStandPull: pull group 0 (the patrolling
// Berserker, formation 49705) is a stand pull scoped from a centre the run
// leader can see, so the exporter cuts no approach-path row through the pack
// field and the fight happens at the stand. Fails on the walk-36 plan ("pull
// group 0 is not a stand pull").
func TestZulGurubMandokirPatrollerIsStandPull(t *testing.T) {
	p, cs := zgLoadPlanAndExport(t)
	leg := zgMandokirLeg(t, p)
	if len(leg.PullGroups) == 0 {
		t.Fatal("no pull groups")
	}
	g := leg.PullGroups[0]
	if !g.StandPull {
		t.Fatal("pull group 0 is not a stand pull")
	}
	if len(g.Approach) == 0 {
		t.Fatal("pull group 0 has no stand (approach point)")
	}
	stand := g.Approach[len(g.Approach)-1]
	// The centre (the pull row's point: the mean of the group's positions) is
	// within the run leader's 60 yd sight of the stand; plan validation
	// (internal/dungeon/plan.go) enforces the same bound, so the run leader
	// sees the row's point from the stand.
	var cx, cy, cz float64
	for _, q := range g.Positions {
		cx, cy, cz = cx+float64(q.X), cy+float64(q.Y), cz+float64(q.Z)
	}
	n := float64(len(g.Positions))
	cen := dungeon.Point{X: float32(cx / n), Y: float32(cy / n), Z: float32(cz / n)}
	if d := dist(stand, cen); d > dungeon.RunLeaderSeeYd {
		t.Errorf("pull group 0: the stand %.1f %.1f %.1f is %.1f yd from the row's point, want <= %g (the run leader sees it from there)",
			stand.X, stand.Y, stand.Z, d, float64(dungeon.RunLeaderSeeYd))
	}
	// The DB spawn of 49705 is within the radius (TB adds a yard of its own),
	// and every other 11352 spawn on the map is outside the radius plus 5
	// (the row would otherwise own another pack's Berserker).
	sp := zgMandokirSpawn(t, cs, 49705)
	if d := dist(cen, sp.Pos); d > float64(g.Radius)+1 {
		t.Errorf("pull group 0: the centre %.1f %.1f %.1f is %.1f yd from 49705's spawn, outside radius %g + 1 (the row would not own the patroller)",
			cen.X, cen.Y, cen.Z, d, g.Radius)
	}
	for _, c := range cs {
		if c.Entry != 11352 || c.GUID == 49705 {
			continue
		}
		if d := dist(cen, c.Pos); d <= float64(g.Radius)+5 {
			t.Errorf("pull group 0: the centre %.1f %.1f %.1f is %.1f yd from the other 11352 spawn %d, inside radius %g + 5 (the row would own it too)",
				cen.X, cen.Y, cen.Z, d, c.GUID, g.Radius)
		}
	}
	// The export (the plain build: the stands are explicit, so nothing is
	// cut): the pull group 0 row carries flag 8192, the row right before it
	// is the move at the group-0 stand, and the next row is the move to the
	// group-1 stand (no "path"/"approach path" row between).
	res, err := planexport.Build(p, planexport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var leg5 []planexport.StepRow
	for _, s := range res.Rows.Steps {
		if s.Leg == 5 {
			leg5 = append(leg5, s)
		}
	}
	pullAt := -1
	for i, s := range leg5 {
		if s.Kind == planexport.KindPull && strings.HasPrefix(s.Comment, "pull group 0") {
			pullAt = i
			break
		}
	}
	if pullAt < 0 {
		t.Fatal("no pull row for group 0 in the exported leg 5")
	}
	if row := leg5[pullAt]; row.Flags&planexport.FlagStandPull == 0 {
		t.Errorf("the pull group 0 row has flags %d, want flag %d (stand pull)", row.Flags, planexport.FlagStandPull)
	}
	if pullAt == 0 {
		t.Fatal("the pull group 0 row is the leg's first row: no stand move before it")
	}
	if before := leg5[pullAt-1]; before.Kind != planexport.KindMove || dist(dungeon.Point{X: before.X, Y: before.Y, Z: before.Z}, stand) > 0.01 {
		t.Errorf("the row before the pull group 0 row is %q %.1f %.1f %.1f, want the move at the stand %.1f %.1f %.1f",
			before.Comment, before.X, before.Y, before.Z, stand.X, stand.Y, stand.Z)
	}
	if pullAt+1 >= len(leg5) {
		t.Fatal("no row after the pull group 0 row")
	}
	after := leg5[pullAt+1]
	g1stand := leg.PullGroups[1].Approach[len(leg.PullGroups[1].Approach)-1]
	if after.Kind != planexport.KindMove || dist(dungeon.Point{X: after.X, Y: after.Y, Z: after.Z}, g1stand) > 0.01 {
		t.Errorf("the row after the pull group 0 row is %q %.1f %.1f %.1f, want the move to the group-1 stand %.1f %.1f %.1f (a cut row stands between)",
			after.Comment, after.X, after.Y, after.Z, g1stand.X, g1stand.Y, g1stand.Z)
	}
}

// zgMandokirExportCut is plan-export's own cutter
// (cmd/turtle-agent plan_export_cmd.go cutWith, the --floor-aware route kept
// in step with it): the floor-aware walk of the navmesh path from a to b,
// with the wet points dropped. The live export (plan-export --floor-aware
// --approach-hops --mmaps) cuts the hops of the Mandokir leg with it; the
// check below runs the same cut, so a row the walk would stand on fails here
// first.
func zgMandokirExportCut(pl *navmesh.Planner) planexport.CutFunc {
	return func(mapID uint32, from, to dungeon.Point) ([]dungeon.Point, []planexport.CutNote, error) {
		path, err := pl.Plan(mapID, navmesh.Vec3{from.X, from.Y, from.Z}, navmesh.Vec3{to.X, to.Y, to.Z})
		if err != nil {
			return nil, nil, err
		}
		if path.Status != navmesh.Complete || len(path.Points) < 2 {
			return nil, nil, fmt.Errorf("no complete path (%s %s)", path.Status, path.Reason)
		}
		via, notes, err := mandokirFloorAwareCut(pl, mapID, path)
		if err != nil {
			return nil, notes, err
		}
		return mandokirDropWetCut(pl, mapID, via, &notes)
	}
}

// TestZulGurubMandokirLiveExportClearOfLaterPacks: the leg-5 export with the
// live flags (Options{ApproachHops: true, Cut: the navmesh cutter} as
// plan_export_cmd.go builds it with --floor-aware) keeps every move row and
// every row-to-row segment from the group-0 stand through the group-5 pull
// >= 20 yd from every pack not yet pulled (the pack the row pulls excluded).
// Fails on the walk-36 plan at the approach-path row (9.0 yd from 49780).
// The existing sweep test (zgParties, Options{}) cannot see this: it models
// every pull as "the party stands at the last approach point", so neither the
// approach-path row the live export adds nor the walk to a far row point is
// ever checked.
func TestZulGurubMandokirLiveExportClearOfLaterPacks(t *testing.T) {
	p, cs := zgLoadPlanAndExport(t)
	leg := zgMandokirLeg(t, p)
	packs := zgPacks(t, *leg, cs)
	pl := zgPlannerOrNil(t)
	if pl == nil {
		t.Skip("no map 309 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 309.mmap and its tiles")
	}
	defer pl.Close()
	res, err := planexport.Build(p, planexport.Options{ApproachHops: true, Cut: zgMandokirExportCut(pl)})
	if err != nil {
		t.Fatal(err)
	}
	var rows []planexport.StepRow
	for _, s := range res.Rows.Steps {
		if s.Leg == 5 {
			rows = append(rows, s)
		}
	}
	// From the group-0 stand's move row through the group-5 pull row.
	start, end := -1, -1
	g0stand := leg.PullGroups[0].Approach[len(leg.PullGroups[0].Approach)-1]
	for i, s := range rows {
		if s.Kind == planexport.KindMove && dist(dungeon.Point{X: s.X, Y: s.Y, Z: s.Z}, g0stand) < 0.01 && start < 0 {
			start = i
		}
		if s.Kind == planexport.KindPull && strings.HasPrefix(s.Comment, "pull group 5") {
			end = i
			break
		}
	}
	if start < 0 {
		t.Fatal("no move row at the group-0 stand in the exported leg 5")
	}
	if end < 0 {
		t.Fatal("no pull row for group 5 in the exported leg 5")
	}
	if start > end {
		t.Fatalf("the group-0 stand row %d comes after the group-5 pull row %d", start, end)
	}
	pulled := map[int]bool{}
	for i := start; i <= end; i++ {
		s := rows[i]
		at := dungeon.Point{X: s.X, Y: s.Y, Z: s.Z}
		if s.Kind == planexport.KindPull || s.Kind == planexport.KindBoss || s.Kind == planexport.KindHold {
			if gi := zgPullGroupIndex(s.Comment); gi >= 0 {
				pulled[gi] = true
			}
			// A pull row's own point is where its pack stands: the row
			// pulls it, so it is not a later pack's wake.
			continue
		}
		here := zgPullGroupIndex(s.Comment)
		var later []creature
		for gi := range packs {
			if gi == here || pulled[gi] {
				continue
			}
			later = append(later, packs[gi]...)
		}
		// The row's own point: a move row is a stand the party stops at,
		// so any later pack within 20 yd wakes before its pull row.
		if len(later) > 0 {
			if d, who := zgMin3(at, later); d < zgLaterYd {
				t.Errorf("exported leg 5 row %d (%s %.1f %.1f) is %.1f yd from %s %d, want >= %g from every pack pulled later",
					i, s.Comment, s.X, s.Y, d, who.Name, who.Entry, zgLaterYd)
			}
		}
		// The segment to the next row keeps the next row's own pack out
		// too: the party walks it before the next row's pull goes out (the
		// pack the next row approaches wakes mid-walk: walk 29's fix 4 on
		// leg 4).
		if i+1 > end {
			continue
		}
		n := rows[i+1]
		to := dungeon.Point{X: n.X, Y: n.Y, Z: n.Z}
		nHere := zgPullGroupIndex(n.Comment)
		var nLater []creature
		for gi := range packs {
			if gi == nHere || pulled[gi] {
				continue
			}
			nLater = append(nLater, packs[gi]...)
		}
		if len(nLater) == 0 {
			continue
		}
		for _, q := range zgSamplesAlong(at, to, zgSampleStep) {
			if d, who := zgMin3(q, nLater); d < zgLaterYd {
				t.Errorf("exported leg 5 rows %d (%s) -> %d (%s) pass %.1f yd from %s %d at %.1f %.1f, want >= %g from every pack pulled later",
					i, s.Comment, i+1, n.Comment, d, who.Name, who.Entry, q.X, q.Y, zgLaterYd)
				break
			}
		}
	}
}

// mandokirFloorAwareSeg is the 3D length of one cutter segment.
func mandokirFloorAwareSeg(a, b navmesh.Vec3) float64 {
	dx, dy, dz := float64(b[0]-a[0]), float64(b[1]-a[1]), float64(b[2]-a[2])
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

// mandokirFloorAwareCut is the floor-aware cutter of cmd/turtle-agent
// plan_export_cmd.go (floorAwareCut, kept in step with it): the path's
// corners are merged (under FloorAwareMergeYd), unneeded (a turn under
// FloorAwareTurnDeg) or pushed FloorAwarePushYd off the nearest wall, then
// the kept points are joined in hops of at most FloorAwareHopYd, every split
// point taking its z from the floor and dropped when there is no ground
// within FloorAwareFloorYd of the straight line's z.
func mandokirFloorAwareCut(pl *navmesh.Planner, mapID uint32, path navmesh.Path) ([]dungeon.Point, []planexport.CutNote, error) {
	var notes []planexport.CutNote
	kept := []navmesh.Vec3{path.Points[0]}
	for i := 1; i < len(path.Points)-1; i++ {
		p := path.Points[i]
		prev := kept[len(kept)-1]
		next := path.Points[i+1]
		if turn := mandokirTurnDeg(path.Points[i-1], p, next); turn < planexport.FloorAwareTurnDeg && mandokirCanDrop(pl, mapID, prev, next) {
			notes = append(notes, planexport.CutNote{Old: mandokirCutPoint(p), Dropped: true, Reason: fmt.Sprintf("the turn is %.0f degrees (under %g): the walk does not need a row here", turn, planexport.FloorAwareTurnDeg)})
			continue
		}
		if d := mandokirFloorAwareSeg(prev, p); d < planexport.FloorAwareMergeYd && mandokirCanDrop(pl, mapID, prev, next) {
			notes = append(notes, planexport.CutNote{Old: mandokirCutPoint(p), Dropped: true, Reason: fmt.Sprintf("%.1f yd from the point before it (under %g): one arrival radius covers both", d, planexport.FloorAwareMergeYd)})
			continue
		}
		q, pushed, why := mandokirPushOffWall(pl, mapID, p)
		if why != "" {
			notes = append(notes, planexport.CutNote{Old: mandokirCutPoint(p), New: mandokirCutPoint(q), Reason: why})
		} else if pushed {
			notes = append(notes, planexport.CutNote{Old: mandokirCutPoint(p), New: mandokirCutPoint(q), Reason: fmt.Sprintf("pushed %g yd off the nearest wall", planexport.FloorAwarePushYd)})
		}
		kept = append(kept, q)
	}
	kept = append(kept, path.Points[len(path.Points)-1])
	var via []dungeon.Point
	for i := 1; i < len(kept); i++ {
		a, b := kept[i-1], kept[i]
		d := mandokirFloorAwareSeg(a, b)
		if d > planexport.FloorAwareHopYd {
			n := int(math.Ceil(d / planexport.FloorAwareHopYd))
			for k := 1; k < n; k++ {
				f := float64(k) / float64(n)
				x, y := a[0]+(b[0]-a[0])*float32(f), a[1]+(b[1]-a[1])*float32(f)
				meanZ := a[2] + (b[2]-a[2])*float32(f)
				floor, ok, err := mandokirSplitFloor(pl, mapID, x, y, meanZ)
				if err != nil {
					return nil, notes, err
				}
				if !ok {
					notes = append(notes, planexport.CutNote{Old: dungeon.Point{X: x, Y: y, Z: meanZ}, Dropped: true,
						Reason: fmt.Sprintf("no ground within %g yd of %.1f yd (the straight line crosses a dip or a liquid bed): the hop stays %.0f yd", planexport.FloorAwareFloorYd, meanZ, d)})
					continue
				}
				via = append(via, dungeon.Point{X: x, Y: y, Z: floor.Z})
			}
		}
		if i < len(kept)-1 {
			via = append(via, mandokirCutPoint(kept[i]))
		}
	}
	return via, notes, nil
}

// mandokirDropWetCut drops the cut points that stand in or under a liquid
// (cmd/turtle-agent plan_export_cmd.go dropWetCut, kept in step with it).
func mandokirDropWetCut(pl *navmesh.Planner, mapID uint32, via []dungeon.Point, notes *[]planexport.CutNote) ([]dungeon.Point, []planexport.CutNote, error) {
	if len(via) == 0 {
		return via, *notes, nil
	}
	out := make([]dungeon.Point, 0, len(via))
	for _, p := range via {
		liq, err := navmesh.LiquidAt(pl, mapID, navmesh.Vec3{p.X, p.Y, p.Z})
		if err != nil {
			return nil, *notes, err
		}
		if liq.Wet {
			what := "the point's own floor is NavWater"
			if !liq.Own {
				what = fmt.Sprintf("a NavWater floor stands %.1f yd above it within %g yd", liq.Z-p.Z, navmesh.LiquidNearYd)
			}
			*notes = append(*notes, planexport.CutNote{Old: p, Dropped: true,
				Reason: fmt.Sprintf("%s: the water is crossed in one hop between dry points", what)})
			continue
		}
		out = append(out, p)
	}
	return out, *notes, nil
}

// mandokirPushOffWall moves a path corner FloorAwarePushYd away from the
// nearest mesh edge it can see within FloorAwareEdgeScanYd (cmd/turtle-agent
// plan_export_cmd.go pushOffWall, kept in step with it).
func mandokirPushOffWall(pl *navmesh.Planner, mapID uint32, p navmesh.Vec3) (navmesh.Vec3, bool, string) {
	dirs := 16
	best, bestR := -1, float32(-1)
	for k := range dirs {
		a := 2 * math.Pi * float64(k) / float64(dirs)
		q := navmesh.Vec3{p[0] + planexport.FloorAwareEdgeScanYd*float32(math.Cos(a)), p[1] + planexport.FloorAwareEdgeScanYd*float32(math.Sin(a)), p[2]}
		ray, err := pl.Raycast(mapID, p, q)
		if err != nil || !ray.Known {
			return p, false, "the mesh cannot measure the wall at the corner: the original is kept"
		}
		if !ray.Clear && (bestR < 0 || ray.Hit < bestR) {
			best, bestR = k, ray.Hit
		}
	}
	if best < 0 {
		return p, false, "" // open ground: no edge to push off
	}
	a := 2 * math.Pi * float64(best) / float64(dirs)
	q := navmesh.Vec3{p[0] - planexport.FloorAwarePushYd*float32(math.Cos(a)), p[1] - planexport.FloorAwarePushYd*float32(math.Sin(a)), p[2]}
	n, ok, err := pl.Nearest(mapID, q)
	if err != nil || !ok {
		return p, false, "no walkable mesh within 4 yd of the pushed point: the original is kept"
	}
	floor, ok, err := mandokirGroundFloor(pl, mapID, n)
	if err != nil || !ok || math.Abs(float64(floor.Z-n[2])) > planexport.FloorAwareFloorYd ||
		floor.Flags&navmesh.NavGround == 0 || floor.Flags&(navmesh.NavWater|navmesh.NavSteep|navmesh.NavMagma|navmesh.NavSlime) != 0 {
		return p, false, fmt.Sprintf("the point %g yd off the wall is not plain ground (water, steep or lava): the original is kept", planexport.FloorAwarePushYd)
	}
	return n, true, ""
}

// mandokirSplitFloor is the floor a split point takes its z from: the
// highest plain ground within FloorAwareFloorYd of meanZ at (x, y)
// (cmd/turtle-agent plan_export_cmd.go splitFloor, kept in step with it).
func mandokirSplitFloor(pl *navmesh.Planner, mapID uint32, x, y, meanZ float32) (navmesh.Floor, bool, error) {
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

// mandokirGroundFloor is the floor at p nearest p's z (cmd/turtle-agent
// plan_export_cmd.go groundFloor, kept in step with it).
func mandokirGroundFloor(pl *navmesh.Planner, mapID uint32, p navmesh.Vec3) (navmesh.Floor, bool, error) {
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

// mandokirCanDrop says the walk from a to b may skip the corner between them
// (cmd/turtle-agent plan_export_cmd.go canDrop, kept in step with it).
func mandokirCanDrop(pl *navmesh.Planner, mapID uint32, a, b navmesh.Vec3) bool {
	d := mandokirFloorAwareSeg(a, b)
	if d > planexport.FloorAwareHopYd {
		return false
	}
	ray, err := pl.Raycast(mapID, a, b)
	if err != nil || !ray.Known || !ray.Clear && float64(ray.Hit) < d-1 {
		return false
	}
	x, y := (a[0]+b[0])/2, (a[1]+b[1])/2
	meanZ := (a[2] + b[2]) / 2
	_, ok, err := mandokirSplitFloor(pl, mapID, x, y, meanZ)
	return err == nil && ok
}

// mandokirTurnDeg is the 3D change of direction at b (cmd/turtle-agent
// plan_export_cmd.go turnDeg, kept in step with it).
func mandokirTurnDeg(a, b, c navmesh.Vec3) float64 {
	u := navmesh.Vec3{b[0] - a[0], b[1] - a[1], b[2] - a[2]}
	v := navmesh.Vec3{c[0] - b[0], c[1] - b[1], c[2] - b[2]}
	lu, lv := mandokirFloorAwareSeg(a, b), mandokirFloorAwareSeg(b, c)
	if lu == 0 || lv == 0 {
		return 180
	}
	dot := float64(u[0])*float64(v[0]) + float64(u[1])*float64(v[1]) + float64(u[2])*float64(v[2])
	cos := math.Max(-1, math.Min(1, dot/(lu*lv)))
	return math.Acos(cos) * 180 / math.Pi
}

func mandokirCutPoint(v navmesh.Vec3) dungeon.Point { return dungeon.Point{X: v[0], Y: v[1], Z: v[2]} }
