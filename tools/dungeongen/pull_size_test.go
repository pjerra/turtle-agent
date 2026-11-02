package main

import (
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

// The terrace rows (Arlokk leg) each estimate 27 bodies with the committed
// fixture (mmaps-free path, LOS always seen): the G12/G13/G16/G17 packs joined
// by the 20 yd ring (cross-pack gaps 11.0-19.5 yd) plus their pack closures.
// Fails today (no lint). The ring-10 pin lives in the pure test
// TestRingTwoHopsThenStops (a 10 yd ring stops the 18 yd chain at one body).
func TestZulGurubArlokkTerraceIsOneTwentySevenPull(t *testing.T) {
	p, err := dungeon.Load("../../dungeons/zul_gurub.json")
	if err != nil {
		t.Fatal(err)
	}
	cs, err := readCreatures("testdata/map309/creatures.tsv")
	if err != nil {
		t.Fatal(err)
	}
	w, _, err := worldspawn.Load("testdata/map309", 309)
	if err != nil {
		t.Fatal(err)
	}
	found, err := PullSizes(p, cs, w)
	if err != nil {
		t.Fatal(err)
	}
	best := -1
	for _, f := range found {
		if f.Leg == 4 && f.Bodies == 27 {
			best = f.Bodies
			t.Logf("leg %d pull %d: %d bodies (ceiling %d): %s", f.Leg, f.Pull, f.Bodies, f.Ceiling, f.Summary)
		}
	}
	if best != 27 {
		t.Fatalf("no 27-body terrace pull on leg 4: %v", found)
	}
}

// The warning counts per plan are pinned like TestNoRouteSegmentPassesALaterPack
// (TestPullSizesUnderCeiling with a known-count map): any change in a count, up
// or down, fails, so a drop needs the pin updated with a one-line reason.
// Counts measured on the committed fixture (mmaps-free path, LOS always seen).
var pullSizeKnownCounts = map[string]int{
	// The repo reach (AggroRadius at the plan's party level plus the party's
	// spread) and the 20 yd ring join neighbouring packs: every dense plan
	// warns on most of its pulls (mmaps-free path, LOS always seen).
	"ragefire_chasm": 28, "wailing_caverns": 54, "deadmines": 131,
	"uldaman": 58, "zulfarrak": 22, "karazhan_crypt": 125,
	"molten_core": 21, "blackwing_lair": 40, "zul_gurub": 82,
	"ruins_of_ahnqiraj": 28, "temple_of_ahnqiraj": 63, "naxxramas": 149,
	"lower_karazhan_halls": 79, "upper_karazhan_halls": 30,
	"emerald_sanctum": 7, "timbermaw_hold": 27,
}

func TestPullSizesUnderCeiling(t *testing.T) {
	sf := loadSpecT(t)
	checked := 0
	for _, ps := range sf.Plans {
		if !ps.Handwritten {
			continue
		}
		dir := exportDir(ps)
		if dir == "" {
			t.Errorf("%s: no creature export under testdata", ps.Name)
			continue
		}
		p, err := dungeon.Load("../../dungeons/" + ps.Name + ".json")
		if err != nil {
			t.Errorf("%s: %v", ps.Name, err)
			continue
		}
		cs, err := readCreatures(dir + "/creatures.tsv")
		if err != nil {
			t.Fatal(err)
		}
		w, _, err := worldspawn.Load(dir, ps.Map)
		if err != nil {
			t.Errorf("%s: %v", ps.Name, err)
			continue
		}
		checked++
		found, err := PullSizes(p, cs, w)
		if err != nil {
			t.Errorf("%s: %v", ps.Name, err)
			continue
		}
		known, listed := pullSizeKnownCounts[ps.Name]
		switch {
		case len(found) == 0 && listed:
			t.Errorf("%s passes now: take it off pullSizeKnownCounts", ps.Name)
		case len(found) != known:
			shown := []string{}
			for _, f := range found {
				shown = append(shown, f.Summary)
			}
			t.Errorf("%s: %d pulls over the ceiling (known %d):\n  %s", ps.Name, len(found), known, strings.Join(shown, "\n  "))
		}
	}
	if checked < 3 {
		t.Errorf("only %d hand-written plans checked", checked)
	}
}

// The lint warns per pull row with its ceiling: leg, pull index, bodies,
// ceiling and guids. A lone pair 30 yd apart from a two-row plan stays quiet
// (each row sees only its own mob); two mobs of one pack 15 yd from the
// stand join through the ring and warn with the row's max_bodies ceiling.
func TestPullSizeWarningNamesLegPullBodiesCeiling(t *testing.T) {
	quiet := &dungeon.Plan{Name: "probe", Map: 1, LevelBand: []int{60, 60}, Entrance: dungeon.Point{},
		Legs: []dungeon.Leg{{Name: "a", To: dungeon.Point{X: 40},
			PullGroups: []dungeon.PullGroup{
				{Entries: []uint32{1}, Positions: []dungeon.Point{{X: 10}}, Radius: 5},
				{Entries: []uint32{2}, Positions: []dungeon.Point{{X: 40}}, Radius: 5},
			}}}}
	qcs := []creature{
		{GUID: 1, Entry: 1, Name: "Early", LevelMax: 60, Faction: 14, DetectionRange: 20, Pos: dungeon.Point{X: 10}},
		{GUID: 2, Entry: 2, Name: "Later", LevelMax: 60, Faction: 14, DetectionRange: 20, Pos: dungeon.Point{X: 40}},
	}
	if found, err := PullSizes(quiet, qcs, nil); err != nil {
		t.Fatal(err)
	} else if len(found) != 0 {
		t.Fatalf("lone pair warns: %v", found)
	}
	p := &dungeon.Plan{Name: "probe", Map: 1, Kind: "dungeon", LevelBand: []int{60, 60}, Entrance: dungeon.Point{},
		Legs: []dungeon.Leg{{Name: "a", To: dungeon.Point{X: 30},
			PullGroups: []dungeon.PullGroup{
				{Entries: []uint32{1}, Positions: []dungeon.Point{{X: 30}}, Radius: 5, MaxBodies: 1},
			}}}}
	cs := []creature{
		{GUID: 1, Entry: 1, Name: "One", LevelMax: 60, Faction: 14, DetectionRange: 20, Pos: dungeon.Point{X: 30}},
		{GUID: 2, Entry: 1, Name: "Two", LevelMax: 60, Faction: 14, DetectionRange: 20, Pos: dungeon.Point{X: 45}},
	}
	found, err := PullSizes(p, cs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 {
		t.Fatalf("%d findings, want 1: %v", len(found), found)
	}
	f := found[0]
	if f.Leg != 0 || f.Pull != 1 || f.Bodies != 2 || f.Ceiling != 1 {
		t.Fatalf("%+v: want leg 0 pull 1, 2 bodies over ceiling 1", f)
	}
	if len(f.Guids) != 2 || !strings.Contains(f.Summary, "leg 0 pull 1: 2 bodies (ceiling 1)") {
		t.Fatalf("%+v: want both guids and the summary line", f)
	}
}
