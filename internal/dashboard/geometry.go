package dashboard

import (
	"fmt"
	"html"
	"math"
	"strings"

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

// GeoLeg is the summary of one leg the page needs.
type GeoLeg struct {
	Index  int        `json:"index"`
	Name   string     `json:"name"`
	Groups int        `json:"groups"`
	Boss   uint32     `json:"boss,omitempty"`
	Path   []GeoPoint `json:"path"`
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
	return fmt.Sprintf(`<rect class="lbg" x="%.1f" y="%.1f" width="%.1f" height="15" rx="3"/><text class="%s" x="%.1f" y="%.1f">%s</text>`,
		x-3, y-11.5, n*6.6+6, cls, x, y, html.EscapeString(text))
}

// BuildGeometry draws plan: the route of every leg (via points, direct steps dashed, then the leg's end), the pull groups as
// circles of their radius, the bosses, and the doors and objects the steps use. The view box fits all of it with a margin.
func BuildGeometry(plan *dungeon.Plan) Geometry {
	g := Geometry{Plan: plan.Name, Map: plan.Map}
	var bb box
	var sb strings.Builder
	prevX, prevY := Project(plan.Entrance.X, plan.Entrance.Y)
	bb.add(prevX, prevY, 0)
	fmt.Fprintf(&sb, `<g class="mk entrance" data-x="%.1f" data-y="%.1f"><circle r="7"/>%s</g>`, prevX, prevY, label("entrance", 11, -9, "lt"))
	for i, leg := range plan.Legs {
		gl := GeoLeg{Index: i, Name: leg.Name, Groups: len(leg.PullGroups)}
		gl.Path = append(gl.Path, GeoPoint{X: prevX, Y: prevY})
		for _, w := range leg.Via {
			x, y := Project(w.X, w.Y)
			gl.Path = append(gl.Path, GeoPoint{X: x, Y: y, Direct: w.Direct})
			bb.add(x, y, 0)
		}
		toX, toY := Project(leg.To.X, leg.To.Y)
		gl.Path = append(gl.Path, GeoPoint{X: toX, Y: toY})
		bb.add(toX, toY, 0)
		fmt.Fprintf(&sb, `<g class="leg" data-leg="%d">`, i)
		writeRoute(&sb, i, gl.Path)
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
				fmt.Fprintf(&sb, `<g class="mk pin" data-leg="%d" data-group="%d" data-x="%.1f" data-y="%.1f"><circle r="9"/><text class="num" y="4">%d</text></g>`, i, k, x, y, k+1)
			}
		}
		if b := leg.Boss; b != nil {
			x, y := Project(b.Position.X, b.Position.Y)
			bb.add(x, y, 6)
			gl.Boss = b.Entry
			fmt.Fprintf(&sb, `<g class="mk boss" data-leg="%d" data-x="%.1f" data-y="%.1f"><polygon points="0,-14 14,0 0,14 -14,0"/><path d="M-5,-4 L5,4 M5,-4 L-5,4"/>%s</g>`,
				i, x, y, label(fmt.Sprintf("boss %d", b.Entry), 18, 5, "lt"))
		}
		for _, p := range stepPoints(leg) {
			x, y := Project(p.pt.X, p.pt.Y)
			bb.add(x, y, 2)
			fmt.Fprintf(&sb, `<g class="mk obj %s" data-leg="%d" data-x="%.1f" data-y="%.1f"><rect x="-5" y="-5" width="10" height="10"/>%s</g>`,
				p.kind, i, x, y, label(p.label, 9, 4, "lt"))
		}
		name := leg.Name
		if name == "" {
			name = fmt.Sprintf("leg %d", i)
		}
		fmt.Fprintf(&sb, `<g class="mk legname" data-x="%.1f" data-y="%.1f">%s</g></g>`, toX, toY, label(fmt.Sprintf("%d %s", i, name), 8, 20, "lt"))
		prevX, prevY = toX, toY
		g.Legs = append(g.Legs, gl)
	}
	g.ViewBox = fit(bb)
	g.Bounds = bb.bounds()
	g.Names = EntryNames(plan)
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
}

// stepPoints are the places the steps of a leg use: objects by their Near point, rituals, waits for hostiles to be gone.
func stepPoints(leg dungeon.Leg) []stepPoint {
	var out []stepPoint
	add := func(steps []dungeon.Step) {
		for _, s := range steps {
			switch {
			case s.UseObject != nil && s.UseObject.Near != nil:
				out = append(out, stepPoint{*s.UseObject.Near, "use", fmt.Sprintf("use %d", s.UseObject.Entry)})
			case s.LootObject != nil && s.LootObject.Near != nil:
				out = append(out, stepPoint{*s.LootObject.Near, "loot", fmt.Sprintf("loot %d", s.LootObject.Entry)})
			case s.Ritual != nil:
				out = append(out, stepPoint{s.Ritual.Position, "ritual", fmt.Sprintf("ritual %d", s.Ritual.Entry)})
			case s.WaitFor != nil && s.WaitFor.GameObject != nil && s.WaitFor.GameObject.Near != nil:
				out = append(out, stepPoint{*s.WaitFor.GameObject.Near, "door", fmt.Sprintf("wait %d %s", s.WaitFor.GameObject.Entry, s.WaitFor.GameObject.State)})
			}
		}
	}
	add(leg.Steps)
	add(leg.AfterBoss)
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
