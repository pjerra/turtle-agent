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

func slotLabels(ls []MinimapLayer) string {
	var names []string
	for _, l := range ls {
		names = append(names, l.Label)
	}
	return strings.Join(names, ", ")
}

func landOf(t *testing.T, dir string, id uint32) [3]MinimapLayer {
	t.Helper()
	ls := MinimapSlots(dir, id)
	if len(ls) != 3 {
		t.Fatalf("slots = %d (%s), want the three kinds", len(ls), slotLabels(ls))
	}
	return [3]MinimapLayer{ls[0], ls[1], ls[2]}
}

// The folder of Zul'Farrak: the main picture is the world map's copy, and every source has its own file. The main copy is not
// listed a second time.
func TestMinimapSlotsZulFarrak(t *testing.T) {
	dir := t.TempDir()
	writeLayerFiles(t, dir, "209", uldamanWorldMap)
	writeLayerFiles(t, dir, "209.worldmap", uldamanWorldMap)
	mini := uldamanWorldMap
	mini.Source = "minimap"
	writeLayerFiles(t, dir, "209.minimap", mini)
	nav := uldamanWorldMap
	nav.Source = "navmesh"
	writeLayerFiles(t, dir, "209.navmesh", nav)

	ls := MinimapSlots(dir, 209)
	if got, want := slotLabels(ls), "World map, Minimap, Navmesh"; got != want {
		t.Fatalf("labels = %s, want %s", got, want)
	}
	for i, kind := range []string{"worldmap", "minimap", "navmesh"} {
		l := ls[i]
		if l.Kind != kind || l.Floor != "" || l.Name != l.Label || !l.Available || l.Reason != "" {
			t.Errorf("%d = %+v", i, l)
		}
	}
	if ls[0].URL != "/minimap/209.worldmap.png" || ls[1].URL != "/minimap/209.minimap.png" || ls[2].URL != "/minimap/209.navmesh.png" {
		t.Errorf("urls = %s, %s, %s", ls[0].URL, ls[1].URL, ls[2].URL)
	}
	x, y, w, h := OverlayRect(uldamanWorldMap)
	if l := ls[0]; l.X != x || l.Y != y || l.W != w || l.H != h {
		t.Errorf("rectangle = %v %v %v %v", l.X, l.Y, l.W, l.H)
	}
	if got := MinimapSlots("", 209); got != nil {
		t.Errorf("no folder gives slots: %v", got)
	}
	if got := MinimapSlots(dir, 70); len(got) != 3 {
		t.Errorf("another map = %v", got)
	}
}

// Uldaman has no minimap: the slot is still there, unavailable, with the built-in reason.
func TestMinimapSlotMissingKind(t *testing.T) {
	dir := t.TempDir()
	writeLayerFiles(t, dir, "70", uldamanWorldMap)
	writeLayerFiles(t, dir, "70.worldmap", uldamanWorldMap)
	nav := uldamanWorldMap
	nav.Source = "navmesh"
	writeLayerFiles(t, dir, "70.navmesh", nav)

	l := landOf(t, dir, 70)
	if !l[0].Available || l[0].Label != "World map" || !l[2].Available || l[2].Label != "Navmesh" {
		t.Fatalf("world map or navmesh = %+v / %+v", l[0], l[2])
	}
	if l[1].Available || l[1].MinimapPicture != nil || l[1].Source != "minimap" || l[1].Kind != "minimap" {
		t.Errorf("minimap = %+v, want unavailable", l[1])
	}
	if l[1].Reason != kindWhy["minimap"] || !strings.Contains(l[1].Reason, "WMO tiles") {
		t.Errorf("minimap reason = %q", l[1].Reason)
	}
}

// Scarlet Monastery: the main world map plus floors 2f-4f, then the missing minimap, then the navmesh. The floors are sorted by
// number, whatever order the folder lists them in.
func TestMinimapSlotsFloors(t *testing.T) {
	dir := t.TempDir()
	writeLayerFiles(t, dir, "189", uldamanWorldMap)
	writeLayerFiles(t, dir, "189.worldmap", uldamanWorldMap)
	for _, fl := range []string{"10f", "3f", "2f"} {
		m := uldamanWorldMap
		m.Name = "Scarlet Monastery " + fl
		writeLayerFiles(t, dir, "189.worldmap-"+fl, m)
	}
	nav := uldamanWorldMap
	nav.Source = "navmesh"
	writeLayerFiles(t, dir, "189.navmesh", nav)

	ls := MinimapSlots(dir, 189)
	if got, want := slotLabels(ls), "World map, World map 2f, World map 3f, World map 10f, Minimap, Navmesh"; got != want {
		t.Fatalf("labels = %s, want %s", got, want)
	}
	for i, fl := range []string{"", "2f", "3f", "10f"} {
		if ls[i].Kind != "worldmap" || ls[i].Floor != fl || !ls[i].Available {
			t.Errorf("%d = %+v", i, ls[i])
		}
	}
	if ls[2].URL != "/minimap/189.worldmap-3f.png" || ls[2].Label != "World map 3f" {
		t.Errorf("3f = %+v", ls[2])
	}
	if ls[3].URL != "/minimap/189.worldmap-10f.png" || ls[3].Label != "World map 10f" {
		t.Errorf("10f = %+v", ls[3])
	}
	if ls[4].Available || ls[4].Kind != "minimap" || ls[5].Kind != "navmesh" {
		t.Errorf("after the floors: %+v / %+v", ls[4], ls[5])
	}
}

// An old folder has only the main picture. It stands in for the one source it names and is never the minimap or navmesh too.
func TestMinimapSlotsOldFolder(t *testing.T) {
	dir := t.TempDir()
	writeLayerFiles(t, dir, "70", uldamanWorldMap)

	l := landOf(t, dir, 70)
	if !l[0].Available || l[0].URL != "/minimap/70.png" || l[0].Kind != "worldmap" {
		t.Errorf("world map = %+v", l[0])
	}
	for _, i := range []int{1, 2} {
		if l[i].Available || l[i].Reason == "" {
			t.Errorf("slot %d = %+v, want unavailable with a reason", i, l[i])
		}
	}
	// a main picture of another source stands in for that source
	mini := uldamanWorldMap
	mini.Source = "minimap"
	dir2 := t.TempDir()
	writeLayerFiles(t, dir2, "70", mini)
	l = landOf(t, dir2, 70)
	if l[0].Available || !l[1].Available || l[1].URL != "/minimap/70.png" {
		t.Errorf("main minimap folder = %+v", l)
	}
}

// <id>.status.json gives a slot's own reason. An oversized or malformed file, an unknown kind, or a file outside the folder's
// allow-list leaves the built-in reasons; a long string is cut to 160 characters.
func TestMinimapSlotStatusReasons(t *testing.T) {
	dir := t.TempDir()
	writeLayerFiles(t, dir, "70", uldamanWorldMap)
	status := `{"minimap":{"reason":"no indoor art yet"},"navmesh":{"reason":"run without --mmaps"},"bogus":{"reason":"x"}}`
	if err := os.WriteFile(filepath.Join(dir, "70.status.json"), []byte(status), 0o644); err != nil {
		t.Fatal(err)
	}
	l := landOf(t, dir, 70)
	if l[1].Reason != "no indoor art yet" || l[2].Reason != "run without --mmaps" {
		t.Errorf("reasons = %q / %q", l[1].Reason, l[2].Reason)
	}

	// malformed: the defaults stand
	if err := os.WriteFile(filepath.Join(dir, "70.status.json"), []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	if l := landOf(t, dir, 70); l[1].Reason != kindWhy["minimap"] || l[2].Reason != kindWhy["navmesh"] {
		t.Errorf("malformed reasons = %q / %q", l[1].Reason, l[2].Reason)
	}

	// oversized: the file is not read at all
	if err := os.WriteFile(filepath.Join(dir, "70.status.json"), []byte(strings.Repeat(" ", 1<<16)+status), 0o644); err != nil {
		t.Fatal(err)
	}
	if l := landOf(t, dir, 70); l[1].Reason != kindWhy["minimap"] {
		t.Errorf("oversized reason = %q", l[1].Reason)
	}

	// control characters become spaces: one line is all the panel has
	ctrl := "line one\nline two\ttab\r\x00nul\x7fdel"
	b, _ := json.Marshal(map[string]map[string]string{"minimap": {"reason": ctrl}})
	if err := os.WriteFile(filepath.Join(dir, "70.status.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if l := landOf(t, dir, 70); l[1].Reason != "line one line two tab  nul del" {
		t.Errorf("control characters = %q", l[1].Reason)
	}

	// a long string is cut
	long := strings.Repeat("x", 300)
	b, _ = json.Marshal(map[string]map[string]string{"minimap": {"reason": long}})
	if err := os.WriteFile(filepath.Join(dir, "70.status.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if l := landOf(t, dir, 70); len([]rune(l[1].Reason)) != maxReasonRunes || l[1].Reason != strings.Repeat("x", 160) {
		t.Errorf("long reason = %d characters", len([]rune(l[1].Reason)))
	}

	// another map's status file is not read
	if err := os.WriteFile(filepath.Join(dir, "71.status.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if l := landOf(t, dir, 70); l[1].Reason != strings.Repeat("x", 160) {
		t.Errorf("71.status.json changed 70: %q", l[1].Reason)
	}

	// a status file that is a symbolic link out of the folder is refused
	outside := t.TempDir()
	target := filepath.Join(outside, "70.status.json")
	if err := os.WriteFile(target, []byte(status), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(dir, "70.status.json"))
	if err := os.Symlink(target, filepath.Join(dir, "70.status.json")); err != nil {
		t.Skip("no symbolic links here")
	}
	if l := landOf(t, dir, 70); l[1].Reason != kindWhy["minimap"] {
		t.Errorf("symlinked status reason = %q", l[1].Reason)
	}
	if got := statusReasons(dir, ".."); got != nil {
		t.Errorf("a relative name was read: %v", got)
	}
}

// A map id can be anything: an empty folder is still answered with three unavailable slots, and no folder gives 404.
func TestMinimapSlotsAndRoutes(t *testing.T) {
	dir := t.TempDir()
	writeLayerFiles(t, dir, "70", uldamanWorldMap)
	writeLayerFiles(t, dir, "70.worldmap", uldamanWorldMap)
	nav := uldamanWorldMap
	nav.Source = "navmesh"
	writeLayerFiles(t, dir, "70.navmesh", nav)
	os.WriteFile(filepath.Join(dir, "209.json"), []byte("{broken"), 0o644)
	os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("no"), 0o644)
	os.WriteFile(filepath.Join(dir, "70.status.png"), []byte("\x89PNG fake"), 0o644)
	os.WriteFile(filepath.Join(dir, "70.status-2f.png"), []byte("\x89PNG fake"), 0o644)

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
	if code, _, body := get("/api/minimap/70"); code != 200 || !strings.Contains(body, `"label":"World map"`) || !strings.Contains(body, `"/minimap/70.navmesh.png"`) {
		t.Errorf("/api/minimap/70 = %d %s", code, body)
	}
	// a map with nothing in the folder: 200 and the three missing slots, each without picture fields
	code, _, body := get("/api/minimap/999")
	if code != 200 {
		t.Fatalf("/api/minimap/999 = %d %s", code, body)
	}
	var slots []map[string]any
	if err := json.Unmarshal([]byte(body), &slots); err != nil {
		t.Fatal(err)
	}
	if len(slots) != 3 {
		t.Fatalf("999 slots = %v", body)
	}
	for i, kind := range []string{"worldmap", "minimap", "navmesh"} {
		if slots[i]["kind"] != kind || slots[i]["available"] != false || slots[i]["reason"] == "" {
			t.Errorf("999 slot %d = %v", i, slots[i])
		}
		for _, k := range []string{"url", "x", "y", "w", "h"} {
			if _, ok := slots[i][k]; ok {
				t.Errorf("999 slot %d has %s: %v", i, k, slots[i])
			}
		}
	}
	// a broken main file is a map with no picture, not a server error
	if code, _, body := get("/api/minimap/209"); code != 200 || !strings.Contains(body, `"available":false`) {
		t.Errorf("/api/minimap/209 = %d %s", code, body)
	}
	for _, path := range []string{"/api/minimap/abc", "/api/minimap/", "/api/minimap/-1", "/api/minimap/70/x"} {
		if code, _, _ := get(path); code != 404 {
			t.Errorf("%s = %d, want 404", path, code)
		}
	}
	if code, ct, body := get("/minimap/70.navmesh.png"); code != 200 || ct != "image/png" || !strings.HasPrefix(body, "\x89PNG") {
		t.Errorf("image = %d %s", code, ct)
	}
	for _, path := range []string{"/minimap/70.json", "/minimap/70.status.json", "/minimap/70.status.png", "/minimap/70.status-2f.png", "/minimap/secret.txt", "/minimap/../secret.txt", "/minimap/%2e%2e/x.png", "/minimap/70.paper.png", "/minimap/", "/minimap/999.png"} {
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
