package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planlint"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

// The warning counts per plan are pinned like TestNoRouteSegmentPassesALaterPack
// (TestPullSizesUnderCeiling with a known-count map): any change in a count, up
// or down, fails, so a drop needs the pin updated with a one-line reason.
// Counts measured on the committed fixture (mmaps-free path, LOS always seen).
// Round 2: the target is the row's own planlint.Creature (ownership + kill_first, never
// an unrelated spawn), which moves zulfarrak 22->53, zul_gurub 82->95,
// ruins_of_ahnqiraj 28->32, temple_of_ahnqiraj 63->74, naxxramas 149->150.
var pullSizeKnownCounts = map[string]int{
	// The repo reach (AggroRadius at the plan's party level plus the party's
	// spread) and the 20 yd ring join neighbouring packs: every dense plan
	// warns on most of its pulls (mmaps-free path, LOS always seen).
	"ragefire_chasm": 28, "wailing_caverns": 54, "deadmines": 131,
	"uldaman": 58, "zulfarrak": 53, "karazhan_crypt": 125,
	"molten_core": 21, "blackwing_lair": 40, "zul_gurub": 88, // re-pinned on the batch tree (was 95): the walk 26b Marli order, walk 28's 27-unit Arlokk pull and walk 29's legs 5/6 pull fewer bodies per row.
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
			t.Errorf("%s: no planlint.Creature export under testdata", ps.Name)
			continue
		}
		p, err := dungeon.Load("../../dungeons/" + ps.Name + ".json")
		if err != nil {
			t.Errorf("%s: %v", ps.Name, err)
			continue
		}
		cs, err := planlint.ReadCreatures(dir + "/creatures.tsv")
		if err != nil {
			t.Fatal(err)
		}
		w, _, err := worldspawn.Load(dir, ps.Map)
		if err != nil {
			t.Errorf("%s: %v", ps.Name, err)
			continue
		}
		checked++
		found, err := planlint.PullSizes(p, cs, w, planlint.PullFilter{})
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
	"zul_gurub":            910,  // re-pinned on the batch tree: boss rows own their boss (was 1049 on dev), and the walk 26b Marli order, walk 28's 27-unit Arlokk pull and walk 29's legs 5/6 walk fewer later packs.
	"ruins_of_ahnqiraj":    178,  // boss rows own their boss (was 192). the repo reach (AggroRadius at 59-60 plus the raid's 10 yd spread) outruns the old 20 yd flat line.
	"temple_of_ahnqiraj":   520,  // boss rows own their boss (was 541). the repo reach (AggroRadius at 58-60 plus the raid's 10 yd spread) still reports more than the old 419: more spawns sit inside the wider reach than the plain-pull approach skip clears.
	"naxxramas":            647,  // boss rows own their boss (was 673). the repo reach (AggroRadius at 59-60 plus the raid's 10 yd spread) outruns the old 20 yd flat line.
	"lower_karazhan_halls": 1000, // boss rows own their boss (was 1010). the repo reach (AggroRadius at 59-60 plus the raid's 10 yd spread) outruns the old 20 yd flat line.
	"upper_karazhan_halls": 383,  // boss rows own their boss (was 403). the live export builds now (GMCrossDirect walks the outland gm_cross gap): the check sees the island hops.
	"emerald_sanctum":      15,   // boss rows own their boss (was 17). the repo reach (AggroRadius at 60 plus the raid's 10 yd spread) outruns detection 20, from 8.
	"timbermaw_hold":       214,  // boss rows own their boss (was 228). the repo reach (AggroRadius at 60 plus the raid's 10 yd spread) outruns the old 20 yd flat line.
}

// TestNoRouteSegmentPassesALaterPack: no straight segment between two consecutive rows passes within aggro of a spawn
// that a later row pulls (or that no row pulls). See planlint.RouteIntoLaterPacks.
func TestNoRouteSegmentPassesALaterPack(t *testing.T) {
	sf := loadSpecT(t)
	checked := 0
	for _, ps := range sf.Plans {
		if !ps.Handwritten {
			continue
		}
		dir := exportDir(ps)
		if dir == "" {
			t.Errorf("%s: no planlint.Creature export under testdata", ps.Name)
			continue
		}
		p, err := dungeon.Load(filepath.Join(plansDirT, ps.Name+".json"))
		if err != nil {
			t.Errorf("%s: %v", ps.Name, err)
			continue
		}
		cs, err := planlint.ReadCreatures(filepath.Join(dir, "creatures.tsv"))
		if err != nil {
			t.Fatal(err)
		}
		checked++
		found, err := planlint.RouteIntoLaterPacks(p, cs, map[int]bool{35: true})
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

// packOverlapKnownFailing lists the hand-written plans that have mobs in range of a pull group that the group does not list, with the
// number of findings counted when the check came in. A plan that passes now fails the test until it is taken off; one that has more
// findings than listed fails too. The generated drafts are not checked: their groups are the 12 yd clusters of the generator with the
// default radius, corrected by the mapping walk.
//
// uldaman and zulfarrak were cleared live with the default 25 yd radius and overlapping packs (the live checks are what settled them); their
// groups are not touched by an offline lane (38 since the courtyard, Zum'rah and Gahz'rilla legs of #192 and #195 came in; was 29). Uldaman's 150 (was 149, 144) count the two Jadespine Basilisks
// added on 2026-10-16 (world guids 30084 and 30089): each lone basilisk's group has scorpids of the pit and the ledge within its 25 yd; and the
// Sentinel's camp split of 2026-10-17 (the lone Hunter's group has the east trio and the west ledge Hunters within its 25 yd). Wailing Caverns passes: its radii are cut to stay short of the nearest mob that is not a
// member (generator rule in the plan notes), and the adds that stand with a boss are the boss's first.
// zul_gurub (2026-10-28): 38 packs have a neighbour of another entry within radius+3 (the shared
// troll/panther/bat clusters of the Arlokk, Jindo and Hakkar terraces). The 20-man cap keeps the
// rows at 12; the room sends the rest with the pull whether the plan lists them or not.
// 35 since the walk-1 fix of leg 0 (2026-10-29): the merged -12153 and -12277 packs list the
// neighbours they used to miss. 32 since the walk-5 fix (2026-10-30): leg 0's west rows are keyed
// on single points of the formations' loops; the Witch Doctor row (radius 15) and the Headhunter
// row (radius 60, its whole loop) have bat spawns in range, all pulled by earlier rows. 39 since the
// walk-7 fix (fix 6, 2026-10-30): leg 0's formation rows are stand_pull rows whose radius marks their
// formations by spawn (the run leader drops every other formation's units), so the Headhunter row's 71 yd
// reaches the static pack's and F3/F4's spawns, and the F3, F4, F7 and F5/F6 rows count their neighbours'
// spawns; none of them is pulled by those rows. 39 after the walk-18 Servant fix (2026-11-01): leg 1's
// four Servant rows are stand pulls (radii 3, 2.6, 10, 10) and reach no unlisted spawn even with the 3 yd
// margin (the D row's nearest other Servant, A's 49065, is 30.4 yd off; the A row's nearest, B's 49063,
// 18.0 yd; the E row's 3 yd reaches only E's own 49056, the next nearest map-309 spawn being C's 49061 at
// 19.0 yd), and leg 7 drops its old D group (radius 5: A 28 yd off, so it reached nothing either): the
// findings are the same 39 as at walk 13. The walk-18 review round 2 (2026-11-01) leaves the count be: the
// E row's radius 1 -> 3 and leg 7's tiger-pack dry-route approach points move no pull position.
// 41 after the walk-20 D+A merge (2026-11-01): leg 1's D row (4 positions, radius 10) and A row (3
// positions + the Blood Priest's, radius 10) become one stand pull (radius 17, 8 positions), the radius the
// export's own row needs to own all eight spawns 49053/54/55/57, 49065/66/67 and the priest 49051 from its
// mean centre (-11882.36 -1866.66): 17 yd owns exactly those eight (the furthest is 49053 at 16.8 yd) and no
// other spawn (the nearest is B's 49063 at 22.4 yd); the brief's radius 10 owns none of them (the mean
// centre is 11.1-16.8 yd from the eight). The 3 yd margin then reaches two B-pack spawns the group does not
// list: the Servant 49063 (18.5 yd from A's -11889.4 -1876.12) and the Blood Priest 49052 (17.0 yd from the
// priest's own position -11888.9 -1881.35). Both are leg 3 (Thekal) spawns, pulled 45+ yd away, and
// neither is inside the exported row's radius.
// Karazhan Crypt's 8 are Skeletal Remains 91924: 7 never wake (no Remains Trigger within 30 yd of them, in the south wing and the
// lower crypt, dead unselectable props) and 1 awake one stands 12.4 yd from a wandering pull - outside its 10 yd radius, inside the
// check's 3 yd aggro margin. 27 since the Corpsemuncher leg (legs[6], PR #320): its radius-25 pull groups cover one packed crypt
// room, so each group reaches the Skeletal Remains, Rotten Zombies, Strigoi and Forgotten Souls of the next ones (19 more); the room
// is pulled group by group from the doorway and the run leader drops units another row owns. Draft plan, not live-checked yet.
var packOverlapKnownFailing = map[string]int{"uldaman": 150, "zulfarrak": 38, "naxxramas": 104, "blackwing_lair": 50, "timbermaw_hold": 13, "temple_of_ahnqiraj": 128, "ruins_of_ahnqiraj": 21, "lower_karazhan_halls": 6, "zul_gurub": 41, "molten_core": 10, "upper_karazhan_halls": 6, "karazhan_crypt": 27}

// exportDir is the folder of a plan's planlint.Creature export under testdata: the spec's data folder, map<id>, or the plan's name.
func exportDir(ps PlanSpec) string {
	for _, d := range []string{ps.Data, "map" + strconv.Itoa(int(ps.Map)), ps.Name} {
		if d == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join("testdata", d, "creatures.tsv")); err == nil {
			return filepath.Join("testdata", d)
		}
	}
	return ""
}

// TestNoMobStandsInAPullGroupsRangeUnlisted: in every hand-written plan, no mob of another pull group, no boss and no forgotten spawn
// stands within a pull group's radius plus the aggro margin without being listed in that group (see planlint.PackOverlaps).
func TestNoMobStandsInAPullGroupsRangeUnlisted(t *testing.T) {
	sf := loadSpecT(t)
	checked := 0
	for _, ps := range sf.Plans {
		if !ps.Handwritten {
			continue
		}
		dir := exportDir(ps)
		if dir == "" {
			t.Errorf("%s: no planlint.Creature export under testdata", ps.Name)
			continue
		}
		p, err := dungeon.Load(filepath.Join(plansDirT, ps.Name+".json"))
		if err != nil {
			t.Errorf("%s: %v", ps.Name, err)
			continue
		}
		cs, err := planlint.ReadCreatures(filepath.Join(dir, "creatures.tsv"))
		if err != nil {
			t.Fatal(err)
		}
		checked++
		found := planlint.PackOverlaps(p, cs, map[int]bool{35: true})
		known, listed := packOverlapKnownFailing[ps.Name]
		switch {
		case len(found) == 0 && listed:
			t.Errorf("%s passes now: take it off packOverlapKnownFailing", ps.Name)
		case len(found) > known:
			t.Errorf("%s: %d mobs in range of a pull group and not listed in it (known %d):\n  %s", ps.Name, len(found), known, strings.Join(found, "\n  "))
		}
	}
	if checked < 3 {
		t.Errorf("only %d hand-written plans checked", checked)
	}
}
