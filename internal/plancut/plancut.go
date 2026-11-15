// Package plancut is plan-export's cutter: it turns one hop between two rows into the via points the exporter writes, along
// the server's own navmesh (navmesh.Planner), as the migration's move rows. The cutter is shared by plan-export, plan-check
// (the lints judge the rows the export writes) and the plan tests that check the deployed cut, so the rows they see are one
// source.
package plancut

import (
	"fmt"
	"math"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// Planner is the offline planner Cut asks: navmesh.Planner is one, a test fake the other.
type Planner interface {
	Plan(mapID uint32, from, to navmesh.Vec3) (navmesh.Path, error)
}

// Cut cuts a hop along the navmesh path. floorAware (plan-export's --floor-aware) keeps a hop of up to
// planexport.FloorAwareHopYd whole, drops the corners a walk gains nothing from, puts the kept corners off the wall, and takes a
// split point's z from the navmesh floor instead of the straight line between the corners.
func Cut(pl Planner, floorAware bool) planexport.CutFunc {
	return func(mapID uint32, from, to dungeon.Point) ([]dungeon.Point, []planexport.CutNote, error) {
		path, err := pl.Plan(mapID, navmesh.Vec3{from.X, from.Y, from.Z}, navmesh.Vec3{to.X, to.Y, to.Z})
		if err != nil {
			return nil, nil, err
		}
		if path.Status != navmesh.Complete || len(path.Points) < 2 {
			return nil, nil, fmt.Errorf("no complete path (%s %s)", path.Status, path.Reason)
		}
		if floorAware {
			if real, ok := pl.(*navmesh.Planner); ok {
				via, notes, err := floorAwareCut(real, mapID, path)
				if err != nil {
					return nil, notes, err
				}
				via, notes, err = dropWetCut(real, mapID, via, &notes)
				if err != nil {
					return nil, notes, err
				}
				return seamFix(pl, mapID, from, via, &notes), notes, nil
			}
		}
		var via []dungeon.Point
		prev := path.Points[0]
		for i := 1; i < len(path.Points); i++ {
			p := path.Points[i]
			d := math.Sqrt(float64(sq32(p[0]-prev[0]) + sq32(p[1]-prev[1]) + sq32(p[2]-prev[2])))
			// A corner is a point of the route; a long stretch between two corners is split into equal hops.
			for k := 1; float64(k)*planexport.MoveGapCutYd < d; k++ {
				f := float32(float64(k) * planexport.MoveGapCutYd / d)
				via = append(via, dungeon.Point{X: prev[0] + (p[0]-prev[0])*f, Y: prev[1] + (p[1]-prev[1])*f, Z: prev[2] + (p[2]-prev[2])*f})
			}
			if i < len(path.Points)-1 {
				via = append(via, dungeon.Point{X: p[0], Y: p[1], Z: p[2]})
			}
			prev = p
		}
		var notes []planexport.CutNote
		if real, ok := pl.(*navmesh.Planner); ok {
			via, notes, err = dropWetCut(real, mapID, via, &notes)
			if err != nil {
				return nil, notes, err
			}
		}
		return seamFix(pl, mapID, from, via, &notes), notes, nil
	}
}

// serverQuery is the server's own path query, when the planner answers it: *navmesh.Planner does, and a test fake may.
type serverQuery interface {
	ServerPath(mapID uint32, from, to navmesh.Vec3) (navmesh.ServerPath, error)
}

// seamFix moves a cut point the server answers NOPATH for off the mmaps cell line it stands on (planexport.OnSeam): the
// server refuses a hop whose goal is exactly on a cell line, and the 0.5 yd move toward the point the hop came from clears it
// (route audit 2026-11-12, finding 3: plan-probe cleared 13 of the 14 listed rows; naxxramas step 637 stayed NOPATH, a steep
// ledge). Only a point the server refuses is touched, and the moved point is kept only when it stands on plain floor: the
// floor-aware z lookup (splitFloor) takes its z at the new XY, and the server then answers a normal path whose verdict is ok
// and whose ActualEnd lies within the run leader's 1 yd arrival tolerance of that point. A seam the server walks, a seam the
// hop runs along, one whose move it refuses too, one with no plain floor at the new XY, one the server paths only
// incompletely or ends short of all stay as the cutter wrote them. The hop to a point starts at the cut point before it, or at
// the point the cut began.
func seamFix(pl Planner, mapID uint32, from dungeon.Point, via []dungeon.Point, notes *[]planexport.CutNote) []dungeon.Point {
	q, ok := pl.(serverQuery)
	floors, floorOK := pl.(navmesh.FloorReader)
	if !ok || !floorOK || len(via) == 0 {
		return via
	}
	prev := from
	for i, v := range via {
		if planexport.OnSeam(v.X) || planexport.OnSeam(v.Y) {
			if n := planexport.OffSeam(prev, v); n != v && seamRefused(q, mapID, prev, v) {
				f, onFloor, err := splitFloor(floors, mapID, n.X, n.Y, v.Z)
				if err == nil && onFloor && seamReached(q, mapID, prev, dungeon.Point{X: n.X, Y: n.Y, Z: f.Z}) {
					n.Z = f.Z
					*notes = append(*notes, planexport.CutNote{Old: v, New: n,
						Reason: fmt.Sprintf("the point stood on a mmaps cell line and the server answered the hop NOPATH: moved %g yd off it", planexport.SeamNudgeYd)})
					via[i] = n
					v = n
				}
			}
		}
		prev = v
	}
	return via
}

// seamRefused says the server answers the hop from-to with NOPATH. A query that fails cannot tell, and no point is moved on it.
func seamRefused(q serverQuery, mapID uint32, from, to dungeon.Point) bool {
	sp, err := q.ServerPath(mapID, navmesh.Vec3{from.X, from.Y, from.Z}, navmesh.Vec3{to.X, to.Y, to.Z})
	return err == nil && sp.Type&navmesh.PathNoPath != 0
}

// seamReached says the server reaches the point: the query answers a normal path (not an incomplete one), its verdict is ok,
// and the path's ActualEnd lies within the run leader's 1 yd arrival tolerance of the point. A failed query, an incomplete
// path and an end off the point all say no, and the point is not moved.
func seamReached(q serverQuery, mapID uint32, from, to dungeon.Point) bool {
	f, t := navmesh.Vec3{from.X, from.Y, from.Z}, navmesh.Vec3{to.X, to.Y, to.Z}
	sp, err := q.ServerPath(mapID, f, t)
	if err != nil || sp.Type&navmesh.PathNormal == 0 || sp.Type&navmesh.PathIncomplete != 0 {
		return false
	}
	return navmesh.ServerMoveVerdict(sp, f, t, 0) == "ok" && cutDist(sp.ActualEnd, t) <= float64(navmesh.AtLeadYd)
}

// dropWetCut drops the cut points that stand in or under a liquid: the plan's move rows are the points the party stops at, and a
// water stop is the audit's water rule (a chase or a fall lands in water, and the run leader's own walk can stall there). The
// water stays a crossing: the hop between the dry points either side is one hop, which the run leader's own path takes.
func dropWetCut(pl *navmesh.Planner, mapID uint32, via []dungeon.Point, notes *[]planexport.CutNote) ([]dungeon.Point, []planexport.CutNote, error) {
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

// floorAwareCut is Cut's --floor-aware route: the path's corners are merged (under FloorAwareMergeYd), unneeded (a turn under
// FloorAwareTurnDeg) or pushed FloorAwarePushYd off the nearest wall, then the kept points are joined in hops of at most
// planexport.FloorAwareHopYd, every split point taking its z from the floor and dropped when there is no ground within
// FloorAwareFloorYd of the straight line's z.
func floorAwareCut(pl *navmesh.Planner, mapID uint32, path navmesh.Path) ([]dungeon.Point, []planexport.CutNote, error) {
	var notes []planexport.CutNote
	kept := []navmesh.Vec3{path.Points[0]}
	for i := 1; i < len(path.Points)-1; i++ {
		p := path.Points[i]
		prev := kept[len(kept)-1]
		next := path.Points[i+1]
		if turn := turnDeg(path.Points[i-1], p, next); turn < planexport.FloorAwareTurnDeg && canDrop(pl, mapID, prev, next) {
			notes = append(notes, planexport.CutNote{Old: cutPoint(p), Dropped: true, Reason: fmt.Sprintf("the turn is %.0f degrees (under %g): the walk does not need a row here", turn, planexport.FloorAwareTurnDeg)})
			continue
		}
		if d := cutDist(prev, p); d < planexport.FloorAwareMergeYd && canDrop(pl, mapID, prev, next) {
			notes = append(notes, planexport.CutNote{Old: cutPoint(p), Dropped: true, Reason: fmt.Sprintf("%.1f yd from the point before it (under %g): one arrival radius covers both", d, planexport.FloorAwareMergeYd)})
			continue
		}
		q, pushed, why := pushOffWall(pl, mapID, p)
		if why != "" {
			notes = append(notes, planexport.CutNote{Old: cutPoint(p), New: cutPoint(q), Reason: why})
		} else if pushed {
			notes = append(notes, planexport.CutNote{Old: cutPoint(p), New: cutPoint(q), Reason: fmt.Sprintf("pushed %g yd off the nearest wall", planexport.FloorAwarePushYd)})
		}
		kept = append(kept, q)
	}
	kept = append(kept, path.Points[len(path.Points)-1])
	var via []dungeon.Point
	for i := 1; i < len(kept); i++ {
		a, b := kept[i-1], kept[i]
		d := cutDist(a, b)
		if d > planexport.FloorAwareHopYd {
			n := int(math.Ceil(float64(d) / planexport.FloorAwareHopYd))
			for k := 1; k < n; k++ {
				f := float64(k) / float64(n)
				x, y := a[0]+(b[0]-a[0])*float32(f), a[1]+(b[1]-a[1])*float32(f)
				meanZ := a[2] + (b[2]-a[2])*float32(f)
				floor, ok, err := splitFloor(pl, mapID, x, y, meanZ)
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
			via = append(via, cutPoint(kept[i]))
		}
	}
	return via, notes, nil
}

// pushOffWall moves a path corner FloorAwarePushYd away from the nearest mesh edge it can see within FloorAwareEdgeScanYd, snaps it
// to the mesh and checks it is plain ground (not water, a steep slope or lava). It reports whether it moved, and why not when it
// should have (the caller keeps the original then).
func pushOffWall(pl *navmesh.Planner, mapID uint32, p navmesh.Vec3) (navmesh.Vec3, bool, string) {
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
	// Away from the nearest edge is the opposite of the ray that hit it.
	a := 2 * math.Pi * float64(best) / float64(dirs)
	q := navmesh.Vec3{p[0] - planexport.FloorAwarePushYd*float32(math.Cos(a)), p[1] - planexport.FloorAwarePushYd*float32(math.Sin(a)), p[2]}
	n, ok, err := pl.Nearest(mapID, q)
	if err != nil || !ok {
		return p, false, "no walkable mesh within 4 yd of the pushed point: the original is kept"
	}
	floor, ok, err := groundFloor(pl, mapID, n)
	if err != nil || !ok || math.Abs(float64(floor.Z-n[2])) > planexport.FloorAwareFloorYd ||
		floor.Flags&navmesh.NavGround == 0 || floor.Flags&(navmesh.NavWater|navmesh.NavSteep|navmesh.NavMagma|navmesh.NavSlime) != 0 {
		return p, false, fmt.Sprintf("the point %g yd off the wall is not plain ground (water, steep or lava): the original is kept", planexport.FloorAwarePushYd)
	}
	return n, true, ""
}

// splitFloor is the floor a split point, or a seam nudge's moved point, takes its z from: the highest plain ground within
// FloorAwareFloorYd of meanZ at (x, y).
func splitFloor(pl navmesh.FloorReader, mapID uint32, x, y, meanZ float32) (navmesh.Floor, bool, error) {
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

// floorAtYd is how far above or below the lead point FloorAt looks for the floor: the 6 yd up and down of the mesh's nearest
// query, which planlint's off-floor rule names.
const floorAtYd = 6

// FloorAt answers the exporter's Options.Floor: the plain ground at p's x and y nearest its z (within floorAtYd), with p's x
// and y kept. The lead row's z comes from a straight line that can run several yards under a ramp or a ledge, and a split
// point's stricter FloorAwareFloorYd band would drop the row there; the floor under the point is what the client stands on (the
// server's own move to it ends at the same z). ok is false when there is no plain ground there.
func FloorAt(pl navmesh.FloorReader) planexport.FloorFunc {
	return func(mapID uint32, p dungeon.Point) (dungeon.Point, bool) {
		fl, err := pl.Floors(mapID, p.X, p.Y, p.Z-floorAtYd, p.Z+floorAtYd)
		if err != nil {
			return dungeon.Point{}, false
		}
		best, ok := navmesh.Floor{}, false
		for _, f := range fl {
			if f.Flags&navmesh.NavGround == 0 || f.Flags&(navmesh.NavWater|navmesh.NavSteep|navmesh.NavMagma|navmesh.NavSlime) != 0 {
				continue
			}
			if d := math.Abs(float64(f.Z - p.Z)); !ok || d < math.Abs(float64(best.Z-p.Z)) {
				best, ok = f, true
			}
		}
		if !ok {
			return dungeon.Point{}, false
		}
		return dungeon.Point{X: p.X, Y: p.Y, Z: best.Z}, true
	}
}

// groundFloor is the floor at p nearest p's z.
func groundFloor(pl *navmesh.Planner, mapID uint32, p navmesh.Vec3) (navmesh.Floor, bool, error) {
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

// canDrop says the walk from a to b may skip the corner between them: the hop stays within FloorAwareHopYd, the straight line
// keeps off the mesh edge, and the ground under its middle is within FloorAwareFloorYd of the line's z (a corner on a ramp or
// over a dip is kept).
func canDrop(pl *navmesh.Planner, mapID uint32, a, b navmesh.Vec3) bool {
	d := cutDist(a, b)
	if d > planexport.FloorAwareHopYd {
		return false
	}
	ray, err := pl.Raycast(mapID, a, b)
	if err != nil || !ray.Known || !ray.Clear && float64(ray.Hit) < d-1 {
		return false
	}
	x, y := (a[0]+b[0])/2, (a[1]+b[1])/2
	meanZ := (a[2] + b[2]) / 2
	_, ok, err := splitFloor(pl, mapID, x, y, meanZ)
	return err == nil && ok
}

// turnDeg is the 3D change of direction at b (0 straight on, 180 back).
func turnDeg(a, b, c navmesh.Vec3) float64 {
	u := navmesh.Vec3{b[0] - a[0], b[1] - a[1], b[2] - a[2]}
	v := navmesh.Vec3{c[0] - b[0], c[1] - b[1], c[2] - b[2]}
	lu, lv := cutDist(a, b), cutDist(b, c)
	if lu == 0 || lv == 0 {
		return 180
	}
	dot := float64(u[0])*float64(v[0]) + float64(u[1])*float64(v[1]) + float64(u[2])*float64(v[2])
	cos := math.Max(-1, math.Min(1, dot/(lu*lv)))
	return math.Acos(cos) * 180 / math.Pi
}

// cutDist is the 3D distance of the cutter's points.
func cutDist(a, b navmesh.Vec3) float64 {
	return math.Sqrt(float64(sq32(a[0]-b[0]) + sq32(a[1]-b[1]) + sq32(a[2]-b[2])))
}

func cutPoint(v navmesh.Vec3) dungeon.Point { return dungeon.Point{X: v[0], Y: v[1], Z: v[2]} }

func sq32(v float32) float32 { return v * v }
