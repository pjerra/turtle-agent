package minimap

// Fixtures for the review's edge cases: draw order, alpha threshold, uid dedup across ADTs, groups without tiles, the
// compositor caps, tile-rectangle work, and the terrain underlay on one of the five overlay maps (Deadmines 36).

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/mpq"
)

func reviewWMO(boxes [][6]float32) []byte {
	var mogi []byte
	for _, b := range boxes {
		e := make([]byte, 32)
		for k, v := range b {
			binary.LittleEndian.PutUint32(e[4+4*k:], math.Float32bits(v))
		}
		mogi = append(mogi, e...)
	}
	return fixChunk("MOGI", mogi)
}

func reviewWDT(flags uint32, names []string, modfs [][]byte, mainData []byte) []byte {
	var out []byte
	hdr := make([]byte, 4)
	binary.LittleEndian.PutUint32(hdr, flags)
	out = append(out, fixChunk("MPHD", hdr)...)
	var mw []byte
	for _, n := range names {
		mw = append(mw, n...)
		mw = append(mw, 0)
	}
	out = append(out, fixChunk("MWMO", mw)...)
	var mf []byte
	for _, m := range modfs {
		mf = append(mf, m...)
	}
	if len(mf) > 0 {
		out = append(out, fixChunk("MODF", mf)...)
	}
	if mainData != nil {
		out = append(out, fixChunk("MAIN", mainData)...)
	}
	return out
}

func reviewADT(wmo string, modf []byte) []byte {
	out := fixChunk("MWMO", append([]byte(wmo), 0))
	out = append(out, fixChunk("MWID", []byte{0, 0, 0, 0})...)
	return append(out, fixChunk("MODF", modf)...)
}

func solidBLP(w, h int, c color.NRGBA) []byte {
	return fixBLP(w, h, func(int, int) color.NRGBA { return c })
}

// detailBLP is a solid tile with a 16 x 16 accent block, so isBlank (which calls 98 % one colour blank) keeps the
// composed fixture picture.
func detailBLP(w, h int, bg, accent color.NRGBA) []byte {
	return fixBLP(w, h, func(x, y int) color.NRGBA {
		if x < 16 && y < 16 {
			return accent
		}
		return bg
	})
}

// reviewClient writes the review fixtures. Maps 903..909 are WMO-only; 36 also has a terrain tile, and 36 is one of the
// five maps whose WMO art is drawn over its terrain minimap.
func reviewClient(t *testing.T) string {
	t.Helper()
	var files []mpq.File
	var trs strings.Builder
	add := func(name string, data []byte) { files = append(files, mpq.File{Name: name, Data: data}) }
	tile := func(base string, g, a, b int, hash string) {
		fmt.Fprintf(&trs, "WMO\\Test\\%s_%03d_%02d_%02d.blp\t%s\n", base, g, a, b, hash)
	}
	wdt := func(dir string, flags uint32, names []string, modfs [][]byte, mainData []byte) {
		add(`World\Maps\`+dir+`\`+dir+`.wdt`, reviewWDT(flags, names, modfs, mainData))
	}
	wmo := func(base string, root []byte) { add(`World\wmo\Test\`+base+`.wmo`, root) }
	blp := func(hash string, data []byte) { add(`Textures\Minimap\`+hash, data) }
	// 903: two stacked groups, the higher box (z0=5) must paint over the lower.
	wmo("order", reviewWMO([][6]float32{{0, 0, 0, 32, 32, 10}, {0, 0, 5, 32, 32, 15}}))
	blp("order0.blp", detailBLP(64, 64, color.NRGBA{0, 0, 200, 255}, color.NRGBA{0, 200, 0, 255}))
	blp("order1.blp", detailBLP(64, 64, color.NRGBA{200, 0, 0, 255}, color.NRGBA{0, 200, 0, 255}))
	tile("order", 0, 0, 0, "order0.blp")
	tile("order", 1, 0, 0, "order1.blp")
	wdt("OrderTest", 1, []string{`World\wmo\Test\order.wmo`}, [][]byte{fixMODF(0, 1, [3]float32{}, [3]float32{})}, nil)
	// 904: alpha 100 is transparent, 128 opaque.
	wmo("alpha", reviewWMO([][6]float32{{0, 0, 0, 32, 32, 10}}))
	blp("alpha0.blp", fixBLP(64, 64, func(x, y int) color.NRGBA {
		switch {
		case y == 63 && x == 0:
			return color.NRGBA{200, 0, 0, 100}
		case y == 63 && x == 1:
			return color.NRGBA{0, 200, 0, 128}
		case x >= 16 && x < 32 && y < 16:
			return color.NRGBA{0, 0, 200, 255}
		case x >= 32 && x < 48 && y < 16:
			return color.NRGBA{200, 200, 0, 255}
		}
		return color.NRGBA{}
	}))
	tile("alpha", 0, 0, 0, "alpha0.blp")
	wdt("AlphaTest", 1, []string{`World\wmo\Test\alpha.wmo`}, [][]byte{fixMODF(0, 1, [3]float32{}, [3]float32{})}, nil)
	// 905: the same uniqueId in two ADTs; only the first placement is drawn.
	wmo("dedup", reviewWMO([][6]float32{{0, 0, 0, 32, 32, 10}}))
	blp("dedup0.blp", detailBLP(64, 64, color.NRGBA{0, 0, 200, 255}, color.NRGBA{0, 200, 0, 255}))
	tile("dedup", 0, 0, 0, "dedup0.blp")
	main := make([]byte, 64*64*8)
	binary.LittleEndian.PutUint32(main, 1)
	binary.LittleEndian.PutUint32(main[8:], 1)
	wdt("DedupTest", 0, nil, nil, main)
	add(`World\Maps\DedupTest\DedupTest_0_0.adt`, reviewADT(`World\wmo\Test\dedup.wmo`,
		fixMODF(0, 777, [3]float32{0, 0, 0}, [3]float32{})))
	add(`World\Maps\DedupTest\DedupTest_1_0.adt`, reviewADT(`World\wmo\Test\dedup.wmo`,
		fixMODF(0, 777, [3]float32{0, 0, 1000}, [3]float32{})))
	// 906: a group with no tiles must not grow the canvas.
	wmo("notiles", reviewWMO([][6]float32{{0, 0, 0, 32, 32, 10}, {1000, 1000, 0, 1064, 1064, 10}}))
	blp("notiles0.blp", detailBLP(64, 64, color.NRGBA{0, 0, 200, 255}, color.NRGBA{0, 200, 0, 255}))
	tile("notiles", 0, 0, 0, "notiles0.blp")
	wdt("NoTilesTest", 1, []string{`World\wmo\Test\notiles.wmo`}, [][]byte{fixMODF(0, 1, [3]float32{}, [3]float32{})}, nil)
	// 907: 4096 placements x 5 groups exceed the drawable cap.
	wmo("cap", reviewWMO([][6]float32{{0, 0, 0, 32, 32, 10}, {0, 0, 0, 32, 32, 10}, {0, 0, 0, 32, 32, 10}, {0, 0, 0, 32, 32, 10}, {0, 0, 0, 32, 32, 10}}))
	blp("cap0.blp", solidBLP(64, 64, color.NRGBA{0, 0, 200, 255}))
	var capModfs [][]byte
	for i := range wmoMaxPlace {
		capModfs = append(capModfs, fixMODF(0, uint32(i+1), [3]float32{}, [3]float32{}))
	}
	for g := range 5 {
		tile("cap", g, 0, 0, "cap0.blp")
	}
	wdt("CapTest", 1, []string{`World\wmo\Test\cap.wmo`}, capModfs, nil)
	// 908: 1024 tiles in a 32 x 32 group exceed the pixel-work cap.
	wmo("work", reviewWMO([][6]float32{{0, 0, 0, 4096, 4096, 10}}))
	blp("work0.blp", solidBLP(128, 128, color.NRGBA{0, 0, 200, 255}))
	for a := range 32 {
		for b := range 32 {
			tile("work", 0, a, b, "work0.blp")
		}
	}
	wdt("WorkTest", 1, []string{`World\wmo\Test\work.wmo`}, [][]byte{fixMODF(0, 1, [3]float32{}, [3]float32{})}, nil)
	// 909: one small tile in a huge group box; the draw must cost the tile, not the box.
	wmo("bigbox", reviewWMO([][6]float32{{0, 0, 0, 2048, 2048, 10}}))
	blp("bigbox0.blp", detailBLP(64, 64, color.NRGBA{0, 0, 200, 255}, color.NRGBA{0, 200, 0, 255}))
	tile("bigbox", 0, 0, 0, "bigbox0.blp")
	wdt("BigBoxTest", 1, []string{`World\wmo\Test\bigbox.wmo`}, [][]byte{fixMODF(0, 1, [3]float32{}, [3]float32{})}, nil)
	// 36: terrain minimap (left half red, right half blue) and an indoor WMO near the tile's north-west corner.
	wmo("deadmines", reviewWMO([][6]float32{{0, 0, 0, 32, 64, 10}}))
	blp("deadmines0.blp", solidBLP(64, 128, color.NRGBA{230, 140, 0, 255}))
	tile("deadmines", 0, 0, 0, "deadmines0.blp")
	blp("deadmines_terrain.blp", fixBLP(256, 256, func(x, y int) color.NRGBA {
		if x < 128 {
			return color.NRGBA{200, 0, 0, 255}
		}
		return color.NRGBA{0, 0, 200, 255}
	}))
	fmt.Fprintf(&trs, "Deadmines\\map32_32.blp\tdeadmines_terrain.blp\n")
	wdt("Deadmines", 1, []string{`World\wmo\Test\deadmines.wmo`}, [][]byte{fixMODF(0, 1, [3]float32{}, [3]float32{})}, nil)
	// 910: group 0 has an invalid (inverted) box, group 1 is valid. The group ids are positional: group 1's tile must
	// stay on group 1's box and group 0's tile must be dropped with its rejected group.
	wmo("pos", reviewWMO([][6]float32{{100, 0, 0, 50, 32, 10}, {0, 0, 0, 32, 32, 10}}))
	blp("pos0.blp", solidBLP(64, 64, color.NRGBA{200, 0, 0, 255}))
	blp("pos1.blp", detailBLP(64, 64, color.NRGBA{0, 0, 200, 255}, color.NRGBA{0, 200, 0, 255}))
	tile("pos", 0, 0, 0, "pos0.blp")
	tile("pos", 1, 0, 0, "pos1.blp")
	wdt("PosTest", 1, []string{`World\wmo\Test\pos.wmo`}, [][]byte{fixMODF(0, 1, [3]float32{}, [3]float32{})}, nil)
	// 209: a map with a terrain minimap that is not one of the five overlay maps. Its WMO art must not be drawn over it.
	wmo("keep", reviewWMO([][6]float32{{0, 0, 0, 32, 32, 10}}))
	blp("keep0.blp", solidBLP(64, 64, color.NRGBA{230, 140, 0, 255}))
	tile("keep", 0, 0, 0, "keep0.blp")
	blp("zf_terrain.blp", fixBLP(256, 256, func(x, y int) color.NRGBA {
		if x < 128 {
			return color.NRGBA{200, 0, 0, 255}
		}
		return color.NRGBA{0, 0, 200, 255}
	}))
	fmt.Fprintf(&trs, "ZulFarrak\\map32_32.blp\tzf_terrain.blp\n")
	wdt("ZulFarrak", 1, []string{`World\wmo\Test\keep.wmo`}, [][]byte{fixMODF(0, 1, [3]float32{}, [3]float32{})}, nil)
	// Map.dbc
	strs := "\x00"
	off := func(s string) uint32 {
		i := strings.Index(strs, s)
		if i < 0 {
			panic("missing string " + s)
		}
		return uint32(i)
	}
	type mapRow struct {
		id   uint32
		dir  string
		name string
	}
	rows := []mapRow{{36, "Deadmines", "Deadmines"}, {209, "ZulFarrak", "Zul'Farrak"}, {903, "OrderTest", "Order Test"}, {904, "AlphaTest", "Alpha Test"},
		{905, "DedupTest", "Dedup Test"}, {906, "NoTilesTest", "No Tiles"}, {907, "CapTest", "Cap Test"},
		{908, "WorkTest", "Work Test"}, {909, "BigBoxTest", "Big Box"}, {910, "PosTest", "Positional Groups"}}
	for _, r := range rows {
		strs += r.dir + "\x00" + r.name + "\x00"
	}
	var recs [][]uint32
	for _, r := range rows {
		rec := make([]uint32, 8)
		rec[0], rec[1], rec[2], rec[4] = r.id, off(r.dir), InstanceParty, off(r.name)
		recs = append(recs, rec)
	}
	add(`DBFilesClient\Map.dbc`, dbcFile(8, 32, recs, strs))
	add(`Textures\Minimap\md5translate.trs`, []byte(trs.String()))
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "patch.MPQ"), mpq.Build(files), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// reviewCompose composes one fixture map (no navmesh bounds).
func reviewCompose(t *testing.T, client string, id uint32) (Layer, wmoInfo, bool, string) {
	t.Helper()
	set, err := mpq.OpenSet(client, ArchiveOrder(client))
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	rows, err := ParseMaps(mustRead(t, set, `DBFilesClient\Map.dbc`))
	if err != nil {
		t.Fatal(err)
	}
	dir := ""
	for _, r := range rows {
		if r.ID == id {
			dir = r.Dir
		}
	}
	trs := ParseTranslate(mustRead(t, set, translatePath))
	return minimapSource(set, trs, dir, id, nil, t.Logf)
}

func TestWMODrawOrder(t *testing.T) {
	l, _, ok, reason := reviewCompose(t, reviewClient(t), 903)
	if !ok {
		t.Fatalf("no minimap: %s", reason)
	}
	// Both groups cover the same 32 x 32 yd box; the one with z0=5 (red) is drawn over the one with z0=0 (blue).
	if got := pixelAt(t, l, -16, -16); got != (color.NRGBA{200, 0, 0, 255}) {
		t.Errorf("the lower group is on top: pixel = %v, want red", got)
	}
}

func TestWMOAlphaThreshold(t *testing.T) {
	l, _, ok, reason := reviewCompose(t, reviewClient(t), 904)
	if !ok {
		t.Fatalf("no minimap: %s", reason)
	}
	if got := pixelAt(t, l, -0.25, -0.25); got.A != 0 {
		t.Errorf("alpha 100 is drawn: %v", got)
	}
	if got := pixelAt(t, l, -0.75, -0.25); got != (color.NRGBA{0, 200, 0, 255}) {
		t.Errorf("alpha 128 is not drawn opaque: %v", got)
	}
}

func TestWMOUIDDedup(t *testing.T) {
	l, info, ok, reason := reviewCompose(t, reviewClient(t), 905)
	if !ok {
		t.Fatalf("no minimap: %s", reason)
	}
	if info.Placements != 1 {
		t.Errorf("placements = %d, want the duplicate uid removed", info.Placements)
	}
	if l.Meta.Width != 64 || l.Meta.Height != 64 {
		t.Errorf("canvas %dx%d, want 64x64 (the second placement is not drawn)", l.Meta.Width, l.Meta.Height)
	}
	// The first ADT places the WMO at world north/west 17066; the second (uid 777, pos z 1000) would be 1000 yd away.
	if got := pixelAt(t, l, 17066.666-16, 17066.666-16); got.A == 0 {
		t.Errorf("the first placement is not drawn: %v", got)
	}
	if c, r := l.Meta.ToPixel(16066.666-16, 17066.666-16); image.Pt(int(c), int(r)).In(l.Img.Bounds()) {
		t.Errorf("the duplicate placement at (%.0f,%.0f) is inside the canvas", 16066.666-16, 17066.666-16)
	}
}

func TestWMOGroupWithoutTiles(t *testing.T) {
	l, _, ok, reason := reviewCompose(t, reviewClient(t), 906)
	if !ok {
		t.Fatalf("no minimap: %s", reason)
	}
	if l.Meta.Width != 64 || l.Meta.Height != 64 {
		t.Errorf("canvas %dx%d, want 64x64: the group without tiles grew the bounds", l.Meta.Width, l.Meta.Height)
	}
	if c, r := l.Meta.ToPixel(-1000, -1000); image.Pt(int(c), int(r)).In(l.Img.Bounds()) {
		t.Errorf("the group without tiles is inside the canvas")
	}
}

func TestWMOTooManyDrawables(t *testing.T) {
	_, _, ok, reason := reviewCompose(t, reviewClient(t), 907)
	if ok {
		t.Fatal("4096 placements of 5 groups composed without the drawable cap")
	}
	if !strings.Contains(reason, "too many WMO groups") {
		t.Errorf("reason = %q, want the drawable cap", reason)
	}
}

func TestWMOTooMuchWork(t *testing.T) {
	_, _, ok, reason := reviewCompose(t, reviewClient(t), 908)
	if ok {
		t.Fatal("1024 tiles composed without the pixel-work cap")
	}
	if !strings.Contains(reason, "would sample more than") {
		t.Errorf("reason = %q, want the pixel-work cap", reason)
	}
}

func TestWMODrawStaysInTileRectangles(t *testing.T) {
	_, info, ok, reason := reviewCompose(t, reviewClient(t), 909)
	if !ok {
		t.Fatalf("no minimap: %s", reason)
	}
	// One 128 x 128 yd tile at 2 px/yd is 65536 sampled pixels; the group box alone is 2048 yd, or 16.7 M samples.
	if info.Work <= 0 || info.Work > 100_000 {
		t.Errorf("work = %d pixels, want the tile rectangle only", info.Work)
	}
}

func TestWMOInvalidGroupKeepsPositionalIDs(t *testing.T) {
	l, info, ok, reason := reviewCompose(t, reviewClient(t), 910)
	if !ok {
		t.Fatalf("no minimap: %s", reason)
	}
	if info.Tiles != 1 {
		t.Errorf("tiles = %d, want group 1's tile only (group 0 was rejected)", info.Tiles)
	}
	if l.Meta.Width != 64 || l.Meta.Height != 64 {
		t.Errorf("canvas %dx%d, want group 1's 64x64 box", l.Meta.Width, l.Meta.Height)
	}
	// The rejected group 0's red tile must not attach to group 1 (the compacted-slice bug); the picture is group 1's.
	if got := pixelAt(t, l, -16, -16); got != (color.NRGBA{0, 0, 200, 255}) {
		t.Errorf("pixel = %v, want group 1's blue: a group id was resolved by compacted position", got)
	}
}

func TestWMOOverlayOnlyOnFiveMaps(t *testing.T) {
	l, _, ok, reason := reviewCompose(t, reviewClient(t), 209)
	if !ok {
		t.Fatalf("no minimap: %s", reason)
	}
	if l.Meta.From != "" {
		t.Errorf("from = %q, want empty: map 209 keeps the old terrain minimap", l.Meta.From)
	}
	// The terrain tile alone is 256 x 256; the WMO box would have widened the canvas.
	if l.Meta.Width != 256 || l.Meta.Height != 256 {
		t.Errorf("canvas %dx%d, want the terrain tile only", l.Meta.Width, l.Meta.Height)
	}
	if got := pixelAt(t, l, -16, -16); got != (color.NRGBA{200, 0, 0, 255}) {
		t.Errorf("pixel = %v, want the terrain red (the WMO's orange is not drawn)", got)
	}
}

func TestWMOTerrainUnderlay(t *testing.T) {
	l, _, ok, reason := reviewCompose(t, reviewClient(t), 36)
	if !ok {
		t.Fatalf("no minimap: %s", reason)
	}
	if l.Meta.From != fromTerrainWMO {
		t.Errorf("from = %q, want %q", l.Meta.From, fromTerrainWMO)
	}
	// Where there is no indoor art the terrain picture must remain; where the WMO tile lies it must cover it.
	if got := pixelAt(t, l, -300, -300); got != (color.NRGBA{0, 0, 200, 255}) {
		t.Errorf("terrain pixel = %v, want the blue half of the terrain tile", got)
	}
	if got := pixelAt(t, l, -16, -16); got != (color.NRGBA{230, 140, 0, 255}) {
		t.Errorf("WMO pixel = %v, want the orange tile over the terrain", got)
	}
}

func TestWMOTooManyDrawablesStatus(t *testing.T) {
	client, out := reviewClient(t), filepath.Join(t.TempDir(), "out")
	if _, err := Run(Options{ClientDir: client, OutDir: out, Maps: []uint32{907}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(out, "907.status.json"))
	if err != nil {
		t.Fatal("the capped map has no status file:", err)
	}
	if !strings.Contains(string(b), "too many WMO groups") {
		t.Errorf("status = %s, want the drawable cap reason", b)
	}
	if _, err := os.Stat(filepath.Join(out, "907.minimap.png")); err == nil {
		t.Error("the capped map still wrote a minimap picture")
	}
}
