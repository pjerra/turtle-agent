package minimap

// The minimap the client draws indoors comes from tiles of the WMO's own groups, not from the terrain. md5translate lists
// them as WMO\...\<base>_<group>_<A>_<B>.blp, and the root WMO's MOGI gives each group's bounding box. This file places
// those groups (with the WDT or ADT MODF of each WMO) on one north-up picture and draws the tiles over the terrain minimap
// on the five maps whose indoor part the plan puts over the terrain (W2a's Q-e). The transform was settled against the
// server's navmesh (W2a): 2 px per yard, a tile index step of 128 yd, bottom-left anchored tiles, a 180-degree turn,
// th = +rot.y, and all groups drawn by PZ+z0 rising.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"sort"
	"strings"

	"github.com/pjerra/turtle-agent/internal/blp"
	"github.com/pjerra/turtle-agent/internal/mpq"
)

const (
	wmoPxPerYd    = 2.0   // the client's indoor minimap scale
	wmoTilePx     = 256.0 // a tile index step in pixels at that scale (128 yd)
	wmoTileYd     = wmoTilePx / wmoPxPerYd
	wmoMapCenter  = 17066.666 // the ADT placement convention: 32 * 533.333
	wmoMaxSide    = 8192      // the longest side of a composed picture
	wmoMaxTiles   = 4096      // decoded tiles per map
	wmoTileBudget = 256 << 20 // decoded tile pixels per map, in bytes (1024 tiles of 256x256)
	wmoMaxTilePx  = 512       // the client's tiles are 256 px at most
	wmoMaxPlace   = 4096      // placements per map
	wmoMaxADTs    = 64        // an instance has at most 36 ADTs; a world map's WDT is not scanned
	wmoMaxGroups  = 4096      // MOGI groups per WMO
	// wmoMaxDrawables and wmoMaxWork bound the compositor: a group is one drawable, and every tile rectangle costs its
	// output pixels. A bigger client file gets a reason in <id>.status.json, not a hang.
	wmoMaxDrawables = 16384
	wmoMaxWork      = 1 << 25 // sampled pixels per map (33.5 M; the largest real map, Dire Maul, samples 16.6 M)
	// wmoNavMargin is how far (yards) a placement's box may lie outside the map's navmesh bounding box and still be
	// drawn. Unrelated outdoor WMOs (Frostmane Hollow's Silithus mini-hives) are hundreds of yards away and have no
	// navmesh; they are dropped.
	wmoNavMargin = 200.0
)

const (
	fromTerrain    = "terrain"
	fromWMO        = "wmo"
	fromTerrainWMO = "terrain+wmo"
)

// overlayMaps are the maps whose indoor WMO art is drawn over an existing terrain minimap (the plan's Q-e). Every other
// map with a usable terrain minimap keeps that picture untouched.
var overlayMaps = map[uint32]bool{33: true, 36: true, 289: true, 329: true, 800: true}

// wmoSkipUIDs are placements that belong to another zone: Frostmane Hollow's two Silithus mini-hives stand on the
// outdoor surface navmesh, so the navmesh-box rule keeps them, but their art has nothing to do with the dungeon.
var wmoSkipUIDs = map[uint32]map[uint32]bool{
	822: {303286: true, 303232: true},
}

// wmoKnownLimits are one-line client-data limits of a composed picture, written into its JSON as "note" for humans (the
// dashboards ignore the key).
var wmoKnownLimits = map[uint32]string{
	189: "Herod's room has navmesh but no client minimap art; the plan point there is off the picture",
	819: "part of the WMO has no client minimap art (navmesh areas are drawn red); the entrance is off the picture",
	822: "the two Silithus mini-hives (uids 303286 and 303232) belong to another zone and are not drawn",
}

// navBounds is the world rectangle of a map's navmesh (x north, y west).
type navBounds struct {
	North, South, West, East float64
}

// navBoundsOf is the world bounding box of the polygons, or nil when there are none.
func navBoundsOf(polys []NavPoly) *navBounds {
	if len(polys) == 0 {
		return nil
	}
	nb := navBounds{North: -1e18, South: 1e18, West: -1e18, East: 1e18}
	for _, p := range polys {
		for _, v := range p.V {
			nb.North, nb.South = math.Max(nb.North, float64(v[0])), math.Min(nb.South, float64(v[0]))
			nb.West, nb.East = math.Max(nb.West, float64(v[1])), math.Min(nb.East, float64(v[1]))
		}
	}
	if nb.North <= nb.South || nb.West <= nb.East {
		return nil
	}
	return &nb
}

// wmoPlacement is one MODF: which WMO stands where. pos and rot are the client's (x east, y up, z north for the ADT
// convention).
type wmoPlacement struct {
	path     string
	uid      uint32
	pos, rot [3]float64
}

// wmoGroup is one MOGI entry: the bounding box of one WMO group and the minimap tiles md5translate lists for it.
type wmoGroup struct {
	index int
	flags uint32
	x0    float64
	y0    float64
	z0    float64
	x1    float64
	y1    float64
	z1    float64
	tiles map[[2]int]*image.NRGBA // (A, B) -> picture; B counts up from y0
}

// wmoModel is one WMO file parsed for its groups and decoded minimap tiles.
type wmoModel struct {
	base    string
	groups  []*wmoGroup
	err     error
	listed  int // tile lines md5translate lists for the groups
	decoded int
	failed  int
}

// wmoBudget bounds what one map composes.
type wmoBudget struct {
	tiles  int
	bytes  int
	capped bool
}

// wmoInfo is what a map's WMO composition measured, for the alignment tests and the reason of a missing picture.
type wmoInfo struct {
	ZMin, ZMax float64 // world z of the group boxes drawn (0 when none)
	Tiles      int     // tile images drawn
	Work       int64   // output pixels the tile rectangles cover
	Placements int     // MODF entries found (after uid dedup)
	Excluded   int     // placements dropped for lying outside the map navmesh
	WDT        bool    // the placements come from the WDT (a WMO-only map)
}

// walkChunks calls f for every chunk of a WMO, WDT or ADT, in file order, and stops at a truncated tail.
func walkChunks(d []byte, f func(id string, data []byte)) {
	for len(d) >= 8 {
		id := string([]byte{d[3], d[2], d[1], d[0]})
		n := int(binary.LittleEndian.Uint32(d[4:]))
		if n < 0 || n > len(d)-8 {
			return
		}
		f(id, d[8:8+n])
		d = d[8+n:]
	}
}

func le32(b []byte, o int) uint32 { return binary.LittleEndian.Uint32(b[o:]) }

func f32(b []byte, o int) float64 { return float64(math.Float32frombits(le32(b, o))) }

// splitCStrings splits a MWMO chunk into its NUL-terminated paths.
func splitCStrings(d []byte) []string {
	var out []string
	for _, s := range strings.Split(string(d), "\x00") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func nameAt(names []string, n uint32) string {
	if int(n) >= len(names) {
		return ""
	}
	return names[n]
}

// wmoPlacements reads the MODF entries of a map: the WDT's own when the map is WMO-only (MPHD flag 0x1 or MODF entries),
// else every ADT listed in the WDT MAIN. Duplicates (the same uniqueId in several ADTs) are removed. The bool says the
// WDT was the source.
func wmoPlacements(set *mpq.Set, dir string, logf func(string, ...any)) ([]wmoPlacement, bool, error) {
	wdt, err := set.Read(`World\Maps\` + dir + `\` + dir + `.wdt`)
	if err != nil {
		return nil, false, fmt.Errorf("WDT: %w", err)
	}
	var mwmo, modf, mainData []byte
	flags := uint32(0)
	walkChunks(wdt, func(id string, d []byte) {
		switch id {
		case "MPHD":
			if len(d) >= 4 {
				flags = le32(d, 0)
			}
		case "MWMO":
			mwmo = append(mwmo, d...)
		case "MODF":
			modf = append(modf, d...)
		case "MAIN":
			mainData = append(mainData, d...)
		}
	})
	names := splitCStrings(mwmo)
	var out []wmoPlacement
	seen := map[uint32]bool{}
	add := func(p wmoPlacement) {
		if p.path == "" || seen[p.uid] || len(out) >= wmoMaxPlace {
			return
		}
		seen[p.uid] = true
		out = append(out, p)
	}
	if len(modf) > 0 || flags&0x1 != 0 {
		for o := 0; o+64 <= len(modf); o += 64 {
			add(parseMODF(modf[o:o+64], func(n uint32) string { return nameAt(names, n) }))
		}
		if len(out) == 0 {
			logf("map %s: the WDT holds no usable MODF entry", dir)
		}
		return out, true, nil
	}
	adts := 0
	for i := range 64 * 64 {
		if (i+1)*8 > len(mainData) {
			break
		}
		if le32(mainData, i*8) == 0 {
			continue
		}
		adts++
		if adts > wmoMaxADTs {
			return nil, false, fmt.Errorf("the WDT lists more than %d ADTs; the WMO scan is limited to instance maps", wmoMaxADTs)
		}
		a, err := set.Read(fmt.Sprintf(`World\Maps\%s\%s_%d_%d.adt`, dir, dir, i%64, i/64))
		if err != nil {
			continue
		}
		var mwmo, modf []byte
		var mwid []uint32
		walkChunks(a, func(id string, d []byte) {
			switch id {
			case "MWMO":
				mwmo = append(mwmo, d...)
			case "MWID":
				for o := 0; o+4 <= len(d); o += 4 {
					mwid = append(mwid, le32(d, o))
				}
			case "MODF":
				modf = append(modf, d...)
			}
		})
		names := splitCStrings(mwmo)
		for o := 0; o+64 <= len(modf); o += 64 {
			add(parseMODF(modf[o:o+64], func(n uint32) string {
				if int(n) >= len(mwid) {
					return ""
				}
				return nameAt(names, mwid[n])
			}))
		}
	}
	return out, false, nil
}

// parseMODF reads one 64-byte MODF entry: nameId u32 @0, uniqueId u32 @4, pos 3xf32 @8, rot 3xf32 @20.
func parseMODF(e []byte, name func(uint32) string) wmoPlacement {
	p := wmoPlacement{path: name(le32(e, 0)), uid: le32(e, 4)}
	for k := range 3 {
		p.pos[k] = f32(e, 8+4*k)
		p.rot[k] = f32(e, 20+4*k)
	}
	for _, v := range []float64{p.pos[0], p.pos[1], p.pos[2], p.rot[0], p.rot[1], p.rot[2]} {
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > 1e6 {
			return wmoPlacement{}
		}
	}
	return p
}

// wmoBase is a WMO path's lower-case file name without the extension.
func wmoBase(path string) string {
	s := path[strings.LastIndexAny(path, `\/`)+1:]
	return strings.TrimSuffix(strings.ToLower(s), ".wmo")
}

// loadWMO reads a WMO root's MOGI and decodes the minimap tiles md5translate lists for it. A missing WMO or a tile that
// does not decode is logged and skipped; it never ends the run.
func loadWMO(set *mpq.Set, trs Translate, path string, budget *wmoBudget, logf func(string, ...any)) *wmoModel {
	base := wmoBase(path)
	m := &wmoModel{base: base}
	rb, err := set.Read(path)
	if err != nil {
		m.err = err
		return m
	}
	var mogi []byte
	walkChunks(rb, func(id string, d []byte) {
		if id == "MOGI" {
			mogi = append(mogi, d...)
		}
	})
	n := min(len(mogi)/32, wmoMaxGroups)
	// MOGI group ids are positional: keep a slot for every entry, including a rejected one, so a tile's t.Group still
	// names its own box.
	m.groups = make([]*wmoGroup, n)
	for i := range n {
		o := i * 32
		g := &wmoGroup{index: i, flags: le32(mogi, o),
			x0: f32(mogi, o+4), y0: f32(mogi, o+8), z0: f32(mogi, o+12),
			x1: f32(mogi, o+16), y1: f32(mogi, o+20), z1: f32(mogi, o+24),
			tiles: map[[2]int]*image.NRGBA{}}
		if !saneBox(g) {
			logf("WMO %s group %d: implausible bounding box; skipped", base, i)
			continue
		}
		m.groups[i] = g
	}
	tiles := trs.WMO[base]
	keys := make([]WMOTile, 0, len(tiles))
	for t := range tiles {
		keys = append(keys, t)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		if a.A != b.A {
			return a.A < b.A
		}
		return a.B < b.B
	})
	for _, t := range keys {
		m.listed++
		if t.Group < 0 || t.Group >= len(m.groups) || m.groups[t.Group] == nil {
			m.failed++ // the tile's group was rejected; drop the tile with it
			continue
		}
		if budget.tiles >= wmoMaxTiles || budget.bytes >= wmoTileBudget {
			if !budget.capped {
				logf("WMO minimap: the %d tile / %d MiB budget is reached; further tiles are skipped", wmoMaxTiles, wmoTileBudget>>20)
				budget.capped = true
			}
			continue
		}
		data, err := set.Read(`Textures\Minimap\` + tiles[t])
		if err != nil {
			m.failed++
			continue // listed but absent from the archives
		}
		img, err := decodeWMOTile(data)
		if err != nil {
			m.failed++
			if m.failed <= 5 {
				logf("WMO %s group %d tile %02d_%02d: %v", base, t.Group, t.A, t.B, err)
			}
			continue
		}
		budget.tiles++
		budget.bytes += img.Bounds().Dx() * img.Bounds().Dy() * 4
		m.decoded++
		m.groups[t.Group].tiles[[2]int{t.A, t.B}] = img
	}
	return m
}

// saneBox rejects a group bounding box with non-finite or inverted edges.
func saneBox(g *wmoGroup) bool {
	for _, v := range []float64{g.x0, g.y0, g.z0, g.x1, g.y1, g.z1} {
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > 1e6 {
			return false
		}
	}
	return g.x1 >= g.x0 && g.y1 >= g.y0 && g.z1 >= g.z0
}

// decodeWMOTile bounds the tile before the decoder allocates its pixels.
func decodeWMOTile(data []byte) (*image.NRGBA, error) {
	if len(data) < 20 || string(data[:4]) != "BLP2" {
		return nil, errors.New("not a BLP2 tile")
	}
	w, h := int(le32(data, 12)), int(le32(data, 16))
	if w <= 0 || h <= 0 || w > wmoMaxTilePx || h > wmoMaxTilePx {
		return nil, fmt.Errorf("tile size %dx%d is outside the client's range", w, h)
	}
	return blp.Decode(data)
}

// wmoDrawable is one group of one placement, ready to draw.
type wmoDrawable struct {
	g                  *wmoGroup
	px, py, pz         float64
	th                 float64 // radians, the yaw
	wx0, wx1, wy0, wy1 float64
	zw, zw1            float64 // PZ + z0 (the draw order) and PZ + z1
}

// world transforms one group-local point (x, y) to world (north, west).
func (d wmoDrawable) world(x, y float64) (float64, float64) {
	cs, sn := math.Cos(d.th), math.Sin(d.th)
	return d.px - (cs*x - sn*y), d.py - (sn*x + cs*y)
}

// local is the inverse of world.
func (d wmoDrawable) local(X, Y float64) (float64, float64) {
	cs, sn := math.Cos(d.th), math.Sin(d.th)
	dx, dy := X-d.px, Y-d.py
	return -(cs*dx + sn*dy), -(-sn*dx + cs*dy)
}

// wmoJob is one tile of one group in output pixels: the tile's local rectangle (clipped to the group box) and the output
// rectangle it can touch. Drawing only these rectangles keeps the work proportional to the tiles, not to the group box
// (an outdoor WMO can have a box hundreds of yards across around one small tile).
type wmoJob struct {
	d              wmoDrawable
	t              *image.NRGBA
	a, b           int
	x0, y0, x1, y1 float64
	rect           image.Rectangle
}

// jobs returns the tile rectangles of a drawable at the canvas scale. It returns no job for a tile outside the group box.
func (d wmoDrawable) jobs(m Meta) []wmoJob {
	out := make([]wmoJob, 0, len(d.g.tiles))
	keys := make([][2]int, 0, len(d.g.tiles))
	for k := range d.g.tiles {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	for _, k := range keys {
		a, b := k[0], k[1]
		x0 := math.Max(d.g.x0, d.g.x0+float64(a)*wmoTileYd)
		x1 := math.Min(d.g.x1, d.g.x0+float64(a+1)*wmoTileYd)
		y0 := math.Max(d.g.y0, d.g.y0+float64(b)*wmoTileYd)
		y1 := math.Min(d.g.y1, d.g.y0+float64(b+1)*wmoTileYd)
		if x1 <= x0 || y1 <= y0 {
			continue
		}
		// The tile's own four world corners give the exact rectangle.
		tn, ts, tw, te := -1e18, 1e18, -1e18, 1e18
		for _, c := range [4][2]float64{{x0, y0}, {x0, y1}, {x1, y0}, {x1, y1}} {
			X, Y := d.world(c[0], c[1])
			tn, ts = math.Max(tn, X), math.Min(ts, X)
			tw, te = math.Max(tw, Y), math.Min(te, Y)
		}
		pc0, pr0 := m.ToPixel(tn, tw)
		pc1, pr1 := m.ToPixel(ts, te)
		rect := image.Rect(int(math.Floor(pc0)), int(math.Floor(pr0)), int(math.Ceil(pc1)), int(math.Ceil(pr1)))
		rect = rect.Intersect(image.Rect(0, 0, m.Width, m.Height))
		if rect.Empty() {
			continue
		}
		out = append(out, wmoJob{d: d, t: d.g.tiles[k], a: a, b: b, x0: x0, y0: y0, x1: x1, y1: y1, rect: rect})
	}
	return out
}

// draw paints one tile rectangle. The inverse of the world transform lands each output pixel in the tile's local grid;
// the pixel is drawn only when it is opaque (alpha >= 128).
func (j wmoJob) draw(img *image.NRGBA, m Meta) {
	for r := j.rect.Min.Y; r < j.rect.Max.Y; r++ {
		for c := j.rect.Min.X; c < j.rect.Max.X; c++ {
			X, Y := m.ToWorld(float64(c)+.5, float64(r)+.5)
			x, y := j.d.local(X, Y)
			if x < j.x0 || x > j.x1 || y < j.y0 || y > j.y1 {
				continue
			}
			U := (x - j.d.g.x0) * wmoPxPerYd
			V := (y - j.d.g.y0) * wmoPxPerYd
			u := int(U - float64(j.a)*wmoTilePx)
			v := j.t.Bounds().Dy() - 1 - int(V-float64(j.b)*wmoTilePx)
			if u < 0 || v < 0 || u >= j.t.Bounds().Dx() || v >= j.t.Bounds().Dy() {
				continue
			}
			px := j.t.NRGBAAt(u, v)
			if px.A < 128 {
				continue
			}
			px.A = 255
			img.SetNRGBA(c, r, px)
		}
	}
}

// placementNearNav reports whether a placement's box intersects the navmesh rectangle grown by wmoNavMargin. A placement
// with no tiles counts as near (it draws nothing).
func placementNearNav(m *wmoModel, px, py, th float64, nav navBounds) bool {
	n, s, w, e := -1e18, 1e18, -1e18, 1e18
	any := false
	d := wmoDrawable{px: px, py: py, th: th}
	for _, g := range m.groups {
		if g == nil || len(g.tiles) == 0 {
			continue
		}
		any = true
		for _, c := range [4][2]float64{{g.x0, g.y0}, {g.x0, g.y1}, {g.x1, g.y0}, {g.x1, g.y1}} {
			X, Y := d.world(c[0], c[1])
			n, s = math.Max(n, X), math.Min(s, X)
			w, e = math.Max(w, Y), math.Min(e, Y)
		}
	}
	if !any {
		return true
	}
	return n >= nav.South-wmoNavMargin && s <= nav.North+wmoNavMargin &&
		w >= nav.East-wmoNavMargin && e <= nav.West+wmoNavMargin
}

// minimapSource builds a map's minimap picture: its terrain tiles, the minimap tiles of the WMOs placed on it drawn over
// them on the five overlay maps, or the WMO tiles alone when there is no terrain picture. ok is false with a one-line
// reason when it has neither.
func minimapSource(set *mpq.Set, trs Translate, dir string, id uint32, nav *navBounds, logf func(string, ...any)) (Layer, wmoInfo, bool, string) {
	var info wmoInfo
	terrainTiles := trs.Terrain[strings.ToLower(dir)]
	hasTerrain := len(terrainTiles) > 0
	var terrain *Layer
	terrainReason := ""
	blank := false
	if hasTerrain {
		img, meta, err := stitch(set, terrainTiles)
		switch {
		case err != nil:
			terrainReason = err.Error()
			logf("map %s: terrain minimap: %v", dir, err)
		case isBlank(img):
			blank = true
			logf("map %s: the terrain minimap tiles are blank; the WMO tiles alone are used", dir)
		default:
			if !overlayMaps[id] {
				// This map's terrain minimap is already the picture: leave it exactly as the old extractor wrote it.
				return Layer{Kind: KindMinimap, Meta: meta, Img: img}, info, true, ""
			}
			meta.From = fromTerrain
			terrain = &Layer{Kind: KindMinimap, Meta: meta, Img: img}
		}
	}
	layer, info, wmoReason := composeWMO(set, trs, dir, terrain, nav, wmoSkipUIDs[id], logf)
	if layer != nil {
		if terrain == nil && isBlank(layer.Img) {
			wmoReason = "the composed WMO minimap is blank"
		} else {
			if note := wmoKnownLimits[id]; note != "" {
				layer.Meta.Note = note
			}
			return *layer, info, true, ""
		}
	}
	if terrain != nil {
		if wmoReason != "" {
			logf("map %s: no WMO minimap over the terrain: %s", dir, wmoReason)
		}
		if note := wmoKnownLimits[id]; note != "" {
			terrain.Meta.Note = note
		}
		return *terrain, info, true, ""
	}
	var parts []string
	if terrainReason != "" {
		parts = append(parts, "terrain: "+terrainReason)
	}
	if wmoReason != "" {
		switch {
		case info.Placements == 0:
			wmoReason = "no WMO placement on this map"
		case info.WDT:
			wmoReason = "indoor dungeon (A: WMO-only map): " + wmoReason
		case blank:
			wmoReason = "indoor dungeon (B: blank terrain tiles): " + wmoReason
		case !hasTerrain:
			wmoReason = "indoor dungeon (C: no terrain tiles): " + wmoReason
		}
		parts = append(parts, wmoReason)
	}
	if len(parts) == 0 {
		parts = append(parts, "no minimap tiles")
	}
	return Layer{}, info, false, oneLine(strings.Join(parts, "; "))
}

// composeWMO draws every WMO group that has tiles, the terrain picture under them when under is not nil, and returns the
// picture plus what it measured. A terrain picture is never changed: when nothing could be drawn the caller keeps it.
func composeWMO(set *mpq.Set, trs Translate, dir string, under *Layer, nav *navBounds, skip map[uint32]bool, logf func(string, ...any)) (*Layer, wmoInfo, string) {
	var info wmoInfo
	places, wdt, err := wmoPlacements(set, dir, logf)
	if err != nil {
		return nil, info, err.Error()
	}
	info.Placements, info.WDT = len(places), wdt
	if len(places) == 0 {
		return nil, info, "the client places no WMO on this map"
	}
	budget := &wmoBudget{}
	models := map[string]*wmoModel{}
	var drawables []wmoDrawable
	var problems []string
	var bases []string
	for _, p := range places {
		key := strings.ToLower(p.path)
		m := models[key]
		if m == nil {
			m = loadWMO(set, trs, p.path, budget, logf)
			models[key] = m
		}
		if m.err != nil {
			if len(problems) < 3 {
				problems = append(problems, m.base+": "+m.err.Error())
			}
			continue
		}
		bases = append(bases, m.base)
		px, py, pz := 0.0, 0.0, 0.0
		if !wdt {
			px, py, pz = wmoMapCenter-p.pos[2], wmoMapCenter-p.pos[0], p.pos[1]
		} else if p.pos != [3]float64{} {
			logf("WMO %s: the WDT MODF has position %v; the client uses (0,0,0) on a WMO-only map", m.base, p.pos)
		}
		d := wmoDrawable{px: px, py: py, pz: pz, th: p.rot[1] * math.Pi / 180}
		if skip[p.uid] {
			info.Excluded++
			logf("WMO %s uid %d: placement belongs to another zone; not drawn", m.base, p.uid)
			continue
		}
		if nav != nil && !placementNearNav(m, px, py, d.th, *nav) {
			info.Excluded++
			logf("WMO %s uid %d: its box is more than %.0f yd outside the map navmesh; not drawn", m.base, p.uid, wmoNavMargin)
			continue
		}
		for _, g := range m.groups {
			if g == nil || len(g.tiles) == 0 {
				continue
			}
			it := d
			it.g = g
			it.zw, it.zw1 = pz+g.z0, pz+g.z1
			it.wx0, it.wy0, it.wx1, it.wy1 = 1e18, 1e18, -1e18, -1e18
			for _, c := range [4][2]float64{{g.x0, g.y0}, {g.x0, g.y1}, {g.x1, g.y0}, {g.x1, g.y1}} {
				X, Y := it.world(c[0], c[1])
				it.wx0, it.wx1 = math.Min(it.wx0, X), math.Max(it.wx1, X)
				it.wy0, it.wy1 = math.Min(it.wy0, Y), math.Max(it.wy1, Y)
			}
			drawables = append(drawables, it)
			if len(drawables) > wmoMaxDrawables {
				return nil, info, fmt.Sprintf("too many WMO groups to draw (more than %d)", wmoMaxDrawables)
			}
		}
	}
	if len(drawables) == 0 {
		switch {
		case len(problems) > 0:
			return nil, info, strings.Join(problems, "; ")
		case len(bases) == 0:
			return nil, info, "none of the placed WMO files is in the client"
		default:
			listed := 0
			for _, m := range models {
				if m.err == nil {
					listed += m.listed
				}
			}
			if len(bases) > 3 {
				bases = bases[:3]
			}
			if listed == 0 {
				return nil, info, "the client lists no WMO minimap tiles for " + strings.Join(bases, ", ")
			}
			return nil, info, fmt.Sprintf("none of the %d WMO minimap tiles decoded", listed)
		}
	}
	sort.SliceStable(drawables, func(i, j int) bool { return drawables[i].zw < drawables[j].zw })

	m := Meta{Source: KindMinimap, PxPerYard: wmoPxPerYd, From: fromWMO}
	if under != nil {
		m.From = fromTerrainWMO
	}
	N, S, W, E := -1e18, 1e18, -1e18, 1e18
	info.ZMin, info.ZMax = math.Inf(1), math.Inf(-1)
	for _, d := range drawables {
		N, S = math.Max(N, d.wx1), math.Min(S, d.wx0)
		W, E = math.Max(W, d.wy1), math.Min(E, d.wy0)
		info.ZMin = math.Min(info.ZMin, d.zw)
		info.ZMax = math.Max(info.ZMax, d.zw1)
	}
	if under != nil {
		N, S = math.Max(N, under.Meta.North), math.Min(S, under.Meta.South)
		W, E = math.Max(W, under.Meta.West), math.Min(E, under.Meta.East)
		info.Tiles += under.Meta.Tiles
	}
	m.North, m.South, m.West, m.East = N, S, W, E
	if m.West <= m.East || m.North <= m.South {
		return nil, info, "the placed WMO groups have no extent"
	}
	ppy := wmoPxPerYd
	if v := wmoMaxSide / (m.West - m.East); v < ppy {
		ppy = v
	}
	if v := wmoMaxSide / (m.North - m.South); v < ppy {
		ppy = v
	}
	if ppy <= 0 {
		return nil, info, "the placed WMO groups are too wide for a picture"
	}
	m.PxPerYard = ppy
	m.Width = min(wmoMaxSide, int(math.Ceil((m.West-m.East)*ppy)))
	m.Height = min(wmoMaxSide, int(math.Ceil((m.North-m.South)*ppy)))
	if m.Width <= 0 || m.Height <= 0 {
		return nil, info, "the placed WMO groups are too narrow for a picture"
	}
	var jobs []wmoJob
	for _, d := range drawables {
		for _, j := range d.jobs(m) {
			info.Work += int64(j.rect.Dx()) * int64(j.rect.Dy())
			if info.Work > wmoMaxWork {
				return nil, info, fmt.Sprintf("the WMO tiles would sample more than %d pixels", wmoMaxWork)
			}
			jobs = append(jobs, j)
		}
	}
	img := image.NewNRGBA(image.Rect(0, 0, m.Width, m.Height))
	if under != nil {
		copyUnder(img, m, under)
	}
	for _, j := range jobs {
		j.draw(img, m)
	}
	seen := map[*wmoGroup]bool{}
	for _, d := range drawables {
		if !seen[d.g] {
			seen[d.g] = true
			info.Tiles += len(d.g.tiles)
		}
	}
	m.Tiles = info.Tiles
	return &Layer{Kind: KindMinimap, Meta: m, Img: img}, info, ""
}

// copyUnder copies the terrain picture into the composed canvas, sampling it per output pixel (nearest neighbour).
func copyUnder(dst *image.NRGBA, m Meta, under *Layer) {
	um := under.Meta
	b := under.Img.Bounds()
	for r := 0; r < m.Height; r++ {
		for c := 0; c < m.Width; c++ {
			X, Y := m.ToWorld(float64(c)+.5, float64(r)+.5)
			u, v := um.ToPixel(X, Y)
			ui, vi := int(math.Floor(u)), int(math.Floor(v))
			if !image.Pt(ui, vi).In(b) {
				continue
			}
			p := color.NRGBAModel.Convert(under.Img.At(ui, vi)).(color.NRGBA)
			dst.SetNRGBA(c, r, p)
		}
	}
}

// oneLine collapses whitespace and caps a reason at 160 characters, as the dashboard's status.json contract says.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 160 {
		s = string(r[:157]) + "..."
	}
	return s
}
