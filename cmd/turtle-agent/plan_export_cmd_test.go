package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/planexport"
)

func runExport(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := planExportCmd(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestPlanExportUldaman(t *testing.T) {
	dir := t.TempDir()
	code, out, errs := runExport("--dir", "../../dungeons", "--stamp", "20261014180000", "--rev", "abc12345", "--out", dir, "uldaman")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errs)
	}
	file := filepath.Join(dir, "20261014180000_world.sql")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "uldaman: map 70") || !strings.Contains(out, "wrote "+file) {
		t.Fatalf("output %q", out)
	}
	rows, err := planexport.Parse(string(data))
	if err != nil || len(rows) != 1 {
		t.Fatalf("%v %d", err, len(rows))
	}
	if rows[0].Plan.Source != "turtle-agent abc12345 dungeons/uldaman.json" {
		t.Fatalf("source %q", rows[0].Plan.Source)
	}
	// the same export again is refused: a migration that is applied is never replaced
	if code, _, errs = runExport("--dir", "../../dungeons", "--stamp", "20261014180000", "--out", dir, "uldaman"); code != exitUsage || !strings.Contains(errs, "exists") {
		t.Fatalf("second export: %d %q", code, errs)
	}
	if code, _, errs = runExport("--dir", "../../dungeons", "--stamp", "20261014180000", "--out", dir, "--force", "uldaman"); code != exitOK {
		t.Fatalf("--force: %d %q", code, errs)
	}
}

func TestPlanExportRefusesDraftsAndAllLiveSkipsThem(t *testing.T) {
	dir := t.TempDir()
	plans := t.TempDir()
	live := `{"version":1,"name":"livecave","status":"live-checked","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[{"name":"a","to":{"x":1,"y":0,"z":0},"boss":{"entry":1,"position":{"x":5,"y":0,"z":0}}}]}`
	draft := strings.NewReplacer("livecave", "draftcave", `"live-checked"`, `"draft (not mapped, not live-checked)"`).Replace(live)
	for n, js := range map[string]string{"livecave": live, "draftcave": draft} {
		if err := os.WriteFile(filepath.Join(plans, n+".json"), []byte(js), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if code, _, errs := runExport("--dir", plans, "--out", dir, "draftcave"); code != exitUsage || !strings.Contains(errs, "draft") {
		t.Fatalf("draft: %d %q", code, errs)
	}
	if code, _, errs := runExport("--dir", plans, "--out", dir, "--allow-draft", "--stamp", "20261014190000", "draftcave"); code != exitOK {
		t.Fatalf("--allow-draft: %d %q", code, errs)
	}
	if code, out, errs := runExport("--dir", plans, "--out", dir, "--stamp", "20261014190100", "--all-live"); code != exitOK || !strings.Contains(out, "livecave") || strings.Contains(out, "draftcave") {
		t.Fatalf("--all-live: %d %q %q", code, out, errs)
	}
	if code, _, _ := runExport("--dir", plans, "--out", dir, "--all-live", "livecave"); code != exitUsage {
		t.Fatal("plans and --all-live together")
	}
	if code, _, _ := runExport("--dir", plans, "livecave"); code != exitUsage {
		t.Fatal("--out is required")
	}
}
