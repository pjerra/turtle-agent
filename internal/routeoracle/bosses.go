package routeoracle

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
)

// The bosses mode asks a different question from the walk mode. The walk mode proves the
// plan's stored hops are walkable. The bosses mode asks whether a route could be planned LIVE
// from a boss list alone: each leg is one navmesh plan from where the party stands to the leg's
// objective (its boss, else its to/last point), avoid-aware, with NONE of the plan's hand
// points and NONE of its break hops. Whatever the mesh cannot reach that way is where hand data
// is still needed, and the report names the stored break that sits there.

const (
	// HandNearYd is how close a stored hand point (via, approach, stand) must lie to the live
	// path to be called redundant: the live path already passes it.
	HandNearYd = 10.0
	// BreakHitYd is how close a stored break must lie to where the live path ends, or to the
	// line from there to the objective, to be named as the break the live plan ran into.
	BreakHitYd = 15.0
)

// BossLeg is one leg's live planning, entrance (or the previous leg's end) to its objective.
type BossLeg struct {
	Leg  int           `json:"leg"`
	Name string        `json:"name,omitempty"`
	From dungeon.Point `json:"from"` // where the live plan starts
	To   dungeon.Point `json:"to"`   // the leg's objective
	// Objective says what the objective is: boss, to, step, or none.
	Objective string `json:"objective"`
	// Status: complete (the mesh reaches the objective), partial or no_path (it does not: the
	// leg is FAILED), or no_objective (nothing stored to plan to).
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	// NonComparable: the leg started from the point a failed leg's path reached, not from the
	// previous objective. Its result is a continuation, labelled and excluded from every
	// aggregate: it says nothing about the plan's own journey.
	NonComparable    bool   `json:"non_comparable,omitempty"`
	NonComparableWhy string `json:"non_comparable_why,omitempty"`
	// Live is the avoid-aware run. End is where the party stands: the objective on a complete
	// path, the nearest reachable point on a partial one.
	Live   Run     `json:"live"`
	PlanMs float64 `json:"plan_ms"`

	// BreakHits (failed legs): the stored breaks the live plan ran into: those that bridge the
	// gap (the mesh reaches the hop's foot from the start and the objective from its landing)
	// and those lying within BreakHitYd of the live end or of the line from it to the
	// objective.
	BreakHits []BreakHit `json:"break_hits,omitempty"`
	// Bridged (a non-comparable leg only): the same leg planned from the previous leg's
	// OBJECTIVE, as if the hand data had crossed the break before it. It is the answer to "what
	// else does the mesh not reach", and enters no primary aggregate (see Totals.Bridged).
	Bridged *BossLeg `json:"bridged,omitempty"`
	// Breaks: all the leg's stored breaks, and whether the live path passes one (within
	// BreakNearYd), which a complete leg shows the mesh needs no hop for.
	Breaks []BreakTouched `json:"breaks,omitempty"`

	// The corridor metrics, complete legs only.
	Packs         []PullOnPath `json:"packs,omitempty"`
	PacksOnPath   int          `json:"packs_on_path"`
	Hand          []HandPoint  `json:"hand,omitempty"`
	HandRedundant int          `json:"hand_redundant"`
	ZJumps        []ZJump      `json:"z_jumps,omitempty"`
}

// BreakHit is a stored break a failed live plan ran into.
type BreakHit struct {
	Via  int           `json:"via"`  // the waypoint's index in the leg's via, from 0
	Kind string        `json:"kind"` // exact, direct, check_skip, gm_cross (comma joined)
	At   dungeon.Point `json:"at"`
	// EndYd and LineYd: the break's distance to where the live path ends, and to the line from
	// there to the objective.
	EndYd  float64 `json:"end_yd"`
	LineYd float64 `json:"line_yd"`
	// Bridges: the mesh reaches the hop's foot (the previous via, or the leg's start) and the
	// objective from the hop's landing, so this hop is what the live plan lacks.
	Bridges bool `json:"bridges"`
	// Why says which of the rules named it.
	Why string `json:"why"`
}

// HandPoint is a stored hand point of a complete leg (a via, approach, boss approach or stand
// that is not a break and not the objective) and how far the live path passes from it.
type HandPoint struct {
	Seq       int           `json:"seq"`
	Kind      string        `json:"kind"`
	At        dungeon.Point `json:"at"`
	NearYd    float64       `json:"near_yd"`
	Redundant bool          `json:"redundant"` // within HandNearYd of the live path
}

// BossesReport is one plan's bosses-mode report.
type BossesReport struct {
	Plan   string     `json:"plan"`
	Map    uint32     `json:"map"`
	Status string     `json:"status,omitempty"`
	Avoid  int        `json:"avoid_boxes"`
	Legs   []BossLeg  `json:"legs"`
	PlanMs float64    `json:"plan_ms"`
	Tiles  []TileFile `json:"tiles"`
}

// Failed is a leg the live plan could not finish and that is a fair test: it started where the
// plan's own journey does.
func (l *BossLeg) Failed() bool {
	return !l.NonComparable && (l.Status == string(navmesh.Partial) || l.Status == string(navmesh.NoPath))
}

// Counted is a leg whose metrics enter the aggregates: complete and comparable.
func (l *BossLeg) Counted() bool { return l.Status == "complete" && !l.NonComparable }

// Totals are the aggregates of a bosses report, over the counted legs only.
type Totals struct {
	Legs, Complete, Failed, NoObjective int
	Continuations, ContinuationsOK      int
	Sum                                 Sums // the counted legs
	// Bridged: the counted legs plus the bridged re-plans of the non-comparable ones (each
	// planned from the previous objective): what the mesh reaches when the hand data crosses
	// every break it must. Never mixed into the primary numbers.
	BridgedComplete, BridgedFailed int
	Bridged                        Sums
}

// Sums are the metric sums of a set of complete legs.
type Sums struct {
	Packs, PacksOn, PacksCorridor int
	Hand, HandRedundant           int
	ZJumps                        int
	LiveYd                        float64
}

func (s *Sums) add(l *BossLeg) {
	s.Packs += len(l.Packs)
	s.PacksOn += l.PacksOnPath
	for i := range l.Packs {
		if l.Packs[i].InCorridor {
			s.PacksCorridor++
		}
	}
	s.Hand += len(l.Hand)
	s.HandRedundant += l.HandRedundant
	s.ZJumps += len(l.ZJumps)
	s.LiveYd += l.Live.LiveLenYd
}

// Totals sums the report. A failed leg and a non-comparable continuation add nothing to the
// primary sums.
func (r BossesReport) Totals() Totals {
	t := Totals{Legs: len(r.Legs)}
	for i := range r.Legs {
		l := &r.Legs[i]
		switch {
		case l.Status == "no_objective":
			t.NoObjective++
		case l.NonComparable:
			t.Continuations++
			if l.Status == "complete" {
				t.ContinuationsOK++
			}
			if b := l.Bridged; b != nil {
				if b.Status == "complete" {
					t.BridgedComplete++
					t.Bridged.add(b)
				} else {
					t.BridgedFailed++
				}
			}
		case l.Failed():
			t.Failed++
			t.BridgedFailed++
		case l.Status == "complete":
			t.Complete++
			t.BridgedComplete++
			t.Sum.add(l)
			t.Bridged.add(l)
		}
	}
	return t
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}

// Summary is the plan's one summary line.
func (r BossesReport) Summary() string {
	t := r.Totals()
	if t.Legs == 0 {
		return fmt.Sprintf("%s: map %d, no legs", r.Plan, r.Map)
	}
	s := fmt.Sprintf("%s: map %d, %d legs: %d complete, %d failed", r.Plan, r.Map, t.Legs, t.Complete, t.Failed)
	if t.Continuations > 0 {
		s += fmt.Sprintf(", %d non-comparable (%d reach their objective)", t.Continuations, t.ContinuationsOK)
	}
	if t.NoObjective > 0 {
		s += fmt.Sprintf(", %d without an objective", t.NoObjective)
	}
	if t.Sum.Packs > 0 {
		s += fmt.Sprintf("; packs on path %d/%d (%.0f%%), in corridor %d (%.0f%%)", t.Sum.PacksOn, t.Sum.Packs, pct(t.Sum.PacksOn, t.Sum.Packs),
			t.Sum.PacksCorridor, pct(t.Sum.PacksCorridor, t.Sum.Packs))
	}
	if t.Sum.Hand > 0 {
		s += fmt.Sprintf("; hand points redundant %d/%d (%.0f%%)", t.Sum.HandRedundant, t.Sum.Hand, pct(t.Sum.HandRedundant, t.Sum.Hand))
	}
	s += fmt.Sprintf("; z jumps %d; live %.0f yd; plan %.0f ms", t.Sum.ZJumps, t.Sum.LiveYd, r.PlanMs)
	if t.Continuations > 0 {
		s += fmt.Sprintf("; bridged (non-comparable legs planned from the previous objective): %d complete, %d failed", t.BridgedComplete-t.Complete, t.BridgedFailed-t.Failed)
	}
	return s
}

// Bosses plans each leg of the plan live: from where the party stands straight to the leg's
// objective on a scratch planner with the plan's avoid boxes, no hand points and no break hops.
// The party starts at the entrance; a leg that reaches its objective leaves it standing there.
//
// A leg whose plan does not reach the objective FAILED (partial or no_path): it names the stored
// breaks it ran into, and adds nothing to any aggregate. The leg after it starts from the last
// point the failed path REACHED, which is not where the plan's journey goes on: that leg is
// labelled non-comparable and is excluded as well, until a leg completes and puts the party on
// a real objective again. Such a leg is also planned a second time from the previous leg's
// objective (Bridged), to show what else the mesh does not reach once the break is crossed by
// hand data; that is reported beside the primary numbers, never in them.
//
// A planner error (no folder, no engine, a broken tile) is returned, not reported.
func Bosses(p *navmesh.Planner, mmapsDir string, plan *dungeon.Plan) (BossesReport, error) {
	if err := CheckName(plan.Name); err != nil {
		return BossesReport{}, fmt.Errorf("plan file: %w", err)
	}
	rep := BossesReport{Plan: plan.Name, Map: plan.Map, Status: plan.Status, Avoid: len(plan.Avoid)}
	scratch := p.Scratch()
	defer scratch.Close()
	scratch.SetAvoid(plan.Map, dungeon.NavBoxes(plan.Avoid))

	cur := vec(plan.Entrance)    // where the party stands
	anchor := vec(plan.Entrance) // the previous objective: where the plan's journey stands
	lost := ""                   // why the party is not on the plan's journey: the last failed leg
	for i := range plan.Legs {
		l := &plan.Legs[i]
		obj, kind := legObjective(l)
		if kind == "none" {
			rep.Legs = append(rep.Legs, BossLeg{Leg: i, Name: l.Name, From: point(cur), To: obj, Objective: kind, Status: "no_objective"})
			continue
		}
		bl, end, err := planBossLeg(scratch, plan, i, cur, kind)
		if err != nil {
			return rep, err
		}
		if lost != "" {
			bl.NonComparable = true
			bl.NonComparableWhy = "planned from " + lost + ", not from the previous objective"
			if anchor != cur {
				b, _, err := planBossLeg(scratch, plan, i, anchor, kind)
				if err != nil {
					return rep, err
				}
				bl.Bridged = &b
			}
		}
		if bl.Status == string(navmesh.Complete) {
			cur, lost = end, ""
		} else {
			lost = fmt.Sprintf("where leg %d (%s) ended: %s, %.1f yd short of its objective", i, l.Name, fmtPos(end), dist3(end, vec(obj)))
			cur = end
		}
		anchor = vec(obj)
		rep.PlanMs += bl.PlanMs
		rep.Legs = append(rep.Legs, bl)
	}
	t, err := tiles(p, scratch, mmapsDir, plan)
	rep.Tiles = t
	return rep, err
}

// planBossLeg plans one leg from a start to its objective and measures the result: a complete
// path gets the corridor metrics; a failed one gets the stored breaks it ran into. end is where
// the party stands afterwards.
func planBossLeg(sc *navmesh.Planner, plan *dungeon.Plan, i int, from Vec3, kind string) (BossLeg, Vec3, error) {
	l := &plan.Legs[i]
	obj, _ := legObjective(l)
	bl := BossLeg{Leg: i, Name: l.Name, From: point(from), To: obj, Objective: kind}
	t0 := time.Now()
	path, err := sc.Plan(plan.Map, from, vec(obj))
	if err != nil {
		return bl, from, fmt.Errorf("plan %s leg %d (%s): %w", plan.Name, i, l.Name, err)
	}
	bl.PlanMs = float64(time.Since(t0).Microseconds()) / 1000
	bl.Status = string(path.Status)
	bl.Live = runOf(path, vec(obj))
	bl.Reason = firstLine(path.Reason)
	end := pathEndOr(path, from)
	if path.Status == navmesh.Complete {
		measureBossLeg(&bl, l, path, obj)
		return bl, end, nil
	}
	bl.Breaks = breaksOf(l, path.Points, end, path.Status == navmesh.Partial)
	bl.BreakHits, err = breakHits(sc, plan.Map, l, from, end, vec(obj))
	if err != nil {
		return bl, end, fmt.Errorf("plan %s leg %d (%s): %w", plan.Name, i, l.Name, err)
	}
	return bl, end, nil
}

// measureBossLeg fills the corridor metrics of a leg whose live path completed: the packs the
// path passes, the hand points it makes redundant, the floor jumps, the breaks it passes.
func measureBossLeg(bl *BossLeg, l *dungeon.Leg, path navmesh.Path, obj dungeon.Point) {
	route := path.Points
	bl.Packs = packsOnRoute(l, route)
	for i := range bl.Packs {
		if bl.Packs[i].OnPath {
			bl.PacksOnPath++
		}
	}
	bl.Hand = handPoints(l, route, obj)
	for i := range bl.Hand {
		if bl.Hand[i].Redundant {
			bl.HandRedundant++
		}
	}
	bl.ZJumps = ZJumps(PathSamples(path))
	bl.Breaks = breaksOf(l, route, Vec3{}, false)
}

// packsOnRoute says, for each pull group, whether its centre lies within the group's pack
// radius of the route (horizontally, and no more than CorridorMaxDZYd above or below it). The
// centre is the one the driver pulls at: the positions' centroid, or a hold's spot.
func packsOnRoute(l *dungeon.Leg, route []Vec3) []PullOnPath {
	var out []PullOnPath
	for gi := range l.PullGroups {
		g := &l.PullGroups[gi]
		radius := g.Radius
		if radius <= 0 {
			radius = DefaultPackRadius
		}
		pg := PullOnPath{Group: gi, RadiusYd: float64(radius), Entries: g.Entries}
		centre := centroid(g.Positions)
		if g.Hold != nil {
			centre = g.Hold.Spot
		}
		if len(g.Positions) > 0 || g.Hold != nil {
			near := NearestPolyline(route, vec(centre))
			pg.NearYd, pg.DZYd = near.Dist2D, near.DZ
			pg.OnPath = near.Dist3D >= 0 && near.Dist2D <= float64(radius) && near.DZ <= CorridorMaxDZYd
			pg.InCorridor = OnCorridor(near, float64(radius))
		}
		out = append(out, pg)
	}
	return out
}

// handPoints lists the leg's stored hand points (via, to, approach, boss approach, stand) and
// how far the live route passes from each. A break waypoint is not one (it is the break, listed
// on its own) and neither is the objective itself.
func handPoints(l *dungeon.Leg, route []Vec3, obj dungeon.Point) []HandPoint {
	var out []HandPoint
	for j, w := range legWalk(l) {
		switch w.kind {
		case "via", "to", "approach", "boss_approach", "stand":
		default:
			continue
		}
		if w.via != nil && breakKind(*w.via) != "" {
			continue
		}
		if w.at == vec(obj) {
			continue
		}
		near := NearestPolyline(route, w.at)
		out = append(out, HandPoint{Seq: j + 1, Kind: w.kind, At: point(w.at), NearYd: near.Dist3D,
			Redundant: near.Dist3D >= 0 && near.Dist3D <= HandNearYd})
	}
	return out
}

// breakHits lists the stored breaks a failed live plan ran into. A break hop (a via that is a
// direct step, a gm_cross landing or a check_skip) is named when
//   - it bridges: the mesh reaches the hop's foot (the previous via, else the leg's start) from
//     the start and the objective from the hop's landing, so the missing link is that hop; or
//   - it lies within BreakHitYd of where the live path ended, or of the line from there to the
//     objective (the break sits at the end of the reachable ground).
//
// The exact via before a named hop is the hop's foot and is named with it.
func breakHits(sc *navmesh.Planner, mapID uint32, l *dungeon.Leg, start, end, obj Vec3) ([]BreakHit, error) {
	var out []BreakHit
	line := []Vec3{end, obj}
	named := map[int]bool{}
	for vi := range l.Via {
		w := l.Via[vi]
		kind := breakKind(w)
		if kind == "" || (!w.Direct && !w.GMCross && w.CheckSkip == "") {
			continue
		}
		at := vec(w.Point)
		foot := start
		if vi > 0 {
			foot = vec(l.Via[vi-1].Point)
		}
		h := BreakHit{Via: vi, Kind: kind, At: w.Point, EndYd: dist3(end, at), LineYd: NearestPolyline(line, at).Dist3D}
		var why []string
		toFoot, err := sc.Plan(mapID, start, foot)
		if err != nil {
			return nil, err
		}
		if toFoot.Status == navmesh.Complete {
			fromLanding, err := sc.Plan(mapID, at, obj)
			if err != nil {
				return nil, err
			}
			if fromLanding.Status == navmesh.Complete {
				h.Bridges = true
				why = append(why, fmt.Sprintf("the mesh reaches its foot %s from the start and the objective from its landing", fmtPos(foot)))
			}
		}
		if h.EndYd <= BreakHitYd || h.LineYd <= BreakHitYd {
			why = append(why, fmt.Sprintf("it lies %.1f yd from the live end and %.1f yd from the line to the objective", h.EndYd, h.LineYd))
		}
		if len(why) == 0 {
			continue
		}
		h.Why = strings.Join(why, "; ")
		named[vi] = true
		out = append(out, h)
	}
	// The exact foot before a named hop.
	for vi := range l.Via {
		if vi+1 < len(l.Via) && named[vi+1] && l.Via[vi].Exact && !named[vi] {
			w := l.Via[vi]
			at := vec(w.Point)
			out = append(out, BreakHit{Via: vi, Kind: breakKind(w), At: w.Point, EndYd: dist3(end, at),
				LineYd: NearestPolyline(line, at).Dist3D, Why: fmt.Sprintf("the exact foot of via %d's hop", vi+1)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Via < out[j].Via })
	return out, nil
}
