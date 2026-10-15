package dashboard

import (
	"fmt"
	"html"
	"math"
	"strconv"
	"strings"
	"unicode"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// defaultPackRadius is the radius of a pull group that names none (the driver's PackRadius).
const defaultPackRadius = 25.0

// minSpan is the smallest width or height of the drawn area, in yards.
const minSpan = 80.0

// Project maps a game position to the drawing's plane. North (+x) is up and west (+y) is left, as the in-game map shows
// them; one unit is one yard.
func Project(x, y float32) (sx, sy float64) { return -float64(y), -float64(x) }

// GeoPoint is a point of a leg's route in drawing coordinates.
type GeoPoint struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Direct bool    `json:"direct,omitempty"`
}

// GeoWaypoint is one stop of a leg's planned route, in the order the plan visits them: the leg's start, its via points, the first
// position of each pull group, the boss, and the leg's end. N counts them from the start (0).
type GeoWaypoint struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Kind   string  `json:"kind"` // start, via, pull, boss, end
	Label  string  `json:"label"`
	N      int     `json:"n"`
	Group  int     `json:"group"` // the pull group's index in its leg, for a pull stop
	Direct bool    `json:"direct,omitempty"`
}

// GeoLeg is the summary of one leg the page needs.
type GeoLeg struct {
	Index  int           `json:"index"`
	Name   string        `json:"name"`
	Label  string        `json:"label"` // the name in words: "Sentinel room"
	Groups int           `json:"groups"`
	Boss   uint32        `json:"boss,omitempty"`
	Path   []GeoPoint    `json:"path"`
	Stops  []GeoWaypoint `json:"stops"`
	// Groups, Objects and BossAt are what the map's pull-group, object and patrol layers draw: every pull group with its place and
	// radius (and its patrol), every object the steps use, and where the boss stands.
	Packs   []GeoGroup  `json:"packs,omitempty"`
	Objects []GeoObject `json:"objects,omitempty"`
	BossAt  *GeoXY      `json:"boss_at,omitempty"`
}

// GeoXY is a point of the drawing.
type GeoXY struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// GeoGroup is one pull group of a leg. X and Y are the first position (the one the group is pulled at), Points all of them.
type GeoGroup struct {
	Index   int        `json:"index"`
	X       float64    `json:"x"`
	Y       float64    `json:"y"`
	R       float64    `json:"r"` // the radius a member may stand from a position
	N       int        `json:"n"` // creatures listed
	Ambush  bool       `json:"ambush,omitempty"`
	Label   string     `json:"label"`
	Entries []uint32   `json:"entries,omitempty"`
	Points  []GeoXY    `json:"points,omitempty"`
	Patrol  *GeoPatrol `json:"patrol,omitempty"`
}

// GeoPatrol is the path of a patrol that walks near a pull group, in the order it walks (it goes on from the last point to the first).
type GeoPatrol struct {
	Path    []GeoXY  `json:"path"`
	PeriodS float64  `json:"period_s,omitempty"`
	Entries []uint32 `json:"entries,omitempty"`
}

// GeoObject is a place a step of the leg uses: a door, chest, altar or keystone. Kind is use, loot, ritual or door (a wait for it).
type GeoObject struct {
	Kind  string  `json:"kind"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Entry uint32  `json:"entry,omitempty"`
	Label string  `json:"label"`
	Step  int     `json:"step"`
	After bool    `json:"after,omitempty"` // a step after the boss
}

// legLabel is a leg's name in words ("sentinel_room" -> "Sentinel room"), "Section N" for a leg without one.
func legLabel(name string, i int) string {
	n := strings.TrimSpace(strings.NewReplacer("_", " ", "-", " ").Replace(name))
	if n == "" {
		return fmt.Sprintf("Section %d", i+1)
	}
	r := []rune(n)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// packLabel names a pull group by the creatures in it: "pack 2: Relic Hunter, Geologist".
func packLabel(k int, pg dungeon.PullGroup, names map[string]string) string {
	var seen []string
	for _, e := range pg.Entries {
		n := names[strconv.FormatUint(uint64(e), 10)]
		if n == "" {
			n = fmt.Sprintf("creature %d", e)
		}
		dup := false
		for _, o := range seen {
			dup = dup || o == n
		}
		if !dup {
			seen = append(seen, n)
		}
	}
	if len(seen) > 3 {
		seen = seen[:3]
	}
	return fmt.Sprintf("pack %d: %s", k+1, strings.Join(seen, ", "))
}

// LegStops is the planned route of leg i from (startX, startY): the via points, the first position of every pull group, the boss, and
// the leg's end, in the order the plan lists them.
func LegStops(leg dungeon.Leg, i int, startX, startY float64, names map[string]string) []GeoWaypoint {
	label := legLabel(leg.Name, i)
	out := []GeoWaypoint{{X: startX, Y: startY, Kind: "start", Label: "start of " + label}}
	add := func(x, y float64, kind, text string, direct bool) {
		out = append(out, GeoWaypoint{X: x, Y: y, Kind: kind, Label: text, N: len(out), Direct: direct})
	}
	for _, w := range leg.Via {
		x, y := Project(w.X, w.Y)
		add(x, y, "via", "waypoint", w.Direct)
	}
	for k, pg := range leg.PullGroups {
		if len(pg.Positions) > 0 {
			x, y := Project(pg.Positions[0].X, pg.Positions[0].Y)
			add(x, y, "pull", packLabel(k, pg, names), false)
			out[len(out)-1].Group = k
		}
	}
	if b := leg.Boss; b != nil {
		x, y := Project(b.Position.X, b.Position.Y)
		text := fmt.Sprintf("boss %d", b.Entry)
		if n := names[strconv.FormatUint(uint64(b.Entry), 10)]; n != "" {
			text = "boss " + n
		}
		add(x, y, "boss", text, false)
	}
	x, y := Project(leg.To.X, leg.To.Y)
	add(x, y, "end", "end of "+label, false)
	return out
}

// Geometry is a plan ready to draw: the static SVG layer and the box it fits in.
type Geometry struct {
	Plan    string            `json:"plan"`
	Map     uint32            `json:"map"`
	ViewBox [4]float64        `json:"view_box"` // min x, min y, width, height
	Bounds  [4]float64        `json:"bounds"`   // the same without the margin: the tight box of everything drawn
	Legs    []GeoLeg          `json:"legs"`
	Names   map[string]string `json:"names,omitempty"` // creature entry (decimal) -> name, as far as the plan's notes name them
	SVG     string            `json:"svg"`             // the inner markup of the <svg>: routes, pull groups, bosses, objects
}

type box struct {
	minX, minY, maxX, maxY float64
	set                    bool
}

func (b *box) add(x, y, r float64) {
	if !b.set {
		b.minX, b.minY, b.maxX, b.maxY, b.set = x-r, y-r, x+r, y+r, true
		return
	}
	b.minX, b.minY = math.Min(b.minX, x-r), math.Min(b.minY, y-r)
	b.maxX, b.maxY = math.Max(b.maxX, x+r), math.Max(b.maxY, y+r)
}

func groupRadius(g dungeon.PullGroup) float64 {
	if g.Radius > 0 {
		return float64(g.Radius)
	}
	return defaultPackRadius
}

// bounds is the tight box of everything added (min x, min y, width, height), or the padded default for an empty one.
func (b box) bounds() [4]float64 {
	if !b.set {
		return [4]float64{-minSpan / 2, -minSpan / 2, minSpan, minSpan}
	}
	return [4]float64{round1(b.minX), round1(b.minY), round1(b.maxX - b.minX), round1(b.maxY - b.minY)}
}

// label draws a text on a dark, semi-transparent plate. The markers are drawn in pixels around their own origin
// and the page scales them by the zoom, so the plate's width is an estimate from the character count.
func label(text string, x, y float64, cls string) string {
	n := float64(len([]rune(text)))
	return fmt.Sprintf(`<g class="lab"><rect class="lbg" x="%.1f" y="%.1f" width="%.1f" height="15" rx="3"/><text class="%s" x="%.1f" y="%.1f">%s</text></g>`,
		x-3, y-11.5, n*6.6+6, cls, x, y, html.EscapeString(text))
}

// BuildGeometry draws plan: the route of every leg (via points, direct steps dashed, then the leg's end), the pull groups as
// circles of their radius, the bosses, and the doors and objects the steps use. The view box fits all of it with a margin.
func BuildGeometry(plan *dungeon.Plan) Geometry {
	g := Geometry{Plan: plan.Name, Map: plan.Map}
	var bb box
	var sb strings.Builder
	names := EntryNames(plan)
	prevX, prevY := Project(plan.Entrance.X, plan.Entrance.Y)
	bb.add(prevX, prevY, 0)
	fmt.Fprintf(&sb, `<g class="mk entrance" data-x="%.1f" data-y="%.1f"><circle r="7"/>%s</g>`, prevX, prevY, label("entrance", 11, -9, "lt"))
	for i, leg := range plan.Legs {
		gl := GeoLeg{Index: i, Name: leg.Name, Label: legLabel(leg.Name, i), Groups: len(leg.PullGroups)}
		gl.Stops = LegStops(leg, i, prevX, prevY, names)
		for _, st := range gl.Stops {
			bb.add(st.X, st.Y, 0)
		}
		for _, st := range gl.Stops {
			// a stop on the same spot as the one before is the same point on the line (a boss at the leg's end)
			if n := len(gl.Path); n > 0 && math.Hypot(gl.Path[n-1].X-st.X, gl.Path[n-1].Y-st.Y) < 0.5 {
				continue
			}
			gl.Path = append(gl.Path, GeoPoint{X: st.X, Y: st.Y, Direct: st.Direct && st.Kind == "via"})
		}
		last := gl.Stops[len(gl.Stops)-1]
		toX, toY := last.X, last.Y
		fmt.Fprintf(&sb, `<g class="leg" data-leg="%d">`, i)
		writeRoute(&sb, i, gl.Path)
		for _, st := range gl.Stops {
			if st.Kind != "start" {
				fmt.Fprintf(&sb, `<g class="mk stepn" data-leg="%d" data-x="%.1f" data-y="%.1f">%s</g>`, i, st.X, st.Y, label(strconv.Itoa(st.N), 10, -8, "lt"))
			}
			if st.Kind == "via" || st.Kind == "end" {
				fmt.Fprintf(&sb, `<g class="mk wp %s" data-leg="%d" data-x="%.1f" data-y="%.1f"><circle r="3.5"/><title>%s</title></g>`,
					st.Kind, i, st.X, st.Y, html.EscapeString(fmt.Sprintf("%s, step %d of %d: %s", gl.Label, st.N, len(gl.Stops)-1, st.Label)))
			}
		}
		for k, pg := range leg.PullGroups {
			r := groupRadius(pg)
			cls := "pull"
			if pg.Ambush {
				cls = "pull ambush"
			}
			for _, pos := range pg.Positions {
				x, y := Project(pos.X, pos.Y)
				bb.add(x, y, r)
				fmt.Fprintf(&sb, `<circle class="%s" data-leg="%d" data-group="%d" cx="%.1f" cy="%.1f" r="%.1f"/>`, cls, i, k, x, y, r)
			}
			if len(pg.Positions) > 0 {
				x, y := Project(pg.Positions[0].X, pg.Positions[0].Y)
				fmt.Fprintf(&sb, `<g class="mk pin" data-leg="%d" data-group="%d" data-x="%.1f" data-y="%.1f"><circle r="9"/><text class="num" y="4">%d</text><title>%s</title></g>`, i, k, x, y, k+1,
					html.EscapeString(fmt.Sprintf("%s, step %d of %d: %s", gl.Label, stopOf(gl.Stops, k), len(gl.Stops)-1, packLabel(k, pg, names))))
			}
		}
		for k, pg := range leg.PullGroups {
			if len(pg.Positions) == 0 {
				continue
			}
			x, y := Project(pg.Positions[0].X, pg.Positions[0].Y)
			gg := GeoGroup{Index: k, X: round1(x), Y: round1(y), R: groupRadius(pg), N: len(pg.Entries), Ambush: pg.Ambush, Label: packLabel(k, pg, names), Entries: pg.Entries}
			for _, pos := range pg.Positions {
				px, py := Project(pos.X, pos.Y)
				gg.Points = append(gg.Points, GeoXY{round1(px), round1(py)})
			}
			if pt := pg.Patrol; pt != nil && len(pt.Path) >= 2 {
				gp := &GeoPatrol{PeriodS: float64(pt.PeriodS), Entries: pt.Entries}
				for _, w := range pt.Path {
					px, py := Project(w.X, w.Y)
					bb.add(px, py, 0)
					gp.Path = append(gp.Path, GeoXY{round1(px), round1(py)})
				}
				gg.Patrol = gp
			}
			gl.Packs = append(gl.Packs, gg)
		}
		if b := leg.Boss; b != nil {
			x, y := Project(b.Position.X, b.Position.Y)
			bb.add(x, y, 6)
			gl.Boss = b.Entry
			gl.BossAt = &GeoXY{round1(x), round1(y)}
			fmt.Fprintf(&sb, `<g class="mk boss" data-leg="%d" data-x="%.1f" data-y="%.1f"><polygon points="0,-14 14,0 0,14 -14,0"/><path d="M-5,-4 L5,4 M5,-4 L-5,4"/>%s</g>`,
				i, x, y, label(stopLabel(gl.Stops, "boss", fmt.Sprintf("boss %d", b.Entry)), 18, 5, "lt"))
		}
		for _, p := range stepPoints(leg) {
			x, y := Project(p.pt.X, p.pt.Y)
			bb.add(x, y, 2)
			gl.Objects = append(gl.Objects, GeoObject{Kind: p.kind, X: round1(x), Y: round1(y), Entry: p.entry, Label: p.label, Step: p.step, After: p.after})
			fmt.Fprintf(&sb, `<g class="mk obj %s" data-leg="%d" data-x="%.1f" data-y="%.1f"><rect class="ic" x="-4" y="-5" width="8" height="10" rx="1"/><circle class="knob" cx="1.5" cy="0" r="1"/>%s</g>`,
				p.kind, i, x, y, label(p.label, 9, 4, "lt"))
		}
		fmt.Fprintf(&sb, `<g class="mk legname" data-x="%.1f" data-y="%.1f">%s</g></g>`, prevX, prevY, label(gl.Label, 9, 22, "lt"))
		prevX, prevY = toX, toY
		g.Legs = append(g.Legs, gl)
	}
	g.ViewBox = fit(bb)
	g.Bounds = bb.bounds()
	g.Names = names
	g.SVG = sb.String()
	return g
}

func writeRoute(sb *strings.Builder, leg int, path []GeoPoint) {
	// Consecutive plain points are one polyline; a direct step is its own dashed line.
	var run []GeoPoint
	flush := func() {
		if len(run) < 2 {
			return
		}
		pts := make([]string, len(run))
		for i, p := range run {
			pts[i] = fmt.Sprintf("%.1f,%.1f", p.X, p.Y)
		}
		joined := strings.Join(pts, " ")
		fmt.Fprintf(sb, `<polyline class="halo" data-leg="%d" points="%s"/><polyline class="route" data-leg="%d" points="%s"/>`, leg, joined, leg, joined)
	}
	for i, p := range path {
		if i > 0 && p.Direct {
			flush()
			run = []GeoPoint{p}
			prev := path[i-1]
			fmt.Fprintf(sb, `<line class="halo dhalo" data-leg="%d" x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f"/><line class="direct" data-leg="%d" x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f"/>`,
				leg, prev.X, prev.Y, p.X, p.Y, leg, prev.X, prev.Y, p.X, p.Y)
			continue
		}
		run = append(run, p)
	}
	flush()
}

type stepPoint struct {
	pt    dungeon.Point
	kind  string
	label string
	entry uint32
	step  int  // the step's index in Steps or AfterBoss
	after bool // it is one of AfterBoss
}

// stepPoints are the places the steps of a leg use: objects by their Near point, rituals, waits for hostiles to be gone.
func stepPoints(leg dungeon.Leg) []stepPoint {
	var out []stepPoint
	add := func(steps []dungeon.Step, after bool) {
		for i, s := range steps {
			var p *stepPoint
			switch {
			case s.UseObject != nil && s.UseObject.Near != nil:
				p = &stepPoint{pt: *s.UseObject.Near, kind: "use", label: fmt.Sprintf("use %d", s.UseObject.Entry), entry: s.UseObject.Entry}
			case s.LootObject != nil && s.LootObject.Near != nil:
				p = &stepPoint{pt: *s.LootObject.Near, kind: "loot", label: fmt.Sprintf("loot %d", s.LootObject.Entry), entry: s.LootObject.Entry}
			case s.Ritual != nil:
				p = &stepPoint{pt: s.Ritual.Position, kind: "ritual", label: fmt.Sprintf("ritual %d", s.Ritual.Entry), entry: s.Ritual.Entry}
			case s.WaitFor != nil && s.WaitFor.GameObject != nil && s.WaitFor.GameObject.Near != nil:
				p = &stepPoint{pt: *s.WaitFor.GameObject.Near, kind: "door", label: fmt.Sprintf("wait %d %s", s.WaitFor.GameObject.Entry, s.WaitFor.GameObject.State), entry: s.WaitFor.GameObject.Entry}
			}
			if p != nil {
				p.step, p.after = i, after
				out = append(out, *p)
			}
		}
	}
	add(leg.Steps, false)
	add(leg.AfterBoss, true)
	return out
}

// fit pads the box by a tenth (at least 15 yards) and widens a box thinner than minSpan, so that a short plan is not
// drawn as a point.
func fit(b box) [4]float64 {
	if !b.set {
		return [4]float64{-minSpan / 2, -minSpan / 2, minSpan, minSpan}
	}
	w, h := b.maxX-b.minX, b.maxY-b.minY
	padX, padY := math.Max(15, w/10), math.Max(15, h/10)
	b.minX, b.maxX, b.minY, b.maxY = b.minX-padX, b.maxX+padX, b.minY-padY, b.maxY+padY
	for _, ax := range []struct{ lo, hi *float64 }{{&b.minX, &b.maxX}, {&b.minY, &b.maxY}} {
		if d := minSpan - (*ax.hi - *ax.lo); d > 0 {
			*ax.lo -= d / 2
			*ax.hi += d / 2
		}
	}
	return [4]float64{round1(b.minX), round1(b.minY), round1(b.maxX - b.minX), round1(b.maxY - b.minY)}
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// GroupOf finds the pull group a hostile with entry at (x, y) belongs to: the group of leg whose entries list it and one of
// whose positions is within the group's radius. group is -1 for no group, and boss is true for a boss or an add of the boss
// fight. A hostile is looked for in the legs from leg on first, so that a pack of the current leg wins over a later twin.
func GroupOf(plan *dungeon.Plan, leg int, entry uint32, x, y float32) (l, group int, boss bool) {
	n := len(plan.Legs)
	best, bestL, bestG, bestD := false, -1, -1, math.MaxFloat64
	for off := 0; off < n; off++ {
		li := (max(leg, 0) + off) % n
		lg := plan.Legs[li]
		if b := lg.Boss; b != nil && (b.Entry == entry || containsU32(b.First, entry)) {
			return li, -1, true
		}
		for gi, pg := range lg.PullGroups {
			if !containsU32(pg.Entries, entry) {
				continue
			}
			r := groupRadius(pg)
			for _, p := range pg.Positions {
				d := math.Hypot(float64(p.X-x), float64(p.Y-y))
				if d <= r && d < bestD {
					best, bestL, bestG, bestD = true, li, gi, d
				}
			}
		}
		if best {
			return bestL, bestG, false // the nearest leg from the current one that has a fit
		}
	}
	return -1, -1, false
}

func containsU32(s []uint32, v uint32) bool {
	for _, e := range s {
		if e == v {
			return true
		}
	}
	return false
}

// stopOf is the step number of the stop of pull group k.
func stopOf(stops []GeoWaypoint, k int) int {
	for _, s := range stops {
		if s.Kind == "pull" && s.Group == k {
			return s.N
		}
	}
	return 0
}

// stopLabel is the label of the first stop of a kind, or fallback.
func stopLabel(stops []GeoWaypoint, kind, fallback string) string {
	for _, s := range stops {
		if s.Kind == kind {
			return s.Label
		}
	}
	return fallback
}
