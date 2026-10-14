package dashboard

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/dungeons"
	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/minimap"
)

// uldamanWorldMap is what `turtle-agent minimap-extract --maps 70` writes for the client's Uldaman world map
// (WorldMapArea.dbc: left 645.12, right -248.55, top 204.31, bottom -391.47; a 1002 x 668 picture).
var uldamanWorldMap = minimap.Meta{Map: 70, Name: "Uldaman", Source: "worldmap", Width: 1002, Height: 668, PxPerYard: 1002 / (645.12 + 248.55),
	North: 204.31, South: -391.47, West: 645.12, East: -248.55}

// pixelOf is where the page draws a world position on the picture: the drawing plane of Project, the picture at OverlayRect,
// scaled from yards to the picture's pixels.
func pixelOf(m minimap.Meta, x, y float32) (px, py float64) {
	rx, ry, rw, rh := OverlayRect(m)
	sx, sy := Project(x, y)
	return (sx - rx) / rw * float64(m.Width), (sy - ry) / rh * float64(m.Height)
}

func TestOverlayRectIsTheNorthWestCornerAndTheExtent(t *testing.T) {
	x, y, w, h := OverlayRect(uldamanWorldMap)
	// the top-left corner of the picture is world (north, west); Project turns it into (-west, -north)
	if math.Abs(x+645.12) > 1e-4 || math.Abs(y+204.31) > 1e-4 {
		t.Errorf("top-left = %v,%v", x, y)
	}
	if math.Abs(w-893.67) > 0.01 || math.Abs(h-595.78) > 0.01 {
		t.Errorf("size = %v x %v yards", w, h)
	}
	// the picture's corners are the world map's edges: south-east is (south, east) -> (-east, -south)
	if math.Abs(x+w-248.55) > 1e-3 || math.Abs(y+h-391.47) > 1e-3 {
		t.Errorf("bottom-right = %v,%v", x+w, y+h)
	}
}

// TestUldamanPlanLandsOnItsRooms checks the transform against the real picture: the pixels were read off the Uldaman world map of
// the client by eye (entrance spiral, the Map Chamber, the round Archaedas hall at the top).
func TestUldamanPlanLandsOnItsRooms(t *testing.T) {
	data, err := dungeons.FS.ReadFile("uldaman.json")
	if err != nil {
		t.Fatal(err)
	}
	p, err := dungeon.Parse("uldaman.json", data)
	if err != nil {
		t.Fatal(err)
	}
	check := func(what string, x, y float32, wantX, wantY float64) {
		t.Helper()
		px, py := pixelOf(uldamanWorldMap, x, y)
		if math.Hypot(px-wantX, py-wantY) > 25 {
			t.Errorf("%s at (%v, %v) lands on pixel %.0f,%.0f; the room is at about %.0f,%.0f", what, x, y, px, py, wantX, wantY)
		}
	}
	check("entrance", p.Entrance.X, p.Entrance.Y, 672, 486)
	var ironaya, archaedas *dungeon.Boss
	for i := range p.Legs {
		if b := p.Legs[i].Boss; b != nil && b.Entry == 7228 {
			ironaya = b
		} else if b != nil && b.Entry == 2748 {
			archaedas = b
		}
	}
	if ironaya == nil || archaedas == nil {
		t.Fatal("the plan has no Ironaya or Archaedas")
	}
	check("Ironaya (Map Chamber)", ironaya.Position.X, ironaya.Position.Y, 376, 493)
	check("Archaedas (the round hall)", archaedas.Position.X, archaedas.Position.Y, 418, 112)
	// west is left and north is up: Archaedas is north-east of the entrance...
	ex, ey := pixelOf(uldamanWorldMap, p.Entrance.X, p.Entrance.Y)
	ax, ay := pixelOf(uldamanWorldMap, archaedas.Position.X, archaedas.Position.Y)
	if !(ay < ey) {
		t.Error("Archaedas (larger x, further north) is not above the entrance")
	}
	if !(ax < ex) {
		t.Error("Archaedas (larger y, further west) is not left of the entrance")
	}
}

func writeLayerFiles(t *testing.T, dir, stem string, m minimap.Meta) {
	t.Helper()
	b, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, stem+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, stem+".png"), []byte("\x89PNG fake"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMinimapLayersAndRoutes(t *testing.T) {
	dir := t.TempDir()
	writeLayerFiles(t, dir, "70", uldamanWorldMap)
	writeLayerFiles(t, dir, "70.worldmap", uldamanWorldMap) // the main image's own copy: not listed twice
	nav := uldamanWorldMap
	nav.Source = "navmesh"
	writeLayerFiles(t, dir, "70.navmesh", nav)
	floor := uldamanWorldMap
	writeLayerFiles(t, dir, "70.worldmap-2f", floor)
	os.WriteFile(filepath.Join(dir, "209.json"), []byte("{broken"), 0o644)
	os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("no"), 0o644)

	ls := MinimapLayers(dir, 70)
	var names []string
	for _, l := range ls {
		names = append(names, l.Name+"="+l.URL)
	}
	if got, want := strings.Join(names, " "), "worldmap=/minimap/70.png navmesh=/minimap/70.navmesh.png worldmap 2f=/minimap/70.worldmap-2f.png"; got != want {
		t.Errorf("layers = %s\nwant     %s", got, want)
	}
	x, y, w, h := OverlayRect(uldamanWorldMap)
	if l := ls[0]; l.X != x || l.Y != y || l.W != w || l.H != h {
		t.Errorf("layer rect = %v %v %v %v", l.X, l.Y, l.W, l.H)
	}
	if got := MinimapLayers(dir, 209); len(got) != 0 {
		t.Errorf("a broken json gives layers: %v", got)
	}
	if MinimapLayers("", 70) != nil {
		t.Error("no folder, no layers")
	}

	p := NewPoller(Options{MinimapDir: dir, ConfigDir: t.TempDir(), Agents: "auto"})
	srv := httptest.NewServer(Handler(p, "", nil))
	defer srv.Close()
	get := func(path string) (int, string, string) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("Content-Type"), string(b)
	}
	if code, _, body := get("/api/minimap/70"); code != 200 || !strings.Contains(body, `"/minimap/70.png"`) {
		t.Errorf("/api/minimap/70 = %d %s", code, body)
	}
	for _, path := range []string{"/api/minimap/209", "/api/minimap/abc", "/api/minimap/", "/api/minimap/-1", "/api/minimap/70/x"} {
		if code, _, _ := get(path); code != 404 {
			t.Errorf("%s = %d, want 404", path, code)
		}
	}
	if code, ct, body := get("/minimap/70.navmesh.png"); code != 200 || ct != "image/png" || !strings.HasPrefix(body, "\x89PNG") {
		t.Errorf("image = %d %s", code, ct)
	}
	for _, path := range []string{"/minimap/70.json", "/minimap/secret.txt", "/minimap/../secret.txt", "/minimap/%2e%2e/x.png", "/minimap/70.paper.png", "/minimap/", "/minimap/999.png"} {
		if code, _, _ := get(path); code == 200 {
			t.Errorf("%s was served", path)
		}
	}
	// no folder configured: nothing is served
	p2 := NewPoller(Options{ConfigDir: t.TempDir(), Agents: "auto"})
	srv2 := httptest.NewServer(Handler(p2, "", nil))
	defer srv2.Close()
	for _, path := range []string{"/api/minimap/70", "/minimap/70.png"} {
		resp, err := http.Get(srv2.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Errorf("%s without a folder = %d", path, resp.StatusCode)
		}
	}
}
