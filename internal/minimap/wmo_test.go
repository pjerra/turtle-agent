package minimap

import (
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/mpq"
)

// Fixture geometry (yards, group-local): group 0 is 32 x 64 yd with one 64 x 128 px tile; group 1 is 160 x 160 yd at
// (100, 200) with 2 x 2 tiles, the top row only 64 px high. The WDT map puts the WMO at (0,0,0) with yaw 0, so world
// = -local; the ADT map places it at pos (100,200,300) with yaw 90.
const (
	fixW = 64  // group 0's tile width
	fixH = 128 // group 0's tile height
)

func fixChunk(id string, data []byte) []byte {
	b := make([]byte, 8+len(data))
	b[0], b[1], b[2], b[3] = id[3], id[2], id[1], id[0]
	binary.LittleEndian.PutUint32(b[4:], uint32(len(data)))
	copy(b[8:], data)
	return b
}

func fixMODF(nameID, uid uint32, pos, rot [3]float32) []byte {
	b := make([]byte, 64)
	binary.LittleEndian.PutUint32(b[0:], nameID)
	binary.LittleEndian.PutUint32(b[4:], uid)
	for k := range 3 {
		binary.LittleEndian.PutUint32(b[8+4*k:], math.Float32bits(pos[k]))
		binary.LittleEndian.PutUint32(b[20+4*k:], math.Float32bits(rot[k]))
	}
	return b
}

// fixWMO is the root file: MOGI with the two group boxes (flags 0).
func fixWMO() []byte {
	var mogi []byte
	for _, b := range [][6]float32{{0, 0, 0, 32, 64, 10}, {100, 200, 5, 260, 360, 20}} {
		e := make([]byte, 32)
		for k, v := range b {
			binary.LittleEndian.PutUint32(e[4+4*k:], math.Float32bits(v))
		}
		mogi = append(mogi, e...)
	}
	return fixChunk("MOGI", mogi)
}

// fixBLP writes a raw BGRA BLP2 tile; paint returns each pixel.
func fixBLP(w, h int, paint func(x, y int) color.NRGBA) []byte {
	px := make([]byte, 0, w*h*4)
	for y := range h {
		for x := range w {
			c := paint(x, y)
			px = append(px, c.B, c.G, c.R, c.A)
		}
	}
	b := make([]byte, 148+1024)
	copy(b, "BLP2")
	binary.LittleEndian.PutUint32(b[4:], 1)
	b[8], b[9], b[10] = 3, 8, 0 // encoding 3 = raw BGRA
	binary.LittleEndian.PutUint32(b[12:], uint32(w))
	binary.LittleEndian.PutUint32(b[16:], uint32(h))
	binary.LittleEndian.PutUint32(b[20:], uint32(len(b)))
	binary.LittleEndian.PutUint32(b[84:], uint32(len(px)))
	return append(b, px...)
}

func fixDot(w, h int, x, y int, c color.NRGBA) []byte {
	return fixBLP(w, h, func(px, py int) color.NRGBA {
		if px == x && py == y {
			return c
		}
		return color.NRGBA{}
	})
}

// fixCorners is group 0's tile: an 8 x 8 block in the bottom-left corner (blue) and one in the bottom-right (green),
// both at the tile's bottom edge, so the v flip and the b direction are visible in the composed picture.
func fixCorners(w, h int) []byte {
	return fixBLP(w, h, func(x, y int) color.NRGBA {
		if y < h-8 {
			return color.NRGBA{}
		}
		switch {
		case x < 8:
			return color.NRGBA{0, 0, 200, 255}
		case x >= w-8:
			return color.NRGBA{0, 200, 0, 255}
		}
		return color.NRGBA{}
	})
}

// wmoFixtureClient writes a client Data folder holding two dungeon maps: 901 (the WDT places the WMO at the origin) and
// 902 (one ADT places it at pos (100,200,300), yaw 90). The WMO's minimap tiles are five BLP2 files.
func wmoFixtureClient(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	const wmoPath = `World\wmo\Test\fixture.wmo`
	names := []byte(wmoPath + "\x00")
	wdt := append(fixChunk("MPHD", []byte{1, 0, 0, 0}), fixChunk("MWMO", names)...)
	wdt = append(wdt, fixChunk("MODF", fixMODF(0, 7, [3]float32{}, [3]float32{}))...)
	adtWDT := fixChunk("MPHD", []byte{0, 0, 0, 0})
	main := make([]byte, 64*64*8)
	binary.LittleEndian.PutUint32(main, 1)
	adtWDT = append(adtWDT, fixChunk("MAIN", main)...)
	adt := append(fixChunk("MWMO", names), fixChunk("MWID", []byte{0, 0, 0, 0})...)
	adt = append(adt, fixChunk("MODF", fixMODF(0, 9, [3]float32{100, 200, 300}, [3]float32{0, 90, 0}))...)
	trs := strings.Join([]string{
		`WMO\Test\fixture_000_00_00.blp` + "\tg0.blp",
		`WMO\Test\fixture_001_00_00.blp` + "\tg100.blp",
		`WMO\Test\fixture_001_01_00.blp` + "\tg110.blp",
		`WMO\Test\fixture_001_00_01.blp` + "\tg101.blp",
		`WMO\Test\fixture_001_01_01.blp` + "\tg111.blp",
		"",
	}, "\n")
	strs := "\x00WMOOnly\x00Wdt Dungeon\x00ADTMap\x00Adt Dungeon\x00"
	off := func(s string) uint32 { return uint32(strings.Index(strs, s)) }
	rec := func(id uint32, dir string, name string) []uint32 {
		r := make([]uint32, 8)
		r[0], r[1], r[2], r[4] = id, off(dir), InstanceParty, off(name)
		return r
	}
	mapDBC := dbcFile(8, 32, [][]uint32{rec(901, "WMOOnly", "Wdt Dungeon"), rec(902, "ADTMap", "Adt Dungeon")}, strs)
	files := []mpq.File{
		{Name: `DBFilesClient\Map.dbc`, Data: mapDBC},
		{Name: `Textures\Minimap\md5translate.trs`, Data: []byte(trs)},
		{Name: `Textures\Minimap\g0.blp`, Data: fixCorners(fixW, fixH)},
		{Name: `Textures\Minimap\g100.blp`, Data: fixDot(256, 256, 0, 255, color.NRGBA{0, 0, 200, 255})},
		{Name: `Textures\Minimap\g110.blp`, Data: fixDot(256, 256, 0, 255, color.NRGBA{0, 200, 0, 255})},
		{Name: `Textures\Minimap\g101.blp`, Data: fixDot(256, 64, 0, 63, color.NRGBA{255, 255, 0, 255})},
		{Name: `Textures\Minimap\g111.blp`, Data: fixDot(256, 64, 0, 63, color.NRGBA{255, 0, 255, 255})},
		{Name: `World\Maps\WMOOnly\WMOOnly.wdt`, Data: wdt},
		{Name: `World\Maps\ADTMap\ADTMap.wdt`, Data: adtWDT},
		{Name: `World\Maps\ADTMap\ADTMap_0_0.adt`, Data: adt},
		{Name: wmoPath, Data: fixWMO()},
	}
	if err := os.WriteFile(filepath.Join(dir, "patch.MPQ"), mpq.Build(files), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// composed returns the minimap layer of a fixture map and its metadata.
func composed(t *testing.T, client string, id uint32) (Layer, wmoInfo) {
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
	l, info, ok, reason := minimapSource(set, trs, dir, t.Logf)
	if !ok {
		t.Fatalf("no minimap: %s", reason)
	}
	return l, info
}

func mustRead(t *testing.T, set *mpq.Set, name string) []byte {
	t.Helper()
	b, err := set.Read(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return b
}

func pixelAt(t *testing.T, l Layer, x, y float64) color.NRGBA {
	t.Helper()
	c, r := l.Meta.ToPixel(x, y)
	img := l.Img.(*image.NRGBA)
	if !image.Pt(int(c), int(r)).In(img.Bounds()) {
		return color.NRGBA{}
	}
	return img.NRGBAAt(int(c), int(r))
}

func TestWMOOnlyTransform(t *testing.T) {
	client := wmoFixtureClient(t)
	l, info := composed(t, client, 901)
	if l.Meta.From != fromWMO || l.Meta.Source != KindMinimap {
		t.Fatalf("from %q source %q, want wmo/minimap", l.Meta.From, l.Meta.Source)
	}
	if info.Tiles != 5 || info.Placements != 1 || !info.WDT {
		t.Fatalf("info = %+v", info)
	}
	// World = -local (the extra 180 degrees). Each pinned point samples the bottom-left pixel of one tile; the tile
	// pins are the only opaque pixels in the fixture.
	for _, c := range []struct {
		name string
		x, y float64
		want color.NRGBA
	}{
		{"group 0 bottom-left", -1.75, -1.75, color.NRGBA{0, 0, 200, 255}},
		{"group 0 bottom-right", -30.25, -1.75, color.NRGBA{0, 200, 0, 255}},
		{"group 1 a0 b0", -100.25, -200.25, color.NRGBA{0, 0, 200, 255}},
		{"group 1 a1 b0", -228.25, -200.25, color.NRGBA{0, 200, 0, 255}},
		{"group 1 a0 b1 (partial top row)", -100.25, -328.25, color.NRGBA{255, 255, 0, 255}},
		{"group 1 a1 b1 (partial top row)", -228.25, -328.25, color.NRGBA{255, 0, 255, 255}},
	} {
		if got := pixelAt(t, l, c.x, c.y); got != c.want {
			t.Errorf("%s at (%v,%v) = %v, want %v", c.name, c.x, c.y, got, c.want)
		}
	}
	// The top-left pixel of the 64 x 128 tile is transparent: without the v flip the bottom-left block would be there.
	if got := pixelAt(t, l, -0.25, -63.75); got.A != 0 {
		t.Errorf("the top-left pixel is painted %v; the tile is not bottom-left anchored", got)
	}
}

func TestADTPlacementTransform(t *testing.T) {
	client := wmoFixtureClient(t)
	l, info := composed(t, client, 902)
	if info.WDT || info.Placements != 1 {
		t.Fatalf("info = %+v, want one ADT placement", info)
	}
	// PX = 17066.666 - pos[2] = 16766.666, PY = 17066.666 - pos[0] = 16966.666, yaw 90 with the extra 180:
	// world X = PX + local y, world Y = PY - local x. The tile is 64 x 128 with an 8 x 8 block in each bottom corner.
	const px, py = wmoMapCenter - 300, wmoMapCenter - 100
	for _, c := range []struct {
		name string
		x, y float64
		want color.NRGBA
	}{
		{"bottom-left block", px + 1.75, py - 1.75, color.NRGBA{0, 0, 200, 255}},
		{"bottom-right block", px + 1.75, py - 30.25, color.NRGBA{0, 200, 0, 255}},
	} {
		if got := pixelAt(t, l, c.x, c.y); got != c.want {
			t.Errorf("%s at (%v,%v) = %v, want %v", c.name, c.x, c.y, got, c.want)
		}
	}
}

func TestWMOComposedGolden(t *testing.T) {
	client := wmoFixtureClient(t)
	l, _ := composed(t, client, 901)
	golden := filepath.Join("testdata", "wmo-fixture.png")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(golden)
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, l.Img); err != nil {
			f.Close()
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		return
	}
	want := readPNG(t, golden)
	got := l.Img.(*image.NRGBA)
	if got.Bounds() != want.Bounds() {
		t.Fatalf("bounds %v, golden %v", got.Bounds(), want.Bounds())
	}
	diff := 0
	for y := got.Bounds().Min.Y; y < got.Bounds().Max.Y; y++ {
		for x := got.Bounds().Min.X; x < got.Bounds().Max.X; x++ {
			c := color.NRGBAModel.Convert(want.At(x, y)).(color.NRGBA)
			if c.A < 128 {
				c = color.NRGBA{}
			} else {
				c.A = 255
			}
			if got.NRGBAAt(x, y) != c {
				if diff == 0 {
					t.Errorf("first difference at (%d,%d): %v, golden %v", x, y, got.NRGBAAt(x, y), c)
				}
				diff++
			}
		}
	}
	if diff > 0 {
		t.Errorf("%d pixels differ from testdata/wmo-fixture.png", diff)
	}
}

func TestStatusFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "7.status.json")
	if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeStatus(dir, 7, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("a stale status file is not removed: %v", err)
	}
	reasons := map[string]string{
		KindMinimap:  "line one\nline two",
		KindWorldMap: strings.Repeat("x", 300),
	}
	if err := writeStatus(dir, 7, reasons); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var sf statusFile
	if err := json.Unmarshal(b, &sf); err != nil {
		t.Fatal(err)
	}
	if got := sf[KindMinimap].Reason; got != "line one line two" {
		t.Errorf("minimap reason = %q, want one line", got)
	}
	if got := sf[KindWorldMap].Reason; len([]rune(got)) != 160 || !strings.HasSuffix(got, "...") {
		t.Errorf("worldmap reason %d runes %q, want 160 ending in ...", len([]rune(got)), got)
	}
	if _, ok := sf[KindNavmesh]; ok {
		t.Error("an absent reason wrote a navmesh entry")
	}
	if len(b) > 64<<10 {
		t.Errorf("status file %d bytes, want at most 64 KiB", len(b))
	}
}

func TestRunWMOFixtureStatusAndJSON(t *testing.T) {
	client, out := wmoFixtureClient(t), filepath.Join(t.TempDir(), "out")
	res, err := Run(Options{ClientDir: client, OutDir: out, Maps: []uint32{901}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Kind != KindMinimap || res[0].Meta.From != fromWMO {
		t.Fatalf("result = %+v", res)
	}
	var m Meta
	b, err := os.ReadFile(filepath.Join(out, "901.minimap.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.From != fromWMO || m.Source != KindMinimap || m.Tiles != 5 {
		t.Errorf("901.minimap.json = %+v", m)
	}
	sb, err := os.ReadFile(filepath.Join(out, "901.status.json"))
	if err != nil {
		t.Fatal(err)
	}
	var sf statusFile
	if err := json.Unmarshal(sb, &sf); err != nil {
		t.Fatal(err)
	}
	if _, ok := sf[KindMinimap]; ok {
		t.Errorf("the produced minimap still has a reason: %+v", sf)
	}
	if sf[KindWorldMap].Reason == "" || sf[KindNavmesh].Reason == "" {
		t.Errorf("missing sources have no reason: %+v", sf)
	}
}
