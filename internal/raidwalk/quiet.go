package raidwalk

import (
	"fmt"
	"math"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/objects"
)

// The quiet distances of the revive procedure (spec 3.5): nothing alive and hostile within 45 yd, nothing alive
// hostile and moving within 80 yd, no patrol whose path comes within 45 yd.
const (
	quietStillYd  = 45
	quietMovingYd = 80
	quietPatrolYd = 45
)

// QuietPatrol is one patrol of the --patrols file, reduced to what the quiet check needs. Exempt is set by the
// caller when it found a member visible far off whose distance grew over the last two checks (spec 3.5).
type QuietPatrol struct {
	ID     string
	Path   []dungeon.Point
	Exempt bool
}

// QuietAt says whether the revive point p is clear of hosts and patrols.
func QuietAt(units []objects.Unit, patrols []QuietPatrol, p dungeon.Point) (bool, string) {
	var moving *objects.Unit
	var movingYd float64
	var still *objects.Unit
	var stillYd float64
	for i := range units {
		u := &units[i]
		if !u.Hostile || u.Dead || u.Type == "player" {
			continue
		}
		d := dist3(p, dungeon.Point{X: u.X, Y: u.Y, Z: u.Z})
		if d <= quietStillYd && (still == nil || d < stillYd) {
			still, stillYd = u, d
		}
		if u.Moving && d <= quietMovingYd && (moving == nil || d < movingYd) {
			moving, movingYd = u, d
		}
	}
	if still != nil {
		return false, fmt.Sprintf("%s %.1f yd from the revive point", still.Name, stillYd)
	}
	if moving != nil {
		return false, fmt.Sprintf("%s %.1f yd from the revive point", moving.Name, movingYd)
	}
	for _, pat := range patrols {
		if pat.Exempt || len(pat.Path) == 0 {
			continue
		}
		d := pathDistance(p, pat.Path)
		if d <= quietPatrolYd {
			id := pat.ID
			if id == "" {
				id = "patrol"
			}
			return false, fmt.Sprintf("%s passes %.1f yd from the revive point", id, d)
		}
	}
	return true, ""
}

func dist3(a, b dungeon.Point) float64 {
	dx, dy, dz := float64(a.X-b.X), float64(a.Y-b.Y), float64(a.Z-b.Z)
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

// pathDistance is the closest distance from p to the path, whose last point closes back to the first.
func pathDistance(p dungeon.Point, path []dungeon.Point) float64 {
	if len(path) == 1 {
		return dist3(p, path[0])
	}
	best := math.Inf(1)
	for i := range path {
		j := (i + 1) % len(path)
		if d := segmentDistance(p, path[i], path[j]); d < best {
			best = d
		}
	}
	return best
}

func segmentDistance(p, a, b dungeon.Point) float64 {
	ax, ay, az := float64(a.X), float64(a.Y), float64(a.Z)
	bx, by, bz := float64(b.X), float64(b.Y), float64(b.Z)
	px, py, pz := float64(p.X), float64(p.Y), float64(p.Z)
	dx, dy, dz := bx-ax, by-ay, bz-az
	den := dx*dx + dy*dy + dz*dz
	if den == 0 {
		return dist3(p, a)
	}
	t := ((px-ax)*dx + (py-ay)*dy + (pz-az)*dz) / den
	t = math.Max(0, math.Min(1, t))
	return math.Sqrt(math.Pow(px-(ax+t*dx), 2) + math.Pow(py-(ay+t*dy), 2) + math.Pow(pz-(az+t*dz), 2))
}
