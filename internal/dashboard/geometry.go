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
	// NeedsWalk: the plan marks the point needs_walk (never snapped to the navmesh; its mapping walk is still to do). CheckSkip is the
	// reason the plan exempts the hop to a via point from the plan check.
	NeedsWalk bool   `json:"needs_walk,omitempty"`
	CheckSkip string `json:"check_skip,omitempty"`
	// GMCross: a GM moves the party to this via point instead of walking (Waypoint.GMCross); the route editor never moves or deletes it.
	GMCross bool `json:"gm_cross,omitempty"`
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
	// Looks are the computed places the lead bot stands to look at each pack (and where the party is gathered for it); Marks are the area
	// triggers, gossip NPCs and stand points of the steps.
	Looks []GeoLook `json:"looks,omitempty"`
	Marks []GeoMark `json:"marks,omitempty"`
}

// GeoLook is the computed look point of one pull group: a straight line from where the lead bot comes from, with the driver's default
// look. The real walk follows the navmesh, so the page labels it "computed". Gather is the point the party is gathered at for a run that
// starts at the group.
type GeoLook struct {
	Group  int     `json:"group"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Gather *GeoXY  `json:"gather,omitempty"`
	Label  string  `json:"label"`
}

// GeoMark is a step's area trigger (Kind "trigger": centre and zone radius R), gossip NPC ("gossip") or stand point ("stand": where the party
// or the talker stands; Link is the object or NPC it is for).
type GeoMark struct {
	Kind  string  `json:"kind"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	R     float64 `json:"r,omitempty"`
	Label string  `json:"label"`
	Step  int     `json:"step"`
	After bool    `json:"after,omitempty"`
	Link  *GeoXY  `json:"link,omitempty"`
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
	Words   string     `json:"words,omitempty"` // the creatures in plain words: "2 Stonevault Oracles, Geologist"
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

// packWords lists the creatures of a pull group in plain words, in the order they first appear: "2 Stonevault Oracles, Geologist". A creature
// the plan's notes do not name is "creature <entry>".
func packWords(pg dungeon.PullGroup, names map[string]string) string {
	var order []uint32
	count := map[uint32]int{}
	for _, e := range pg.Entries {
		if count[e] == 0 {
			order = append(order, e)
		}
		count[e]++
	}
	var parts []string
	for _, e := range order {
		n, k := names[strconv.FormatUint(uint64(e), 10)], count[e]
		switch {
		case n == "" && k == 1:
			parts = append(parts, fmt.Sprintf("creature %d", e))
		case n == "":
			parts = append(parts, fmt.Sprintf("%d x creature %d", k, e))
		case k == 1:
			parts = append(parts, n)
		case strings.HasSuffix(n, "s"):
			parts = append(parts, fmt.Sprintf("%d %s", k, n))
		default:
			parts = append(parts, fmt.Sprintf("%d %ss", k, n))
		}
	}
	return strings.Join(parts, ", ")
}

// LegStops is the planned route of leg i from (startX, startY), in the order the driver does it (plan.go Leg, plancheck.go checkLeg): the
// via points, the leg's `to` (kind "stage": where the party gathers before the packs), the first position of every pull group, the steps'
// object places, the boss, and the steps after the boss. The route line itself is the start, the via points and the stage point; the packs,
// the steps and the boss are marks off that line.
func LegStops(leg dungeon.Leg, i int, startX, startY float64, names map[string]string) []GeoWaypoint {
	label := legLabel(leg.Name, i)
	out := []GeoWaypoint{{X: startX, Y: startY, Kind: "start", Label: "start of " + label}}
	add := func(x, y float64, kind, text string, direct bool) {
		out = append(out, GeoWaypoint{X: x, Y: y, Kind: kind, Label: text, N: len(out), Direct: direct})
	}
	for _, w := range leg.Via {
		x, y := Project(w.X, w.Y)
		add(x, y, "via", "waypoint", w.Direct)
		out[len(out)-1].CheckSkip = w.CheckSkip
		out[len(out)-1].GMCross = w.GMCross
	}
	x, y := Project(leg.To.X, leg.To.Y)
	add(x, y, "stage", "stage point of "+label+" (the party gathers here before the packs)", false)
	out[len(out)-1].NeedsWalk = leg.NeedsWalk
	for k, pg := range leg.PullGroups {
		if len(pg.Positions) > 0 {
			x, y := Project(pg.Positions[0].X, pg.Positions[0].Y)
			add(x, y, "pull", packLabel(k, pg, names), false)
			out[len(out)-1].Group = k
			out[len(out)-1].NeedsWalk = pg.NeedsWalk
		}
	}
	steps := func(after bool) {
		for _, p := range stepPoints(leg) {
			if p.after != after {
				continue
			}
			x, y := Project(p.pt.X, p.pt.Y)
			text := p.label
			if after {
				text = "after the boss: " + text
			}
			add(x, y, "step", text, false)
			out[len(out)-1].Group = p.step
		}
	}
	steps(false)
	if b := leg.Boss; b != nil {
		x, y := Project(b.Position.X, b.Position.Y)
		text := fmt.Sprintf("boss %d", b.Entry)
		if n := names[strconv.FormatUint(uint64(b.Entry), 10)]; n != "" {
			text = "boss " + n
		}
		add(x, y, "boss", text, false)
		out[len(out)-1].NeedsWalk = b.NeedsWalk
	}
	steps(true)
	return out
}

// LegExit is where the party stands when leg ends and the next leg starts from: the boss, else the last position of the last pull group,
// else the leg's `to`. In the drawing's plane.
func LegExit(leg dungeon.Leg) (x, y float64) {
	if b := leg.Boss; b != nil {
		return Project(b.Position.X, b.Position.Y)
	}
	for k := len(leg.PullGroups) - 1; k >= 0; k-- {
		if pos := leg.PullGroups[k].Positions; len(pos) > 0 {
			return Project(pos[len(pos)-1].X, pos[len(pos)-1].Y)
		}
	}
	return Project(leg.To.X, leg.To.Y)
}

// Geometry is a plan ready to draw: the static SVG layer and the box it fits in.
type Geometry struct {
	Plan    string            `json:"plan"`
	Map     uint32            `json:"map"`
	ViewBox [4]float64        `json:"view_box"` // min x, min y, width, height
	Bounds  [4]float64        `json:"bounds"`   // the same without the margin: the tight box of everything drawn
	Legs    []GeoLeg          `json:"legs"`
	Names   map[string]string `json:"names,omitempty"`  // creature entry (decimal) -> name, as far as the plan's notes name them
	SVG     string            `json:"svg"`              // the inner markup of the <svg>: routes, pull groups, bosses, objects
	Source  *PlanSource       `json:"source,omitempty"` // the file the plan was read from; the plan cache sets it
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
			if st.Kind != "start" && st.Kind != "via" && st.Kind != "stage" {
				continue // the packs, steps and boss are marks off the route line
			}
			// a stop on the same spot as the one before is the same point on the line
			if n := len(gl.Path); n > 0 && math.Hypot(gl.Path[n-1].X-st.X, gl.Path[n-1].Y-st.Y) < 0.5 {
				continue
			}
			gl.Path = append(gl.Path, GeoPoint{X: st.X, Y: st.Y, Direct: st.Direct && st.Kind == "via"})
		}
		toX, toY := LegExit(leg)
		fmt.Fprintf(&sb, `<g class="leg" data-leg="%d">`, i)
		writeRoute(&sb, i, gl.Path)
		for _, st := range gl.Stops {
			if st.Kind != "start" {
				fmt.Fprintf(&sb, `<g class="mk stepn" data-leg="%d" data-x="%.1f" data-y="%.1f">%s</g>`, i, st.X, st.Y, label(strconv.Itoa(st.N), 10, -8, "lt"))
			}
			if st.Kind == "via" || st.Kind == "stage" {
				fmt.Fprintf(&sb, `<g class="mk wp %s%s" data-leg="%d" data-x="%.1f" data-y="%.1f"><circle r="3.5"/><title>%s</title></g>`,
					st.Kind, flagClasses(st), i, st.X, st.Y, html.EscapeString(fmt.Sprintf("%s, step %d of %d: %s", gl.Label, st.N, len(gl.Stops)-1, st.Label)+flagText(st)))
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
				fmt.Fprintf(&sb, `<g class="mk pin%s" data-leg="%d" data-group="%d" data-x="%.1f" data-y="%.1f"><circle r="9"/><text class="num" y="4">%d</text><title>%s</title></g>`, flagClasses(GeoWaypoint{NeedsWalk: pg.NeedsWalk}), i, k, x, y, k+1,
					html.EscapeString(fmt.Sprintf("%s, step %d of %d: %s", gl.Label, stopOf(gl.Stops, k), len(gl.Stops)-1, packLabel(k, pg, names))+flagText(GeoWaypoint{NeedsWalk: pg.NeedsWalk})))
			}
		}
		for k, pg := range leg.PullGroups {
			if len(pg.Positions) == 0 {
				continue
			}
			x, y := Project(pg.Positions[0].X, pg.Positions[0].Y)
			gg := GeoGroup{Index: k, X: round1(x), Y: round1(y), R: groupRadius(pg), N: len(pg.Entries), Ambush: pg.Ambush, Label: packLabel(k, pg, names), Words: packWords(pg, names), Entries: pg.Entries}
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
			fmt.Fprintf(&sb, `<g class="mk boss%s" data-leg="%d" data-x="%.1f" data-y="%.1f"><polygon points="0,-14 14,0 0,14 -14,0"/><path d="M-5,-4 L5,4 M5,-4 L-5,4"/>%s</g>`,
				flagClasses(GeoWaypoint{NeedsWalk: b.NeedsWalk}), i, x, y, label(stopLabel(gl.Stops, "boss", fmt.Sprintf("boss %d", b.Entry)), 18, 5, "lt"))
		}
		for _, p := range stepPoints(leg) {
			x, y := Project(p.pt.X, p.pt.Y)
			bb.add(x, y, 2)
			gl.Objects = append(gl.Objects, GeoObject{Kind: p.kind, X: round1(x), Y: round1(y), Entry: p.entry, Label: p.label, Step: p.step, After: p.after})
			fmt.Fprintf(&sb, `<g class="mk obj %s" data-leg="%d" data-x="%.1f" data-y="%.1f"><rect class="ic" x="-4" y="-5" width="8" height="10" rx="1"/><circle class="knob" cx="1.5" cy="0" r="1"/>%s</g>`,
				p.kind, i, x, y, label(p.label, 9, 4, "lt"))
		}
		gl.Looks = lookPoints(plan, i)
		for _, lk := range gl.Looks {
			px, py := Project(leg.PullGroups[lk.Group].Positions[0].X, leg.PullGroups[lk.Group].Positions[0].Y)
			bb.add(lk.X, lk.Y, 0)
			fmt.Fprintf(&sb, `<g class="look" data-leg="%d" data-group="%d"><line class="lookline" x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f"/><g class="mk lookpt" data-x="%.1f" data-y="%.1f"><rect x="-4" y="-4" width="8" height="8"/><title>%s</title></g>`,
				i, lk.Group, lk.X, lk.Y, px, py, lk.X, lk.Y, html.EscapeString(lk.Label))
			if lk.Gather != nil {
				bb.add(lk.Gather.X, lk.Gather.Y, 0)
				fmt.Fprintf(&sb, `<g class="mk gatherpt" data-x="%.1f" data-y="%.1f"><polygon points="0,-5 5,4 -5,4"/><title>%s</title></g>`, lk.Gather.X, lk.Gather.Y,
					html.EscapeString(fmt.Sprintf("computed gather spot for pack %d (a run that starts at it gathers the party here; straight line, no navmesh)", lk.Group+1)))
			}
			sb.WriteString(`</g>`)
		}
		gl.Marks = stepMarks(leg)
		for _, m := range gl.Marks {
			bb.add(m.X, m.Y, m.R)
			switch m.Kind {
			case "trigger":
				fmt.Fprintf(&sb, `<circle class="trg" data-leg="%d" cx="%.1f" cy="%.1f" r="%.1f"/>`, i, m.X, m.Y, m.R)
				fmt.Fprintf(&sb, `<g class="mk trgmk" data-leg="%d" data-x="%.1f" data-y="%.1f"><path d="M0,-8 L7,5 L-7,5 Z"/>%s</g>`, i, m.X, m.Y, label(m.Label, 10, 4, "lt"))
			case "stand":
				if m.Link != nil {
					fmt.Fprintf(&sb, `<line class="standline" data-leg="%d" x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f"/>`, i, m.X, m.Y, m.Link.X, m.Link.Y)
				}
				fmt.Fprintf(&sb, `<g class="mk stand" data-leg="%d" data-x="%.1f" data-y="%.1f"><path d="M-4,0 L4,0 M0,-4 L0,4"/><title>%s</title></g>`, i, m.X, m.Y, html.EscapeString(m.Label))
			default:
				fmt.Fprintf(&sb, `<g class="mk gossip" data-leg="%d" data-x="%.1f" data-y="%.1f"><circle r="5"/><text class="num" y="3">?</text>%s</g>`, i, m.X, m.Y, label(m.Label, 9, 4, "lt"))
			}
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

// flagClasses are the extra classes of a route mark that the plan flags: needs_walk (nw) and a check_skip hop (cskip).
func flagClasses(w GeoWaypoint) string {
	c := ""
	if w.NeedsWalk {
		c += " nw"
	}
	if w.CheckSkip != "" {
		c += " cskip"
	}
	if w.GMCross {
		c += " gmx"
	}
	return c
}

// flagText is the sentence a flagged mark adds to its hover text.
func flagText(w GeoWaypoint) string {
	t := ""
	if w.NeedsWalk {
		t += " [needs_walk: not on the navmesh, its mapping walk is still to do]"
	}
	if w.CheckSkip != "" {
		t += " [hop exempt from the plan check: " + w.CheckSkip + "]"
	}
	if w.GMCross {
		t += " [gm_cross: a GM moves the party here]"
	}
	return t
}

// lookPoints are the computed look points of the pull groups of leg i: the lead bot comes from the leg's end for the first pack and from
// the pack before for the others, on a straight line (the page says so), and stands LookStandoff short of the pack unless the line is
// shorter. A group without a position has none.
func lookPoints(plan *dungeon.Plan, i int) []GeoLook {
	leg := plan.Legs[i]
	names := EntryNames(plan)
	var out []GeoLook
	from := leg.To
	for k, pg := range leg.PullGroups {
		if len(pg.Positions) == 0 {
			continue
		}
		pack := pg.Positions[0]
		stand, moved := dungeon.DefaultLookPoint(from, pack)
		if !moved {
			stand = from // the lead bot is within the standoff already and stays where it is
		}
		x, y := Project(stand.X, stand.Y)
		g := plan.DefaultGatherSpot(i, k)
		gx, gy := Project(g.X, g.Y)
		out = append(out, GeoLook{Group: k, X: round1(x), Y: round1(y), Gather: &GeoXY{round1(gx), round1(gy)},
			Label: "computed look point for " + packLabel(k, pg, names) + " (straight line from where the lead comes from; the real one follows the navmesh)"})
		from = pack
	}
	return out
}

// stepMarks are the area triggers (zone and stand point), gossip NPCs and the stand points of use, loot and ritual steps of a leg.
func stepMarks(leg dungeon.Leg) []GeoMark {
	var out []GeoMark
	add := func(steps []dungeon.Step, after bool) {
		for i, s := range steps {
			stand := func(st *dungeon.Point, text string, at dungeon.Point) {
				if st == nil {
					return
				}
				x, y := Project(st.X, st.Y)
				ax, ay := Project(at.X, at.Y)
				out = append(out, GeoMark{Kind: "stand", X: round1(x), Y: round1(y), Label: text, Step: i, After: after, Link: &GeoXY{round1(ax), round1(ay)}})
			}
			switch {
			case s.AreaTrigger != nil:
				a := s.AreaTrigger
				x, y := Project(a.Center.X, a.Center.Y)
				out = append(out, GeoMark{Kind: "trigger", X: round1(x), Y: round1(y), R: float64(a.Radius), Label: fmt.Sprintf("area trigger %d", a.ID), Step: i, After: after})
				stand(&a.Stand, fmt.Sprintf("stand for area trigger %d", a.ID), a.Center)
			case s.Gossip != nil:
				g := s.Gossip
				x, y := Project(g.Near.X, g.Near.Y)
				out = append(out, GeoMark{Kind: "gossip", X: round1(x), Y: round1(y), Label: fmt.Sprintf("talk to %d", g.NPC), Step: i, After: after})
				stand(g.Stand, fmt.Sprintf("stand to talk to %d", g.NPC), g.Near)
			case s.UseObject != nil && s.UseObject.Near != nil:
				stand(s.UseObject.Stand, fmt.Sprintf("stand to use %d", s.UseObject.Entry), *s.UseObject.Near)
			case s.LootObject != nil && s.LootObject.Near != nil:
				stand(s.LootObject.Stand, fmt.Sprintf("stand to loot %d", s.LootObject.Entry), *s.LootObject.Near)
			case s.Ritual != nil:
				stand(s.Ritual.Stand, fmt.Sprintf("stand for ritual %d", s.Ritual.Entry), s.Ritual.Position)
			}
		}
	}
	add(leg.Steps, false)
	add(leg.AfterBoss, true)
	return out
}
