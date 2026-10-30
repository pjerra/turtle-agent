package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/planexport"
)

func writePlanDiffFixture(t *testing.T, dir, name, sql string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(sql), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPlanDiffCmdFailOnChange(t *testing.T) {
	old := "../../internal/planexport/testdata/onyxias_lair-20261029120000.sql"
	new := "../../internal/planexport/testdata/onyxias_lair-20261029130000.sql"
	var out, errOut bytes.Buffer
	if code := planDiffCmd([]string{old, new}, &out, &errOut); code != exitOK {
		t.Fatalf("without the flag: exit %d (%s)", code, errOut.String())
	}
	out.Reset()
	if code := planDiffCmd([]string{old, new, "--fail-on-change"}, &out, &errOut); code != exitPlanDiffChanged {
		t.Fatalf("with the flag: exit %d, want %d (%s)", code, exitPlanDiffChanged, errOut.String())
	}

	// A comment-only change is not a change for --fail-on-change.
	data, err := os.ReadFile(new)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.ReplaceAll(string(data), ", 'path'),", ", 'walk'),")
	if edited == string(data) {
		t.Fatal("the fixture has no 'path' comment to change")
	}
	dir := t.TempDir()
	a := writePlanDiffFixture(t, dir, "a.sql", string(data))
	b := writePlanDiffFixture(t, dir, "b.sql", edited)
	out.Reset()
	code := planDiffCmd([]string{"--fail-on-change", a, b}, &out, &errOut)
	if code != exitOK {
		t.Fatalf("comment only: exit %d (%s)\n%s", code, errOut.String(), out.String())
	}
	if !strings.Contains(out.String(), "0 moved, 0 changed, 0 added, 0 removed,") || !strings.Contains(out.String(), "comment") {
		t.Errorf("comment-only diff text:\n%s", out.String())
	}
}

func TestPlanDiffCmdRefusesMapMismatch(t *testing.T) {
	mk := func(m uint32) string {
		return planexport.WriteSQL([]planexport.Rows{{
			Plan:  planexport.PlanRow{Plan: "x", Map: m},
			Legs:  []planexport.LegRow{{Plan: "x", Leg: 0, Name: "a"}},
			Steps: []planexport.StepRow{{Plan: "x", Leg: 0, Seq: 0, Kind: "move", X: 1, Y: 2, Z: 3}},
		}})
	}
	dir := t.TempDir()
	a := writePlanDiffFixture(t, dir, "a.sql", mk(249))
	b := writePlanDiffFixture(t, dir, "b.sql", mk(250))
	var out, errOut bytes.Buffer
	if code := planDiffCmd([]string{a, b}, &out, &errOut); code != exitUsage {
		t.Fatalf("exit %d, want %d (%s)", code, exitUsage, errOut.String())
	}
	if !strings.Contains(errOut.String(), "different maps") {
		t.Errorf("error %q", errOut.String())
	}
}

func TestDiffJSONInput(t *testing.T) {
	data, err := os.ReadFile("../../dungeons/ragefire_chasm.json")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	oldFile := filepath.Join(dir, "rfc.json")
	if err := os.WriteFile(oldFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	legs := p["legs"].([]any)
	to := legs[0].(map[string]any)["to"].(map[string]any)
	to["x"] = to["x"].(float64) + 1
	edited, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	newFile := filepath.Join(dir, "rfc-moved.json")
	if err := os.WriteFile(newFile, edited, 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := planDiffCmd([]string{oldFile, newFile}, &out, &errOut); code != exitOK {
		t.Fatalf("exit %d (%s)", code, errOut.String())
	}
	text := out.String()
	if !strings.Contains(text, "1 moved, 0 changed, 0 added, 0 removed, 0 comment") {
		t.Errorf("text:\n%s", text)
	}
	if !strings.Contains(text, "rows built from the plan JSON (no navmesh cut)") {
		t.Errorf("no plan-JSON note:\n%s", text)
	}
	if strings.Count(text, "moved 1.00 yd") != 1 {
		t.Errorf("want one moved step of 1.00 yd:\n%s", text)
	}
}

func TestPlanDiffCmdOutLayout(t *testing.T) {
	old := "../../internal/planexport/testdata/onyxias_lair-20261029120000.sql"
	new := "../../internal/planexport/testdata/onyxias_lair-20261029130000.sql"
	dir := t.TempDir()
	outDir := filepath.Join(dir, "out")
	var out, errOut bytes.Buffer
	args := []string{old, new, "--out", outDir, "--minimaps", filepath.Join(dir, "no-pictures")}
	if code := planDiffCmd(args, &out, &errOut); code != exitOK {
		t.Fatalf("exit %d (%s)", code, errOut.String())
	}
	for _, n := range []string{"diff-onyxias_lair.txt", "diff-onyxias_lair.json", "diff-onyxias_lair.svg"} {
		st, err := os.Stat(filepath.Join(outDir, n))
		if err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Errorf("%s: mode %v", n, st.Mode().Perm())
		}
	}
	if !strings.Contains(out.String(), "blank background") {
		t.Errorf("no note for the missing picture:\n%s", out.String())
	}
	// A second run refuses to overwrite the diff.
	if code := planDiffCmd(args, &out, &errOut); code != exitUsage || !strings.Contains(errOut.String(), "exists") {
		t.Errorf("second run: exit %d (%s)", code, errOut.String())
	}
	// --picture none without --svg writes no SVG.
	noneDir := filepath.Join(dir, "none")
	if code := planDiffCmd([]string{old, new, "--out", noneDir, "--picture", "none"}, &out, &errOut); code != exitOK {
		t.Fatalf("picture none: exit %d (%s)", code, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(noneDir, "diff-onyxias_lair.svg")); err == nil {
		t.Error("--picture none wrote a blank SVG")
	}
}
