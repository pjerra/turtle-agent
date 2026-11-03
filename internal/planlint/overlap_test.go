package planlint

import (
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// The check finds what Codex found in the first Wailing Caverns plan: a mob 3 yd from a one-member group, and a boss inside a pack's radius.
func TestPackOverlapsFindsAForeignMobAndABoss(t *testing.T) {
	cs := []Creature{
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
