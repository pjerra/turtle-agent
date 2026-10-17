package dungeon

import (
	"fmt"
	"math"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

// The route trace: the plan check's walk, with the paths kept. CheckPlan asks the navmesh for the way from each point of the plan to the next
// (the walk between legs included, and each pack's walk to its look point) and keeps only the verdict; TracePlan runs the same walk through the
// same code and keeps what the planner answered, so the route editor can draw the path the bots take and flag the segments the planner cannot
// path. The stops are numbered in run order across the whole plan.

// RouteStop is one place of the run, in the order the driver goes there: a via point, a leg's stage point (`to`), a pull group (at its first
// position), a step's stand point, or the boss.
type RouteStop struct {
	Seq   int    `json:"seq"` // 1-based, across the whole plan
	Leg   int    `json:"leg"`
	Kind  string `json:"kind"`            // via, stage, pack, step, boss
	Part  string `json:"part"`            // the plan check's name for it: "via 2", "to", "pull group look", "steps[1].use_object.stand", "boss look"
	Group int    `json:"group,omitempty"` // 1-based pull group of a pack stop
	At    Point  `json:"at"`              // where the stop is: the point, the pack's first position, the boss's position
	Text  string `json:"text,omitempty"`  // what happens at a step ("use game object 1234")
	Team  string `json:"team,omitempty"`  // a pack or boss gated to a team: looked at, not fought, by the other team's run
	// Issues are the plan check's failures of the stop itself (a needs_walk point, a stand outside its trigger zone); a segment's own failure is on
	// the segment. Warnings are the check's warnings on it.
	Issues   []string `json:"issues,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	// Notes say what the walk did not need ("already in view of the pack: no walk").
	Notes []string `json:"notes,omitempty"`
}

// RouteSeg is the walk to a stop, or a part of it (a pack with several positions is looked at from one place after the other).
type RouteSeg struct {
	Stop int    `json:"stop"` // Seq of the stop it walks to
	Leg  int    `json:"leg"`
	Kind string `json:"kind"` // the stop's kind, or direct (a straight step), gmcross (a GM moves the party) or skip (a hop the plan exempts)
	Part string `json:"part"`
	// Link: the first walk of a leg, from where the party stood at the end of the leg before (from the entrance for the first leg).
	Link bool  `json:"link,omitempty"`
	From Point `json:"from"`
	To   Point `json:"to"`
	// Path is the navmesh path the planner answered (up to the look point for a pack or the boss); empty for a segment with no path.
	Path   []Point `json:"path,omitempty"`
	Length float32 `json:"length"`
	OK     bool    `json:"ok"`
	Reason string  `json:"reason,omitempty"`
	Detail string  `json:"detail,omitempty"`
	// Exempt: the plan exempts the hop from the check (Waypoint.CheckSkip, or a gm_cross); Detail is the plan's reason.
	Exempt   bool     `json:"exempt,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// RouteTrace is the result of TracePlan.
type RouteTrace struct {
	Plan  string      `json:"plan"`
	Map   uint32      `json:"map"`
	Stops []RouteStop `json:"stops"`
	Segs  []RouteSeg  `json:"segs"`
	Check *PlanCheck  `json:"-"`
}

// TracePlan walks the whole plan like CheckPlan does (the driver's default look) and returns the stops and the paths. The error is for what the
// walk cannot answer (no navmesh engine, no files for the map), as for CheckPlan.
func TracePlan(p *Plan, nav Navmesh) (*RouteTrace, error) {
	return TracePlanWith(p, nav, CheckOptions{})
}

// TracePlanWith is TracePlan with the options of the check (the run's tuned look, a start inside the plan).
func TracePlanWith(p *Plan, nav Navmesh, opt CheckOptions) (*RouteTrace, error) {
	rec := &routeRec{open: -1}
	res, err := checkPlanRec(p, nav, opt, LookPoint, rec)
	if err != nil {
		return nil, err
	}
	return &RouteTrace{Plan: p.Name, Map: p.Map, Stops: rec.stops, Segs: rec.segs, Check: res}, nil
}

// routeRec collects the stops and segments while the plan checker walks. Every method is safe on a nil receiver: the plain check records nothing.
type routeRec struct {
	stops    []RouteStop
	segs     []RouteSeg
	open     int // the segment being walked (index in segs), -1 for none
	leg      int
	legFirst bool // the next segment is the first of its leg
}

// startLeg says the checker begins leg i.
func (r *routeRec) startLeg(i int) {
	if r == nil {
		return
	}
	r.leg, r.legFirst = i, true
}

// stop opens a stop; the segments that follow walk to it.
func (r *routeRec) stop(kind, part string, group int, at Point, team string) {
	if r == nil {
		return
	}
	r.stops = append(r.stops, RouteStop{Seq: len(r.stops) + 1, Leg: r.leg, Kind: kind, Part: part, Group: group, At: at, Team: team})
}

// note adds a note to the current stop.
func (r *routeRec) note(format string, a ...any) {
	if r == nil || len(r.stops) == 0 {
		return
	}
	s := &r.stops[len(r.stops)-1]
	s.Notes = append(s.Notes, fmt.Sprintf(format, a...))
}

// begin opens the segment from `from` to `to` for the current stop. kind "" takes the stop's kind. The segment starts as a straight, successful
// one; path and the checker's failures fill it in.
func (r *routeRec) begin(kind, part string, from, to Point) {
	if r == nil || len(r.stops) == 0 {
		return
	}
	st := r.stops[len(r.stops)-1]
	if kind == "" {
		kind = st.Kind
	}
	r.segs = append(r.segs, RouteSeg{Stop: st.Seq, Leg: r.leg, Kind: kind, Part: part, Link: r.legFirst, From: from, To: to, OK: true})
	r.legFirst = false
	r.open = len(r.segs) - 1
}

// end closes the open segment.
func (r *routeRec) end() {
	if r != nil {
		r.open = -1
	}
}

func (r *routeRec) seg() *RouteSeg {
	if r == nil || r.open < 0 || r.open >= len(r.segs) {
		return nil
	}
	return &r.segs[r.open]
}

// straight makes the open segment the straight line from its start to its end (a direct step, or the chord drawn for a segment with no path).
func (r *routeRec) straight() {
	if s := r.seg(); s != nil {
		s.Path, s.Length = []Point{s.From, s.To}, float32(dist3(s.From, s.To))
	}
}

// path keeps the planner's answer on the open segment.
func (r *routeRec) path(pth navmesh.Path) {
	s := r.seg()
	if s == nil {
		return
	}
	s.Path = make([]Point, len(pth.Points))
	for i, v := range pth.Points {
		s.Path[i] = point(v)
	}
	s.Length = pth.Length
}

// lookPath keeps the part of the planner's path the lead bot walks to look at a pack: from the start up to the look point. A lead bot that
// stays where it is (the pack is in view already) has a path of one point.
func (r *routeRec) lookPath(pts []Point, stand Point, moved bool) {
	s := r.seg()
	if s == nil {
		return
	}
	s.To = stand
	if !moved {
		s.Path, s.Length = []Point{s.From}, 0
		return
	}
	s.Path = cutPath(pts, stand)
	s.Length = openPathLen(s.Path)
}

// fail records a failing point: on the open segment when there is one, else on the current stop.
func (r *routeRec) fail(is PlanIssue) {
	if r == nil {
		return
	}
	if s := r.seg(); s != nil {
		if s.OK {
			s.OK, s.Reason, s.Detail = false, is.Reason, is.Detail
			return
		}
		s.Detail += "; " + is.Reason + ": " + is.Detail
		return
	}
	if len(r.stops) > 0 {
		st := &r.stops[len(r.stops)-1]
		st.Issues = append(st.Issues, is.Reason+": "+is.Detail)
	}
}

// warn records a warning on the open segment, else on the current stop.
func (r *routeRec) warn(is PlanIssue) {
	if r == nil {
		return
	}
	text := is.Reason + ": " + is.Detail
	if s := r.seg(); s != nil {
		s.Warnings = append(s.Warnings, text)
	} else if len(r.stops) > 0 {
		st := &r.stops[len(r.stops)-1]
		st.Warnings = append(st.Warnings, text)
	}
}

// skip records a hop the plan exempts: on the open segment (a gm_cross) or as a segment of its own (a check_skip hop).
func (r *routeRec) skip(is PlanIssue) {
	if r == nil {
		return
	}
	if s := r.seg(); s != nil {
		s.Exempt, s.Detail = true, is.Detail
		return
	}
	r.begin("skip", is.Part, is.From, is.Point)
	r.straight()
	if s := r.seg(); s != nil {
		s.Exempt, s.Detail = true, is.Detail
	}
	r.end()
}

// cutPath is the part of a path from its start to the point stand, which lies on it (or next to it): the points before the segment stand is on,
// then stand.
func cutPath(pts []Point, stand Point) []Point {
	if len(pts) < 2 {
		return []Point{stand}
	}
	best, at := math.Inf(1), 1
	for i := 1; i < len(pts); i++ {
		if d := distToSegment(stand, pts[i-1], pts[i]); d < best-1e-6 {
			best, at = d, i
		}
	}
	out := append([]Point{}, pts[:at]...)
	return append(out, stand)
}

func openPathLen(pts []Point) float32 {
	var n float64
	for i := 1; i < len(pts); i++ {
		n += dist3(pts[i-1], pts[i])
	}
	return float32(n)
}

// placeStop gives the current stop its place and the check's name for it.
func (r *routeRec) placeStop(part string, at Point, what string) {
	if r == nil || len(r.stops) == 0 {
		return
	}
	s := &r.stops[len(r.stops)-1]
	s.Part, s.At, s.Text = part, at, what
}

// dropStop takes the current stop back: a step with no stand point has no place on the map.
func (r *routeRec) dropStop() {
	if r != nil && len(r.stops) > 0 {
		r.stops = r.stops[:len(r.stops)-1]
	}
}
