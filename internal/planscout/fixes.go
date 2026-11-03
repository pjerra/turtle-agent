package planscout

import (
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/pjerra/turtle-agent/internal/aggroest"
	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/planlint"
	"github.com/pjerra/turtle-agent/internal/routeedit"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

// The fix kinds.
const (
	FixSnapPoint = "snap_point"
	FixInsertVia = "insert_via"
	FixDryDetour = "dry_detour"
	FixMoveStand = "move_stand"
	FixSplitPull = "split_pull"
)

// FixPack is one pack the split_pull names: the plan group that owns it later, or "unplanned pack <guid>".
type FixPack struct {
	Pack   string   `json:"pack"`
	Guids  []uint32 `json:"guids"`
	Bodies int      `json:"bodies"`
}

// FixStand is a proposed stand (move_stand, split_pull).
type FixStand struct {
	X       float32 `json:"x"`
	Y       float32 `json:"y"`
	Z       float32 `json:"z"`
	ClearYd float64 `json:"clear_yd"`
	PathYd  float64 `json:"path_yd"`
}

// Fix is one proposed correction of the plan.
type Fix struct {
	ID    int                   `json:"id"`
	Step  int                   `json:"step"`
	Check string                `json:"check"`
	Kind  string                `json:"kind"`
	Apply bool                  `json:"apply"`
	Ref   *planexport.RowOrigin `json:"ref,omitempty"`
	Point *dungeon.Point        `json:"point,omitempty"`
	Vias  []dungeon.Point       `json:"vias,omitempty"`
	// Joiners and PullFirst are split_pull's: the packs outside the pull, in the order to pull them first.
	Joiners   []FixPack `json:"joiners,omitempty"`
	PullFirst []string  `json:"pull_first,omitempty"`
	Stand     *FixStand `json:"stand,omitempty"`
	Reason    string    `json:"reason,omitempty"`
}

// FixFile is the fix file (tb-scout-fix v1).
type FixFile struct {
	Format     string `json:"format"`
	Version    int    `json:"version"`
	Plan       string `json:"plan"`
	Map        uint32 `json:"map"`
	RowsSHA256 string `json:"rows_sha256"`
	Scout      string `json:"scout"`
	Fixes      []Fix  `json:"fixes"`
}

// waterPolygonNav is what a dry detour needs: the polygons of a box and a planner that keeps out of the boxes it is given.
type waterPolygonNav interface {
	Polygons(mapID uint32, x0, y0, x1, y1 float32) ([]navmesh.Polygon, bool, error)
	Scratch() *navmesh.Planner
}

// ProposeFixes turns the report's non-ok checks into corrections of the plan. A row without an origin gets apply:false and
// coordinates only.
func ProposeFixes(rep *Report, in Input, nav Nav, w *worldspawn.World) []Fix {
	var out []Fix
	spread := float64(aggroest.StandSpreadPartyYd)
	if in.Plan != nil && in.Plan.IsRaid() {
		spread = aggroest.StandSpreadRaidYd
	}
	level := repLevel(in, rep)
	rows := planlint.PullRowsOf(in.Rows.Steps)
	for _, st := range rep.Steps {
		idx := st.Step - 1
		if idx < 0 || idx >= len(in.Rows.Steps) {
			continue
		}
		for _, c := range st.Checks {
			if c.Verdict == VerdictOK || c.Source != SourceOffline {
				continue
			}
			origin := st.Origin
			switch c.Name {
			case planlint.LintOffFloor, planlint.LintReach:
				if f, ok := snapFix(rep, in, nav, st, c, origin); ok {
					out = append(out, f)
				}
			case planlint.LintHop, planlint.LintArrival, planlint.LintClimb:
				if f, ok := viaFix(rep, in, nav, st, c, origin); ok {
					out = append(out, f)
				}
			case planlint.LintWater:
				out = append(out, dryFixes(rep, in, nav, st, c, origin, idx)...)
			case planlint.LintAggro:
				if f, ok := standFix(in, nav, w, st, c, origin, idx, rows, level, spread); ok {
					out = append(out, f)
				}
			case planlint.LintJoiners:
				out = append(out, splitFixes(in, nav, w, st, c, origin, idx, rows, level, spread)...)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Step != out[j].Step {
			return out[i].Step < out[j].Step
		}
		return out[i].Kind < out[j].Kind
	})
	for i := range out {
		out[i].ID = i + 1
	}
	return out
}

func repLevel(in Input, rep *Report) int {
	if in.Plan != nil {
		if _, hi := in.Plan.Band(); hi != 0 {
			return hi
		}
	}
	return 60
}

// prevPoint is the positioned row before idx.
func prevPoint(in Input, idx int) (dungeon.Point, bool) {
	for k := idx - 1; k >= 0; k-- {
		s := in.Rows.Steps[k]
		if s.X != 0 || s.Y != 0 || s.Z != 0 {
			return dungeon.Point{X: s.X, Y: s.Y, Z: s.Z}, true
		}
	}
	return dungeon.Point{}, false
}

func rowPoint(s planexport.StepRow) dungeon.Point { return dungeon.Point{X: s.X, Y: s.Y, Z: s.Z} }

// snapFix moves a row point to the nearest walkable spot on its floor, with a complete path from the row before it.
func snapFix(rep *Report, in Input, nav Nav, st StepResult, c Check, origin *planexport.RowOrigin) (Fix, bool) {
	p := dungeon.Point{X: st.X, Y: st.Y, Z: st.Z}
	q, ok, err := nav.Nearest(rep.Map, navmesh.Vec3{p.X, p.Y, p.Z})
	if err != nil || !ok {
		return Fix{}, false
	}
	to := dungeon.Point{X: q[0], Y: q[1], Z: q[2]}
	if d := dist3(p, to); d > routeedit.FloorWindow {
		return Fix{}, false
	}
	if prev, ok := prevPoint(in, st.Step-1); ok {
		path, err := nav.Plan(rep.Map, navmesh.Vec3{prev.X, prev.Y, prev.Z}, q)
		if err != nil || path.Status != navmesh.Complete {
			return Fix{}, false
		}
	}
	f := Fix{Step: st.Step, Check: c.Name, Kind: FixSnapPoint, Apply: origin != nil, Ref: origin, Point: &to,
		Reason: "the row point moved to the nearest walkable floor, with a complete path from the row before it"}
	return f, true
}

// viaFix cuts the offline server path into vias (every MoveGapCutYd, and for a climb every ClimbViaDzYd of height).
func viaFix(rep *Report, in Input, nav Nav, st StepResult, c Check, origin *planexport.RowOrigin) (Fix, bool) {
	prev, ok := prevPoint(in, st.Step-1)
	if !ok {
		return Fix{}, false
	}
	to := dungeon.Point{X: st.X, Y: st.Y, Z: st.Z}
	pts := pathPoints(rep.Map, nav, prev, to)
	if len(pts) < 2 {
		return Fix{}, false
	}
	vias := cutVias(rep.Map, nav, pts, c.Name == planlint.LintClimb)
	if len(vias) == 0 {
		return Fix{}, false
	}
	reason := c.Reason
	if c.Name == planlint.LintClimb {
		reason += "; vias every " + ftoa(planlint.ClimbViaDzYd) + " yd of climb"
	} else {
		reason += "; vias every " + ftoa(planexport.MoveGapCutYd) + " yd"
	}
	f := Fix{Step: st.Step, Check: c.Name, Kind: FixInsertVia, Apply: origin != nil, Ref: origin, Vias: vias,
		Reason: reason}
	if origin == nil {
		f.Reason += "; no plan element for this row: a migration's rows are not the plan's own"
	}
	return f, true
}

func pathPoints(mapID uint32, nav Nav, from, to dungeon.Point) []dungeon.Point {
	sp, err := nav.ServerPath(mapID, navmesh.Vec3{from.X, from.Y, from.Z}, navmesh.Vec3{to.X, to.Y, to.Z})
	if err == nil && len(sp.Points) >= 2 && sp.Type&navmesh.PathNoPath == 0 {
		out := make([]dungeon.Point, len(sp.Points))
		for i, p := range sp.Points {
			out[i] = dungeon.Point{X: p[0], Y: p[1], Z: p[2]}
		}
		return out
	}
	path, err := nav.Plan(mapID, navmesh.Vec3{from.X, from.Y, from.Z}, navmesh.Vec3{to.X, to.Y, to.Z})
	if err != nil || len(path.Points) < 2 {
		return nil
	}
	out := make([]dungeon.Point, len(path.Points))
	for i, p := range path.Points {
		out[i] = dungeon.Point{X: p[0], Y: p[1], Z: p[2]}
	}
	return out
}

// cutVias keeps every MoveGapCutYd along the path and, for a climb, a point before each ClimbViaDzYd of height change.
func cutVias(mapID uint32, nav Nav, pts []dungeon.Point, climb bool) []dungeon.Point {
	var out []dungeon.Point
	add := func(p dungeon.Point) {
		if q, ok, err := nav.Nearest(mapID, navmesh.Vec3{p.X, p.Y, p.Z}); err == nil && ok {
			p = dungeon.Point{X: q[0], Y: q[1], Z: q[2]}
		}
		for _, o := range out {
			if dist3(o, p) < 1 {
				return
			}
		}
		out = append(out, p)
	}
	last := pts[0]
	lastZ := pts[0].Z
	for k := 1; k < len(pts); k++ {
		p := pts[k]
		d := dist3(last, p)
		if d > planexport.MoveGapCutYd {
			n := int(math.Ceil(d / planexport.MoveGapCutYd))
			for j := 1; j < n; j++ {
				f := float32(j) / float32(n)
				add(dungeon.Point{X: last.X + (p.X-last.X)*f, Y: last.Y + (p.Y-last.Y)*f, Z: last.Z + (p.Z-last.Z)*f})
			}
		}
		if climb {
			dz := math.Abs(float64(p.Z - lastZ))
			if n := int(math.Ceil(dz / planlint.ClimbViaDzYd)); n > 1 {
				for j := 1; j < n; j++ {
					f := float32(j) / float32(n)
					add(dungeon.Point{X: last.X + (p.X-last.X)*f, Y: last.Y + (p.Y-last.Y)*f, Z: last.Z + (p.Z-last.Z)*f})
				}
			}
		}
		last, lastZ = p, p.Z
	}
	return out
}

// dryFixes proposes a dry stand for a wet gather point, and a dry route for a wet hop where the navmesh can list its water.
func dryFixes(rep *Report, in Input, nav Nav, st StepResult, c Check, origin *planexport.RowOrigin, idx int) []Fix {
	var out []Fix
	p := dungeon.Point{X: st.X, Y: st.Y, Z: st.Z}
	if st.Kind == planexport.KindMove || st.Kind == planexport.KindHold {
		if f, ok := dryPoint(rep.Map, in, nav, idx, p, origin, c); ok {
			out = append(out, f)
		}
	}
	if f, ok := dryRoute(rep.Map, in, nav, st, c, origin, idx); ok {
		out = append(out, f)
	}
	if len(out) == 0 {
		out = append(out, Fix{Step: st.Step, Check: c.Name, Kind: FixDryDetour, Apply: false, Ref: origin,
			Reason: c.Reason + "; no dry point within " + ftoa(DetourMaxYd) + " yd with a complete path"})
	}
	return out
}

// dryPoint is the nearest point within DetourMaxYd whose floor is not NavWater and that has a complete path from before.
func dryPoint(mapID uint32, in Input, nav Nav, idx int, p dungeon.Point, origin *planexport.RowOrigin, c Check) (Fix, bool) {
	prev, ok := prevPoint(in, idx)
	if !ok {
		prev = p
	}
	for r := float32(2); r <= DetourMaxYd; r += 2 {
		for k := range 16 {
			a := 2 * math.Pi * float64(k) / 16
			q := dungeon.Point{X: p.X + r*float32(math.Cos(a)), Y: p.Y + r*float32(math.Sin(a)), Z: p.Z}
			fl, err := nav.Floors(mapID, q.X, q.Y, p.Z-4, p.Z+4)
			if err != nil {
				continue
			}
			for _, f := range fl {
				if f.Flags&navmesh.NavWater != 0 {
					continue
				}
				at := dungeon.Point{X: q.X, Y: q.Y, Z: f.Z}
				if path, err := nav.Plan(mapID, navmesh.Vec3{prev.X, prev.Y, prev.Z}, navmesh.Vec3{at.X, at.Y, at.Z}); err == nil && path.Status == navmesh.Complete {
					return Fix{Step: stepOf(idx), Check: c.Name, Kind: FixDryDetour, Apply: origin != nil, Ref: origin, Point: &at,
						Reason: "the wet gather point moved to dry ground within " + ftoa(DetourMaxYd) + " yd, with a complete path from the row before it"}, true
				}
			}
		}
	}
	return Fix{}, false
}

func stepOf(idx int) int { return idx + 1 }

// dryRoute re-plans a wet hop around every NavWater polygon of its box (the planner's avoid boxes), cut into vias.
func dryRoute(mapID uint32, in Input, nav Nav, st StepResult, c Check, origin *planexport.RowOrigin, idx int) (Fix, bool) {
	pn, ok := nav.(waterPolygonNav)
	if !ok {
		return Fix{}, false
	}
	prev, ok := prevPoint(in, idx)
	if !ok {
		return Fix{}, false
	}
	to := dungeon.Point{X: st.X, Y: st.Y, Z: st.Z}
	x0, x1 := min32(prev.X, to.X)-20, max32(prev.X, to.X)+20
	y0, y1 := min32(prev.Y, to.Y)-20, max32(prev.Y, to.Y)+20
	polys, _, err := pn.Polygons(mapID, x0, y0, x1, y1)
	if err != nil {
		return Fix{}, false
	}
	var boxes []navmesh.Box
	for _, poly := range polys {
		if poly.Flags&navmesh.NavWater == 0 || len(poly.Verts) == 0 {
			continue
		}
		b := navmesh.Box{Min: poly.Verts[0], Max: poly.Verts[0]}
		for _, v := range poly.Verts[1:] {
			for i := range 3 {
				b.Min[i] = min32(b.Min[i], v[i])
				b.Max[i] = max32(b.Max[i], v[i])
			}
		}
		boxes = append(boxes, b)
	}
	if len(boxes) == 0 {
		return Fix{}, false
	}
	sc := pn.Scratch()
	defer sc.Close()
	sc.SetAvoid(mapID, boxes)
	path, err := sc.Plan(mapID, navmesh.Vec3{prev.X, prev.Y, prev.Z}, navmesh.Vec3{to.X, to.Y, to.Z})
	if err != nil || path.Status != navmesh.Complete {
		return Fix{Step: st.Step, Check: c.Name, Kind: FixDryDetour, Apply: false, Ref: origin,
			Reason: c.Reason + "; the dry route (" + itoa(len(boxes)) + " water polygons) does not complete"}, true
	}
	pts := make([]dungeon.Point, len(path.Points))
	for i, q := range path.Points {
		pts[i] = dungeon.Point{X: q[0], Y: q[1], Z: q[2]}
	}
	vias := cutVias(mapID, sc, pts, false)
	if len(vias) == 0 {
		return Fix{}, false
	}
	return Fix{Step: st.Step, Check: c.Name, Kind: FixDryDetour, Apply: true, Ref: origin, Vias: vias,
		Reason: c.Reason + "; a dry route that keeps out of " + itoa(len(boxes)) + " NavWater polygons"}, true
}

// standFix proposes a stand for a pull whose gather point is too near other packs.
func standFix(in Input, nav Nav, w *worldspawn.World, st StepResult, c Check, origin *planexport.RowOrigin, idx int, rows []planlint.PullRow, level int, spread float64) (Fix, bool) {
	if w == nil {
		return Fix{}, false
	}
	owned := planlint.PullOwners(rows, w.Spawns)
	cand, ok := bestStand(in, nav, w, idx, st, rows, owned, level, spread)
	if !ok {
		return Fix{}, false
	}
	return Fix{Step: st.Step, Check: c.Name, Kind: FixMoveStand, Apply: origin != nil, Ref: origin, Stand: &cand,
		Reason: c.Reason + "; a stand " + ftoa(cand.ClearYd) + " yd clear of every other pack"}, true
}

// bestStand searches the stand candidates around the pull row's point and returns the clearest with the shortest path.
func bestStand(in Input, nav Nav, w *worldspawn.World, idx int, st StepResult, rows []planlint.PullRow, owned map[uint64]int, level int, spread float64) (FixStand, bool) {
	centre := dungeon.Point{X: st.X, Y: st.Y, Z: st.Z}
	prev, _ := prevPoint(in, idx)
	best := FixStand{}
	bestPath := math.Inf(1)
	for _, r := range []float64{StandMinYd, 20, 25, 35, 45, 55} {
		for k := range 24 {
			a := 2 * math.Pi * float64(k) / 24
			q := dungeon.Point{X: centre.X + float32(r)*float32(math.Cos(a)), Y: centre.Y + float32(r)*float32(math.Sin(a)), Z: centre.Z}
			n, ok, err := nav.Nearest(in.Rows.Plan.Map, navmesh.Vec3{q.X, q.Y, q.Z})
			if err != nil || !ok || math.Abs(float64(n[2]-centre.Z)) > StandZTolYd {
				continue
			}
			at := dungeon.Point{X: n[0], Y: n[1], Z: n[2]}
			clear, ok := standClear(w, at, idx, rows[idx].Entries, owned, level, spread)
			if !ok {
				continue
			}
			path, err := nav.Plan(in.Rows.Plan.Map, navmesh.Vec3{prev.X, prev.Y, prev.Z}, navmesh.Vec3{at.X, at.Y, at.Z})
			if err != nil || path.Status != navmesh.Complete {
				continue
			}
			if d := float64(path.Length); d < bestPath {
				bestPath = d
				best = FixStand{X: at.X, Y: at.Y, Z: at.Z, ClearYd: clear, PathYd: d}
			}
		}
	}
	return best, bestPath < math.Inf(1)
}

// standClear says every hostile spawn not owned by this pull or an earlier one is farther than its reach plus the spread
// plus the margin (and a later pull's pack at least StandHostileYd), and returns the smallest such margin.
func standClear(w *worldspawn.World, at dungeon.Point, idx int, entries []uint32, owned map[uint64]int, level int, spread float64) (float64, bool) {
	worst := math.Inf(1)
	for _, sp := range w.Spawns {
		if !sp.Hostile() || sp.LevelMax <= 1 {
			continue
		}
		if hasEntry(sp.Entry, entries) {
			continue // the pack this stand is for
		}
		reach := float64(worldspawn.AggroRadius(sp, level, 1))
		if reach <= 0 {
			continue
		}
		o, isOwned := owned[sp.GUID]
		need := reach + spread + StandPullMarginYd
		if isOwned && o > idx && need < StandHostileYd {
			need = StandHostileYd
		}
		if isOwned && o <= idx {
			continue // this pull, or one already dead
		}
		d := dist3(at, sp.Pos)
		if m := d - need; m < worst {
			worst = m
		}
		if d <= need {
			return 0, false
		}
	}
	return worst, true
}

// splitFixes names a pull's joiner packs, the plan group that owns each later, and the stand to pull from.
func splitFixes(in Input, nav Nav, w *worldspawn.World, st StepResult, c Check, origin *planexport.RowOrigin, idx int, rows []planlint.PullRow, level int, spread float64) []Fix {
	var out []Fix
	var owned map[uint64]int
	if w != nil {
		owned = planlint.PullOwners(rows, w.Spawns)
	}
	packs := make([]FixPack, 0, len(c.Packs))
	for _, p := range c.Packs {
		name := packName(in, rows, owned, p, idx)
		packs = append(packs, FixPack{Pack: name, Guids: p.Guids, Bodies: p.Bodies})
	}
	sort.SliceStable(packs, func(i, j int) bool {
		return packDist(w, packs[i], st) < packDist(w, packs[j], st)
	})
	first := make([]string, len(packs))
	for i, p := range packs {
		first[i] = p.Pack
	}
	f := Fix{Step: st.Step, Check: c.Name, Kind: FixSplitPull, Apply: false, Ref: origin, Joiners: packs, PullFirst: first,
		Reason: c.Reason + "; a new pull group changes ownership and the step pins: advice for the plan worker, never applied"}
	if w != nil {
		if cand, ok := bestStand(in, nav, w, idx, st, rows, owned, level, spread); ok {
			f.Stand = &cand
		}
	}
	out = append(out, f)
	return out
}

// packName names a joiner pack by the plan pull group that owns it later, or as unplanned.
func packName(in Input, rows []planlint.PullRow, owned map[uint64]int, p planlint.JoinerPack, idx int) string {
	for _, g := range p.Guids {
		o, ok := owned[uint64(g)]
		if !ok || o <= idx {
			continue
		}
		if in.Origins != nil && o < len(in.Origins) {
			org := in.Origins[o]
			if org.Part == "pull" || org.Part == "boss" || org.Part == "hold" {
				return fmt.Sprintf("leg %d group %d (step %d)", rows[o].Leg, org.Group, o+1)
			}
		}
		return fmt.Sprintf("leg %d (step %d)", rows[o].Leg, o+1)
	}
	return "unplanned pack " + utoa(p.ID)
}

// packDist is the distance from the pull row to the pack's nearest spawn (the order to pull the joiners first).
func packDist(w *worldspawn.World, p FixPack, st StepResult) float64 {
	if w == nil {
		return math.Inf(1)
	}
	guids := map[uint32]bool{}
	for _, g := range p.Guids {
		guids[g] = true
	}
	best := math.Inf(1)
	for _, sp := range w.Spawns {
		if !guids[uint32(sp.GUID)] {
			continue
		}
		if d := dist3(dungeon.Point{X: st.X, Y: st.Y, Z: st.Z}, sp.Pos); d < best {
			best = d
		}
	}
	return best
}

func ftoa(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
func itoa(v int) string     { return strconv.Itoa(v) }
func utoa(v uint64) string  { return strconv.FormatUint(v, 10) }

func min32(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}

func max32(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

func dist3(a, b dungeon.Point) float64 {
	dx, dy, dz := float64(a.X-b.X), float64(a.Y-b.Y), float64(a.Z-b.Z)
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

func hasEntry(e uint32, es []uint32) bool {
	for _, x := range es {
		if x == e {
			return true
		}
	}
	return false
}
