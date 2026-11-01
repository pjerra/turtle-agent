package raidwalk

import (
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

func TestBossRowsCounts(t *testing.T) {
	plan := &dungeon.Plan{Legs: []dungeon.Leg{
		{Name: "a", Boss: &dungeon.Boss{Entry: 1, Position: dungeon.Point{X: 0, Y: 0, Z: 50}, First: []uint32{10}}},
		// Boss b lists 10 too, and lists boss a's entry 1 as a first add.
		{Name: "b", Boss: &dungeon.Boss{Entry: 2, Position: dungeon.Point{X: 1000, Y: 0}, First: []uint32{10, 1}}},
		// Boss c has no position in the plan.
		{Name: "c", Boss: &dungeon.Boss{Entry: 3, First: []uint32{30}}},
		{Name: "d", Boss: &dungeon.Boss{Entry: 4, Position: dungeon.Point{X: -500, Y: -500}, Hold: &dungeon.Hold{Entries: []uint32{40}}}},
	}}
	b := NewBossRows(plan)
	for _, tc := range []struct {
		name  string
		entry uint32
		x, y  float32
		want  bool
	}{
		{"not listed", 99, 0, 0, false},
		{"boss far from its spot", 2, 5000, 5000, true},
		{"hold entry far away", 40, 5000, 5000, true},
		{"boss entry that is also a first add of another boss", 1, 5000, 5000, true},
		{"first add near boss a", 10, 20, 20, true},
		{"first add near boss b (listed by two bosses)", 10, 1030, 10, true},
		{"first add between both bosses", 10, 500, 0, false},
		{"first add at exactly 40 yd", 10, 40, 0, true},
		{"first add just past 40 yd", 10, 40.5, 0, false},
		{"first add of a boss with no position", 30, 5000, 5000, true},
	} {
		if got := b.Counts(tc.entry, tc.x, tc.y); got != tc.want {
			t.Errorf("%s: Counts(%d, %v, %v) = %v, want %v", tc.name, tc.entry, tc.x, tc.y, got, tc.want)
		}
	}
	if got := NewBossRows(nil).Counts(1, 0, 0); got {
		t.Error("a nil plan has no boss rows")
	}
	for entry, want := range map[uint32]string{
		1:  "boss 1",
		40: "boss 40",
		10: "boss row 10 (first add of a, b)",
		30: "boss row 30 (first add of c)",
	} {
		if got := b.Words(entry); got != want {
			t.Errorf("Words(%d) = %q, want %q", entry, got, want)
		}
	}
}
