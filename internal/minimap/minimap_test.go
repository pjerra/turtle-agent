package minimap

import (
	"bytes"
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

func near(a, b, eps float64) bool { return math.Abs(a-b) <= eps }

func TestTileOf(t *testing.T) {
	// The planner's mmtile rule: row from x, column from y, both 32 - v/533.33 floored.
	for _, c := range []struct {
		x, y float64
		want Tile
	}{
		{0, 0, Tile{32, 32}},
		{-228.9, 46.1, Tile{32, 31}},   // Uldaman entrance
		{1212.7, 842, Tile{29, 30}},    // Zul'Farrak entrance
		{1815.8, 670.4, Tile{28, 30}},  // Antu'sul
		{-8913, 554, Tile{48, 30}},     // Stormwind
		{17066, 17066, Tile{0, 0}},     // north-west corner of the grid
		{-17066, -17066, Tile{63, 63}}, // south-east corner
	} {
		if got := TileOf(c.x, c.y); got != c.want {
			t.Errorf("TileOf(%v, %v) = %v, want %v", c.x, c.y, got, c.want)
		}
	}
}

func TestBoundsAndPixelTransform(t *testing.T) {
	// Tiles rows 29..31, columns 27..33 at 256 px: Zul'Farrak's block.
	m := Bounds(29, 27, 31, 33, TilePx)
	if m.Width != 7*256 || m.Height != 3*256 {
		t.Fatalf("size %dx%d", m.Width, m.Height)
	}
	if !near(m.North, 3*TileSize, 1e-6) || !near(m.South, 0, 1e-6) {
		t.Errorf("north/south = %v/%v, want %v/0", m.North, m.South, 3*TileSize)
	}
	if !near(m.West, 5*TileSize, 1e-6) || !near(m.East, -2*TileSize, 1e-6) {
		t.Errorf("west/east = %v/%v", m.West, m.East)
	}
	// The top-left pixel is the north-west corner; north is up and west is left.
	if x, y := m.ToWorld(0, 0); !near(x, m.North, 1e-9) || !near(y, m.West, 1e-9) {
		t.Errorf("pixel 0,0 = world %v,%v", x, y)
	}
	px, py := m.ToPixel(m.North-100, m.West-50) // 100 yd south, 50 yd east
	if !near(px, 50*m.PxPerYard, 1e-6) || !near(py, 100*m.PxPerYard, 1e-6) {
		t.Errorf("pixel = %v,%v: 50 yd east is right, 100 yd south is down", px, py)
	}
	// A point of tile (row 30, col 30) lies in that tile's block of the image.
	x, y := (32-30.5)*TileSize, (32-30.5)*TileSize
	px, py = m.ToPixel(x, y)
	if c, r := int(px)/TilePx, int(py)/TilePx; c != 30-27 || r != 30-29 {
		t.Errorf("tile (30,30) centre lands in image tile col %d row %d, want 3, 1", c, r)
	}
	// round trip
	wx, wy := m.ToWorld(px, py)
	if !near(wx, x, 1e-6) || !near(wy, y, 1e-6) {
		t.Errorf("round trip = %v,%v, want %v,%v", wx, wy, x, y)
	}
}

func TestParseTranslate(t *testing.T) {
	in := "dir: Azeroth\nAzeroth\\map32_48.blp\tabc123.blp\n\nZulFarrak\\map29_27.blp\tf00d.blp\r\nZULFARRAK\\map30_28.blp\t0bad.blp\nbroken line\nx\\notamap.blp\tz.blp\n" +
		"WMO\\World\\wmo\\Dungeon\\kz_uldaman_b_006_03_00.blp\tw1.blp\n" +
		"wmo/world/wmo/dungeon/stratholme_b_012_04_01.blp\tw2.blp\n" +
		"WMO\\World\\wmo\\Dungeon\\mini-hive_d_000_00_00.blp\tw3.blp\n" +
		"WMO\\World\\wmo\\Dungeon\\bad_0_0.blp\tw4.blp\n" +
		"WMO\\World\\wmo\\Dungeon\\sign_+03_00_00.blp\tw5.blp\n"
	got := ParseTranslate([]byte(in))
	zf := got.Terrain["zulfarrak"]
	if len(zf) != 2 {
		t.Fatalf("zulfarrak tiles = %v", zf)
	}
	// map<A>_<B>: A is the column (world y), B the row (world x)
	if zf[Tile{Row: 27, Col: 29}] != "f00d.blp" || zf[Tile{Row: 28, Col: 30}] != "0bad.blp" {
		t.Errorf("tiles = %v", zf)
	}
	if got.Terrain["azeroth"][Tile{Row: 48, Col: 32}] != "abc123.blp" {
		t.Errorf("azeroth = %v", got.Terrain["azeroth"])
	}
	if len(got.Terrain["wmo"]) != 0 {
		t.Errorf("a WMO tile landed in the terrain tiles: %v", got.Terrain["wmo"])
	}
	// the base name keeps its underscores; the group, A and B are the last three fixed-width fields
	if got.WMO["kz_uldaman_b"][WMOTile{Group: 6, A: 3, B: 0}] != "w1.blp" {
		t.Errorf("kz_uldaman_b = %v", got.WMO["kz_uldaman_b"])
	}
	if got.WMO["stratholme_b"][WMOTile{Group: 12, A: 4, B: 1}] != "w2.blp" {
		t.Errorf("stratholme_b = %v", got.WMO["stratholme_b"])
	}
	if got.WMO["mini-hive_d"][WMOTile{Group: 0, A: 0, B: 0}] != "w3.blp" {
		t.Errorf("mini-hive_d = %v", got.WMO["mini-hive_d"])
	}
	if len(got.WMO["bad"]) != 0 {
		t.Errorf("a tile without the fixed-width numbers is accepted: %v", got.WMO["bad"])
	}
	if len(got.WMO["sign"]) != 0 {
		t.Errorf("a signed field is accepted: %v", got.WMO["sign"])
	}
}

func dbcFile(fields, recSize int, recs [][]uint32, strs string) []byte {
	var b bytes.Buffer
	b.WriteString("WDBC")
	for _, v := range []uint32{uint32(len(recs)), uint32(fields), uint32(recSize), uint32(len(strs))} {
		binary.Write(&b, binary.LittleEndian, v)
	}
	for _, r := range recs {
		for _, v := range r {
			binary.Write(&b, binary.LittleEndian, v)
		}
	}
	b.WriteString(strs)
	return b.Bytes()
}

func TestParseMaps(t *testing.T) {
	strs := "\x00Azeroth\x00Eastern Kingdoms\x00ZulFarrak\x00Zul'Farrak\x00"
	off := func(s string) uint32 { return uint32(strings.Index(strs, s)) }
	rec := func(id uint32, dir string, typ uint32, name string) []uint32 {
		r := make([]uint32, 8)
		r[0], r[1], r[2], r[4] = id, off(dir), typ, off(name)
		return r
	}
	b := dbcFile(8, 32, [][]uint32{rec(0, "Azeroth", 0, "Eastern Kingdoms"), rec(209, "ZulFarrak", 1, "Zul'Farrak")}, strs)
	rows, err := ParseMaps(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[1] != (MapRow{ID: 209, Dir: "ZulFarrak", InstanceType: 1, Name: "Zul'Farrak"}) {
		t.Errorf("rows = %+v", rows)
	}
	if rows[0].Instance() || !rows[1].Instance() || !(MapRow{InstanceType: InstanceRaid}).Instance() || (MapRow{InstanceType: InstanceBG}).Instance() {
		t.Error("Instance() = dungeons and raids only")
	}
	if _, err := ParseMaps(b[:30]); err == nil {
		t.Error("a truncated dbc parses")
	}
	if _, err := ParseMaps(dbcFile(2, 8, [][]uint32{{1, 2}}, "\x00")); err == nil {
		t.Error("a dbc with too few fields parses as Map.dbc")
	}
}

func TestParseWorldMapAreas(t *testing.T) {
	strs := "\x00Uldaman\x00"
	f := func(v float32) uint32 { return math.Float32bits(v) }
	b := dbcFile(8, 32, [][]uint32{{1, 70, 0, 0, 0, 0, 0, 0}, {2, 70, 1337, 1, f(645.12), f(-248.55), f(204.31), f(-391.47)}}, strs)
	rows, err := ParseWorldMapAreas(b)
	if err != nil {
		t.Fatal(err)
	}
	if rows[1].Name != "Uldaman" || rows[1].Map != 70 || !near(rows[1].Left, 645.12, 0.01) || !near(rows[1].Bottom, -391.47, 0.01) || rows[0].Name != "" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestParseMapListAndPrefer(t *testing.T) {
	if ids, all, err := ParseMapList("70, 209"); err != nil || all || len(ids) != 2 || ids[0] != 70 || ids[1] != 209 {
		t.Errorf("ids = %v %v %v", ids, all, err)
	}
	if _, all, err := ParseMapList("all-instances"); err != nil || !all {
		t.Errorf("all-instances = %v %v", all, err)
	}
	for _, bad := range []string{"", "x", "70,,y"} {
		if _, _, err := ParseMapList(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if p, err := ParsePrefer(""); err != nil || p[0] != KindWorldMap {
		t.Errorf("default prefer = %v %v", p, err)
	}
	if _, err := ParsePrefer("minimap,paper"); err == nil {
		t.Error("an unknown source is accepted")
	}
}

func TestArchiveOrder(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"patch-10.mpq", "patch-3.mpq", "patch-2.MPQ", "patch.MPQ", "texture.MPQ", "dbc.MPQ", "base.MPQ", "readme.txt", "Patch-4.MPQ"} {
		os.WriteFile(filepath.Join(dir, n), nil, 0o644)
	}
	got := strings.Join(ArchiveOrder(dir), " ")
	if want := "dbc.MPQ texture.MPQ base.MPQ patch.MPQ patch-2.MPQ patch-3.mpq Patch-4.MPQ patch-10.mpq"; got != want {
		t.Errorf("order = %s\nwant    %s", got, want)
	}
}

func TestParseMMTileAndRender(t *testing.T) {
	// One square polygon, Detour coordinates (y, z, x) = world (z... see the swap): world (x, y, z) = (dz, dx, dy).
	verts := [][3]float32{{0, 5, 0}, {10, 5, 0}, {10, 5, 10}, {0, 5, 10}} // detour x,y,z
	b := mmtile(verts, [][]uint16{{0, 1, 2, 3}})
	polys, err := ParseMMTile(b)
	if err != nil || len(polys) != 1 || len(polys[0].V) != 4 {
		t.Fatalf("polys = %v %v", polys, err)
	}
	if v := polys[0].V[1]; v != [3]float32{0, 10, 5} {
		t.Errorf("vertex 1 = %v, want world (x 0, y 10, z 5)", v)
	}
	img, m, err := RenderNavmesh(polys, 4, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if m.Source != KindNavmesh || m.West <= m.East || m.North <= m.South {
		t.Errorf("meta = %+v", m)
	}
	// the polygon covers world x 0..10, y 0..10: its centre is opaque, the margin is transparent
	cx, cy := m.ToPixel(5, 5)
	if img.NRGBAAt(int(cx), int(cy)).A != 255 {
		t.Error("the centre of the polygon is not painted")
	}
	if img.NRGBAAt(0, 0).A != 0 {
		t.Error("the margin is painted")
	}
	// north is up: a point further north (larger x) has a smaller row
	_, r1 := m.ToPixel(9, 5)
	_, r2 := m.ToPixel(1, 5)
	if r1 >= r2 {
		t.Error("north is not up")
	}
	if _, err := ParseMMTile(b[:50]); err == nil {
		t.Error("a short tile parses")
	}
	if _, _, err := RenderNavmesh(nil, 1, 100); err == nil {
		t.Error("an empty render succeeds")
	}
}

// mmtile builds a minimal server tile: the 20-byte header, a Detour v7 header and the vertices and polygons.
func mmtile(verts [][3]float32, polys [][]uint16) []byte {
	le := binary.LittleEndian
	b := make([]byte, mmapHdrLen+dtHdrLen)
	le.PutUint32(b, mmapMagic)
	le.PutUint32(b[4:], 7)
	le.PutUint32(b[mmapHdrLen:], dtMagic)
	le.PutUint32(b[mmapHdrLen+4:], 7)
	le.PutUint32(b[mmapHdrLen+24:], uint32(len(polys)))
	le.PutUint32(b[mmapHdrLen+28:], uint32(len(verts)))
	for _, v := range verts {
		for _, f := range v {
			b = binary.LittleEndian.AppendUint32(b, math.Float32bits(f))
		}
	}
	for _, p := range polys {
		e := make([]byte, 32)
		for i, vi := range p {
			le.PutUint16(e[4+i*2:], vi)
		}
		e[30] = byte(len(p))
		b = append(b, e...)
	}
	return b
}

// fakeClient writes a small client Data folder: Map.dbc with a tiled dungeon (map 209) and a bare one (map 70),
// the translate file, one minimap tile, WorldMapArea.dbc and a world map for 70.
func fakeClient(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	solid := func(c color.NRGBA, w, h int) []byte {
		// BGRA BLP2
		px := make([]byte, 0, w*h*4)
		for i := 0; i < w*h; i++ {
			px = append(px, c.B, c.G, c.R, c.A)
		}
		b := make([]byte, 148+1024)
		copy(b, "BLP2")
		binary.LittleEndian.PutUint32(b[4:], 1)
		b[8], b[9] = 3, 8
		binary.LittleEndian.PutUint32(b[12:], uint32(w))
		binary.LittleEndian.PutUint32(b[16:], uint32(h))
		binary.LittleEndian.PutUint32(b[20:], uint32(len(b)))
		binary.LittleEndian.PutUint32(b[84:], uint32(len(px)))
		return append(b, px...)
	}
	strs := "\x00ZulFarrak\x00Zul'Farrak\x00Uldaman\x00Uldaman\x00"
	off := func(s string) uint32 { return uint32(strings.LastIndex(strs, s)) }
	offFirst := func(s string) uint32 { return uint32(strings.Index(strs, s)) }
	rec := func(id uint32, dir uint32, typ uint32, name uint32) []uint32 {
		r := make([]uint32, 8)
		r[0], r[1], r[2], r[4] = id, dir, typ, name
		return r
	}
	mapDBC := dbcFile(8, 32, [][]uint32{rec(209, offFirst("ZulFarrak"), 1, offFirst("Zul'Farrak")), rec(70, off("Uldaman"), 1, off("Uldaman"))}, strs)
	f := func(v float32) uint32 { return math.Float32bits(v) }
	wma := dbcFile(8, 32, [][]uint32{{1, 70, 1337, off("Uldaman"), f(600), f(-400), f(300), f(-368)}}, strs)
	files := []mpq.File{
		{Name: `DBFilesClient\Map.dbc`, Data: mapDBC},
		{Name: `DBFilesClient\WorldMapArea.dbc`, Data: wma},
		{Name: `Textures\Minimap\md5translate.trs`, Data: []byte("dir: ZulFarrak\nZulFarrak\\map29_30.blp\taaa.blp\nZulFarrak\\map30_30.blp\tbbb.blp\n")},
		{Name: `Textures\Minimap\aaa.blp`, Data: solid(color.NRGBA{200, 0, 0, 255}, TilePx, TilePx)},
		{Name: `Textures\Minimap\bbb.blp`, Data: solid(color.NRGBA{0, 0, 200, 255}, TilePx, TilePx)},
	}
	patch := []mpq.File{}
	for n := 1; n <= 12; n++ {
		patch = append(patch, mpq.File{Name: `Interface\WorldMap\Uldaman\Uldaman` + itoa(n) + `.blp`, Data: solid(color.NRGBA{0, 160, 0, 255}, 256, 256)})
	}
	if err := os.WriteFile(filepath.Join(dir, "dbc.MPQ"), mpq.Build(files[:2]), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "texture.MPQ"), mpq.Build(files[2:5]), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "patch.MPQ"), mpq.Build(patch), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readPNG(t *testing.T, p string) image.Image {
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
	return img
}

func TestRunExtractsMinimapWorldmapAndNavmesh(t *testing.T) {
	client, out := fakeClient(t), filepath.Join(t.TempDir(), "out")
	mm := t.TempDir()
	// map 209 has tiles; give map 70 a one-polygon navmesh as the other layer
	os.WriteFile(filepath.Join(mm, "0700000.mmtile"), mmtile([][3]float32{{0, 5, 0}, {10, 5, 0}, {10, 5, 10}, {0, 5, 10}}, [][]uint16{{0, 1, 2, 3}}), 0o644)
	res, err := Run(Options{ClientDir: client, OutDir: out, All: true, MmapsDirs: []string{mm}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || res[0].ID != 70 || res[1].ID != 209 {
		t.Fatalf("results = %+v", res)
	}
	// 70: world map main (default order), navmesh as another layer
	if res[0].Kind != KindWorldMap || len(res[0].Layers) != 2 {
		t.Errorf("map 70: kind %s, layers %d", res[0].Kind, len(res[0].Layers))
	}
	var m Meta
	b, err := os.ReadFile(filepath.Join(out, "70.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.Width != 1002 || m.Height != 668 || m.Map != 70 || m.Name != "Uldaman" || m.West != 600 || m.East != -400 || m.North != 300 || m.South != -368 || !near(m.PxPerYard, 1.002, 1e-9) {
		t.Errorf("70.json = %+v", m)
	}
	if c := color.NRGBAModel.Convert(readPNG(t, filepath.Join(out, "70.png")).At(501, 300)).(color.NRGBA); c != (color.NRGBA{0, 160, 0, 255}) {
		t.Errorf("world map pixel = %v; the 4x3 tiles are not laid out", c)
	}
	for _, f := range []string{"70.worldmap.png", "70.navmesh.png", "70.navmesh.json", "209.minimap.png"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Errorf("missing %s", f)
		}
	}
	// 209: minimap only. File map29_30 is column 29, row 30 and map30_30 column 30, row 30: two tiles side by side.
	m209 := res[1].Meta
	if res[1].Kind != KindMinimap || m209.Width != 512 || m209.Height != 256 || m209.Tiles != 2 {
		t.Fatalf("map 209 = %+v (kind %s)", m209, res[1].Kind)
	}
	if !near(m209.North, 2*TileSize, 1e-6) || !near(m209.West, 3*TileSize, 1e-6) {
		t.Errorf("209 north/west = %v/%v, want %v/%v", m209.North, m209.West, 2*TileSize, 3*TileSize)
	}
	img209 := readPNG(t, filepath.Join(out, "209.png"))
	if c := color.NRGBAModel.Convert(img209.At(10, 10)).(color.NRGBA); c.R != 200 { // column 29 is the west (left) tile
		t.Errorf("left tile = %v, want the red one", c)
	}
	if c := color.NRGBAModel.Convert(img209.At(300, 10)).(color.NRGBA); c.B != 200 {
		t.Errorf("right tile = %v, want the blue one", c)
	}
	// preferring the minimap and a map with only one source
	res, err = Run(Options{ClientDir: client, OutDir: filepath.Join(t.TempDir(), "o2"), Maps: []uint32{70}, Prefer: []string{KindNavmesh, KindWorldMap}, MmapsDirs: []string{mm}})
	if err != nil || res[0].Kind != KindNavmesh {
		t.Errorf("prefer navmesh: %v %+v", err, res)
	}
	if _, err := Run(Options{ClientDir: client, OutDir: out, Maps: []uint32{9999}}); err == nil {
		t.Error("an unknown map is accepted")
	}
}

func TestRunListOnlyWritesNothing(t *testing.T) {
	client, out := fakeClient(t), filepath.Join(t.TempDir(), "out")
	res, err := Run(Options{ClientDir: client, OutDir: out, All: true, ListOnly: true})
	if err != nil || len(res) != 2 {
		t.Fatalf("%v %v", res, err)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("--list created the output folder")
	}
}

func TestIsBlank(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 10, 10))
	if !isBlank(img) {
		t.Error("a transparent image is blank")
	}
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	if !isBlank(img) {
		t.Error("one colour is blank")
	}
	img.SetNRGBA(1, 1, color.NRGBA{1, 2, 3, 255})
	img.SetNRGBA(2, 2, color.NRGBA{9, 2, 3, 255})
	img.SetNRGBA(3, 3, color.NRGBA{9, 9, 3, 255})
	if isBlank(img) {
		t.Error("an image with detail is not blank")
	}
}
