package mapview

import (
	"fmt"
	"math"
	"strings"
)

const rowH = 17

// legendHeight is the height in pixels of the legend, for the image's size.
func (s *Scene) legendHeight() int {
	_, h := s.legend(0, 0, 0, false)
	return int(math.Ceil(h))
}

func (s *Scene) writeLegend(w *svgw, floor int, x, y float64, dynamic bool) {
	body, _ := s.legend(x, y, floor, dynamic)
	w.WriteString(body)
}

// routeLength is the length in yards of the continuous parts of a track (holes and leaps not counted).
func (s *Scene) routeLength(tr Track) float64 {
	n := 0.0
	for _, seg := range s.routeSegments(tr, -1) {
		if seg.kind != "line" {
			continue
		}
		for i := 1; i < len(seg.pts); i++ {
			n += dist2D(seg.pts[i-1], seg.pts[i])
		}
	}
	return n
}

// legend draws the legend with its top left corner at (x, y) and returns it with its height.
func (s *Scene) legend(x, y float64, floor int, dynamic bool) (string, float64) {
	w := &svgw{}
	cy := y
	head := func(t string) {
		cy += 6
		w.f(`<text x="%s" y="%s" font-weight="bold" fill="%s">%s</text>`+"\n", f1(x), f1(cy+10), colText, esc(t))
		cy += rowH
	}
	row := func(glyph, text string) {
		w.f(`<g transform="translate(%s %s)">%s</g><text x="%s" y="%s" fill="%s">%s</text>`+"\n",
			f1(x+11), f1(cy+8), glyph, f1(x+28), f1(cy+12), colText, esc(text))
		cy += rowH
	}
	line := func(col, dash string, wd float64) string {
		return fmt.Sprintf(`<path d="M-10 0H10" stroke="%s" stroke-width="%s" stroke-dasharray="%s" fill="none"/>`, col, f1(wd), dash)
	}
	dot := func(f Fate) string { return fmt.Sprintf(`<circle r="3.2" %s/>`, fateStyle(f)) }

	if len(s.Run.Tracks) > 0 {
		head("Party routes")
		for i, tr := range s.Run.Tracks {
			t := tr.Name
			if tr.Leader {
				t += " (leader)"
			}
			row(line(MemberColor(i), "", map[bool]float64{true: 2.4, false: 1.6}[tr.Leader]), fmt.Sprintf("%s, %.0f yd", t, s.routeLength(tr)))
		}
		if s.Run.Seconds > 0 {
			row(`<circle r="2.6" fill="#555555" stroke="#ffffff"/>`, "time tick every "+fmtTick(tickInterval(s.Run.Seconds)))
		}
		if s.Run.Gaps > 0 {
			row(line("#555555", "1 4", 1.4), "no position for a while")
		}
	}
	counts := s.fateCounts()
	if len(s.Run.Creatures) > 0 {
		head("Hostile creatures")
		for _, f := range []Fate{FateKilled, FateSkipped, FateEvaded, FateAlive} {
			if counts[f] == 0 {
				continue
			}
			text := map[Fate]string{FateKilled: "killed", FateSkipped: "skipped (never fought)",
				FateEvaded: "evaded (reset)", FateAlive: "fought, left alive"}[f]
			row(dot(f), fmt.Sprintf("%s: %d", text, counts[f]))
		}
		bosses, packs := 0, map[int]bool{}
		for _, c := range s.Run.Creatures {
			if c.Boss {
				bosses++
			}
			if c.Pack > 0 {
				packs[c.Pack] = true
			}
		}
		if len(packs) > 0 {
			row(`<circle r="6" fill="none" stroke="#777777" stroke-dasharray="3 3"/>`, fmt.Sprintf("pack: %d %s", len(packs), plural(len(packs), "group", "groups")))
		}
		if bosses > 0 {
			row(fmt.Sprintf(`<path d="%s" fill="%s" stroke="%s" stroke-width="1.5"/>`, starPath(0, 0, 8, 3.6), colKilled, colBoss), fmt.Sprintf("boss: %d", bosses))
		}
	}
	kinds := map[string]int{}
	flagged := 0
	for _, m := range s.Run.Markers {
		kinds[m.Kind]++
		if m.Flag {
			flagged++
		}
	}
	if len(kinds) > 0 {
		head("Events")
		if n := kinds[MarkPull]; n > 0 {
			row(fmt.Sprintf(`<path d="M0 -6l6 6l-6 6l-6 -6z" fill="#ffffff" stroke="%s" stroke-width="1.6"/>`, colEvaded), fmt.Sprintf("pull (first combat): %d", n))
		}
		if n := kinds[MarkDeath]; n > 0 {
			row(`<path d="M-5 -5L5 5M-5 5L5 -5" stroke="#111111" stroke-width="2.4"/>`, fmt.Sprintf("death: %d", n))
		}
		if n := kinds[MarkWipe]; n > 0 {
			row(`<circle r="7" fill="#ffffff" stroke="#111111" stroke-width="2.4"/>`, fmt.Sprintf("wipe: %d", n))
		}
		if n := kinds[MarkDoor]; n > 0 {
			row(fmt.Sprintf(`<rect x="-6" y="-6" width="12" height="12" fill="%s"/>`, colDoor), fmt.Sprintf("door or lever used (D, L, G): %d", n))
		}
		if n := kinds[MarkTeleport] + kinds[MarkGM]; n > 0 {
			row(fmt.Sprintf(`<path d="M0 -7l7 12h-14z" fill="%s"/>`, colTeleport), fmt.Sprintf("teleport (T) or GM shortcut (GM): %d", n))
		}
		if n := kinds[MarkLeg]; n > 0 {
			row(fmt.Sprintf(`<path d="M-5 6v-14h9l-3 4l3 4h-9" fill="%s" stroke="%s"/>`, colPlan, colText), fmt.Sprintf("leg of the plan began: %d", n))
		}
	}
	if o := s.Overlay; o != nil {
		head("Plan: " + o.Name)
		row(line(colPlan, "7 4", 2), "planned walk through the legs")
		row(fmt.Sprintf(`<path d="M0 -5l4 5l-4 5l-4 -5z" fill="none" stroke="%s" stroke-width="1.3"/>`, colPlan), "planned pull group position")
		if len(o.Bosses) > 0 {
			row(fmt.Sprintf(`<path d="%s" fill="none" stroke="%s" stroke-width="1.5"/>`, starPath(0, 0, 8, 3.6), colPlan), "planned boss")
		}
	}
	if s.Terrain {
		head("Floor (navmesh)")
		row(fmt.Sprintf(`<rect x="-8" y="-5" width="16" height="10" fill="%s" stroke="%s"/>`, colGround, colGroundLn), "walkable ground")
		hasWater := false
		for _, f := range s.Floors {
			for _, p := range f.Polys {
				if terrainClass(p.Flags) == "water" {
					hasWater = true
				}
			}
		}
		if hasWater {
			row(fmt.Sprintf(`<rect x="-8" y="-5" width="16" height="10" fill="%s"/>`, colWater), "water")
		}
	}
	if len(s.Notes) > 0 {
		head("Notes")
		for _, n := range s.Notes {
			for i, l := range wrapText(n, 44) {
				pre := ""
				if i == 0 {
					pre = "- "
				} else {
					pre = "  "
				}
				w.f(`<text x="%s" y="%s" fill="%s" font-size="10">%s</text>`+"\n", f1(x), f1(cy+10), colMuted, esc(pre+l))
				cy += 13
			}
		}
	}
	_ = dynamic
	_ = floor
	return w.String(), cy - y
}

// wrapText breaks text at spaces into lines of at most width characters.
func wrapText(s string, width int) []string {
	var out []string
	line := ""
	for _, word := range strings.Fields(s) {
		if line != "" && len(line)+1+len(word) > width {
			out = append(out, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += word
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}
