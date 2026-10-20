package routeoracle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
)

// Run is one live planning of a leg: what the navmesh answered for it.
type Run struct {
	Status    string  `json:"status"`     // complete, partial, no_path; or error, when the planner failed
	EndGapYd  float64 `json:"end_gap_yd"` // from the path's end to the leg's objective
	LiveLenYd float64 `json:"live_len_yd"`
	Reason    string  `json:"reason,omitempty"`
}

// StoredPoint is a stored point of a leg and how far the live path passes from it.
type StoredPoint struct {
	Seq    int           `json:"seq"`  // the point's place among the leg's stored points, from 1
	Kind   string        `json:"kind"` // via, to, approach, pull, hold, boss_approach, boss, objective
	At     dungeon.Point `json:"at"`
	NearYd float64       `json:"near_yd"`
	// Redundant: within RedundantYd of the live path, so the stored point adds nothing.
	Redundant bool `json:"redundant"`
}

// BreakTouched is a break waypoint of a leg (a via with exact/direct/gm_cross/check_skip)
// and whether the leg's live path touched it.
type BreakTouched struct {
	Via     int           `json:"via"` // the waypoint's index in the leg's via, from 0
	Kind    string        `json:"kind"`
	At      dungeon.Point `json:"at"`
	Touched bool          `json:"touched"`
	Why     string        `json:"why,omitempty"`
}

// PullOnPath is a pull group and whether the live path's corridor holds one of its positions.
type PullOnPath struct {
	Group    int      `json:"group"`
	OnPath   bool     `json:"on_path"`
	NearYd   float64  `json:"near_yd"` // horizontal distance of the nearest position to the path
	DZYd     float64  `json:"dz_yd"`
	RadiusYd float64  `json:"radius_yd"`
	Entries  []uint32 `json:"entries"`
}

// LegReport is one leg's comparison of the stored route and the live path.
type LegReport struct {
	Leg  int           `json:"leg"`
	Name string        `json:"name,omitempty"`
	From dungeon.Point `json:"from"` // where the leg's live path starts (the previous leg's objective, or the entrance)
	To   dungeon.Point `json:"to"`   // the leg's objective
	// Objective says what the leg's objective point is: boss, to, step (a leg without a boss
	// or to: its last step's point) or none (nothing planned for the leg).
	Objective string `json:"objective"`
	Plain     Run    `json:"plain"` // the plan on the plain planner
	Avoid     Run    `json:"avoid"` // the plan on a scratch planner with SetAvoid(plan.avoid)

	StoredLenYd float64 `json:"stored_len_yd"` // the leg's stored chain, from the previous leg's end
	// Ratio is stored length over live length (the avoid run's): below 1 the stored chain cuts
	// corners the walk does not take, above 1 the walk goes where the stored points do not.
	Ratio         float64        `json:"ratio"`
	StoredMaxYd   float64        `json:"stored_max_yd"` // the stored point farthest from the live path
	StoredMeanYd  float64        `json:"stored_mean_yd"`
	ZJumps        []ZJump        `json:"z_jumps,omitempty"`
	Breaks        []BreakTouched `json:"breaks,omitempty"`
	Pulls         []PullOnPath   `json:"pulls,omitempty"`
	Stored        []StoredPoint  `json:"stored,omitempty"`
	BreaksTouched int            `json:"breaks_touched"`
	PacksOnPath   int            `json:"packs_on_path"`
	PlanMs        float64        `json:"plan_ms"` // both runs' planning time together
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
	PlanMs float64     `json:"plan_ms"` // the legs' planning time together
	Tiles  []TileFile  `json:"tiles"`
}

// Summary is the plan's one summary line: legs by status (the avoid run's), the mean ratio
// over the legs whose live walk is at least 1 yd (a leg whose objective stands on the last
// one's has no walk to measure), how many packs of how many the corridor holds, and the plan
// time.
func (r Report) Summary() string {
	var complete, partial, nopath, other int
	var ratio, on, packs float64
	liveLegs := 0
	for i := range r.Legs {
		switch r.Legs[i].Avoid.Status {
		case string(navmesh.Complete):
			complete++
		case string(navmesh.Partial):
			partial++
		case string(navmesh.NoPath):
			nopath++
		default:
			other++
		}
		if r.Legs[i].Avoid.LiveLenYd >= 1 {
			liveLegs++
			ratio += r.Legs[i].Ratio
		}
		on += float64(r.Legs[i].PacksOnPath)
		packs += float64(len(r.Legs[i].Pulls))
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
	if liveLegs > 0 {
		mean = ratio / float64(liveLegs)
	}
	return fmt.Sprintf("%s: map %d, %d legs: %d complete, %d partial, %d no_path; mean ratio %.2f%s; plan %.0f ms",
		r.Plan, r.Map, n, complete, partial, nopath, mean, packsOn, r.PlanMs)
}

// Oracle builds the plan's report: per leg, the objective chain's walk planned twice (plain,
// and on a scratch planner with the plan's avoid boxes), the stored chain's length and its
// points' distances to the live path, and the leg's breaks and pull groups.
//
// The live path of the metrics is the avoid run's: the one a run keeps out of the plan's
// avoid boxes with. A planner error (no folder, no engine, a broken tile) is returned, not
// reported.
func Oracle(p *navmesh.Planner, mmapsDir string, plan *dungeon.Plan) (Report, error) {
	rep := Report{Plan: plan.Name, Map: plan.Map, Status: plan.Status, Avoid: len(plan.Avoid)}
	scratch := p.Scratch()
	defer scratch.Close()
	scratch.SetAvoid(plan.Map, dungeon.NavBoxes(plan.Avoid))

	// The stored chain: the entrance, then every leg's own points in the order planexport
	// writes them. Leg i's stored walk runs from where leg i-1's chain ended to its own last
	// point (the first index of the connection is legStart).
	chain := []Vec3{vec(plan.Entrance)}
	legStart := 0

	prev := vec(plan.Entrance) // where the leg's live path starts: the previous leg's objective
	for i := range plan.Legs {
		l := &plan.Legs[i]
		lr := LegReport{Leg: i, Name: l.Name, From: dungeon.Point{X: prev[0], Y: prev[1], Z: prev[2]}}
		obj, kind := legObjective(l)
		lr.To = obj
		lr.Objective = kind

		// The leg's stored points go on the chain before the walk is measured, so the stored
		// length holds the hop in from the previous leg's end.
		for _, s := range legStored(l, obj, kind) {
			chain = append(chain, s.at)
			lr.Stored = append(lr.Stored, StoredPoint{Seq: len(lr.Stored) + 1, Kind: s.kind,
				At: dungeon.Point{X: s.at[0], Y: s.at[1], Z: s.at[2]}})
		}
		lr.StoredLenYd = PolylineLength(chain[legStart:])
		legStart = len(chain) - 1

		if kind == "none" {
			lr.Plain = Run{Status: "no_objective"}
			lr.Avoid = lr.Plain
			rep.Legs = append(rep.Legs, lr)
			continue
		}
		t1 := time.Now()
		plain, err := p.Plan(plan.Map, prev, vec(obj))
		if err != nil {
			return rep, fmt.Errorf("plan %s leg %d (%s): plain plan: %w", plan.Name, i, l.Name, err)
		}
		aware, err := scratch.Plan(plan.Map, prev, vec(obj))
		if err != nil {
			return rep, fmt.Errorf("plan %s leg %d (%s): avoid plan: %w", plan.Name, i, l.Name, err)
		}
		lr.PlanMs = float64(time.Since(t1).Microseconds()) / 1000
		rep.PlanMs += lr.PlanMs
		lr.Plain = runOf(plain, obj)
		lr.Avoid = runOf(aware, obj)
		prev = vec(obj)

		if aware.Status != navmesh.NoPath && len(aware.Points) > 0 {
			storedNear(&lr, aware)
			lr.ZJumps = ZJumps(PathSamples(aware))
			lr.Breaks = breaks(l, aware)
			lr.Pulls = pullGroups(l, aware)
		}
		for _, b := range lr.Breaks {
			if b.Touched {
				lr.BreaksTouched++
			}
		}
		for _, g := range lr.Pulls {
			if g.OnPath {
				lr.PacksOnPath++
			}
		}
		rep.Legs = append(rep.Legs, lr)
	}
	t, err := tiles(p, scratch, mmapsDir, plan)
	rep.Tiles = t
	if err != nil {
		return rep, err
	}
	return rep, nil
}

// runOf turns a planned path into a Run.
func runOf(p navmesh.Path, goal dungeon.Point) Run {
	r := Run{Status: string(p.Status), LiveLenYd: float64(p.Length), Reason: p.Reason}
	if n := len(p.Points); n > 0 {
		r.EndGapYd = dist3(p.Points[n-1], vec(goal))
	}
	return r
}

// storedNear measures every stored point of the leg against the live path's polyline: how
// far the path passes from it, and whether the point is redundant (within RedundantYd of it).
// The mean and the most go in the leg's report.
func storedNear(lr *LegReport, aware navmesh.Path) {
	var sum, most float64
	n := 0
	for i := range lr.Stored {
		near := NearestPolyline(aware.Points, vec(lr.Stored[i].At))
		lr.Stored[i].NearYd = near.Dist3D
		lr.Stored[i].Redundant = Redundant(near)
		sum += near.Dist3D
		if near.Dist3D > most {
			most = near.Dist3D
		}
		n++
	}
	if n > 0 {
		lr.StoredMeanYd = sum / float64(n)
		lr.StoredMaxYd = most
	}
	if lr.Avoid.LiveLenYd > 0 {
		lr.Ratio = lr.StoredLenYd / lr.Avoid.LiveLenYd
	}
}

// breaks lists the leg's break waypoints (a via with exact/direct/gm_cross/check_skip) and
// whether the leg's live path touched them: within BreakNearYd of the foot, or, when the leg
// is Partial, with the path's end within BreakGapYd of the foot.
func breaks(l *dungeon.Leg, aware navmesh.Path) []BreakTouched {
	var out []BreakTouched
	for vi := range l.Via {
		w := l.Via[vi]
		kind := breakKind(w)
		if kind == "" {
			continue
		}
		b := BreakTouched{Via: vi, Kind: kind, At: w.Point}
		if len(aware.Points) > 0 {
			near := NearestPolyline(aware.Points, vec(w.Point))
			if near.Dist3D <= BreakNearYd {
				b.Touched = true
				b.Why = fmt.Sprintf("the path passes %.1f yd from it", near.Dist3D)
			}
			if !b.Touched && aware.Status == navmesh.Partial {
				if end := aware.Points[len(aware.Points)-1]; dist3(end, vec(w.Point)) <= BreakGapYd {
					b.Touched = true
					b.Why = fmt.Sprintf("the partial path ends %.1f yd from it", dist3(end, vec(w.Point)))
				}
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
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += "," + p
	}
	return out
}

// pullGroups is the leg's pull groups and whether the live path's corridor (the group's
// radius plus CorridorSlackYd horizontally, CorridorMaxDZYd in height) holds one of the
// group's positions.
func pullGroups(l *dungeon.Leg, aware navmesh.Path) []PullOnPath {
	var out []PullOnPath
	for gi := range l.PullGroups {
		g := &l.PullGroups[gi]
		radius := g.Radius
		if radius <= 0 {
			radius = DefaultPackRadius
		}
		pg := PullOnPath{Group: gi, RadiusYd: float64(radius), Entries: g.Entries}
		for _, p := range g.Positions {
			near := NearestPolyline(aware.Points, vec(p))
			if !pg.OnPath || near.Dist2D < pg.NearYd {
				pg.NearYd, pg.DZYd = near.Dist2D, near.DZ
			}
			if OnCorridor(near, float64(radius)) {
				pg.OnPath = true
			}
		}
		out = append(out, pg)
	}
	return out
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

// lastStepPoint is the last step of the list that holds a position: an object's or trigger's
// stand point, else the point the object stands at.
func lastStepPoint(steps []dungeon.Step) (dungeon.Point, bool) {
	for i := len(steps) - 1; i >= 0; i-- {
		st := steps[i]
		switch {
		case st.UseObject != nil:
			if st.UseObject.Stand != nil {
				return *st.UseObject.Stand, true
			}
			if st.UseObject.Near != nil {
				return *st.UseObject.Near, true
			}
		case st.LootObject != nil:
			if st.LootObject.Stand != nil {
				return *st.LootObject.Stand, true
			}
			if st.LootObject.Near != nil {
				return *st.LootObject.Near, true
			}
		case st.AreaTrigger != nil:
			return st.AreaTrigger.Stand, true
		}
	}
	return dungeon.Point{}, false
}

// storedSpec is one stored point to put on the chain.
type storedSpec struct {
	at   Vec3
	kind string
}

// legStored is the leg's own stored points in the order planexport writes the walk: the via
// waypoints, the To, each pull group's approach points and its pull point (the positions'
// centroid, or a hold's spot), the boss's approach points and its position. A leg without a
// boss or To ends its chain at its objective (the last step's point).
func legStored(l *dungeon.Leg, obj dungeon.Point, objKind string) []storedSpec {
	var out []storedSpec
	add := func(p dungeon.Point, kind string) {
		out = append(out, storedSpec{at: vec(p), kind: kind})
	}
	for _, w := range l.Via {
		add(w.Point, "via")
	}
	if l.To != (dungeon.Point{}) {
		add(l.To, "to")
	}
	for gi := range l.PullGroups {
		g := &l.PullGroups[gi]
		for _, a := range g.Approach {
			add(a, "approach")
		}
		if g.Hold != nil {
			add(g.Hold.Spot, "hold")
			continue
		}
		add(centroid(g.Positions), "pull")
	}
	if l.Boss != nil {
		for _, a := range l.Boss.Approach {
			add(a, "boss_approach")
		}
		add(l.Boss.Position, "boss")
	}
	if objKind == "step" {
		add(obj, "objective")
	}
	return out
}

// centroid is the mean of the points, as planexport writes a pull row.
func centroid(pts []dungeon.Point) dungeon.Point {
	var cx, cy, cz float64
	for _, p := range pts {
		cx, cy, cz = cx+float64(p.X), cy+float64(p.Y), cz+float64(p.Z)
	}
	n := float64(len(pts))
	return dungeon.Point{X: float32(cx / n), Y: float32(cy / n), Z: float32(cz / n)}
}

func vec(p dungeon.Point) Vec3 { return Vec3{p.X, p.Y, p.Z} }

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
				first = fmt.Errorf("plan %s tile %s: %w", plan.Name, n, err)
			}
			continue
		}
		sum := sha256.Sum256(b)
		out = append(out, TileFile{Name: n, Sha256: hex.EncodeToString(sum[:]), Bytes: int64(len(b))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, first
}
