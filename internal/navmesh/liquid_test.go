package navmesh

import (
	"math"
	"testing"
)

// floorsAt answers Floors from a height-to-flags map of the (x, y) points it is asked about (rounded to the yard), so a test
// can put a water floor under the point or on one of the LiquidScanDirs ring points.
type floorsAt struct {
	at map[[2]int]Floor // key: rounded x, y; the Z and Flags to answer
}

func (f floorsAt) Floors(_ uint32, x, y, lo, hi float32) ([]Floor, error) {
	k := [2]int{int(math.Round(float64(x))), int(math.Round(float64(y)))}
	if q, ok := f.at[k]; ok && q.Z >= lo && q.Z <= hi {
		return []Floor{q}, nil
	}
	return nil, nil
}

func TestLiquidAtOwnFloor(t *testing.T) {
	nav := floorsAt{at: map[[2]int]Floor{{0, 0}: {Z: 12, Flags: NavWater}}}
	got, err := LiquidAt(nav, 1, Vec3{0, 0, 12})
	if err != nil || !got.Wet || !got.Own || got.Z != 12 {
		t.Fatalf("got %+v, %v, want the point's own NavWater floor", got, err)
	}
}

func TestLiquidAtUnderNearbySurface(t *testing.T) {
	// Ground under the point, and a water floor at x = LiquidNearYd standing LiquidAboveYd + 2 above it: the bed of a basin.
	nav := floorsAt{at: map[[2]int]Floor{{int(math.Round(LiquidNearYd)), 0}: {Z: 10, Flags: NavWater}}}
	got, err := LiquidAt(nav, 1, Vec3{0, 0, 4})
	if err != nil || !got.Wet || got.Own || got.Z != 10 {
		t.Fatalf("got %+v, %v, want wet under a nearby surface at z 10", got, err)
	}
}

func TestLiquidAtShoreIsDry(t *testing.T) {
	// A water floor within LiquidAboveYd of the point's own z is the shore, not a surface overhead.
	nav := floorsAt{at: map[[2]int]Floor{{int(math.Round(LiquidNearYd)), 0}: {Z: 5, Flags: NavWater}}}
	got, err := LiquidAt(nav, 1, Vec3{0, 0, 4})
	if err != nil || got.Wet {
		t.Fatalf("got %+v, %v, want dry (the water is under the point's own height)", got, err)
	}
}

func TestLiquidAtDry(t *testing.T) {
	nav := floorsAt{at: map[[2]int]Floor{{0, 0}: {Z: 8, Flags: NavGround}}}
	got, err := LiquidAt(nav, 1, Vec3{0, 0, 8})
	if err != nil || got.Wet {
		t.Fatalf("got %+v, %v, want dry", got, err)
	}
}
