package raidwalk

import (
	"fmt"
	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"math"
	"sort"
	"strings"
	"time"
)

// The quiet distances of the revive procedure (spec 3.5): nothing alive and hostile within 45 yd, nothing alive
// hostile and moving within 80 yd, no patrol whose path comes within 45 yd. quietIdleYd is D-A (walk 25): a hostile
// that is IDLE (not in combat, no target, not moving) and stands farther out than this never blocks the revive, the
// same idea as the placement's landing reach (placeReachYd) but a little wider: an idle mob at home far enough out is
// exactly what the run faced when it started there, and a door-to-end run has it alive at the same distance. An idle
// unit at 30 yd or closer still blocks, and every unit in combat, holding a target or moving keeps the 45/80 yd rules
// below.
//
// Being still proves nothing about where home is (Codex HIGH, turtle-agent#360): a patrol that paused at 31-45 yd
// passed the old stillness check, and the raid could be summoned next to it. So the exemption also needs a proven
// home (PlanHomeSpawns, from the plan): the unit must stand within quietHomeYd of a plan spawn of its entry — by
// spawn GUID when the plan names it (a creature's low bits are its spawn guid), else by entry — and units of a group
// the plan marks as a patrol (PullGroup.Patrol) are NEVER exempt, even at the group's own positions. Without plan
// spawn data (a nil list) there is no exemption: the strict 45 yd rule holds.
//
// The exemption needs TWO reads (review round 1, Opus M1): quietIdleHold is how far apart they must be and
// quietIdleMoveYd how close to the same spot, so a mob that was idle once and starts to move is not waved through on
// the strength of one stale sighting.
//
// A critter never counts (turtle-agent#379): a level-1 unit of the place phase's max-HP band is no threat to a revive,
// so it is not a blocker at any distance or state. Walk 27 C restart 2 waited 115 s on a moving Snake 2914 at 72 yd
// beside an idle Mad Servant off its spawn; with the critter out of the list the refusal's all_idle names the real
// blocker and the wait is not blamed on a snake.
const (
	quietStillYd  = 45
	quietMovingYd = 80
	quietPatrolYd = 45
	quietIdleYd   = 30
	// quietHomeYd is how close to a plan spawn a unit must stand for the idle exemption: it is at its HOME spawn.
	quietHomeYd = 3
	// quietIdleHold / quietIdleMoveYd are D-A's two-read rule.
	quietIdleHold   = 5 * time.Second
	quietIdleMoveYd = 1
	// quietIdleMaxGap bounds how old the first of the two reads may be (Codex round 3): a sighting from long ago,
	// or from before a stretch of failed reads, is no evidence about the unit now.
	quietIdleMaxGap = 30 * time.Second
	// quietCritterHP is the top of a critter's max-HP band, the place phase's own (placeCritterHP): a level-1 unit
	// with at most this many hit points is a critter and never counts in the quiet check (turtle-agent#379). The
	// units read has no rank field, so the level is the rank the frame/toolkit would carry: critters are level 1.
	quietCritterHP = 100
)

// critter says the unit never counts in the quiet check: a level-1 unit of the place phase's max-HP band is a critter,
// and a critter never blocks a revive, so it must not make all_idle false either. The units read carries no rank field
// (the frame/toolkit's critter flag is the kind, and the toolkit's rank is on its own `q units` reply), so the level and
// max health are what is left of those fields; a unit with no level read (0) is never a critter (fail closed).
func critter(u *objects.Unit) bool {
	return u.Type == "creature" && u.Level == 1 && u.MaxHealth > 0 && u.MaxHealth <= quietCritterHP
}

// QuietHomeSpawn is one plan spawn the idle exemption trusts: an entry and where it stands (a pull group's position,
// the boss's position, a left-alone spawn's At), with the spawn guid when the plan names one. Patrol says the group
// the plan marks as a patrol (PullGroup.Patrol): a patrol-group unit is never exempt.
type QuietHomeSpawn struct {
	Entry  uint32
	Spawn  uint32 // creature spawn guid low, when the plan names it; 0: match by entry
	At     dungeon.Point
	Patrol bool
}

// QuietHomeSpawns is the plan spawn list the idle exemption reads: nil (no data) exempts nothing.
type QuietHomeSpawns []QuietHomeSpawn

// PlanHomeSpawns is the idle exemption's home list for a plan: every pull group position (entries paired with the
// positions they stand at, or with every position when fewer positions than entries), the boss at its position, and
// the left-alone spawns at theirs. A group with a Patrol block marks every spawn it yields as a patrol one.
func PlanHomeSpawns(plan *dungeon.Plan) QuietHomeSpawns {
	if plan == nil {
		return nil
	}
	var out QuietHomeSpawns
	for _, l := range plan.Legs {
		for _, g := range l.PullGroups {
			pos := g.Positions
			for i, e := range g.Entries {
				q := pos
				if i < len(pos) {
					q = pos[i : i+1]
				}
				for _, at := range q {
					out = append(out, QuietHomeSpawn{Entry: e, At: at, Patrol: g.Patrol != nil})
				}
			}
		}
		if l.Boss != nil {
			out = append(out, QuietHomeSpawn{Entry: l.Boss.Entry, At: l.Boss.Position})
		}
		for _, lv := range l.Leave {
			out = append(out, QuietHomeSpawn{Entry: lv.Entry, Spawn: lv.Spawn, At: lv.At})
		}
	}
	return out
}

// spawnLow is the spawn guid low a creature unit's GUID carries (bits 0-23): 0 when the GUID has no creature form.
func spawnLow(u *objects.Unit) uint32 {
	if u.GUID.High() != packet.HighUnit {
		return 0
	}
	return uint32(uint64(u.GUID) & 0xFFFFFF)
}

// atHome says the unit stands at its HOME spawn: within quietHomeYd (3D) of a same-entry plan spawn — the exact one
// when the plan names its spawn guid and the unit's GUID carries one, else any spawn of that entry — and never for a
// patrol-group unit. A nil list is no data: no exemption.
func (homes QuietHomeSpawns) atHome(u *objects.Unit) bool {
	if homes == nil {
		return false
	}
	up := dungeon.Point{X: u.X, Y: u.Y, Z: u.Z}
	low := spawnLow(u)
	byEntry := false
	for _, h := range homes {
		if h.Entry != u.Entry {
			continue
		}
		if h.Patrol {
			// The plan knows this entry as a patrol member here: never exempt it, even at the group's positions.
			return false
		}
		if h.Spawn != 0 && low != 0 {
			if h.Spawn != low {
				continue
			}
			if dist3(up, h.At) <= quietHomeYd {
				return true
			}
			continue
		}
		if dist3(up, h.At) <= quietHomeYd {
			byEntry = true
		}
	}
	return byEntry
}

// QuietIdleReads is the revive's memory of the last read that saw each hostile idle at home: D-A's exemption needs the
// unit seen idle twice, quietIdleHold apart and within quietIdleMoveYd of the same position (review round 1, Opus M1).
type QuietIdleReads struct {
	prev map[string]quietIdleAt
}

type quietIdleAt struct {
	at   time.Time
	x, y float32
}

// Reset forgets every sighting: a failed units read leaves a gap the two-read rule cannot see across.
func (r *QuietIdleReads) Reset() { r.prev = map[string]quietIdleAt{} }

// NewQuietIdleReads makes the per-revive read memory.
func NewQuietIdleReads() *QuietIdleReads { return &QuietIdleReads{prev: map[string]quietIdleAt{}} }

// idleHome records this read of u and says whether it is the second of two reads quietIdleHold apart that both saw u
// idle within quietIdleMoveYd of the same spot (x/y: an idle mob's z does not wander). A read that does not see u idle
// forgets the earlier one.
func (r *QuietIdleReads) idleHome(u *objects.Unit, now time.Time) bool {
	key := strings.ToUpper(u.GUID.String())
	if u.InCombat || u.Target != 0 || u.Moving {
		delete(r.prev, key)
		return false
	}
	prev, ok := r.prev[key]
	r.prev[key] = quietIdleAt{at: now, x: u.X, y: u.Y}
	if !ok || now.Sub(prev.at) < quietIdleHold || now.Sub(prev.at) > quietIdleMaxGap {
		return false
	}
	return math.Hypot(float64(prev.x-u.X), float64(prev.y-u.Y)) <= quietIdleMoveYd
}

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
func QuietBlockers(units []objects.Unit, patrols []QuietPatrol, p dungeon.Point, idle *QuietIdleReads, homes QuietHomeSpawns, now time.Time) []QuietBlocker {
	// rank orders the list the way QuietAt reports: still units (0), then moving ones (1), then patrols (2). The key is
	// the distance within a rank; a patrol keeps its file order (key 0 with a stable sort).
	type ranked struct {
		b    QuietBlocker
		rank int
		key  float64
	}
	var rs []ranked
	seen := map[string]bool{}
	for i := range units {
		u := &units[i]
		// A critter never counts, at any distance and in any state (turtle-agent#379: the moving Snake 2914 at 72 yd
		// made all_idle false beside an idle Mad Servant). It is skipped before the read memory, so it is never a
		// blocker and never a reason to wait.
		if !u.Hostile || u.Dead || u.Type == "player" || critter(u) {
			continue
		}
		seen[strings.ToUpper(u.GUID.String())] = true
		d := dist3(p, dungeon.Point{X: u.X, Y: u.Y, Z: u.Z})
		// D-A (walk 25, two reads since review round 1): an idle unit beyond the landing reach never blocks the revive,
		// but only once two reads quietIdleHold apart saw it idle at the same spot, and only at its proven home (Codex
		// HIGH). idleHome records this read either way; a nil tracker, or no plan home, exempts nothing.
		home := idle != nil && idle.idleHome(u, now) && homes.atHome(u)
		if home && d > quietIdleYd {
			continue
		}
		switch {
		case d <= quietStillYd:
			rs = append(rs, ranked{b: unitBlocker(u, d), rank: 0, key: d})
		case u.Moving && d <= quietMovingYd:
			rs = append(rs, ranked{b: unitBlocker(u, d), rank: 1, key: d})
		}
	}
	// Snapshot-aware (Codex round 3): a unit this complete read did not see as a live hostile loses its earlier
	// sighting, so one that vanished, died or respawned needs two fresh reads again.
	if idle != nil {
		for k := range idle.prev {
			if !seen[k] {
				delete(idle.prev, k)
			}
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

// QuietAt says whether the revive point p is clear of hosts and patrols. idle is the read memory D-A's two-read
// exemption needs; a nil one exempts no idle unit. homes is the plan's home spawns the proven-home rule reads; a nil
// one (no plan data) exempts nothing.
func QuietAt(units []objects.Unit, patrols []QuietPatrol, p dungeon.Point, idle *QuietIdleReads, homes QuietHomeSpawns, now time.Time) (bool, string) {
	bs := QuietBlockers(units, patrols, p, idle, homes, now)
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
