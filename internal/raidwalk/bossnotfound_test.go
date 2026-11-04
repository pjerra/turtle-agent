package raidwalk

import (
	"path/filepath"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// The open row that summons a leg's boss comes from the plan's annotation (boss.summon_open_step), not from the rows' shape:
// Zul'Gurub's Arlokk 14515 carries it (the gong 180526 is the leg's use_object step) and her boss row reports the gong row; Thekal's
// leg has no such step, so his boss row reports none.
func TestSummonOpenStep(t *testing.T) {
	p, err := dungeon.Load(filepath.Join("..", "..", "dungeons", "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	rows := zgBuild(t)
	bossStep, openStep := 0, 0
	for i, s := range rows.Steps {
		if s.Kind == planexport.KindBoss && s.Entry == 14515 {
			bossStep = i + 1
		}
		if s.Kind == planexport.KindOpen && s.Entry == 180526 {
			openStep = i + 1
		}
	}
	if bossStep == 0 || openStep == 0 {
		t.Fatalf("the ZG rows lost Arlokk's gong and boss rows (boss %d, open %d)", bossStep, openStep)
	}
	if got, ok := SummonOpenStep(rows, p, bossStep); !ok || got != openStep {
		t.Fatalf("SummonOpenStep(%d) = %d, %v; want the gong at %d", bossStep, got, ok, openStep)
	}
	// Thekal's leg has no summoning open step: no re-run, whatever stands around his row.
	thekal := 0
	for i, s := range rows.Steps {
		if s.Kind == planexport.KindBoss && s.Entry == 14599 {
			thekal = i + 1
		}
	}
	if got, ok := SummonOpenStep(rows, p, thekal); ok {
		t.Fatalf("Thekal's boss row %d reports an open step %d", thekal, got)
	}
	// Not a boss row, out of range, no plan: false.
	if _, ok := SummonOpenStep(rows, p, openStep); ok {
		t.Fatal("the gong row is no boss row")
	}
	if _, ok := SummonOpenStep(rows, p, 0); ok {
		t.Fatal("step 0 is out of range")
	}
	if _, ok := SummonOpenStep(rows, p, len(rows.Steps)+1); ok {
		t.Fatal("a step past the rows is out of range")
	}
	if _, ok := SummonOpenStep(rows, nil, bossStep); ok {
		t.Fatal("no plan: no annotation to read")
	}
}

// A boss with no annotation near a door or lever that opens a step before it gets no re-run: proximity and order are no evidence
// that the object summons the boss (the rows carry no link; only the plan does).
func TestSummonOpenStepNeedsTheAnnotation(t *testing.T) {
	plan := func(summon int) *dungeon.Plan {
		return &dungeon.Plan{Name: "p", Map: 1, Legs: []dungeon.Leg{{
			Name:       "l",
			Steps:      []dungeon.Step{{UseObject: &dungeon.UseObject{ObjectRef: dungeon.ObjectRef{Entry: 900, Near: &dungeon.Point{X: 2, Y: 0, Z: 0}}}}},
			Boss:       &dungeon.Boss{Entry: 7, Position: dungeon.Point{X: 5, Y: 0, Z: 0}, SummonOpenStep: summon},
			PullGroups: nil,
		}}}
	}
	rows := planexport.Rows{
		Plan: planexport.PlanRow{Plan: "p", Map: 1},
		Legs: []planexport.LegRow{{Plan: "p", Leg: 0, Name: "l"}},
		Steps: []planexport.StepRow{
			{Plan: "p", Leg: 0, Seq: 0, Kind: planexport.KindMove, X: 0, Y: 0, Z: 0},
			{Plan: "p", Leg: 0, Seq: 1, Kind: planexport.KindOpen, Entry: 900, X: 2, Y: 0, Z: 0},
			{Plan: "p", Leg: 0, Seq: 2, Kind: planexport.KindBoss, Entry: 7, X: 5, Y: 0, Z: 0},
		},
	}
	if got, ok := SummonOpenStep(rows, plan(0), 3); ok {
		t.Fatalf("an un-annotated boss near the open row reports step %d: proximity is no link", got)
	}
	if got, ok := SummonOpenStep(rows, plan(1), 3); !ok || got != 2 {
		t.Fatalf("the annotated boss = %d, %v; want the open row at 2", got, ok)
	}
	// The annotated step's open row is missing from the rows (a deployment of another plan version): no re-run, not a wrong step.
	stale := rows
	stale.Steps = []planexport.StepRow{
		{Plan: "p", Leg: 0, Seq: 0, Kind: planexport.KindMove, X: 0, Y: 0, Z: 0},
		{Plan: "p", Leg: 0, Seq: 1, Kind: planexport.KindBoss, Entry: 7, X: 5, Y: 0, Z: 0},
	}
	if got, ok := SummonOpenStep(stale, plan(1), 2); ok {
		t.Fatalf("rows without the open row report step %d", got)
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
