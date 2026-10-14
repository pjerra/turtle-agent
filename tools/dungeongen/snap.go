package main

// Snapping: a draft plan's points come from spawn positions, and a spawn may stand where the navmesh has no floor (a mob on a ledge, in a wall
// niche, over a gap). snaps.json (next to plans.json) maps such a point to the nearest navmesh floor on the same level, or marks it needs_walk
// when there is none within snapMaxH yards. The generator applies the table, so it stays deterministic and runs without navmesh files;
// `dungeongen -spec plans.json -snap-update -mmaps-root ~/tb-ci` rebuilds the table from the navmesh (maps without files keep their entries).

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
)

const (
	snapMaxH = 5.0 // yards on the ground plane a point may move
	snapMaxZ = 3.0 // yards up or down: the same floor
	// A point is on the mesh when the mesh's nearest floor is within meshOKZ up or down and meshOKH sideways (the plans test's rule).
	meshOKZ = 3.0
	meshOKH = 1.0
	// A pull position is a creature's, and a creature may stand off the mesh a little: it only needs snapping beyond these.
	pullOKZ = 4.0
	pullOKH = 2.5
)

// Snap is one entry of snaps.json: the generated point, and the mesh point it becomes or needs_walk.
type Snap struct {
	From      dungeon.Point  `json:"from"`
	To        *dungeon.Point `json:"to,omitempty"`
	NeedsWalk bool           `json:"needs_walk,omitempty"`
}

// Snaps is snaps.json: plan name to its entries.
type Snaps map[string][]Snap

func loadSnaps(path string) (Snaps, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Snaps{}, nil
	}
	if err != nil {
		return nil, err
	}
	s := Snaps{}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

func (s Snaps) write(path string) error {
	var names []string
	for n := range s {
		names = append(names, n)
	}
	sort.Strings(names)
	var b bytes.Buffer
	b.WriteString("{\n")
	for i, n := range names {
		fmt.Fprintf(&b, "  %q: [\n", n)
		for j, e := range s[n] {
			line, err := json.Marshal(e)
			if err != nil {
				return err
			}
			sep := ","
			if j == len(s[n])-1 {
				sep = ""
			}
			fmt.Fprintf(&b, "    %s%s\n", line, sep)
		}
		sep := ","
		if i == len(names)-1 {
			sep = ""
		}
		fmt.Fprintf(&b, "  ]%s\n", sep)
	}
	b.WriteString("}\n")
	return os.WriteFile(path, b.Bytes(), 0o644)
}

// planPointRefs calls f on a pointer to every point of the plan the sweep looks at, with a flag that says whether it is a pull position and
// the setter of its needs_walk mark.
func planPointRefs(p *dungeon.Plan, f func(pt *dungeon.Point, pull bool, mark func())) {
	for i := range p.Legs {
		l := &p.Legs[i]
		for j := range l.Via {
			f(&l.Via[j].Point, false, func() { l.NeedsWalk = true })
		}
		f(&l.To, false, func() { l.NeedsWalk = true })
		for j := range l.PullGroups {
			g := &l.PullGroups[j]
			for k := range g.Positions {
				f(&g.Positions[k], true, func() { g.NeedsWalk = true })
			}
		}
		if l.Boss != nil {
			f(&l.Boss.Position, false, func() { l.Boss.NeedsWalk = true })
		}
	}
}

// ApplySnaps moves the points of p that the table lists and marks the ones it gives up on. It returns how many it moved and marked.
func ApplySnaps(p *dungeon.Plan, es []Snap) (moved, marked int) {
	by := map[dungeon.Point]Snap{}
	for _, e := range es {
		by[e.From] = e
	}
	planPointRefs(p, func(pt *dungeon.Point, _ bool, mark func()) {
		e, ok := by[*pt]
		switch {
		case !ok:
		case e.NeedsWalk:
			mark()
			marked++
		case e.To != nil:
			*pt = *e.To
			moved++
		}
	})
	return moved, marked
}

// onMesh says whether the mesh has a floor under pt within the tolerance.
func onMesh(pl *navmesh.Planner, m uint32, pt dungeon.Point, maxZ, maxH float64) (bool, error) {
	n, ok, err := pl.Nearest(m, navmesh.Vec3{pt.X, pt.Y, pt.Z})
	if err != nil || !ok {
		return false, err
	}
	return math.Abs(float64(n[2]-pt.Z)) <= maxZ && math.Hypot(float64(n[0]-pt.X), float64(n[1]-pt.Y)) <= maxH, nil
}

// snapPoint finds the walkable point nearest to pt on the same floor: within snapMaxH on the ground plane and snapMaxZ vertically, preferring
// the floor at or below the point (a floor above is taken only when none is below). ok false: nothing sensible within reach.
func snapPoint(pl *navmesh.Planner, m uint32, pt dungeon.Point) (dungeon.Point, bool, error) {
	polys, _, err := pl.Polygons(m, pt.X-snapMaxH-1, pt.Y-snapMaxH-1, pt.X+snapMaxH+1, pt.Y+snapMaxH+1)
	if err != nil {
		return dungeon.Point{}, false, err
	}
	type cand struct {
		x, y float64
		d    float64
	}
	var cs []cand
	for _, poly := range polys {
		if poly.Flags&navmesh.NavGround == 0 || len(poly.Verts) < 3 {
			continue
		}
		x, y, d := closestInPoly(poly.Verts, float64(pt.X), float64(pt.Y))
		if d <= snapMaxH {
			cs = append(cs, cand{x, y, d})
		}
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].d < cs[j].d })
	for _, c := range cs {
		fl, err := pl.Floors(m, float32(c.x), float32(c.y), pt.Z-snapMaxZ, pt.Z+snapMaxZ)
		if err != nil {
			return dungeon.Point{}, false, err
		}
		z, found := float32(0), false
		for _, f := range fl { // highest first
			if f.Flags&navmesh.NavGround == 0 {
				continue
			}
			if f.Z <= pt.Z+0.5 {
				z, found = f.Z, true
				break
			}
		}
		if !found { // nothing at or below: the lowest floor above
			for i := len(fl) - 1; i >= 0; i-- {
				if fl[i].Flags&navmesh.NavGround != 0 {
					z, found = fl[i].Z, true
					break
				}
			}
		}
		if !found {
			continue
		}
		out := dungeon.Point{X: round2(c.x), Y: round2(c.y), Z: round2(float64(z))}
		if ok, err := onMesh(pl, m, out, meshOKZ, meshOKH); err != nil {
			return dungeon.Point{}, false, err
		} else if ok {
			return out, true, nil
		}
	}
	return dungeon.Point{}, false, nil
}

func round2(v float64) float32 { return float32(math.Round(v*100) / 100) }

// closestInPoly is the point of the polygon (on the ground plane) nearest to (px, py), moved 0.4 yd inside so that it is not on an edge, and its
// distance from (px, py).
func closestInPoly(vs []navmesh.Vec3, px, py float64) (x, y, d float64) {
	var cx, cy float64
	for _, v := range vs {
		cx += float64(v[0])
		cy += float64(v[1])
	}
	cx /= float64(len(vs))
	cy /= float64(len(vs))
	inside := true
	bx, by, bd := px, py, math.MaxFloat64
	for i := range vs {
		a, b := vs[i], vs[(i+1)%len(vs)]
		ax, ay, ex, ey := float64(a[0]), float64(a[1]), float64(b[0])-float64(a[0]), float64(b[1])-float64(a[1])
		if ex*(py-ay)-ey*(px-ax) < 0 { // right of an edge of a counter-clockwise polygon (or the other way for clockwise: both tried below)
			inside = false
		}
		t := 0.0
		if l := ex*ex + ey*ey; l > 0 {
			t = math.Max(0, math.Min(1, ((px-ax)*ex+(py-ay)*ey)/l))
		}
		qx, qy := ax+t*ex, ay+t*ey
		if dd := math.Hypot(px-qx, py-qy); dd < bd {
			bx, by, bd = qx, qy, dd
		}
	}
	// The winding is not known: a point inside is on the same side of every edge.
	if !inside {
		inside = true
		for i := range vs {
			a, b := vs[i], vs[(i+1)%len(vs)]
			if (float64(b[0])-float64(a[0]))*(py-float64(a[1]))-(float64(b[1])-float64(a[1]))*(px-float64(a[0])) > 0 {
				inside = false
			}
		}
	}
	if inside {
		return px, py, 0
	}
	if dc := math.Hypot(cx-bx, cy-by); dc > 0 {
		s := math.Min(0.4, dc) / dc
		bx, by = bx+(cx-bx)*s, by+(cy-by)*s
	}
	return bx, by, bd
}

func mmapsFor(root string, m uint32) string {
	d := filepath.Join(root, fmt.Sprintf("mmaps-turtle%03d", m))
	if _, err := os.Stat(filepath.Join(d, fmt.Sprintf("%03d.mmap", m))); err != nil {
		return ""
	}
	return d
}

// UpdateSnaps rebuilds the table for every generated plan whose map has navmesh files under root: it generates the plan without snaps, looks at
// each point and records the moves. The other plans keep their entries. It returns the lines of a report.
func UpdateSnaps(sf *SpecFile, dataRoot, root string) (Snaps, []string, error) {
	out := Snaps{}
	for k, v := range sf.Snaps {
		out[k] = v
	}
	var report []string
	for _, ps := range sf.Plans {
		if ps.Handwritten {
			continue
		}
		dir := mmapsFor(root, ps.Map)
		if dir == "" {
			report = append(report, fmt.Sprintf("%s: no navmesh files for map %d, entries kept", ps.Name, ps.Map))
			continue
		}
		p, err := GenerateSpec(ps, dataRoot)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", ps.Name, err)
		}
		pl := navmesh.NewPlanner(dir)
		var es []Snap
		seen := map[dungeon.Point]bool{}
		var ferr error
		planPointRefs(p, func(pt *dungeon.Point, pull bool, _ func()) {
			if ferr != nil || seen[*pt] {
				return
			}
			maxZ, maxH := meshOKZ, meshOKH
			if pull {
				maxZ, maxH = pullOKZ, pullOKH
			}
			ok, err := onMesh(pl, ps.Map, *pt, maxZ, maxH)
			if err != nil || ok {
				ferr = err
				return
			}
			seen[*pt] = true
			to, found, err := snapPoint(pl, ps.Map, *pt)
			if err != nil {
				ferr = err
				return
			}
			if found {
				es = append(es, Snap{From: *pt, To: &to})
			} else {
				es = append(es, Snap{From: *pt, NeedsWalk: true})
			}
		})
		pl.Close()
		if ferr != nil {
			return nil, nil, fmt.Errorf("%s: %w", ps.Name, ferr)
		}
		moved, marked := 0, 0
		for _, e := range es {
			if e.NeedsWalk {
				marked++
			} else {
				moved++
			}
		}
		if len(es) == 0 {
			delete(out, ps.Name)
		} else {
			out[ps.Name] = es
		}
		report = append(report, fmt.Sprintf("%s: %d snapped, %d needs_walk", ps.Name, moved, marked))
	}
	return out, report, nil
}
