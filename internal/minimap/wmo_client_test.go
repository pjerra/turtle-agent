package minimap

// Client-gated checks of the WMO minimap transform against the server's navmesh and the plan's points. They need the
// owner's client and are skipped otherwise:
//
//	TURTLE_CLIENT_DATA  the client Data folder
//	TURTLE_MINIMAP_DIR  a minimap-extract output folder (its <id>.navmesh.png for the WMO-only proof maps)
//	TURTLE_MMAPS_DIR    the server's mmaps folder (the z-band navmesh of the ADT proof maps)
//	TURTLE_PLANS_DIR    turtle-agent's dungeons folder (the entrances and bosses)
//	TURTLE_W2B_DIR      optional: write the all-dungeon overlay pictures there
//
// The thresholds are W2a's numbers, with margin: 70 IoU >= 0.60, 429 IoU >= 0.70, 129 art-on-navmesh >= 0.80,
// 533 art-on-navmesh >= 0.90; every proof map has a best shift of at most 2 yd and all its plan points on the art.

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/mpq"
)

type mapPoint struct {
	name string
	x, y float64
}

type alignMetrics struct {
	iou, artOnNav, navCovered float64
	shiftX, shiftY            int // the shift that wins by at least 2 % of the navmesh; 0,0 when none does
	rawShiftX, rawShiftY      int // the plain argmax, for the log
	points, hits              int
	missed                    []string
}

type alignFixture struct {
	client, minimapDir, mmapsDir, plansDir string
}

func alignDirs(t *testing.T) alignFixture {
	t.Helper()
	f := alignFixture{
		client:     os.Getenv("TURTLE_CLIENT_DATA"),
		minimapDir: os.Getenv("TURTLE_MINIMAP_DIR"),
		mmapsDir:   os.Getenv("TURTLE_MMAPS_DIR"),
		plansDir:   os.Getenv("TURTLE_PLANS_DIR"),
	}
	if f.client == "" || f.minimapDir == "" || f.mmapsDir == "" || f.plansDir == "" {
		fmt.Fprintf(os.Stderr, "NOT RUN %s: set TURTLE_CLIENT_DATA, TURTLE_MINIMAP_DIR, TURTLE_MMAPS_DIR and TURTLE_PLANS_DIR to run the client alignment tests\n", t.Name())
		t.Skip("set TURTLE_CLIENT_DATA, TURTLE_MINIMAP_DIR, TURTLE_MMAPS_DIR and TURTLE_PLANS_DIR to run the client alignment test")
	}
	return f
}

func (f alignFixture) open(t *testing.T) (*mpq.Set, Translate) {
	t.Helper()
	set, err := mpq.OpenSet(f.client, ArchiveOrder(f.client))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { set.Close() })
	trs := ParseTranslate(mustRead(t, set, translatePath))
	return set, trs
}

// mapDir finds a map's Map.dbc folder.
func (f alignFixture) mapDir(t *testing.T, set *mpq.Set, id uint32) string {
	t.Helper()
	rows, err := ParseMaps(mustRead(t, set, `DBFilesClient\Map.dbc`))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ID == id {
			return r.Dir
		}
	}
	t.Fatalf("map %d is not in Map.dbc", id)
	return ""
}

// composeFull calls the production minimap source for one map with the map's navmesh box, and returns the polygons it
// read for the z-band render.
func (f alignFixture) composeFull(t *testing.T, set *mpq.Set, trs Translate, id uint32) (Layer, wmoInfo, []NavPoly) {
	t.Helper()
	polys, tiles, perr := LoadMapPolys([]string{f.mmapsDir}, id)
	if perr != nil || tiles == 0 {
		t.Fatalf("map %d: mmaps: %v (%d tiles)", id, perr, tiles)
	}
	l, info, ok, reason := minimapSource(set, trs, f.mapDir(t, set, id), id, navBoundsOf(polys), t.Logf)
	if !ok {
		t.Fatalf("map %d has no minimap: %s", id, reason)
	}
	if l.Meta.Source != KindMinimap || l.Meta.From != fromWMO {
		t.Fatalf("map %d: source %q from %q, want minimap/wmo", id, l.Meta.Source, l.Meta.From)
	}
	return l, info, polys
}

// zBandNav renders the navmesh polygons of a map whose mean height lies within the WMO groups' z range, so the picture
// is compared with the dungeon floors, not the outdoor terrain of an ADT map.
func (f alignFixture) zBandNav(t *testing.T, id uint32, info wmoInfo, polys []NavPoly) (image.Image, Meta) {
	t.Helper()
	var keep []NavPoly
	for _, p := range polys {
		z := 0.0
		for _, v := range p.V {
			z += float64(v[2])
		}
		z /= float64(len(p.V))
		if z >= info.ZMin-3 && z <= info.ZMax+3 {
			keep = append(keep, p)
		}
	}
	img, meta, err := RenderNavmesh(keep, 2, 8192)
	if err != nil {
		t.Fatalf("map %d: z-band navmesh %.1f..%.1f: %v", id, info.ZMin, info.ZMax, err)
	}
	return img, meta
}

func (f alignFixture) liveNav(t *testing.T, id uint32) (image.Image, Meta) {
	t.Helper()
	p := filepath.Join(f.minimapDir, strconv.FormatUint(uint64(id), 10)+".navmesh.png")
	return loadMetaPNG(t, p)
}

func loadMetaPNG(t *testing.T, p string) (image.Image, Meta) {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	var m Meta
	b, err := os.ReadFile(strings.TrimSuffix(p, ".png") + ".json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return img, m
}

// planPoints reads every dungeon plan of a map.
func planPoints(t *testing.T, dir string, id uint32) []mapPoint {
	t.Helper()
	fs, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(fs)
	var pts []mapPoint
	type p struct{ X, Y float64 }
	for _, f := range fs {
		var d struct {
			Name     string
			Map      int
			Entrance *p
			Legs     []struct {
				Name string
				Boss *struct{ Position *p }
			}
		}
		b, err := os.ReadFile(f)
		if err != nil || json.Unmarshal(b, &d) != nil || d.Map != int(id) {
			continue
		}
		if d.Entrance != nil {
			pts = append(pts, mapPoint{d.Name + ":entrance", d.Entrance.X, d.Entrance.Y})
		}
		for _, l := range d.Legs {
			if l.Boss != nil && l.Boss.Position != nil {
				pts = append(pts, mapPoint{d.Name + ":" + l.Name, l.Boss.Position.X, l.Boss.Position.Y})
			}
		}
	}
	return pts
}

func opaquePixel(img image.Image, x, y int) bool {
	if !image.Pt(x, y).In(img.Bounds()) {
		return false
	}
	_, _, _, a := img.At(x, y).RGBA()
	return a > 0x8000
}

// measure compares the picture with the navmesh on a 1-yd grid and finds the residual shift that covers the most
// navmesh (the search W2a settled on; a 5-yd centroid offset does not work because the art shows walls and rims).
func measure(art image.Image, am Meta, nav image.Image, nm Meta, pts []mapPoint) alignMetrics {
	var r alignMetrics
	N, S := math.Max(am.North, nm.North), math.Min(am.South, nm.South)
	W, E := math.Max(am.West, nm.West), math.Min(am.East, nm.East)
	var inter, uni, na, ma float64
	for X := S; X < N; X++ {
		for Y := E; Y < W; Y++ {
			c, row := am.ToPixel(X+.5, Y+.5)
			mm := opaquePixel(art, int(c), int(row))
			c, row = nm.ToPixel(X+.5, Y+.5)
			nn := opaquePixel(nav, int(c), int(row))
			if mm {
				ma++
			}
			if nn {
				na++
			}
			if mm && nn {
				inter++
			}
			if mm || nn {
				uni++
			}
		}
	}
	if ma > 0 {
		r.artOnNav = inter / ma
	}
	if na > 0 {
		r.navCovered = inter / na
	}
	if uni > 0 {
		r.iou = inter / uni
	}
	gw, gh := int(W-E)+1, int(N-S)+1
	mg, ng := make([]bool, gw*gh), make([]bool, gw*gh)
	for i := range gh {
		for j := range gw {
			X, Y := S+float64(i)+.5, E+float64(j)+.5
			c, row := am.ToPixel(X, Y)
			mg[i*gw+j] = opaquePixel(art, int(c), int(row))
			c, row = nm.ToPixel(X, Y)
			ng[i*gw+j] = opaquePixel(nav, int(c), int(row))
		}
	}
	best, bdx, bdy := -1, 0, 0
	zero := 0
	for k := range mg {
		if mg[k] && ng[k] {
			zero++
		}
	}
	for dx := -10; dx <= 10; dx++ {
		for dy := -10; dy <= 10; dy++ {
			n := 0
			for i := max(0, -dx); i < gh && i+dx < gh; i++ {
				for j := max(0, -dy); j < gw && j+dy < gw; j++ {
					if ng[i*gw+j] && mg[(i+dx)*gw+j+dy] {
						n++
					}
				}
			}
			if n > best {
				best, bdx, bdy = n, dx, dy
			}
		}
	}
	r.rawShiftX, r.rawShiftY = bdx, bdy
	// A shift is only meaningful when it wins clearly. On a map whose navmesh is much larger than the art (Naxxramas:
	// the z band also holds the outdoor terrain), a 1-yd grid is self-similar and a shift can win by a fraction of a
	// percent; report the zero shift then. A real misalignment wins by far more (W2a's wrong variants went to the
	// search edge).
	if best > zero+max(1, int(0.02*na)) {
		r.shiftX, r.shiftY = bdx, bdy
	}
	// A plan point is on the art when an opaque pixel lies within 3 yd of it.
	rad := max(1, int(math.Ceil(3*am.PxPerYard)))
	for _, p := range pts {
		r.points++
		c, row := am.ToPixel(p.x, p.y)
		found := false
		for dy := -rad; dy <= rad && !found; dy++ {
			for dx := -rad; dx <= rad; dx++ {
				if opaquePixel(art, int(c)+dx, int(row)+dy) {
					found = true
					break
				}
			}
		}
		if found {
			r.hits++
		} else {
			r.missed = append(r.missed, p.name)
		}
	}
	return r
}

func (r alignMetrics) String() string {
	s := fmt.Sprintf("shift=(%d,%d) rawShift=(%d,%d) iou=%.3f artOnNav=%.3f navCovered=%.3f points=%d/%d",
		r.shiftX, r.shiftY, r.rawShiftX, r.rawShiftY, r.iou, r.artOnNav, r.navCovered, r.hits, r.points)
	if len(r.missed) > 0 {
		s += " missed=" + strings.Join(r.missed, ",")
	}
	return s
}

func (r alignMetrics) shiftWithin(yd int) bool {
	return absInt(r.shiftX) <= yd && absInt(r.shiftY) <= yd
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func TestWMOAlignmentProofMaps(t *testing.T) {
	f := alignDirs(t)
	set, trs := f.open(t)
	plans := func(id uint32) []mapPoint { return planPoints(t, f.plansDir, id) }
	for _, c := range []struct {
		id    uint32
		zband bool
		min   float64
		ic    bool
		pts   int
	}{
		{70, false, 0.60, true, 9},
		{429, false, 0.70, true, 17},
		{129, true, 0.80, false, 5},
		{533, true, 0.90, false, 16},
	} {
		t.Run(strconv.FormatUint(uint64(c.id), 10), func(t *testing.T) {
			l, info, polys := f.composeFull(t, set, trs, c.id)
			var nav image.Image
			var nm Meta
			if c.zband {
				nav, nm = f.zBandNav(t, c.id, info, polys)
			} else {
				nav, nm = f.liveNav(t, c.id)
			}
			m := measure(l.Img, l.Meta, nav, nm, plans(c.id))
			t.Logf("RESULT map=%d %s tiles=%d z=%.1f..%.1f", c.id, m, info.Tiles, info.ZMin, info.ZMax)
			if !m.shiftWithin(2) {
				t.Errorf("map %d: best shift %d,%d yd, want at most 2", c.id, m.shiftX, m.shiftY)
			}
			if c.ic {
				if m.iou < c.min {
					t.Errorf("map %d: IoU %.3f, want at least %.2f", c.id, m.iou, c.min)
				}
			} else if m.artOnNav < c.min {
				t.Errorf("map %d: art on navmesh %.3f, want at least %.2f", c.id, m.artOnNav, c.min)
			}
			if m.points < c.pts || m.hits != m.points {
				t.Errorf("map %d: plan points %d/%d, want %d/%d", c.id, m.hits, m.points, c.pts, c.pts)
			}
		})
	}
}

// alignExpect is the recorded state of a known-exception map: the metric with its band, the plan points and the shift.
// A value outside the band in either direction fails the test, so an improvement also asks for an updated table.
type alignExpect struct {
	metric string
	value  float64
	band   float64
	hits   int
	total  int
	shiftX int
	shiftY int
	rawX   int
	rawY   int
	pinRaw bool // the raw shift is part of the exception (533)
	reason string
}

// knownExceptions are the maps whose measured alignment cannot meet a spec threshold for a known reason. The test fails
// on every deviation from these recorded values; the reason is also written into the picture JSON as "note" where the
// picture itself has a client-data limit.
var knownExceptions = map[uint32]alignExpect{
	189: {metric: "artOnNav", value: 0.624, band: 0.03, hits: 13, total: 14,
		reason: "Herod's room has navmesh but no client art: artOnNav 0.624, 13/14 points (scarlet_armory:herod off the art)"},
	819: {metric: "artOnNav", value: 0.780, band: 0.03, hits: 6, total: 7,
		reason: "part of the WMO has no navmesh (red areas where the client has no art): artOnNav 0.780, 6/7 points (entrance)"},
	822: {metric: "artOnNav", value: 0.696, band: 0.03, hits: 5, total: 5,
		reason: "client art includes walls and rims with no navmesh (artOnNav 0.696); the two unrelated mini-hives are excluded, shift 0,0"},
	533: {metric: "artOnNav", value: 0.939, band: 0.03, hits: 16, total: 16, rawX: 2, rawY: -6, pinRaw: true,
		reason: "the raw shift argmax is flat on a z-band navmesh 7x the art (artOnNav 0.939, 16/16); the filtered shift is 0,0"},
}

// exceptionProblem is the first measured value that left an exception's recorded state.
func exceptionProblem(m alignMetrics, exp alignExpect) string {
	metric := m.artOnNav
	if exp.metric == "iou" {
		metric = m.iou
	}
	switch {
	case math.Abs(metric-exp.value) > exp.band:
		return fmt.Sprintf("%s %.3f outside %.3f +/- %.2f", exp.metric, metric, exp.value, exp.band)
	case m.hits != exp.hits || m.points != exp.total:
		return fmt.Sprintf("points %d/%d, recorded %d/%d", m.hits, m.points, exp.hits, exp.total)
	case m.shiftX != exp.shiftX || m.shiftY != exp.shiftY:
		return fmt.Sprintf("shift (%d,%d), recorded (%d,%d)", m.shiftX, m.shiftY, exp.shiftX, exp.shiftY)
	case exp.pinRaw && (m.rawShiftX != exp.rawX || m.rawShiftY != exp.rawY):
		return fmt.Sprintf("raw shift (%d,%d), recorded (%d,%d)", m.rawShiftX, m.rawShiftY, exp.rawX, exp.rawY)
	}
	return ""
}

// dungeonIDs are the 21 instance maps that have no client minimap (plan table).
var dungeonIDs = []uint32{34, 35, 43, 48, 70, 90, 109, 129, 189, 229, 230, 249, 349, 389, 409, 429, 533, 816, 818, 819, 822}

// TestWMOAllDungeons composes every target dungeon, writes an overlay picture of the art over the z-band navmesh with
// the plan's entrances and bosses marked, and logs the pass numbers. It is the W2b verification run.
func TestWMOAllDungeons(t *testing.T) {
	outDir := os.Getenv("TURTLE_W2B_DIR")
	if outDir == "" {
		fmt.Fprintln(os.Stderr, "NOT RUN TestWMOAllDungeons: set TURTLE_W2B_DIR to write the all-dungeon overlays")
		t.Skip("set TURTLE_W2B_DIR to write the all-dungeon overlays")
	}
	f := alignDirs(t)
	set, trs := f.open(t)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, id := range dungeonIDs {
		t.Run(strconv.FormatUint(uint64(id), 10), func(t *testing.T) {
			l, info, polys := f.composeFull(t, set, trs, id)
			nav, nm := f.zBandNav(t, id, info, polys)
			pts := planPoints(t, f.plansDir, id)
			m := measure(l.Img, l.Meta, nav, nm, pts)
			name := strings.NewReplacer(" ", "_", "'", "", ":", "").Replace(strings.ToLower(strings.TrimSpace(mapName(t, set, id))))
			if err := writeOverlay(filepath.Join(outDir, fmt.Sprintf("%d-%s.overlay.png", id, name)), l.Img, l.Meta, nav, nm, pts); err != nil {
				t.Fatal(err)
			}
			pass, metric := true, "iou"
			if info.WDT {
				pass = m.iou >= 0.50
			} else {
				metric = "artOnNav"
				pass = m.artOnNav >= 0.80
			}
			pass = pass && m.shiftWithin(2) && m.hits == m.points
			raw3 := absInt(m.rawShiftX) > 3 || absInt(m.rawShiftY) > 3
			t.Logf("W2B map=%d name=%q produced=y tiles=%d excluded=%d %s metric=%s pass=%v rawOver3=%v",
				id, mapName(t, set, id), info.Tiles, info.Excluded, m, metric, pass, raw3)
			if exp, ok := knownExceptions[id]; ok {
				if problem := exceptionProblem(m, exp); problem != "" {
					t.Errorf("map %d known exception left its recorded state: %s; update the table (%s)", id, problem, m)
				} else {
					t.Logf("KNOWN EXCEPTION map=%d: %s", id, exp.reason)
				}
			} else if !pass || raw3 {
				t.Errorf("map %d alignment failed: %s (pass=%v, raw shift over 3 yd: %v)", id, m, pass, raw3)
			}
		})
	}
}

// TestFrostmaneHivesExcluded checks the navmesh-box placement rule on the map that needed it: the two outdoor mini-hive
// WMOs (uids 303286 and 303232) stand far from the dungeon and must not grow the picture.
func TestFrostmaneHivesExcluded(t *testing.T) {
	f := alignDirs(t)
	set, trs := f.open(t)
	l, info, polys := f.composeFull(t, set, trs, 822)
	if info.Excluded < 2 {
		t.Errorf("excluded = %d placements, want the two mini-hive WMOs", info.Excluded)
	}
	// The full picture was 2798 px wide with the hives; the dungeon alone is under 2000 px.
	if l.Meta.Width > 2000 {
		t.Errorf("canvas %d px wide, want the hives' ~350 yd of picture gone", l.Meta.Width)
	}
	nav, nm := f.zBandNav(t, 822, info, polys)
	m := measure(l.Img, l.Meta, nav, nm, planPoints(t, f.plansDir, 822))
	t.Logf("822: excluded=%d canvas=%dx%d %s", info.Excluded, l.Meta.Width, l.Meta.Height, m)
}

func mapName(t *testing.T, set *mpq.Set, id uint32) string {
	t.Helper()
	rows, err := ParseMaps(mustRead(t, set, `DBFilesClient\Map.dbc`))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ID == id {
			return r.Name
		}
	}
	return ""
}

// writeOverlay draws the art, tints it green where navmesh lies under it, paints navmesh that has no art red, and marks
// every plan point with a yellow cross.
func writeOverlay(path string, art image.Image, am Meta, nav image.Image, nm Meta, pts []mapPoint) error {
	ov := image.NewNRGBA(image.Rect(0, 0, am.Width, am.Height))
	for r := range am.Height {
		for c := range am.Width {
			X, Y := am.ToWorld(float64(c)+.5, float64(r)+.5)
			nc, nr := nm.ToPixel(X, Y)
			nn := opaquePixel(nav, int(nc), int(nr))
			a := color.NRGBAModel.Convert(art.At(c, r)).(color.NRGBA)
			switch {
			case a.A > 0 && nn:
				ov.SetNRGBA(c, r, color.NRGBA{uint8(int(a.R) * 6 / 10), uint8(min(255, int(a.G)*6/10+90)), uint8(int(a.B) * 6 / 10), 255})
			case a.A > 0:
				ov.SetNRGBA(c, r, a)
			case nn:
				ov.SetNRGBA(c, r, color.NRGBA{230, 30, 30, 255})
			default:
				ov.SetNRGBA(c, r, color.NRGBA{20, 20, 28, 255})
			}
		}
	}
	for _, p := range pts {
		c, r := am.ToPixel(p.x, p.y)
		for d := -10; d <= 10; d++ {
			for _, q := range []image.Point{{int(c) + d, int(r)}, {int(c), int(r) + d}, {int(c) + d, int(r) + d}, {int(c) + d, int(r) - d}} {
				if q.In(ov.Bounds()) {
					ov.SetNRGBA(q.X, q.Y, color.NRGBA{255, 230, 0, 255})
				}
			}
		}
	}
	fh, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(fh, ov); err != nil {
		fh.Close()
		return err
	}
	return fh.Close()
}
