package routeoracle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
)

// PlanName is the strict basename a plan's name must take, in the plan file and on the
// command line (the same regex the CLI's --plan flags hold): the name is also the file stem
// the report is written under, so a name read from a plan JSON ("../escape") must never
// reach a path.
var PlanName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// CheckName says why the plan name is not one a report may be written under.
func CheckName(name string) error {
	if !PlanName.MatchString(name) {
		return fmt.Errorf("plan name %q is not a plan name (a-z, 0-9, _ and -, at most 40 characters)", name)
	}
	return nil
}

// Run is one live planning of a walk: what the navmesh answered for it.
type Run struct {
	Status    string  `json:"status"`     // complete, partial, no_path; or error, when the planner failed
	EndGapYd  float64 `json:"end_gap_yd"` // from the path's end to the walk's goal
	LiveLenYd float64 `json:"live_len_yd"`
	// End is where the path ends: the walk's goal, or, on a partial path, the nearest point
	// to it the mesh reaches — where the party is left standing.
	End    dungeon.Point `json:"end,omitempty"`
	Reason string        `json:"reason,omitempty"`
}

// Segment is one stored hop of a leg: from the previous stored walk point to this one, with
// the two navmesh plans of the same hop. A leg's segments are the hops the plan stores, in
// the order planexport writes the points, so the live route goes through exactly the points
// the plan does.
type Segment struct {
	Seq  int    `json:"seq"`  // the leg's hops, from 1
	Kind string `json:"kind"` // via, to, approach, pull, hold, stand, boss_approach, boss
	// Group is the pull group the hop belongs to (the hop into the group's approach points
	// or its pull centre); -1 for the leg's shared walk. A group's corridor membership is
	// said against the route without its own hops.
	Group       int           `json:"group,omitempty"`
	From        dungeon.Point `json:"from"`
	At          dungeon.Point `json:"at"`
	StoredLenYd float64       `json:"stored_len_yd"` // the straight hop between the two stored points
	Plain       Run           `json:"plain"`
	Avoid       Run           `json:"avoid"`
	// Ratio is the stored hop over the avoid run's live length: the mesh detour the hop takes.
	Ratio float64 `json:"ratio"`
	// Note says the hop kind beyond the plain walk: a direct hop walked in a line, a
	// gm_cross landing a GM moves the party to, or a hop the plan's check_skip exempts.
	Note string `json:"note,omitempty"`
	// NonComparable: planned from the point the partial path before it reached, after the
	// leg's break, so the walk is not the one the plan stores and its numbers score nothing.
	NonComparable bool `json:"non_comparable,omitempty"`
}

// StoredPoint is a stored walk point of a leg and how far the leg's live route passes from it.
type StoredPoint struct {
	Seq    int           `json:"seq"` // the point's place among the leg's walk points, from 1
	Kind   string        `json:"kind"`
	At     dungeon.Point `json:"at"`
	NearYd float64       `json:"near_yd"`
	// Redundant: the route would lose nothing without the point — the navmesh path from the
	// previous to the next stored point passes within RedundantYd of it. Said only of the
	// approach and via points (a to, a stand and a boss are the leg's objectives, not hops
	// the route may skip).
	Redundant bool `json:"redundant"`
}

// BreakTouched is a break waypoint of a leg (a via with exact/direct/gm_cross/check_skip)
// and whether the leg's live route touched it.
type BreakTouched struct {
	Via     int           `json:"via"`           // the waypoint's index in the leg's via, from 0
	Leg     *int          `json:"leg,omitempty"` // boss-only mode: the stored leg it belongs to (a pointer: leg 0 is written)
	Kind    string        `json:"kind"`
	At      dungeon.Point `json:"at"`
	Touched bool          `json:"touched"`
	Why     string        `json:"why,omitempty"`
}

// PullOnPath is a pull group and whether the leg's live route's corridor holds one of its
// positions.
type PullOnPath struct {
	Group    int      `json:"group"`
	Leg      *int     `json:"leg,omitempty"` // boss-only mode: the stored leg the group belongs to (a pointer: leg 0 is written; absent outside a folded row)
	OnPath   bool     `json:"on_path"`
	NearYd   float64  `json:"near_yd"` // horizontal distance of the nearest position to the route
	DZYd     float64  `json:"dz_yd"`
	RadiusYd float64  `json:"radius_yd"`
	Entries  []uint32 `json:"entries"`
}

// PackPositions is the positions a pull group is judged by: its mob positions, else a hold's
// spot. A group with neither has nothing to pull or to walk to: it is EMPTY, left out of the
// legs' walk and out of every pack count (the reports count them).
func PackPositions(g *dungeon.PullGroup) []dungeon.Point {
	if len(g.Positions) > 0 {
		return g.Positions
	}
	if g.Hold != nil {
		return []dungeon.Point{g.Hold.Spot}
	}
	return nil
}

// GroupOnRoute is the one definition of "a pack is on the route" both modes use: one of the
// group's positions lies within the group's radius plus CorridorSlackYd of the route
// horizontally and no more than CorridorMaxDZYd above or below it. ok is false for an empty
// group.
func GroupOnRoute(gi int, g *dungeon.PullGroup, route []Vec3) (pg PullOnPath, ok bool) {
	pos := PackPositions(g)
	if len(pos) == 0 {
		return PullOnPath{}, false
	}
	radius := g.Radius
	if radius <= 0 {
		radius = DefaultPackRadius
	}
	pg = PullOnPath{Group: gi, RadiusYd: float64(radius), Entries: g.Entries}
	if len(route) == 0 {
		return pg, true
	}
	for i := range pos {
		near := NearestPolyline(route, vec(pos[i]))
		// NearYd is the minimum over ALL positions, whether or not the group is on the path yet.
		if i == 0 || near.Dist2D < pg.NearYd {
			pg.NearYd, pg.DZYd = near.Dist2D, near.DZ
		}
		if OnCorridor(near, float64(radius)) {
			pg.OnPath = true
		}
	}
	return pg, true
}

// emptyGroups counts the plan's pull groups with no position and no hold.
func emptyGroups(plan *dungeon.Plan) int {
	n := 0
	for li := range plan.Legs {
		for gi := range plan.Legs[li].PullGroups {
			if len(PackPositions(&plan.Legs[li].PullGroups[gi])) == 0 {
				n++
			}
		}
	}
	return n
}

// LegReport is one leg's comparison of the stored route and the live route.
type LegReport struct {
	Leg  int           `json:"leg"`
	Name string        `json:"name,omitempty"`
	From dungeon.Point `json:"from"` // where the leg's route starts (the previous leg's objective, or the entrance)
	To   dungeon.Point `json:"to"`   // the leg's objective
	// Objective says what the leg's objective point is: boss, to, step (a leg without a boss
	// or to: its last step's point) or none (nothing stored for the leg).
	Objective string `json:"objective"`
	// Status is the leg's own: complete when every comparable segment plans complete, the
	// first failing segment's status (partial, no_path) when one does not, or indeterminate
	// when a leg before it failed and nothing is planned here.
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	// Plain and Avoid sum the comparable segments' runs; the avoid ones are the scratch
	// planner with SetAvoid of the plan's avoid boxes, whose paths are the live route.
	Plain    Run       `json:"plain"`
	Avoid    Run       `json:"avoid"`
	Segments []Segment `json:"segments,omitempty"`

	StoredLenYd  float64        `json:"stored_len_yd"` // the comparable segments' stored hops together
	Ratio        float64        `json:"ratio"`         // stored over live (the avoid run's)
	StoredMaxYd  float64        `json:"stored_max_yd"` // the stored point farthest from the live route
	StoredMeanYd float64        `json:"stored_mean_yd"`
	ZJumps       []ZJump        `json:"z_jumps,omitempty"`
	Breaks       []BreakTouched `json:"breaks,omitempty"`
	Pulls        []PullOnPath   `json:"pulls,omitempty"`
	Stored       []StoredPoint  `json:"stored,omitempty"`

	BreaksTouched int     `json:"breaks_touched"`
	PacksOnPath   int     `json:"packs_on_path"`
	PlanMs        float64 `json:"plan_ms"` // the leg's plans together
}

// TileFile is one mmap or mmtile file the report's planning used.
type TileFile struct {
	Name   string `json:"name"`
	Sha256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// Report is one plan's oracle report.
type Report struct {
	Plan   string      `json:"plan"`
	Map    uint32      `json:"map"`
	Status string      `json:"status,omitempty"` // the plan's own status line
	Avoid  int         `json:"avoid_boxes"`
	Legs   []LegReport `json:"legs"`
	// EmptyGroups: pull groups with no position and no hold, skipped everywhere (not walked to,
	// not in a pack count).
	EmptyGroups int        `json:"empty_groups,omitempty"`
	PlanMs      float64    `json:"plan_ms"` // the legs' plans together
	Tiles       []TileFile `json:"tiles"`
}

// Summary is the plan's one summary line. The aggregates are over the COMPLETE legs only: a
// leg with a partial or no-path hop is failed (its route is not the one the plan stores, and
// the point a partial path ends at would poison every number behind it), and the legs after a
// failed one are indeterminate. The mean ratio, the packs and the counts are theirs alone.
func (r Report) Summary() string {
	var complete, failed, indet, other int
	var ratio, on, packs float64
	for i := range r.Legs {
		switch r.Legs[i].Status {
		case "complete":
			complete++
		case "indeterminate":
			indet++
		case string(navmesh.Partial), string(navmesh.NoPath), "no_objective":
			failed++
		default:
			other++
		}
		if r.Legs[i].Status == "complete" {
			ratio += r.Legs[i].Ratio
			on += float64(r.Legs[i].PacksOnPath)
			packs += float64(len(r.Legs[i].Pulls))
		}
	}
	n := len(r.Legs)
	if n == 0 {
		return fmt.Sprintf("%s: map %d, no legs", r.Plan, r.Map)
	}
	var packsOn string
	if packs > 0 {
		packsOn = fmt.Sprintf(", packs on path %d/%d (%.0f%%)", int(on), int(packs), 100*on/packs)
	}
	mean := 0.0
	if complete > 0 {
		mean = ratio / float64(complete)
	}
	empty := ""
	if r.EmptyGroups > 0 {
		empty = fmt.Sprintf("; %d empty pull groups skipped", r.EmptyGroups)
	}
	return fmt.Sprintf("%s: map %d, %d legs: %d complete, %d failed, %d indeterminate; mean ratio %.2f%s%s; plan %.0f ms",
		r.Plan, r.Map, n, complete, failed, indet, mean, packsOn, empty, r.PlanMs)
}

// Oracle builds the plan's report. The walk it plans is the driver's own, leg by leg: the
// leg's stored points in the order planexport writes them (the via points, the To, each pull
// group's approach points and its pull point — the positions' centroid, or a hold's spot —,
// the steps' stand points, the boss's approach points and position, the after-boss stands),
// from where the previous leg left the party. Every hop of that walk is planned twice: plain,
// and on a scratch planner with the plan's avoid boxes, whose paths are the live route the
// metrics use, because it is the one a run plans with.
//
// A hop that does not plan complete fails the leg: the leg keeps the failing hop's status
// (partial, no_path), its remaining hops are planned on from the point the failed path
// reached and marked non-comparable, and the legs after it are indeterminate — the failed
// leg's and their numbers score nothing (they are still in the report, for the reading).
//
// A planner error (no folder, no engine, a broken tile) is returned, not reported.
func Oracle(p *navmesh.Planner, mmapsDir string, plan *dungeon.Plan) (Report, error) {
	if err := CheckName(plan.Name); err != nil {
		return Report{}, fmt.Errorf("plan file: %w", err)
	}
	rep := Report{Plan: plan.Name, Map: plan.Map, Status: plan.Status, Avoid: len(plan.Avoid), EmptyGroups: emptyGroups(plan)}
	scratch := p.Scratch()
	defer scratch.Close()
	scratch.SetAvoid(plan.Map, dungeon.NavBoxes(plan.Avoid))

	cur := vec(plan.Entrance) // where the driver actually stands (a partial path ends short of its goal)
	broken := ""              // why nothing after the failed leg is comparable
	for i := range plan.Legs {
		l := &plan.Legs[i]
		lr := LegReport{Leg: i, Name: l.Name, From: point(cur)}
		obj, kind := legObjective(l)
		lr.To = obj
		lr.Objective = kind

		switch {
		case broken != "":
			lr.Status, lr.Reason = "indeterminate", broken
			rep.Legs = append(rep.Legs, lr)
			continue
		case kind == "none":
			lr.Status = "no_objective"
			rep.Legs = append(rep.Legs, lr)
			continue
		}

		walk := legWalk(l)
		lr.Stored = make([]StoredPoint, len(walk))
		for j, w := range walk {
			lr.Stored[j] = StoredPoint{Seq: j + 1, Kind: w.kind, At: point(w.at)}
		}
		paths := make([]navmesh.Path, len(walk))
		failing := -1
		var failingEnd Vec3
		for j, w := range walk {
			seg := Segment{Seq: j + 1, Kind: w.kind, Group: w.group, From: point(cur), At: point(w.at), StoredLenYd: dist3(cur, w.at)}
			seg.NonComparable = failing >= 0
			t1 := time.Now()
			plain, _, note, err := hop(plan.Map, p, cur, w)
			if err != nil {
				return rep, fmt.Errorf("plan %s leg %d (%s) hop %d: %s plan: %w", plan.Name, i, l.Name, j+1, note, err)
			}
			aware, anext, note, err := hop(plan.Map, scratch, cur, w)
			if err != nil {
				return rep, fmt.Errorf("plan %s leg %d (%s) hop %d: %s plan: %w", plan.Name, i, l.Name, j+1, note, err)
			}
			seg.Plain, seg.Avoid, seg.Note = runOf(plain, w.at), runOf(aware, w.at), note
			if seg.Avoid.LiveLenYd > 0 {
				seg.Ratio = seg.StoredLenYd / seg.Avoid.LiveLenYd
			}
			lr.PlanMs += float64(time.Since(t1).Microseconds()) / 1000
			cur = anext
			paths[j] = aware
			lr.Segments = append(lr.Segments, seg)
			if failing < 0 && seg.Avoid.Status != string(navmesh.Complete) && seg.Avoid.Status != hopSkipped {
				failing = j
				failingEnd = anext
			}
		}
		sumLeg(&lr, l, failing, paths, failingEnd)
		if failing >= 0 {
			f := &lr.Segments[failing]
			broken = fmt.Sprintf("leg %d (%s) failed: hop %d (%s) is %s: %s", i, l.Name, f.Seq, f.Kind,
				f.Avoid.Status, firstLine(f.Avoid.Reason))
		}
		if err := markRedundant(plan.Map, scratch, walk, &lr); err != nil {
			return rep, fmt.Errorf("plan %s leg %d (%s): %w", plan.Name, i, l.Name, err)
		}
		for i := range lr.Breaks {
			if lr.Breaks[i].Touched {
				lr.BreaksTouched++
			}
		}
		for i := range lr.Pulls {
			if lr.Pulls[i].OnPath {
				lr.PacksOnPath++
			}
		}
		rep.PlanMs += lr.PlanMs
		rep.Legs = append(rep.Legs, lr)
	}
	t, err := tiles(p, scratch, mmapsDir, plan)
	rep.Tiles = t
	if err != nil {
		return rep, err
	}
	return rep, nil
}

// sumLeg sums the comparable segments into the leg's own run, measures the stored points,
// the breaks, the pull groups and the Z jumps against the leg's live route, and says the
// leg's status. failingEnd is the point the failing hop's path reached (the party's spot on
// a partial path).
func sumLeg(lr *LegReport, l *dungeon.Leg, failing int, paths []navmesh.Path, failingEnd Vec3) {
	var stored, plain, live, lastGap float64
	var route, samples []Vec3
	for i := range lr.Segments {
		seg := &lr.Segments[i]
		samples = append(samples, PathSamples(paths[i])...)
		if seg.NonComparable {
			continue
		}
		route = append(route, paths[i].Points...)
		live += seg.Avoid.LiveLenYd
		plain += seg.Plain.LiveLenYd
		stored += seg.StoredLenYd
		lastGap = seg.Avoid.EndGapYd
	}
	failingPartial := failing >= 0 && paths[failing].Status == navmesh.Partial
	switch {
	case failing >= 0:
		f := &lr.Segments[failing]
		lr.Status = f.Avoid.Status
		lr.Reason = fmt.Sprintf("hop %d (%s) is %s: %s", f.Seq, f.Kind, f.Avoid.Status, firstLine(f.Avoid.Reason))
		lr.Avoid = f.Avoid
		lr.Plain = f.Plain
	default:
		lr.Status = "complete"
		lr.Avoid = Run{Status: "complete", EndGapYd: lastGap, LiveLenYd: live}
		lr.Plain = Run{Status: "complete", EndGapYd: lastGap, LiveLenYd: plain}
	}
	lr.StoredLenYd = stored
	if live > 0 {
		lr.Ratio = stored / live
	}
	if len(route) > 0 {
		var sum, most float64
		for i := range lr.Stored {
			near := NearestPolyline(route, vec(lr.Stored[i].At))
			lr.Stored[i].NearYd = near.Dist3D
			sum += near.Dist3D
			if near.Dist3D > most {
				most = near.Dist3D
			}
		}
		lr.StoredMeanYd = sum / float64(len(lr.Stored))
		lr.StoredMaxYd = most
	}
	lr.ZJumps = ZJumps(samples)
	lr.Breaks = breaksOf(l, route, failingEnd, failingPartial)
	lr.Pulls = pullGroupsOf(l, paths, lr.Segments)
}

// hop plans one hop of the leg's walk the way the dungeon driver takes it (plancheck's
// walks): a plain waypoint on the navmesh; a direct waypoint as a straight line within the
// module's limits, with ground under its end and a clear line over the mesh; a gm_cross
// waypoint as a landing the party needs mesh floor under, with no walk looked for (a GM
// moves the party there); and a hop the plan's check_skip exempts is not walked on the mesh
// at all. next is where the party stands afterwards: the path's end for a plain walk (its
// start again when no path plans), the landing point for the three break kinds.
func hop(mapID uint32, pl *navmesh.Planner, cur Vec3, spec storedSpec) (path navmesh.Path, next Vec3, note string, err error) {
	next = cur
	// A pull group's centre and a boss are looked at, the way plancheck's look is tolerant:
	// the party stands at the pull's standoff when a complete path plans, or when the
	// planned path's end comes within the pull's 25 yd view of the anchor — a pack on a
	// ledge or under a bridge ends the path short of it and is pulled fine live.
	switch spec.kind {
	case "pull", "boss":
		path, err = pl.Plan(mapID, cur, spec.at)
		if err != nil {
			return navmesh.Path{}, next, "look", err
		}
		end := pathEndOr(path, cur)
		if path.Status != navmesh.Complete && dist3(end, spec.at) > dungeon.ClearSeeRadius {
			return path, end, "look", nil
		}
		if pts := pathPoints(path); len(pts) >= 2 {
			stand, moved := dungeon.LookPoint(pts, point(spec.at), lookStandoffYd, lookStandoffYd,
				func(p dungeon.Point) dungeon.Point {
					if n, ok, _ := pl.Nearest(mapID, vec(p)); ok {
						return point(n)
					}
					return p
				}, path.OffMesh)
			if moved {
				next = vec(stand)
			}
		}
		path.Status = navmesh.Complete
		return path, next, "look", nil
	}
	if spec.via == nil {
		path, err = pl.Plan(mapID, cur, spec.at)
		if n := len(path.Points); n > 0 {
			next = path.Points[n-1]
		}
		return path, next, "", err
	}
	w := spec.via
	switch {
	case w.GMCross:
		n, ok, err := pl.Nearest(mapID, spec.at)
		if err != nil {
			return navmesh.Path{}, next, "gm_cross", err
		}
		if !ok || dist3(n, spec.at) > hopFloorYd {
			return navmesh.Path{Status: navmesh.NoPath, Reason: fmt.Sprintf(
				"the gm_cross landing %s is off the navmesh (no walkable polygon within %.1f yd)",
				fmtPos(spec.at), hopFloorYd)}, spec.at, "gm_cross", nil
		}
		return navmesh.Path{Status: navmesh.Complete}, spec.at, "gm_cross (a GM moves the party here; no walk)", nil
	case w.CheckSkip != "":
		return navmesh.Path{Status: hopSkipped}, spec.at, "skipped (the plan's check_skip: the mesh cannot judge the hop on purpose)", nil
	case w.Direct:
		if !dungeon.DirectStepOK(point(cur), point(spec.at)) {
			return navmesh.Path{Status: navmesh.NoPath, Reason: fmt.Sprintf(
					"%.1f yd across and %.1f up or down: the direct limits are %g and %g",
					dist2(cur, spec.at), math.Abs(float64(spec.at[2]-cur[2])), dungeon.DirectMaxDistance, dungeon.DirectMaxVertical)},
				spec.at, "direct", nil
		}
		n, ok, err := pl.Nearest(mapID, spec.at)
		if err != nil {
			return navmesh.Path{}, spec.at, "direct", err
		}
		if !ok || dist3(n, spec.at) > dungeon.DirectGroundTolerance {
			return navmesh.Path{Status: navmesh.NoPath, Reason: fmt.Sprintf(
					"no ground within %g yd under the landing %s", dungeon.DirectGroundTolerance, fmtPos(spec.at))},
				spec.at, "direct", nil
		}
		ray, err := pl.Raycast(mapID, cur, spec.at)
		if err != nil {
			return navmesh.Path{}, spec.at, "direct", err
		}
		if !ray.Known || float64(ray.Hit) < dist2(cur, spec.at)-checkRayEndYd {
			return navmesh.Path{Status: navmesh.NoPath, Reason: fmt.Sprintf(
					"the line from %s leaves the mesh after %.1f of %.1f yd", fmtPos(cur), ray.Hit, dist2(cur, spec.at))},
				spec.at, "direct", nil
		}
		return navmesh.Path{Status: navmesh.Complete, Points: []Vec3{cur, spec.at}, Length: float32(dist3(cur, spec.at))},
			spec.at, "direct (walked in a line)", nil
	}
	// A plain via: walked like the leg's other points.
	path, err = pl.Plan(mapID, cur, spec.at)
	if n := len(path.Points); n > 0 {
		next = path.Points[n-1]
	}
	return path, next, "", err
}

// The hop check's own limits, in yards: the plan check's walk and ray tolerances
// (plancheck.go's checkWalkDZ, checkWalkDH and checkRayEnd), and the driver's pull standoff
// (driver.go's look_standoff default).
const (
	hopFloorYd     = 2.5
	checkRayEndYd  = 1.5
	lookStandoffYd = 21.0
)

// hopSkipped is a hop the oracle does not measure: the plan exempts it (check_skip) or a GM
// moves the party past it.
const hopSkipped = "skipped"

// pathPoints is a planned path's corners as the plan format's points.
func pathPoints(p navmesh.Path) []dungeon.Point {
	out := make([]dungeon.Point, len(p.Points))
	for i, v := range p.Points {
		out[i] = point(v)
	}
	return out
}

// pathEndOr is where a planned path ends, or the fallback when it holds no corners.
func pathEndOr(p navmesh.Path, fallback Vec3) Vec3 {
	if n := len(p.Points); n > 0 {
		return p.Points[n-1]
	}
	return fallback
}

// fmtPos is a position for a message.
func fmtPos(v Vec3) string { return fmt.Sprintf("%.1f %.1f %.1f", v[0], v[1], v[2]) }

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return s
}

// runOf turns a planned path into a Run.
func runOf(p navmesh.Path, goal Vec3) Run {
	r := Run{Status: string(p.Status), LiveLenYd: float64(p.Length), Reason: p.Reason}
	if n := len(p.Points); n > 0 {
		r.End = point(p.Points[n-1])
		r.EndGapYd = dist3(p.Points[n-1], goal)
	}
	return r
}

// breaksOf lists the leg's break waypoints (a via with exact/direct/gm_cross/check_skip) and
// whether the leg's live route touched them: within BreakNearYd of the foot, or, when the
// failing hop is partial, with the path's end within BreakGapYd of the foot.
func breaksOf(l *dungeon.Leg, route []Vec3, failingEnd Vec3, failingPartial bool) []BreakTouched {
	var out []BreakTouched
	for vi := range l.Via {
		w := l.Via[vi]
		kind := breakKind(w)
		if kind == "" {
			continue
		}
		b := BreakTouched{Via: vi, Kind: kind, At: w.Point}
		if len(route) > 0 {
			near := NearestPolyline(route, vec(w.Point))
			if near.Dist3D <= BreakNearYd {
				b.Touched = true
				b.Why = fmt.Sprintf("the route passes %.1f yd from it", near.Dist3D)
			}
		}
		if !b.Touched && failingPartial && len(route) > 0 {
			if d := dist3(failingEnd, vec(w.Point)); d <= BreakGapYd {
				b.Touched = true
				b.Why = fmt.Sprintf("the partial path ends %.1f yd from it", d)
			}
		}
		out = append(out, b)
	}
	return out
}

// breakKind is the waypoint's break flags, comma joined; "" when it is none.
func breakKind(w dungeon.Waypoint) string {
	var parts []string
	if w.Exact {
		parts = append(parts, "exact")
	}
	if w.Direct {
		parts = append(parts, "direct")
	}
	if w.GMCross {
		parts = append(parts, "gm_cross")
	}
	if w.CheckSkip != "" {
		parts = append(parts, "check_skip")
	}
	return strings.Join(parts, ",")
}

// pullGroupsOf is the leg's pull groups and whether the leg's live route passes one of the
// group's positions: within the group's radius plus CorridorSlackYd of the route
// horizontally, no more than CorridorMaxDZYd above or below the route at that point. The
// route measured is the one the driver walks anyway — every hop but the group's own (its
// approach hops and the hop into its pull centre) — so a group the party would pass without
// the stored waypoints counts as on the route.
func pullGroupsOf(l *dungeon.Leg, paths []navmesh.Path, segs []Segment) []PullOnPath {
	var out []PullOnPath
	for gi := range l.PullGroups {
		g := &l.PullGroups[gi]
		var route []Vec3
		for i := range segs {
			if segs[i].Group != gi && len(paths[i].Points) > 0 {
				route = append(route, paths[i].Points...)
			}
		}
		pg, ok := GroupOnRoute(gi, g, route)
		if !ok {
			continue // an empty group: not in the pack counts
		}
		out = append(out, pg)
	}
	return out
}

// markRedundant says, for each stored approach or via point of a complete leg, whether the
// route would lose nothing without it: the navmesh path from the previous to the next stored
// point (the avoid-aware one) passes within RedundantYd of the point. The leg's first and
// last walk points have no such pair of neighbours and are never redundant.
func markRedundant(mapID uint32, sc *navmesh.Planner, walk []storedSpec, lr *LegReport) error {
	for i := range lr.Stored {
		s := &lr.Stored[i]
		if !isApproachVia(s.Kind) || i == 0 || i == len(walk)-1 {
			continue
		}
		skip, err := sc.Plan(mapID, walk[i-1].at, walk[i+1].at)
		if err != nil {
			return fmt.Errorf("hop %d redundancy plan: %w", i+1, err)
		}
		if skip.Status == navmesh.NoPath || len(skip.Points) == 0 {
			continue
		}
		s.Redundant = Redundant(NearestPolyline(skip.Points, walk[i].at))
	}
	return nil
}

// isApproachVia says whether the stored point is an approach or via point (the redundancy
// check's kind; a boss's approach is one too).
func isApproachVia(kind string) bool {
	return kind == "approach" || kind == "via" || kind == "boss_approach"
}

// legObjective is the leg's objective chain point: the boss's position, else the leg's To,
// else the last step's point, else nothing.
func legObjective(l *dungeon.Leg) (dungeon.Point, string) {
	if l.Boss != nil {
		return l.Boss.Position, "boss"
	}
	if l.To != (dungeon.Point{}) {
		return l.To, "to"
	}
	if p, ok := lastStepPoint(l.Steps); ok {
		return p, "step"
	}
	if p, ok := lastStepPoint(l.AfterBoss); ok {
		return p, "step"
	}
	return dungeon.Point{}, "none"
}

// lastStepPoint is the last step of the list that holds a stand point.
func lastStepPoint(steps []dungeon.Step) (dungeon.Point, bool) {
	for i := len(steps) - 1; i >= 0; i-- {
		if p, ok := stepPoint(steps[i]); ok {
			return p, true
		}
	}
	return dungeon.Point{}, false
}

// stepPoint is a step's stand point: where the step wants the party standing (an object's or
// trigger's stand, else the point the object stands at).
func stepPoint(st dungeon.Step) (dungeon.Point, bool) {
	switch {
	case st.UseObject != nil:
		return standOrNear(st.UseObject.Stand, st.UseObject.Near)
	case st.LootObject != nil:
		return standOrNear(st.LootObject.Stand, st.LootObject.Near)
	case st.Ritual != nil:
		return standOrNear(st.Ritual.Stand, &st.Ritual.Position)
	case st.Gossip != nil:
		return standOrNear(st.Gossip.Stand, &st.Gossip.Near)
	case st.AreaTrigger != nil:
		return st.AreaTrigger.Stand, true
	}
	return dungeon.Point{}, false
}

func standOrNear(stand, near *dungeon.Point) (dungeon.Point, bool) {
	if stand != nil {
		return *stand, true
	}
	if near != nil {
		return *near, true
	}
	return dungeon.Point{}, false
}

// storedSpec is one stored point of the leg's walk; group is the pull group it belongs to
// (its approach points and pull point), -1 for the shared walk (via, to, stands, boss). via
// holds the waypoint a via point was stored from, with its direct/gm_cross/check_skip flags.
type storedSpec struct {
	at    Vec3
	kind  string
	group int
	via   *dungeon.Waypoint
}

// legWalk is the leg's stored walk: its own points in the order planexport writes them — the
// via waypoints, the To, each pull group's approach points and its pull point (the
// positions' centroid, or a hold's spot), the steps' stand points, the boss's approach
// points and position, the after-boss stands. The first point is reached from where the
// previous leg left the party; a leg without any of these has no walk.
func legWalk(l *dungeon.Leg) []storedSpec {
	var out []storedSpec
	add := func(p dungeon.Point, kind string, group int) *storedSpec {
		out = append(out, storedSpec{at: vec(p), kind: kind, group: group})
		return &out[len(out)-1]
	}
	for i := range l.Via {
		add(l.Via[i].Point, "via", -1).via = &l.Via[i]
	}
	if l.To != (dungeon.Point{}) {
		add(l.To, "to", -1)
	}
	for gi := range l.PullGroups {
		g := &l.PullGroups[gi]
		if len(PackPositions(g)) == 0 {
			continue // an empty group has nothing to pull: no walk to it (the centroid of nothing is NaN)
		}
		for i := range g.Approach {
			add(g.Approach[i], "approach", gi)
		}
		if g.Hold != nil {
			add(g.Hold.Spot, "hold", gi)
			continue
		}
		add(centroid(g.Positions), "pull", gi)
	}
	for i := range l.Steps {
		if p, ok := stepPoint(l.Steps[i]); ok {
			add(p, "stand", -1)
		}
	}
	if l.Boss != nil {
		for i := range l.Boss.Approach {
			add(l.Boss.Approach[i], "boss_approach", -1)
		}
		add(l.Boss.Position, "boss", -1)
	}
	for i := range l.AfterBoss {
		if p, ok := stepPoint(l.AfterBoss[i]); ok {
			add(p, "stand", -1)
		}
	}
	return out
}

// centroid is the mean of the points, as planexport writes a pull row.
func centroid(pts []dungeon.Point) dungeon.Point {
	var cx, cy, cz float64
	for i := range pts {
		cx, cy, cz = cx+float64(pts[i].X), cy+float64(pts[i].Y), cz+float64(pts[i].Z)
	}
	n := float64(len(pts))
	return dungeon.Point{X: float32(cx / n), Y: float32(cy / n), Z: float32(cz / n)}
}

func vec(p dungeon.Point) Vec3   { return Vec3{p.X, p.Y, p.Z} }
func point(v Vec3) dungeon.Point { return dungeon.Point{X: v[0], Y: v[1], Z: v[2]} }

// tiles is the mmap and mmtile files the report's planning used, with their SHA-256 and
// sizes: the two planners' loaded tiles together and the map's own .mmap. A file that cannot
// be read is an error: the report would name files it does not have.
func tiles(p, scratch *navmesh.Planner, dir string, plan *dungeon.Plan) ([]TileFile, error) {
	used := map[string]bool{}
	for _, n := range p.Tiles(plan.Map) {
		used[n] = true
	}
	for _, n := range scratch.Tiles(plan.Map) {
		used[n] = true
	}
	used[fmt.Sprintf("%03d.mmap", plan.Map)] = true
	var out []TileFile
	var first error
	for n := range used {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			if first == nil {
				first = fmt.Errorf("tile %s: %w", n, err)
			}
			continue
		}
		sum := sha256.Sum256(b)
		out = append(out, TileFile{Name: n, Sha256: hex.EncodeToString(sum[:]), Bytes: int64(len(b))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, first
}
