package plandiff

import (
	"bytes"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/planexport"
)

func fixture(t *testing.T, stamp string) planexport.Rows {
	t.Helper()
	data, err := os.ReadFile("../planexport/testdata/onyxias_lair-" + stamp + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := planexport.ParseMigration(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("%s: %d plans, want 1", stamp, len(rows))
	}
	return rows[0]
}

func changesOfType(d Diff, typ string) []Change {
	var out []Change
	for _, c := range d.Changes {
		if c.Type == typ {
			out = append(out, c)
		}
	}
	return out
}

func TestDiffIdentical(t *testing.T) {
	r := fixture(t, "20261029130000")
	d := Rows(r, r)
	if d.OldSteps != 29 || d.NewSteps != 29 {
		t.Fatalf("steps %d -> %d", d.OldSteps, d.NewSteps)
	}
	if len(d.Changes) != 0 || d.Renumbered != 0 || len(d.PlanFields) != 0 {
		t.Fatalf("identical rows differ: %+v", d)
	}
	if d.Counts["moved"] != 0 || d.Counts["comment"] != 0 {
		t.Errorf("counts %v", d.Counts)
	}
	var out bytes.Buffer
	d.Text(&out)
	if !strings.Contains(out.String(), "onyxias_lair: 29 -> 29 steps: 0 moved, 0 changed, 0 added, 0 removed, 0 comment; 0 renumbered; plan: none") {
		t.Errorf("text:\n%s", out.String())
	}
}

func TestDiffOnyxiaBossApproach(t *testing.T) {
	old, new := fixture(t, "20261029120000"), fixture(t, "20261029130000")
	d := Rows(old, new)
	if d.Plan != "onyxias_lair" || d.Map != 249 || d.OldSteps != 29 || d.NewSteps != 29 {
		t.Fatalf("diff %+v", d)
	}
	moved := changesOfType(d, TypeMoved)
	if len(moved) != 2 {
		t.Fatalf("%d moved, want 2: %+v", len(moved), d.Changes)
	}
	first := moved[0]
	if first.OldStep != 26 || first.NewStep != 26 || first.OldSeq != 25 || first.NewSeq != 25 || first.Kind != "move" || first.Leg != "0" {
		t.Errorf("first moved: %+v", first)
	}
	if math.Abs(first.DeltaYd-7.92) > 0.01 || math.Abs(float64(first.Old.Z)+74.62) > 0.01 || math.Abs(float64(first.New.Z)+66.70) > 0.01 {
		t.Errorf("first moved geometry: %+v delta %v", first, first.DeltaYd)
	}
	if !strings.Contains(strings.Join(first.Fields, ","), "comment") {
		t.Errorf("first moved fields %v, want comment", first.Fields)
	}
	second := moved[1]
	if second.OldStep != 27 || second.NewStep != 27 || second.OldSeq != 26 || second.NewSeq != 26 {
		t.Errorf("second moved: %+v", second)
	}
	if math.Abs(second.DeltaYd-17.01) > 0.01 || math.Abs(float64(second.Old.X)+99.82) > 0.01 || math.Abs(float64(second.New.X)+114.73) > 0.01 {
		t.Errorf("second moved geometry: %+v delta %v", second, second.DeltaYd)
	}
	if n := len(changesOfType(d, TypeAdded)) + len(changesOfType(d, TypeRemoved)) + len(changesOfType(d, TypeChanged)); n != 0 {
		t.Errorf("%d added/removed/changed, want 0", n)
	}
	if d.Renumbered != 0 {
		t.Errorf("renumbered %d", d.Renumbered)
	}
	if len(d.PlanFields) != 1 || d.PlanFields[0] != "source" {
		t.Errorf("plan fields %v, want [source]", d.PlanFields)
	}
	var out bytes.Buffer
	d.Text(&out)
	if !strings.Contains(out.String(), "step 26 (leg 0 seq 25) move: moved 7.92 yd (-149.46 -210.48 -74.62 -> -149.46 -210.48 -66.70); comment 'path' -> 'boss approach'") {
		t.Errorf("text:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "2 moved, 0 changed, 0 added, 0 removed, 0 comment; 0 renumbered; plan: source") {
		t.Errorf("summary:\n%s", out.String())
	}
}

// insertStep inserts s into rows (global step position at) and renumbers the seq of that leg's steps after it.
func insertStep(rows planexport.Rows, at int, s planexport.StepRow) planexport.Rows {
	steps := append([]planexport.StepRow{}, rows.Steps[:at]...)
	steps = append(steps, s)
	steps = append(steps, rows.Steps[at:]...)
	for i := range steps {
		if steps[i].Leg == s.Leg {
			steps[i].Seq = i - legStart(steps, s.Leg)
		}
	}
	rows.Steps = steps
	return rows
}

func legStart(steps []planexport.StepRow, leg int) int {
	for i, s := range steps {
		if s.Leg == leg {
			return i
		}
	}
	return 0
}

func removeStep(rows planexport.Rows, at int) planexport.Rows {
	rows.Steps = append(append([]planexport.StepRow{}, rows.Steps[:at]...), rows.Steps[at+1:]...)
	leg := rows.Steps[at-1].Leg
	for i := range rows.Steps {
		if rows.Steps[i].Leg == leg {
			rows.Steps[i].Seq = i - legStart(rows.Steps, leg)
		}
	}
	return rows
}

func cloneRows(r planexport.Rows) planexport.Rows {
	r.Steps = append([]planexport.StepRow{}, r.Steps...)
	r.Legs = append([]planexport.LegRow{}, r.Legs...)
	return r
}

func TestDiffInsertRenumbers(t *testing.T) {
	base := fixture(t, "20261029130000")
	ins := planexport.StepRow{Plan: base.Plan.Plan, Leg: 0, Seq: 5, Kind: planexport.KindMove, X: 1, Y: 2, Z: 3, Comment: "inserted"}
	newRows := insertStep(cloneRows(base), 5, ins)
	d := Rows(base, newRows)
	if d.NewSteps != 30 {
		t.Fatalf("new steps %d", d.NewSteps)
	}
	if len(d.Changes) != 1 || d.Changes[0].Type != TypeAdded || d.Changes[0].OldStep != 0 || d.Changes[0].NewStep != 6 {
		t.Fatalf("changes %+v", d.Changes)
	}
	if d.Renumbered != 24 {
		t.Errorf("renumbered %d, want 24", d.Renumbered)
	}
	if len(d.Notes) == 0 || !strings.Contains(strings.Join(d.Notes, "\n"), "renumbered") {
		t.Errorf("notes %v", d.Notes)
	}
}

func TestDiffRemove(t *testing.T) {
	base := fixture(t, "20261029130000")
	newRows := removeStep(cloneRows(base), 5)
	d := Rows(base, newRows)
	if d.NewSteps != 28 {
		t.Fatalf("new steps %d", d.NewSteps)
	}
	if len(d.Changes) != 1 || d.Changes[0].Type != TypeRemoved || d.Changes[0].OldStep != 6 || d.Changes[0].NewStep != 0 {
		t.Fatalf("changes %+v", d.Changes)
	}
	if d.Renumbered != 23 {
		t.Errorf("renumbered %d, want 23", d.Renumbered)
	}
}

func smallRows() planexport.Rows {
	return planexport.Rows{
		Plan: planexport.PlanRow{Plan: "x", Map: 1, Source: "s"},
		Legs: []planexport.LegRow{{Plan: "x", Leg: 0, Name: "a"}},
		Steps: []planexport.StepRow{
			{Plan: "x", Leg: 0, Seq: 0, Kind: planexport.KindMove, X: 1, Y: 2, Z: 3, Comment: "one"},
			{Plan: "x", Leg: 0, Seq: 1, Kind: planexport.KindPull, X: 4, Y: 5, Z: 6, Radius: 1, Entries: []uint32{9}, Comment: "two"},
		},
	}
}

func TestDiffLegAddedRemoved(t *testing.T) {
	old := smallRows()
	new := cloneRows(old)
	new.Legs = append(new.Legs, planexport.LegRow{Plan: "x", Leg: 1, Name: "b"})
	new.Steps = append(new.Steps, planexport.StepRow{Plan: "x", Leg: 1, Seq: 0, Kind: planexport.KindMove, X: 7, Y: 8, Z: 9})
	d := Rows(old, new)
	if len(changesOfType(d, TypeLegAdded)) != 1 || len(changesOfType(d, TypeAdded)) != 1 {
		t.Fatalf("added leg: %+v", d.Changes)
	}
	if d.Changes[0].Type != TypeLegAdded || d.Changes[0].Leg != "b" || d.Changes[1].Type != TypeAdded || d.Changes[1].NewStep != 3 {
		t.Errorf("added leg changes %+v", d.Changes)
	}
	back := Rows(new, old)
	if len(changesOfType(back, TypeLegRemoved)) != 1 || len(changesOfType(back, TypeRemoved)) != 1 {
		t.Fatalf("removed leg: %+v", back.Changes)
	}
}

func TestDiffKindSwapIsRemovePlusAdd(t *testing.T) {
	old := smallRows()
	new := cloneRows(old)
	new.Steps[1].Kind = planexport.KindBoss
	d := Rows(old, new)
	if len(changesOfType(d, TypeRemoved)) != 1 || len(changesOfType(d, TypeAdded)) != 1 {
		t.Fatalf("kind swap paired: %+v", d.Changes)
	}
	if len(d.Changes) != 2 {
		t.Errorf("%d changes, want 2", len(d.Changes))
	}
}
