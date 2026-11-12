// The scout's offline rules, added to the placement lints so plan-check and plan-scout judge a plan the same way: the reach
// and end of every hop, its length from the plan row and from where a fight leaves the lead (drift), the arrival radius, the
// gather-anchor climb (CheckAnchor: walk 31 f step 23), both ends swimmable (type 0x81), and the bodies a pull wakes that it
// does not own (joiners).
package planlint

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

// The rules' constants, in yards, pinned by the planscout test TestConstantsMirrorTheRunLeader.
const (
	ReactDistanceYd   = 150
	TooFarLegYd       = 120
	DriftMarginYd     = 10
	ArriveYd          = 3
	StalledArriveYd   = 6
	ExactArriveYd     = 1.5
	StragglerDzYd     = 8
	ClimbViaDzYd      = 6
	StandZTolYd       = 4.5
	SeeYd             = 60
	FightDriftYd      = 30
	DetourMaxYd       = 15
	StandMinYd        = 15
	StandHostileYd    = 20
	StandPullMarginYd = 5
	MaxMoveEndOffset  = navmesh.MaxMoveEndOffset
	JoinerWarnBodies  = 1
	JoinerFailBodies  = 3
)

// The scout's lint names.
const (
	LintReach   = "reach"
	LintHop     = "hop_len"
	LintArrival = "arrival"
	LintClimb   = "climb_anchor"
	LintJoiners = "joiners"
)

// Severity values of Finding.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// PathNav is what the path lints ask: the server's own query and the offline planner. *navmesh.Planner implements it; a Nav
// without it (the placement lints' fake) runs no reach, arrival, hop-path or joiner lint.
type PathNav interface {
	ServerPath(mapID uint32, from, to navmesh.Vec3) (navmesh.ServerPath, error)
	Plan(mapID uint32, from, to navmesh.Vec3) (navmesh.Path, error)
}

// JoinerPack is one pack the estimate counts outside the pull's target pack.
type JoinerPack struct {
	ID     uint64   `json:"id"`
	Name   string   `json:"name,omitempty"`
	Bodies int      `json:"bodies"`
	Guids  []uint32 `json:"guids"`
}

// hopOrigin is where a hop may start: the plan row before it, or a point the fight's drift puts the lead on.
type hopOrigin struct {
	Point dungeon.Point
	Drift bool
	Label string
	// Step is the row the origin is the point of (1-based).
	Step int
}

// origins lists the points row i is walked from (spec 3.2): the positioned row before it, the drift ring of a pull or boss
// row before it, and for a stand pull its stand and the anchor before the stand.
func (c *checker) origins(i int) []hopOrigin {
	steps := c.steps
	var out []hopOrigin
	add := func(p dungeon.Point, drift bool, label string, step int) {
		for _, o := range out {
			if dist3(o.Point, p) <= 1 {
				return
			}
		}
		out = append(out, hopOrigin{Point: p, Drift: drift, Label: label, Step: step})
	}
	prev := c.prev(i)
	if prev < 0 {
		return nil
	}
	p := pointOf(steps[prev])
	add(p, false, fmt.Sprintf("row %d", prev+1), prev+1)
	if steps[prev].Kind == planexport.KindPull || steps[prev].Kind == planexport.KindBoss {
		radius := float64(steps[prev].Radius)
		if radius <= 0 {
			if steps[prev].Kind == planexport.KindBoss {
				radius = planexport.DefaultBossRad
			} else {
				radius = planexport.DefaultPullYd
			}
		}
		radius = math.Min(radius, FightDriftYd)
		for k := range 8 {
			a := 2 * math.Pi * float64(k) / 8
			q := dungeon.Point{X: p.X + float32(radius)*float32(math.Cos(a)), Y: p.Y + float32(radius)*float32(math.Sin(a)), Z: p.Z}
			if n, ok, err := c.nav.Nearest(c.opt.Map, v3(q)); err == nil && ok && dist3(q, point3(n)) <= 5 {
				add(point3(n), true, fmt.Sprintf("drift from row %d", prev+1), prev+1)
			}
		}
	}
	if (steps[i].Kind == planexport.KindPull || steps[i].Kind == planexport.KindBoss) && steps[i].Flags&planexport.FlagStandPull != 0 &&
		moveKind(steps[prev].Kind) {
		if anchor := c.prev(prev); anchor >= 0 {
			add(pointOf(steps[anchor]), false, fmt.Sprintf("anchor row %d", anchor+1), anchor+1)
		}
	}
	return out
}

// HopOrigin is one point row i may be walked from, as origins lists it: the plan row before it, a point of the previous
// pull's drift ring, or (for a stand pull) its stand and the anchor before the stand.
type HopOrigin struct {
	Point dungeon.Point
	// Step is the row the origin is the point of (1-based).
	Step  int
	Drift bool
	Label string
}

// HopOrigins lists the points row i is walked from (spec 3.2). The scout's live path pass asks every one; the offline
// hop_len lint measures them.
func HopOrigins(rows planexport.Rows, nav Nav, i int) []HopOrigin {
	c := &checker{nav: nav, opt: Options{Map: rows.Plan.Map}, steps: rows.Steps}
	var out []HopOrigin
	for _, o := range c.origins(i) {
		out = append(out, HopOrigin{Point: o.Point, Step: o.Step, Drift: o.Drift, Label: o.Label})
	}
	return out
}

// GatherPoints marks the move row right before a pull, boss or hold row: the party necessarily stops there.
func GatherPoints(steps []planexport.StepRow) []bool { return gatherPoints(steps) }

// PrevPositioned is the index of the positioned row before i, or -1.
func PrevPositioned(steps []planexport.StepRow, i int) int {
	return (&checker{steps: steps}).prev(i)
}

// walkedTo says the party walks to row i's own point: a move, a hold, or a pull/boss it walks to (a stand pull is fought
// from its stand, whose climb is climb_anchor's).
func walkedTo(s planexport.StepRow) bool {
	switch s.Kind {
	case planexport.KindMove, planexport.KindMoveDirect, planexport.KindMoveHopExact, planexport.KindHold:
		return true
	case planexport.KindPull, planexport.KindBoss:
		return s.Flags&planexport.FlagStandPull == 0
	}
	return false
}

func (c *checker) severityFinding(i int, s planexport.StepRow, lint, detail, sev string) Finding {
	f := c.finding(i, s, lint, detail)
	f.Severity = sev
	return f
}

// checkHopLen measures row i's straight 3D distance from every origin: over the move cap minus the drift margin is an error,
// over the module's leg length (kTooFarLegYd) a warning.
func (c *checker) checkHopLen(i int, s planexport.StepRow) []Finding {
	if !walkedTo(s) {
		return nil
	}
	p := pointOf(s)
	var best *hopOrigin
	bestD := 0.0
	for _, o := range c.origins(i) {
		d := dist3(o.Point, p)
		if d > bestD {
			o := o
			best, bestD = &o, d
		}
	}
	if best == nil {
		return nil
	}
	fail := bestD > ReactDistanceYd-DriftMarginYd
	warn := bestD > TooFarLegYd
	if !fail && !warn {
		return nil
	}
	sev, what := SeverityWarning, fmt.Sprintf("over the %d yd leg", TooFarLegYd)
	if fail {
		sev, what = SeverityError, "over the move cap minus the drift margin"
	}
	detail := fmt.Sprintf("%.1f yd from %s (%d yd is %s)", bestD, best.Label, ReactDistanceYd-DriftMarginYd, what)
	if fail && best.Drift {
		detail += "; drift"
	}
	return []Finding{c.severityFinding(i, s, LintHop, detail, sev)}
}

// checkArrival: the server's own path to a move row must end within ArriveYd of it, and within StalledArriveYd of the walk
// mode that stops once the lead stops gaining.
func (c *checker) checkArrival(i int, s planexport.StepRow) []Finding {
	pn, ok := c.nav.(PathNav)
	if !ok || !moveKind(s.Kind) {
		return nil
	}
	prev := c.prev(i)
	if prev < 0 {
		return nil
	}
	sp, err := pn.ServerPath(c.opt.Map, v3(pointOf(c.steps[prev])), v3(pointOf(s)))
	if err != nil {
		return nil
	}
	end := dist3(point3(sp.ActualEnd), pointOf(s))
	switch {
	case end > StalledArriveYd:
		return []Finding{c.severityFinding(i, s, LintArrival, fmt.Sprintf("the walk never arrives: the path ends %.1f yd short (the stall rule is %d yd)", end, StalledArriveYd), SeverityError)}
	case end > ArriveYd:
		return []Finding{c.severityFinding(i, s, LintArrival, fmt.Sprintf("arrives only by the stall rule: the path ends %.1f yd short (kArriveYd %d)", end, ArriveYd), SeverityWarning)}
	}
	return nil
}

// checkReach: the server's query must not answer NOPATH, unreachable or off-mesh (planprobe's pull/boss tolerance kept).
func (c *checker) checkReach(i int, s planexport.StepRow) []Finding {
	pn, ok := c.nav.(PathNav)
	if !ok {
		return nil
	}
	if noHop(s) {
		return nil // the row's point is where the module does its work, not a hop: see noHop
	}
	prev := c.prev(i)
	if prev < 0 {
		return nil
	}
	from, to := v3(pointOf(c.steps[prev])), v3(pointOf(s))
	sp, err := pn.ServerPath(c.opt.Map, from, to)
	if err != nil {
		return nil
	}
	verdict := navmesh.ServerMoveVerdict(sp, from, to, 0)
	end := dist3(point3(sp.ActualEnd), pointOf(s))
	pullLike := s.Kind == planexport.KindPull || s.Kind == planexport.KindBoss
	var detail string
	switch {
	case sp.Type&navmesh.PathNoPath != 0:
		detail = "server NOPATH"
	case verdict == "unreachable":
		detail = "server unreachable"
	case verdict == "off-mesh" && !(pullLike && end <= float64(effRadius(s))+MaxMoveEndOffset):
		detail = fmt.Sprintf("server off-mesh (the path ends %.1f yd from the row)", end)
	default:
		return nil
	}
	return []Finding{c.severityFinding(i, s, LintReach, detail, SeverityError)}
}

// effRadius is the pull or boss radius of a row, with the module's defaults for 0 (15 yd pull, 30 yd boss).
func effRadius(s planexport.StepRow) float32 {
	if s.Radius > 0 {
		return s.Radius
	}
	if s.Kind == planexport.KindBoss {
		return planexport.DefaultBossRad
	}
	return planexport.DefaultPullYd
}

// checkClimbAnchor is CheckAnchor's rule for the gather point: the stand of a stand pull, and the point of a plain pull, may
// not stand more than StragglerDzYd above or below the anchor the lead arrives at. Elsewhere a climb only warns (ramps are
// normal).
func (c *checker) checkClimbAnchor(i int, s planexport.StepRow) []Finding {
	if s.Kind == planexport.KindPull || s.Kind == planexport.KindBoss {
		prev := c.prev(i)
		if prev < 0 {
			return nil
		}
		if s.Flags&planexport.FlagStandPull != 0 {
			if !moveKind(c.steps[prev].Kind) {
				return nil
			}
			stand := pointOf(c.steps[prev])
			anchor := c.prev(prev)
			if anchor < 0 {
				return nil
			}
			at := pointOf(c.steps[anchor])
			if dz := math.Abs(float64(stand.Z - at.Z)); dz > StragglerDzYd {
				f := c.severityFinding(prev, c.steps[prev], LintClimb, fmt.Sprintf("the stand is %.1f yd %s the anchor row %d: a stall on this slope leaves the lead more than %d yd from its gather point: CheckAnchor Off (walk 31 f step 23)", dz, aboveBelow(stand.Z, at.Z), anchor+1, StragglerDzYd), SeverityError)
				return []Finding{f}
			}
			return nil
		}
		if dz := math.Abs(float64(s.Z - c.steps[prev].Z)); dz > StragglerDzYd {
			return []Finding{c.severityFinding(i, s, LintClimb, fmt.Sprintf("%.1f yd %s the move before it: the walk to a plain pull climbs more than %d yd", dz, aboveBelow(s.Z, c.steps[prev].Z), StragglerDzYd), SeverityError)}
		}
		return nil
	}
	if !moveKind(s.Kind) {
		return nil
	}
	prev := c.prev(i)
	if prev < 0 || !moveKind(c.steps[prev].Kind) {
		return nil
	}
	// A stand row's own climb is the pull's climb_anchor finding (the anchor -> stand hop); do not warn twice.
	if c.gather[i] {
		return nil
	}
	if dz := math.Abs(float64(s.Z - c.steps[prev].Z)); dz > StragglerDzYd {
		return []Finding{c.severityFinding(i, s, LintClimb, fmt.Sprintf("%.1f yd climb from the move before it (a plain move: count only, only the gather anchor matters)", dz), SeverityWarning)}
	}
	return nil
}

// checkBothEndsWater: both ends on NavWater floors is the core's straight swim line (type 0x81), never the mesh path.
func (c *checker) checkBothEndsWater(i int, s planexport.StepRow) []Finding {
	if !walkedTo(s) && s.Kind != planexport.KindPull && s.Kind != planexport.KindBoss {
		return nil
	}
	prev := c.prev(i)
	if prev < 0 {
		return nil
	}
	_, okFrom := c.waterFloor(pointOf(c.steps[prev]))
	_, okTo := c.waterFloor(pointOf(s))
	if !okFrom || !okTo {
		return nil
	}
	f := c.severityFinding(i, s, LintWater, fmt.Sprintf("both ends on NavWater floors (%.1f %.1f %.1f and %.1f %.1f %.1f): the server swims a straight line, type 0x81", c.steps[prev].X, c.steps[prev].Y, c.steps[prev].Z, s.X, s.Y, s.Z), SeverityError)
	return []Finding{f}
}

// waterFloor says the floor at p (within 5 yd, 2 yd in height) is NavWater.
func (c *checker) waterFloor(p dungeon.Point) (navmesh.Floor, bool) {
	fl, err := c.nav.Floors(c.opt.Map, p.X, p.Y, p.Z-5, p.Z+5)
	if err != nil || len(fl) == 0 {
		return navmesh.Floor{}, false
	}
	f, ok := nearestFloor(fl, p.Z)
	if !ok || math.Abs(float64(f.Z-p.Z)) > 2 || f.Flags&navmesh.NavWater == 0 {
		return navmesh.Floor{}, false
	}
	return f, true
}

// checkRiverBelow: a walk whose path runs within 5 yd (2D) of a NavWater floor 6+ yd below it: a chase or a fall lands in
// water. It samples the offline path every 4 yd; the whole check needs PathNav.
func (c *checker) checkRiverBelow(i int, s planexport.StepRow) []Finding {
	pn, ok := c.nav.(PathNav)
	if !ok || !moveKind(s.Kind) {
		return nil
	}
	prev := c.prev(i)
	if prev < 0 {
		return nil
	}
	path, err := pn.Plan(c.opt.Map, v3(pointOf(c.steps[prev])), v3(pointOf(s)))
	if err != nil || len(path.Points) < 2 {
		return nil
	}
	for k := 1; k < len(path.Points); k++ {
		a, b := path.Points[k-1], path.Points[k]
		d := dist3(point3(a), point3(b))
		n := int(math.Ceil(float64(d) / 4))
		if n < 1 {
			n = 1
		}
		for j := 0; j <= n; j++ {
			t := float32(j) / float32(n)
			p := dungeon.Point{X: a[0] + (b[0]-a[0])*t, Y: a[1] + (b[1]-a[1])*t, Z: a[2] + (b[2]-a[2])*t}
			// Within 5 yd 2D of the path: the sample itself and four points 4 yd off it.
			for _, off := range [][2]float32{{0, 0}, {4, 0}, {-4, 0}, {0, 4}, {0, -4}} {
				fl, err := c.nav.Floors(c.opt.Map, p.X+off[0], p.Y+off[1], p.Z-40, p.Z-6)
				if err != nil {
					continue
				}
				for _, f := range fl {
					if f.Flags&navmesh.NavWater != 0 {
						return []Finding{c.severityFinding(i, s, LintWater, fmt.Sprintf("river below: a NavWater floor %.1f yd under the path at %.1f %.1f (a chase or a fall lands in water)", float64(p.Z-f.Z), p.X+off[0], p.Y+off[1]), SeverityWarning)}
					}
				}
			}
		}
	}
	return nil
}

// pullRows converts the export rows to the estimator's rows (the scout's and plan-check's joiner geometry).
func (c *checker) pullRows() []PullRow { return PullRowsOf(c.steps) }

// PullRowsOf converts export rows to the estimator's rows: the party point of a stand pull is its stand (the move row right
// before it), the walk into a row is the straight segment from the previous party point.
func PullRowsOf(steps []planexport.StepRow) []PullRow {
	out := make([]PullRow, len(steps))
	var prevParty *dungeon.Point
	c := &checker{steps: steps}
	for i, s := range steps {
		if !positioned(s) {
			continue
		}
		p := pointOf(s)
		party := p
		if (s.Kind == planexport.KindPull || s.Kind == planexport.KindBoss) && s.Flags&planexport.FlagStandPull != 0 {
			if prev := c.prev(i); prev >= 0 && moveKind(c.steps[prev].Kind) {
				party = pointOf(c.steps[prev])
			}
		}
		row := PullRow{Leg: s.Leg, Seq: s.Seq, Kind: s.Kind, Point: p, Party: party, Entries: s.Entries, Radius: s.Radius, Item: s.Item,
			Team: rowTeam(s.Flags), Owns: (s.Kind == planexport.KindPull || s.Kind == planexport.KindBoss || s.Kind == planexport.KindHold) && len(s.Entries) > 0}
		if prevParty != nil {
			row.Walk = []dungeon.Point{*prevParty, party}
		}
		prevParty = &row.Party
		out[i] = row
	}
	return out
}

func rowTeam(flags uint32) string {
	switch {
	case flags&planexport.FlagAlliance != 0:
		return dungeon.TeamAlliance
	case flags&planexport.FlagHorde != 0:
		return dungeon.TeamHorde
	}
	return ""
}

// checkJoiners estimates every pull row's bodies and names the ones outside its target pack. A body count over the row's
// ceiling, or three or more bodies outside the pull, is an error; one or two warn.
func (c *checker) checkJoiners(i int, s planexport.StepRow) []Finding {
	if c.opt.World == nil || s.Kind != planexport.KindPull {
		return nil
	}
	rows := c.pullRows()
	row := indexOfSeq(rows, s.Leg, s.Seq)
	if row < 0 {
		return nil
	}
	if !c.joinsDone {
		c.joins = PullSizes(rows, c.opt.World.Spawns, c.opt.World, func(sp worldspawn.Spawn) bool { return sp.Hostile() && sp.LevelMax > 1 }, PullOptions{
			PartyLevel: c.opt.Level, Raid: c.opt.Raid, Ceiling: c.opt.PullCeiling, KillFirst: c.opt.KillFirst, Fight: true, Joiners: true, All: true,
		})
		c.joinsDone = true
	}
	for _, f := range c.joins {
		if f.Row != row {
			continue
		}
		over := s.Item != 0 && f.Bodies > int(s.Item)
		if f.Joiners < JoinerWarnBodies && !over {
			return nil
		}
		sev := SeverityWarning
		if f.Joiners >= JoinerFailBodies || over {
			sev = SeverityError
		}
		detail := fmt.Sprintf("%d bodies outside the target pack (estimate, sight assumed): %s", f.Joiners, guidsText(f.JoinerGuids))
		if s.Item != 0 && f.Bodies > int(s.Item) {
			detail += fmt.Sprintf("; %d bodies over the row's %d", f.Bodies, s.Item)
		}
		out := c.severityFinding(i, s, LintJoiners, detail, sev)
		out.Bodies, out.Ceiling, out.Guids, out.JoinerPacks = f.Bodies, f.Ceiling, f.JoinerGuids, f.JoinerPacks
		return []Finding{out}
	}
	return nil
}

func indexOfSeq(rows []PullRow, leg, seq int) int {
	for i := range rows {
		if rows[i].Leg == leg && rows[i].Seq == seq {
			return i
		}
	}
	return -1
}

func guidsText(gs []uint32) string {
	if len(gs) == 0 {
		return "none"
	}
	sort.Slice(gs, func(i, j int) bool { return gs[i] < gs[j] })
	parts := make([]string, len(gs))
	for i, g := range gs {
		parts[i] = fmt.Sprint(g)
	}
	return strings.Join(parts, ", ")
}
