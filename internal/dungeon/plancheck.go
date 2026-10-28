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
	// ReasonDetour (a warning, never a failure: the draft plans of other dungeons have many) is a pull group whose way from where the party stands is a long detour for a mob that is near: the bots' pull finds no
	// firing position ("The tank could not start moving to a firing position for the pull", live, Zul'Farrak).
	ReasonDetour = "detour"
	// ReasonOutsideZone is an area_trigger whose stand point lies outside its zone: whoever stands there takes no trigger.
	ReasonOutsideZone = "outside the zone"
	// ReasonNeedsWalk is a point the plan marks needs_walk: it was never snapped to the navmesh and its mapping walk is still to do.
	ReasonNeedsWalk = "needs a mapping walk"

	// ReasonAvoid is a point the party would stand on inside one of the plan's avoid boxes (deadly ground the navmesh calls plain ground).
	ReasonAvoid = "in an avoid box"
	// ReasonAvoidCut is a point the navmesh reaches only through one of the plan's avoid boxes: the box cuts the plan's route.
	ReasonAvoidCut = "cut by an avoid box"

	// ReasonSkipped is the reason of a hop the plan exempts (Waypoint.CheckSkip).
	ReasonSkipped = "skipped"

	// WarnNeedlessDirect is the reason of the warning on a direct hop the navmesh already joins.
	WarnNeedlessDirect = "needless direct"
	// WarnEstimatedPatrol is the reason of the warning on a patrol whose period_s is an estimate (Patrol.PeriodEstimate).
	WarnEstimatedPatrol = "estimated patrol period"

	// ReasonServerUnreachable is a hop the server's own path query refuses: the run
	// leader's CheckMove answers "unreachable" (or the path's end is short after 3
	// incomplete legs) from where the party stands, although the offline query has a
	// complete path.
	ReasonServerUnreachable = "server unreachable"
	// ReasonArriveSwallowed is a direct hop the run leader never sends: it lies within
	// the arrival radius (DungeonRunPolicy.h kArriveYd) of the point before it, so the
	// run counts it reached (WalkTo returns "arrived") and the party stays behind.
	ReasonArriveSwallowed = "hop inside the arrival radius"
	// ReasonInsideWake is a party point of a leg whose boss has a scripted wake (Boss.WakeYd) that stands inside the wake with
	// the room the fight needs: the boss wakes with the party strung out on the walk in, before its own step.
	ReasonInsideWake = "inside the boss's wake radius"
)

// ArriveYd is the run leader's arrival radius: within it of its point a move counts as
// reached without sending an order (TortoiseBots DungeonRunPolicy.h kArriveYd, WalkTo).
const ArriveYd = 3.0

// The room a scripted wake needs beyond its radius (Boss.WakeYd), in yards on top of the boss's own bounding radius
// (Boss.BoundingYd).
const (
	// WakeBotBoundYd is a bot's bounding radius: the core's proximity tests add both units' radii to the distance.
	WakeBotBoundYd = 0.5
	// WakeRingRaidYd is a raid's follow ring: the formation puts the front bots this much nearer the boss than the lead bot.
	WakeRingRaidYd = 8.0
	// WakeRingPartyYd is a party's follow ring (AiPlayerbot.RaidFollowDistance is 5; a party's bots stand nearer).
	WakeRingPartyYd = 3.0
	// WakeMarginYd is the rest of the margin.
	WakeMarginYd = 5.0
)

// ServerQuerier is the optional part of a Navmesh that replays the server's own path
// query; *navmesh.Planner implements it. The test fakes compile without it.
type ServerQuerier interface {
	ServerPath(mapID uint32, from, to navmesh.Vec3) (navmesh.ServerPath, error)
}

// Navmesh is what the plan check asks of the navmesh; navmesh.Planner is one.
type Navmesh interface {
	Plan(mapID uint32, from, to navmesh.Vec3) (navmesh.Path, error)
	Raycast(mapID uint32, from, to navmesh.Vec3) (navmesh.Ray, error)
	Nearest(mapID uint32, p navmesh.Vec3) (navmesh.Vec3, bool, error)
}

// AvoidSetter is the optional part of a Navmesh that keeps out of avoid boxes (navmesh.Planner.SetAvoid).
type AvoidSetter interface {
	SetAvoid(mapID uint32, boxes []navmesh.Box)
}

// NavBoxes is the plan's avoid boxes as the navmesh takes them.
func NavBoxes(bs []AvoidBox) []navmesh.Box {
	if len(bs) == 0 {
		return nil
	}
	out := make([]navmesh.Box, len(bs))
	for i, b := range bs {
		out[i] = navmesh.Box{Min: vec(b.Min), Max: vec(b.Max)}
	}
	return out
}

// avoided fails a point the party would stand on inside an avoid box, and says whether it did.
func (c *planChecker) avoided(part string, group int, at, from Point) bool {
	b, ok := c.p.InAvoid(at)
	if !ok {
		return false
	}
	c.fail(part, group, at, from, ReasonAvoid, "%q (%s): ground that kills although the navmesh calls it ground", b.Name, boxText(b))
	return true
}

// cutByAvoid says whether a path the navmesh does not find with the plan's avoid boxes set is found without them, and names the boxes
// that path crosses (all of them when it crosses none: the marks took a polygon the path needs). Only for a navmesh that takes the boxes.
func (c *planChecker) cutByAvoid(from, to Point) (string, bool, error) {
	a, ok := c.nav.(AvoidSetter)
	if !ok || len(c.p.Avoid) == 0 {
		return "", false, nil
	}
	a.SetAvoid(c.p.Map, nil)
	path, err := c.nav.Plan(c.p.Map, vec(from), vec(to))
	a.SetAvoid(c.p.Map, NavBoxes(c.p.Avoid))
	if err != nil || path.Status != navmesh.Complete {
		return "", false, err
	}
	var names []string
	for _, b := range c.p.Avoid {
		hit := false
		for _, seg := range append([][]navmesh.Vec3{path.Points}, path.Ground...) {
			for _, q := range seg {
				hit = hit || b.Contains(point(q))
			}
		}
		if hit {
			names = append(names, fmt.Sprintf("%q", b.Name))
		}
	}
	if len(names) == 0 {
		for _, b := range c.p.Avoid {
			names = append(names, fmt.Sprintf("%q", b.Name))
		}
	}
	return strings.Join(names, ", "), true, nil
}

// failPath records a path that is not complete: as cut by an avoid box when the navmesh finds it without the boxes, else as pathReason says.
func (c *planChecker) failPath(part string, group int, at, from Point, path navmesh.Path) error {
	names, cut, err := c.cutByAvoid(from, at)
	if err != nil {
		return err
	}
	if cut {
		c.fail(part, group, at, from, ReasonAvoidCut, "path_to is %s with the avoid boxes kept out of and complete without them: avoid box %s cuts the route (move the box's edge, or the point)", path.Status, names)
		return nil
	}
	c.fail(part, group, at, from, pathReason(path), "path_to is %s: %s", path.Status, path.Reason)
	return nil
}

// A pull group is a detour when it stands within pullDetourNear yards in a line and the path to it is longer than pullDetourFactor times that
// plus pullDetourSlack yards.
const (
	pullDetourNear   = 45.0
	pullDetourFactor = 2.0
	pullDetourSlack  = 25.0
)

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
	// Every text shows both numberings (PullGroupRef: "pull group 9 of 12 (from_pull_group 8)").
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
		where += " " + PullGroupRef(i.PullGroup-1, i.PullGroups)
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
	// RunLeader are the packs and bosses the TortoiseBots run leader would walk toward in navmesh hops of its own (Plan.RunLeaderReach,
	// reason WarnRunLeaderHop): notes for plan-export, never a failure; the agent's own driver does not walk that way.
	RunLeader []PlanIssue `json:"run_leader"`
}

// OK says the plan passed.
func (c *PlanCheck) OK() bool { return len(c.Issues) == 0 }

// Summary is one line per issue, for a refusal or a log.
func (c *PlanCheck) Summary() string { return joinIssues(c.Issues) }

// ExemptSummary is one line per hop the plan exempts.
func (c *PlanCheck) ExemptSummary() string { return joinIssues(c.Exempt) }

// RunLeaderSummary is one line per run leader hop.
func (c *PlanCheck) RunLeaderSummary() string { return joinIssues(c.RunLeader) }

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
			return fmt.Errorf("%w: leg %d (%s) has %d pull groups (from_pull_group 0-%d; dungeon_status numbers them 1-%d)", ErrCheckStart, o.FromLeg, p.Legs[o.FromLeg].Name, n, n-1, n)
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
	return checkPlanRec(p, nav, opt, look, nil)
}

// checkPlanRec is checkPlan that also keeps the stops and paths in rec (nil: it keeps nothing); TracePlan is its caller.
func checkPlanRec(p *Plan, nav Navmesh, opt CheckOptions, look lookFunc, rec *routeRec) (*PlanCheck, error) {
	if err := opt.Validate(p); err != nil {
		return nil, err
	}
	if opt.LookStandoff <= 0 {
		opt.LookStandoff = LookStandoff
	}
	if opt.LookArrive <= 0 {
		opt.LookArrive = lookArrive
	}
	// The navmesh keeps out of the plan's avoid boxes, as the run's planner does (RemoteDriver.markAvoid).
	if a, ok := nav.(AvoidSetter); ok {
		a.SetAvoid(p.Map, NavBoxes(p.Avoid))
	}
	c := &planChecker{p: p, nav: nav, lookAt: look, opt: opt, rec: rec, res: &PlanCheck{Plan: p.Name, Map: p.Map, Issues: []PlanIssue{}, Warnings: []PlanIssue{}, Exempt: []PlanIssue{}, RunLeader: []PlanIssue{}}, ends: map[string]Point{}}
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
	// A keyed object or an item need that nothing in the plan provides (plan_keys.go): the run would be refused "no-item" at the step.
	for _, g := range p.KeyGaps(opt.FromLeg) {
		c.res.Issues = append(c.res.Issues, PlanIssue{Leg: g.Leg, LegName: g.LegName, Part: g.Path, Point: g.At, Reason: "key", Detail: g.Why})
	}
	if err := c.runLeader(opt.FromLeg); err != nil {
		return nil, err
	}
	return c.res, nil
}

// runLeader checks the points only the TortoiseBots run leader walks (a pull group's or a boss's approach points, a hold's spot): each
// must be walked to from the run leader's point before it, as checkLeg walks a via point. It also warns of every pack or boss the run
// leader would walk toward in navmesh hops of its own (Plan.RunLeaderReach), in PlanCheck.RunLeader.
func (c *planChecker) runLeader(from int) error {
	prev := c.p.Entrance
	if from > 0 {
		prev, _ = c.p.LegStart(from)
	}
	var err error
	c.p.runLeaderWalk(from, func(m RunLeaderPoint) {
		if err != nil {
			return
		}
		if !strings.Contains(m.Part, "approach") && !strings.Contains(m.Part, "hold spot") {
			// Checked by checkLeg already: the approach pass starts from where
			// its walk left the party (the arrival rule or the server walk may
			// have kept it behind), not from the plan's point.
			prev = m.Point
			if at, ok := c.ends[endKey(m.Leg, m.Part)]; ok {
				prev = at
			}
			return
		}
		c.leg, c.l = m.Leg, c.p.Legs[m.Leg]
		var at Point
		if at, err = c.walk("run leader "+m.Part, m.Point, prev); err == nil {
			prev = at
		}
	}, func(int, string, Point, int, string) {})
	if err != nil {
		return err
	}
	c.res.RunLeader = append(c.res.RunLeader, c.p.RunLeaderReach(from)...)
	return nil
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
	// rec keeps the stops and paths of a route trace (nil for a plain check).
	rec *routeRec
	res *PlanCheck
	leg int
	l   Leg
	// ends is where checkLeg's walk left the party after each move row of the
	// plan, keyed "leg/part" with runLeaderWalk's part names: the run leader's
	// approach pass starts from there, not from the plan's point.
	ends map[string]Point
}

// endKey keys ends: the leg and the runLeaderWalk part name of the move row.
func endKey(leg int, part string) string { return fmt.Sprintf("%d/%s", leg, part) }

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
	is := c.issue(part, group, at, from, reason, format, a...)
	c.res.Issues = append(c.res.Issues, is)
	c.rec.fail(is)
}

// warn records a warning of the current leg.
func (c *planChecker) warn(part string, at, from Point, reason, format string, a ...any) {
	is := c.issue(part, 0, at, from, reason, format, a...)
	c.res.Warnings = append(c.res.Warnings, is)
	c.rec.warn(is)
}

// wake checks a leg's boss with a scripted wake (Boss.WakeYd): every point the party stands on before the boss fight, and every
// vertex of the path between two of them, must lie outside the wake plus the room the fight needs (both bounding radii, the follow
// ring and WakeMarginYd). The walk to the boss itself is its own pull and is not a party point. group is checkLeg's start group:
// when a run starts at a pull group, the leg's vias, `to` and the groups before it are not walked and are not checked.
func (c *planChecker) wake(cur Point, group int) {
	b := c.l.Boss
	if b == nil || b.WakeYd <= 0 {
		return
	}
	need := b.WakeYd + b.BoundingYd + WakeBotBoundYd + WakeMarginYd
	if c.p.IsRaid() {
		need += WakeRingRaidYd
	} else {
		need += WakeRingPartyYd
	}
	type stop struct {
		part  string
		group int
		at    Point
	}
	var stops []stop
	if group < 0 {
		for vi, w := range c.l.Via {
			stops = append(stops, stop{fmt.Sprintf("via %d", vi), 0, w.Point})
		}
		stops = append(stops, stop{"to", 0, c.l.To})
	}
	for gi, g := range c.l.PullGroups {
		if gi < group {
			continue
		}
		for ai, a := range g.Approach {
			stops = append(stops, stop{fmt.Sprintf("pull group %d approach %d", gi+1, ai), gi + 1, a})
		}
	}
	for ai, a := range b.Approach {
		stops = append(stops, stop{fmt.Sprintf("boss approach %d", ai), 0, a})
	}
	check := func(part string, group int, at, from Point) {
		if d := dist3(at, b.Position); d < need {
			c.fail(part, group, at, from, ReasonInsideWake, "%.1f yd from boss %d (needs %.1f)", d, b.Entry, need)
		}
	}
	prev := cur
	for i, s := range stops {
		check(s.part, s.group, s.at, prev)
		if i > 0 {
			// The path between two party points is walked in the boss's chamber too: a corner that cuts the corner of the chamber
			// would wake her where no row stands.
			if path, err := c.nav.Plan(c.p.Map, vec(prev), vec(s.at)); err == nil && len(path.Points) > 2 {
				for _, v := range path.Points[1 : len(path.Points)-1] {
					check(s.part, s.group, point(v), prev)
				}
			}
		}
		prev = s.at
	}
}

// checkLeg walks one leg from cur and returns where the party stands at its end. A leg the run enters at a pull group (group 0 or more)
// starts at that group: the leg's own via points and `to` and the groups before it are not walked.
func (c *planChecker) checkLeg(l Leg, cur Point, group int) (Point, error) {
	c.l = l
	c.rec.startLeg(c.leg)
	c.wake(cur, group)
	if group < 0 {
		for vi, w := range l.Via {
			var err error
			c.rec.stop("via", fmt.Sprintf("via %d", vi), 0, w.Point, "")
			if !w.GMCross && dist3(cur, w.Point) <= ArriveYd && dist3(cur, w.Point) > 0.05 {
				// Within the run leader's arrival radius the point counts as
				// reached: WalkTo returns "arrived" without sending an order,
				// so the party stays where it was (kArriveYd, WalkTo).
				part := fmt.Sprintf("via %d", vi)
				if w.Direct {
					part = fmt.Sprintf("direct via %d", vi)
				}
				c.ends[endKey(c.leg, part)] = cur
				if w.Direct {
					c.fail(part, 0, w.Point, cur, ReasonArriveSwallowed, "%.1f yd from the point before it: the run leader counts it reached (kArriveYd %g) and never sends the hop; the party stays at %.1f %.1f %.1f", dist3(cur, w.Point), ArriveYd, cur.X, cur.Y, cur.Z)
				}
				continue
			}
			switch {
			case w.GMCross:
				cur, err = c.gmCross(fmt.Sprintf("gm_cross via %d", vi), w.Point, cur)
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
			c.ends[endKey(c.leg, fmt.Sprintf("via %d", vi))] = cur
		}
		c.rec.stop("stage", "to", 0, l.To, "")
		if !l.NeedsWalk {
			var err error
			if cur, err = c.walk("to", l.To, cur); err != nil {
				return cur, err
			}
			c.ends[endKey(c.leg, "to")] = cur
		} else {
			c.needsWalk("to", 0, "", l.To, cur)
			cur = l.To
		}
		group = 0
	}
	for gi := group; gi < len(l.PullGroups); gi++ {
		g := l.PullGroups[gi]
		if len(g.Positions) > 0 {
			c.rec.stop("pack", "pull group look", gi+1, g.Positions[0], g.Team)
		}
		if g.Patrol != nil && g.Patrol.PeriodEstimate {
			c.warn("patrol", g.Positions[0], cur, WarnEstimatedPatrol, "pull group %d: the patrol of %v has only an estimated period_s (%.0f s): the driver does not predict its phase, it locates the patrol and waits until it is past; the walk measures the round", gi+1, g.Patrol.Entries, g.Patrol.PeriodS)
		}
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
				c.rec.note("a position %.0f yd from where the party stands is in view already: no walk", dist3(cur, at))
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
	if b := l.Boss; b != nil {
		c.rec.stop("boss", "boss look", 0, b.Position, b.Team)
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

// steps walks the party to the stand point of each step that has one (use_object, loot_object, ritual, area_trigger, gossip): the driver stands the party there
// before the step. A step without a stand stands wherever the object is approached from, which only a run can say.
func (c *planChecker) steps(kind string, steps []Step, cur Point) (Point, error) {
	for si, st := range steps {
		var stand *Point
		what := ""
		label := fmt.Sprintf("%s[%d].", kind, si)
		c.rec.stop("step", label, 0, Point{}, "") // the checks below may fail the step itself; the stop gets its place and name once they are known
		switch {
		case st.UseObject != nil:
			stand, label, what = st.UseObject.Stand, label+"use_object.stand", fmt.Sprintf("use game object %d", st.UseObject.Entry)
			if st.UseObject.ViaItem { // the item's spell is cast at the object from the stand: it has a short range
				c.useRange(label, st.UseObject.ObjectRef, stand, cur)
			}
		case st.LootObject != nil:
			stand, label, what = st.LootObject.Stand, label+"loot_object.stand", fmt.Sprintf("loot game object %d", st.LootObject.Entry)
			c.useRange(label, st.LootObject.ObjectRef, stand, cur)
		case st.Ritual != nil:
			stand, label, what = st.Ritual.Stand, label+"ritual.stand", fmt.Sprintf("ritual at game object %d", st.Ritual.Entry)
		case st.AreaTrigger != nil:
			t := st.AreaTrigger
			stand, label, what = &t.Stand, label+"area_trigger.stand", fmt.Sprintf("take area trigger %d", t.ID)
			c.trigger(label, t, cur)
		case st.Gossip != nil:
			g := st.Gossip
			stand, label, what = g.Stand, label+"gossip.stand", fmt.Sprintf("talk to NPC %d", g.NPC)
			if stand != nil && dist3(*stand, g.Near) > GossipRange {
				c.count()
				c.fail(label, 0, *stand, cur, ReasonTooFar, "%.1f yd from the NPC's spawn %.1f %.1f %.1f: the server answers a gossip hello only within %g yd", dist3(*stand, g.Near), g.Near.X, g.Near.Y, g.Near.Z, GossipRange)
			}
		}
		if stand == nil {
			c.rec.dropStop()
			continue
		}
		c.rec.placeStop(label, *stand, what)
		prev := cur
		var err error
		if cur, err = c.walk(label, *stand, cur); err != nil {
			return cur, err
		}
		if st.Gossip != nil && st.Gossip.Talker() == GossipByAgent {
			cur = prev // the agent's own character walks to it: the party stays where it was
		}
		c.ends[endKey(c.leg, label)] = cur
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
	is := c.issue(part, 0, at, from, ReasonSkipped, "%s", why)
	c.res.Exempt = append(c.res.Exempt, is)
	c.rec.skip(is)
}

// count counts a checked point (only those of the run's part of the plan).
func (c *planChecker) count() {
	c.res.Points++
}

func (c *planChecker) walk(part string, to, from Point) (Point, error) {
	c.count()
	c.rec.begin("", part, from, to)
	defer c.rec.end()
	if c.avoided(part, 0, to, from) {
		return from, nil
	}
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
	c.rec.path(path)
	// The offline query judges the hop first: when it answers anything but
	// complete the run never sends the hop, so the run leader's own query is
	// not asked. The route-trace exception stays for the Plan failure only.
	if path.Status != navmesh.Complete {
		err := c.failPath(part, 0, to, from, path)
		if c.rec != nil && err == nil && pathReason(path) == ReasonUnreachable {
			// A route trace judges each segment from where the plan means the party to stand: the point is on the mesh, so the next segment
			// starts at it and one unreachable point does not turn the segments after it red. (The plain check stays where it was.)
			return to, nil
		}
		return from, err
	}
	// The offline query takes the hop: the run leader's own query judges it
	// next (it answers 'arrived' inside the arrival radius: the run leader
	// sends NO order, so the modelled position stays where it was).
	if at, ok, err := c.serverWalk(part, 0, to, from); err != nil {
		return at, err
	} else if !ok {
		return at, nil
	} else if at != to {
		// The server answered 'arrived': the party stays where it was.
		return at, nil
	}
	return to, nil
}

// serverWalk simulates the run leader's walk of one hop with the server's own path
// query: from cur toward to, at most 3 legs (kWalkTries, DungeonRunPolicy.h:155). It
// returns where the party ends: to when the server takes it, cur (or a pocket the
// incomplete path ends in) when the server refuses it. When the navmesh is not a
// ServerQuerier, there is no server check and the hop passes.
func (c *planChecker) serverWalk(part string, group int, to, from Point) (Point, bool, error) {
	q, ok := c.nav.(ServerQuerier)
	if !ok {
		return to, true, nil
	}
	cur := from
	for range 3 {
		if dist3(cur, to) <= ArriveYd {
			// Inside the arrival radius the run leader sends NO order
			// (WalkTo returns "arrived"): the party stays where it was.
			return from, true, nil
		}
		sp, err := q.ServerPath(c.p.Map, vec(cur), vec(to))
		if err != nil {
			return cur, false, err
		}
		if v := navmesh.ServerMoveVerdict(sp, vec(cur), vec(to), 0); v != "ok" {
			c.fail(part, group, to, from, ReasonServerUnreachable, "the server's PathFinder (2048 nodes, 256 polys, smooth path, ground|water incl. steep) from %.1f %.1f %.1f answers type 0x%x: cmd move %s", cur.X, cur.Y, cur.Z, sp.Type, v)
			return cur, false, nil
		}
		if sp.Type&navmesh.PathNormal != 0 {
			return to, true, nil
		}
		cur = point(sp.ActualEnd)
	}
	c.fail(part, group, to, from, ReasonServerUnreachable, "after 3 incomplete legs the lead stands at %.1f %.1f %.1f, %.1f yd short", cur.X, cur.Y, cur.Z, dist3(cur, to))
	return cur, false, nil
}

// pathReason is the reason a path that is not complete gives: the goal off the mesh, else unreachable.
func pathReason(p navmesh.Path) string {
	if p.Status == navmesh.NoPath && strings.HasPrefix(p.Reason, "goal ") {
		return ReasonOffMesh
	}
	return ReasonUnreachable
}

// gmCross checks a gm_cross via: the point needs mesh floor under it (the party is put there and walks on from it), but no path from the
// point before it: a GM moves the party, so it is listed as exempt from the walk (ReasonSkipped), with what the move is.
func (c *planChecker) gmCross(part string, to, from Point) (Point, error) {
	c.count()
	c.rec.begin("gmcross", part, from, to)
	c.rec.straight()
	defer c.rec.end()
	if c.avoided(part, 0, to, from) {
		return to, nil
	}
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
		return to, nil
	}
	c.skip(part, to, from, fmt.Sprintf("gm_cross: a GM moves the party here (%.0f yd from the point before it); the walk starts from it", dist3(from, to)))
	return to, nil
}

// direct checks a direct via: a step within the limits of the module (DirectStepOK), ground under its end and a clear line over the mesh.
func (c *planChecker) direct(part string, to, from Point) (Point, error) {
	c.count()
	c.rec.begin("direct", part, from, to)
	c.rec.straight()
	defer c.rec.end()
	if c.avoided(part, 0, to, from) {
		return to, nil
	}
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
	c.rec.begin("", part, from, at)
	c.rec.straight() // the chord a failing look is drawn with; a path the planner answers replaces it
	defer c.rec.end()
	c.team = team
	defer func() { c.team = "" }()
	path, err := c.nav.Plan(c.p.Map, vec(from), vec(at))
	if err != nil {
		return from, err
	}
	if len(path.Points) < 2 {
		if path.Status != navmesh.Complete { // complete with one point: the party is at the pack already
			return from, c.failPath(part, group, at, from, path)
		}
		c.rec.lookPath(nil, from, false)
		return from, nil
	}
	pts := make([]Point, len(path.Points))
	for i, v := range path.Points {
		pts[i] = point(v)
	}
	end := pts[len(pts)-1]
	// A pack that is near but whose way is a long detour (another layer: a mob under a bridge, on a ledge) leaves the tank no firing position
	// within the ranged pull range: it answers unreachable. Only pull group looks (a boss is walked to).
	if part == "pull group look" && path.Status == navmesh.Complete {
		if straight := dist3(from, at); straight <= pullDetourNear && float64(path.Length) > pullDetourFactor*straight+pullDetourSlack {
			is := c.issue(part, group, at, from, ReasonDetour, "the pack is %.0f yd away but the way to it is %.0f yd: the tank finds no firing position within the ranged pull range (it answers unreachable); stand the party on the pack's own layer", straight, path.Length)
			c.res.Warnings = append(c.res.Warnings, is)
			c.rec.warn(is)
		}
	}
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
	c.rec.lookPath(pts, stand, moved)
	if b, ok := c.p.InAvoid(stand); ok {
		c.fail(part, group, at, from, ReasonAvoid, "the look point %.1f %.1f %.1f lies in avoid box %q (%s)", stand.X, stand.Y, stand.Z, b.Name, boxText(b))
	}
	// The lead bot may stop LookArrive short of the look point, and the view is ClearSeeRadius: the look point must leave room for both.
	if d := dist3(stand, at); d > ClearSeeRadius-float64(c.opt.LookArrive) {
		c.fail(part, group, at, from, ReasonTooFar, "the look point %.1f %.1f %.1f is %.0f yd from it (the view is %d yd, less %g for the lead bot's stop): no point of the path comes within the look view of the pack (it ends %.1f %.1f %.1f, %.1f yd short of it)", stand.X, stand.Y, stand.Z, d, ClearSeeRadius, c.opt.LookArrive, end.X, end.Y, end.Z, dist3(end, at))
	}
	return stand, nil
}
