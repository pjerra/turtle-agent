package mapview

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

// FloorOptions tunes the split of a map into floors by height.
type FloorOptions struct {
	// MinGap is the vertical distance in yards between two walkable surfaces over the same spot that makes them
	// two floors.
	MinGap float32
	// MinShare is the share of the walkable area a cut must separate (spots where surfaces lie over each other)
	// to count; a smaller one is a ramp or a bridge, not a floor.
	MinShare float64
}

// DefaultFloorOptions is what the CLI uses.
func DefaultFloorOptions() FloorOptions { return FloorOptions{MinGap: 6, MinShare: 0.02} }

// cellSize is the grid in yards the surfaces are compared on.
const cellSize = 2.0

// SplitHeights finds where a stack of floors separates: the heights, ascending, that divide the walkable surfaces
// into floors. It looks for the spots where one walkable surface lies at least MinGap yards over another (the
// upper storey over the hall under it); the height halfway between the two is a vote for a cut, and the cuts are
// where enough votes (MinShare of the walkable area) gather. A map with nothing stacked has no cut: surfaces that
// merely differ in height (a hill, a ramp) never vote.
func SplitHeights(polys []navmesh.Polygon, o FloorOptions) []float32 {
	type cell [2]int32
	levels := map[cell][]float32{}
	const tol = 2.5 // surfaces closer than this over a spot are one level
	for _, p := range polys {
		if len(p.Verts) < 3 || p.Flags&(navmesh.NavGround|navmesh.NavWater) == 0 || p.Flags&navmesh.NavSteep != 0 {
			continue
		}
		pl, ok := newPlane(p.Verts)
		if !ok {
			continue
		}
		minX, minY := math.Inf(1), math.Inf(1)
		maxX, maxY := math.Inf(-1), math.Inf(-1)
		for _, v := range p.Verts {
			minX, maxX = math.Min(minX, float64(v[0])), math.Max(maxX, float64(v[0]))
			minY, maxY = math.Min(minY, float64(v[1])), math.Max(maxY, float64(v[1]))
		}
		for cx := int32(math.Floor(minX / cellSize)); float64(cx)*cellSize <= maxX; cx++ {
			for cy := int32(math.Floor(minY / cellSize)); float64(cy)*cellSize <= maxY; cy++ {
				x, y := (float64(cx)+0.5)*cellSize, (float64(cy)+0.5)*cellSize
				if !insideConvex(p.Verts, x, y) {
					continue
				}
				z := float32(pl.z(x, y))
				k := cell{cx, cy}
				lv := levels[k]
				dup := false
				for _, l := range lv {
					if math.Abs(float64(l-z)) < tol {
						dup = true
						break
					}
				}
				if !dup && len(lv) < 8 {
					levels[k] = append(lv, z)
				}
			}
		}
	}
	if len(levels) == 0 {
		return nil
	}
	var votes []float64
	for _, lv := range levels {
		sort.Slice(lv, func(i, j int) bool { return lv[i] < lv[j] })
		for i := 1; i < len(lv); i++ {
			if lv[i]-lv[i-1] >= o.MinGap {
				votes = append(votes, float64(lv[i]+lv[i-1])/2)
			}
		}
	}
	if len(votes) == 0 {
		return nil
	}
	sort.Float64s(votes)
	need := math.Max(8, o.MinShare*float64(len(levels)))
	var cuts []float32
	for i := 0; i < len(votes); {
		j := i
		for j < len(votes) && votes[j]-votes[i] <= float64(o.MinGap) {
			j++
		}
		if float64(j-i) >= need {
			cuts = append(cuts, float32(math.Round(votes[(i+j)/2]*10)/10))
			i = j
			continue
		}
		i++
	}
	// Cuts closer than MinGap are one.
	out := cuts[:0]
	for _, c := range cuts {
		if n := len(out); n > 0 && c-out[n-1] < o.MinGap {
			continue
		}
		out = append(out, c)
	}
	return out
}

// plane is the plane through a polygon, to read its height anywhere over it.
type plane struct{ a, b, c float64 } // z = a*x + b*y + c

func newPlane(v []navmesh.Vec3) (plane, bool) {
	p0 := v[0]
	for i := 1; i+1 < len(v); i++ {
		x1, y1, z1 := float64(v[i][0]-p0[0]), float64(v[i][1]-p0[1]), float64(v[i][2]-p0[2])
		x2, y2, z2 := float64(v[i+1][0]-p0[0]), float64(v[i+1][1]-p0[1]), float64(v[i+1][2]-p0[2])
		det := x1*y2 - x2*y1
		if math.Abs(det) < 1e-6 {
			continue
		}
		a := (z1*y2 - z2*y1) / det
		b := (x1*z2 - x2*z1) / det
		return plane{a, b, float64(p0[2]) - a*float64(p0[0]) - b*float64(p0[1])}, true
	}
	return plane{}, false
}

func (p plane) z(x, y float64) float64 { return p.a*x + p.b*y + p.c }

// insideConvex says whether (x, y) lies in a convex polygon, whichever way its corners run.
func insideConvex(v []navmesh.Vec3, x, y float64) bool {
	pos, neg := false, false
	for i := range v {
		j := (i + 1) % len(v)
		c := (float64(v[j][0])-float64(v[i][0]))*(y-float64(v[i][1])) - (float64(v[j][1])-float64(v[i][1]))*(x-float64(v[i][0]))
		if c > 0 {
			pos = true
		} else if c < 0 {
			neg = true
		}
		if pos && neg {
			return false
		}
	}
	return true
}

// Floor is one height band of the map with the navmesh polygons on it.
type Floor struct {
	Name  string
	Lo    float32 // the band holds heights from Lo (inclusive) up to Hi (exclusive); -Inf and +Inf at the ends
	Hi    float32
	Polys []navmesh.Polygon
}

// FloorIndex is the number of the floor a height falls on, given the cuts (ascending).
func FloorIndex(cuts []float32, z float32) int {
	return sort.Search(len(cuts), func(i int) bool { return cuts[i] > z })
}

// floorName describes a band for a legend and a file name.
func floorName(i, n int, lo, hi float32) string {
	switch {
	case n == 1:
		return "all heights"
	case i == 0:
		return fmt.Sprintf("floor 1, below z %.0f", hi)
	case i == n-1:
		return fmt.Sprintf("floor %d, above z %.0f", i+1, lo)
	}
	return fmt.Sprintf("floor %d, z %.0f to %.0f", i+1, lo, hi)
}

// buildFloors files the polygons under the floors the cuts make.
func buildFloors(polys []navmesh.Polygon, cuts []float32) []Floor {
	fl := make([]Floor, len(cuts)+1)
	for i := range fl {
		lo, hi := float32(math.Inf(-1)), float32(math.Inf(1))
		if i > 0 {
			lo = cuts[i-1]
		}
		if i < len(cuts) {
			hi = cuts[i]
		}
		fl[i] = Floor{Name: floorName(i, len(fl), lo, hi), Lo: lo, Hi: hi}
	}
	for _, p := range polys {
		if len(p.Verts) < 3 {
			continue
		}
		z := float32(0)
		for _, v := range p.Verts {
			z += v[2]
		}
		i := FloorIndex(cuts, z/float32(len(p.Verts)))
		fl[i].Polys = append(fl[i].Polys, p)
	}
	return fl
}

// ParseCuts reads the --floors flag: "auto", "off", or heights such as "-30,-10", ascending.
func ParseCuts(s string) (cuts []float32, auto bool, err error) {
	s = strings.TrimSpace(s)
	switch s {
	case "", "auto":
		return nil, true, nil
	case "off", "none":
		return nil, false, nil
	}
	for _, f := range strings.Split(s, ",") {
		v, err := strconv.ParseFloat(strings.TrimSpace(f), 32)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, false, fmt.Errorf("floors: %q is not a height", f)
		}
		if n := len(cuts); n > 0 && float32(v) <= cuts[n-1] {
			return nil, false, fmt.Errorf("floors: heights must ascend (%v after %v)", v, cuts[n-1])
		}
		cuts = append(cuts, float32(v))
	}
	return cuts, false, nil
}
