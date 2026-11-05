package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// --as renames every plan row to the shadow name: only the plan column changes.
func TestPlanExportAsRenamesEveryRow(t *testing.T) {
	dir := t.TempDir()
	code, out, errs := runExport("--dir", "../../dungeons", "--stamp", "20261014201000", "--rev", "abc12345", "--out", dir, "--as", "uldaman_x1", "--allow-draft", "uldaman")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if !strings.Contains(out, "uldaman: map 70") || !strings.Contains(out, "as uldaman_x1") {
		t.Fatalf("output %q names no shadow", out)
	}
	data, err := os.ReadFile(filepath.Join(dir, "20261014201000_world.sql"))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := planexport.Parse(string(data))
	if err != nil || len(rows) != 1 {
		t.Fatalf("%v %d", err, len(rows))
	}
	got := rows[0]
	if got.Plan.Plan != "uldaman_x1" {
		t.Fatalf("plan row %q, want uldaman_x1", got.Plan.Plan)
	}
	for _, l := range got.Legs {
		if l.Plan != "uldaman_x1" {
			t.Fatalf("leg row %q, want uldaman_x1", l.Plan)
		}
	}
	for _, s := range got.Steps {
		if s.Plan != "uldaman_x1" {
			t.Fatalf("step row kind %s plan %q, want uldaman_x1", s.Kind, s.Plan)
		}
	}
	// Only the plan column changes: the rows equal the plain export row for row.
	p, err := dungeon.Load("../../dungeons/uldaman.json")
	if err != nil {
		t.Fatal(err)
	}
	base, err := planexport.Build(p, planexport.Options{Source: "turtle-agent abc12345 dungeons/uldaman.json"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Plan.Map != base.Rows.Plan.Map || len(got.Legs) != len(base.Rows.Legs) || len(got.Steps) != len(base.Rows.Steps) {
		t.Fatalf("shadow shape %+v, want %+v", got.Plan, base.Rows.Plan)
	}
	strip := func(r planexport.Rows) planexport.Rows {
		r.Plan.Plan = ""
		r.Plan.Source = ""
		for i := range r.Legs {
			r.Legs[i].Plan = ""
		}
		for i := range r.Steps {
			r.Steps[i].Plan = ""
		}
		return r
	}
	if err := planexport.Equal([]planexport.Rows{strip(base.Rows)}, []planexport.Rows{strip(got)}); err != nil {
		t.Fatalf("shadow rows differ from the plain export: %v", err)
	}
}

// The DELETEs of a shadow export touch only the shadow name: the base plan's rows survive.
func TestPlanExportAsNeverDeletesAnotherPlan(t *testing.T) {
	dir := t.TempDir()
	code, _, errs := runExport("--dir", "../../dungeons", "--stamp", "20261014201100", "--out", dir, "--as", "uldaman_x1", "uldaman")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errs)
	}
	data, err := os.ReadFile(filepath.Join(dir, "20261014201100_world.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "DELETE FROM") && strings.Contains(line, "uldaman") && !strings.Contains(line, "uldaman_x1") {
			t.Fatalf("the shadow export deletes the base plan's rows: %s", line)
		}
	}
}

// Shadow names are server plan names without a dash: ^[a-z0-9_]+$.
func TestPlanExportAsRefusesBadNames(t *testing.T) {
	for _, name := range []string{"Uldaman_x1", "uldaman-x1", "uldaman x1", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		code, _, errs := runExport("--dir", "../../dungeons", "--out", t.TempDir(), "--as", name, "uldaman")
		if code != exitUsage || !strings.Contains(errs, "--as") {
			t.Fatalf("%q: exit %d %q, want the --as refusal", name, code, errs)
		}
	}
}

// --as needs a single named plan, not --all-live: a batch export never mixes shadows in.
func TestPlanExportAsRefusesAllLive(t *testing.T) {
	code, _, errs := runExport("--dir", "../../dungeons", "--out", t.TempDir(), "--as", "uldaman_x1", "--all-live")
	if code != exitUsage || !strings.Contains(errs, "--as") {
		t.Fatalf("exit %d %q, want the --as refusal", code, errs)
	}
}

// The raid-walk dry run with a shadow plan reads the shadow rows from --sql and the geometry from the base plan's dir.
func TestRaidWalkShadowPlanDryRun(t *testing.T) {
	sqlDir, baseDir := t.TempDir(), t.TempDir()
	if code, _, errs := runExport("--dir", "../../dungeons", "--stamp", "20261014201200", "--out", sqlDir, "--as", "onyxias_lair_x1", "--allow-draft", "onyxias_lair"); code != exitOK {
		t.Fatalf("export: exit %d: %s", code, errs)
	}
	sql := filepath.Join(sqlDir, "20261014201200_world.sql")
	// The B-plan dir holds the base plan's file under the shadow name (the walk of piece 5 writes this).
	raw, err := os.ReadFile("../../dungeons/onyxias_lair.json")
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte(`"name":"onyxias_lair"`), []byte(`"name":"onyxias_lair_x1"`), 1)
	if err := os.WriteFile(filepath.Join(baseDir, "onyxias_lair_x1.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := raidWalkCmd(nil, []string{"onyxias_lair_x1", "--dir", baseDir, "--sql", sql, "--dry-run"}, &out, &errOut)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "run start onyxias_lair_x1 until boss") {
		t.Fatalf("dry run does not start the shadow plan:\n%s", out.String())
	}
	if !strings.Contains(errOut.String(), "shadow onyxias_lair_x1: base plan onyxias_lair from") && !strings.Contains(out.String(), "onyxias_lair") {
		t.Fatalf("dry run names no base plan:\n%s\n%s", out.String(), errOut.String())
	}
}

// plan-check lints a shadow migration's plan rows by name.
func TestPlanCheckShadowPlanByName(t *testing.T) {
	sqlDir := t.TempDir()
	if code, _, errs := runExport("--dir", "../../dungeons", "--stamp", "20261014201300", "--out", sqlDir, "--as", "onyxias_lair_x1", "--allow-draft", "onyxias_lair"); code != exitOK {
		t.Fatalf("export: exit %d: %s", code, errs)
	}
	var out, errOut bytes.Buffer
	code := planCheckCmd([]string{"--mmaps", t.TempDir(), "--sql", filepath.Join(sqlDir, "20261014201300_world.sql"), "--plan", "onyxias_lair_x1"}, &out, &errOut)
	if code == exitUsage && strings.Contains(errOut.String(), "holds no plan") {
		t.Fatalf("shadow plan not found: %s", errOut.String())
	}
}
