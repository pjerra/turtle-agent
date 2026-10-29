package trashcheck

import (
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/patrols"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

// modelPatrol is one waypoint loop the model simulates: a DB loop (worldspawn) or a recording (patrols.File).
type modelPatrol struct {
	id         string
	guids      []string
	spawnGUIDs []uint64
	entries    []uint32
	name       string
	path       []dungeon.Point
	waits      []int
	period     float32
	estimate   bool
	phaseMS    int64
	source     string
	leader     *worldspawn.Spawn // the export row the aggro radius comes from; nil: the defaults
	pack       int               // index in the model's packs, -1: no pack
}

// loadPatrols builds the patrol list: the world export's loops, then the recordings, each replacing the DB loop it matches
// (spec 5.3: a recorded patrol's id or any of its guids names the DB patrol) and any other added.
func (m *model) loadPatrols(recorded []patrols.Patrol) {
	for _, p := range m.world.Patrols {
		if len(p.Path) < 2 {
			continue
		}
		mp := modelPatrol{
			id:         p.ID,
			guids:      append([]string(nil), p.GUIDs...),
			spawnGUIDs: append([]uint64(nil), p.SpawnGUIDs...),
			entries:    append([]uint32(nil), p.Entries...),
			name:       p.Name,
			path:       p.Path,
			waits:      p.WaitsMS,
			period:     p.PeriodS,
			estimate:   p.PeriodEstimate,
			source:     p.Source,
			pack:       -1,
		}
		if len(mp.spawnGUIDs) > 0 {
			mp.leader = m.spawnByGUID(mp.spawnGUIDs[0])
			if i, ok := m.packOf[mp.spawnGUIDs[0]]; ok {
				mp.pack = i
			}
		}
		m.patrols = append(m.patrols, mp)
	}
	dbCount := len(m.patrols)
	for _, r := range recorded {
		if len(r.Path) < 2 || r.PeriodS <= 0 {
			continue
		}
		mp := m.fromRecorded(r)
		at := m.matchPatrol(r, dbCount)
		if at < 0 {
			m.patrols = append(m.patrols, mp)
			continue
		}
		old := m.patrols[at]
		if mp.id == "" {
			mp.id = old.id
		}
		if mp.name == "" {
			mp.name = old.name
		}
		if len(mp.entries) == 0 {
			mp.entries = old.entries
		}
		if mp.leader == nil {
			mp.leader = old.leader
		}
		if mp.pack < 0 {
			mp.pack = old.pack
		}
		m.patrols[at] = mp
	}
	// Every member of a loop walks it: a waker check takes them at the loop, not at the spawn point.
	for _, p := range m.patrols {
		for _, g := range p.spawnGUIDs {
			m.paths[g] = p.path
		}
	}
}

// fromRecorded turns a recording into the model's patrol.
func (m *model) fromRecorded(r patrols.Patrol) modelPatrol {
	mp := modelPatrol{
		id:       r.ID,
		guids:    append([]string(nil), r.GUIDs...),
		entries:  append([]uint32(nil), r.Entries...),
		name:     r.Name,
		path:     r.Path,
		waits:    r.WaitsMS,
		period:   r.PeriodS,
		estimate: r.PeriodEstimate,
		phaseMS:  r.PhaseEpochMS,
		source:   r.Source,
		pack:     -1,
	}
	for _, g := range r.SpawnGUIDs {
		mp.spawnGUIDs = append(mp.spawnGUIDs, uint64(g))
	}
	for _, g := range mp.spawnGUIDs {
		if s := m.spawnByGUID(g); s != nil {
			mp.leader = s
			break
		}
	}
	if len(mp.spawnGUIDs) > 0 {
		if i, ok := m.packOf[mp.spawnGUIDs[0]]; ok {
			mp.pack = i
		}
	}
	return mp
}

// matchPatrol finds the DB patrol a recording replaces, by its id or any of its guids (hex world guid or spawn guid).
func (m *model) matchPatrol(r patrols.Patrol, dbCount int) int {
	keys := map[string]bool{}
	add := func(s string) {
		if s != "" {
			keys[strings.ToLower(s)] = true
		}
	}
	add(r.ID)
	for _, g := range r.GUIDs {
		add(g)
	}
	for _, g := range r.SpawnGUIDs {
		add(strconv.FormatUint(uint64(g), 10))
	}
	for i := range min(dbCount, len(m.patrols)) {
		p := m.patrols[i]
		if keys[strings.ToLower(p.id)] {
			return i
		}
		for _, g := range p.guids {
			if keys[strings.ToLower(g)] {
				return i
			}
		}
		for _, g := range p.spawnGUIDs {
			if keys[strconv.FormatUint(g, 10)] {
				return i
			}
		}
	}
	return -1
}

// spawnByGUID is the kept export row of a spawn, or nil.
func (m *model) spawnByGUID(guid uint64) *worldspawn.Spawn {
	i, ok := m.index[guid]
	if !ok {
		return nil
	}
	s := m.world.Spawns[i]
	return &s
}

// patrolAlive says the loop's pack is still there when the segment ending at step n is walked.
func (m *model) patrolAlive(i, n int) bool {
	if m.patrols[i].pack < 0 {
		return true
	}
	return m.alive(m.patrols[i].pack, n)
}

// patrolReach is the leader's wake reach: its aggro radius plus the party's spread plus its wander; 20 yd when the export has
// no row for it (a recording of a spawn outside the export).
func (m *model) patrolReach(p modelPatrol) float32 {
	lead := worldspawn.Spawn{DetectionYd: 20, LevelMax: m.opt.Level}
	if p.leader != nil {
		lead = *p.leader
	}
	return m.reach(lead)
}

// patrolHits simulates every patrol alive at the segment and reports the windows its leader is within reach of the segment's
// route samples, or within reach plus 5 yd of one of its stands.
func (m *model) patrolHits(s *seg, st stands) []PatrolHit {
	if len(m.patrols) == 0 || len(s.samples) == 0 {
		return nil
	}
	var standPoints []standHit
	for _, num := range []int{s.from.num, s.to.num} {
		if p, ok := st.at[num]; ok {
			at := "stand"
			if num == 1 {
				at = "start"
			}
			standPoints = append(standPoints, standHit{p: p, at: at})
		}
	}
	var out []PatrolHit
	n := s.to.num
	for i := range m.patrols {
		p := &m.patrols[i]
		if !m.patrolAlive(i, n) {
			continue
		}
		reach := m.patrolReach(*p)
		if p.leader != nil && p.leader.FlagsExtra&worldspawn.NoAggro != 0 {
			continue
		}
		if reach <= 0 {
			continue
		}
		// A loop that cannot come near this segment (bounding boxes) has no window: skip the simulation.
		if !boxNear(p.path, s.samples, reach+standReachYd) {
			continue
		}
		l := newLoop(*p)
		windows, at := l.windows(s.samples, standPoints, reach, reach+standReachYd)
		if len(windows) == 0 {
			continue
		}
		var sum float32
		for _, w := range windows {
			sum += w.ToS - w.FromS
		}
		share := float32(0)
		if p.period > 0 {
			share = sum / p.period
		}
		out = append(out, PatrolHit{
			ID:       p.id,
			GUIDs:    p.guids,
			Entries:  p.entries,
			Name:     p.name,
			PeriodS:  round2(p.period),
			Estimate: p.estimate,
			Windows:  windows,
			Share:    round2(share),
			At:       at,
			EpochMS:  p.phaseMS,
			pack:     p.pack,
		})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out
}

// loop is a patrol loop's timeline: the waits at the points and the travel between them, scaled so one round takes the
// patrol's period.
type loop struct {
	path   []dungeon.Point
	arrive []float32 // seconds at which the leader reaches point i
	depart []float32 // seconds at which it leaves point i (after its wait)
	period float32
}

// newLoop builds the timeline. The period wins over the loop's length and its waits: a recording's period is measured, and a
// DB loop's is an estimate, and either way the simulation must walk exactly one round per period.
func newLoop(p modelPatrol) loop {
	n := len(p.path)
	l := loop{path: p.path, arrive: make([]float32, n), depart: make([]float32, n), period: p.period}
	if n == 0 || l.period <= 0 {
		return l
	}
	var travel float32
	for i := range n {
		travel += dist3(p.path[i], p.path[(i+1)%n])
	}
	var waits float32
	for _, w := range p.waits {
		waits += float32(w) / 1000
	}
	run := l.period - waits
	if run < 0.01 {
		run = 0.01
	}
	speed := float32(0)
	if travel > 0 {
		speed = travel / run
	}
	l.arrive[0] = 0
	l.depart[0] = l.wait(p, 0)
	for i := range n - 1 {
		stepT := float32(0)
		if speed > 0 {
			stepT = dist3(p.path[i], p.path[i+1]) / speed
		}
		l.arrive[i+1] = l.depart[i] + stepT
		l.depart[i+1] = l.arrive[i+1] + l.wait(p, i+1)
	}
	return l
}

// wait is the wait at point i, seconds.
func (l loop) wait(p modelPatrol, i int) float32 {
	if i < 0 || i >= len(p.waits) {
		return 0
	}
	return float32(p.waits[i]) / 1000
}

// at is the leader's position t seconds into the round.
func (l loop) at(t float32) dungeon.Point {
	n := len(l.path)
	if n == 0 {
		return dungeon.Point{}
	}
	if l.period > 0 {
		t = float32(math.Mod(float64(t), float64(l.period)))
		if t < 0 {
			t += l.period
		}
	}
	for i := range n {
		if t >= l.arrive[i] && t <= l.depart[i] {
			return l.path[i]
		}
		j := (i + 1) % n
		a, b := l.depart[i], l.arrive[j]
		if j == 0 {
			b += l.period
		}
		if t > a && t < b {
			f := (t - a) / (b - a)
			return dungeon.Point{
				X: l.path[i].X + (l.path[j].X-l.path[i].X)*f,
				Y: l.path[i].Y + (l.path[j].Y-l.path[i].Y)*f,
				Z: l.path[i].Z + (l.path[j].Z-l.path[i].Z)*f,
			}
		}
	}
	return l.path[0]
}

// standHit is one stand of a segment and what the report calls it.
type standHit struct {
	p  dungeon.Point
	at string
}

// windows walks one round in patrolStepS steps and returns the merged windows in which the leader is within reach of the
// samples (or within reach+bonus of a stand), and the strongest place that made them (start > stand > route).
func (l loop) windows(samples []dungeon.Point, stands []standHit, reach, standReach float32) ([]Window, string) {
	if l.period <= 0 || len(l.path) == 0 {
		return nil, ""
	}
	const route, stand, start = 1, 2, 3
	var out []Window
	open := false
	var from float32
	best := 0
	for t := float32(0); t < l.period; t += patrolStepS {
		pos := l.at(t)
		rank := 0
		if min, ok := minDist(samples, pos); ok && min <= reach {
			rank = route
		}
		for _, s := range stands {
			if dist3(s.p, pos) <= standReach {
				if s.at == "start" {
					rank = start
				} else if rank < stand {
					rank = stand
				}
			}
		}
		if rank > 0 {
			if rank > best {
				best = rank
			}
			if !open {
				open, from = true, t
			}
			continue
		}
		if open {
			out = append(out, Window{FromS: round2(from), ToS: round2(t)})
			open = false
		}
	}
	if open {
		out = append(out, Window{FromS: round2(from), ToS: round2(l.period)})
	}
	if len(out) > 1 && out[0].FromS == 0 && out[len(out)-1].ToS == round2(l.period) {
		last := len(out) - 1
		out[last].ToS = round2(l.period + out[0].ToS)
		out = out[1:]
	}
	at := ""
	switch best {
	case start:
		at = "start"
	case stand:
		at = "stand"
	case route:
		at = "route"
	}
	return out, at
}

// boxNear says a loop's bounding box (grown by yd) may meet the samples' bounding box.
func boxNear(path []dungeon.Point, samples []dungeon.Point, yd float32) bool {
	if len(path) == 0 || len(samples) == 0 {
		return false
	}
	lo, hi := bbox(path)
	slo, shi := bbox(samples)
	for k := 0; k < 3; k++ {
		if lo[k]-yd > shi[k] || hi[k]+yd < slo[k] {
			return false
		}
	}
	return true
}

// bbox is a point list's low and high corner.
func bbox(ps []dungeon.Point) ([3]float32, [3]float32) {
	lo := [3]float32{ps[0].X, ps[0].Y, ps[0].Z}
	hi := lo
	for _, p := range ps[1:] {
		for k, v := range [3]float32{p.X, p.Y, p.Z} {
			if v < lo[k] {
				lo[k] = v
			}
			if v > hi[k] {
				hi[k] = v
			}
		}
	}
	return lo, hi
}
