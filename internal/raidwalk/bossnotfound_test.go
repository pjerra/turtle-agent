package raidwalk

import (
	"path/filepath"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// The open row that summons a leg's boss: the last non-move row before the boss row, when it is an open. Zul'Gurub's Arlokk
// leg is the case (the gong 180526, then the boss); Thekal's leg ends in pulls, so it has none.
func TestOpenStepBeforeBoss(t *testing.T) {
	rows := zgBuild(t)
	bossStep, openStep := 0, 0
	leg := -1
	for i, s := range rows.Steps {
		if s.Kind == planexport.KindBoss && s.Entry == 14515 {
			bossStep, leg = i+1, s.Leg
		}
	}
	for i, s := range rows.Steps {
		if i+1 < bossStep && s.Leg == leg && s.Kind == planexport.KindOpen {
			openStep = i + 1
		}
	}
	if bossStep == 0 || openStep == 0 {
		t.Fatalf("the ZG rows lost Arlokk's gong and boss rows (boss %d, open %d)", bossStep, openStep)
	}
	if got, ok := OpenStepBeforeBoss(rows, bossStep); !ok || got != openStep {
		t.Fatalf("OpenStepBeforeBoss(%d) = %d, %v; want the gong at %d", bossStep, got, ok, openStep)
	}
	// Thekal's leg before the boss holds pulls: no summoning open.
	thekal := 0
	for i, s := range rows.Steps {
		if s.Kind == planexport.KindBoss && s.Entry == 14599 {
			thekal = i + 1
		}
	}
	if got, ok := OpenStepBeforeBoss(rows, thekal); ok {
		t.Fatalf("Thekal's boss row %d reports an open step %d", thekal, got)
	}
	// Not a boss row, out of range: false.
	if _, ok := OpenStepBeforeBoss(rows, 1); ok {
		t.Fatal("step 1 is no boss row")
	}
	if _, ok := OpenStepBeforeBoss(rows, 0); ok {
		t.Fatal("step 0 is out of range")
	}
	if _, ok := OpenStepBeforeBoss(rows, len(rows.Steps)+1); ok {
		t.Fatal("a step past the rows is out of range")
	}
}

// The open row is taken as the boss's summoning object only when it stands at the boss row (within OpenStepNearBossYd) and is the
// last non-move row before it: a door or lever elsewhere in the wing, or an open followed by a pull, is no evidence (the summoning
// link is inferred from the plan's shape, so the rule stays narrow).
func TestOpenStepBeforeBossNarrow(t *testing.T) {
	mk := func(rows ...planexport.StepRow) planexport.Rows {
		for i := range rows {
			rows[i].Plan, rows[i].Leg = "p", 1
		}
		return planexport.Rows{Plan: planexport.PlanRow{Plan: "p", Map: 1}, Legs: []planexport.LegRow{{Plan: "p", Leg: 1}}, Steps: rows}
	}
	boss := planexport.StepRow{Kind: planexport.KindBoss, Entry: 14515, X: 0, Y: 0, Z: 0}
	move := planexport.StepRow{Kind: planexport.KindMove, X: 0, Y: 0, Z: 0}
	cases := []struct {
		name  string
		rows  planexport.Rows
		want  int
		found bool
	}{
		{"the object at the boss row", mk(move, planexport.StepRow{Kind: planexport.KindOpen, Entry: 180526, X: 28, Y: 0}, boss), 2, true},
		{"an open far from the boss row", mk(move, planexport.StepRow{Kind: planexport.KindOpen, Entry: 900, X: 200, Y: 0}, boss), 0, false},
		{"a pull after the open", mk(move, planexport.StepRow{Kind: planexport.KindOpen, Entry: 900, X: 5, Y: 0},
			planexport.StepRow{Kind: planexport.KindPull, Entry: 1, X: 5, Y: 0}, boss), 0, false},
		{"an open_proof counts", mk(move, planexport.StepRow{Kind: planexport.KindOpenProof, Entry: 148830, X: 0, Y: 10}, boss), 2, true},
	}
	for _, c := range cases {
		got, ok := OpenStepBeforeBoss(c.rows, len(c.rows.Steps))
		if ok != c.found || got != c.want {
			t.Errorf("%s: OpenStepBeforeBoss = %d, %v; want %d, %v", c.name, got, ok, c.want, c.found)
		}
	}
}

// zgBuild is the Zul'Gurub plan's rows as the walk deploys them.
func zgBuild(t *testing.T) planexport.Rows {
	t.Helper()
	p, err := dungeon.Load(filepath.Join("..", "..", "dungeons", "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := planexport.Build(p, planexport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return res.Rows
}
