package mapview

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"flag"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/packet"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// golden compares got with testdata/name; with -update it rewrites the file.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (go test ./internal/mapview -update writes it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs from the golden file (%d bytes, want %d); go test ./internal/mapview -update rewrites it after a look at the diff", name, len(got), len(want))
	}
}

// wellFormed fails unless the bytes are one XML document.
func wellFormed(t *testing.T, doc []byte) {
	t.Helper()
	dec := xml.NewDecoder(bytes.NewReader(doc))
	for {
		if _, err := dec.Token(); err != nil {
			if err == io.EOF {
				return
			}
			t.Fatalf("not well-formed XML: %v", err)
		}
	}
}

var t0 = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

func at(sec float64) time.Time { return t0.Add(time.Duration(sec * float64(time.Second))) }

// squares is a grid of n x n cells of side step at height z, from (x0, y0).
func squares(x0, y0, step float32, n int, z float32) []navmesh.Polygon {
	var out []navmesh.Polygon
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			a, b := x0+float32(i)*step, y0+float32(j)*step
			out = append(out, navmesh.Polygon{Flags: navmesh.NavGround, Verts: []navmesh.Vec3{{a, b, z}, {a + step, b, z}, {a + step, b + step, z}, {a, b + step, z}}})
		}
	}
	return out
}

// twoFloors is a hall with a gallery over its middle: 12 x 12 cells of 5 yd at z 0, 6 x 6 cells at z 20 above them,
// and a pool of water beside the hall.
func twoFloors() []navmesh.Polygon {
	polys := squares(0, 0, 5, 12, 0)
	polys = append(polys, squares(15, 15, 5, 6, 20)...)
	pool := squares(65, 0, 5, 3, 0)
	for i := range pool {
		pool[i].Flags = navmesh.NavWater
	}
	return append(polys, pool...)
}

func testPlan(t *testing.T) *dungeon.Plan {
	t.Helper()
	p, err := dungeon.Parse("test.json", []byte(`{"version":1,"name":"hall","map":5,"entrance":{"x":2,"y":2,"z":0},"legs":[
	 {"name":"gallery","to":{"x":30,"y":30,"z":20},"pull_groups":[{"entries":[100,100],"positions":[{"x":10,"y":12,"z":0},{"x":12,"y":14,"z":0}]}],
	  "steps":[{"use_object":{"entry":900,"near":{"x":32,"y":28,"z":20},"stand":{"x":31,"y":28,"z":20}}}],
	  "boss":{"entry":200,"position":{"x":40,"y":40,"z":20}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// runFixture builds a small run by hand: two members, a wolf pair that dies, an ogre that evades, a skipped ogre, a
// boss killed on the gallery, a death, a door, a GM teleport, a wipe, and a hole in Bob's route.
func runFixture(t *testing.T) (Run, *dungeon.Plan) {
	t.Helper()
	plan := testPlan(t)
	cfg := DefaultConfig()
	cfg.Map = 5
	cfg.MinMove = 0.1
	b := NewBuilder(cfg)
	b.SetBoss(BossEntries(plan)...)
	wolf := func(g uint64, x, y float32, dead, combat bool) UnitFrame {
		return UnitFrame{GUID: g, Entry: 100, Name: "Cave Wolf", Level: 40, Health: 100, MaxHealth: 3000, InCombat: combat, Dead: dead, Pos: Pos{X: x, Y: y}}
	}
	for i := 0; i <= 120; i++ {
		s := float64(i) * 5
		alice := Pos{X: 2 + float32(i)*0.5, Y: 2 + float32(i)*0.5, Z: 0}
		if i >= 60 { // up to the gallery
			alice.Z = 20
		}
		f := Frame{T: at(s), Map: 5, Members: []MemberFrame{{Name: "Alice", Pos: alice, Self: true}}}
		if i < 40 || i > 70 { // Bob is out of sight in between
			f.Members = append(f.Members, MemberFrame{Name: "Bob", Pos: Pos{X: alice.X - 2, Y: alice.Y + 1, Z: alice.Z}})
		}
		f.Hostiles = []UnitFrame{
			wolf(1, 10, 12, i >= 20, i >= 10 && i < 20),
			wolf(2, 12, 14, i >= 21, i >= 10 && i < 21),
			{GUID: 3, Entry: 101, Name: "Ogre", Level: 42, MaxHealth: 4000, InCombat: i >= 30 && i < 34, Pos: Pos{X: 50, Y: 8}},
			{GUID: 4, Entry: 101, Name: "Sleepy Ogre", Level: 42, MaxHealth: 4000, Pos: Pos{X: 56, Y: 50}},
			{GUID: 5, Entry: 200, Name: "Gallery King", Level: 45, Health: 60000, MaxHealth: 60000, InCombat: i >= 80 && i < 100, Dead: i >= 100, Pos: Pos{X: 40, Y: 40, Z: 20}},
			{GUID: 6, Entry: 102, Name: "Straggler", Level: 40, MaxHealth: 3000, InCombat: i >= 105, Pos: Pos{X: 20, Y: 60}},
		}
		f.Objects = []ObjectFrame{{GUID: 77, Entry: 900, Name: "Gallery Lever", Type: "button", Pos: Pos{X: 32, Y: 28, Z: 20}}}
		b.AddFrame(f)
	}
	var evs []events.Event
	add := func(e events.Event) { evs = append(evs, e) }
	add(events.Event{Time: at(50), Kind: events.KindAttackStart, Source: "Alice", Target: "Cave Wolf", TargetGUID: 1, Party: true})
	add(events.Event{Time: at(52), Kind: events.KindMelee, Source: "Cave Wolf", SourceGUID: 1, Target: "Alice", Party: true})
	add(events.Event{Time: at(100), Kind: events.KindKill, Source: "Alice", Target: "Cave Wolf", TargetGUID: 1, Party: true})
	add(events.Event{Time: at(100), Kind: events.KindDeath, Source: "Cave Wolf", SourceGUID: 1})
	add(events.Event{Time: at(105), Kind: events.KindDeath, Source: "Cave Wolf", SourceGUID: 2})
	add(events.Event{Time: at(150), Kind: events.KindAttackStart, Source: "Bob", Target: "Ogre", TargetGUID: 3, Party: true})
	add(events.Event{Time: at(402), Kind: events.KindAttackStart, Source: "Alice", Target: "Gallery King", TargetGUID: 5, Party: true})
	add(events.Event{Time: at(500), Kind: events.KindKill, Source: "Alice", Target: "Gallery King", TargetGUID: 5, Party: true})
	add(events.Event{Time: at(300), Kind: events.KindDeath, Source: "Bob", SourceGUID: 0x10, Party: true})
	add(events.Event{Time: at(360), Kind: events.KindGameObject, SourceGUID: 77, Data: map[string]any{"change": "state", "old": "ready", "new": "active", "entry": float64(900), "type": "button"}})
	add(events.Event{Time: at(380), Kind: events.KindGameObject, SourceGUID: 77, Data: map[string]any{"change": "flags", "old": float64(1), "new": float64(2)}})
	add(events.Event{Time: at(310), Kind: events.KindSession, Data: map[string]any{"state": "teleported", "x": float64(5), "y": float64(5), "z": float64(0)}})
	add(events.Event{Time: at(200), Kind: events.KindSession, Data: map[string]any{"state": "teleported", "x": float64(30), "y": float64(30), "z": float64(0)}})
	add(events.Event{Time: at(1), Kind: events.KindDungeonLeg, Text: "leg 0 gallery: start", Data: map[string]any{"leg": float64(0), "name": "gallery", "phase": "start"}})
	add(events.Event{Time: at(590), Kind: events.KindDungeonFailed, Text: "dungeon hall failed: wipe", Data: map[string]any{"error": "wipe"}})
	rep := &dungeon.Report{Plan: "hall", Map: 5, Started: at(0),
		Shortcuts: []dungeon.Shortcut{{Kind: "teleport_to_leg_start", Leg: 0, Detail: "go leg", At: at(311)}, {Kind: "revive", Leg: 0, Detail: "Bob", At: at(320)}},
		Deaths:    []dungeon.Death{{Name: "Bob", Leg: 0, At: at(300)}}}
	return b.Build(evs, rep), plan
}

func TestBuildTracksCreaturesAndMarkers(t *testing.T) {
	run, _ := runFixture(t)
	if run.Map != 5 || run.Title != "hall (map 5)" || run.Seconds != 600 || len(run.Tracks) != 2 || !run.Tracks[0].Leader || run.Tracks[0].Name != "Alice" {
		t.Fatalf("%+v", run)
	}
	// A standing or slow member keeps few samples; Bob's hole (frames 40..70) is a gap.
	if run.Gaps != 1 || len(run.Tracks[0].Samples) != 121 {
		t.Fatalf("gaps %d, Alice %d samples", run.Gaps, len(run.Tracks[0].Samples))
	}
	bosses := 0
	for _, c := range run.Creatures {
		if c.Boss {
			bosses++
		}
	}
	byGUID := map[uint64]Creature{}
	for _, c := range run.Creatures {
		byGUID[c.GUID] = c
	}
	want := map[uint64]Fate{1: FateKilled, 2: FateKilled, 3: FateEvaded, 4: FateSkipped, 5: FateKilled, 6: FateAlive}
	for g, f := range want {
		if byGUID[g].Fate != f {
			t.Errorf("creature %d: fate %q, want %q", g, byGUID[g].Fate, f)
		}
	}
	if !byGUID[5].Boss || bosses != 1 || byGUID[1].Pack == 0 || byGUID[1].Pack != byGUID[2].Pack || byGUID[3].Pack != 0 {
		t.Errorf("boss/pack: %+v", byGUID)
	}
	if got := byGUID[1].FateT; got != 100 { // the kill event's time beats the later corpse frame
		t.Errorf("wolf 1 died at %v", got)
	}
	kinds := map[string]int{}
	var tp []Marker
	for _, m := range run.Markers {
		kinds[m.Kind]++
		if m.Kind == MarkTeleport {
			tp = append(tp, m)
		}
	}
	// Pulls: wolves at 50, ogre at 150, the king at 402; the melee at 52 is the same fight.
	if kinds[MarkPull] != 3 || kinds[MarkDeath] != 1 || kinds[MarkWipe] != 1 || kinds[MarkDoor] != 1 || kinds[MarkLeg] != 1 ||
		kinds[MarkBossKill] != 1 || kinds[MarkTeleport] != 2 || kinds[MarkGM] != 1 {
		t.Fatalf("%v", kinds)
	}
	if len(tp) != 2 || tp[0].T != 200 || tp[0].Flag || tp[1].T != 310 || !tp[1].Flag || !strings.Contains(tp[1].Label, "teleport_to_leg_start") || tp[1].From == nil {
		t.Errorf("teleports %+v", tp)
	}
	// A deleted-from-the-log duplicate: the report's death of Bob is the event's, not a second marker.
	if kinds[MarkDeath] != 1 {
		t.Errorf("death markers %d", kinds[MarkDeath])
	}
}

func TestTrackAtInterpolatesAndClamps(t *testing.T) {
	tr := Track{Samples: []Sample{{T: 10, Pos: Pos{X: 0, Y: 0, Z: 0}}, {T: 20, Pos: Pos{X: 10, Y: 20, Z: 4}}}}
	for _, c := range []struct {
		t    float64
		want Pos
	}{{0, Pos{}}, {10, Pos{}}, {15, Pos{X: 5, Y: 10, Z: 2}}, {99, Pos{X: 10, Y: 20, Z: 4}}} {
		if got, ok := tr.At(c.t); !ok || got != c.want {
			t.Errorf("At(%v) = %v %v, want %v", c.t, got, ok, c.want)
		}
	}
	if _, ok := (&Track{}).At(1); ok {
		t.Error("an empty track has a position")
	}
}

func TestBuilderKeepsFewSamplesOfAStandingMemberAndBoundsMemory(t *testing.T) {
	b := NewBuilder(DefaultConfig())
	for i := 0; i < 100; i++ {
		b.AddFrame(Frame{T: at(float64(i)), Map: 1, Members: []MemberFrame{{Name: "A", Pos: Pos{X: 5, Y: 5}}}})
	}
	if n := b.Samples(); n != 10 { // one at the start, then one every 10 s
		t.Fatalf("%d samples of a standing member", n)
	}
	for i := 0; i < maxSamplesPerMember+500; i++ { // a walker keeps every sample, up to the cap
		b.AddFrame(Frame{T: at(1000 + float64(i)), Map: 1, Members: []MemberFrame{{Name: "B", Pos: Pos{X: float32(i) * 3}}}})
	}
	if n := b.Samples(); n > maxSamplesPerMember+20 {
		t.Fatalf("%d samples kept, cap %d", n, maxSamplesPerMember)
	}
}

func TestBuilderMapAutoPicksTheBusiestAndLateFramesSort(t *testing.T) {
	b := NewBuilder(DefaultConfig())
	b.AddFrame(Frame{T: at(30), Map: 70, Members: []MemberFrame{{Name: "A", Pos: Pos{X: 3}}}})
	b.AddFrame(Frame{T: at(10), Map: 70, Members: []MemberFrame{{Name: "A", Pos: Pos{X: 1}}}})
	b.AddFrame(Frame{T: at(20), Map: 70, Members: []MemberFrame{{Name: "A", Pos: Pos{X: 2}}}})
	b.AddFrame(Frame{T: at(25), Map: 0, Members: []MemberFrame{{Name: "A", Pos: Pos{X: 9}}}})
	run := b.Build(nil, nil)
	if run.Map != 70 || run.Title != "run on map 70" {
		t.Fatalf("%d %q", run.Map, run.Title)
	}
	s := run.Tracks[0].Samples
	for i := 1; i < len(s); i++ {
		if s[i].T < s[i-1].T {
			t.Fatalf("samples out of order: %+v", s)
		}
	}
	b.SetMap(0)
	if r := b.Build(nil, nil); r.Map != 0 {
		t.Fatalf("map %d, want the map asked for (0 is a map too)", r.Map)
	}
}

func TestSplitHeightsFindsTheGalleryAndLeavesRampsAlone(t *testing.T) {
	cuts := SplitHeights(twoFloors(), DefaultFloorOptions())
	if len(cuts) != 1 || cuts[0] < 8 || cuts[0] > 12 {
		t.Fatalf("cuts %v, want one near 10", cuts)
	}
	// A ramp over the whole hall and a hill: heights differ, nothing lies over anything.
	var ramp []navmesh.Polygon
	for i := 0; i < 20; i++ {
		x := float32(i) * 5
		ramp = append(ramp, navmesh.Polygon{Flags: navmesh.NavGround, Verts: []navmesh.Vec3{{x, 0, x / 2}, {x + 5, 0, x/2 + 2.5}, {x + 5, 5, x/2 + 2.5}, {x, 5, x / 2}}})
	}
	if cuts := SplitHeights(ramp, DefaultFloorOptions()); len(cuts) != 0 {
		t.Fatalf("a ramp split into floors at %v", cuts)
	}
	if cuts := SplitHeights(nil, DefaultFloorOptions()); cuts != nil {
		t.Fatalf("%v", cuts)
	}
	// A bridge too small to matter (a few cells) is not a floor.
	small := append(squares(0, 0, 5, 12, 0), squares(0, 0, 3, 1, 20)...)
	if cuts := SplitHeights(small, DefaultFloorOptions()); len(cuts) != 0 {
		t.Fatalf("a footbridge made floors at %v", cuts)
	}
}

func TestFloorsSplitThePolygonsAndFollowTheCuts(t *testing.T) {
	fl := buildFloors(twoFloors(), []float32{10})
	if len(fl) != 2 || len(fl[0].Polys) != 144+9 || len(fl[1].Polys) != 36 {
		t.Fatalf("%d floors, %d and %d polygons", len(fl), len(fl[0].Polys), len(fl[1].Polys))
	}
	if FloorIndex([]float32{10, 30}, 9.9) != 0 || FloorIndex([]float32{10, 30}, 10) != 1 || FloorIndex([]float32{10, 30}, 31) != 2 || FloorIndex(nil, 5) != 0 {
		t.Fatal("FloorIndex")
	}
	for _, c := range []struct {
		in    string
		cuts  []float32
		auto  bool
		fails bool
	}{{"", nil, true, false}, {"auto", nil, true, false}, {"off", nil, false, false}, {"-30, -10", []float32{-30, -10}, false, false},
		{"5,5", nil, false, true}, {"x", nil, false, true}, {"NaN", nil, false, true}} {
		cuts, auto, err := ParseCuts(c.in)
		if (err != nil) != c.fails || auto != c.auto || len(cuts) != len(c.cuts) {
			t.Errorf("ParseCuts(%q) = %v %v %v", c.in, cuts, auto, err)
		}
	}
}

func fixtureScene(t *testing.T, auto bool) *Scene {
	t.Helper()
	run, plan := runFixture(t)
	sc, err := NewScene(run, SceneOptions{Polys: twoFloors(), Auto: auto, Overlay: OverlayFromPlan(plan), Pad: 10})
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

func TestGoldenSVGOfTheHandBuiltRun(t *testing.T) {
	sc := fixtureScene(t, true)
	if sc.FloorCount() != 2 || len(sc.Cuts) != 1 {
		t.Fatalf("floors %d cuts %v", sc.FloorCount(), sc.Cuts)
	}
	for i := 0; i < sc.FloorCount(); i++ {
		svg := sc.SVG(i)
		wellFormed(t, svg)
		golden(t, "hall-floor"+string(rune('1'+i))+".golden.svg", svg)
	}
	// The same run twice gives the same bytes.
	if !bytes.Equal(sc.SVG(0), fixtureScene(t, true).SVG(0)) {
		t.Fatal("the SVG is not deterministic")
	}
	// One floor: everything on one image.
	one := fixtureScene(t, false)
	if one.FloorCount() != 1 || !bytes.Contains(one.SVG(0), []byte("Gallery King")) {
		t.Fatalf("floors %d", one.FloorCount())
	}
}

func TestFloorImagesShowOnlyWhatIsOnThatFloor(t *testing.T) {
	sc := fixtureScene(t, true)
	lower, upper := string(sc.SVG(0)), string(sc.SVG(1))
	// The boss, the lever and the gallery waypoint are upstairs; the wolves and the hall's door-less floor are down.
	for _, want := range []string{"Gallery King (killed", "Gallery Lever", "planned boss 200", "use 900"} {
		if strings.Contains(lower, want) || !strings.Contains(upper, want) {
			t.Errorf("%q: lower %v, upper %v", want, strings.Contains(lower, want), strings.Contains(upper, want))
		}
	}
	for _, want := range []string{"Cave Wolf", "Sleepy Ogre"} {
		if !strings.Contains(lower, want) || strings.Contains(upper, want) {
			t.Errorf("%q: lower %v, upper %v", want, strings.Contains(lower, want), strings.Contains(upper, want))
		}
	}
	if !strings.Contains(lower, "floor 1") || !strings.Contains(upper, "floor 2") {
		t.Error("the floor is not named in the title")
	}
}

func TestPlanOverlayIsDrawnAndLabelled(t *testing.T) {
	sc := fixtureScene(t, false)
	svg := string(sc.SVG(0))
	for _, want := range []string{`class="plan"`, "entrance", "1 gallery", "planned boss 200", "use 900", "planned walk through the legs"} {
		if !strings.Contains(svg, want) {
			t.Errorf("no %q in the SVG", want)
		}
	}
	run, _ := runFixture(t)
	bare, err := NewScene(run, SceneOptions{Polys: twoFloors()})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bare.SVG(0)), `class="plan"`) {
		t.Error("a plan was drawn without one")
	}
	o := OverlayFromPlan(testPlan(t))
	if len(o.Route) != 2 || len(o.Groups) != 1 || len(o.Bosses) != 1 || len(o.Uses) != 1 || o.Uses[0].Pos.X != 31 {
		t.Errorf("%+v", o)
	}
	if got := BossEntries(testPlan(t)); len(got) != 1 || got[0] != 200 {
		t.Errorf("boss entries %v", got)
	}
}

func TestNamesAreEscapedAndNothingLoadsFromOutside(t *testing.T) {
	cfg := DefaultConfig()
	b := NewBuilder(cfg)
	evil := `<script>alert(1)</script>&"'` + "\x00\x1b"
	b.AddFrame(Frame{T: at(0), Map: 1, Members: []MemberFrame{{Name: evil, Self: true, Pos: Pos{X: 1, Y: 1}}},
		Hostiles: []UnitFrame{{GUID: 1, Name: "</title><g onload=x>", Pos: Pos{X: 3, Y: 3}}}})
	b.AddFrame(Frame{T: at(60), Map: 1, Members: []MemberFrame{{Name: evil, Self: true, Pos: Pos{X: 30, Y: 30}}}})
	sc, err := NewScene(b.Build(nil, nil), SceneOptions{})
	if err != nil {
		t.Fatal(err)
	}
	svg := sc.SVG(0)
	wellFormed(t, svg)
	html, err := sc.HTML()
	if err != nil {
		t.Fatal(err)
	}
	for name, doc := range map[string][]byte{"svg": svg, "html": html} {
		s := string(doc)
		if strings.Contains(s, "<script>alert") || strings.Contains(s, "</title><g") || strings.ContainsAny(s, "\x00\x1b") {
			t.Errorf("%s: unescaped name", name)
		}
		for _, bad := range []string{"src=", "href=", "@import", "url(http", "<link", "<img"} {
			if strings.Contains(s, bad) {
				t.Errorf("%s loads something: %q", name, bad)
			}
		}
	}
	if n := strings.Count(string(html), "<script"); n != 2 {
		t.Errorf("%d script blocks, want the data block and the player", n)
	}
}

func TestHTMLReplayCarriesTheDataAndTheFloors(t *testing.T) {
	sc := fixtureScene(t, true)
	doc, err := sc.HTML()
	if err != nil {
		t.Fatal(err)
	}
	s := string(doc)
	if !strings.HasPrefix(s, "<!doctype html>") || strings.Count(s, `class="floor"`) != 2 || strings.Count(s, `class="tab"`) != 2 ||
		!strings.Contains(s, `id="slider"`) || strings.Count(s, `<g class="dyn">`) != 2 {
		t.Fatalf("shape: %d bytes", len(s))
	}
	start := strings.Index(s, `id="run-data">`) + len(`id="run-data">`)
	end := start + strings.Index(s[start:], "</script>")
	var d replayData
	if err := json.Unmarshal([]byte(s[start:end]), &d); err != nil {
		t.Fatal(err)
	}
	if d.Seconds != 600 || d.Floors != 2 || len(d.Tracks) != 2 || len(d.Creatures) != 6 || len(d.Tracks[0].Pts) != 121 {
		t.Fatalf("%+v", d)
	}
	// Alice climbs at frame 60: the points above the cut are on floor 1 (0-based), and Bob's hole is a break.
	if d.Tracks[0].Pts[0][3] != 0 || d.Tracks[0].Pts[100][3] != 1 {
		t.Errorf("floors of the points: %v %v", d.Tracks[0].Pts[0], d.Tracks[0].Pts[100])
	}
	breaks := 0
	for _, p := range d.Tracks[1].Pts {
		if p[4] == 1 {
			breaks++
		}
	}
	if breaks != 1 {
		t.Errorf("%d breaks in Bob's route", breaks)
	}
	if strings.Contains(s, "http://") && strings.Count(s, "http://") != strings.Count(s, "http://www.w3.org/2000/svg") {
		t.Error("the page refers to another site")
	}
	// A map of one floor has no tabs.
	one, err := fixtureScene(t, false).HTML()
	if err != nil || strings.Contains(string(one), `class="tab"`) {
		t.Fatalf("%v: tabs on a one-floor map", err)
	}
}

func TestSceneRefusesAnEmptyRunAndBoundsFollowTheRun(t *testing.T) {
	if _, err := NewScene(Run{Map: 3}, SceneOptions{}); err == nil {
		t.Fatal("an empty run drew")
	}
	run, plan := runFixture(t)
	box, ok := run.Bounds(10, 60, OverlayFromPlan(plan).Points()...)
	if !ok || box.MinX > 2-10+0.01 || box.MaxX < 62 || box.Width() <= 0 {
		t.Fatalf("%+v", box)
	}
	// A creature seen from far off does not stretch the box; one close to the route does.
	far := run
	far.Creatures = append([]Creature{{GUID: 99, Map: 5, Pos: Pos{X: 5000, Y: 5000}}}, run.Creatures...)
	if b2, _ := far.Bounds(10, 60); b2.MaxX > 1000 {
		t.Fatalf("a creature 5000 yd away stretched the box: %+v", b2)
	}
	if _, ok := (&Run{Map: 1}).Bounds(10, 60); ok {
		t.Fatal("bounds of nothing")
	}
	// A single position still gets a box of the minimum size.
	tiny := Run{Map: 1, Tracks: []Track{{Samples: []Sample{{Map: 1, Pos: Pos{X: 100, Y: 100}}}}}}
	if b3, ok := tiny.Bounds(5, 60); !ok || math.Abs(float64(b3.Height())-60) > 0.01 {
		t.Fatalf("%+v", b3)
	}
}

func TestTickIntervalsAndClockFormats(t *testing.T) {
	for _, c := range []struct {
		sec  float64
		want float64
	}{{100, 15}, {600, 60}, {4000, 600}, {40000, 3600}} {
		if got := tickInterval(c.sec); got != c.want {
			t.Errorf("tickInterval(%v) = %v, want %v", c.sec, got, c.want)
		}
	}
	for sec, want := range map[float64]string{5: "0:05", 65: "1:05", 3725: "1:02:05"} {
		if got := fmtClock(sec); got != want {
			t.Errorf("fmtClock(%v) = %q", sec, got)
		}
	}
	for sec, want := range map[float64]string{30: "30s", 300: "5m", 90: "1m30", 3600: "1h", 5400: "1h30"} {
		if got := fmtTick(sec); got != want {
			t.Errorf("fmtTick(%v) = %q", sec, got)
		}
	}
}

func TestParseEventsReadsArraysPagesAndLines(t *testing.T) {
	e1 := `{"seq":1,"time":"2026-10-13T12:00:00Z","mono_ms":1,"kind":"kill","source_guid":"0x0000000000000010","target":"Wolf","target_guid":"0xF130000100000001"}`
	e2 := `{"seq":2,"time":"2026-10-13T12:00:01Z","mono_ms":2,"kind":"death"}`
	for name, in := range map[string]string{"array": "[" + e1 + "," + e2 + "]", "page": `{"events":[` + e1 + "," + e2 + `],"next":2}`, "lines": e1 + "\n" + e2 + "\n"} {
		evs, err := ParseEvents([]byte(in))
		if err != nil || len(evs) != 2 || evs[0].Kind != "kill" || evs[0].TargetGUID != packet.GUID(0xF130000100000001) || evs[1].Seq != 2 {
			t.Errorf("%s: %v %+v", name, err, evs)
		}
	}
	if evs, err := ParseEvents([]byte("  \n")); err != nil || evs != nil {
		t.Errorf("empty: %v %v", evs, err)
	}
	for _, bad := range []string{"nonsense", `[{"seq":"x"}]`, `{"events":5}`} {
		if _, err := ParseEvents([]byte(bad)); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestAddBotwatchAndReadReport(t *testing.T) {
	dir := t.TempDir()
	bw := filepath.Join(dir, "bw.jsonl")
	line := func(ts float64, name string, m int, x float64) string {
		b, _ := json.Marshal(map[string]any{"t": ts, "bots": []map[string]any{{"name": name, "map": m, "x": x, "y": 5, "z": 0}, {"name": "Stranger", "map": m, "x": 9, "y": 9, "z": 0}}})
		return string(b) + "\n"
	}
	base := float64(t0.Unix())
	data := line(base, "Cod", 70, 1) + "\n" + line(base+5, "Cod", 70, 20) + "not json\n" + line(base+9, "Cod", 0, 3)
	if err := os.WriteFile(bw, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	b := NewBuilder(DefaultConfig())
	n, err := AddBotwatch(b, bw, []string{"Cod"})
	if err != nil || n != 3 {
		t.Fatalf("%d %v", n, err)
	}
	run := b.Build(nil, nil)
	if len(run.Tracks) != 1 || run.Tracks[0].Name != "Cod" || run.Map != 70 || len(run.Tracks[0].Samples) != 3 {
		t.Fatalf("%+v", run.Tracks)
	}
	if _, err := AddBotwatch(b, filepath.Join(dir, "nope"), nil); err == nil {
		t.Fatal("a missing file read")
	}

	bare := `{"plan":"hall","map":5,"state":"failed","started":"2026-10-13T12:00:00Z","wipes":1}`
	for name, doc := range map[string]string{"bare": bare, "status": `{"running":false,"report":` + bare + `}`, "rpc": `{"jsonrpc":"2.0","result":{"state":"failed","report":` + bare + `}}`} {
		p := filepath.Join(dir, name+".json")
		os.WriteFile(p, []byte(doc), 0o600)
		rep, err := ReadReport(p)
		if err != nil || rep.Plan != "hall" || rep.Wipes != 1 {
			t.Errorf("%s: %v %+v", name, err, rep)
		}
	}
	empty := filepath.Join(dir, "empty.json")
	os.WriteFile(empty, []byte(`{"unrelated":1}`), 0o600)
	if _, err := ReadReport(empty); err == nil {
		t.Error("a file with no report read")
	}
}

func TestWindowKeepsOneStretchOfARun(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Map = 5
	cfg.MinMove = 0.1
	cfg.From, cfg.To = at(100), at(200)
	b := NewBuilder(cfg)
	for i := 0; i <= 60; i++ {
		b.AddFrame(Frame{T: at(float64(i) * 10), Map: 5, Members: []MemberFrame{{Name: "A", Self: true, Pos: Pos{X: float32(i), Y: 1}}},
			Hostiles: []UnitFrame{{GUID: uint64(i%3) + 1, Name: "Wolf", Pos: Pos{X: 5, Y: float32(i % 3)}}}})
	}
	evs := []events.Event{
		{Time: at(50), Kind: events.KindDeath, Source: "A", Party: true},  // before the window
		{Time: at(150), Kind: events.KindDeath, Source: "A", Party: true}, // inside
		{Time: at(500), Kind: events.KindDeath, Source: "A", Party: true}, // after
	}
	rep := &dungeon.Report{Plan: "x", Started: at(0), Deaths: []dungeon.Death{{Name: "A", At: at(60)}, {Name: "A", At: at(150)}}}
	run := b.Build(evs, rep)
	if n := len(run.Tracks[0].Samples); n != 11 || run.Seconds != 100 || !run.Start.Equal(at(100)) {
		t.Fatalf("%d samples, %v s from %v", n, run.Seconds, run.Start)
	}
	if len(run.Markers) != 1 || run.Markers[0].T != 50 {
		t.Fatalf("markers %+v", run.Markers)
	}
}

// Events of the time a member was on another map are not drawn on this one, whatever the position they borrow.
func TestMarkersOfAnotherMapAreLeftOut(t *testing.T) {
	cfg := DefaultConfig()
	b := NewBuilder(cfg)
	for i := 0; i < 20; i++ {
		m, x := uint32(70), float32(i)*3
		if i >= 10 {
			m, x = 1, float32(-7000+i*3)
		}
		b.AddFrame(Frame{T: at(float64(i) * 10), Map: m, Members: []MemberFrame{{Name: "A", Self: true, Pos: Pos{X: x, Y: 5}}}})
	}
	for i := 0; i < 20; i++ { // map 70 gets more samples than map 1
		b.AddFrame(Frame{T: at(float64(i)*10 + 5), Map: 70, Members: []MemberFrame{{Name: "A", Self: true, Pos: Pos{X: float32(i)*3 + 1, Y: 5}}}})
	}
	evs := []events.Event{
		{Time: at(30), Kind: events.KindAttackStart, Source: "A", Target: "Gnoll", Party: true},
		{Time: at(150), Kind: events.KindAttackStart, Source: "A", Target: "Boar", Party: true},
	}
	run := b.Build(evs, nil)
	if run.Map != 70 || len(run.Markers) != 1 || run.Markers[0].T != 30 || run.Markers[0].Map != 70 {
		t.Fatalf("map %d, markers %+v", run.Map, run.Markers)
	}
	box, ok := run.Bounds(5, 20)
	if !ok || box.MinX < -100 {
		t.Fatalf("%+v", box)
	}
}
