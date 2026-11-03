package navmesh

import "math"

// The liquid check's windows, in yards.
const (
	// LiquidOwnYd is how close to the point's z its floor must be to be its own surface (planlint's off-floor rule).
	LiquidOwnYd = 1.5
	// LiquidNearYd is how far round the point the mesh's water polygons are looked for.
	LiquidNearYd = 6.0
	// LiquidAboveYd is how far below a nearby water floor a point must stand to be under its surface.
	LiquidAboveYd = 2.0
	// LiquidScanDirs is the number of directions the nearby water is looked for in.
	LiquidScanDirs = 8
	// liquidScanUpYd bounds how high above the point the nearby water is looked for (deeper basins).
	liquidScanUpYd = 30.0
)

// Liquid is a navmesh answer to LiquidAt: whether the point is in or under a liquid, and the water floor that says so. Own
// says the water is the point's own floor; else it is a nearby water floor standing LiquidAboveYd or more above the point.
type Liquid struct {
	Wet   bool
	Z     float32
	Where Vec3
	Own   bool
}

// FloorReader is what LiquidAt asks of the navmesh: Floors. *Planner, planlint.Nav and the route-regen planner all are one.
type FloorReader interface {
	Floors(mapID uint32, x, y, lo, hi float32) ([]Floor, error)
}

// LiquidAt says whether the mesh puts p in or under a liquid. The mmap generator flags the liquid's bed as NavWater where the
// liquid is at least a body deep, and plain ground over the shallows, so the point's own floor is the first answer; a point
// whose own floor is ground but that stands under a nearby NavWater floor is in the water too (the lake bed a walk dips
// into, the canal under its rim). Both answers are the tiles' own area flags: nothing is guessed from heights.
func LiquidAt(nav FloorReader, mapID uint32, p Vec3) (Liquid, error) {
	fl, err := nav.Floors(mapID, p[0], p[1], p[2]-LiquidOwnYd, p[2]+LiquidOwnYd)
	if err != nil {
		return Liquid{}, err
	}
	own, ownD := Liquid{}, float32(math.MaxFloat32)
	for _, f := range fl {
		if f.Flags&NavWater == 0 {
			continue
		}
		if d := float32(math.Abs(float64(f.Z - p[2]))); d <= LiquidOwnYd && d < ownD {
			own, ownD = Liquid{Wet: true, Z: f.Z, Where: p, Own: true}, d
		}
	}
	if own.Wet {
		return own, nil
	}
	best := Liquid{}
	for k := range LiquidScanDirs {
		a := 2 * math.Pi * float64(k) / LiquidScanDirs
		q := Vec3{p[0] + float32(LiquidNearYd*math.Cos(a)), p[1] + float32(LiquidNearYd*math.Sin(a)), p[2]}
		fl, err := nav.Floors(mapID, q[0], q[1], q[2]+LiquidAboveYd, q[2]+liquidScanUpYd)
		if err != nil {
			return Liquid{}, err
		}
		for _, f := range fl {
			if f.Flags&NavWater == 0 {
				continue
			}
			if !best.Wet || f.Z < best.Z {
				best = Liquid{Wet: true, Z: f.Z, Where: q}
			}
		}
	}
	return best, nil
}
