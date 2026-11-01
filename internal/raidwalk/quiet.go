package raidwalk

import (
	"fmt"
	"math"
	"sort"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/objects"
)

// The quiet distances of the revive procedure (spec 3.5): nothing alive and hostile within 45 yd, nothing alive
// hostile and moving within 80 yd, no patrol whose path comes within 45 yd. quietIdleYd is D-A (walk 25): a hostile
// that is IDLE (not in combat, no target, not moving) and stands farther out than this never blocks the revive. The 25
// matches the walk command's landing reach (placeReachYd), the reach the placement's own quiet accepted for the same
// point: an idle mob at home beyond it is exactly what the run faced when it started there, and a door-to-end run has
// it alive at the same distance. An idle unit at 25 yd or closer still blocks, and every unit in combat, holding a
// target or moving keeps the 45/80 yd rules below.
const (
	quietStillYd  = 45
	quietMovingYd = 80
	quietPatrolYd = 45
	quietIdleYd   = 25
)

// QuietPatrol is one patrol of the --patrols file, reduced to what the quiet check needs. Exempt is set by the
// caller when it found a member visible far off whose distance grew over the last two checks (spec 3.5).
type QuietPatrol struct {
	ID     string
	Path   []dungeon.Point
	Exempt bool
}

// QuietBlocker is one thing that keeps a revive point from being quiet: a hostile unit, by its own 45/80 yd rule (and
// D-A's idle exemption), or a --patrols patrol whose path comes too close.
type QuietBlocker struct {
	// Patrol says this blocker is a patrol, not a unit: only Name and Dist are set.
	Patrol   bool
	GUID     string
	Entry    uint32
	Name     string
	Dist     float64
	Moving   bool
	InCombat bool
	Target   uint64
}

// Reason is the blocker as QuietAt's reason names it.
func (b QuietBlocker) Reason() string {
	if b.Patrol {
		return fmt.Sprintf("%s passes %.1f yd from the revive point", b.Name, b.Dist)
	}
	return fmt.Sprintf("%s %.1f yd from the revive point", b.Name, b.Dist)
}

// QuietBlockers lists every blocker of the revive point p, the nearest first within its own rule (a unit inside the
// 45 yd still rule before a moving one, units before patrols, so the first one is the one QuietAt names). QuietAt uses
// it for its answer; the revive's run.log line and its summary refusal (D-E) use the whole list.
func QuietBlockers(units []objects.Unit, patrols []QuietPatrol, p dungeon.Point) []QuietBlocker {
	// rank orders the list the way QuietAt reports: still units (0), then moving ones (1), then patrols (2). The key is
	// the distance within a rank; a patrol keeps its file order (key 0 with a stable sort).
	type ranked struct {
		b    QuietBlocker
		rank int
		key  float64
	}
	var rs []ranked
	for i := range units {
		u := &units[i]
		if !u.Hostile || u.Dead || u.Type == "player" {
			continue
		}
		d := dist3(p, dungeon.Point{X: u.X, Y: u.Y, Z: u.Z})
		// D-A (walk 25): an idle unit beyond the landing reach never blocks the revive.
		if !u.InCombat && u.Target == 0 && !u.Moving && d > quietIdleYd {
			continue
		}
		switch {
		case d <= quietStillYd:
			rs = append(rs, ranked{b: unitBlocker(u, d), rank: 0, key: d})
		case u.Moving && d <= quietMovingYd:
			rs = append(rs, ranked{b: unitBlocker(u, d), rank: 1, key: d})
		}
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
			rs = append(rs, ranked{b: QuietBlocker{Patrol: true, Name: id, Dist: d}, rank: 2})
		}
	}
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].rank != rs[j].rank {
			return rs[i].rank < rs[j].rank
		}
		return rs[i].key < rs[j].key
	})
	out := make([]QuietBlocker, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.b)
	}
	return out
}

func unitBlocker(u *objects.Unit, d float64) QuietBlocker {
	return QuietBlocker{GUID: u.GUID.String(), Entry: u.Entry, Name: u.Name, Dist: d,
		Moving: u.Moving, InCombat: u.InCombat, Target: uint64(u.Target)}
}

// QuietAt says whether the revive point p is clear of hosts and patrols.
func QuietAt(units []objects.Unit, patrols []QuietPatrol, p dungeon.Point) (bool, string) {
	bs := QuietBlockers(units, patrols, p)
	if len(bs) == 0 {
		return true, ""
	}
	return false, bs[0].Reason()
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
