package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

const tinyPlan = `{
  "version": 1,
  "name": "tiny",
  "status": "live-checked",
  "map": 1,
  "entrance": {"x": 0, "y": 0, "z": 0},
  "legs": [
    {
      "name": "first",
      "to": {"x": 10, "y": 0, "z": 0},
      "pull_groups": [
        {"entries": [1], "positions": [{"x": 10, "y": 0, "z": 0}]}
      ]
    }
  ]
}`

func writeTinyPlan(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "tiny.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func tinyRows(t *testing.T) planexport.Rows {
	t.Helper()
	dir := t.TempDir()
	p, err := dungeon.Load(writeTinyPlan(t, dir, tinyPlan))
	if err != nil {
		t.Fatal(err)
	}
	res, err := planexport.Build(p, planexport.Options{Source: "drift-test", DropCompanionsLoot: true})
	if err != nil {
		t.Fatal(err)
	}
	return res.Rows
}

func writeMigration(t *testing.T, dir, name string, rows []planexport.Rows) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(planexport.WriteSQL(rows)), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseMigrationReadsTheThreeTablesAndIgnoresOthers(t *testing.T) {
	sql := `
-- a comment with a ; that is not a statement end
CREATE TABLE IF NOT EXISTS ` + "`ai_dungeon_plan`" + ` (` + "`plan`" + ` varchar(32));
INSERT INTO ` + "`ai_dungeon_plan`" + ` (` + "`plan`, `map`, `cc_priority`, `cc_always`, `kill_first`, `source`, `comment`" + `) VALUES
('tiny', 1, '2,3', '', '4', 'src', 'a; comment');
INSERT INTO ` + "`ai_dungeon_leg`" + ` (` + "`plan`, `leg`, `name`, `comment`" + `) VALUES
('tiny', 0, 'first', '');
INSERT INTO ` + "`ai_dungeon_step`" + ` (` + "`plan`, `leg`, `seq`, `kind`, `entry`, `x`, `y`, `z`, `radius`, `item`, `param`, `flags`, `timeout_s`, `entries`, `comment`" + `) VALUES
('tiny', 0, 0, 'move', 0, 1.5, 2.25, 3, 0, 0, 0, 0, 0, '', 'to'),
('tiny', 0, 1, 'pull', 0, 10, 0, 0, 15, 0, 0, 0, 0, '1,2', 'pull group 0');
INSERT INTO ` + "`ai_dungeon_leg_need`" + ` (` + "`plan`, `leg`, `item`" + `) VALUES
('tiny', 0, 9240);
REPLACE INTO ` + "`ai_dungeon_boss`" + ` (` + "`map`, `entry`, `name`" + `) VALUES
(1, 2, 'Boss');
`
	plans, err := ParseMigration(sql)
	if err != nil {
		t.Fatal(err)
	}
	r, ok := plans["tiny"]
	if !ok {
		t.Fatalf("tiny not parsed: %+v", plans)
	}
	if r.Plan.Map != 1 || entriesText(r.Plan.CCPriority) != "2,3" || entriesText(r.Plan.KillFirst) != "4" {
		t.Fatalf("plan row %+v", r.Plan)
	}
	if len(r.Legs) != 1 || r.Legs[0].Name != "first" {
		t.Fatalf("leg rows %+v", r.Legs)
	}
	if len(r.Steps) != 2 || r.Steps[1].Kind != "pull" || entriesText(r.Steps[1].Entries) != "1,2" || r.Steps[1].Radius != 15 {
		t.Fatalf("step rows %+v", r.Steps)
	}
}

func TestLoadExportsPrefersTheNewestFullExport(t *testing.T) {
	sqlDir := t.TempDir()
	rows := tinyRows(t)
	writeMigration(t, sqlDir, "20260101000000_world.sql", []planexport.Rows{rows})

	// A later migration that only replaces one leg's steps is an update, not a new export.
	partial := `
DELETE FROM ` + "`ai_dungeon_step`" + ` WHERE ` + "`plan`" + ` = 'tiny' AND ` + "`leg`" + ` = 0;
INSERT INTO ` + "`ai_dungeon_step`" + ` (` + "`plan`, `leg`, `seq`, `kind`, `entry`, `x`, `y`, `z`, `radius`, `item`, `param`, `flags`, `timeout_s`, `entries`, `comment`" + `) VALUES
('tiny', 0, 0, 'move', 0, 99, 0, 0, 0, 0, 0, 0, 0, '', 'patched');
`
	if err := os.WriteFile(filepath.Join(sqlDir, "20260102000000_world.sql"), []byte(partial), 0o644); err != nil {
		t.Fatal(err)
	}
	exports, err := LoadExports(sqlDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := exports["tiny"].File; got != "20260101000000_world.sql" {
		t.Fatalf("newest export %q, want the full export", got)
	}
}

func TestRunReportsInSyncAndDifferences(t *testing.T) {
	plansDir := t.TempDir()
	writeTinyPlan(t, plansDir, tinyPlan)
	sqlDir := t.TempDir()
	rows := tinyRows(t)
	writeMigration(t, sqlDir, "20260101000000_world.sql", []planexport.Rows{rows})

	rep, err := Run(plansDir, sqlDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Plans) != 1 || rep.Plans[0].State != stateInSync {
		t.Fatalf("report %+v", rep)
	}

	// A newer full export with one point moved differs, and the first differing step names it.
	changed := tinyRows(t)
	changed.Steps[0].X = 11.25
	writeMigration(t, sqlDir, "20260102000000_world.sql", []planexport.Rows{changed})
	rep, err = Run(plansDir, sqlDir)
	if err != nil {
		t.Fatal(err)
	}
	r := rep.Plans[0]
	if r.State != stateDiffers || r.Upstream != "20260102000000_world.sql" || !strings.Contains(r.FirstStep, "leg 0 seq 0") {
		t.Fatalf("report %+v", r)
	}
	if len(Differences(rep)) != 1 {
		t.Fatalf("differences %+v", Differences(rep))
	}
}

func TestRunReportsNotExported(t *testing.T) {
	plansDir := t.TempDir()
	writeTinyPlan(t, plansDir, tinyPlan)
	rep, err := Run(plansDir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Plans) != 1 || rep.Plans[0].State != stateNotExported {
		t.Fatalf("report %+v", rep)
	}
}

func TestCompareRowsNamesTheFirstStepDifference(t *testing.T) {
	rows := tinyRows(t)
	first, step := CompareRows(rows, rows)
	if first != "" || step != "" {
		t.Fatalf("equal rows differ: %q %q", first, step)
	}
	changed := tinyRows(t)
	changed.Steps[0].Kind = "pull"
	first, step = CompareRows(rows, changed)
	if !strings.Contains(first, "leg 0 seq 0") || !strings.Contains(step, "kind upstream") {
		t.Fatalf("first %q step %q", first, step)
	}

	planChanged := tinyRows(t)
	planChanged.Plan.KillFirst = []uint32{9}
	first, step = CompareRows(rows, planChanged)
	if !strings.Contains(first, "kill_first") || step != "" {
		t.Fatalf("first %q step %q", first, step)
	}
}

func TestSplitTupleHandlesQuotesAndCommas(t *testing.T) {
	got, err := splitTuple(`('turtle', 'Gni''kiv, the Medallion', 1, 'a\\b')`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"turtle", "Gni'kiv, the Medallion", "1", `a\b`}
	if len(got) != len(want) {
		t.Fatalf("%q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("value %d: %q, want %q", i, got[i], want[i])
		}
	}
}
