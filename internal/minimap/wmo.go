package minimap

// The minimap the client draws indoors comes from tiles of the WMO's own groups, not from the terrain. md5translate lists
// them as WMO\...\<base>_<group>_<A>_<B>.blp, and the root WMO's MOGI gives each group's bounding box. This file places
// those groups (with the WDT or ADT MODF of each WMO) on one north-up picture and draws the tiles over the terrain minimap
// when the map has one. The transform was settled against the server's navmesh (W2a): 2 px per yard, a tile index step
// of 128 yd, bottom-left anchored tiles, a 180-degree turn, th = +rot.y, and all groups drawn by PZ+z0 rising.

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
	wmoPxPerYd    = 2.0       // the client's indoor minimap scale
	wmoTilePx     = 256.0     // a tile index step in pixels at that scale
	wmoTileYd     = 128.0     // ... in yards
	wmoMapCenter  = 17066.666 // the ADT placement convention: 32 * 533.333
	wmoMaxSide    = 8192      // the longest side of a composed picture
	wmoMaxTiles   = 4096      // decoded tiles per map
	wmoTileBudget = 256 << 20 // decoded tile pixels per map, in bytes (256 tiles of 256x256 are 16 MiB)
	wmoMaxTilePx  = 512       // the client's tiles are 256 px at most
	wmoMaxPlace   = 4096      // placements per map
	wmoMaxADTs    = 64        // an instance has at most 36 ADTs; a world map's WDT is not scanned
	wmoMaxGroups  = 4096      // MOGI groups per WMO
)

const (
	fromTerrain    = "terrain"
	fromWMO        = "wmo"
	fromTerrainWMO = "terrain+wmo"
)

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
	Placements int     // MODF entries found
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
	var mwmo, modf, main []byte
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
			main = append(main, d...)
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
		if (i+1)*8 > len(main) {
			break
		}
		if le32(main, i*8) == 0 {
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
	for i := range min(len(mogi)/32, wmoMaxGroups) {
		o := i * 32
		g := &wmoGroup{index: i, flags: le32(mogi, o),
			x0: f32(mogi, o+4), y0: f32(mogi, o+8), z0: f32(mogi, o+12),
			x1: f32(mogi, o+16), y1: f32(mogi, o+20), z1: f32(mogi, o+24),
			tiles: map[[2]int]*image.NRGBA{}}
		if !saneBox(g) {
			logf("WMO %s group %d: implausible bounding box; skipped", base, i)
			continue
		}
		m.groups = append(m.groups, g)
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
		if t.Group < 0 || t.Group >= len(m.groups) {
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

// minimapSource builds a map's minimap picture: its terrain tiles, the minimap tiles of the WMOs placed on it drawn over
// them, or the WMO tiles alone when there is no terrain picture. ok is false with a one-line reason when it has neither.
func minimapSource(set *mpq.Set, trs Translate, dir string, logf func(string, ...any)) (Layer, wmoInfo, bool, string) {
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
			meta.From = fromTerrain
			terrain = &Layer{Kind: KindMinimap, Meta: meta, Img: img}
		}
	}
	layer, info, wmoReason := composeWMO(set, trs, dir, terrain, logf)
	if layer != nil {
		if terrain == nil && isBlank(layer.Img) {
			wmoReason = "the composed WMO minimap is blank"
		} else {
			return *layer, info, true, ""
		}
	}
	if terrain != nil {
		if wmoReason != "" {
			logf("map %s: no WMO minimap over the terrain: %s", dir, wmoReason)
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
func composeWMO(set *mpq.Set, trs Translate, dir string, under *Layer, logf func(string, ...any)) (*Layer, wmoInfo, string) {
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
		for _, g := range m.groups {
			if len(g.tiles) == 0 {
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
	img := image.NewNRGBA(image.Rect(0, 0, m.Width, m.Height))
	if under != nil {
		copyUnder(img, m, under)
	}
	for _, d := range drawables {
		d.draw(img, m)
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

// draw paints one group's tiles into the canvas. It walks the group's bounding rectangle in the canvas and samples each
// output pixel back into the group's local grid, so rotation and scale are exact at every pixel.
func (d wmoDrawable) draw(img *image.NRGBA, m Meta) {
	c0, r0 := m.ToPixel(d.wx1, d.wy1)
	c1, r1 := m.ToPixel(d.wx0, d.wy0)
	x0, x1 := int(math.Floor(c0)), int(math.Ceil(c1))
	y0, y1 := int(math.Floor(r0)), int(math.Ceil(r1))
	x0, x1 = max(x0, 0), min(x1, m.Width-1)
	y0, y1 = max(y0, 0), min(y1, m.Height-1)
	for r := y0; r <= y1; r++ {
		for c := x0; c <= x1; c++ {
			X, Y := m.ToWorld(float64(c)+.5, float64(r)+.5)
			x, y := d.local(X, Y)
			if x < d.g.x0 || x > d.g.x1 || y < d.g.y0 || y > d.g.y1 {
				continue
			}
			px, ok := d.g.pixelAt(x, y)
			if !ok || px.A < 128 {
				continue
			}
			px.A = 255
			img.SetNRGBA(c, r, px)
		}
	}
}

// pixelAt maps a group-local point to a pixel of its tile: U = 2(x-x0) counts along local x and Vup = 2(y-y0) up from
// y0. The tile's pixels are bottom-left anchored, so image row H-1 holds y0's edge.
func (g *wmoGroup) pixelAt(x, y float64) (color.NRGBA, bool) {
	U := (x - g.x0) * wmoPxPerYd
	V := (y - g.y0) * wmoPxPerYd
	a := int(math.Floor(U / wmoTilePx))
	b := int(math.Floor(V / wmoTilePx))
	t := g.tiles[[2]int{a, b}]
	if t == nil {
		return color.NRGBA{}, false
	}
	u := int(U - float64(a)*wmoTilePx)
	v := t.Bounds().Dy() - 1 - int(V-float64(b)*wmoTilePx)
	if u < 0 || v < 0 || u >= t.Bounds().Dx() || v >= t.Bounds().Dy() {
		return color.NRGBA{}, false
	}
	return t.NRGBAAt(u, v), true
}

// oneLine collapses whitespace and caps a reason at 160 characters, as the dashboard's status.json contract says.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 160 {
		s = string(r[:157]) + "..."
	}
	return s
}
