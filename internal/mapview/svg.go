package mapview

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

// Layout of the image, in pixels.
const (
	margin     = 24
	titleH     = 46
	legendW    = 280
	footerH    = 30
	fontFamily = "DejaVu Sans, Verdana, Arial, sans-serif"
)

// Colours of the drawing.
const (
	colBackdrop = "#f7f6f2"
	colText     = "#222222"
	colMuted    = "#666666"
	colGround   = "#e4dfcd"
	colGroundLn = "#cfc8ae"
	colWater    = "#bcd8ea"
	colHazard   = "#ebb5a8"
	colSteep    = "#d2d2d2"
	colKilled   = "#b2182b"
	colSkipped  = "#7a7a7a"
	colEvaded   = "#e08214"
	colAlive    = "#762a83"
	colBoss     = "#c99700"
	colPlan     = "#4b3fa0"
	colDoor     = "#8c5a1e"
	colTeleport = "#c2185b"
)

// fateColor is the colour of a creature's dot.
func fateColor(f Fate) string {
	switch f {
	case FateKilled:
		return colKilled
	case FateEvaded:
		return colEvaded
	case FateAlive:
		return colAlive
	}
	return colSkipped
}

// f1 formats a number with one decimal, without a minus sign on zero.
func f1(v float64) string {
	s := strconv.FormatFloat(v, 'f', 1, 64)
	if s == "-0.0" {
		return "0.0"
	}
	return s
}

// esc makes text safe in XML and HTML: markup characters escaped, control characters dropped.
func esc(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '&':
			b.WriteString("&amp;")
		case r == '"':
			b.WriteString("&quot;")
		case r == '\'':
			b.WriteString("&#39;")
		case r == '\t' || r == '\n' || r == '\r':
			b.WriteByte(' ')
		case r < 0x20 || r == 0x7f || r == utf8.RuneError:
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

type svgw struct {
	strings.Builder
}

func (w *svgw) f(format string, args ...any) { fmt.Fprintf(&w.Builder, format, args...) }

// FloorCount is the number of images a scene makes.
func (s *Scene) FloorCount() int { return len(s.Floors) }

// SVG draws one floor (0 for a map of one floor) as a standalone SVG document.
func (s *Scene) SVG(floor int) []byte {
	if floor < 0 || floor >= len(s.Floors) {
		floor = 0
	}
	w := &svgw{}
	s.writeSVG(w, floor, false)
	return []byte(w.String())
}

// layout returns the position of the map area and the size of the whole image.
func (s *Scene) layout() (mx, my, width, height float64) {
	mx, my = margin, margin+titleH
	width = mx + s.W + margin + legendW + margin
	height = math.Max(my+s.H+footerH+margin, my+float64(s.legendHeight())+footerH+margin)
	return
}

func (s *Scene) floorTitle(floor int) string {
	if len(s.Floors) > 1 {
		return fmt.Sprintf("%s, %s", s.Run.Title, s.Floors[floor].Name)
	}
	return s.Run.Title
}

// writeSVG writes the document. With dynamic set the routes, creatures and markers are left out for the HTML
// replay to draw; the rest is the same.
func (s *Scene) writeSVG(w *svgw, floor int, dynamic bool) {
	mx, my, width, height := s.layout()
	w.f(`<svg xmlns="http://www.w3.org/2000/svg" width="%s" height="%s" viewBox="0 0 %s %s" role="img" font-family="%s" font-size="11">`+"\n",
		f1(width), f1(height), f1(width), f1(height), fontFamily)
	w.f("<title>%s</title>\n", esc(s.floorTitle(floor)))
	w.f("<desc>Top-down map of map %d: %d routes, %d hostile creatures, %d events. North is up.</desc>\n",
		s.Run.Map, len(s.Run.Tracks), len(s.Run.Creatures), len(s.Run.Markers))
	w.f(`<defs><clipPath id="map-clip"><rect x="%s" y="%s" width="%s" height="%s"/></clipPath>`+
		`<marker id="arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0 0L10 5L0 10z" fill="%s"/></marker></defs>`+"\n",
		f1(mx), f1(my), f1(s.W), f1(s.H), colTeleport)
	w.f(`<rect width="100%%" height="100%%" fill="%s"/>`+"\n", colBackdrop)
	w.f(`<text x="%s" y="%s" font-size="17" font-weight="bold" fill="%s">%s</text>`+"\n", f1(margin), f1(margin+14), colText, esc(s.floorTitle(floor)))
	w.f(`<text x="%s" y="%s" fill="%s">%s</text>`+"\n", f1(margin), f1(margin+32), colMuted, esc(s.subtitle()))
	w.f(`<rect x="%s" y="%s" width="%s" height="%s" fill="#ffffff" stroke="#bbbbbb"/>`+"\n", f1(mx), f1(my), f1(s.W), f1(s.H))
	w.f(`<g clip-path="url(#map-clip)"><g transform="translate(%s %s)">`+"\n", f1(mx), f1(my))
	s.writeBackground(w, floor)
	s.writeOverlay(w, floor)
	if !dynamic {
		s.writeStatic(w, floor)
	} else {
		w.WriteString(`<g id="dyn"></g>` + "\n")
	}
	w.WriteString("</g></g>\n")
	s.writeScale(w, mx, my)
	s.writeLegend(w, floor, mx+s.W+margin, my, dynamic)
	w.f(`<text x="%s" y="%s" fill="%s">%s</text>`+"\n", f1(margin), f1(height-12), colMuted, esc(s.footer()))
	w.WriteString("</svg>\n")
}

func (s *Scene) subtitle() string {
	start := ""
	if !s.Run.Start.IsZero() {
		start = s.Run.Start.UTC().Format("2006-01-02 15:04:05 UTC") + ", "
	}
	return fmt.Sprintf("map %d, %s%s long, %d members, %d hostile creatures seen", s.Run.Map, start, fmtClock(s.Run.Seconds),
		len(s.Run.Tracks), len(s.Run.Creatures))
}

func (s *Scene) footer() string {
	return "turtle-agent map: drawn from what the agent saw, not from the game world. North is up."
}

func (s *Scene) writeBackground(w *svgw, floor int) {
	if floor >= len(s.Floors) {
		return
	}
	polys := s.Floors[floor].Polys
	if len(polys) == 0 {
		return
	}
	// Cull what falls outside the map or is too small to see; double the size limit until the path is modest.
	type drawn struct {
		class string
		pts   [][2]float64
		size  float64
	}
	var all []drawn
	for _, p := range polys {
		pts := make([][2]float64, len(p.Verts))
		minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
		for i, v := range p.Verts {
			x, y := s.px(v[0], v[1])
			pts[i] = [2]float64{x, y}
			minX, maxX = math.Min(minX, x), math.Max(maxX, x)
			minY, maxY = math.Min(minY, y), math.Max(maxY, y)
		}
		if maxX < 0 || minX > s.W || maxY < 0 || minY > s.H {
			continue
		}
		all = append(all, drawn{terrainClass(p.Flags), pts, math.Max(maxX-minX, maxY-minY)})
	}
	const maxPolys = 40000
	for limit := 0.5; len(all) > maxPolys; limit *= 2 {
		kept := all[:0]
		for _, d := range all {
			if d.size >= limit {
				kept = append(kept, d)
			}
		}
		all = kept
	}
	for _, class := range []string{"steep", "ground", "water", "hazard"} {
		var d strings.Builder
		for _, p := range all {
			if p.class != class {
				continue
			}
			for i, q := range p.pts {
				if i == 0 {
					d.WriteString("M")
				} else {
					d.WriteString("L")
				}
				d.WriteString(f1(q[0]))
				d.WriteByte(' ')
				d.WriteString(f1(q[1]))
			}
			d.WriteString("Z")
		}
		if d.Len() == 0 {
			continue
		}
		fill, stroke := colGround, colGroundLn
		switch class {
		case "water":
			fill, stroke = colWater, colWater
		case "hazard":
			fill, stroke = colHazard, colHazard
		case "steep":
			fill, stroke = colSteep, colSteep
		}
		w.f(`<path class="terrain %s" d="%s" fill="%s" stroke="%s" stroke-width="0.4" stroke-linejoin="round"/>`+"\n", class, d.String(), fill, stroke)
	}
}

// terrainClass names how a navmesh polygon is drawn.
func terrainClass(flags uint16) string {
	switch {
	case flags&(navmesh.NavMagma|navmesh.NavSlime) != 0:
		return "hazard"
	case flags&navmesh.NavWater != 0:
		return "water"
	case flags&navmesh.NavSteep != 0:
		return "steep"
	}
	return "ground"
}

// writeOverlay draws the dungeon plan under the run.
func (s *Scene) writeOverlay(w *svgw, floor int) {
	o := s.Overlay
	if o == nil {
		return
	}
	on := func(p Pos) bool { return len(s.Floors) < 2 || s.FloorOf(p.Z) == floor }
	w.f(`<g class="plan" fill="none" stroke="%s">`+"\n", colPlan)
	var d strings.Builder
	for i, p := range o.Route {
		x, y := s.px(p.X, p.Y)
		if i == 0 {
			d.WriteString("M")
		} else {
			d.WriteString("L")
		}
		d.WriteString(f1(x) + " " + f1(y))
	}
	if d.Len() > 0 {
		w.f(`<path d="%s" stroke-width="2" stroke-dasharray="7 4" opacity="0.75"/>`+"\n", d.String())
	}
	for i, p := range o.Route {
		if !on(p) {
			continue
		}
		x, y := s.px(p.X, p.Y)
		label := "entrance"
		if i > 0 {
			label = fmt.Sprintf("%d %s", i, o.Legs[i-1].Name)
		}
		w.f(`<rect x="%s" y="%s" width="8" height="8" fill="#ffffff" stroke-width="1.6"/>`+"\n", f1(x-4), f1(y-4))
		w.f(`<text x="%s" y="%s" stroke="none" fill="%s" font-size="10">%s</text>`+"\n", f1(x+7), f1(y-6), colPlan, esc(label))
	}
	for _, g := range o.Groups {
		for _, p := range g.Positions {
			if !on(p) {
				continue
			}
			x, y := s.px(p.X, p.Y)
			dash := ""
			if g.Ambush {
				dash = ` stroke-dasharray="2 2"`
			}
			w.f(`<path d="M%s %sl4 4l-4 4l-4 -4z" stroke-width="1.3"%s/>`+"\n", f1(x), f1(y-4), dash)
		}
	}
	for _, b := range o.Bosses {
		if !on(b.Pos) {
			continue
		}
		x, y := s.px(b.Pos.X, b.Pos.Y)
		w.f(`<path d="%s" stroke-width="1.6"/>`+"\n", starPath(x, y, 9, 4))
		w.f(`<text x="%s" y="%s" stroke="none" fill="%s" font-size="10">planned boss %d</text>`+"\n", f1(x+11), f1(y+4), colPlan, b.Entry)
	}
	for _, u := range o.Uses {
		if !on(u.Pos) {
			continue
		}
		x, y := s.px(u.Pos.X, u.Pos.Y)
		w.f(`<circle cx="%s" cy="%s" r="4" stroke-width="1.4" stroke-dasharray="2 1.5"/>`+"\n", f1(x), f1(y))
		w.f(`<text x="%s" y="%s" stroke="none" fill="%s" font-size="9">use %d</text>`+"\n", f1(x+6), f1(y+3), colPlan, u.Entry)
	}
	w.WriteString("</g>\n")
}

// starPath is a five-pointed star centred at x, y.
func starPath(x, y, outer, inner float64) string {
	var d strings.Builder
	for i := 0; i < 10; i++ {
		r := outer
		if i%2 == 1 {
			r = inner
		}
		a := -math.Pi/2 + float64(i)*math.Pi/5
		if i == 0 {
			d.WriteString("M")
		} else {
			d.WriteString("L")
		}
		d.WriteString(f1(x+r*math.Cos(a)) + " " + f1(y+r*math.Sin(a)))
	}
	d.WriteString("Z")
	return d.String()
}

// simplify thins a polyline (Douglas-Peucker) to within eps pixels.
func simplify(pts [][2]float64, eps float64) [][2]float64 {
	if len(pts) < 3 {
		return pts
	}
	keep := make([]bool, len(pts))
	keep[0], keep[len(pts)-1] = true, true
	type span struct{ a, b int }
	stack := []span{{0, len(pts) - 1}}
	for len(stack) > 0 {
		sp := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		far, at := 0.0, -1
		for i := sp.a + 1; i < sp.b; i++ {
			if d := segPointDist(pts[i], pts[sp.a], pts[sp.b]); d > far {
				far, at = d, i
			}
		}
		if at >= 0 && far > eps {
			keep[at] = true
			stack = append(stack, span{sp.a, at}, span{at, sp.b})
		}
	}
	out := make([][2]float64, 0, len(pts))
	for i, p := range pts {
		if keep[i] {
			out = append(out, p)
		}
	}
	return out
}

func segPointDist(p, a, b [2]float64) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	l := dx*dx + dy*dy
	if l == 0 {
		return math.Hypot(p[0]-a[0], p[1]-a[1])
	}
	t := math.Max(0, math.Min(1, ((p[0]-a[0])*dx+(p[1]-a[1])*dy)/l))
	return math.Hypot(p[0]-a[0]-t*dx, p[1]-a[1]-t*dy)
}

// writeStatic draws the packs, routes, creatures and markers of one floor.
func (s *Scene) writeStatic(w *svgw, floor int) {
	multi := len(s.Floors) > 1
	on := func(p Pos) bool { return !multi || s.FloorOf(p.Z) == floor }
	s.writePacks(w, on)
	// Routes, the leader last so it lies on top.
	order := make([]int, len(s.Run.Tracks))
	for i := range order {
		order[i] = len(order) - 1 - i
	}
	flo := floor
	if !multi {
		flo = -1
	}
	for _, i := range order {
		tr := s.Run.Tracks[i]
		col := MemberColor(i)
		w.f(`<g class="route" data-member="%s" fill="none" stroke="%s" stroke-linejoin="round" stroke-linecap="round">`+"\n", esc(tr.Name), col)
		for _, seg := range s.routeSegments(tr, flo) {
			pts := make([][2]float64, len(seg.pts))
			for k, p := range seg.pts {
				x, y := s.px(p.X, p.Y)
				pts[k] = [2]float64{x, y}
			}
			var d strings.Builder
			for k, p := range simplify(pts, 0.4) {
				if k == 0 {
					d.WriteString("M")
				} else {
					d.WriteString("L")
				}
				d.WriteString(f1(p[0]) + " " + f1(p[1]))
			}
			switch seg.kind {
			case "gap":
				w.f(`<path d="%s" stroke-width="1.2" stroke-dasharray="1 4" opacity="0.8"/>`+"\n", d.String())
			case "jump":
				w.f(`<path d="%s" stroke="%s" stroke-width="1.4" stroke-dasharray="6 4" marker-end="url(#arrow)"/>`+"\n", d.String(), colTeleport)
			default:
				w.f(`<path d="%s" stroke-width="%s" opacity="0.9"/>`+"\n", d.String(), f1(map[bool]float64{true: 2.2, false: 1.5}[tr.Leader]))
			}
		}
		w.WriteString("</g>\n")
	}
	// Time ticks: dots on every route, labels on the leader's.
	for _, i := range order {
		tr := s.Run.Tracks[i]
		pts, secs := s.tickPoints(tr, flo)
		for k, p := range pts {
			x, y := s.px(p.X, p.Y)
			w.f(`<circle class="tick" cx="%s" cy="%s" r="2.4" fill="%s" stroke="#ffffff" stroke-width="0.8"/>`+"\n", f1(x), f1(y), MemberColor(i))
			if tr.Leader {
				w.f(`<text x="%s" y="%s" font-size="9" fill="%s" stroke="#ffffff" stroke-width="2.5" paint-order="stroke">%s</text>`+"\n",
					f1(x+4), f1(y-4), colText, fmtTick(secs[k]))
			}
		}
	}
	// Creatures.
	w.WriteString(`<g class="creatures">` + "\n")
	for _, c := range s.Run.Creatures {
		if !on(c.Pos) || c.Boss {
			continue
		}
		x, y := s.px(c.X, c.Y)
		w.f(`<circle cx="%s" cy="%s" r="3.2" %s><title>%s</title></circle>`+"\n", f1(x), f1(y), fateStyle(c.Fate), esc(creatureTitle(c)))
	}
	w.WriteString("</g>\n")
	// Bosses, with their names.
	for _, c := range s.Run.Creatures {
		if !on(c.Pos) || !c.Boss {
			continue
		}
		x, y := s.px(c.X, c.Y)
		w.f(`<g class="boss"><path d="%s" fill="%s" stroke="%s" stroke-width="2"/>`, starPath(x, y, 10, 4.5), fateColor(c.Fate), colBoss)
		label := c.Name
		if c.Fate == FateKilled {
			label += " (killed " + fmtClock(c.FateT) + ")"
		} else {
			label += " (" + string(c.Fate) + ")"
		}
		w.f(`<text x="%s" y="%s" font-size="11" font-weight="bold" fill="%s" stroke="#ffffff" stroke-width="3" paint-order="stroke">%s</text></g>`+"\n",
			f1(x+13), f1(y+4), colText, esc(label))
	}
	// Events.
	for _, m := range s.Run.Markers {
		if m.Map != s.Run.Map || !on(m.Pos) {
			continue
		}
		s.writeMarker(w, m)
	}
}

func fateStyle(f Fate) string {
	if f == FateSkipped {
		return fmt.Sprintf(`fill="#ffffff" stroke="%s" stroke-width="1.4"`, colSkipped)
	}
	return fmt.Sprintf(`fill="%s" stroke="#ffffff" stroke-width="1"`, fateColor(f))
}

func creatureTitle(c Creature) string {
	t := fmt.Sprintf("%s (entry %d", c.Name, c.Entry)
	if c.Level > 0 {
		t += fmt.Sprintf(", level %d", c.Level)
	}
	t += "): " + string(c.Fate)
	if c.Fate == FateKilled || c.Fate == FateEvaded {
		t += " at " + fmtClock(c.FateT)
	}
	return t
}

// writePacks rings the packs of creatures.
func (s *Scene) writePacks(w *svgw, on func(Pos) bool) {
	type acc struct {
		x, y, n float64
		members []Creature
	}
	packs := map[int]*acc{}
	for _, c := range s.Run.Creatures {
		if c.Pack == 0 || !on(c.Pos) {
			continue
		}
		a := packs[c.Pack]
		if a == nil {
			a = &acc{}
			packs[c.Pack] = a
		}
		a.x += float64(c.X)
		a.y += float64(c.Y)
		a.n++
		a.members = append(a.members, c)
	}
	ids := make([]int, 0, len(packs))
	for id := range packs {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	if len(ids) == 0 {
		return
	}
	w.WriteString(`<g class="packs" fill="#000000" fill-opacity="0.04" stroke="#777777" stroke-width="1" stroke-dasharray="3 3">` + "\n")
	for _, id := range ids {
		a := packs[id]
		cx, cy := a.x/a.n, a.y/a.n
		r := 0.0
		for _, c := range a.members {
			r = math.Max(r, math.Hypot(float64(c.X)-cx, float64(c.Y)-cy))
		}
		px, py := s.px(float32(cx), float32(cy))
		w.f(`<circle cx="%s" cy="%s" r="%s"><title>pack %d, %d creatures</title></circle>`+"\n", f1(px), f1(py), f1(math.Max(r*s.Scale+7, 9)), id, int(a.n))
	}
	w.WriteString("</g>\n")
}

func (s *Scene) writeMarker(w *svgw, m Marker) {
	x, y := s.px(m.X, m.Y)
	title := fmt.Sprintf("%s at %s", m.Label, fmtClock(m.T))
	switch m.Kind {
	case MarkPull:
		w.f(`<g class="pull"><path d="M%s %sl7 7l-7 7l-7 -7z" fill="#ffffff" stroke="%s" stroke-width="1.8"/>`+
			`<text x="%s" y="%s" font-size="8" text-anchor="middle" fill="%s">%s</text><title>%s</title></g>`+"\n",
			f1(x), f1(y-7), colEvaded, f1(x), f1(y+3), colText, esc(pullNumber(m.Label)), esc(title))
	case MarkDeath:
		w.f(`<g class="death" stroke="#111111" stroke-width="3" stroke-linecap="round"><path d="M%s %sL%s %sM%s %sL%s %s"/>`+
			`<path d="M%s %sL%s %sM%s %sL%s %s" stroke="%s" stroke-width="1.4"/><title>%s</title></g>`+"\n",
			f1(x-5), f1(y-5), f1(x+5), f1(y+5), f1(x-5), f1(y+5), f1(x+5), f1(y-5),
			f1(x-5), f1(y-5), f1(x+5), f1(y+5), f1(x-5), f1(y+5), f1(x+5), f1(y-5), colKilled, esc(title))
		w.f(`<text x="%s" y="%s" font-size="9" fill="%s" stroke="#ffffff" stroke-width="2.5" paint-order="stroke">%s</text>`+"\n", f1(x+7), f1(y+14), colKilled, esc(m.Label))
	case MarkWipe:
		w.f(`<g class="wipe"><circle cx="%s" cy="%s" r="11" fill="#ffffff" fill-opacity="0.8" stroke="#111111" stroke-width="3"/>`+
			`<text x="%s" y="%s" font-size="11" font-weight="bold" text-anchor="middle" fill="#111111">W</text><title>%s</title></g>`+"\n",
			f1(x), f1(y), f1(x), f1(y+4), esc(title))
	case MarkDoor:
		letter := "D"
		switch {
		case strings.Contains(m.Label, "[button]"):
			letter = "L"
		case strings.Contains(m.Label, "[goober]"):
			letter = "G"
		}
		w.f(`<g class="door"><rect x="%s" y="%s" width="12" height="12" fill="%s" stroke="#ffffff" stroke-width="1"/>`+
			`<text x="%s" y="%s" font-size="9" font-weight="bold" text-anchor="middle" fill="#ffffff">%s</text><title>%s</title></g>`+"\n",
			f1(x-6), f1(y-6), colDoor, f1(x), f1(y+3.5), letter, esc(title))
	case MarkTeleport, MarkGM:
		if m.From != nil {
			fx, fy := s.px(m.From.X, m.From.Y)
			w.f(`<path class="teleport" d="M%s %sL%s %s" fill="none" stroke="%s" stroke-width="1.4" stroke-dasharray="6 4" marker-end="url(#arrow)"/>`+"\n", f1(fx), f1(fy), f1(x), f1(y), colTeleport)
		}
		w.f(`<g class="teleport"><path d="M%s %sl7 12h-14z" fill="%s" stroke="#ffffff" stroke-width="1"/><text x="%s" y="%s" font-size="8" font-weight="bold" text-anchor="middle" fill="#ffffff">%s</text><title>%s</title></g>`+"\n",
			f1(x), f1(y-7), colTeleport, f1(x), f1(y+3.5), map[bool]string{true: "GM", false: "T"}[m.Flag], esc(title))
	case MarkLeg:
		w.f(`<g class="leg"><path d="M%s %sv-16" stroke="%s" stroke-width="1.5"/><path d="M%s %sh10l-3 4l3 4h-10z" fill="%s"/>`+
			`<text x="%s" y="%s" font-size="9" fill="%s" stroke="#ffffff" stroke-width="2.5" paint-order="stroke">%s</text><title>%s</title></g>`+"\n",
			f1(x), f1(y), colText, f1(x), f1(y-16), colPlan, f1(x+13), f1(y-9), colText, esc(m.Label), esc(title))
	case MarkBossKill:
		w.f(`<circle class="boss-kill" cx="%s" cy="%s" r="14" fill="none" stroke="%s" stroke-width="2"><title>%s</title></circle>`+"\n", f1(x), f1(y), colBoss, esc(title))
	}
}

// pullNumber is the number in a label such as "pull 3: Gorm".
func pullNumber(label string) string {
	rest := strings.TrimPrefix(label, "pull ")
	if i := strings.IndexAny(rest, ": "); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// writeScale draws the scale bar and the north arrow under the map's corner.
func (s *Scene) writeScale(w *svgw, mx, my float64) {
	// A round length that spans about a fifth of the map's width.
	want := float64(s.Box.Width()) / 5
	length := 1.0
	for _, v := range []float64{1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000} {
		if v <= want {
			length = v
		}
	}
	x0, y0 := mx+12, my+s.H-14
	px := length * s.Scale
	w.f(`<g class="scale"><rect x="%s" y="%s" width="%s" height="22" fill="#ffffff" fill-opacity="0.8"/>`+
		`<path d="M%s %sh%sM%s %sv-5M%s %sv-5" stroke="%s" stroke-width="2"/>`+
		`<text x="%s" y="%s" font-size="10" fill="%s">%.0f yd</text></g>`+"\n",
		f1(x0-6), f1(y0-16), f1(px+66), f1(x0), f1(y0), f1(px), f1(x0), f1(y0), f1(x0+px), f1(y0), colText, f1(x0+px+6), f1(y0), colText, length)
	nx, ny := mx+s.W-22, my+30
	w.f(`<g class="north"><path d="M%s %sl5 14h-10z" fill="%s"/><text x="%s" y="%s" font-size="11" font-weight="bold" text-anchor="middle" fill="%s">N</text></g>`+"\n",
		f1(nx), f1(ny-14), colText, f1(nx), f1(ny+12), colText)
}
