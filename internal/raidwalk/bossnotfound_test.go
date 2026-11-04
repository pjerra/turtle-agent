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
