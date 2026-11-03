package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// mustRoute runs the check on a synthetic plan: a refused export is the test's own failure, never a finding.
func mustRoute(t *testing.T, p *dungeon.Plan, cs []creature) []string {
	t.Helper()
	got, err := RouteIntoLaterPacks(p, cs, map[int]bool{35: true})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

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
	got, err := RouteIntoLaterPacks(p, cs, map[int]bool{35: true})
	if err != nil {
		t.Fatal(err)
	}
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
	got, err = RouteIntoLaterPacks(p, cs, map[int]bool{35: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatalf("the walk to the later pack is still a finding (owned by the later row): got none")
	}
	for _, s := range got {
		if strings.Contains(s, "no row") {
			t.Fatalf("every finding is owned now: %v", got)
		}
	}
}

// A plain pull's own approach is not a later pack: the segment ending at the move row before it walks out to pull
// that pack, so its spawn is no finding even though the later row owns it. Move rows own nothing (owner -1);
// comparing against the move row's owner reports it.
func TestRouteIntoLaterPacksIgnoresPlainPullApproach(t *testing.T) {
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
	got, err := RouteIntoLaterPacks(p, cs, map[int]bool{35: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range got {
		if strings.Contains(s, "Early") {
			t.Fatalf("an already-pulled pack near a later segment is no finding: %v", got)
		}
	}
	for _, s := range got {
		if strings.Contains(s, "Later") && strings.Contains(s, "guid 2") &&
			strings.Contains(s, "rows 1->2") {
			t.Fatalf("the plain pull's own approach is not a later pack: %v", got)
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

// A stand pull's stand inside a pack's aggro IS reported: the party walks to the stand and waits there, so the stand
// is where the pull happens, not an approach walked through. The segment ending at the stand's move row reports the
// pack the stand row pulls even though it is the next row.
func TestRouteIntoLaterPacksReportsStandPullStandInsideAggro(t *testing.T) {
	p := &dungeon.Plan{Name: "probe", Map: 1, Entrance: dungeon.Point{},
		Legs: []dungeon.Leg{{
			Name: "a",
			To:   dungeon.Point{X: -100},
			PullGroups: []dungeon.PullGroup{
				{Entries: []uint32{1}, Positions: []dungeon.Point{{X: 50}}, Radius: 5,
					Approach: []dungeon.Point{{X: 0}}, StandPull: true},
				{Entries: []uint32{2}, Positions: []dungeon.Point{{X: 0, Y: 80}}, Radius: 55,
					Approach: []dungeon.Point{{X: 0, Y: 60}}},
			},
		}}}
	// Stand sits at (0,0); the pack it pulls sits 12 yd off the stand's outgoing segment (a real stand-in-aggro
	// defect: the party waits at the stand inside the pack's reach).
	cs := []creature{
		{GUID: 1, Entry: 1, Name: "Early", LevelMax: 20, Faction: 14, Pos: dungeon.Point{X: 50}},
		{GUID: 2, Entry: 2, Name: "Later", LevelMax: 20, Faction: 14, Pos: dungeon.Point{X: -12, Y: 30}, DetectionRange: 15},
	}
	found := false
	for _, s := range mustRoute(t, p, cs) {
		if strings.Contains(s, "Later") && strings.Contains(s, "guid 2") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the stand-pull stand inside the later pack's aggro is still reported: %v", mustRoute(t, p, cs))
	}
}

// A stand pull is fought from its stand: the segments around its row start and end at the stand (the move row right
// before it), not at the row's own point (the pack centroid). Later sits 12 yd off the true lines through the stand
// but 28 yd off every centroid line, and StrayQ sits 12 yd off the invented stand -> centroid hop but 50 yd off every
// walked line: the check reports Later by the stand, and nothing by the centroid. The stand -> stand hop itself is
// zero-length (the party-point fix collapses it) and skipped, so the report comes from the walked line into the stand.
func TestRouteIntoLaterPacksStandPullWalksFromTheStand(t *testing.T) {
	p := &dungeon.Plan{Name: "probe", Map: 1, Entrance: dungeon.Point{},
		Legs: []dungeon.Leg{{
			Name: "a",
			To:   dungeon.Point{X: -100},
			PullGroups: []dungeon.PullGroup{
				{Entries: []uint32{1}, Positions: []dungeon.Point{{X: 50}}, Radius: 5,
					Approach: []dungeon.Point{{X: 0}}, StandPull: true},
				{Entries: []uint32{2}, Positions: []dungeon.Point{{X: 0, Y: 80}}, Radius: 55,
					Approach: []dungeon.Point{{X: 0, Y: 60}}},
			},
		}}}
	cs := []creature{
		{GUID: 1, Entry: 1, Name: "Early", LevelMax: 20, Faction: 14, Pos: dungeon.Point{X: 50}},
		{GUID: 2, Entry: 2, Name: "Later", LevelMax: 20, Faction: 14, Pos: dungeon.Point{X: -12, Y: 30}, DetectionRange: 15},
		{GUID: 3, Entry: 99, Name: "StrayQ", LevelMax: 20, Faction: 14, Pos: dungeon.Point{X: 50, Y: 12}, DetectionRange: 15},
	}
	got, err := RouteIntoLaterPacks(p, cs, map[int]bool{35: true})
	if err != nil {
		t.Fatal(err)
	}
	want := "probe leg 0 rows 0->1 (end leg 0): passes 30.0 yd from Later (guid 2, entry 2) owned by leg 0 row 4"
	found := false
	for _, s := range got {
		if s == want {
			found = true
		}
		if strings.Contains(s, "StrayQ") {
			t.Fatalf("the invented stand -> centroid hop is not walked: %v", got)
		}
		if strings.Contains(s, "Early") {
			t.Fatalf("an already-pulled pack near a later segment is no finding: %v", got)
		}
	}
	if !found {
		t.Fatalf("the stand's outgoing segment passes the later pack: want %q, got %v", want, got)
	}
}

// The aggro reach is the repo's rule: AggroRadius at the plan's party level (level_band[0]), plus the party's spread
// and the spawn's wander. A NO_AGGRO spawn next to the route is never reported (the mutation drops the skip); a raid
// spawn 22 yd off the route with the 10 yd spread still is (the reach needs the spread, not just detection range).
func TestRouteIntoLaterPacksUsesTheRepoAggroRule(t *testing.T) {
	mkplan := func(kind string) *dungeon.Plan {
		return &dungeon.Plan{Name: "probe", Map: 1, Kind: kind, LevelBand: []int{60, 60}, Entrance: dungeon.Point{},
			Legs: []dungeon.Leg{{
				Name: "a",
				To:   dungeon.Point{X: 20},
				PullGroups: []dungeon.PullGroup{
					{Entries: []uint32{1}, Positions: []dungeon.Point{{X: 10}}, Radius: 5},
					{Entries: []uint32{2}, Positions: []dungeon.Point{{X: 10}}, Radius: 5},
				},
			}}}
	}
	p := mkplan("")
	cs := []creature{
		{GUID: 1, Entry: 1, Name: "Early", LevelMax: 20, Faction: 14, Pos: dungeon.Point{X: 10}},
		// NO_AGGRO (flags_extra 0x2): 2 yd off the route, inside any detection range, spread or wander: still no
		// finding even though the reach (spread plus wander on top of a 0 AggroRadius) would otherwise cover it.
		{GUID: 2, Entry: 2, Name: "Quiet", LevelMax: 60, Faction: 14, Pos: dungeon.Point{X: 10, Y: 2}, DetectionRange: 20, FlagsExtra: 0x2},
		// A raid spawn 22 yd off the route: detection range alone (20) misses it, but the raid's 10 yd spread reaches 30.
		{GUID: 3, Entry: 2, Name: "Raid", LevelMax: 60, Faction: 14, Pos: dungeon.Point{X: 10, Y: 22}, DetectionRange: 20},
	}
	got, err := RouteIntoLaterPacks(p, cs, map[int]bool{35: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range got {
		if strings.Contains(s, "Quiet") {
			t.Fatalf("a NO_AGGRO spawn next to the route is no finding: %v", got)
		}
	}
	raid := mkplan("raid")
	raidGot := mustRoute(t, raid, cs[1:])
	found := false
	for _, s := range raidGot {
		if strings.Contains(s, "Raid") && strings.Contains(s, "guid 3") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a raid spawn 22 yd off the route with the 10 yd spread is a finding: %v", raidGot)
	}
}

// legSkip is keyed by the plan leg, not the export leg: with an optional leg before a skipped one the legs after close
// up, so looking the skip up by the export leg (the mutation) reads the wrong plan leg. The optional leg here leaves
// no rows; the plan's leg 1 is the export's leg 0, and its skip entry must still be skipped there.
func TestRouteIntoLaterPacksMapsSkipToPlanLeg(t *testing.T) {
	p := &dungeon.Plan{Name: "probe", Map: 1, Entrance: dungeon.Point{},
		Legs: []dungeon.Leg{{
			Name:     "opt",
			To:       dungeon.Point{X: 5},
			Optional: &dungeon.OptionalLeg{Item: 9240, Name: "Mallet"},
			Steps: []dungeon.Step{{
				UseObject: &dungeon.UseObject{Entry: 1, Near: &dungeon.Point{X: 5}, Stand: &dungeon.Point{X: 4}, RequiresItem: &dungeon.ItemRef{Entry: 9240, Name: "Mallet"}},
			}},
		}, {
			Name: "a",
			To:   dungeon.Point{X: 20},
			Skip: []uint32{2},
			PullGroups: []dungeon.PullGroup{
				{Entries: []uint32{1}, Positions: []dungeon.Point{{X: 10}}, Radius: 5},
				{Entries: []uint32{2}, Positions: []dungeon.Point{{X: 10}}, Radius: 5},
			},
		}}}
	// The skipped entry sits 10 yd off the route: no finding even though a later row pulls it.
	cs := []creature{
		{GUID: 1, Entry: 1, Name: "Early", LevelMax: 20, Faction: 14, Pos: dungeon.Point{X: 10}},
		{GUID: 2, Entry: 2, Name: "Skipped", LevelMax: 20, Faction: 14, Pos: dungeon.Point{X: 10, Y: 10}, DetectionRange: 15},
	}
	for _, s := range mustRoute(t, p, cs) {
		if strings.Contains(s, "Skipped") {
			t.Fatalf("a skip-listed entry is no finding even after an optional leg shifts the export legs: %v", mustRoute(t, p, cs))
		}
	}
}

// The detection default is 20: a spawn the export carries no range for engages at 20 yd, not 0. Turning the default
// off (the mutation) makes the walk past the later pack clean, so the test goes red. The reach adds the party's
// spread on top (3 yd for a dungeon), so a detection-5 spawn 10 yd off the route stays out, while detection 20 still
// reports it.
func TestRouteIntoLaterPacksDefaultsDetectionTo20(t *testing.T) {
	p := &dungeon.Plan{Name: "probe", Map: 1, LevelBand: []int{60, 60}, Entrance: dungeon.Point{},
		Legs: []dungeon.Leg{{
			Name: "a",
			To:   dungeon.Point{X: 20},
			PullGroups: []dungeon.PullGroup{
				{Entries: []uint32{1}, Positions: []dungeon.Point{{X: 10}}, Radius: 5},
				{Entries: []uint32{2}, Positions: []dungeon.Point{{X: 10}}, Radius: 5},
			},
		}}}
	cs := []creature{
		{GUID: 1, Entry: 1, Name: "Early", LevelMax: 60, Faction: 14, Pos: dungeon.Point{X: 10}},
		{GUID: 2, Entry: 2, Name: "Later", LevelMax: 60, Faction: 14, Pos: dungeon.Point{X: 10, Y: 10}},
	}
	if got := mustRoute(t, p, cs); len(got) == 0 {
		t.Fatalf("a later pack 10 yd off the route is a finding at the 20 yd default: got none")
	}
	cs[1].DetectionRange = 5
	if got := mustRoute(t, p, cs); len(got) != 0 {
		t.Fatalf("a later pack 10 yd off the route with detection 5 is no finding (reach 8): %v", got)
	}
}

// routeAggroKnownFailing lists the hand-written plans whose route passes within aggro of a pack a later row pulls
// (or of no row). The check walks the live-export rows (plandrift options: DropCompanionsLoot, CarrierChests,
// GMCrossDirect) and uses the repo's aggro rule (worldspawn.AggroRadius at level_band[0], plus the party's spread and
// the spawn's wander). A plan that passes now fails the test until it is taken off; any other change in a count, up
// or down, fails too, so a drop needs the pin updated with a one-line reason. Draft plans are not checked: skeletons
// have no mapped route. Straight segments are the approximation (the navmesh path is out of scope, L1) and patrols
// stand where the export put them (L2).
//
// Counts are measured on origin/dev (the ZG count must drop when W25-1 lands: the Arlokk terrace reordering walks
// each approach on its pack's level; the comment then says so).
var routeAggroKnownFailing = map[string]int{
	"ragefire_chasm":       153,  // boss rows own their boss (was 161). the repo reach (AggroRadius at 13-18 plus spread and wander) outruns the old 20 yd flat line.
	"wailing_caverns":      173,  // boss rows own their boss (was 188). the repo reach (AggroRadius at 22-27 plus spread and wander) outruns the old 20 yd flat line.
	"deadmines":            641,  // boss rows own their boss (was 661). the repo reach (AggroRadius at 18-22 plus spread and wander) outruns the old 20 yd flat line.
	"uldaman":              249,  // boss rows own their boss (was 268). the repo reach (AggroRadius at 41-45 plus spread and wander) outruns the old 20 yd flat line.
	"zulfarrak":            56,   // boss rows own their boss (was 66 with #391, 65 before it): a pass past a boss's own row is no finding.
	"karazhan_crypt":       447,  // boss rows own their boss (was 459). the repo reach (AggroRadius at 59-60 plus spread and wander) outruns the old 20 yd flat line.
	"molten_core":          270,  // boss rows own their boss (was 315). the repo reach (AggroRadius at 60 plus the raid's 10 yd spread) outruns the old 20 yd flat line.
	"blackwing_lair":       321,  // boss rows own their boss (was 344). the repo reach (AggroRadius at 58-60 plus the raid's 10 yd spread) outruns the old 20 yd flat line.
	"zul_gurub":            856,  // re-pinned for BOTS-PATHING-WAYPOINTS items 1-8 (was 835): the leg-0 entrance stand pulls and the early Berserker rows walk beside the ramp packs before their rows (the route into the ramp is the packs' own camp), and the new Arlokk/Thekal patrol blocks add their waypoint points.: the Mar'li east-slope approach, the Arlokk per-formation rows with their own stands, the Mandokir patrol + chain rows, the Jin'do stand pulls and the Hakkar land route walk fewer later packs (the deleted fish rows carried most of the old segments).
	"ruins_of_ahnqiraj":    178,  // boss rows own their boss (was 192). the repo reach (AggroRadius at 59-60 plus the raid's 10 yd spread) outruns the old 20 yd flat line.
	"temple_of_ahnqiraj":   520,  // boss rows own their boss (was 541). the repo reach (AggroRadius at 58-60 plus the raid's 10 yd spread) still reports more than the old 419: more spawns sit inside the wider reach than the plain-pull approach skip clears.
	"naxxramas":            647,  // boss rows own their boss (was 673). the repo reach (AggroRadius at 59-60 plus the raid's 10 yd spread) outruns the old 20 yd flat line.
	"lower_karazhan_halls": 1000, // boss rows own their boss (was 1010). the repo reach (AggroRadius at 59-60 plus the raid's 10 yd spread) outruns the old 20 yd flat line.
	"upper_karazhan_halls": 383,  // boss rows own their boss (was 403). the live export builds now (GMCrossDirect walks the outland gm_cross gap): the check sees the island hops.
	"emerald_sanctum":      15,   // boss rows own their boss (was 17). the repo reach (AggroRadius at 60 plus the raid's 10 yd spread) outruns detection 20, from 8.
	"timbermaw_hold":       214,  // boss rows own their boss (was 228). the repo reach (AggroRadius at 60 plus the raid's 10 yd spread) outruns the old 20 yd flat line.
	"frostmane_hollow":     171, "dire_maul_east": 90, "dire_maul_west": 212, "razorfen_downs": 191, "sunken_temple": 66,
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
		found, err := RouteIntoLaterPacks(p, cs, map[int]bool{35: true})
		if err != nil {
			t.Errorf("%s: %v", ps.Name, err)
			continue
		}
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

// A boss row owns its own boss and the boss's phase entries (boss.phase_entries, which the export does not carry): the
// final approach walks to the boss row's point, passing the boss at 0 yd and the phase add at 3.6 yd, and that is no
// finding. Before the fix the boss row owned only its First entries, so both were reported as owned by no row.
func TestRouteIntoLaterPacksBossRowOwnsItsBoss(t *testing.T) {
	p := &dungeon.Plan{Name: "probe", Map: 1, Entrance: dungeon.Point{},
		Legs: []dungeon.Leg{{
			Name: "a",
			Boss: &dungeon.Boss{Entry: 7, Position: dungeon.Point{X: 40}, Radius: 5,
				Approach:     []dungeon.Point{{X: 0}},
				PhaseEntries: []uint32{8}},
		}}}
	cs := []creature{
		{GUID: 1, Entry: 7, Name: "Boss", LevelMax: 20, Faction: 14, Pos: dungeon.Point{X: 40}},
		{GUID: 2, Entry: 8, Name: "Add", LevelMax: 20, Faction: 14, Pos: dungeon.Point{X: 42, Y: 3}},
	}
	if got := mustRoute(t, p, cs); len(got) != 0 {
		t.Fatalf("the final approach passing its own boss and phase add is no finding: %v", got)
	}
}
