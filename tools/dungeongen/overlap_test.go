package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

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
// spawns; none of them is pulled by those rows.
// Karazhan Crypt's 8 are Skeletal Remains 91924: 7 never wake (no Remains Trigger within 30 yd of them, in the south wing and the
// lower crypt, dead unselectable props) and 1 awake one stands 12.4 yd from a wandering pull - outside its 10 yd radius, inside the
// check's 3 yd aggro margin.
var packOverlapKnownFailing = map[string]int{"uldaman": 150, "zulfarrak": 38, "naxxramas": 104, "blackwing_lair": 50, "timbermaw_hold": 13, "temple_of_ahnqiraj": 128, "ruins_of_ahnqiraj": 21, "lower_karazhan_halls": 6, "zul_gurub": 39, "molten_core": 10, "upper_karazhan_halls": 6, "karazhan_crypt": 8}

// exportDir is the folder of a plan's creature export under testdata: the spec's data folder, map<id>, or the plan's name.
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
// stands within a pull group's radius plus the aggro margin without being listed in that group (see PackOverlaps).
func TestNoMobStandsInAPullGroupsRangeUnlisted(t *testing.T) {
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
		found := PackOverlaps(p, cs, map[int]bool{35: true})
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

// The check finds what Codex found in the first Wailing Caverns plan: a mob 3 yd from a one-member group, and a boss inside a pack's radius.
func TestPackOverlapsFindsAForeignMobAndABoss(t *testing.T) {
	cs := []creature{
		{Entry: 1, Name: "Pack", LevelMax: 20, Faction: 14, Pos: dungeon.Point{X: 0, Y: 0, Z: 0}},
		{Entry: 2, Name: "Neighbour", LevelMax: 20, Faction: 14, Pos: dungeon.Point{X: 3, Y: 0, Z: 0}},
		{Entry: 3, Name: "Boss", LevelMax: 21, Faction: 14, Pos: dungeon.Point{X: 12, Y: 0, Z: 0}},
		{Entry: 4, Name: "Far", LevelMax: 20, Faction: 14, Pos: dungeon.Point{X: 40, Y: 0, Z: 0}},
		{Entry: 5, Name: "Friend", LevelMax: 20, Faction: 35, Pos: dungeon.Point{X: 1, Y: 0, Z: 0}},
		{Entry: 6, Name: "Critter", LevelMax: 1, Faction: 14, Pos: dungeon.Point{X: 1, Y: 1, Z: 0}},
	}
	p := &dungeon.Plan{Legs: []dungeon.Leg{{Name: "a", PullGroups: []dungeon.PullGroup{{Entries: []uint32{1}, Positions: []dungeon.Point{{}}, Radius: 10}}}}}
	got := PackOverlaps(p, cs, map[int]bool{35: true})
	if len(got) != 2 || !strings.Contains(got[0], "Neighbour") || !strings.Contains(got[1], "Boss") {
		t.Fatalf("%v", got)
	}
	p.Legs[0].PullGroups[0].Entries = []uint32{1, 2, 3}
	if got := PackOverlaps(p, cs, map[int]bool{35: true}); len(got) != 0 {
		t.Fatalf("listed mobs are in the pack: %v", got)
	}
}
