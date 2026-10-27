package dungeon

import (
	"fmt"
	"math"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

// Staging a leg at its safe start for the TortoiseBots run leader
// (`dungeon_stage`, `from leg <name>`): the run leader needs the party at the
// leg's first row before it starts partway, and a fresh instance has every
// skipped pack alive. A stage point must stand clear of the packs the run
// has not cleared yet, and the walk from it to the leg's first point must
// reach and stay clear of them too.

const (
	// StageSkippedYd is the stage point's distance to a pack/boss/patrol
	// point of a SKIPPED leg (alive in a fresh instance), 3D. It is the
	// start driver's gatherSafe: a pull from 30 yd out never reaches the
	// party (the commander's social-aggro radius).
	StageSkippedYd = 30.0
	// StageOwnYd is the stage point's distance to a pack/boss/patrol point
	// of the staged leg or later (the vanilla base aggro radius), 3D. The
	// run pulls those later, so the party may stand nearer than to a pack
	// that is alive on arrival; under 20 yd the pack comes at the party.
	StageOwnYd = 20.0
	// StageWalkYd is every sample's distance, on the walk from the stage
	// point to the first point, to a SKIPPED danger point, 3D.
	StageWalkYd = 20.0
	// StageBackStepYd is the spacing of the back-off candidates along the
	// way in.
	StageBackStepYd = 5.0
	// StageBackMaxYd is how far back along the way in the search goes.
	StageBackMaxYd = 120.0
	// StageMaxWalkYd is the longest server walk from a candidate to the
	// first point (AiPlayerbot.ReactDistance).
	StageMaxWalkYd = 150.0
	// StageEarlierLegs is how many legs back the earlier-leg proposal looks.
	StageEarlierLegs = 3
	// StageSampleYd is the walk sample spacing (navmesh.GroundStep).
	StageSampleYd = 4.0
)

// stageDanger is one point of a pack a stage point must stand clear of: a
// pull-group position, a patrol path point, or a boss position.
type stageDanger struct {
	At     Point
	Leg    int
	Group  int // pull group index (0-based), -1 for the boss
	Entry  uint32
	Patrol bool // a point of the group's patrol path
}

// String names the danger the way the refusals do.
func (d stageDanger) String() string {
	return d.describe("")
}

func (d stageDanger) describe(legName string) string {
	if d.Group < 0 {
		return fmt.Sprintf("the boss of leg %d %s (entry %d)", d.Leg, legName, d.Entry)
	}
	if d.Patrol {
		return fmt.Sprintf("the patrol of pull group %d of leg %d %s (entry %d)", d.Group+1, d.Leg, legName, d.Entry)
	}
	return fmt.Sprintf("pull group %d of leg %d %s (entry %d)", d.Group+1, d.Leg, legName, d.Entry)
}

// stageDangers collects every pull-group position, patrol path point and
// boss position of the plan. Legs at or below clearedThrough are left out
// entirely; legs before i are skipped, legs at or after i are own. Team and
// Ambush are ignored (conservative). Optional legs are included (their mobs
// stand in the world).
func stageDangers(p *Plan, i, clearedThrough int) (skipped, own []stageDanger) {
	for li := range p.Legs {
		if li <= clearedThrough {
			continue
		}
		l := p.Legs[li]
		for gi, g := range l.PullGroups {
			for _, at := range g.Positions {
				d := stageDanger{At: at, Leg: li, Group: gi, Entry: firstStageEntry(g.Entries)}
				if li < i {
					skipped = append(skipped, d)
				} else {
					own = append(own, d)
				}
			}
			if g.Patrol != nil {
				for _, at := range g.Patrol.Path {
					d := stageDanger{At: at, Leg: li, Group: gi, Entry: firstStageEntry(g.Patrol.Entries), Patrol: true}
					if li < i {
						skipped = append(skipped, d)
					} else {
						own = append(own, d)
					}
				}
			}
		}
		if l.Boss != nil {
			d := stageDanger{At: l.Boss.Position, Leg: li, Group: -1, Entry: l.Boss.Entry}
			if li < i {
				skipped = append(skipped, d)
			} else {
				own = append(own, d)
			}
		}
	}
	return skipped, own
}

func firstStageEntry(es []uint32) uint32 {
	if len(es) > 0 {
		return es[0]
	}
	return 0
}

// StageOptions are the offline options of StageLeg.
type StageOptions struct {
	ClearedThrough int    // legs 0..this one count as cleared (their packs dead); -1: none implied by callers
	FirstRow       *Point // the leg's first row; nil: LegFirstPoint
	UseSafePoint   bool
	Force          bool
}

// StageProposal is a leg to stage at instead: the safe point before the leg
// (before_leg), or an earlier leg that stages safely (earlier_leg).
type StageProposal struct {
	Kind    string  `json:"kind"` // "before_leg" or "earlier_leg"
	Leg     int     `json:"leg"`
	LegName string  `json:"leg_name"`
	Point   Point   `json:"point"`
	BackYd  float64 `json:"back_yd"`
}

// StageResult is the offline answer of StageLeg.
type StageResult struct {
	Leg      int            `json:"leg"`
	LegName  string         `json:"leg_name"`
	Map      uint32         `json:"map"`
	First    Point          `json:"first"`
	Anchor   Point          `json:"anchor"`
	OK       bool           `json:"ok"`
	Kind     string         `json:"kind,omitempty"` // leg_first, before_leg, forced
	Point    Point          `json:"point"`
	BackYd   float64        `json:"back_yd"`
	Refusal  string         `json:"refusal,omitempty"`
	Proposal *StageProposal `json:"proposal,omitempty"`
	Warnings []string       `json:"warnings"`
}

// LegFirstPoint is the plan's first point of a leg: its first via point,
// else its To.
func (p *Plan) LegFirstPoint(i int) Point {
	l := p.Legs[i]
	if len(l.Via) > 0 {
		return l.Via[0].Point
	}
	return l.To
}

// StageLeg picks the safe stage point of leg i: its first point when that
// is safe, else the first safe point back on the way in (with UseSafePoint),
// or a refusal with an earlier-leg proposal.
func StageLeg(p *Plan, leg int, nav Navmesh, opt StageOptions) (StageResult, error) {
	if leg < 0 || leg >= len(p.Legs) {
		return StageResult{}, fmt.Errorf("stage: leg %d: the plan has legs 0-%d", leg, len(p.Legs)-1)
	}
	if opt.ClearedThrough >= leg {
		return StageResult{}, fmt.Errorf("stage: cleared_through %d must be before the staged leg %d", opt.ClearedThrough, leg)
	}
	if _, ok := nav.(ServerQuerier); !ok {
		return StageResult{}, fmt.Errorf("stage: the navmesh cannot replay the server's path query (a navmesh.Planner over mmaps_dir is needed)")
	}
	r := StageResult{Leg: leg, LegName: p.Legs[leg].Name, Map: p.Map}
	r.First = p.LegFirstPoint(leg)
	if opt.FirstRow != nil {
		r.First = *opt.FirstRow
	}
	a, _ := p.LegStart(leg)
	r.Anchor = a
	st := stageState{p: p, leg: leg, nav: nav, first: r.First, anchor: a, cleared: opt.ClearedThrough}
	st.skipped, st.own = stageDangers(p, leg, opt.ClearedThrough)
	st.next = st.nextPoint(opt.FirstRow != nil)
	out := r
	// R2 first, then R1: both are refusals no stage point can fix.
	if st.next.have {
		if fault := st.serverFault(st.first, st.next.at); fault != "" {
			rr := fmt.Sprintf("leg %d %s: the server's path query from its first point %.1f %.1f %.1f does not reach its next point %.1f %.1f %.1f (%s): the first point is on another floor or cut off; run plan-check --from-leg %d",
				leg, p.Legs[leg].Name, st.first.X, st.first.Y, st.first.Z, st.next.at.X, st.next.at.Y, st.next.at.Z, fault, leg)
			return st.refuse(out, rr, nil, opt, true, true), nil
		}
	}
	if d, danger := st.nearestSkipped(st.first); d < StageWalkYd {
		rr := fmt.Sprintf("leg %d %s cannot be staged in a fresh instance: its first point %.1f %.1f %.1f is %.0f yd from %s. No point before it helps: the run walks there first.",
			leg, p.Legs[leg].Name, st.first.X, st.first.Y, st.first.Z, d, danger.describe(p.Legs[danger.Leg].Name))
		return st.refuse(out, rr, nil, opt, true, false), nil
	}

	// evaluate(F).
	if fault, snapped := st.evaluate(st.first, firstDesc(st.first)); fault == "" {
		out.OK, out.Kind, out.Point, out.BackYd = true, "leg_first", snapped, 0
		return out, nil
	} else {
		prop := st.backOff()
		if prop != nil {
			if opt.UseSafePoint {
				out.OK, out.Kind, out.Point, out.BackYd = true, "before_leg", prop.Point, prop.BackYd
				return out, nil
			}
			rr := fmt.Sprintf("leg %d %s: %s. Nearest safe point before it: %.1f %.1f %.1f (%.0f yd back on the way in): pass use_safe_point to stage there.",
				leg, p.Legs[leg].Name, fault, prop.Point.X, prop.Point.Y, prop.Point.Z, prop.BackYd)
			return st.forceOrRefuse(out, rr, prop, opt, fault), nil
		}
		rr := fmt.Sprintf("leg %d %s: %s; no point up to %.0f yd back toward %s is safe either.",
			leg, p.Legs[leg].Name, fault, float64(StageBackMaxYd), anchorText(p, leg))
		return st.refuse(out, rr, nil, opt, true, false), nil
	}
}

// stageState is the per-leg check state of one StageLeg call.
type stageState struct {
	p       *Plan
	leg     int
	nav     Navmesh
	first   Point
	anchor  Point
	cleared int
	skipped []stageDanger
	own     []stageDanger
	next    stageNextPoint
}

type stageNextPoint struct {
	at   Point
	have bool
}

// nextPoint is N: the next run-leader point of the leg after F. When F came
// from a path row, N is the leg's first plan point. Else the leg's
// run-leader points in runLeaderWalk order are walked and the first one
// more than ArriveYd from F is taken. None: no reach check below.
func (s *stageState) nextPoint(fromRow bool) stageNextPoint {
	if fromRow {
		// The first row came from a path row: N is the leg's first
		// plan point, but only when it stands past F. When the first
		// row IS Via[0] LegFirstPoint is F itself, and using it as N
		// would pass R2 vacuously: fall through to the walk search.
		if fp := s.p.LegFirstPoint(s.leg); dist3(fp, s.first) > ArriveYd {
			return stageNextPoint{at: fp, have: true}
		}
	}
	var out []Point
	s.p.runLeaderWalk(s.leg, func(m RunLeaderPoint) {
		if m.Leg == s.leg {
			out = append(out, m.Point)
		}
	}, func(leg int, part string, at Point, group int, team string) {
		out = append(out, at)
	})
	for _, at := range out {
		if dist3(s.first, at) > ArriveYd {
			return stageNextPoint{at: at, have: true}
		}
	}
	return stageNextPoint{}
}

// evaluate checks one candidate C (description desc): the first fault wins.
// It returns the fault ("" when C passes) and the snapped point.
func (s *stageState) evaluate(c Point, desc string) (string, Point) {
	// 1. On mesh.
	n, ok, err := s.nav.Nearest(s.p.Map, toVec(c))
	if err != nil {
		return fmt.Sprintf("stage: %v", err), c
	}
	snapped := Point{n[0], n[1], n[2]}
	if !ok || math.Abs(float64(n[2]-c.Z)) > 3.0 || math.Hypot(float64(n[0]-c.X), float64(n[1]-c.Y)) > 2.5 {
		return fmt.Sprintf("%s is off the navmesh (no walkable floor within 2.5 yd and 3 yd up or down)", desc), c
	}
	// 2. Skipped packs.
	if d, danger := s.nearestSkipped(snapped); d < StageSkippedYd {
		return fmt.Sprintf("%s is %.0f yd from %s, alive in a fresh instance (a stage point needs %.0f)", desc, d, danger.describe(s.p.Legs[danger.Leg].Name), float64(StageSkippedYd)), snapped
	}
	// 3. Own packs.
	if d, danger := s.nearestOwn(snapped); d < StageOwnYd {
		return fmt.Sprintf("%s is %.0f yd from %s, which the run pulls later (a stage point needs %.0f, or the pack comes at the party on arrival)", desc, d, danger.describe(s.p.Legs[danger.Leg].Name), float64(StageOwnYd)), snapped
	}
	// 4. Reach F (server-exact): up to 3 legs of ServerPath +
	// ServerMoveVerdict; inside ArriveYd counts as arrived; collect every
	// sp.Points for the walk check.
	walk, fault := s.serverWalk(snapped, s.first, desc)
	if fault != "" {
		return fault, snapped
	}
	if walk.length > StageMaxWalkYd {
		return fmt.Sprintf("the walk from %s to the leg's first point is %.0f yd (at most %.0f)", desc, walk.length, float64(StageMaxWalkYd)), snapped
	}
	// 5. Walk clear: densify the collected server points every
	// StageSampleYd, endpoints included; every sample's nearest SKIPPED
	// danger >= StageWalkYd.
	for _, smp := range densify(walk.points, StageSampleYd) {
		if d, danger := s.nearestSkipped(smp); d < StageWalkYd {
			return fmt.Sprintf("the walk from %s to the leg's first point passes %.0f yd from %s at %.1f %.1f %.1f, alive in a fresh instance (a walk needs %.0f)",
				desc, d, danger.describe(s.p.Legs[danger.Leg].Name), smp.X, smp.Y, smp.Z, float64(StageWalkYd)), snapped
		}
	}
	return "", snapped
}

// serverWalk simulates the run leader's walk like planChecker.serverWalk:
// up to 3 legs of ServerPath + verdict; inside ArriveYd counts as arrived.
func (s *stageState) serverWalk(from, to Point, desc string) (stageWalk, string) {
	q := s.nav.(ServerQuerier)
	var out stageWalk
	cur := from
	for range 3 {
		if dist3(cur, to) <= ArriveYd {
			return out, ""
		}
		sp, err := q.ServerPath(s.p.Map, toVec(cur), toVec(to))
		if err != nil {
			return out, fmt.Sprintf("stage: %v", err)
		}
		if v := navmesh.ServerMoveVerdict(sp, toVec(cur), toVec(to), 0); v != "ok" {
			return out, fmt.Sprintf("the server's path query from %s does not reach the leg's first point %.1f %.1f %.1f (%s: another floor or no way)", desc, to.X, to.Y, to.Z, v)
		}
		out.add(sp)
		if sp.Type&navmesh.PathNormal != 0 {
			return out, ""
		}
		cur = Point{sp.ActualEnd[0], sp.ActualEnd[1], sp.ActualEnd[2]}
	}
	return out, fmt.Sprintf("the server's path query from %s does not reach the leg's first point %.1f %.1f %.1f (%s: another floor or no way)", desc, to.X, to.Y, to.Z, "3 incomplete legs")
}

// serverFault is the R2 wording: the F->N walk failing.
func (s *stageState) serverFault(from, to Point) string {
	q := s.nav.(ServerQuerier)
	cur := from
	for range 3 {
		if dist3(cur, to) <= ArriveYd {
			return ""
		}
		sp, err := q.ServerPath(s.p.Map, toVec(cur), toVec(to))
		if err != nil {
			return err.Error()
		}
		if v := navmesh.ServerMoveVerdict(sp, toVec(cur), toVec(to), 0); v != "ok" {
			return v
		}
		if sp.Type&navmesh.PathNormal != 0 {
			return ""
		}
		cur = Point{sp.ActualEnd[0], sp.ActualEnd[1], sp.ActualEnd[2]}
	}
	return "3 incomplete legs"
}

// stageWalk is one server walk: the collected points and their length.
type stageWalk struct {
	points []Point
	length float64
}

func (w *stageWalk) add(sp navmesh.ServerPath) {
	for i, v := range sp.Points {
		pt := Point{v[0], v[1], v[2]}
		if i == 0 && len(w.points) > 0 {
			continue
		}
		if len(w.points) > 0 {
			w.length += dist3(w.points[len(w.points)-1], pt)
		}
		w.points = append(w.points, pt)
	}
}

// densify samples the polyline every yd (endpoints included).
func densify(pts []Point, yd float64) []Point {
	if len(pts) == 0 {
		return nil
	}
	if len(pts) == 1 {
		return []Point{pts[0]}
	}
	var out []Point
	for i := 1; i < len(pts); i++ {
		a, b := pts[i-1], pts[i]
		d := dist3(a, b)
		n := int(d / yd)
		for k := 0; k <= n; k++ {
			t := 0.0
			if d > 0 {
				t = float64(k) * yd / d
			}
			if i > 1 && k == 0 {
				continue
			}
			out = append(out, Point{
				X: float32(float64(a.X) + t*float64(b.X-a.X)),
				Y: float32(float64(a.Y) + t*float64(b.Y-a.Y)),
				Z: float32(float64(a.Z) + t*float64(b.Z-a.Z)),
			})
		}
	}
	out = append(out, pts[len(pts)-1])
	return out
}

// backOff searches the way in (anchor->first) backwards for the first
// candidate that passes evaluate: every StageBackStepYd of path length (5,
// 10, 15, ...) up to min(StageBackMaxYd, path length), interpolated on the
// polyline (z too). nav.Plan non-complete or under 2 points: [anchor, first].
func (s *stageState) backOff() *StageProposal {
	pts := []Point{s.anchor, s.first}
	if path, err := s.nav.Plan(s.p.Map, toVec(s.anchor), toVec(s.first)); err == nil && path.Status == navmesh.Complete && len(path.Points) >= 2 {
		pts = nil
		for _, v := range path.Points {
			pts = append(pts, Point{v[0], v[1], v[2]})
		}
	}
	seg := make([]float64, len(pts))
	for i := 1; i < len(pts); i++ {
		seg[i] = seg[i-1] + dist3(pts[i-1], pts[i])
	}
	total := seg[len(seg)-1]
	if total <= 0 {
		return nil
	}
	maxBack := float64(StageBackMaxYd)
	if total < maxBack {
		maxBack = total
	}
	for back := float64(StageBackStepYd); back <= maxBack+1e-9; back += float64(StageBackStepYd) {
		at := backPoint(pts, seg, total, back)
		desc := fmt.Sprintf("the point %.1f %.1f %.1f (%.0f yd back on the way in)", at.X, at.Y, at.Z, back)
		if fault, snapped := s.evaluate(at, desc); fault == "" {
			return &StageProposal{Kind: "before_leg", Leg: s.leg, LegName: s.p.Legs[s.leg].Name, Point: snapped, BackYd: back}
		}
	}
	return nil
}

// backPoint interpolates the point `back` yards before the end of the polyline.
func backPoint(pts []Point, seg []float64, total, back float64) Point {
	want := total - back
	for i := 1; i < len(pts); i++ {
		if seg[i] >= want {
			a, b := pts[i-1], pts[i]
			d := seg[i] - seg[i-1]
			if d == 0 {
				return b
			}
			t := (want - seg[i-1]) / d
			return Point{X: float32(float64(a.X) + t*float64(b.X-a.X)), Y: float32(float64(a.Y) + t*float64(b.Y-a.Y)), Z: float32(float64(a.Z) + t*float64(b.Z-a.Z))}
		}
	}
	return pts[0]
}

// refuse records the refusal, applies force for non-R2 refusals, and
// (top-level only) appends the earlier-leg proposal. R2 gets no proposal
// and is not forceable. Force snapshots the refusal AFTER the earlier-leg
// note is appended, so Warnings[0] is the full refusal text.
func (s *stageState) refuse(out StageResult, refusal string, prop *StageProposal, opt StageOptions, top, isR2 bool) StageResult {
	out.Refusal, out.Proposal = refusal, prop
	if top && opt.Force && isR2 {
		out.Warnings = append(out.Warnings, "force does not pass a leg whose first point is cut off")
	}
	if top && !isR2 {
		s.earlier(&out, opt)
	}
	if top && opt.Force && !isR2 {
		if ok, fr := s.force(out, out.Refusal, prop); ok {
			return fr
		} else if fr.Refusal != "" {
			out = fr
		}
	}
	return out
}

// forceOrRefuse applies force to a non-R2 refusal (keeping the before_leg
// proposal) or returns the refusal. The earlier-leg search does not run
// here: a before_leg point was found (spec step 4). Force snapshots the
// refusal as-is, so Warnings[0] is the full refusal text.
func (s *stageState) forceOrRefuse(out StageResult, refusal string, prop *StageProposal, opt StageOptions, fault string) StageResult {
	out.Refusal, out.Proposal = refusal, prop
	if opt.Force {
		if ok, fr := s.force(out, out.Refusal, prop); ok {
			return fr
		} else if fr.Refusal != "" {
			out = fr
		}
	}
	return out
}

// force stages at F anyway (keeping the proposal). Off-mesh F stays refused.
func (s *stageState) force(out StageResult, refusal string, prop *StageProposal) (bool, StageResult) {
	if fault, _ := s.evaluate(s.first, firstDesc(s.first)); fault != "" && isOffMeshFault(s.nav, s.p.Map, s.first) {
		out.Refusal = "force cannot stage off the navmesh"
		out.Proposal = prop
		return false, out
	}
	_, snapped := s.evaluate(s.first, firstDesc(s.first))
	out.OK, out.Kind, out.Point, out.BackYd = true, "forced", snapped, 0
	out.Warnings = append([]string{refusal}, out.Warnings...)
	out.Refusal = ""
	out.Proposal = prop
	return true, out
}

// earlier appends the earlier-leg proposal: for j = i-1 down to
// max(0, i-StageEarlierLegs) and j > ClearedThrough, the first j whose own
// check with UseSafePoint is OK (no recursion, its own first point from
// LegFirstPoint, its own anchor and danger sets). It never overwrites an
// existing before_leg proposal: that point is the one to stage at, and the
// text names the earlier leg as well. None: the clear note.
func (s *stageState) earlier(out *StageResult, opt StageOptions) {
	for j := s.leg - 1; j >= s.leg-StageEarlierLegs && j >= 0; j-- {
		if j <= opt.ClearedThrough {
			continue
		}
		jfirst := s.p.LegFirstPoint(j)
		ja, _ := s.p.LegStart(j)
		sub := stageState{p: s.p, leg: j, nav: s.nav, first: jfirst, anchor: ja, cleared: opt.ClearedThrough}
		sub.skipped, sub.own = stageDangers(s.p, j, opt.ClearedThrough)
		sub.next = sub.nextPoint(false)
		jr := StageResult{Leg: j, LegName: s.p.Legs[j].Name, Map: s.p.Map, First: jfirst, Anchor: ja}
		if st := sub.check(jr); st.OK {
			if out.Proposal == nil {
				out.Proposal = &StageProposal{Kind: "earlier_leg", Leg: j, LegName: s.p.Legs[j].Name, Point: st.Point, BackYd: st.BackYd}
			}
			out.Refusal += fmt.Sprintf(" Nearest earlier leg that stages safely: leg %d %s at %.1f %.1f %.1f: stage that leg instead.",
				j, s.p.Legs[j].Name, st.Point.X, st.Point.Y, st.Point.Z)
			return
		}
	}
	out.Refusal += fmt.Sprintf(" No leg up to %d back stages safely: clear the legs before it first (a run, or cleared_through), or pass force.", StageEarlierLegs)
}

// check runs steps R2, R1, evaluate(F), back-off for a sub-leg (no
// earlier-leg recursion, no force).
func (s *stageState) check(out StageResult) StageResult {
	if s.next.have {
		if fault := s.serverFault(s.first, s.next.at); fault != "" {
			out.Refusal = fmt.Sprintf("leg %d %s: the server's path query from its first point %.1f %.1f %.1f does not reach its next point %.1f %.1f %.1f (%s): the first point is on another floor or cut off; run plan-check --from-leg %d",
				s.leg, s.p.Legs[s.leg].Name, s.first.X, s.first.Y, s.first.Z, s.next.at.X, s.next.at.Y, s.next.at.Z, fault, s.leg)
			return out
		}
	}
	if d, danger := s.nearestSkipped(s.first); d < StageWalkYd {
		out.Refusal = fmt.Sprintf("leg %d %s cannot be staged in a fresh instance: its first point %.1f %.1f %.1f is %.0f yd from %s. No point before it helps: the run walks there first.",
			s.leg, s.p.Legs[s.leg].Name, s.first.X, s.first.Y, s.first.Z, d, danger.describe(s.p.Legs[danger.Leg].Name))
		return out
	}
	if fault, snapped := s.evaluate(s.first, firstDesc(s.first)); fault == "" {
		out.OK, out.Kind, out.Point, out.BackYd = true, "leg_first", snapped, 0
		return out
	} else {
		if prop := s.backOff(); prop != nil {
			out.OK, out.Kind, out.Point, out.BackYd = true, "before_leg", prop.Point, prop.BackYd
			return out
		}
		out.Refusal = fmt.Sprintf("leg %d %s: %s; no point up to %.0f yd back toward %s is safe either.",
			s.leg, s.p.Legs[s.leg].Name, fault, float64(StageBackMaxYd), anchorText(s.p, s.leg))
		return out
	}
}

// nearestSkipped returns the nearest skipped danger to at and its distance
// (huge when none).
func (s *stageState) nearestSkipped(at Point) (float64, stageDanger) {
	return nearestStageDanger(s.p, s.skipped, at)
}

// nearestOwn returns the nearest own danger to at and its distance (huge
// when none).
func (s *stageState) nearestOwn(at Point) (float64, stageDanger) {
	return nearestStageDanger(s.p, s.own, at)
}

func nearestStageDanger(p *Plan, ds []stageDanger, at Point) (float64, stageDanger) {
	best := math.MaxFloat64
	var bd stageDanger
	for _, d := range ds {
		if dd := dist3(d.At, at); dd < best {
			best, bd = dd, d
		}
	}
	_ = p
	return best, bd
}

func firstDesc(f Point) string {
	return fmt.Sprintf("the leg's first point %.1f %.1f %.1f", f.X, f.Y, f.Z)
}

func anchorText(p *Plan, leg int) string {
	if leg == 0 {
		return "the entrance"
	}
	return fmt.Sprintf("the end of leg %d %s", leg-1, p.Legs[leg-1].Name)
}

func isOffMeshFault(nav Navmesh, mapID uint32, at Point) bool {
	n, ok, err := nav.Nearest(mapID, toVec(at))
	if err != nil || !ok {
		return true
	}
	return math.Abs(float64(n[2]-at.Z)) > 3.0 || math.Hypot(float64(n[0]-at.X), float64(n[1]-at.Y)) > 2.5
}

func containsRefusal(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func toVec(p Point) navmesh.Vec3 { return navmesh.Vec3{p.X, p.Y, p.Z} }
