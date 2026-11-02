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
	early := 0
	for _, s := range got {
		if strings.Contains(s, "Early") {
			early++
			if !strings.Contains(s, "owned by row") {
				t.Fatalf("the early pack is owned by its row: %v", got)
			}
		}
	}
	if early == 0 {
		t.Fatalf("the early pack is owned by its row, not by no row: %v", got)
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
// (or of no row), with the number of findings counted when the check came in. A plan that passes now fails the test
// until it is taken off; one that has more findings than listed fails too. Draft plans are not checked: skeletons
// have no mapped route.
//
// Counts are measured on origin/dev (the ZG count must drop when W25-1 lands: the Arlokk terrace reordering walks
// each approach on its pack's level; the comment then says so).
var routeAggroKnownFailing = map[string]int{
	"ragefire_chasm": 101, "wailing_caverns": 83, "deadmines": 399, "uldaman": 339, "zulfarrak": 1,
	"karazhan_crypt": 336, "molten_core": 534, "blackwing_lair": 286, "zul_gurub": 1324,
	"ruins_of_ahnqiraj": 465, "temple_of_ahnqiraj": 1305, "naxxramas": 584,
	"lower_karazhan_halls": 505, "upper_karazhan_halls": 1, "emerald_sanctum": 84, "timbermaw_hold": 183,
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
		case len(found) > known:
			t.Errorf("%s: %d route segments pass a later pack (known %d):\n  %s", ps.Name, len(found), known, strings.Join(found, "\n  "))
		}
	}
	if checked < 3 {
		t.Errorf("only %d hand-written plans checked", checked)
	}
}
