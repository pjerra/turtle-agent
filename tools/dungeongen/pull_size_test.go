package main

import (
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/aggroest"
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
	found, err := PullSizes(p, cs, w, PullFilter{})
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
// Round 2: the target is the row's own creature (ownership + kill_first, never
// an unrelated spawn), which moves zulfarrak 22->53, zul_gurub 82->95,
// ruins_of_ahnqiraj 28->32, temple_of_ahnqiraj 63->74, naxxramas 149->150.
var pullSizeKnownCounts = map[string]int{
	// The repo reach (AggroRadius at the plan's party level plus the party's
	// spread) and the 20 yd ring join neighbouring packs: every dense plan
	// warns on most of its pulls (mmaps-free path, LOS always seen).
	"ragefire_chasm": 28, "wailing_caverns": 54, "deadmines": 131,
	"uldaman": 58, "zulfarrak": 53, "karazhan_crypt": 125,
	"molten_core": 21, "blackwing_lair": 40, "zul_gurub": 95,
	"ruins_of_ahnqiraj": 32, "temple_of_ahnqiraj": 74, "naxxramas": 150,
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
		found, err := PullSizes(p, cs, w, PullFilter{})
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
	if found, err := PullSizes(quiet, qcs, nil, PullFilter{}); err != nil {
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
	found, err := PullSizes(p, cs, nil, PullFilter{})
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

// A friendly spawn next to a pull is not counted: with the neighbour in an
// ignored faction (or 35, friendly to all, or the check's friendly list) the
// row's own mob pulls alone and stays under its ceiling; unfiltered it joins
// through the ring and warns. A row gated to the other team is not pulled at
// all: it warns for neither team, and consumes no pull number.
func TestPullSizeIgnoresFriendlySpawnsAndOtherTeamRows(t *testing.T) {
	p := &dungeon.Plan{Name: "probe", Map: 1, Kind: "dungeon", LevelBand: []int{60, 60}, Entrance: dungeon.Point{},
		Legs: []dungeon.Leg{{Name: "a", To: dungeon.Point{X: 30},
			PullGroups: []dungeon.PullGroup{
				{Entries: []uint32{1}, Positions: []dungeon.Point{{X: 30}}, Radius: 5, MaxBodies: 1},
			}}}}
	cs := []creature{
		{GUID: 1, Entry: 1, Name: "One", LevelMax: 60, Faction: 14, DetectionRange: 20, Pos: dungeon.Point{X: 30}},
		{GUID: 2, Entry: 2, Name: "Friend", LevelMax: 60, Faction: 35, DetectionRange: 20, Pos: dungeon.Point{X: 35}},
	}
	if found, err := PullSizes(p, cs, nil, PullFilter{}); err != nil {
		t.Fatal(err)
	} else if len(found) != 0 {
		t.Fatalf("faction 35 warns: %v", found)
	}
	for _, f := range []PullFilter{{Ignore: map[int]bool{77: true}}, {Friendly: map[int]bool{77: true}}} {
		quiet := []creature{
			{GUID: 1, Entry: 1, Name: "One", LevelMax: 60, Faction: 14, DetectionRange: 20, Pos: dungeon.Point{X: 30}},
			{GUID: 2, Entry: 2, Name: "Friend", LevelMax: 60, Faction: 77, DetectionRange: 20, Pos: dungeon.Point{X: 35}},
		}
		if found, err := PullSizes(p, quiet, nil, f); err != nil {
			t.Fatal(err)
		} else if len(found) != 0 {
			t.Fatalf("friendly 77 warns (%v): %v", f, found)
		}
		if found, err := PullSizes(p, quiet, nil, PullFilter{}); err != nil {
			t.Fatal(err)
		} else if len(found) != 1 || found[0].Bodies != 2 {
			t.Fatalf("unfiltered 77 stays quiet: %v", found)
		}
	}
	gated := &dungeon.Plan{Name: "probe", Map: 1, Kind: "dungeon", LevelBand: []int{60, 60}, Entrance: dungeon.Point{},
		Legs: []dungeon.Leg{{Name: "a", To: dungeon.Point{X: 30},
			PullGroups: []dungeon.PullGroup{
				{Entries: []uint32{1}, Positions: []dungeon.Point{{X: 10}}, Radius: 5, MaxBodies: 1, Team: dungeon.TeamHorde},
				{Entries: []uint32{1}, Positions: []dungeon.Point{{X: 30}}, Radius: 5, MaxBodies: 1},
			}}}}
	hostile := []creature{
		{GUID: 1, Entry: 1, Name: "One", LevelMax: 60, Faction: 14, DetectionRange: 20, Pos: dungeon.Point{X: 10}},
		{GUID: 2, Entry: 1, Name: "Two", LevelMax: 60, Faction: 14, DetectionRange: 20, Pos: dungeon.Point{X: 12}},
		{GUID: 3, Entry: 1, Name: "Three", LevelMax: 60, Faction: 14, DetectionRange: 20, Pos: dungeon.Point{X: 30}},
		{GUID: 4, Entry: 1, Name: "Four", LevelMax: 60, Faction: 14, DetectionRange: 20, Pos: dungeon.Point{X: 32}},
	}
	found, err := PullSizes(gated, hostile, nil, PullFilter{Team: dungeon.TeamAlliance})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Pull != 1 {
		t.Fatalf("alliance sees one pull 1: %v", found)
	}
	if found, err := PullSizes(gated, hostile, nil, PullFilter{}); err != nil {
		t.Fatal(err)
	} else if len(found) != 2 || found[0].Pull != 1 || found[1].Pull != 2 {
		t.Fatalf("no team pulls both rows: %v", found)
	}
}

// The target is the row's own creature, kill_first rank first: with the bat
// rider 14750 in kill_first the target is the rider even when the formation's
// nearest mob to the stand is another unit, and a closer unrelated spawn next
// to a stand-pull row is never chosen.
func TestPullSizeTargetIsOwnedKillFirstFirst(t *testing.T) {
	p := &dungeon.Plan{Name: "probe", Map: 1, Kind: "raid", LevelBand: []int{60, 60},
		KillFirst: []uint32{14750}, Entrance: dungeon.Point{},
		Legs: []dungeon.Leg{{Name: "a", To: dungeon.Point{X: 30},
			PullGroups: []dungeon.PullGroup{
				{Entries: []uint32{11368, 14750}, Positions: []dungeon.Point{{X: 30}}, Radius: 25,
					Approach: []dungeon.Point{{X: 0}}, StandPull: true},
			}}}}
	cs := []creature{
		{GUID: 1, Entry: 11368, Name: "Bat", LevelMax: 60, Faction: 14, DetectionRange: 20, Pos: dungeon.Point{X: 28}},
		{GUID: 2, Entry: 14750, Name: "Rider", LevelMax: 60, Faction: 14, DetectionRange: 20, Pos: dungeon.Point{X: 34}},
		{GUID: 3, Entry: 999, Name: "Stranger", LevelMax: 60, Faction: 14, DetectionRange: 20, Pos: dungeon.Point{X: 1}},
	}
	pts, err := routePoints(p)
	if err != nil {
		t.Fatal(err)
	}
	var rp routePoint
	for _, q := range pts {
		if q.kind == "pull" {
			rp = q
		}
	}
	mobs, entries := pullMobs(p, cs, nil, PullFilter{})
	owned := pullOwners(pts, cs)
	if got := pullTarget(p, mobs, entries, rp, owned); got < 0 || mobs[got].Guid != 2 {
		t.Fatalf("target guid %v, want the kill_first rider 2", mobs)
	}
	// Without kill_first the nearer owned mob wins, still never the stranger.
	p.KillFirst = nil
	if got := pullTarget(p, mobs, entries, rp, owned); got < 0 || mobs[got].Guid != 1 {
		t.Fatalf("target guid %v, want the nearer owned bat 1", mobs)
	}
}

// pullTarget never picks a mob no row owns: the owned map's zero value is 0,
// which is also the route's first row index, so a presence check (not a bare
// comparison) decides. A mob the first row owns still wins.
func TestPullSizeTargetIgnoresUnownedMobAtRouteStart(t *testing.T) {
	p := &dungeon.Plan{Name: "probe", Map: 1, LevelBand: []int{60, 60}, Entrance: dungeon.Point{}}
	rp := routePoint{kind: "pull", px: 10, idx: 0}
	mobs := []aggroest.Mob{{Guid: 7, Pos: dungeon.Point{X: 10}, Level: 60, DetectionYd: 20, Faction: 14}}
	entries := map[uint32]uint32{7: 9}
	if got := pullTarget(p, mobs, entries, rp, map[uint64]int{}); got != -1 {
		t.Fatalf("unowned mob is the target: %v", mobs)
	}
	if got := pullTarget(p, mobs, entries, rp, map[uint64]int{7: 0}); got != 0 {
		t.Fatalf("owned mob is not the target: %v", mobs)
	}
}

// Every pull row of the leg is numbered in route order, not only the rows
// over their ceiling: a quiet row between two loud ones still consumes its
// number, so a row's number never moves when a ceiling changes.
func TestPullSizeNumbersEveryPullRowOfTheLeg(t *testing.T) {
	p := &dungeon.Plan{Name: "probe", Map: 1, Kind: "dungeon", LevelBand: []int{60, 60}, Entrance: dungeon.Point{},
		Legs: []dungeon.Leg{{Name: "a", To: dungeon.Point{X: 90},
			PullGroups: []dungeon.PullGroup{
				{Entries: []uint32{1}, Positions: []dungeon.Point{{X: 10}}, Radius: 5, MaxBodies: 1},
				{Entries: []uint32{2}, Positions: []dungeon.Point{{X: 40}}, Radius: 5, MaxBodies: 80},
				{Entries: []uint32{3}, Positions: []dungeon.Point{{X: 70}}, Radius: 5, MaxBodies: 1},
			}}}}
	cs := []creature{
		{GUID: 1, Entry: 1, Name: "One", LevelMax: 60, Faction: 14, DetectionRange: 20, Pos: dungeon.Point{X: 10}},
		{GUID: 2, Entry: 1, Name: "Two", LevelMax: 60, Faction: 14, DetectionRange: 20, Pos: dungeon.Point{X: 12}},
		{GUID: 3, Entry: 2, Name: "Lone", LevelMax: 60, Faction: 14, DetectionRange: 20, Pos: dungeon.Point{X: 40}},
		{GUID: 4, Entry: 3, Name: "Three", LevelMax: 60, Faction: 14, DetectionRange: 20, Pos: dungeon.Point{X: 70}},
		{GUID: 5, Entry: 3, Name: "Four", LevelMax: 60, Faction: 14, DetectionRange: 20, Pos: dungeon.Point{X: 72}},
	}
	found, err := PullSizes(p, cs, nil, PullFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 || found[0].Pull != 1 || found[1].Pull != 3 {
		t.Fatalf("pull numbers %v, want rows 1 and 3 (row 2 quiet)", found)
	}
}

// A pull row that owns nothing is a finding of its own ("pull N: no target"),
// never an unrelated spawn's estimate.
func TestPullSizeRowWithNoTargetIsANoTargetFinding(t *testing.T) {
	p := &dungeon.Plan{Name: "probe", Map: 1, Kind: "dungeon", LevelBand: []int{60, 60}, Entrance: dungeon.Point{},
		Legs: []dungeon.Leg{{Name: "a", To: dungeon.Point{X: 10},
			PullGroups: []dungeon.PullGroup{
				{Entries: []uint32{1}, Positions: []dungeon.Point{{X: 100}}, Radius: 5},
			}}}}
	cs := []creature{
		{GUID: 7, Entry: 9, Name: "Elsewhere", LevelMax: 60, Faction: 14, DetectionRange: 20, Pos: dungeon.Point{X: 10}},
	}
	found, err := PullSizes(p, cs, nil, PullFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Bodies != 0 || found[0].Summary != "leg 0 pull 1: no target" {
		t.Fatalf("findings %v, want one no-target finding for pull 1", found)
	}
}

// A plan with pull rows and no eligible mobs at all (an empty or
// friendly-only export) still gives every pull row a "no target" finding:
// the lint never returns clean because there was nothing to estimate.
func TestPullSizeNoEligibleMobsStillFindsEveryPullRow(t *testing.T) {
	p := &dungeon.Plan{Name: "probe", Map: 1, Kind: "dungeon", LevelBand: []int{60, 60}, Entrance: dungeon.Point{},
		Legs: []dungeon.Leg{{Name: "a", To: dungeon.Point{X: 10},
			PullGroups: []dungeon.PullGroup{
				{Entries: []uint32{1}, Positions: []dungeon.Point{{X: 10}}, Radius: 5},
			}}}}
	found, err := PullSizes(p, nil, nil, PullFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Summary != "leg 0 pull 1: no target" {
		t.Fatalf("findings %v, want one no-target finding for pull 1", found)
	}
}
