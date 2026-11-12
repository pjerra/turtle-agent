package planexport

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The deployed Onyxia migrations carry the ai_dungeon_boss block (a DELETE and a REPLACE INTO) that WriteSQL never writes: a worker
// reads them all the same, and the plan tables come out complete.
func TestParseMigrationReadsHandEditedRaidMigration(t *testing.T) {
	for _, c := range []struct {
		file string
		z26  float32 // the z of step 26 (seq 25): the mapped -74.62397 in 120000, the fixed -66.7 in 130000
	}{
		{"onyxias_lair-20261029120000.sql", -74.62397},
		{"onyxias_lair-20261029130000.sql", -66.7},
	} {
		t.Run(c.file, func(t *testing.T) {
			rows, notes, err := ParseMigration(readFixture(t, c.file))
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 {
				t.Fatalf("%d plans, want 1", len(rows))
			}
			r := rows[0]
			if r.Plan.Plan != "onyxias_lair" || r.Plan.Map != 249 {
				t.Fatalf("plan row %+v", r.Plan)
			}
			if len(r.Legs) != 1 || len(r.Steps) != 29 {
				t.Fatalf("%d legs, %d steps, want 1 and 29", len(r.Legs), len(r.Steps))
			}
			if s := r.Steps[25]; s.Seq != 25 || math.Abs(float64(s.Z-c.z26)) > 1e-4 {
				t.Fatalf("step 26: seq %d, z %v, want seq 25, z %v", s.Seq, s.Z, c.z26)
			}
			if s := r.Steps[28]; s.Kind != "boss" || s.Entry != 10184 {
				t.Fatalf("last step %+v, want the boss 10184", s)
			}
			if len(notes) != 1 || !strings.HasPrefix(notes[0], "skipped ai_dungeon_boss:") {
				t.Fatalf("notes %q, want one note on the skipped ai_dungeon_boss block", notes)
			}
		})
	}
}

// Parse stays strict: the same file stops at the REPLACE INTO line WriteSQL never writes.
func TestParseStillRefusesReplaceInto(t *testing.T) {
	_, err := Parse(readFixture(t, "onyxias_lair-20261029120000.sql"))
	if err == nil {
		t.Fatal("strict Parse read a migration with a REPLACE INTO")
	}
	for _, want := range []string{"line 107", "REPLACE INTO `ai_dungeon_boss`"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name %q", err, want)
		}
	}
}

func TestParseMigrationRoundTrip(t *testing.T) {
	want := []Rows{build(t, parsePlan(t, small))}
	got, notes, err := ParseMigration(WriteSQL(want))
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 {
		t.Fatalf("notes %q on a migration of plan tables only", notes)
	}
	if err := Equal(want, got); err != nil {
		t.Fatal(err)
	}
}

// secondPlan is a copy of r under another plan name, so a migration can hold two plans.
func secondPlan(r Rows, name string) Rows {
	r.Plan.Plan = name
	r.Legs = append([]LegRow(nil), r.Legs...)
	r.Steps = append([]StepRow(nil), r.Steps...)
	for i := range r.Legs {
		r.Legs[i].Plan = name
	}
	for i := range r.Steps {
		r.Steps[i].Plan = name
	}
	return r
}

// A migration may carry hand edits the exporter cannot write (zulfarrak's 20261102190000: UPDATE ai_dungeon_plan,
// UPDATE ai_dungeon_step). The lenient reader skips each to its `;`, says the hand edits are NOT applied, and reads the rows
// after it — an INSERT after an UPDATE included. It used to stop at the first and read nothing after.
func TestParseMigrationSkipsHandEditsAndReadsOn(t *testing.T) {
	a := build(t, parsePlan(t, small))
	b := secondPlan(a, "Tiny_Cave_B")
	want := []Rows{a, b}
	sql := WriteSQL([]Rows{a}) + `
-- hand-set after the export
UPDATE ` + "`ai_dungeon_plan`" + ` SET ` + "`cc_priority`" + ` = '7246' WHERE ` + "`plan`" + ` = 'Tiny_Cave';
UPDATE ` + "`ai_dungeon_step`" + `
   SET ` + "`entry`" + ` = 962
 WHERE ` + "`plan`" + ` = 'Tiny_Cave';
` + WriteSQL([]Rows{b})
	got, notes, err := ParseMigration(sql)
	if err != nil {
		t.Fatalf("a hand edit stopped the reader: %v", err)
	}
	if err := Equal(want, got); err != nil {
		t.Fatalf("the rows after the hand edits were not read: %v", err)
	}
	var skipped int
	for _, n := range notes {
		if strings.Contains(n, "is not an INSERT statement") && strings.Contains(n, "hand edits are NOT applied") {
			skipped++
		}
	}
	if skipped != 2 {
		t.Fatalf("notes %q, want one for each of the two skipped UPDATE statements", notes)
	}
	if _, err := Parse(sql); err == nil {
		t.Fatal("strict Parse read a migration with an UPDATE")
	}
}

// A migration wrapped in START TRANSACTION ... COMMIT reads the same: each wrapper statement is skipped and named, and the
// plan rows between them are read.
func TestParseMigrationSkipsATransactionWrapper(t *testing.T) {
	want := []Rows{build(t, parsePlan(t, small))}
	got, notes, err := ParseMigration("START TRANSACTION;\n" + WriteSQL(want) + "COMMIT;\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := Equal(want, got); err != nil {
		t.Fatal(err)
	}
	if len(notes) != 2 || !strings.Contains(notes[0], "START TRANSACTION") || !strings.Contains(notes[1], "COMMIT") {
		t.Fatalf("notes %q, want START TRANSACTION and COMMIT named once each", notes)
	}
}
