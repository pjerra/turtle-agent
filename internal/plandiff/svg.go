package plandiff

import (
	"encoding/base64"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/pjerra/turtle-agent/internal/minimap"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// TwoFloorDesc is the <desc> of an overlay on a map whose minimaps folder has a second floor picture
// (<map>.worldmap-2f.json): the drawing is 2D and z decides where a point belongs.
const TwoFloorDesc = "2D overlay: z decides on multi-floor maps"

// SVG draws the diff over the map's picture: the old route dashed grey, the new route solid, one marker per change. A nil or
// unusable picture gives a blank canvas around the plan's points (2 px/yd). The image is the picture's PNG, if any.
func SVG(d Diff, oldRows, newRows planexport.Rows, pic *minimap.Meta, png []byte) []byte {
	return renderSVG(d, oldRows, newRows, pic, png, false)
}

// SVG2F is SVG for a map whose minimaps folder has <map>.worldmap-2f.json: the file carries TwoFloorDesc.
func SVG2F(d Diff, oldRows, newRows planexport.Rows, pic *minimap.Meta, png []byte) []byte {
	return renderSVG(d, oldRows, newRows, pic, png, true)
}

func renderSVG(d Diff, oldRows, newRows planexport.Rows, pic *minimap.Meta, png []byte, twoFloor bool) []byte {
	m := usableMeta(pic)
	if m == nil {
		b := boundsOf(oldRows, newRows)
		m = &b
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	b.WriteString("\n")
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d">`, m.Width, m.Height, m.Width, m.Height)
	b.WriteString("\n")
	if twoFloor {
		fmt.Fprintf(&b, "<desc>%s</desc>\n", xmlEscape(TwoFloorDesc))
	}
	b.WriteString(`<defs><marker id="plandiff-arrow" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto"><path d="M0,0 L8,4 L0,8 Z" fill="#ef6c00"/></marker></defs>`)
	b.WriteString("\n")
	if len(png) > 0 {
		fmt.Fprintf(&b, `<image x="0" y="0" width="%d" height="%d" href="data:image/png;base64,%s"/>`, m.Width, m.Height, base64.StdEncoding.EncodeToString(png))
		b.WriteString("\n")
	}
	writeRoute(&b, "old", oldRows, m, "#9e9e9e", 1.5, ` stroke-dasharray="4 3"`)
	writeRoute(&b, "new", newRows, m, "#1565c0", 2, "")
	for _, c := range d.Changes {
		writeMarker(&b, m, c)
	}
	b.WriteString("</svg>\n")
	return []byte(b.String())
}

func usableMeta(m *minimap.Meta) *minimap.Meta {
	if m == nil || m.Width <= 0 || m.Height <= 0 || m.PxPerYard <= 0 {
		return nil
	}
	return m
}

// boundsOf builds a canvas around every positioned point of both routes, 20 yd of margin at 2 px/yd.
func boundsOf(rows ...planexport.Rows) minimap.Meta {
	minX, maxX := math.Inf(1), math.Inf(-1)
	minY, maxY := math.Inf(1), math.Inf(-1)
	any := false
	for _, r := range rows {
		for _, s := range r.Steps {
			if s.X == 0 && s.Y == 0 && s.Z == 0 {
				continue
			}
			any = true
			minX, maxX = math.Min(minX, float64(s.X)), math.Max(maxX, float64(s.X))
			minY, maxY = math.Min(minY, float64(s.Y)), math.Max(maxY, float64(s.Y))
		}
	}
	if !any {
		minX, maxX, minY, maxY = 0, 100, 0, 100
	}
	const pad, pxPerYard = 20.0, 2.0
	m := minimap.Meta{
		PxPerYard: pxPerYard,
		North:     maxX + pad,
		South:     minX - pad,
		West:      maxY + pad,
		East:      minY - pad,
	}
	m.Width = int(math.Ceil((m.West - m.East) * pxPerYard))
	m.Height = int(math.Ceil((m.North - m.South) * pxPerYard))
	if m.Width < 1 {
		m.Width = 1
	}
	if m.Height < 1 {
		m.Height = 1
	}
	return m
}

func writeRoute(b *strings.Builder, class string, rows planexport.Rows, m *minimap.Meta, color string, width float64, extra string) {
	steps := append([]planexport.StepRow{}, rows.Steps...)
	sort.SliceStable(steps, func(i, j int) bool {
		if steps[i].Leg != steps[j].Leg {
			return steps[i].Leg < steps[j].Leg
		}
		return steps[i].Seq < steps[j].Seq
	})
	var pts []string
	for _, s := range steps {
		if s.X == 0 && s.Y == 0 && s.Z == 0 {
			continue
		}
		px, py := m.ToPixel(float64(s.X), float64(s.Y))
		pts = append(pts, coord(px)+","+coord(py))
	}
	if len(pts) == 0 {
		return
	}
	fmt.Fprintf(b, `<polyline class="%s" points="%s" fill="none" stroke="%s" stroke-width="%s"%s/>`, class,
		strings.Join(pts, " "), color, coord(width), extra)
	b.WriteString("\n")
}

func writeMarker(b *strings.Builder, m *minimap.Meta, c Change) {
	switch c.Type {
	case TypeMoved:
		if c.Old == nil || c.New == nil {
			return
		}
		x1, y1 := m.ToPixel(float64(c.Old.X), float64(c.Old.Y))
		x2, y2 := m.ToPixel(float64(c.New.X), float64(c.New.Y))
		wrapTitle(b, c)
		if math.Hypot(x2-x1, y2-y1) < 0.5 {
			// A move in z only: the 2D overlay has no direction to point, so the marker is a circle and the label's z says the rest.
			fmt.Fprintf(b, `<circle class="moved" cx="%s" cy="%s" r="5" fill="none" stroke="#ef6c00" stroke-width="2"/>`, coord(x2), coord(y2))
		} else {
			fmt.Fprintf(b, `<line class="moved" x1="%s" y1="%s" x2="%s" y2="%s" stroke="#ef6c00" stroke-width="2" marker-end="url(#plandiff-arrow)"/>`,
				coord(x1), coord(y1), coord(x2), coord(y2))
		}
		writeLabel(b, x2, y2, c, c.New)
		b.WriteString("</g>\n")
	case TypeAdded:
		if c.New == nil {
			return
		}
		x, y := m.ToPixel(float64(c.New.X), float64(c.New.Y))
		wrapTitle(b, c)
		fmt.Fprintf(b, `<circle class="added" cx="%s" cy="%s" r="5" fill="#2e7d32"/>`, coord(x), coord(y))
		writeLabel(b, x, y, c, c.New)
		b.WriteString("</g>\n")
	case TypeRemoved:
		if c.Old == nil {
			return
		}
		x, y := m.ToPixel(float64(c.Old.X), float64(c.Old.Y))
		wrapTitle(b, c)
		fmt.Fprintf(b, `<g class="cross"><line x1="%s" y1="%s" x2="%s" y2="%s" stroke="#c62828" stroke-width="2"/><line x1="%s" y1="%s" x2="%s" y2="%s" stroke="#c62828" stroke-width="2"/></g>`,
			coord(x-4), coord(y-4), coord(x+4), coord(y+4), coord(x-4), coord(y+4), coord(x+4), coord(y-4))
		writeLabel(b, x, y, c, c.Old)
		b.WriteString("</g>\n")
	case TypeChanged:
		s := c.New
		if s == nil {
			s = c.Old
		}
		if s == nil {
			return
		}
		x, y := m.ToPixel(float64(s.X), float64(s.Y))
		wrapTitle(b, c)
		fmt.Fprintf(b, `<circle class="changed" cx="%s" cy="%s" r="5" fill="none" stroke="#7b1fa2" stroke-width="2"/>`, coord(x), coord(y))
		writeLabel(b, x, y, c, s)
		b.WriteString("</g>\n")
	}
}

func wrapTitle(b *strings.Builder, c Change) {
	b.WriteString("<g>")
	fmt.Fprintf(b, "<title>%s</title>", xmlEscape(changeText(c)))
}

func writeLabel(b *strings.Builder, x, y float64, c Change, s *planexport.StepRow) {
	n := c.NewStep
	if c.Type == TypeRemoved {
		n = c.OldStep
	}
	fmt.Fprintf(b, `<text x="%s" y="%s" font-size="11" font-family="sans-serif" fill="#111111">%d z %s</text>`,
		coord(x+6), coord(y-6), n, coord(float64(s.Z)))
}

func coord(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }

// xmlEscape escapes the text of an SVG element or attribute.
func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace(s)
}
