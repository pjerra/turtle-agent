package dungeon

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

// The plan check (issue #187): before a run, walk every point of a plan on the navmesh the run itself plans on, so that a point the
// driver cannot reach, or a pack it cannot see, is found in seconds instead of by a stopped run. It follows the order the driver goes:
// the walk from the previous leg's end to each via point and to the leg's `to` (a direct via is a straight step: within the limits of
// DirectStepOK, ground under its end and a clear line: a direct step walks the straight line, so the navmesh ray is the test), then every
// pull group in order and the boss. A pack is looked at from LookPoint (what confirmGone's approach walks to), which the check wants
// within ClearSeeRadius of the pack. It casts no ray to a pack: navmesh.Planner.Raycast is only a walkability stand-in for line of sight
// (the server's vmaps are not here), and it called most of Zul'Farrak's packs blind, which were pulled fine live. A direct hop over ground
// the navmesh already joins with a straight path is a warning, not a failure: a plain walk does the same and survives a fight.

// The reasons a plan point fails.
const (
	ReasonUnreachable = "unreachable"
	ReasonNoLOS       = "no line of sight"
	ReasonTooFar      = "too far"
	ReasonOffMesh     = "off the mesh"
	// ReasonOutsideZone is an area_trigger whose stand point lies outside its zone: whoever stands there takes no trigger.
	ReasonOutsideZone = "outside the zone"
	// ReasonNeedsWalk is a point the plan marks needs_walk: it was never snapped to the navmesh and its mapping walk is still to do.
	ReasonNeedsWalk = "needs a mapping walk"

	// ReasonSkipped is the reason of a hop the plan exempts (Waypoint.CheckSkip).
	ReasonSkipped = "skipped"

	// WarnNeedlessDirect is the reason of the warning on a direct hop the navmesh already joins.
	WarnNeedlessDirect = "needless direct"
)

// Navmesh is what the plan check asks of the navmesh; navmesh.Planner is one.
type Navmesh interface {
	Plan(mapID uint32, from, to navmesh.Vec3) (navmesh.Path, error)
	Raycast(mapID uint32, from, to navmesh.Vec3) (navmesh.Ray, error)
	Nearest(mapID uint32, p navmesh.Vec3) (navmesh.Vec3, bool, error)
}

// The tolerances of the check, in yards.
const (
	// checkWalkDZ and checkWalkDH: a walk point needs mesh floor this near it (up or down, sideways).
	checkWalkDZ = 3.0
	checkWalkDH = 2.5
	// checkStraight: a path up to this many times the straight distance (plus checkStraightSlack yards) counts as nearly straight.
	checkStraight      = 1.15
	checkStraightSlack = 1.0
	// checkRayEnd is how far short of its end a direct hop's ray may stop and still reach it (a unit stands on the mesh's edge as often as inside it).
	checkRayEnd = 1.5
)

// PlanIssue is one failing point of a plan.
type PlanIssue struct {
	Leg     int    `json:"leg"`
	LegName string `json:"leg_name,omitempty"`
	// Part says what the point is: "via 2", "direct via 1", "to", "pull group look" or "boss look".
	Part string `json:"part"`
	// PullGroup and PullGroups number the pack the way dungeon_status does ("pull group 9 of 12"); 0 for a point that is not a pack's.
	PullGroup  int   `json:"pull_group,omitempty"`
	PullGroups int   `json:"pull_groups,omitempty"`
	Point      Point `json:"point"`
	// From is where the walk to the point starts (the previous point of the plan, or the stand point of the previous pack).
	From   Point  `json:"from"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
	// Team is the team a pull group or boss is gated to ("horde"). The other team's run still walks to look at it (gatedLook).
	Team string `json:"team,omitempty"`
}

func (i PlanIssue) String() string {
	where := fmt.Sprintf("leg %d", i.Leg)
	if i.LegName != "" {
		where += " (" + i.LegName + ")"
	}
	if i.PullGroup > 0 {
		where += fmt.Sprintf(" pull group %d of %d", i.PullGroup, i.PullGroups)
	}
	if i.Team != "" {
		where += " [" + i.Team + " only]"
	}
	s := fmt.Sprintf("%s %s %.1f %.1f %.1f: %s", where, i.Part, i.Point.X, i.Point.Y, i.Point.Z, i.Reason)
	if i.Detail != "" {
		s += " (" + i.Detail + ")"
	}
	return s
}

// PlanCheck is the result of CheckPlan.
type PlanCheck struct {
	Plan string `json:"plan"`
	Map  uint32 `json:"map"`
	// Points is how many plan points were checked; needs_walk points are left out (the generator could not snap them to the mesh).
	Points int         `json:"points"`
	Issues []PlanIssue `json:"issues"`
	// Warnings are points that work but are fragile (WarnNeedlessDirect); they never fail the plan.
	Warnings []PlanIssue `json:"warnings"`
	// Exempt are the hops the plan exempts from the check (Waypoint.CheckSkip); Detail is the plan's reason.
	Exempt []PlanIssue `json:"exempt"`
}

// OK says the plan passed.
func (c *PlanCheck) OK() bool { return len(c.Issues) == 0 }

// Summary is one line per issue, for a refusal or a log.
func (c *PlanCheck) Summary() string { return joinIssues(c.Issues) }

// ExemptSummary is one line per hop the plan exempts.
func (c *PlanCheck) ExemptSummary() string { return joinIssues(c.Exempt) }

// WarningSummary is one line per warning.
func (c *PlanCheck) WarningSummary() string { return joinIssues(c.Warnings) }

func joinIssues(is []PlanIssue) string {
	lines := make([]string, len(is))
	for i, x := range is {
		lines[i] = x.String()
	}
	return strings.Join(lines, "\n")
}

// ErrCheckStart is the error of a check asked to start at a leg or pull group the plan does not have.
var ErrCheckStart = errors.New("plan check: bad start")

// CheckOptions are the choices of a plan check. A run that starts inside the plan is checked from where the driver puts the party: at
// Plan.LegStart (the end of the leg before) for a leg, at Plan.GatherSpot for a pull group; the earlier part of the plan is not walked.
// The zero value checks the whole plan with the driver's default look. A pull group or boss gated to a team is checked like any other:
// the remote driver looks at a part of the other team (gatedLook, the same walk to its positions) before it skips the fight, and its issues
// carry the team.
type CheckOptions struct {
	// FromLeg and FromPullGroup (0-based, as dungeon_run takes them) say where the run starts. With a pull group (nil: none given; 0 is the
	// leg's first group) the run starts at the group: the leg's own via points and `to` and the groups before it are not checked.
	FromLeg       int
	FromPullGroup *int
	// LookStandoff and LookArrive are the run's tuned look (tune.go: look_standoff, look_arrive); 0 takes the defaults.
	LookStandoff, LookArrive float32
}

// Validate says whether the plan has the leg and pull group the check is to start at (an ErrCheckStart error when not).
func (o CheckOptions) Validate(p *Plan) error {
	if o.FromLeg < 0 || o.FromLeg >= len(p.Legs) {
		return fmt.Errorf("%w: leg %d: the plan has %d legs (numbers 0-%d)", ErrCheckStart, o.FromLeg, len(p.Legs), len(p.Legs)-1)
	}
	if g := o.FromPullGroup; g != nil {
		if n := len(p.Legs[o.FromLeg].PullGroups); *g < 0 || *g >= n {
			return fmt.Errorf("%w: pull group %d: leg %d (%s) has %d pull groups (numbers 0-%d)", ErrCheckStart, *g, o.FromLeg, p.Legs[o.FromLeg].Name, n, n-1)
		}
	}
	return nil
}

// CheckPlan walks the plan from leg fromLeg on.
// The error is for what the check cannot answer (no navmesh engine, no files for the map): the plan is then unchecked, not failed.
func CheckPlan(p *Plan, nav Navmesh, fromLeg int) (*PlanCheck, error) {
	return CheckPlanWith(p, nav, CheckOptions{FromLeg: fromLeg})
}

// CheckPlanWith is CheckPlan with options.
func CheckPlanWith(p *Plan, nav Navmesh, opt CheckOptions) (*PlanCheck, error) {
	return checkPlan(p, nav, opt, LookPoint)
}

// checkPlan is CheckPlanWith with the look point function given (the tests run the old one, StandoffPoint alone, on the old plan).
func checkPlan(p *Plan, nav Navmesh, opt CheckOptions, look lookFunc) (*PlanCheck, error) {
	if err := opt.Validate(p); err != nil {
		return nil, err
	}
	if opt.LookStandoff <= 0 {
		opt.LookStandoff = LookStandoff
	}
	if opt.LookArrive <= 0 {
		opt.LookArrive = lookArrive
	}
	c := &planChecker{p: p, nav: nav, lookAt: look, opt: opt, res: &PlanCheck{Plan: p.Name, Map: p.Map, Issues: []PlanIssue{}, Warnings: []PlanIssue{}, Exempt: []PlanIssue{}}}
	cur, group := p.Entrance, -1
	switch {
	case opt.FromPullGroup != nil:
		// The commander's own move puts the party at the group's gather spot (RemoteDriver.startPoint: the same function).
		group = *opt.FromPullGroup
		cur, _ = p.GatherSpot(opt.FromLeg, group, c.planPoints)
	case opt.FromLeg > 0:
		cur, _ = p.LegStart(opt.FromLeg)
	}
	if c.pathErr != nil {
		return nil, c.pathErr
	}
	for i := opt.FromLeg; i < len(p.Legs); i++ {
		c.leg = i
		var err error
		if cur, err = c.checkLeg(p.Legs[i], cur, group); err != nil {
			return nil, err
		}
		group = -1
	}
	return c.res, nil
}

// lookFunc finds where the lead bot stands to look at a pack: LookPoint's signature.
type lookFunc func(path []Point, pack Point, standoff, view float32, snap func(Point) Point) (Point, bool)

// planPoints is GatherSpot's path function over the navmesh.
func (c *planChecker) planPoints(from, to Point) []Point {
	path, err := c.nav.Plan(c.p.Map, vec(from), vec(to))
	if err != nil {
		if c.pathErr == nil {
			c.pathErr = err
		}
		return nil
	}
	pts := make([]Point, len(path.Points))
	for i, v := range path.Points {
		pts[i] = point(v)
	}
	return pts
}

type planChecker struct {
	p      *Plan
	nav    Navmesh
	lookAt lookFunc
	opt    CheckOptions
	// pathErr is the first navmesh error of planPoints (GatherSpot takes a function without an error).
	pathErr error
	// team is the team of the pack or boss being looked at (set on its issues).
	team string
	res  *PlanCheck
	leg  int
	l    Leg
}

func vec(p Point) navmesh.Vec3 { return navmesh.Vec3{p.X, p.Y, p.Z} }

func point(v navmesh.Vec3) Point { return Point{v[0], v[1], v[2]} }

// issue builds an issue of the current leg.
func (c *planChecker) issue(part string, group int, at, from Point, reason, format string, a ...any) PlanIssue {
	is := PlanIssue{Leg: c.leg, LegName: c.l.Name, Part: part, Point: at, From: from, Reason: reason, Detail: fmt.Sprintf(format, a...)}
	if group > 0 {
		is.PullGroup, is.PullGroups = group, len(c.l.PullGroups)
	}
	is.Team = c.team
	return is
}

// fail records a failing point of the current leg.
func (c *planChecker) fail(part string, group int, at, from Point, reason, format string, a ...any) {
	c.res.Issues = append(c.res.Issues, c.issue(part, group, at, from, reason, format, a...))
}

// warn records a warning of the current leg.
func (c *planChecker) warn(part string, at, from Point, reason, format string, a ...any) {
	c.res.Warnings = append(c.res.Warnings, c.issue(part, 0, at, from, reason, format, a...))
}

// checkLeg walks one leg from cur and returns where the party stands at its end. A leg the run enters at a pull group (group 0 or more)
// starts at that group: the leg's own via points and `to` and the groups before it are not walked.
func (c *planChecker) checkLeg(l Leg, cur Point, group int) (Point, error) {
	c.l = l
	if group < 0 {
		for vi, w := range l.Via {
			var err error
			switch {
			case w.CheckSkip != "":
				c.skip(fmt.Sprintf("via %d", vi), w.Point, cur, w.CheckSkip)
				cur = w.Point
			case w.Direct:
				cur, err = c.direct(fmt.Sprintf("direct via %d", vi), w.Point, cur)
			default:
				cur, err = c.walk(fmt.Sprintf("via %d", vi), w.Point, cur)
			}
			if err != nil {
				return cur, err
			}
		}
		if !l.NeedsWalk {
			var err error
			if cur, err = c.walk("to", l.To, cur); err != nil {
				return cur, err
			}
		} else {
			c.needsWalk("to", 0, "", l.To, cur)
			cur = l.To
		}
		group = 0
	}
	for gi := group; gi < len(l.PullGroups); gi++ {
		g := l.PullGroups[gi]
		if g.NeedsWalk {
			c.needsWalk("pull group look", gi+1, g.Team, g.Positions[0], cur)
			continue
		}
		// The driver looks at the positions in order, each look starting where the one before it ended (confirmGone), and the next group
		// goes on from the last of them. A position within ClearSeeRadius of the party needs no look: a living bot that far from it that
		// sees none is the evidence (anchorGone).
		for _, at := range g.Positions {
			if dist3(cur, at) <= ClearSeeRadius {
				c.count()
				continue
			}
			stand, err := c.look("pull group look", gi+1, g.Team, at, cur)
			if err != nil {
				return cur, err
			}
			cur = stand
		}
	}
	var err error
	if cur, err = c.steps("steps", l.Steps, cur); err != nil {
		return cur, err
	}
	if b := l.Boss; b != nil && b.NeedsWalk {
		c.needsWalk("boss look", 0, b.Team, b.Position, cur)
	} else if b != nil {
		stand, err := c.look("boss look", 0, b.Team, b.Position, cur)
		if err != nil {
			return cur, err
		}
		cur = stand
	}
	return c.steps("after_boss", l.AfterBoss, cur)
}

// trigger checks the geometry of an area_trigger step: its stand point inside the zone (a trigger is taken only by someone standing in it),
// and the leg's boss, if there is one, within TriggerMaxStandToBoss of the stand (the trigger starts that fight: Zum'rah turns hostile
// where the party stands). The stand's walk is checked with the other stands.
func (c *planChecker) trigger(part string, t *AreaTrigger, from Point) {
	if d := dist3(t.Stand, t.Center); d > float64(t.Radius) {
		c.fail(part, 0, t.Stand, from, ReasonOutsideZone, "the stand is %.1f yd from the centre %.1f %.1f %.1f of trigger %d; its radius is %g", d, t.Center.X, t.Center.Y, t.Center.Z, t.ID, t.Radius)
	}
	if b := c.l.Boss; b != nil {
		if d := dist3(t.Stand, b.Position); d > TriggerMaxStandToBoss {
			c.fail(part, 0, t.Stand, from, ReasonTooFar, "boss %d stands %.1f yd from the stand (at most %g): the trigger would start a fight the party is not at", b.Entry, d, TriggerMaxStandToBoss)
		}
	}
}

// useRange checks that the stand point of a loot_object step (a chest) is within the use range (UseRange) of the object's position: a chest the
// party stands farther from than that is not opened. A use_object is not measured: a gong or a door is used from farther than a chest is. A step with no stand or no position has nothing to measure.
func (c *planChecker) useRange(part string, ref ObjectRef, stand *Point, from Point) {
	if stand == nil || ref.Near == nil {
		return
	}
	if d := dist3(*stand, *ref.Near); d > UseRange {
		c.fail(part, 0, *stand, from, ReasonTooFar, "the stand is %.1f yd from game object %d at %.1f %.1f %.1f: the use range is %g yd", d, ref.Entry, ref.Near.X, ref.Near.Y, ref.Near.Z, UseRange)
	}
}

// steps walks the party to the stand point of each step that has one (use_object, loot_object, ritual): the driver stands the party there
// before the step. A step without a stand stands wherever the object is approached from, which only a run can say.
func (c *planChecker) steps(kind string, steps []Step, cur Point) (Point, error) {
	for si, st := range steps {
		var stand *Point
		label := fmt.Sprintf("%s[%d].", kind, si)
		switch {
		case st.UseObject != nil:
			stand, label = st.UseObject.Stand, label+"use_object.stand"
		case st.LootObject != nil:
			stand, label = st.LootObject.Stand, label+"loot_object.stand"
			c.useRange(label, st.LootObject.ObjectRef, stand, cur)
		case st.Ritual != nil:
			stand, label = st.Ritual.Stand, label+"ritual.stand"
		case st.AreaTrigger != nil:
			t := st.AreaTrigger
			stand, label = &t.Stand, label+"area_trigger.stand"
			c.trigger(label, t, cur)
		}
		if stand == nil {
			continue
		}
		var err error
		if cur, err = c.walk(label, *stand, cur); err != nil {
			return cur, err
		}
	}
	return cur, nil
}

// needsWalk records a point the plan marks needs_walk: the generator could not snap it to the navmesh, so nothing about it is checked and a
// live-checked plan must not carry one.
func (c *planChecker) needsWalk(part string, group int, team string, at, from Point) {
	c.team = team
	c.fail(part, group, at, from, ReasonNeedsWalk, "the plan marks it needs_walk: its mapping walk is still to do")
	c.team = ""
}

// skip records a hop the plan exempts from the check.
func (c *planChecker) skip(part string, at, from Point, why string) {
	c.res.Exempt = append(c.res.Exempt, c.issue(part, 0, at, from, ReasonSkipped, "%s", why))
}

// count counts a checked point (only those of the run's part of the plan).
func (c *planChecker) count() {
	c.res.Points++
}

// walk checks a plain waypoint: mesh floor under it and a complete navmesh path to it. It returns the point the party is at afterwards.
func (c *planChecker) walk(part string, to, from Point) (Point, error) {
	c.count()
	n, ok, err := c.nav.Nearest(c.p.Map, vec(to))
	if err != nil {
		return from, err
	}
	if dz, dh := math.Abs(float64(n[2]-to.Z)), math.Hypot(float64(n[0]-to.X), float64(n[1]-to.Y)); !ok || dz > checkWalkDZ || dh > checkWalkDH {
		if ok {
			c.fail(part, 0, to, from, ReasonOffMesh, "the nearest floor %.1f %.1f %.1f is %.1f yd above or below it and %.1f yd off", n[0], n[1], n[2], dz, dh)
		} else {
			c.fail(part, 0, to, from, ReasonOffMesh, "no walkable polygon within 4 yd")
		}
		return from, nil
	}
	path, err := c.nav.Plan(c.p.Map, vec(from), vec(to))
	if err != nil {
		return from, err
	}
	if path.Status != navmesh.Complete {
		c.fail(part, 0, to, from, pathReason(path), "path_to is %s: %s", path.Status, path.Reason)
		return from, nil
	}
	return to, nil
}

// pathReason is the reason a path that is not complete gives: the goal off the mesh, else unreachable.
func pathReason(p navmesh.Path) string {
	if p.Status == navmesh.NoPath && strings.HasPrefix(p.Reason, "goal ") {
		return ReasonOffMesh
	}
	return ReasonUnreachable
}

// direct checks a direct via: a step within the limits of the module (DirectStepOK), ground under its end and a clear line over the mesh.
func (c *planChecker) direct(part string, to, from Point) (Point, error) {
	c.count()
	if !DirectStepOK(from, to) {
		c.fail(part, 0, to, from, ReasonTooFar, "%.1f yd across and %.1f up or down: the limits are %g and %g", hdist(from, to), math.Abs(float64(to.Z-from.Z)), DirectMaxDistance, DirectMaxVertical)
		return to, nil
	}
	n, ok, err := c.nav.Nearest(c.p.Map, vec(to))
	if err != nil {
		return from, err
	}
	if dz := math.Abs(float64(n[2] - to.Z)); !ok || dz > DirectGroundTolerance || math.Hypot(float64(n[0]-to.X), float64(n[1]-to.Y)) > checkWalkDH {
		c.fail(part, 0, to, from, ReasonOffMesh, "no ground within %g yd under it", DirectGroundTolerance)
		return to, nil
	}
	if clear, hit, err := c.line(from, to); err != nil {
		return from, err
	} else if !clear {
		c.fail(part, 0, to, from, ReasonNoLOS, "the line from %.1f %.1f %.1f leaves the mesh after %.1f of %.1f yd", from.X, from.Y, from.Z, hit, hdist(from, to))
		return to, nil
	}
	// The hop is fine; is it needed? A navmesh that joins the two points with a straight path walks it as a plain waypoint, which can be
	// sent again from wherever a fight leaves the lead bot (a direct step needs the lead bot at its start and in sight of its end).
	path, err := c.nav.Plan(c.p.Map, vec(from), vec(to))
	if err != nil {
		return from, err
	}
	if straight := dist3(from, to); path.Status == navmesh.Complete && float64(path.Length) <= straight*checkStraight+checkStraightSlack {
		c.warn(part, to, from, WarnNeedlessDirect, "the navmesh joins it with a complete path of %.1f yd (%.1f yd in a line): drop the direct flag", path.Length, straight)
	}
	return to, nil
}

// line casts the walkability ray from one point to the other.
func (c *planChecker) line(from, to Point) (clear bool, hit float64, err error) {
	ray, err := c.nav.Raycast(c.p.Map, vec(from), vec(to))
	if err != nil {
		return false, 0, err
	}
	if !ray.Known {
		return false, 0, nil // the start is off the mesh or a tile is missing: the walk checks already say so
	}
	if ray.Clear {
		return true, 0, nil
	}
	return float64(ray.Hit) >= hdist(from, to)-checkRayEnd, float64(ray.Hit), nil
}

// look checks a pack's position (or the boss's): the path from where the party stands must reach it, or end within the look view of it, and
// the look point on it (LookPoint) must be within ClearSeeRadius of the pack. It returns the look point (the party's place afterwards;
// `from` when the path is shorter than the standoff, as the driver does not move then).
func (c *planChecker) look(part string, group int, team string, at, from Point) (Point, error) {
	c.count()
	c.team = team
	defer func() { c.team = "" }()
	path, err := c.nav.Plan(c.p.Map, vec(from), vec(at))
	if err != nil {
		return from, err
	}
	if len(path.Points) < 2 {
		if path.Status != navmesh.Complete { // complete with one point: the party is at the pack already
			c.fail(part, group, at, from, pathReason(path), "path_to is %s: %s", path.Status, path.Reason)
		}
		return from, nil
	}
	pts := make([]Point, len(path.Points))
	for i, v := range path.Points {
		pts[i] = point(v)
	}
	end := pts[len(pts)-1]
	view := lookView(c.opt.LookStandoff, c.opt.LookArrive)
	var snapErr error
	snap := func(p Point) Point { // the same floor snap the driver passes (FloorSnapper)
		n, ok, err := c.nav.Nearest(c.p.Map, vec(p))
		if err != nil {
			snapErr = err
		}
		if !ok {
			return p
		}
		return point(n)
	}
	stand, moved := c.lookAt(pts, at, c.opt.LookStandoff, view, snap)
	if snapErr != nil {
		return from, snapErr
	}
	if !moved {
		stand = from
	}
	// The lead bot may stop LookArrive short of the look point, and the view is ClearSeeRadius: the look point must leave room for both.
	if d := dist3(stand, at); d > ClearSeeRadius-float64(c.opt.LookArrive) {
		c.fail(part, group, at, from, ReasonTooFar, "the look point %.1f %.1f %.1f is %.0f yd from it (the view is %d yd, less %g for the lead bot's stop): no point of the path comes within the look view of the pack (it ends %.1f %.1f %.1f, %.1f yd short of it)", stand.X, stand.Y, stand.Z, d, ClearSeeRadius, c.opt.LookArrive, end.X, end.Y, end.Z, dist3(end, at))
	}
	return stand, nil
}
