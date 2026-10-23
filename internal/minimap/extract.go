package minimap

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/pjerra/turtle-agent/internal/blp"
	"github.com/pjerra/turtle-agent/internal/mpq"
)

// ArchiveOrder is the client's load order of the archives in Data: later ones override earlier ones. The numbered patches
// (patch-2 ... patch-9, any case) come last, in numeric order.
func ArchiveOrder(dir string) []string {
	base := []string{"dbc", "fonts", "interface", "misc", "model", "sound", "speech", "terrain", "texture", "wmo", "base", "backup", "patch"}
	ents, _ := os.ReadDir(dir)
	have := map[string]string{} // lower-case name -> real name
	for _, e := range ents {
		have[strings.ToLower(e.Name())] = e.Name()
	}
	var out []string
	for _, n := range base {
		if r, ok := have[n+".mpq"]; ok {
			out = append(out, r)
		}
	}
	type num struct {
		n    int
		name string
	}
	var nums []num
	re := regexp.MustCompile(`^patch-(\d+)\.mpq$`)
	for l, r := range have {
		if m := re.FindStringSubmatch(l); m != nil {
			n, _ := strconv.Atoi(m[1])
			nums = append(nums, num{n, r})
		}
	}
	sort.Slice(nums, func(i, j int) bool { return nums[i].n < nums[j].n })
	for _, n := range nums {
		out = append(out, n.name)
	}
	return out
}

// Kinds of image a map can get.
const (
	KindMinimap  = "minimap"  // the client's minimap tiles of the terrain (Textures\Minimap)
	KindWorldMap = "worldmap" // the map picture of the area the world map shows (Interface\WorldMap\<area>)
	KindNavmesh  = "navmesh"  // a height-shaded top-down render of the server's navmesh
	KindNone     = "none"
)

// DefaultPrefer is the order the sources are tried in for the map's main image.
var DefaultPrefer = []string{KindWorldMap, KindMinimap, KindNavmesh}

// Layer is one image of a map.
type Layer struct {
	Kind  string
	Floor string // "" for the main picture, else the area suffix ("2f")
	Meta  Meta
	// Img is the picture. Run clears it after writing the files, so a run over every instance holds only the metadata;
	// ListOnly keeps the pictures.
	Img image.Image
}

// Label is the layer's short name in the --list table: its kind, what a minimap is made of, and its floor. A minimap
// with no "from" in its JSON is the old terrain-only picture.
func (l Layer) Label() string {
	s := l.Kind
	if l.Kind == KindMinimap {
		from := l.Meta.From
		if from == "" {
			from = fromTerrain
		}
		s += "(" + from + ")"
	}
	return s + l.Floor
}

// Result is what became of one map.
type Result struct {
	ID     uint32
	Name   string
	Dir    string
	Type   uint32
	Kind   string // the source of the main image <id>.png, KindNone if there is none
	Meta   Meta
	Layers []Layer // every image found, the main one first
	Note   string  // why a source gave nothing
	Error  error
}

// Options configure Run.
type Options struct {
	ClientDir string // the client's Data folder
	OutDir    string
	MmapsDirs []string // optional: server mmaps folders, for the navmesh render
	Maps      []uint32
	All       bool     // every dungeon and raid of Map.dbc
	Prefer    []string // order of the sources for <id>.png; DefaultPrefer when empty
	ListOnly  bool     // report what would be extracted, write nothing
	Log       func(format string, args ...any)
}

const translatePath = `Textures\Minimap\md5translate.trs`

// ParseMapList reads --maps: "all-instances", or ids separated by commas.
func ParseMapList(s string) (ids []uint32, all bool, err error) {
	if strings.TrimSpace(s) == "all-instances" {
		return nil, true, nil
	}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.ParseUint(p, 10, 32)
		if err != nil {
			return nil, false, fmt.Errorf("--maps: %q is not a map id", p)
		}
		ids = append(ids, uint32(n))
	}
	if len(ids) == 0 {
		return nil, false, errors.New("--maps names no map")
	}
	return ids, false, nil
}

// ParsePrefer reads --prefer: source names, comma separated, a subset of minimap, worldmap, navmesh.
func ParsePrefer(s string) ([]string, error) {
	if strings.TrimSpace(s) == "" {
		return DefaultPrefer, nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != KindMinimap && p != KindWorldMap && p != KindNavmesh {
			return nil, fmt.Errorf("--prefer: %q is not minimap, worldmap or navmesh", p)
		}
		out = append(out, p)
	}
	return out, nil
}

// Run extracts the images of the chosen maps.
func Run(o Options) ([]Result, error) {
	logf := o.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	prefer := o.Prefer
	if len(prefer) == 0 {
		prefer = DefaultPrefer
	}
	set, err := mpq.OpenSet(o.ClientDir, ArchiveOrder(o.ClientDir))
	if err != nil {
		return nil, err
	}
	defer set.Close()
	logf("archives: %s", strings.Join(baseNames(set.Archives()), " "))
	dbc, err := set.Read(`DBFilesClient\Map.dbc`)
	if err != nil {
		return nil, fmt.Errorf("Map.dbc: %w", err)
	}
	rows, err := ParseMaps(dbc)
	if err != nil {
		return nil, err
	}
	byID := map[uint32]MapRow{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	trsData, err := set.Read(translatePath)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", translatePath, err)
	}
	trs := ParseTranslate(trsData)
	var wma []WorldMapArea
	if b, err := set.Read(`DBFilesClient\WorldMapArea.dbc`); err == nil {
		if wma, err = ParseWorldMapAreas(b); err != nil {
			return nil, err
		}
	}
	var want []MapRow
	if o.All {
		for _, r := range rows {
			if r.Instance() {
				want = append(want, r)
			}
		}
	} else {
		for _, id := range o.Maps {
			r, ok := byID[id]
			if !ok {
				return nil, fmt.Errorf("map %d is not in Map.dbc", id)
			}
			want = append(want, r)
		}
	}
	sort.Slice(want, func(i, j int) bool { return want[i].ID < want[j].ID })
	if !o.ListOnly {
		if err := os.MkdirAll(o.OutDir, 0o755); err != nil {
			return nil, err
		}
	}
	var out []Result
	for _, r := range want {
		res := Result{ID: r.ID, Name: r.Name, Dir: r.Dir, Type: r.InstanceType, Kind: KindNone}
		reasons := map[string]string{} // the reason of every source the map has no image for, for <id>.status.json
		var notes []string
		have := map[string][]Layer{}
		// The navmesh serves twice: its world box drops WMO placements that stand far outside the map (Frostmane
		// Hollow's unrelated mini-hives), and its render is the navmesh layer.
		var polys []NavPoly
		var navTiles int
		var navErr error
		if len(o.MmapsDirs) > 0 {
			polys, navTiles, navErr = LoadMapPolys(o.MmapsDirs, r.ID)
		}
		var nb *navBounds
		if navErr == nil {
			nb = navBoundsOf(polys)
		}
		fail := func(err error) {
			if err != nil && res.Error == nil {
				res.Error = err
			}
		}
		if l, _, ok, reason := minimapSource(set, trs, r.Dir, r.ID, nb, logf); ok {
			have[KindMinimap] = []Layer{l}
		} else {
			reasons[KindMinimap] = reason
			notes = append(notes, reason)
		}
		if ls, err := worldMaps(set, wma, r.ID); err != nil {
			fail(err)
			reasons[KindWorldMap] = err.Error()
			notes = append(notes, err.Error())
		} else if len(ls) > 0 {
			have[KindWorldMap] = ls
		} else {
			reasons[KindWorldMap] = noWorldMapReason
			notes = append(notes, noWorldMapReason)
		}
		switch {
		case len(o.MmapsDirs) == 0:
			reasons[KindNavmesh] = noMmapsReason
			notes = append(notes, noMmapsReason)
		case navErr != nil:
			fail(navErr)
			reasons[KindNavmesh] = navErr.Error()
			notes = append(notes, navErr.Error())
		case navTiles == 0:
			reasons[KindNavmesh] = "no navmesh picture: the mmaps folder has no tile for this map"
			notes = append(notes, reasons[KindNavmesh])
		default:
			nimg, nmeta, rerr := RenderNavmesh(polys, 2, 4096)
			if rerr != nil {
				fail(rerr)
				reasons[KindNavmesh] = rerr.Error()
				notes = append(notes, rerr.Error())
			} else {
				nmeta.Tiles = navTiles
				have[KindNavmesh] = []Layer{{Kind: KindNavmesh, Meta: nmeta, Img: nimg}}
			}
		}
		for _, k := range prefer {
			if ls := have[k]; len(ls) > 0 && res.Kind == KindNone {
				res.Kind, res.Meta = k, ls[0].Meta
				res.Meta.Map, res.Meta.Name = r.ID, r.Name
			}
		}
		for _, k := range []string{res.Kind, KindMinimap, KindWorldMap, KindNavmesh} {
			if k == KindNone {
				continue
			}
			for _, l := range have[k] {
				l.Meta.Map, l.Meta.Name = r.ID, r.Name
				dup := false
				for _, e := range res.Layers {
					dup = dup || (e.Kind == l.Kind && e.Floor == l.Floor)
				}
				if !dup {
					res.Layers = append(res.Layers, l)
				}
			}
		}
		if res.Kind == KindNone {
			res.Note = strings.Join(notes, "; ")
		}
		if !o.ListOnly {
			for i, l := range res.Layers {
				if err := writeLayer(o.OutDir, r.ID, l, i == 0); err != nil {
					res.Error = err
				}
			}
			if err := writeStatus(o.OutDir, r.ID, reasons); err != nil {
				res.Error = err
			}
			// The files are the product; the pixels of a map this big must not all stay in memory until the end.
			for i := range res.Layers {
				res.Layers[i].Img = nil
			}
		}
		var kinds []string
		for _, l := range res.Layers {
			kinds = append(kinds, l.Label())
		}
		logf("map %d %s [%s]: main %s, images %s %s", r.ID, r.Name, r.Dir, res.Kind, strings.Join(kinds, ","), res.Note)
		out = append(out, res)
	}
	return out, nil
}

// One-line reasons for the sources a map often lacks, in the wording the dashboard uses when no status file says more.
const (
	noWorldMapReason = "no world map picture: the client has no WorldMapArea row for this map"
	noMmapsReason    = "no navmesh picture: minimap-extract ran without --mmaps"
)

// statusFile is the dashboard's <id>.status.json contract: a reason per source that has no picture.
type statusFile map[string]statusReason

type statusReason struct {
	Reason string `json:"reason"`
}

// writeStatus writes <id>.status.json when a source is missing and removes a stale one when every source exists.
func writeStatus(dir string, id uint32, reasons map[string]string) error {
	p := filepath.Join(dir, strconv.FormatUint(uint64(id), 10)+".status.json")
	if len(reasons) == 0 {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	sf := statusFile{}
	for _, k := range []string{KindMinimap, KindWorldMap, KindNavmesh} {
		if r := oneLine(reasons[k]); r != "" {
			sf[k] = statusReason{Reason: r}
		}
	}
	j, err := json.MarshalIndent(sf, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(j, '\n'), 0o644)
}

// isBlank reports whether an image has next to nothing on it: at least 98 % of the pixels one colour or transparent.
// It keeps at most 16 candidate colours (a Misra-Gries summary finds every colour above 1/17 of the pixels, and 98 %
// is far above that), so a large composed minimap costs no map of all its colours.
func isBlank(img image.Image) bool {
	b := img.Bounds()
	scan := func(f func(color.NRGBA) bool) {
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
				if c.A == 0 {
					continue
				}
				if !f(c) {
					return
				}
			}
		}
	}
	cand := map[color.NRGBA]int{}
	seen := 0
	scan(func(c color.NRGBA) bool {
		seen++
		if _, ok := cand[c]; ok {
			cand[c]++
		} else if len(cand) < 16 {
			cand[c] = 1
		} else {
			for k, n := range cand {
				n--
				if n <= 0 {
					delete(cand, k)
				} else {
					cand[k] = n
				}
			}
		}
		return true
	})
	if seen == 0 {
		return true
	}
	top := 0
	for c := range cand {
		n := 0
		scan(func(p color.NRGBA) bool {
			if p == c {
				n++
			}
			return true
		})
		top = max(top, n)
	}
	return float64(top) >= 0.98*float64(seen)
}

func baseNames(ps []string) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = filepath.Base(p)
	}
	return out
}

// stitch lays the tiles of one map on one image.
func stitch(set *mpq.Set, tiles map[Tile]string) (image.Image, Meta, error) {
	r0, c0, r1, c1 := 1<<30, 1<<30, -1, -1
	for t := range tiles {
		r0, r1 = min(r0, t.Row), max(r1, t.Row)
		c0, c1 = min(c0, t.Col), max(c1, t.Col)
	}
	m := Bounds(r0, c0, r1, c1, TilePx)
	if m.Width > 16384 || m.Height > 16384 {
		return nil, m, fmt.Errorf("image would be %dx%d", m.Width, m.Height)
	}
	img := image.NewNRGBA(image.Rect(0, 0, m.Width, m.Height))
	got := 0
	for t, hash := range tiles {
		data, err := set.Read(`Textures\Minimap\` + hash)
		if err != nil {
			continue // a listed tile the archives do not hold
		}
		tile, err := blp.Decode(data)
		if err != nil {
			return nil, m, fmt.Errorf("tile %d_%d (%s): %w", t.Row, t.Col, hash, err)
		}
		at := image.Pt((t.Col-c0)*TilePx, (t.Row-r0)*TilePx)
		draw.Draw(img, image.Rectangle{at, at.Add(image.Pt(TilePx, TilePx))}, tile, tile.Bounds().Min, draw.Src)
		got++
	}
	if got == 0 {
		return nil, m, errors.New("none of the listed tiles is in the archives")
	}
	m.Source, m.Tiles = KindMinimap, got
	return img, m, nil
}

// writeLayer writes <id>.<kind>[-floor].png and .json; the main image also becomes <id>.png and <id>.json.
func writeLayer(dir string, id uint32, l Layer, main bool) error {
	names := []string{strconv.FormatUint(uint64(id), 10) + "." + l.Kind + l.Floor}
	if main {
		names = append(names, strconv.FormatUint(uint64(id), 10))
	}
	for _, n := range names {
		if err := writePair(filepath.Join(dir, n), l.Meta, l.Img); err != nil {
			return err
		}
	}
	return nil
}

func writePair(base string, m Meta, img image.Image) error {
	f, err := os.Create(base + ".png")
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	j, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(base+".json", append(j, '\n'), 0o644)
}

var floorSuffix = regexp.MustCompile(`^(.*?)(\d+f)$`)

// worldMaps builds the world-map pictures of a map: the 4 x 3 tiles of Interface\WorldMap\<area>\<area><1..12>.blp cut to
// 1002 x 668 pixels, with the area's edges from WorldMapArea.dbc. An area named <base>2f, <base>3f is another floor.
func worldMaps(set *mpq.Set, rows []WorldMapArea, id uint32) ([]Layer, error) {
	var out []Layer
	base := ""
	for _, w := range rows {
		if w.Map != id || w.Name == "" || w.Left == w.Right || w.Top == w.Bottom {
			continue
		}
		floor := ""
		if m := floorSuffix.FindStringSubmatch(w.Name); m != nil && base != "" && m[1] == base {
			floor = "-" + m[2]
		} else if base == "" {
			base = w.Name
		}
		img := image.NewNRGBA(image.Rect(0, 0, 1024, 768))
		got := 0
		for n := 1; n <= 12; n++ {
			data, err := set.Read(fmt.Sprintf(`Interface\WorldMap\%s\%s%d.blp`, w.Name, w.Name, n))
			if err != nil {
				continue
			}
			tile, err := blp.Decode(data)
			if err != nil {
				return nil, fmt.Errorf("world map %s tile %d: %w", w.Name, n, err)
			}
			at := image.Pt((n-1)%4*256, (n-1)/4*256)
			draw.Draw(img, image.Rectangle{at, at.Add(image.Pt(256, 256))}, tile, tile.Bounds().Min, draw.Src)
			got++
		}
		if got == 0 {
			continue
		}
		crop := img.SubImage(image.Rect(0, 0, 1002, 668)).(*image.NRGBA)
		m := Meta{Source: KindWorldMap, Width: 1002, Height: 668, PxPerYard: 1002 / (w.Left - w.Right),
			North: w.Top, South: w.Bottom, West: w.Left, East: w.Right, Tiles: got}
		out = append(out, Layer{Kind: KindWorldMap, Floor: floor, Meta: m, Img: crop})
	}
	return out, nil
}
