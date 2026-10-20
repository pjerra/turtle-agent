// Package routeoracle compares a dungeon plan's stored route with what the server's navmesh
// plans for the same walks, leg by leg, and reports how far the two are apart. It is the
// route-oracle command's engine and Stage 1's fixture writer: the stored polyline is what the
// plan and planexport hold, the live path is what Planner.Plan answers, and the report's
// numbers (ratio, corridor membership, break proximity, Z jumps) are the gap between them.
//
// The package reads plans and mmaps and nothing else.
package routeoracle

import (
	"math"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

// Vec3 is a WoW position (x, y, z).
type Vec3 = navmesh.Vec3

// The report's distances and the thresholds behind them, in yards. The brief fixes them:
// a pull group is on the path when one of its positions stands within its radius plus
// CorridorSlackYd of it (horizontally) and no more than CorridorMaxDZYd above or below it;
// a stored approach or via point within RedundantYd of the path adds nothing; a break
// (a waypoint with exact/direct/gm_cross/check_skip) is touched when the path passes within
// BreakNearYd of its foot, or when the leg is Partial and the path's end gap ends within
// BreakGapYd of the foot; consecutive path samples more than ZJumpYd apart in Z are a jump
// the mesh joins without a climb.
const (
	CorridorSlackYd = 18.0
	CorridorMaxDZYd = 8.0
	RedundantYd     = 5.0
	BreakNearYd     = 6.0
	BreakGapYd      = 10.0
	ZJumpYd         = 8.0

	// DefaultPackRadius is the dungeon driver's radius around a pull position when a group
	// gives none (driver.go's PackRadius default).
	DefaultPackRadius = 25.0
)

func sq(v float64) float64 { return v * v }

// dist3 is the straight distance between two points.
func dist3(a, b Vec3) float64 {
	return math.Sqrt(sq(float64(a[0]-b[0])) + sq(float64(a[1]-b[1])) + sq(float64(a[2]-b[2])))
}

// dist2 is the horizontal distance between two points.
func dist2(a, b Vec3) float64 {
	return math.Sqrt(sq(float64(a[0]-b[0])) + sq(float64(a[1]-b[1])))
}

// NearPoint is the distance of a point to a polyline: the straight (3D) distance to the
// nearest point of it, the horizontal (2D) one, the height between the two, and the nearest
// point itself. Dist3D is the honest "how far is it from what is walked"; the corridor tests
// take Dist2D and DZ apart, because a pack a floor above a corridor is not on it however
// close horizontally.
type NearPoint struct {
	Dist3D float64
	Dist2D float64
	DZ     float64
	At     Vec3 // the nearest point of the polyline
}

// NearestPolyline is the nearest point of the polyline pts to p. A one-point polyline is a
// point; an empty one returns nothing (Dist3D negative).
func NearestPolyline(pts []Vec3, p Vec3) NearPoint {
	if len(pts) == 0 {
		return NearPoint{Dist3D: -1}
	}
	best := NearPoint{Dist3D: math.Inf(1), Dist2D: math.Inf(1)}
	upd := func(q Vec3) {
		d3, d2, dz := dist3(p, q), dist2(p, q), math.Abs(float64(p[2]-q[2]))
		if d3 < best.Dist3D {
			best = NearPoint{Dist3D: d3, Dist2D: d2, DZ: dz, At: q}
		}
	}
	upd(pts[0])
	upd(pts[len(pts)-1])
	for i := 1; i < len(pts); i++ {
		a, b := pts[i-1], pts[i]
		ab := [3]float64{float64(b[0] - a[0]), float64(b[1] - a[1]), float64(b[2] - a[2])}
		ln := sq(ab[0]) + sq(ab[1]) + sq(ab[2])
		if ln <= 0 {
			continue
		}
		ap := [3]float64{float64(p[0] - a[0]), float64(p[1] - a[1]), float64(p[2] - a[2])}
		f := (ab[0]*ap[0] + ab[1]*ap[1] + ab[2]*ap[2]) / ln
		if f <= 0 || f >= 1 {
			continue // the ends are done
		}
		upd(Vec3{a[0] + float32(ab[0]*f), a[1] + float32(ab[1]*f), a[2] + float32(ab[2]*f)})
	}
	return best
}

// PolylineLength is the length of the walk along the polyline, in yards.
func PolylineLength(pts []Vec3) float64 {
	var n float64
	for i := 1; i < len(pts); i++ {
		n += dist3(pts[i-1], pts[i])
	}
	return n
}

// PathSamples flattens a navmesh path into the samples a walk passes: every corner and,
// between two corners, the surface samples of the segment. A gap between two samples of
// these more than ZJumpYd tall is ground the walk falls or hops.
func PathSamples(path navmesh.Path) []Vec3 {
	if len(path.Points) == 0 {
		return nil
	}
	out := make([]Vec3, 0, len(path.Points))
	out = append(out, path.Points[0])
	for i := 1; i < len(path.Points); i++ {
		out = append(out, path.Ground[i-1]...)
		out = append(out, path.Points[i])
	}
	return out
}

// ZJump is one step of more than ZJumpYd between two consecutive samples of a path: a rise
// or drop the mesh joins without a climb a walk can take. At is the lower of the two samples.
type ZJump struct {
	At     Vec3
	DropYd float64
}

// ZJumps lists the jumps between consecutive samples of a path, in walk order.
func ZJumps(samples []Vec3) []ZJump {
	var out []ZJump
	for i := 1; i < len(samples); i++ {
		dz := float64(samples[i][2] - samples[i-1][2])
		if math.Abs(dz) > ZJumpYd {
			at := samples[i-1]
			if dz < 0 {
				at = samples[i]
			}
			out = append(out, ZJump{At: at, DropYd: math.Abs(dz)})
		}
	}
	return out
}

// OnCorridor says whether a position is in the corridor of the polyline: within radius
// (a pack's radius) plus CorridorSlackYd of it horizontally, and no more than
// CorridorMaxDZYd above or below the path at that point.
func OnCorridor(near NearPoint, radius float64) bool {
	if near.Dist3D < 0 {
		return false
	}
	return near.Dist2D <= radius+CorridorSlackYd && near.DZ <= CorridorMaxDZYd
}

// Redundant says whether a stored approach or via point lies within RedundantYd of the
// live path: the walk the plan stores adds nothing the path does not already pass.
func Redundant(near NearPoint) bool {
	return near.Dist3D >= 0 && near.Dist3D <= RedundantYd
}
