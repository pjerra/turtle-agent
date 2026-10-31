// Package trashcheck estimates what one walk of a raid plan wakes: for every segment the run leader walks, the packs whose
// aggro radius (worldspawn.AggroRadius, the run leader's PullAggroEstimatePolicy rule 2), the family assist hop (rule 3) or
// an aggro link / creature group (rule 1) brings them along, and the windows in which a patrolling pack's loop crosses the
// route. A pack the plan's own pull or boss row clears before the segment is dead and ignored; a pack no row ever clears is
// unplanned and stays alive. The model is static: spawn positions come from the world export, patrol phase is unknown unless a
// recording gives it, and there is no vmap line of sight, so aggro through a wall is over-counted (every report says so).
package trashcheck

import (
	"fmt"
	"math"
	"sort"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/patrols"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

// Format and Version name the report (tb-trash-check v1).
const (
	Format  = "tb-trash-check"
	Version = 1
)

// Segment verdicts, worst first (Report.Counts counts them).
const (
	VerdictClear  = "clear"
	VerdictOwn    = "own"
	VerdictPatrol = "patrol"
	VerdictDanger = "danger"
)

// Pack classes of a segment.
const (
	ClassOwn    = "own"    // the plan pulls this pack here (or stands right before doing so)
	ClassDanger = "danger" // this segment wakes the pack and no row pulls it here
)

const (
	// routeSampleYd is the spacing of the route samples the model measures from.
	routeSampleYd = 2
	// otherFloorYd is how far above or below a route sample a spawn may stand before it is taken for another floor: the
	// navmesh's own poly search is about 5 yd, and a raid's floors are more than 15 yd apart.
	otherFloorYd = 15
	// patrolStepS is the simulation step of a patrol loop, seconds.
	patrolStepS = 0.5
	// standReachYd is added to the aggro reach for a stand: the party stands still there (the pull stand, the revive point).
	standReachYd = 5
	// pullReachYd and bossReachYd are the clearing model's reach of a targeting row with radius 0
	// (planexport.DefaultPullYd and DefaultBossRad).
	pullReachYd = 15
	bossReachYd = 30
	// targetSlackYd is added to a targeting row's radius: the pack is pulled from where it stands, not walked onto.
	targetSlackYd = 5
)

// Nav is the route query the model asks for a segment (navmesh.Planner satisfies it). Without it the route is the straight
// line between the two rows.
type Nav interface {
	ServerPath(mapID uint32, from, to navmesh.Vec3) (navmesh.ServerPath, error)
}

// Options are the model's knobs (the command's flags).
type Options struct {
	// Level is the party's lowest level: the widest aggro radius, as the run leader assumes.
	Level int
	// Spread is how far the party spreads from the route (the run leader's spreadYd; 10 for raids).
	Spread float32
	// AggroRate scales every aggro radius (1: the core's).
	AggroRate float32
	// AssistYd is the family assist hop's radius (the core's CreatureFamilyAssistanceRadius, 10 yd).
	AssistYd float32
	// Nav is the route query; nil: straight lines.
	Nav Nav
	// Recorded are patrol recordings (tb-patrols): a patrol whose id or a guid matches a DB patrol replaces it, any other
	// is added.
	Recorded []patrols.Patrol
}

// Waker is one spawn a segment wakes by proximity.
type Waker struct {
	GUID    uint64  `json:"guid"`
	Entry   uint32  `json:"entry"`
	Name    string  `json:"name"`
	Level   int     `json:"level"`
	AggroYd float32 `json:"aggro_yd"` // AggroRadius + spread + wander, the reach the spawn woke at
	MinYd   float32 `json:"min_yd"`   // its distance to the segment's nearest route sample, 3D yd
}

// SegmentPack is one pack a segment wakes.
type SegmentPack struct {
	Pack uint64 `json:"pack"`
	// Class is own when the plan pulls the pack at this segment (its targeting row or the stand right before it), danger
	// otherwise.
	Class      string   `json:"class"`
	TargetedAt *int     `json:"targeted_at"` // the step of the pull/boss/hold row that clears the pack, null: unplanned
	Unplanned  bool     `json:"unplanned"`
	Wakers     []Waker  `json:"wakers"`
	Assisted   []uint64 `json:"assisted"` // spawns the family assist hop of these wakers brings
	Brings     int      `json:"brings"`   // the spawns of the pack that come along (its size)
	// LinkedPacks are the other woken packs of the segment: the closure of the assist hop and the groups brings them together.
	LinkedPacks []uint64 `json:"linked_packs"`
}

// Window is a time interval of a patrol loop, seconds from the loop's start point. A window that crosses the loop's start has
// a ToS beyond the period.
type Window struct {
	FromS float32 `json:"from_s"`
	ToS   float32 `json:"to_s"`
}

// PatrolHit is one patrol whose loop crosses a segment, with the windows it is there.
type PatrolHit struct {
	ID       string   `json:"id"`
	GUIDs    []string `json:"guids"`
	Entries  []uint32 `json:"entries"`
	Name     string   `json:"name"`
	PeriodS  float32  `json:"period_s"`
	Estimate bool     `json:"estimate"`
	Windows  []Window `json:"windows"`
	Share    float32  `json:"share"` // the windows' sum over the period
	At       string   `json:"at"`    // route, stand (a stand before a pull) or start (step 1, the revive point)
	EpochMS  int64    `json:"epoch_ms"`

	pack int  // the index of the patrol's pack in the model, -1: none
	own  bool // the plan pulls this patrol here: the hit is not a finding
}

// Row is one end of a segment: the step's own row.
type Row struct {
	Step    int     `json:"step"`
	Leg     int     `json:"leg"`
	Seq     int     `json:"seq"`
	Kind    string  `json:"kind"`
	Comment string  `json:"comment"`
	X       float32 `json:"x"`
	Y       float32 `json:"y"`
	Z       float32 `json:"z"`
}

// Segment is one hop between two positioned rows and what walking it wakes.
type Segment struct {
	From    Row           `json:"from"`
	To      Row           `json:"to"`
	Verdict string        `json:"verdict"`
	Packs   []SegmentPack `json:"packs"`
	Patrols []PatrolHit   `json:"patrols"`
}

// Vec is a 3D point in the report.
type Vec struct {
	X float32 `json:"x"`
	Y float32 `json:"y"`
	Z float32 `json:"z"`
}

// PackRow is one pack of the map (listed once, not per segment).
type PackRow struct {
	ID         uint64   `json:"id"`
	Members    []uint64 `json:"members"`
	Centroid   Vec      `json:"centroid"`
	TargetedAt *int     `json:"targeted_at"`
	Unplanned  bool     `json:"unplanned"`
	Pooled     bool     `json:"pooled"`
	Patrol     bool     `json:"patrol"`
}

// PatrolRow is one patrol the model simulated.
type PatrolRow struct {
	ID         string   `json:"id"`
	GUIDs      []string `json:"guids"`
	Entries    []uint32 `json:"entries"`
	Name       string   `json:"name"`
	PeriodS    float32  `json:"period_s"`
	Estimate   bool     `json:"estimate"`
	LengthYd   float32  `json:"length_yd"`
	Points     int      `json:"points"`
	Path       []Vec    `json:"path"`
	Source     string   `json:"source"`
	TargetedAt *int     `json:"targeted_at"`
}

// Counts are the segment verdicts.
type Counts struct {
	Clear  int `json:"clear"`
	Own    int `json:"own"`
	Patrol int `json:"patrol"`
	Danger int `json:"danger"`
}

// Report is tb-trash-check v1: the model's answer for one plan.
type Report struct {
	Format    string      `json:"format"`
	Version   int         `json:"version"`
	Plan      string      `json:"plan"`
	Map       uint32      `json:"map"`
	Source    string      `json:"source"`     // "sql <file>" or "json <file>"
	SpawnsDir string      `json:"spawns_dir"` // the world export the spawns came from
	Level     int         `json:"level"`
	Spread    float32     `json:"spread"`
	AggroRate float32     `json:"aggro_rate"`
	AssistYd  float32     `json:"assist_yd"`
	Route     string      `json:"route"` // "server paths (--mmaps)" or "straight lines (no --mmaps)"
	Steps     int         `json:"steps"`
	Segments  []Segment   `json:"segments"`
	Packs     []PackRow   `json:"packs"`
	Patrols   []PatrolRow `json:"patrols"`
	Notes     []string    `json:"notes"`
	Counts    Counts      `json:"counts"`
}

// step is one ai_dungeon_step row with its server step number.
type step struct {
	num        int // the TBM:RUN step number: the row's index in (leg, seq) order plus 1
	leg, seq   int
	kind       string
	comment    string
	p          dungeon.Point
	positioned bool
	radius     float32
	entry      uint32
	entries    []uint32
}

// row renders the step as the report's segment end.
func (s step) row() Row {
	return Row{Step: s.num, Leg: s.leg, Seq: s.seq, Kind: s.kind, Comment: s.comment, X: s.p.X, Y: s.p.Y, Z: s.p.Z}
}

// orderedSteps copies the rows in (leg, seq) order, the order the server numbers its steps in, and numbers them.
func orderedSteps(rs []planexport.StepRow) []step {
	idx := make([]int, len(rs))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		x, y := rs[idx[a]], rs[idx[b]]
		if x.Leg != y.Leg {
			return x.Leg < y.Leg
		}
		return x.Seq < y.Seq
	})
	out := make([]step, len(rs))
	for i, k := range idx {
		r := rs[k]
		out[i] = step{
			num:        i + 1,
			leg:        r.Leg,
			seq:        r.Seq,
			kind:       r.Kind,
			comment:    r.Comment,
			p:          dungeon.Point{X: r.X, Y: r.Y, Z: r.Z},
			positioned: positioned(r.X, r.Y, r.Z),
			radius:     r.Radius,
			entry:      r.Entry,
			entries:    r.Entries,
		}
	}
	return out
}

// positioned says a row has a point: the zero point is the rows' "no position".
func positioned(x, y, z float32) bool { return x != 0 || y != 0 || z != 0 }

// targetEntries are the entries a pull, boss or hold row clears.
func targetEntries(s step) []uint32 {
	out := append([]uint32(nil), s.entries...)
	if s.kind == planexport.KindBoss && s.entry != 0 {
		found := false
		for _, e := range out {
			found = found || e == s.entry
		}
		if !found {
			out = append(out, s.entry)
		}
	}
	return out
}

// targetReach is how near a targeting row may stand to a pack it clears: its radius plus 5 yd, or the default reach when the
// row gives none.
func targetReach(s step) float32 {
	if s.radius > 0 {
		return s.radius + targetSlackYd
	}
	if s.kind == planexport.KindBoss {
		return bossReachYd
	}
	return pullReachYd
}

// isMove says the row is a move (an approach a party can stand at the end of).
func isMove(kind string) bool {
	switch kind {
	case planexport.KindMove, planexport.KindMoveDirect, planexport.KindMoveHopExact:
		return true
	}
	return false
}

// stands are the rows a party stands still at: the move right before a pull or boss row in its leg, and step 1 (the revive
// point of the wipe recipe, spec 3.5).
type stands struct {
	at   map[int]dungeon.Point // step number -> point
	pull map[int]int           // stand step -> the step of the pull/boss row right after it in the same leg
}

// findStands reads the stands out of the steps.
func findStands(steps []step) stands {
	st := stands{at: map[int]dungeon.Point{}, pull: map[int]int{}}
	if len(steps) > 0 && steps[0].positioned {
		st.at[steps[0].num] = steps[0].p
	}
	for i, s := range steps {
		if !s.positioned || !isMove(s.kind) {
			continue
		}
		for j := i + 1; j < len(steps); j++ {
			n := steps[j]
			if n.leg != s.leg {
				break
			}
			if !n.positioned {
				continue
			}
			if n.kind == planexport.KindPull || n.kind == planexport.KindBoss {
				st.at[s.num] = s.p
				st.pull[s.num] = n.num
			}
			break
		}
	}
	return st
}

// seg is one segment with its route samples.
type seg struct {
	from    step
	to      step
	samples []dungeon.Point
}

// buildSegments makes the segments between consecutive positioned rows (across legs, as the run leader walks them) and their
// route samples. Without a navmesh (or without a server path) the route is the straight line; noPath counts the fallbacks.
func buildSegments(steps []step, mapID uint32, nav Nav, noPath *int) []seg {
	var out []seg
	var prev *step
	for i := range steps {
		s := &steps[i]
		if !s.positioned {
			continue
		}
		if prev != nil {
			line := []dungeon.Point{prev.p, s.p}
			if nav != nil {
				sp, err := nav.ServerPath(mapID, vec(prev.p), vec(s.p))
				switch {
				case err != nil, sp.Type == navmesh.PathNoPath, len(sp.Points) < 2:
					*noPath++
				default:
					line = pointsOf(sp.Points)
				}
			}
			out = append(out, seg{from: *prev, to: *s, samples: sampleLine(line, routeSampleYd)})
		}
		prev = s
	}
	return out
}

func vec(p dungeon.Point) navmesh.Vec3 { return navmesh.Vec3{p.X, p.Y, p.Z} }

func pointsOf(vs []navmesh.Vec3) []dungeon.Point {
	out := make([]dungeon.Point, len(vs))
	for i, v := range vs {
		out[i] = dungeon.Point{X: v[0], Y: v[1], Z: v[2]}
	}
	return out
}

// sampleLine walks a polyline and returns a point every spacing yd (both ends included).
func sampleLine(pts []dungeon.Point, spacing float32) []dungeon.Point {
	if len(pts) == 0 {
		return nil
	}
	out := []dungeon.Point{pts[0]}
	for i := 1; i < len(pts); i++ {
		a, b := pts[i-1], pts[i]
		d := dist3(a, b)
		n := int(math.Ceil(float64(d / spacing)))
		if n < 1 {
			n = 1
		}
		for k := 1; k <= n; k++ {
			f := float32(k) / float32(n)
			out = append(out, dungeon.Point{
				X: a.X + (b.X-a.X)*f,
				Y: a.Y + (b.Y-a.Y)*f,
				Z: a.Z + (b.Z-a.Z)*f,
			})
		}
	}
	return out
}

// dist3 is the plain 3D distance (the core measures every aggro and assist range in 3D).
func dist3(a, b dungeon.Point) float32 {
	dx, dy, dz := a.X-b.X, a.Y-b.Y, a.Z-b.Z
	return float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
}

// dist2 is the horizontal distance, for the floor filter.
func dist2(a, b dungeon.Point) float32 {
	dx, dy := a.X-b.X, a.Y-b.Y
	return float32(math.Sqrt(float64(dx*dx + dy*dy)))
}

// model is the per-plan evaluation state.
type model struct {
	opt     Options
	world   *worldspawn.World // the kept spawns (its Spawns are the report's spawn rows)
	index   map[uint64]int    // spawn guid -> index in world.Spawns
	packs   []worldspawn.Pack
	packOf  map[uint64]int
	target  []*int                     // per pack: the step that clears it, nil: unplanned
	patrols []modelPatrol              // the DB loops and the recordings
	paths   map[uint64][]dungeon.Point // spawn guid -> its patrol loop, for the waker exclusion
	assist  *assistIndex
}

// Check evaluates one plan's rows against one map's world export.
func Check(rows planexport.Rows, w *worldspawn.World, opt Options) *Report {
	if opt.AggroRate == 0 {
		opt.AggroRate = 1
	}
	if opt.AssistYd <= 0 {
		opt.AssistYd = 10
	}
	rep := &Report{
		Format: Format, Version: Version,
		Plan: rows.Plan.Plan, Map: rows.Plan.Map,
		Level: opt.Level, Spread: opt.Spread, AggroRate: opt.AggroRate, AssistYd: opt.AssistYd,
	}
	rep.Route = "straight lines (no --mmaps)"
	if opt.Nav != nil {
		rep.Route = "server paths (--mmaps)"
	}
	rep.Notes = append(rep.Notes, "no LOS: through-wall aggro is over-counted")

	steps := orderedSteps(rows.Steps)
	rep.Steps = len(steps)
	var noPath int
	segs := buildSegments(steps, w.Map, opt.Nav, &noPath)
	if noPath > 0 {
		rep.Notes = append(rep.Notes, fmt.Sprintf("%d segments: no server path, straight line used", noPath))
	}
	st := findStands(steps)

	// The spawns on another floor never wake: drop them once, with a count.
	kept, ignored := floorFilter(w.Spawns, segs, opt)
	if ignored > 0 {
		rep.Notes = append(rep.Notes, fmt.Sprintf("%d spawns on other floors ignored", ignored))
	}
	m := newModel(kept, w, opt)
	var holdsNoPoint int
	for _, s := range steps {
		if s.kind == planexport.KindHold && !s.positioned {
			holdsNoPoint++
		}
	}
	if holdsNoPoint > 0 {
		rep.Notes = append(rep.Notes, fmt.Sprintf("%d hold rows have no point: not used for targeting", holdsNoPoint))
	}
	m.loadPatrols(opt.Recorded)
	m.targets(steps)
	m.assist = m.assistIndex()
	for i := range m.packs {
		rep.Packs = append(rep.Packs, m.packRow(i))
	}
	rep.Patrols = m.patrolRows()

	for i := range segs {
		m.addSegment(rep, &segs[i], st)
	}
	return rep
}

// floorFilter keeps the spawns that stand on the route's floors: a spawn more than otherFloorYd above or below every sample
// within its 2D aggro reach is ignored (a ledge, an upper floor), and the count is the note.
func floorFilter(spawns []worldspawn.Spawn, segs []seg, opt Options) ([]worldspawn.Spawn, int) {
	kept := make([]worldspawn.Spawn, 0, len(spawns))
	ignored := 0
	for _, s := range spawns {
		reach := worldspawn.AggroRadius(s, opt.Level, opt.AggroRate) + opt.Spread + s.Wander
		reachable, sameFloor := false, false
		for _, g := range segs {
			for _, sample := range g.samples {
				if dist2(s.Pos, sample) > reach {
					continue
				}
				reachable = true
				if dz := s.Pos.Z - sample.Z; dz <= otherFloorYd && dz >= -otherFloorYd {
					sameFloor = true
				}
			}
			if sameFloor {
				break
			}
		}
		if reachable && !sameFloor {
			ignored++
			continue
		}
		kept = append(kept, s)
	}
	return kept, ignored
}

// newModel builds the pack model over the kept spawns.
func newModel(kept []worldspawn.Spawn, w *worldspawn.World, opt Options) *model {
	fw := &worldspawn.World{Map: w.Map, Spawns: kept}
	in := map[uint64]bool{}
	for _, s := range kept {
		in[s.GUID] = true
	}
	for _, l := range w.Links {
		if in[l.Slave] && in[l.Master] {
			fw.Links = append(fw.Links, l)
		}
	}
	for _, p := range w.Patrols {
		if len(p.SpawnGUIDs) > 0 && in[p.SpawnGUIDs[0]] {
			fw.Patrols = append(fw.Patrols, p)
		}
	}
	m := &model{
		opt:    opt,
		world:  fw,
		index:  map[uint64]int{},
		packOf: map[uint64]int{},
		paths:  map[uint64][]dungeon.Point{},
	}
	hostile := make([]worldspawn.Spawn, 0, len(kept))
	for i, s := range kept {
		m.index[s.GUID] = i
		if s.Hostile() {
			hostile = append(hostile, s)
		}
	}
	m.packs = (&worldspawn.World{Map: w.Map, Spawns: hostile, Links: fw.Links}).Packs(worldspawn.PackOptions{AssistYd: opt.AssistYd})
	m.target = make([]*int, len(m.packs))
	for i, p := range m.packs {
		for _, g := range p.Members {
			m.packOf[g] = i
		}
	}
	return m
}

// packRow renders one pack for the report.
func (m *model) packRow(i int) PackRow {
	p := m.packs[i]
	c := p.Centroid
	return PackRow{
		ID:         p.ID,
		Members:    append([]uint64(nil), p.Members...),
		Centroid:   Vec{X: round2(c.X), Y: round2(c.Y), Z: round2(c.Z)},
		TargetedAt: m.target[i],
		Unplanned:  m.target[i] == nil,
		Pooled:     p.Pooled,
		Patrol:     p.Patrol,
	}
}

// targets assigns every pack the first pull/boss/hold row that clears it: the row's entries hold one of its member entries and
// its point stands within the row's reach of the member's spawn or of any point of the member's patrol loop.
func (m *model) targets(steps []step) {
	for _, s := range steps {
		if !s.positioned || (s.kind != planexport.KindPull && s.kind != planexport.KindBoss && s.kind != planexport.KindHold) {
			continue
		}
		es := targetEntries(s)
		if len(es) == 0 {
			continue
		}
		reach := targetReach(s)
		for i := range m.packs {
			if m.target[i] != nil {
				continue
			}
			if !m.packClearedBy(i, es, s.p, reach) {
				continue
			}
			step := s.num
			m.target[i] = &step
		}
	}
}

// packClearedBy reports whether the row clears the pack.
func (m *model) packClearedBy(pack int, entries []uint32, point dungeon.Point, reach float32) bool {
	want := map[uint32]bool{}
	for _, e := range entries {
		want[e] = true
	}
	for _, g := range m.packs[pack].Members {
		i, ok := m.index[g]
		if !ok {
			continue
		}
		s := m.world.Spawns[i]
		if !want[s.Entry] {
			continue
		}
		if dist3(s.Pos, point) <= reach {
			return true
		}
		for _, p := range m.paths[g] {
			if dist3(p, point) <= reach {
				return true
			}
		}
	}
	return false
}

// patrolRows renders every simulated patrol.
func (m *model) patrolRows() []PatrolRow {
	if len(m.patrols) == 0 {
		return nil
	}
	out := make([]PatrolRow, len(m.patrols))
	for i, p := range m.patrols {
		var length float32
		for k := 1; k <= len(p.path); k++ {
			length += dist3(p.path[k-1], p.path[k%len(p.path)])
		}
		path := make([]Vec, len(p.path))
		for k, pt := range p.path {
			path[k] = Vec{X: pt.X, Y: pt.Y, Z: pt.Z}
		}
		out[i] = PatrolRow{
			ID:       p.id,
			GUIDs:    p.guids,
			Entries:  p.entries,
			Name:     p.name,
			PeriodS:  round2(p.period),
			Estimate: p.estimate,
			LengthYd: round2(length),
			Points:   len(p.path),
			Path:     path,
			Source:   p.source,
		}
		if p.pack >= 0 {
			out[i].TargetedAt = m.target[p.pack]
		}
	}
	return out
}

// addSegment evaluates one segment: its woken packs, its patrol windows and its verdict.
func (m *model) addSegment(rep *Report, s *seg, st stands) {
	out := Segment{From: s.from.row(), To: s.to.row()}
	n := s.to.num

	// The family assist hop: one level from the proximity wakers, over the static spawns of the map.
	wakers := map[int][]Waker{}
	assisted := map[int][]uint64{}
	for i := range m.packs {
		if !m.alive(i, n) {
			continue
		}
		for _, g := range m.packs[i].Members {
			if _, patrolling := m.paths[g]; patrolling {
				continue // a patrol is reported through its windows, not as a static hit
			}
			si, ok := m.index[g]
			if !ok {
				continue
			}
			sp := m.world.Spawns[si]
			reach := m.reach(sp)
			min, ok := minDist(s.samples, sp.Pos)
			if !ok || min > reach {
				continue
			}
			wakers[i] = append(wakers[i], Waker{
				GUID:    sp.GUID,
				Entry:   sp.Entry,
				Name:    sp.Name,
				Level:   sp.LevelMax,
				AggroYd: round2(reach),
				MinYd:   round2(min),
			})
			for _, other := range m.assist.near(sp, m.opt.AssistYd) {
				if other.Faction != sp.Faction {
					continue
				}
				if other.FlagsExtra&worldspawn.NoAssist != 0 || other.FlagsExtra&worldspawn.NoAggro != 0 {
					continue
				}
				j, ok := m.packOf[other.GUID]
				if !ok || !m.alive(j, n) {
					continue
				}
				if _, patrolling := m.paths[other.GUID]; patrolling {
					continue
				}
				assisted[j] = appendUnique(assisted[j], other.GUID)
			}
		}
	}
	involved := map[int]bool{}
	for i := range wakers {
		involved[i] = true
	}
	for i := range assisted {
		involved[i] = true
	}
	ids := make([]int, 0, len(involved))
	for i := range involved {
		ids = append(ids, i)
	}
	sort.Slice(ids, func(a, b int) bool { return m.packs[ids[a]].ID < m.packs[ids[b]].ID })
	for _, i := range ids {
		p := m.packs[i]
		class := ClassDanger
		if m.own(i, s.to, st) {
			class = ClassOwn
		}
		linked := make([]uint64, 0, len(ids)-1)
		for _, j := range ids {
			if j != i {
				linked = append(linked, m.packs[j].ID)
			}
		}
		sort.Slice(linked, func(a, b int) bool { return linked[a] < linked[b] })
		wk := wakers[i]
		sort.Slice(wk, func(a, b int) bool {
			if wk[a].MinYd != wk[b].MinYd {
				return wk[a].MinYd < wk[b].MinYd
			}
			return wk[a].GUID < wk[b].GUID
		})
		as := assisted[i]
		sort.Slice(as, func(a, b int) bool { return as[a] < as[b] })
		out.Packs = append(out.Packs, SegmentPack{
			Pack:        p.ID,
			Class:       class,
			TargetedAt:  m.target[i],
			Unplanned:   m.target[i] == nil,
			Wakers:      wk,
			Assisted:    as,
			Brings:      len(p.Members),
			LinkedPacks: linked,
		})
	}

	// The patrols: every loop alive at this step, with the windows its leader is within reach of the route or a stand.
	out.Patrols = m.patrolHits(s, st)

	out.Verdict = VerdictClear
	danger, own, patrolFinding := false, false, false
	for _, p := range out.Packs {
		if p.Class == ClassDanger {
			danger = true
		} else {
			own = true
		}
	}
	for i := range out.Patrols {
		h := &out.Patrols[i]
		if h.pack >= 0 && m.target[h.pack] != nil && m.own(h.pack, s.to, st) {
			h.own = true
			own = true
			continue
		}
		patrolFinding = true
	}
	switch {
	case danger:
		out.Verdict = VerdictDanger
		rep.Counts.Danger++
	case patrolFinding:
		out.Verdict = VerdictPatrol
		rep.Counts.Patrol++
	case own:
		out.Verdict = VerdictOwn
		rep.Counts.Own++
	default:
		rep.Counts.Clear++
	}
	rep.Segments = append(rep.Segments, out)
}

// reach is a spawn's proximity wake reach: the aggro radius plus the party's spread plus its wander.
func (m *model) reach(s worldspawn.Spawn) float32 {
	return worldspawn.AggroRadius(s, m.opt.Level, m.opt.AggroRate) + m.opt.Spread + s.Wander
}

// alive says a pack is still there when the segment ending at step n is walked: it is cleared at n (the intended pull) or later,
// or never.
func (m *model) alive(pack, n int) bool {
	t := m.target[pack]
	return t == nil || *t >= n
}

// own says this segment is the pack's intended clear: it ends at the pack's targeting row, or at the stand right before it.
func (m *model) own(pack int, to step, st stands) bool {
	t := m.target[pack]
	if t == nil {
		return false
	}
	return to.num == *t || st.pull[to.num] == *t
}

// assistIndex is a uniform grid over the static hostile spawns, for the family assist hop.
type assistIndex struct {
	cell float32
	grid map[[2]int32][]worldspawn.Spawn
}

func (m *model) assistIndex() *assistIndex {
	cell := m.opt.AssistYd
	if cell < 1 {
		cell = 1
	}
	a := &assistIndex{cell: cell, grid: map[[2]int32][]worldspawn.Spawn{}}
	for _, s := range m.world.Spawns {
		if !s.Hostile() {
			continue
		}
		if _, patrolling := m.paths[s.GUID]; patrolling {
			continue
		}
		k := a.key(s.Pos)
		a.grid[k] = append(a.grid[k], s)
	}
	return a
}

func (a *assistIndex) key(p dungeon.Point) [2]int32 {
	return [2]int32{int32(math.Floor(float64(p.X / a.cell))), int32(math.Floor(float64(p.Y / a.cell)))}
}

// near returns the spawns within yd (3D) of from, from its own and the neighbouring cells.
func (a *assistIndex) near(from worldspawn.Spawn, yd float32) []worldspawn.Spawn {
	k := a.key(from.Pos)
	span := int32(math.Ceil(float64(yd / a.cell)))
	var out []worldspawn.Spawn
	for dx := -span; dx <= span; dx++ {
		for dy := -span; dy <= span; dy++ {
			for _, s := range a.grid[[2]int32{k[0] + dx, k[1] + dy}] {
				if s.GUID == from.GUID {
					continue
				}
				if dist3(s.Pos, from.Pos) <= yd {
					out = append(out, s)
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GUID < out[j].GUID })
	return out
}

// minDist is the 3D distance from p to the nearest of the samples.
func minDist(samples []dungeon.Point, p dungeon.Point) (float32, bool) {
	var best float32
	found := false
	for _, s := range samples {
		if d := dist3(s, p); !found || d < best {
			best, found = d, true
		}
	}
	return best, found
}

// appendUnique adds guid to guids when it is not there.
func appendUnique(guids []uint64, guid uint64) []uint64 {
	for _, g := range guids {
		if g == guid {
			return guids
		}
	}
	return append(guids, guid)
}

// round2 rounds to 2 decimals for the report.
func round2(v float32) float32 { return float32(math.Round(float64(v)*100) / 100) }
