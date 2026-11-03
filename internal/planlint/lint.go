// Package planlint checks the points of an export's rows (the ai_dungeon_step rows a run leader walks, not only the plan's own
// waypoints) against the placement rules of the Zul'Gurub waypoint audit: a point must stand on plain ground, away from edges
// and ledges, its hop from the row before it must be worth a separate goal, and it must keep out of the aggro of units the run
// has not pulled yet and out of patrol lanes. It reads facts only (navmesh, world export); it never changes a row.
package planlint

import (
	"fmt"
	"math"
	"strings"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

// The lints.
const (
	LintWater    = "water"
	LintOffFloor = "off-floor"
	LintEdge     = "edge"
	LintLedge    = "ledge"
	LintShortHop = "short hop"
	LintAggro    = "aggro"
	LintPatrol   = "patrol"
)

// The thresholds, in yards (the audit's §3 placement rules).
const (
	OffFloorYd     = 1.5
	EdgeYd         = 2.5
	StandEdgeYd    = 4.0
	LedgeNearYd    = 4.5
	LedgeLowYd     = 4.5
	LedgeHighYd    = 8.0
	ShortHopYd     = 8.0
	AggroMarginYd  = 8.0
	PatrolMarginYd = 5.0
	// RayDirs is the number of directions the clearance rays are cast in.
	RayDirs = 16
	// EdgeRayYd is how far the clearance rays reach: past the widest edge the check wants to see (StandEdgeYd).
	EdgeRayYd = 6.0
	// wanderCapYd bounds a random mover's wander: the ZG export has a wander of 11356 yd (the champion's entry in the
	// wrong column), which is no radius; the audit ignores a wander this large too.
	wanderCapYd = 50.0
)

// Nav is the navmesh the lints ask: navmesh.Planner is one.
type Nav interface {
	Floors(mapID uint32, x, y, lo, hi float32) ([]navmesh.Floor, error)
	Nearest(mapID uint32, p navmesh.Vec3) (navmesh.Vec3, bool, error)
	Raycast(mapID uint32, from, to navmesh.Vec3) (navmesh.Ray, error)
}

// Options are the lints' choices.
type Options struct {
	// Map is the plan's map id.
	Map uint32
	// Level is the party level the aggro radius is computed at (the audit used 60).
	Level int
	// World is the map's world export (spawns, groups, patrol lanes). Nil skips the aggro, patrol and joiner lints.
	World *worldspawn.World
	// Raid, KillFirst and PullCeiling are the plan's pull rules (the joiner lint); zero values are a party with none.
	Raid        bool
	KillFirst   []uint32
	PullCeiling int
}

// Finding is one point that fails one lint.
type Finding struct {
	// Step is the row's number in the export, counted from 1 in file order (the same number .bot run status prints).
	Step int    `json:"step"`
	Leg  int    `json:"leg"`
	Kind string `json:"kind"`
	// Lint is LintWater, LintOffFloor, LintEdge, LintLedge, LintShortHop, LintAggro or LintPatrol.
	Lint  string        `json:"lint"`
	Point dungeon.Point `json:"point"`
	// From is the point before the row (the short hop's other end); nil for a lint of the point alone.
	From   *dungeon.Point `json:"from,omitempty"`
	Detail string         `json:"detail"`
	// Stand says the point is the move row a pull, boss or hold is fought from (the rules are tighter there).
	Stand bool `json:"stand,omitempty"`
	// Severity is SeverityError (the finding fails the check) or SeverityWarning (the default).
	Severity string `json:"severity,omitempty"`
	// Bodies, Ceiling, Guids and JoinerPacks carry a joiner finding's payload.
	Bodies      int          `json:"bodies,omitempty"`
	Ceiling     int          `json:"ceiling,omitempty"`
	Guids       []uint32     `json:"guids,omitempty"`
	JoinerPacks []JoinerPack `json:"joiner_packs,omitempty"`
}

func (f Finding) String() string {
	s := fmt.Sprintf("step %d (leg %d) %.1f %.1f %.1f: %s: %s", f.Step, f.Leg, f.Point.X, f.Point.Y, f.Point.Z, f.Lint, f.Detail)
	if f.From != nil {
		s = fmt.Sprintf("%s from %.1f %.1f %.1f", s, f.From.X, f.From.Y, f.From.Z)
	}
	return s
}

// Counts is how many findings each lint has.
func Counts(fs []Finding) map[string]int {
	c := map[string]int{}
	for _, f := range fs {
		c[f.Lint]++
	}
	return c
}

// Lint checks every positioned row's move point. A pull, boss or hold row's own point is not linted (a pack centre may stand in
// water by design); its stand, the move row right before it, is.
func Lint(rows planexport.Rows, nav Nav, opt Options) ([]Finding, error) {
	if opt.Level == 0 {
		opt.Level = 60
	}
	c := &checker{nav: nav, opt: opt, rows: rows}
	c.steps = rows.Steps
	c.gather = gatherPoints(c.steps)
	c.owner = owners(c.steps, opt.World)
	var out []Finding
	for i, s := range c.steps {
		if moveKind(s.Kind) {
			p := pointOf(s)
			f, err := c.point(i, s, p)
			if err != nil {
				return nil, err
			}
			out = append(out, f...)
			if prev := c.prev(i); prev >= 0 {
				out = append(out, c.shortHop(i, s, p, pointOf(c.steps[prev]))...)
			}
		}
		if c.gather[i] {
			f, err := c.stand(i, s, pointOf(s))
			if err != nil {
				return nil, err
			}
			out = append(out, f...)
		}
		out = append(out, c.checkReach(i, s)...)
		out = append(out, c.checkHopLen(i, s)...)
		out = append(out, c.checkArrival(i, s)...)
		out = append(out, c.checkClimbAnchor(i, s)...)
		out = append(out, c.checkBothEndsWater(i, s)...)
		out = append(out, c.checkRiverBelow(i, s)...)
		out = append(out, c.checkJoiners(i, s)...)
	}
	return out, nil
}

type checker struct {
	nav    Nav
	opt    Options
	rows   planexport.Rows
	steps  []planexport.StepRow
	gather []bool
	owner  map[uint64]int // spawn guid -> step number of the pull or boss row that owns it
	// joins caches the joiner estimate: one PullSizes pass per Lint call, not one per pull row.
	joins     []PullFinding
	joinsDone bool
}

// moveKind says the row is a point the party walks to (a pull, boss or hold row is a fight, not a walk).
func moveKind(kind string) bool {
	switch kind {
	case planexport.KindMove, planexport.KindMoveDirect, planexport.KindMoveHopExact:
		return true
	}
	return false
}

// positioned says the row has a place on the map.
func positioned(s planexport.StepRow) bool { return s.X != 0 || s.Y != 0 || s.Z != 0 }

func pointOf(s planexport.StepRow) dungeon.Point { return dungeon.Point{X: s.X, Y: s.Y, Z: s.Z} }

// prev is the index of the positioned row before i, or -1.
func (c *checker) prev(i int) int {
	for k := i - 1; k >= 0; k-- {
		if positioned(c.steps[k]) {
			return k
		}
	}
	return -1
}

// gatherPoints marks the move row right before a pull, boss or hold row: the party necessarily stops there.
func gatherPoints(steps []planexport.StepRow) []bool {
	out := make([]bool, len(steps))
	for i, s := range steps {
		if !moveKind(s.Kind) {
			continue
		}
		for k := i + 1; k < len(steps); k++ {
			if !positioned(steps[k]) {
				continue
			}
			switch steps[k].Kind {
			case planexport.KindPull, planexport.KindBoss, planexport.KindHold:
				out[i] = true
			}
			break
		}
	}
	return out
}

func (c *checker) finding(i int, s planexport.StepRow, lint, detail string) Finding {
	return Finding{Step: i + 1, Leg: s.Leg, Kind: s.Kind, Lint: lint, Point: pointOf(s), Detail: detail}
}

// point lints one move point: water, off-floor, edge and ledge.
func (c *checker) point(i int, s planexport.StepRow, p dungeon.Point) ([]Finding, error) {
	var out []Finding
	n, ok, err := c.nav.Nearest(c.opt.Map, v3(p))
	if err != nil {
		return nil, err
	}
	if !ok {
		f := c.finding(i, s, LintOffFloor, "no walkable polygon within 4 yd")
		f.Severity = SeverityError
		return append(out, f), nil
	}
	if d := dist3(p, point3(n)); d > OffFloorYd {
		f := c.finding(i, s, LintOffFloor, fmt.Sprintf("the nearest floor %.1f %.1f %.1f is %.1f yd away", n[0], n[1], n[2], d))
		f.Severity = SeverityError
		out = append(out, f)
	}
	fl, err := c.nav.Floors(c.opt.Map, n[0], n[1], n[2]-6, n[2]+6)
	if err != nil {
		return nil, err
	}
	if here, ok := nearestFloor(fl, n[2]); ok && here.Flags&navmesh.NavWater != 0 {
		f := c.finding(i, s, LintWater, fmt.Sprintf("the floor %.1f %.1f %.1f is NavWater", n[0], n[1], here.Z))
		if c.gather[i] {
			f.Severity = SeverityError // a stand, an approach end or a gather anchor in water
		}
		out = append(out, f)
	}
	limit := EdgeYd
	if c.gather[i] {
		limit = StandEdgeYd
	}
	if hit, known, err := c.clearance(p); err != nil {
		return nil, err
	} else if known && hit < limit {
		out = append(out, c.finding(i, s, LintEdge, fmt.Sprintf("a mesh edge is %.1f yd away (the rule is %g yd%s)", hit, limit, standNote(c.gather[i]))))
	}
	if z, ok, err := c.ledge(p); err != nil {
		return nil, err
	} else if ok {
		out = append(out, c.finding(i, s, LintLedge, fmt.Sprintf("another floor stands %.1f yd %s within %g yd", math.Abs(float64(z-p.Z)), aboveBelow(z, p.Z), LedgeNearYd)))
	}
	f, err := c.patrol(i, s, p)
	if err != nil {
		return nil, err
	}
	if f != nil {
		out = append(out, *f)
	}
	return out, nil
}

// stand lints the move row a pull, boss or hold is fought from: the aggro of every unit the run has not pulled yet (the target
// pack left out), and every patrol lane.
func (c *checker) stand(i int, s planexport.StepRow, p dungeon.Point) ([]Finding, error) {
	var out []Finding
	if c.opt.World == nil {
		return out, nil
	}
	step := i + 1
	targets := c.targetEntries(i)
	var worst *worldspawn.Spawn
	worstMargin := math.Inf(1)
	for k := range c.opt.World.Spawns {
		sp := c.opt.World.Spawns[k]
		if !sp.Hostile() || c.patrolling(sp) {
			continue
		}
		if _, target := targets[sp.Entry]; target {
			continue // the pack this stand is for
		}
		if st, owned := c.owner[sp.GUID]; owned && st <= step {
			continue // pulled before (or at) this row
		}
		if sp.LevelMax <= 1 {
			continue // a critter (the export has no creature type): it never wakes a raid
		}
		aggro := worldspawn.AggroRadius(sp, c.opt.Level, 1)
		if aggro == 0 {
			continue // NO_AGGRO, or no detection range: it never wakes
		}
		radius := float64(aggro) + AggroMarginYd
		if sp.MovementType == 1 && sp.Wander < wanderCapYd { // a random mover wanders into the radius; the export has typos (11356 yd), the audit caps at 50
			radius += float64(sp.Wander)
		}
		margin := dist3(p, sp.Pos) - radius
		if margin < 0 && margin < worstMargin {
			worst, worstMargin = &c.opt.World.Spawns[k], margin
		}
	}
	if worst != nil {
		f := c.severityFinding(i, s, LintAggro, fmt.Sprintf("%.1f yd inside the aggro+%g yd of %s %d (spawn %d at %.1f %.1f %.1f, %.1f yd away; the run has not pulled it)", -worstMargin, AggroMarginYd, worst.Name, worst.Entry, worst.GUID, worst.Pos.X, worst.Pos.Y, worst.Pos.Z, dist3(p, worst.Pos)), SeverityError)
		f.Stand = true
		out = append(out, f)
	}
	return out, nil
}

// patrol lints a point against every patrol lane its patroller has not been pulled for.
func (c *checker) patrol(i int, s planexport.StepRow, p dungeon.Point) (*Finding, error) {
	if c.opt.World == nil {
		return nil, nil
	}
	step := i + 1
	var worst *worldspawn.Patrol
	worstMargin := math.Inf(1)
	var worstAt dungeon.Point
	for k := range c.opt.World.Patrols {
		pt := c.opt.World.Patrols[k]
		if len(pt.Path) < 2 {
			continue
		}
		if c.pulledEntry(pt.Entries, step) {
			continue
		}
		sp, ok := c.patrolSpawn(pt)
		if !ok {
			continue
		}
		aggro := worldspawn.AggroRadius(sp, c.opt.Level, 1)
		if aggro == 0 {
			continue
		}
		radius := float64(aggro) + PatrolMarginYd
		d, at := loopDist(p, pt.Path)
		if margin := d - radius; margin < 0 && margin < worstMargin {
			worst, worstMargin, worstAt = &c.opt.World.Patrols[k], margin, at
		}
	}
	if worst == nil {
		return nil, nil
	}
	f := c.finding(i, s, LintPatrol, fmt.Sprintf("%.1f yd inside the lane of %s %v (%.1f yd from its point %.1f %.1f %.1f; the rule is aggro+%g yd)", -worstMargin, worst.Name, worst.Entries, dist3(p, worstAt), worstAt.X, worstAt.Y, worstAt.Z, PatrolMarginYd))
	return &f, nil
}

// shortHop lints a hop shorter than ShortHopYd between two positioned rows.
func (c *checker) shortHop(i int, s planexport.StepRow, p, from dungeon.Point) []Finding {
	if d := dist3(from, p); d < ShortHopYd {
		f := c.finding(i, s, LintShortHop, fmt.Sprintf("%.1f yd from the row before it (under %g): the arrival radius (3 yd) covers both", d, ShortHopYd))
		f.From = &from
		return []Finding{f}
	}
	return nil
}

// clearance is the nearest mesh edge around p: the shortest hit of RayDirs rays, and whether the mesh could answer at all.
func (c *checker) clearance(p dungeon.Point) (float64, bool, error) {
	best := math.Inf(1)
	known := false
	for k := range RayDirs {
		a := 2 * math.Pi * float64(k) / RayDirs
		q := dungeon.Point{X: p.X + EdgeRayYd*float32(math.Cos(a)), Y: p.Y + EdgeRayYd*float32(math.Sin(a)), Z: p.Z}
		ray, err := c.nav.Raycast(c.opt.Map, v3(p), v3(q))
		if err != nil {
			return 0, false, err
		}
		if !ray.Known {
			continue
		}
		known = true
		if !ray.Clear && float64(ray.Hit) < best {
			best = float64(ray.Hit)
		}
	}
	if best == math.Inf(1) {
		return EdgeRayYd, known, nil
	}
	return best, known, nil
}

// ledge looks LedgeNearYd around p for a floor LedgeLowYd-LedgeHighYd above or below it.
func (c *checker) ledge(p dungeon.Point) (float32, bool, error) {
	for k := range 8 {
		a := 2 * math.Pi * float64(k) / 8
		x, y := p.X+LedgeNearYd*float32(math.Cos(a)), p.Y+LedgeNearYd*float32(math.Sin(a))
		fl, err := c.nav.Floors(c.opt.Map, x, y, p.Z-LedgeHighYd, p.Z+LedgeHighYd)
		if err != nil {
			return 0, false, err
		}
		for _, f := range fl {
			if dz := math.Abs(float64(f.Z - p.Z)); dz >= LedgeLowYd && dz <= LedgeHighYd {
				return f.Z, true, nil
			}
		}
	}
	return 0, false, nil
}

// targetEntries is the entries of the next pull or boss row at or after i.
func (c *checker) targetEntries(i int) map[uint32]bool {
	for k := i; k < len(c.steps); k++ {
		s := c.steps[k]
		if s.Kind == planexport.KindPull || s.Kind == planexport.KindBoss {
			m := map[uint32]bool{}
			for _, e := range s.Entries {
				m[e] = true
			}
			return m
		}
	}
	return map[uint32]bool{}
}

// pulledEntry says a pull or boss row at or before step lists one of the entries.
func (c *checker) pulledEntry(entries []uint32, step int) bool {
	for k := 0; k < len(c.steps) && k < step; k++ {
		s := c.steps[k]
		if s.Kind != planexport.KindPull && s.Kind != planexport.KindBoss {
			continue
		}
		for _, e := range s.Entries {
			for _, want := range entries {
				if e == want {
					return true
				}
			}
		}
	}
	return false
}

// patrolling says the spawn has a patrol lane.
func (c *checker) patrolling(s worldspawn.Spawn) bool {
	for _, pt := range c.opt.World.Patrols {
		for _, g := range pt.SpawnGUIDs {
			if g == s.GUID {
				return true
			}
		}
	}
	return false
}

// patrolSpawn is the patrol's leader spawn (the aggro radius its lane carries).
func (c *checker) patrolSpawn(pt worldspawn.Patrol) (worldspawn.Spawn, bool) {
	for _, s := range c.opt.World.Spawns {
		if len(pt.SpawnGUIDs) > 0 && s.GUID == pt.SpawnGUIDs[0] {
			return s, true
		}
		if len(pt.SpawnGUIDs) == 0 {
			for _, e := range pt.Entries {
				if s.Entry == e {
					return s, true
				}
			}
		}
	}
	return worldspawn.Spawn{}, false
}

// owners maps each spawn to the step of the first pull or boss row that takes it: the row lists its entry and stands within its
// radius (at least 3 yd) plus 3. A group member is owned with its leader (the audit's rule).
func owners(steps []planexport.StepRow, w *worldspawn.World) map[uint64]int {
	owner := map[uint64]int{}
	if w == nil {
		return owner
	}
	members := map[uint64][]uint64{}
	for _, s := range w.Spawns {
		if s.GroupLeader != 0 {
			members[s.GroupLeader] = append(members[s.GroupLeader], s.GUID)
			members[s.GroupLeader] = append(members[s.GroupLeader], s.GroupLeader)
		}
	}
	for _, sp := range w.Spawns {
		if _, ok := owner[sp.GUID]; ok {
			continue
		}
		for i, r := range steps {
			if r.Kind != planexport.KindPull && r.Kind != planexport.KindBoss {
				continue
			}
			if !hasEntry(r.Entries, sp.Entry) {
				continue
			}
			reach := float64(r.Radius)
			if reach < 3 {
				reach = 3
			}
			if dist3(pointOf(r), sp.Pos) > reach+3 {
				continue
			}
			owner[sp.GUID] = i + 1
			leader := sp.GroupLeader
			if leader == 0 {
				leader = sp.GUID
			}
			for _, g := range members[leader] {
				if _, ok := owner[g]; !ok {
					owner[g] = i + 1
				}
			}
			break
		}
	}
	return owner
}

func hasEntry(entries []uint32, e uint32) bool {
	for _, x := range entries {
		if x == e {
			return true
		}
	}
	return false
}

// nearestFloor is the floor nearest z.
func nearestFloor(fl []navmesh.Floor, z float32) (navmesh.Floor, bool) {
	best, ok := navmesh.Floor{}, false
	for _, f := range fl {
		if !ok || math.Abs(float64(f.Z-z)) < math.Abs(float64(best.Z-z)) {
			best, ok = f, true
		}
	}
	return best, ok
}

// loopDist is the 3D distance from p to the patrol's closed loop, and the loop point it is measured from.
func loopDist(p dungeon.Point, path []dungeon.Point) (float64, dungeon.Point) {
	best, at := math.Inf(1), path[0]
	for k := range path {
		a, b := path[k], path[(k+1)%len(path)]
		d, q := segDist(p, a, b)
		if d < best {
			best, at = d, q
		}
	}
	return best, at
}

// segDist is the 3D distance from p to the segment a-b, and the nearest point on it.
func segDist(p, a, b dungeon.Point) (float64, dungeon.Point) {
	vx, vy, vz := float64(b.X-a.X), float64(b.Y-a.Y), float64(b.Z-a.Z)
	px, py, pz := float64(p.X-a.X), float64(p.Y-a.Y), float64(p.Z-a.Z)
	t := 0.0
	if l := vx*vx + vy*vy + vz*vz; l > 0 {
		t = math.Max(0, math.Min(1, (px*vx+py*vy+pz*vz)/l))
	}
	q := dungeon.Point{X: a.X + float32(t)*float32(vx), Y: a.Y + float32(t)*float32(vy), Z: a.Z + float32(t)*float32(vz)}
	return dist3(p, q), q
}

func dist3(a, b dungeon.Point) float64 {
	dx, dy, dz := float64(a.X-b.X), float64(a.Y-b.Y), float64(a.Z-b.Z)
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

func v3(p dungeon.Point) navmesh.Vec3 { return navmesh.Vec3{p.X, p.Y, p.Z} }

func point3(v navmesh.Vec3) dungeon.Point { return dungeon.Point{X: v[0], Y: v[1], Z: v[2]} }

func standNote(stand bool) string {
	if stand {
		return ", tighter at a gather point"
	}
	return ""
}

func aboveBelow(z, at float32) string {
	if z > at {
		return "above"
	}
	return "below"
}

// Summary is one line per finding, for a report.
func Summary(fs []Finding) string {
	lines := make([]string, len(fs))
	for i, f := range fs {
		lines[i] = f.String()
	}
	return strings.Join(lines, "\n")
}
