package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// The check finds a segment walked through a pack a later row pulls: the party walks past the later pack's spawn on
// its way to an earlier one, and the spawn is within its aggro of the walked line.
func TestRouteIntoLaterPacksFindsASegmentThroughAPack(t *testing.T) {
	p := &dungeon.Plan{Name: "probe", Map: 1, Entrance: dungeon.Point{},
		Legs: []dungeon.Leg{{
			Name: "a",
			To:   dungeon.Point{X: 20},
			PullGroups: []dungeon.PullGroup{
				{Entries: []uint32{1}, Positions: []dungeon.Point{{X: 10, Y: 8}}, Radius: 5},
				{Entries: []uint32{2}, Positions: []dungeon.Point{{X: 40}}, Radius: 5,
					Approach: []dungeon.Point{{X: 40}}},
			},
		}}}
	cs := []creature{
		{GUID: 1, Entry: 1, Name: "Early", LevelMax: 20, Faction: 14, Pos: dungeon.Point{X: 10, Y: 8}},
		{GUID: 2, Entry: 2, Name: "Later", LevelMax: 20, Faction: 14, Pos: dungeon.Point{X: 25, Y: -2}},
	}
	got := RouteIntoLaterPacks(p, cs, map[int]bool{35: true})
	found := false
	for _, s := range got {
		if strings.Contains(s, "Later") && strings.Contains(s, "guid 2") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a segment through a later pack is no finding: %v", got)
	}
	// The early pack sits on the walked line too, but its row already pulled it: it is no finding. (A move
	// row ends the middle segment; its -1 owner must not report packs pulled before it.)
	for _, s := range got {
		if strings.Contains(s, "Early") {
			t.Fatalf("an already-pulled pack near a later segment is no finding: %v", got)
		}
	}
	// The route stops at the later pack's spawn and the later pull's radius owns it from there: every finding is
	// owned, none is "no row".
	p.Legs[0].To = dungeon.Point{X: 25, Y: -2}
	p.Legs[0].PullGroups[1].Positions[0] = dungeon.Point{X: 25, Y: -2}
	got = RouteIntoLaterPacks(p, cs, map[int]bool{35: true})
	if len(got) == 0 {
		t.Fatalf("the walk to the later pack is still a finding (owned by the later row): got none")
	}
	for _, s := range got {
		if strings.Contains(s, "no row") {
			t.Fatalf("every finding is owned now: %v", got)
		}
	}
}

// The segment to a later pull's approach walks past the already-pulled pack: its spawn is owned by an earlier row,
// so it is no finding. Move rows own nothing (owner -1); comparing against the move row's owner reports it.
func TestRouteIntoLaterPacksIgnoresAlreadyPulledPack(t *testing.T) {
	p := &dungeon.Plan{Name: "probe", Map: 1, Entrance: dungeon.Point{},
		Legs: []dungeon.Leg{{
			Name: "a",
			PullGroups: []dungeon.PullGroup{
				{Entries: []uint32{1}, Positions: []dungeon.Point{{X: 10}}, Radius: 5},
				{Entries: []uint32{2}, Positions: []dungeon.Point{{X: 30}}, Radius: 5,
					Approach: []dungeon.Point{{X: 20}}},
			},
		}}}
	cs := []creature{
		{GUID: 1, Entry: 1, Name: "Early", LevelMax: 20, Faction: 14, Pos: dungeon.Point{X: 10}},
		{GUID: 2, Entry: 2, Name: "Later", LevelMax: 20, Faction: 14, Pos: dungeon.Point{X: 30}},
	}
	got := RouteIntoLaterPacks(p, cs, map[int]bool{35: true})
	for _, s := range got {
		if strings.Contains(s, "Early") {
			t.Fatalf("an already-pulled pack near a later segment is no finding: %v", got)
		}
	}
	found := false
	for _, s := range got {
		if strings.Contains(s, "Later") && strings.Contains(s, "guid 2") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the later pack past the approach is still a finding: %v", got)
	}
}

// The detection default is 20: a spawn the export carries no range for engages at 20 yd, not 0. Turning the default
// off (the mutation) makes the walk past the later pack clean, so the test goes red.
func TestRouteIntoLaterPacksDefaultsDetectionTo20(t *testing.T) {
	p := &dungeon.Plan{Name: "probe", Map: 1, Entrance: dungeon.Point{},
		Legs: []dungeon.Leg{{
			Name: "a",
			To:   dungeon.Point{X: 20},
			PullGroups: []dungeon.PullGroup{
				{Entries: []uint32{1}, Positions: []dungeon.Point{{X: 10}}, Radius: 5},
				{Entries: []uint32{2}, Positions: []dungeon.Point{{X: 10}}, Radius: 5},
			},
		}}}
	cs := []creature{
		{GUID: 1, Entry: 1, Name: "Early", LevelMax: 20, Faction: 14, Pos: dungeon.Point{X: 10}},
		{GUID: 2, Entry: 2, Name: "Later", LevelMax: 20, Faction: 14, Pos: dungeon.Point{X: 10, Y: 10}},
	}
	if got := RouteIntoLaterPacks(p, cs, map[int]bool{35: true}); len(got) == 0 {
		t.Fatalf("a later pack 10 yd off the route is a finding at the 20 yd default: got none")
	}
	cs[1].DetectionRange = 5
	if got := RouteIntoLaterPacks(p, cs, map[int]bool{35: true}); len(got) != 0 {
		t.Fatalf("a later pack 10 yd off the route with detection 5 is no finding: %v", got)
	}
}

// routeAggroKnownFailing lists the hand-written plans whose route passes within aggro of a pack a later row pulls
// (or of no row), with the number of findings counted after the move-row owner fix (each spawn's owning row keeps
// its absolute route index, and a spawn counts only when its owner sits after the segment's end). A plan that
// passes now fails the test until it is taken off; any other change in a count, up or down, fails too, so a drop
// needs the pin updated with a one-line reason. Draft plans are not checked: skeletons have no mapped route.
//
// Counts are measured on origin/dev (the ZG count must drop when W25-1 lands: the Arlokk terrace reordering walks
// each approach on its pack's level; the comment then says so).
var routeAggroKnownFailing = map[string]int{
	"ragefire_chasm":       77,  // was 101: the move-row owner fix clears already-pulled packs near later segments.
	"wailing_caverns":      40,  // was 83: the move-row owner fix clears already-pulled packs near later segments.
	"deadmines":            330, // was 399: the move-row owner fix clears already-pulled packs near later segments.
	"uldaman":              113, // was 339: the move-row owner fix clears already-pulled packs near later segments.
	"zulfarrak":            1,   // unchanged: no already-pulled pack sat near a later segment.
	"karazhan_crypt":       210, // was 336: the move-row owner fix clears already-pulled packs near later segments.
	"molten_core":          83,  // was 534: the move-row owner fix clears already-pulled packs near later segments.
	"blackwing_lair":       98,  // was 286: the move-row owner fix clears already-pulled packs near later segments.
	"zul_gurub":            463, // was 1324: the move-row owner fix clears already-pulled packs near later segments.
	"ruins_of_ahnqiraj":    154, // was 465: the move-row owner fix clears already-pulled packs near later segments.
	"temple_of_ahnqiraj":   419, // was 1305: the move-row owner fix clears already-pulled packs near later segments.
	"naxxramas":            271, // was 584: the move-row owner fix clears already-pulled packs near later segments.
	"lower_karazhan_halls": 280, // was 505: the move-row owner fix clears already-pulled packs near later segments.
	"upper_karazhan_halls": 1,   // unchanged: no already-pulled pack sat near a later segment.
	"emerald_sanctum":      8,   // was 84: the move-row owner fix clears already-pulled packs near later segments.
	"timbermaw_hold":       105, // was 183: the move-row owner fix clears already-pulled packs near later segments.
}

// TestNoRouteSegmentPassesALaterPack: no straight segment between two consecutive rows passes within aggro of a spawn
// that a later row pulls (or that no row pulls). See RouteIntoLaterPacks.
func TestNoRouteSegmentPassesALaterPack(t *testing.T) {
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
		p, err := dungeon.Load(filepath.Join(plansDirT, ps.Name+".json"))
		if err != nil {
			t.Errorf("%s: %v", ps.Name, err)
			continue
		}
		cs, err := readCreatures(filepath.Join(dir, "creatures.tsv"))
		if err != nil {
			t.Fatal(err)
		}
		checked++
		found := RouteIntoLaterPacks(p, cs, map[int]bool{35: true})
		known, listed := routeAggroKnownFailing[ps.Name]
		switch {
		case len(found) == 0 && listed:
			t.Errorf("%s passes now: take it off routeAggroKnownFailing", ps.Name)
		case len(found) != known:
			t.Errorf("%s: %d route segments pass a later pack (known %d):\n  %s", ps.Name, len(found), known, strings.Join(found, "\n  "))
		}
	}
	if checked < 3 {
		t.Errorf("only %d hand-written plans checked", checked)
	}
}
