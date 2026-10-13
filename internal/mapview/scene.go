package mapview

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

// LoadBackground reads the navmesh polygons under a box. It never fails the picture: when there is no planner, no
// engine or no tile, the polygons are nil and note says why.
func LoadBackground(p *navmesh.Planner, mapID uint32, box Box) (polys []navmesh.Polygon, truncated bool, note string) {
	if p == nil {
		return nil, false, `no navmesh background: "mmaps_dir" is not set`
	}
	polys, truncated, err := p.Polygons(mapID, box.MinX, box.MinY, box.MaxX, box.MaxY)
	switch {
	case errors.Is(err, navmesh.ErrNoEngine):
		return nil, false, "no navmesh background: this turtle-agent was built without cgo"
	case err != nil:
		return nil, false, "no navmesh background: " + err.Error()
	case len(polys) == 0:
		return nil, false, fmt.Sprintf("no navmesh background: no tiles of map %d under the box", mapID)
	}
	return polys, truncated, ""
}

// SceneOptions say what the image shows and how.
type SceneOptions struct {
	Box *Box // nil: the box round everything on the map
	Pad float32
	// Cuts divide the map into floors by height (ascending); with Auto the cuts come from the navmesh polygons
	// instead (SplitHeights) and Cuts are ignored.
	Cuts      []float32
	Auto      bool
	FloorOpts FloorOptions
	// Polys is the navmesh background, nil for none; PolysTruncated says the mesh holds more than was loaded.
	Polys          []navmesh.Polygon
	PolysTruncated bool
	// BackgroundNote says why there is no background (no mmaps, no tiles), for the image's notes.
	BackgroundNote string
	Overlay        *Overlay
	// MaxSide is the longest side of the map in pixels; 0: 960.
	MaxSide int
	Config  Config
}

// DefaultPad is the margin in yards round the run's extent.
const DefaultPad = 25

// Scene is a Run with its background, floors and projection: what the renderers draw.
type Scene struct {
	Run     Run
	Box     Box
	Cuts    []float32
	Floors  []Floor
	Overlay *Overlay
	Notes   []string
	Cfg     Config
	// Scale is pixels per yard; W and H the size of the map area in pixels.
	Scale float64
	W, H  float64
	// Terrain says a navmesh background was given.
	Terrain bool
}

// NewScene projects the run. It fails when there is nothing to draw (no position on the map, and no box given).
func NewScene(run Run, o SceneOptions) (*Scene, error) {
	pad := o.Pad
	if pad <= 0 {
		pad = DefaultPad
	}
	var box Box
	if o.Box != nil {
		box = *o.Box
	} else {
		b, ok := run.Bounds(pad, 60, o.Overlay.Points()...)
		if !ok {
			return nil, fmt.Errorf("mapview: nothing to draw: no position on map %d", run.Map)
		}
		box = b
	}
	if box.Width() <= 0 || box.Height() <= 0 {
		return nil, fmt.Errorf("mapview: the box is empty (%+v)", box)
	}
	cfg := o.Config
	if cfg.GapAfter == 0 {
		cfg = DefaultConfig()
	}
	s := &Scene{Run: run, Box: box, Overlay: o.Overlay, Cfg: cfg, Notes: append([]string(nil), run.Notes...)}
	side := float64(o.MaxSide)
	if side <= 0 {
		side = 960
	}
	s.Scale = math.Min(side/float64(box.Width()), side/float64(box.Height()))
	s.Scale = math.Min(s.Scale, 40)
	s.W, s.H = float64(box.Width())*s.Scale, float64(box.Height())*s.Scale

	cuts := o.Cuts
	if o.Auto {
		fo := o.FloorOpts
		if fo.MinGap == 0 {
			fo = DefaultFloorOptions()
		}
		cuts = SplitHeights(o.Polys, fo)
	}
	s.Cuts = cuts
	s.Terrain = len(o.Polys) > 0
	s.Floors = buildFloors(o.Polys, cuts)
	if len(cuts) > 0 {
		s.Notes = append(s.Notes, fmt.Sprintf("%d floors, split at z %s: one image per floor", len(s.Floors), fmtCuts(cuts)))
	}
	switch {
	case o.BackgroundNote != "":
		s.Notes = append(s.Notes, o.BackgroundNote)
	case len(o.Polys) == 0:
		s.Notes = append(s.Notes, "no navmesh background")
	case o.PolysTruncated:
		s.Notes = append(s.Notes, "the navmesh background is cut to the tiles nearest the middle of the box")
	}
	return s, nil
}

func fmtCuts(c []float32) string {
	out := ""
	for i, v := range c {
		if i > 0 {
			out += ", "
		}
		out += fmt.Sprintf("%.0f", v)
	}
	return out
}

// FloorOf is the floor a height falls on.
func (s *Scene) FloorOf(z float32) int { return FloorIndex(s.Cuts, z) }

// px projects a position: north (x) up, west (y) left.
func (s *Scene) px(x, y float32) (float64, float64) {
	return float64(s.Box.MaxY-y) * s.Scale, float64(s.Box.MaxX-x) * s.Scale
}

// tickInterval is the spacing in seconds of the time ticks on the routes: the finest of a few round values that
// leaves no more than about a dozen ticks.
func tickInterval(seconds float64) float64 {
	for _, v := range []float64{15, 30, 60, 120, 300, 600, 900, 1800, 3600, 7200, 14400} {
		if seconds/v <= 12 {
			return v
		}
	}
	return 28800
}

// fmtClock formats seconds as m:ss, or h:mm:ss from an hour.
func fmtClock(sec float64) string {
	n := int(math.Round(sec))
	if n < 0 {
		n = 0
	}
	if n >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", n/3600, n/60%60, n%60)
	}
	return fmt.Sprintf("%d:%02d", n/60, n%60)
}

// fmtTick labels a tick: 30s, 5m, 1h30.
func fmtTick(sec float64) string {
	n := int(math.Round(sec))
	switch {
	case n < 60:
		return fmt.Sprintf("%ds", n)
	case n%3600 == 0:
		return fmt.Sprintf("%dh", n/3600)
	case n >= 3600:
		return fmt.Sprintf("%dh%02d", n/3600, n/60%60)
	case n%60 == 0:
		return fmt.Sprintf("%dm", n/60)
	}
	return fmt.Sprintf("%dm%02d", n/60, n%60)
}

// memberColors are the colours of the routes, leader first: the Okabe-Ito set, which stays apart for the common
// kinds of colour blindness, without its yellow (too faint on the floor colour).
var memberColors = []string{"#0072B2", "#D55E00", "#009E73", "#CC79A7", "#56B4E9", "#7A5195", "#E69F00", "#555555"}

// MemberColor is the colour of the i-th track.
func MemberColor(i int) string { return memberColors[i%len(memberColors)] }

// fateCounts counts the creatures by fate.
func (s *Scene) fateCounts() map[Fate]int {
	m := map[Fate]int{}
	for _, c := range s.Run.Creatures {
		m[c.Fate]++
	}
	return m
}

// segment is a stretch of a route: points joined by a continuous line, or a connector across a hole.
type segment struct {
	pts  []Pos
	kind string // "line", "gap" (no position for a while) or "jump" (a teleport-sized leap)
}

// routeSegments cuts one track into the pieces drawn on a floor (floor < 0: all of them).
func (s *Scene) routeSegments(tr Track, floor int) []segment {
	var out []segment
	var cur []Pos
	flush := func() {
		if len(cur) > 1 {
			out = append(out, segment{pts: cur, kind: "line"})
		}
		cur = nil
	}
	onFloor := func(p Pos) bool { return floor < 0 || s.FloorOf(p.Z) == floor }
	gap := s.Cfg.GapAfter.Seconds()
	for i := 1; i < len(tr.Samples); i++ {
		a, b := tr.Samples[i-1], tr.Samples[i]
		if a.Map != s.Run.Map || b.Map != s.Run.Map {
			flush()
			continue
		}
		if !onFloor(a.Pos) && !onFloor(b.Pos) {
			flush()
			continue
		}
		d := dist2D(a.Pos, b.Pos)
		switch {
		case d >= s.Cfg.TeleportMin:
			flush()
			out = append(out, segment{pts: []Pos{a.Pos, b.Pos}, kind: "jump"})
		case b.T-a.T > gap:
			flush()
			out = append(out, segment{pts: []Pos{a.Pos, b.Pos}, kind: "gap"})
		default:
			if len(cur) == 0 {
				cur = append(cur, a.Pos)
			}
			cur = append(cur, b.Pos)
		}
	}
	flush()
	return out
}

// tickPoints are the positions at the time ticks of a track.
func (s *Scene) tickPoints(tr Track, floor int) (pts []Pos, secs []float64) {
	if s.Run.Seconds <= 0 {
		return nil, nil
	}
	step := tickInterval(s.Run.Seconds)
	gap := s.Cfg.GapAfter.Seconds()
	for t := step; t < s.Run.Seconds; t += step {
		p, ok := tr.At(t)
		if !ok {
			continue
		}
		// Only where the member was seen near that time.
		i := sort.Search(len(tr.Samples), func(i int) bool { return tr.Samples[i].T >= t })
		near := false
		for _, j := range []int{i - 1, i} {
			if j >= 0 && j < len(tr.Samples) && math.Abs(tr.Samples[j].T-t) <= gap/2 && tr.Samples[j].Map == s.Run.Map {
				near = true
			}
		}
		if !near || (floor >= 0 && s.FloorOf(p.Z) != floor) {
			continue
		}
		pts, secs = append(pts, p), append(secs, t)
	}
	return pts, secs
}
