package patrols

import (
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// Sample is one sighting of one unit at one moment.
type Sample struct {
	T       time.Time
	GUID    packet.GUID
	Entry   uint32
	Name    string
	X, Y, Z float32
	// Moving is the unit's own movement flag (a creature is not moving while it stands at a waypoint).
	Moving bool
}

// Options carries the analysis' knobs. None are defined yet: every threshold is a constant of this package (the values the spec
// names), and a caller passes the zero value.
type Options struct{}

const (
	staticExtentYd     = 3  // extent under this is a static unit
	wandererExtentYd   = 15 // extent under this without a loop is a wanderer
	loopMatchYd        = 3  // a loop returns within this of the start point
	loopMinLag         = 10 * time.Second
	loopConfirms       = 5 // samples after the return must match too
	simplifyEpsilonYd  = 1.5
	waitRadiusYd       = 1
	minWaitMS          = 2000
	formationYd        = 8
	formationShare     = 0.9
	formationMinCommon = 2
)

// Analyze sorts a recording's sightings into patrols (loops with a period), static units and wanderers. The zero Sample (no time)
// stands for a sighting without a position and is dropped.
func Analyze(samples []Sample, _ Options) ([]Patrol, []Static, []Wanderer, []string) {
	var static []Static
	var wanderers []Wanderer
	var notes []string
	var movers []mover

	for _, tr := range group(samples) {
		mx, my, mz := medianPoint(tr.samples)
		ext := extent(tr.samples, mx, my, mz)
		if ext < staticExtentYd {
			static = append(static, Static{GUID: tr.guid.String(), Entry: tr.samples[0].Entry, X: mx, Y: my, Z: mz})
			continue
		}
		first, last := tr.samples[0], tr.samples[len(tr.samples)-1]
		span := last.T.Sub(first.T).Seconds()
		lags, complete := loopLags(tr.samples)
		if !complete && ext < wandererExtentYd {
			wanderers = append(wanderers, Wanderer{
				GUID: tr.guid.String(), Entry: tr.samples[0].Entry,
				Center: dungeon.Point{X: mx, Y: my, Z: mz}, Radius: float32(ext),
			})
			continue
		}
		period := span
		if complete {
			period = medianOf(lags)
		}
		if period <= 0 {
			period = 1 // a file must hold a positive round (Read refuses 0)
		}
		p := buildPatrol(tr, period, complete)
		if !complete {
			notes = append(notes, fmt.Sprintf("%s (%s): seen leaving the area or a loop longer than the recording", tr.label(), tr.guid))
		}
		movers = append(movers, mover{track: tr, patrol: p})
	}
	return mergeFormations(movers), static, wanderers, notes
}

// track is every sighting of one guid, in time order.
type track struct {
	guid    packet.GUID
	samples []Sample
}

func (t track) label() string {
	if t.samples[0].Name != "" {
		return t.samples[0].Name
	}
	return fmt.Sprint(t.samples[0].Entry)
}

// group collects the positioned samples by guid, sorted by guid and by time.
func group(samples []Sample) []track {
	byGUID := make(map[packet.GUID][]Sample)
	for _, s := range samples {
		if s.T.IsZero() {
			continue
		}
		byGUID[s.GUID] = append(byGUID[s.GUID], s)
	}
	guids := make([]packet.GUID, 0, len(byGUID))
	for g := range byGUID {
		guids = append(guids, g)
	}
	slices.Sort(guids)
	out := make([]track, 0, len(guids))
	for _, g := range guids {
		tr := byGUID[g]
		slices.SortStableFunc(tr, func(a, b Sample) int { return a.T.Compare(b.T) })
		out = append(out, track{guid: g, samples: tr})
	}
	return out
}

// medianPoint is the per-axis median of the sightings.
func medianPoint(ss []Sample) (float32, float32, float32) {
	xs := make([]float32, len(ss))
	ys := make([]float32, len(ss))
	zs := make([]float32, len(ss))
	for i, s := range ss {
		xs[i], ys[i], zs[i] = s.X, s.Y, s.Z
	}
	return median(xs), median(ys), median(zs)
}

func median(v []float32) float32 {
	slices.Sort(v)
	n := len(v)
	if n%2 == 1 {
		return v[n/2]
	}
	return (v[n/2-1] + v[n/2]) / 2
}

// extent is the largest distance of a sighting from the point.
func extent(ss []Sample, x, y, z float32) float64 {
	max := 0.0
	for _, s := range ss {
		if d := dist3(s.X, s.Y, s.Z, x, y, z); d > max {
			max = d
		}
	}
	return max
}

// loopLags returns the first lag at which the unit is back where it was (and stays there for the next samples), from every start.
func loopLags(ss []Sample) ([]float64, bool) {
	lags := make([]float64, 0, len(ss))
	for i := range ss {
		if lag, ok := firstLoopLag(ss, i); ok {
			lags = append(lags, lag)
		}
	}
	return lags, len(lags) > 0
}

func firstLoopLag(ss []Sample, i int) (float64, bool) {
	for j := i + 1; j < len(ss); j++ {
		if ss[j].T.Sub(ss[i].T) < loopMinLag || dist3Sq(ss[j], ss[i]) > loopMatchYd*loopMatchYd {
			continue
		}
		if !confirmsLoop(ss, i, j) {
			continue
		}
		return ss[j].T.Sub(ss[i].T).Seconds(), true
	}
	return 0, false
}

// confirmsLoop checks that the five samples after j are where the five after i were: a path that merely crosses its start is not
// a loop.
func confirmsLoop(ss []Sample, i, j int) bool {
	for k := 1; k <= loopConfirms; k++ {
		if j+k >= len(ss) || dist3Sq(ss[j+k], ss[i+k]) > loopMatchYd*loopMatchYd {
			return false
		}
	}
	return true
}

func medianOf(v []float64) float64 {
	slices.Sort(v)
	n := len(v)
	if n%2 == 1 {
		return v[n/2]
	}
	return (v[n/2-1] + v[n/2]) / 2
}

// buildPatrol makes a patrol from the lead member's recording and one measured round.
func buildPatrol(tr track, period float64, complete bool) Patrol {
	span := tr.samples[len(tr.samples)-1].T.Sub(tr.samples[0].T).Seconds()
	cycles := int(math.Floor(span/period + 1e-9))
	inRound := tr.samples
	if span > period {
		inRound = tr.samples[:roundEnd(tr.samples, period)]
	}
	path := simplify(inRound, simplifyEpsilonYd)
	p := Patrol{
		ID:             tr.guid.String(),
		GUIDs:          []string{tr.guid.String()},
		Entries:        []uint32{tr.samples[0].Entry},
		Name:           tr.label(),
		Path:           path,
		WaitsMS:        waits(inRound, path),
		PeriodS:        float32(period),
		PeriodEstimate: cycles < 2,
		CyclesSeen:     cycles,
		Complete:       complete,
		Samples:        len(tr.samples),
	}
	if len(path) > 0 {
		p.PhaseEpochMS = phaseMS(tr.samples, path[0])
	}
	return p
}

// roundEnd is the index one past the last sample of the first round (t - first <= period).
func roundEnd(ss []Sample, period float64) int {
	round := time.Duration(period * float64(time.Second))
	for i, s := range ss {
		if s.T.Sub(ss[0].T) > round {
			return i
		}
	}
	return len(ss)
}

// simplify is Douglas-Peucker on the 2D path, keeping z from the samples it keeps.
func simplify(ss []Sample, eps float64) []dungeon.Point {
	pts := make([]dungeon.Point, len(ss))
	for i, s := range ss {
		pts[i] = dungeon.Point{X: s.X, Y: s.Y, Z: s.Z}
	}
	if len(pts) < 3 {
		return pts
	}
	keep := make([]bool, len(pts))
	keep[0], keep[len(pts)-1] = true, true
	type span struct{ a, b int }
	stack := []span{{0, len(pts) - 1}}
	for len(stack) > 0 {
		s := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		far, at := -1.0, -1
		for i := s.a + 1; i < s.b; i++ {
			if d := segDist2D(pts[i], pts[s.a], pts[s.b]); d > far {
				far, at = d, i
			}
		}
		if at >= 0 && far > eps {
			keep[at] = true
			stack = append(stack, span{s.a, at}, span{at, s.b})
		}
	}
	out := make([]dungeon.Point, 0, len(pts))
	for i, p := range pts {
		if keep[i] {
			out = append(out, p)
		}
	}
	return out
}

// segDist2D is the 2D distance of p from the segment ab.
func segDist2D(p, a, b dungeon.Point) float64 {
	ax, ay := float64(a.X), float64(a.Y)
	dx, dy := float64(b.X)-ax, float64(b.Y)-ay
	px, py := float64(p.X)-ax, float64(p.Y)-ay
	den := dx*dx + dy*dy
	if den == 0 {
		return math.Hypot(px, py)
	}
	t := (px*dx + py*dy) / den
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(px-t*dx, py-t*dy)
}

// waits is the time spent within a yard of each path point while not moving; under 2 s counts as 0, else to 100 ms.
func waits(ss []Sample, path []dungeon.Point) []int {
	out := make([]int, len(path))
	if len(path) == 0 {
		return out
	}
	for i := 0; i+1 < len(ss); i++ {
		a, b := ss[i], ss[i+1]
		if a.Moving || b.Moving {
			continue
		}
		dt := b.T.Sub(a.T)
		if dt <= 0 {
			continue
		}
		nearest, at := math.MaxFloat64, -1
		for k, p := range path {
			if d := dist3SqPoint(a, p); d < nearest {
				nearest, at = d, k
			}
		}
		if at >= 0 && nearest <= waitRadiusYd*waitRadiusYd {
			out[at] += int(dt / time.Millisecond)
		}
	}
	for i, ms := range out {
		if ms < minWaitMS {
			out[i] = 0
			continue
		}
		out[i] = int(math.Round(float64(ms)/100)) * 100
	}
	return out
}

// phaseMS is the wall time of the latest sighting at point p.
func phaseMS(ss []Sample, p dungeon.Point) int64 {
	var at time.Time
	for _, s := range ss {
		if dist3SqPoint(s, p) > waitRadiusYd*waitRadiusYd {
			continue
		}
		if s.T.After(at) {
			at = s.T
		}
	}
	return at.UnixMilli()
}

// mover is an analysed unit, kept until the formations are merged.
type mover struct {
	track
	patrol Patrol
}

// mergeFormations joins the movers that stay close to each other (a formation: a tank and its followers) into one patrol, led by
// the smallest guid.
func mergeFormations(movers []mover) []Patrol {
	if len(movers) == 0 {
		return nil
	}
	parent := make([]int, len(movers))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	for i := range movers {
		for j := i + 1; j < len(movers); j++ {
			if find(i) == find(j) || !sameFormation(movers[i].samples, movers[j].samples) {
				continue
			}
			parent[find(j)] = find(i)
		}
	}
	seen := make(map[int]bool, len(movers))
	out := make([]Patrol, 0, len(movers))
	for i := range movers {
		root := find(i)
		if seen[root] {
			continue
		}
		seen[root] = true
		p := movers[i].patrol
		for j := i + 1; j < len(movers); j++ {
			if find(j) != root {
				continue
			}
			p.GUIDs = append(p.GUIDs, movers[j].guid.String())
			p.Entries = appendEntry(p.Entries, movers[j].samples[0].Entry)
		}
		out = append(out, p)
	}
	return out
}

func appendEntry(entries []uint32, entry uint32) []uint32 {
	if slices.Contains(entries, entry) {
		return entries
	}
	return append(entries, entry)
}

// sameFormation reports whether two movers are within 8 yd of each other at 90 % of the moments both were seen.
func sameFormation(a, b []Sample) bool {
	index := make(map[int64]int, len(b))
	for i, s := range b {
		index[s.T.UnixNano()] = i
	}
	common, near := 0, 0
	for _, s := range a {
		j, ok := index[s.T.UnixNano()]
		if !ok {
			continue
		}
		common++
		if dist3Sq(s, b[j]) <= formationYd*formationYd {
			near++
		}
	}
	return common >= formationMinCommon && float64(near) >= formationShare*float64(common)
}

func dist3(x1, y1, z1, x2, y2, z2 float32) float64 {
	dx, dy, dz := float64(x1-x2), float64(y1-y2), float64(z1-z2)
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

func dist3Sq(a, b Sample) float64 {
	dx, dy, dz := float64(a.X-b.X), float64(a.Y-b.Y), float64(a.Z-b.Z)
	return dx*dx + dy*dy + dz*dz
}

func dist3SqPoint(a Sample, p dungeon.Point) float64 {
	dx, dy, dz := float64(a.X-p.X), float64(a.Y-p.Y), float64(a.Z-p.Z)
	return dx*dx + dy*dy + dz*dz
}
