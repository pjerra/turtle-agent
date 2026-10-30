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
