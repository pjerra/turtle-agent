// Package planprobe measures the run leader's exported rows of one dungeon plan: every hop between two positioned rows is
// replayed on the navmesh with the server's own query and with the offline planner, the row's floor is looked up, and the
// walk's surface (water, lava, steep steps, drops) is sampled along the path. A plan-probe report says where a row sits under
// or over the floor, where the server refuses a hop, and where the walk crosses water, lava or a cliff; it is the static
// counterpart of plan-check (which follows the agent's driver) for the rows TortoiseBots' run leader walks (README,
// "Plan probe").
//
// The package is pure over the Nav interface: the command gives it a navmesh planner, the tests a fake.
package planprobe

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/mapview"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// Format and Version are the report's file format (tb-plan-probe v1).
const (
	Format  = "tb-plan-probe"
	Version = 1
)

// The report's verdicts, worst first.
const (
	VerdictOK   = "ok"
	VerdictWarn = "warn"
	VerdictBad  = "bad"
)

// Nav is what the probe asks the navmesh: the server's own query, the offline planner, the floors at a point and the nearest
// walkable point. *navmesh.Planner implements it.
type Nav interface {
	ServerPath(mapID uint32, from, to navmesh.Vec3) (navmesh.ServerPath, error)
	Plan(mapID uint32, from, to navmesh.Vec3) (navmesh.Path, error)
	Floors(mapID uint32, x, y, lo, hi float32) ([]navmesh.Floor, error)
	Nearest(mapID uint32, p navmesh.Vec3) (navmesh.Vec3, bool, error)
}

// Options are the probe's choices.
type Options struct {
	// FromStep drops the segments that end before this run-leader step (0: all).
	FromStep int
}

// Report is one plan's probe (tb-plan-probe v1).
type Report struct {
	Format   string    `json:"format"`
	Version  int       `json:"version"`
	Plan     string    `json:"plan"`
	Map      uint32    `json:"map"`
	Source   string    `json:"source,omitempty"`
	Mmaps    string    `json:"mmaps,omitempty"`
	Steps    int       `json:"steps"`
	Segments []Segment `json:"segments"`
	Notes    []string  `json:"notes"`
	Counts   Counts    `json:"counts"`
}

// Counts are the segments by verdict.
type Counts struct {
	OK   int `json:"ok"`
	Warn int `json:"warn"`
	Bad  int `json:"bad"`
}

// End is one end of a segment: the row and its run-leader step (1-based, as TBM:RUN numbers it).
type End struct {
	Step    int     `json:"step"`
	Leg     int     `json:"leg"`
	Seq     int     `json:"seq"`
	Kind    string  `json:"kind"`
	Comment string  `json:"comment"`
	X       float32 `json:"x"`
	Y       float32 `json:"y"`
	Z       float32 `json:"z"`
}

// Segment is one hop between two consecutive positioned rows.
type Segment struct {
	From      End      `json:"from"`
	To        End      `json:"to"`
	Straight  float32  `json:"straight"`
	Server    Server   `json:"server"`
	Offline   Offline  `json:"offline"`
	Ratio     float32  `json:"ratio"`
	Point     Point    `json:"point"`
	WaterYd   float32  `json:"water_yd"`
	LavaYd    float32  `json:"lava_yd"`
	MaxClimb  float32  `json:"max_climb"`
	MaxDrop   float32  `json:"max_drop"`
	Steep     bool     `json:"steep"`
	FloorFrom int      `json:"floor_from"`
	FloorTo   int      `json:"floor_to"`
	Hop       bool     `json:"hop"`
	Verdict   string   `json:"verdict"`
	Reasons   []string `json:"reasons"`
}

// Server is the server's own path query for the hop (the run leader's CheckMove inputs).
type Server struct {
	Type      uint32  `json:"type"`
	Verdict   string  `json:"verdict"`
	Length    float32 `json:"length"`
	EndShort  float32 `json:"end_short"`
	StartPoly float32 `json:"start_poly"`
	EndPoly   float32 `json:"end_poly"`
}

// Offline is the offline planner's independent answer.
type Offline struct {
	Status string  `json:"status"`
	Length float32 `json:"length"`
}

// Point is what stands at the segment's end: the floors over its (x, y), the height gap to the nearest, the distance to the
// nearest walkable point, and how many floors the spot has. Snap and FloorDZ are -1 when the navmesh has no answer.
type Point struct {
	Floors     []Floor `json:"floors"`
	FloorDZ    float32 `json:"floor_dz"`
	Snap       float32 `json:"snap"`
	FloorsHere int     `json:"floors_here"`
}

// Floor is one navmesh surface at the end's (x, y): its height and its polygon flags.
type Floor struct {
	Z     float32 `json:"z"`
	Flags uint16  `json:"flags"`
}

// Probe measures every segment of the rows. Steps are the rows in (leg, seq) order, numbered from 1; a positioned row is one
// with (x, y, z) != (0, 0, 0), and a segment joins two consecutive positioned rows (across legs). cuts are the map's floor
// cuts (mapview.SplitHeights). Source and Mmaps are left for the caller to fill.
func Probe(rows planexport.Rows, nav Nav, cuts []float32, opt Options) (*Report, error) {
	steps := append([]planexport.StepRow(nil), rows.Steps...)
	sort.SliceStable(steps, func(i, j int) bool {
		if steps[i].Leg != steps[j].Leg {
			return steps[i].Leg < steps[j].Leg
		}
		return steps[i].Seq < steps[j].Seq
	})
	type ref struct {
		step int
		row  planexport.StepRow
	}
	var pos []ref
	for i, s := range steps {
		if positioned(s) {
			pos = append(pos, ref{i + 1, s})
		}
	}
	rep := &Report{Format: Format, Version: Version, Plan: rows.Plan.Plan, Map: rows.Plan.Map, Steps: len(steps),
		Segments: []Segment{}, Notes: []string{}}
	for k := 1; k < len(pos); k++ {
		a, b := pos[k-1], pos[k]
		if opt.FromStep > 0 && b.step < opt.FromStep {
			continue
		}
		seg, err := measure(nav, rep.Map, a.step, a.row, b.step, b.row, cuts)
		if err != nil {
			return nil, err
		}
		rep.Segments = append(rep.Segments, seg)
		switch seg.Verdict {
		case VerdictBad:
			rep.Counts.Bad++
		case VerdictWarn:
			rep.Counts.Warn++
		default:
			rep.Counts.OK++
		}
	}
	rep.Notes = append(rep.Notes, planNotes(rep, cuts)...)
	return rep, nil
}

// positioned says whether a row has a point (a rest or loot row carries none: 0, 0, 0).
func positioned(s planexport.StepRow) bool { return s.X != 0 || s.Y != 0 || s.Z != 0 }

// measure probes one segment A -> B.
func measure(nav Nav, mapID uint32, aStep int, a planexport.StepRow, bStep int, b planexport.StepRow, cuts []float32) (Segment, error) {
	from := navmesh.Vec3{a.X, a.Y, a.Z}
	to := navmesh.Vec3{b.X, b.Y, b.Z}
	seg := Segment{From: end(aStep, a), To: end(bStep, b), Reasons: []string{}}
	seg.Straight = r2(float32(dist3(from, to)))

	sp, err := nav.ServerPath(mapID, from, to)
	if err != nil {
		return seg, err
	}
	verdict := navmesh.ServerMoveVerdict(sp, from, to, 0)
	endShort := float32(dist3(sp.ActualEnd, to))
	seg.Server = Server{Type: sp.Type, Verdict: verdict, Length: r2(sp.Length), EndShort: r2(endShort),
		StartPoly: r2(sp.StartDist), EndPoly: r2(sp.EndDist)}
	seg.Ratio = 1
	if seg.Straight >= 1 {
		seg.Ratio = r2(sp.Length / seg.Straight)
	}

	offline, err := nav.Plan(mapID, from, to)
	if err != nil {
		return seg, err
	}
	seg.Offline = Offline{Status: string(offline.Status), Length: r2(offline.Length)}

	floors, err := nav.Floors(mapID, to[0], to[1], to[2]-40, to[2]+40)
	if err != nil {
		return seg, err
	}
	seg.Point = point(floors, to)
	near, ok, err := nav.Nearest(mapID, to)
	if err != nil {
		return seg, err
	}
	seg.Point.Snap = -1
	if ok {
		seg.Point.Snap = r2(float32(dist3(near, to)))
	}

	if err := surface(nav, mapID, offline, &seg); err != nil {
		return seg, err
	}

	seg.FloorFrom = mapview.FloorIndex(cuts, a.Z)
	seg.FloorTo = mapview.FloorIndex(cuts, b.Z)
	seg.Hop = float64(seg.Straight) > dungeon.RunLeaderSeeYd

	var bad, warn []string
	pullLike := b.Kind == planexport.KindPull || b.Kind == planexport.KindBoss
	if !pullLike && seg.Point.FloorDZ > 5 {
		bad = append(bad, fmt.Sprintf("no floor within 5 yd (row z %.2f, floor %.2f)", b.Z, nearestFloorZ(floors, to[2])))
	}
	switch {
	case sp.Type&navmesh.PathNoPath != 0:
		bad = append(bad, "server NOPATH")
	case verdict == "unreachable":
		bad = append(bad, "server unreachable")
	case verdict == "off-mesh" && !(pullLike && float64(endShort) <= float64(effRadius(b))+5):
		bad = append(bad, fmt.Sprintf("server off-mesh (end %.1f yd from the row)", endShort))
	}
	if seg.Ratio > 1.6 {
		warn = append(warn, fmt.Sprintf("path %.2fx the straight line", seg.Ratio))
	}
	if seg.WaterYd > 0 {
		warn = append(warn, fmt.Sprintf("water %.1f yd", seg.WaterYd))
	}
	if seg.LavaYd > 0 {
		warn = append(warn, fmt.Sprintf("lava %.1f yd", seg.LavaYd))
	}
	if seg.Steep {
		warn = append(warn, "steep")
	}
	switch {
	case seg.MaxDrop >= 14:
		warn = append(warn, fmt.Sprintf("fall %.1f yd (approximate 1.12 fall damage)", seg.MaxDrop))
	case seg.MaxDrop >= 6:
		warn = append(warn, fmt.Sprintf("drop %.1f yd", seg.MaxDrop))
	}
	if seg.Point.Snap > 2 {
		warn = append(warn, fmt.Sprintf("%.1f yd off the nearest walkable point", seg.Point.Snap))
	}
	if seg.FloorFrom != seg.FloorTo {
		warn = append(warn, fmt.Sprintf("floor change %d->%d", seg.FloorFrom, seg.FloorTo))
	}
	if seg.Point.FloorsHere >= 2 {
		warn = append(warn, fmt.Sprintf("multi-floor x,y: the row's z picks floor z=%.2f; others %s", b.Z, otherFloors(floors, to[2])))
	}
	if disagree(offline.Status, sp.Type) {
		warn = append(warn, fmt.Sprintf("offline %s, server %s", offline.Status, verdict))
	}
	if seg.Hop {
		warn = append(warn, fmt.Sprintf("%.0f yd hop: the run leader walks it in hops of its own", seg.Straight))
	}

	seg.Reasons = append(append([]string{}, bad...), warn...)
	switch {
	case len(bad) > 0:
		seg.Verdict = VerdictBad
	case len(warn) > 0:
		seg.Verdict = VerdictWarn
	default:
		seg.Verdict = VerdictOK
	}
	return seg, nil
}

func end(step int, s planexport.StepRow) End {
	return End{Step: step, Leg: s.Leg, Seq: s.Seq, Kind: s.Kind, Comment: s.Comment, X: s.X, Y: s.Y, Z: s.Z}
}

// point reads the floors over the end's (x, y): the list, the height gap to the nearest one and how many floors (surfaces 6 yd
// apart or more) the spot has.
func point(floors []navmesh.Floor, to navmesh.Vec3) Point {
	p := Point{Floors: make([]Floor, 0, len(floors)), FloorDZ: -1, Snap: -1, FloorsHere: countFloors(floors)}
	for _, f := range floors {
		p.Floors = append(p.Floors, Floor{Z: r2(f.Z), Flags: f.Flags})
	}
	if len(floors) > 0 {
		p.FloorDZ = r2(float32(math.Abs(float64(nearestFloorZ(floors, to[2])) - float64(to[2]))))
	}
	return p
}

// nearestFloorZ is the floor height nearest z.
func nearestFloorZ(floors []navmesh.Floor, z float32) float32 {
	best := float32(0)
	bestD := math.Inf(1)
	for _, f := range floors {
		if d := math.Abs(float64(f.Z - z)); d < bestD {
			best, bestD = f.Z, d
		}
	}
	return best
}

// countFloors counts the distinct surfaces: floors closer than 6 yd are one surface.
func countFloors(floors []navmesh.Floor) int {
	if len(floors) == 0 {
		return 0
	}
	zs := make([]float32, len(floors))
	for i, f := range floors {
		zs[i] = f.Z
	}
	sort.Slice(zs, func(i, j int) bool { return zs[i] < zs[j] })
	n := 1
	last := zs[0]
	for _, z := range zs[1:] {
		if z-last >= 6 {
			n++
		}
		last = z
	}
	return n
}

// otherFloors lists the surfaces other than the one nearest z, highest first.
func otherFloors(floors []navmesh.Floor, z float32) string {
	near := nearestFloorZ(floors, z)
	var zs []float32
	for _, f := range floors {
		if f.Z != near {
			zs = append(zs, f.Z)
		}
	}
	sort.Slice(zs, func(i, j int) bool { return zs[i] > zs[j] })
	out := make([]string, len(zs))
	for i, z := range zs {
		out[i] = fmt.Sprintf("%.2f", z)
	}
	if len(out) == 0 {
		return "none"
	}
	return strings.Join(out, ", ")
}

// surface samples the offline path (its corners and the ground between them, every 4 yd) and sums the water and lava steps and
// the largest climb and drop. Each sample takes the floor nearest its own z; consecutive pairs also decide the steep flag.
func surface(nav Nav, mapID uint32, offline navmesh.Path, seg *Segment) error {
	samples := samplesOf(offline)
	var prev navmesh.Vec3
	for i, s := range samples {
		floors, err := nav.Floors(mapID, s[0], s[1], s[2]-3, s[2]+3)
		if err != nil {
			return err
		}
		var flags uint16
		if len(floors) > 0 {
			flags = flagsAt(floors, s[2])
		}
		if i > 0 {
			d := float32(dist3(prev, s))
			if flags&navmesh.NavWater != 0 {
				seg.WaterYd += d
			}
			if flags&(navmesh.NavMagma|navmesh.NavSlime) != 0 {
				seg.LavaYd += d
			}
			dz := s[2] - prev[2]
			if dz > seg.MaxClimb {
				seg.MaxClimb = dz
			}
			if -dz > seg.MaxDrop {
				seg.MaxDrop = -dz
			}
			if h := horizontal(prev, s); math.Abs(float64(dz)) >= 2 && h > 0 && math.Abs(float64(dz))/h > 1 {
				seg.Steep = true
			}
		}
		prev = s
	}
	seg.WaterYd = r2(seg.WaterYd)
	seg.LavaYd = r2(seg.LavaYd)
	seg.MaxClimb = r2(seg.MaxClimb)
	seg.MaxDrop = r2(seg.MaxDrop)
	return nil
}

// samplesOf is the path's corners with the ground samples between them.
func samplesOf(p navmesh.Path) []navmesh.Vec3 {
	var out []navmesh.Vec3
	for i, pt := range p.Points {
		out = append(out, pt)
		if i < len(p.Ground) {
			out = append(out, p.Ground[i]...)
		}
	}
	return out
}

// flagsAt is the flags of the floor nearest z.
func flagsAt(floors []navmesh.Floor, z float32) uint16 {
	near := nearestFloorZ(floors, z)
	for _, f := range floors {
		if f.Z == near {
			return f.Flags
		}
	}
	return 0
}

// disagree says the two queries do not agree on whether the hop completes.
func disagree(offline navmesh.Status, typ uint32) bool {
	serverComplete := typ&(navmesh.PathNoPath|navmesh.PathIncomplete) == 0
	return (offline == navmesh.Complete) != serverComplete
}

// effRadius is the pull or boss radius of a row, with the module's defaults for 0 (15 yd pull, 30 yd boss).
func effRadius(s planexport.StepRow) float32 {
	if s.Radius > 0 {
		return s.Radius
	}
	if s.Kind == planexport.KindBoss {
		return planexport.DefaultBossRad
	}
	return planexport.DefaultPullYd
}

// planNotes are the plan's notes: the multi-floor warning and the worst ten non-ok segments.
func planNotes(rep *Report, cuts []float32) []string {
	var notes []string
	multi := len(cuts) > 0
	for _, s := range rep.Segments {
		if s.Point.FloorsHere >= 2 {
			multi = true
		}
	}
	if multi {
		note := "multi-floor map: 2D overlays mislead; z decides"
		if len(cuts) > 0 {
			parts := make([]string, len(cuts))
			for i, c := range cuts {
				parts[i] = fmt.Sprintf("%.1f", c)
			}
			note += " (floor cuts at z " + strings.Join(parts, ", ") + ")"
		}
		notes = append(notes, note)
	}
	var worst []*Segment
	for i := range rep.Segments {
		if rep.Segments[i].Verdict != VerdictOK {
			worst = append(worst, &rep.Segments[i])
		}
	}
	sort.SliceStable(worst, func(i, j int) bool {
		ri, rj := rank(worst[i].Verdict), rank(worst[j].Verdict)
		if ri != rj {
			return ri > rj
		}
		return worst[i].Straight > worst[j].Straight
	})
	if len(worst) > 10 {
		worst = worst[:10]
	}
	for _, s := range worst {
		notes = append(notes, fmt.Sprintf("worst: step %d->%d %s: %s", s.From.Step, s.To.Step,
			strings.ToUpper(s.Verdict), strings.Join(s.Reasons, "; ")))
	}
	return notes
}

func rank(verdict string) int {
	switch verdict {
	case VerdictBad:
		return 2
	case VerdictWarn:
		return 1
	}
	return 0
}

// Text writes the report as the console form: one line per non-ok segment, then the counts.
func (r *Report) Text(w io.Writer) {
	for _, s := range r.Segments {
		if s.Verdict == VerdictOK {
			continue
		}
		fmt.Fprintf(w, "step %d->%d (%s) %s: %s\n", s.From.Step, s.To.Step, describe(&s.To),
			strings.ToUpper(s.Verdict), strings.Join(s.Reasons, "; "))
	}
	fmt.Fprintf(w, "%s: %d steps, %d segments: %d ok, %d warn, %d bad\n", r.Plan, r.Steps, len(r.Segments),
		r.Counts.OK, r.Counts.Warn, r.Counts.Bad)
}

// describe is the row in a text line: `move 'path'`, or `move` without a comment.
func describe(e *End) string {
	if e.Comment == "" {
		return e.Kind
	}
	return e.Kind + " '" + e.Comment + "'"
}

func dist3(a, b navmesh.Vec3) float64 {
	dx, dy, dz := float64(a[0]-b[0]), float64(a[1]-b[1]), float64(a[2]-b[2])
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

func horizontal(a, b navmesh.Vec3) float64 {
	dx, dy := float64(a[0]-b[0]), float64(a[1]-b[1])
	return math.Sqrt(dx*dx + dy*dy)
}

func r2(v float32) float32 { return float32(math.Round(float64(v)*100) / 100) }
