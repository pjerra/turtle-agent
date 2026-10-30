package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const reportCmdFight = `{"fight":{"id":"3-1","commander":"Cmd","plan":"zul_gurub","leg":"jeklik","step":2,"kind":"move","start":1793387810,"dur_ms":100000,"outcome":"end",
"engaged":[{"e":11350,"n":"Axe Thrower","c":2}],"deaths":[{"name":"Rosaline","t_ms":12000}],
"log":[{"k":"cast","t":10000,"sid":24024,"s":"Unstable Concoction","e":14750,"n":"Bat Rider"}]}}`

const reportCmdLog = "20:16:38.100 TBM:RUN|zul_gurub|running|1/50|jeklik|move|begin|started\n" +
	"20:16:52.200 TBM:RUN|zul_gurub|running|2/50|jeklik|move|walk|fighting: the tank engages 11372 on arrival (0.0 yd)\n"

func reportDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "combat-fight-3-1.json"), []byte(reportCmdFight), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.log"), []byte(reportCmdLog), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runReport(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := raidWalkCmd(context.Background(), append([]string{"report"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

func TestRaidWalkReportPrintsMarkdown(t *testing.T) {
	dir := reportDir(t)
	code, out, errs := runReport(t, dir, "--spell", "24024", "--clock-offset", "1h")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errs)
	}
	for _, want := range []string{"# Raid walk report: zul_gurub", "| 2 | 3-1 (move) | 19:16:50 | 100s |", "YES: fighting: the tank engages 11372 on arrival", "Unstable Concoction (24024) | 1 (Rosaline)"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
	// without the offset the log's 20:16:52 is read as UTC, an hour after the fight: the arrival line falls outside the fight's step window
	// and the contact column is still computed from the step's lines
	if code, out, _ := runReport(t, dir); code != exitOK || !strings.Contains(out, "# Raid walk report") {
		t.Fatalf("no offset: %d %s", code, out)
	}
}

func TestRaidWalkReportWritesFiles(t *testing.T) {
	dir := reportDir(t)
	out := filepath.Join(t.TempDir(), "rep")
	if code, stdout, errs := runReport(t, dir, "--out", out, "--clock-offset", "1h"); code != exitOK || stdout != "" {
		t.Fatalf("exit %d stdout %q: %s", code, stdout, errs)
	}
	raw, err := os.ReadFile(filepath.Join(out, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Format string `json:"format"`
		Pulls  []struct {
			Fight string `json:"fight"`
			Step  int    `json:"step"`
		} `json:"pulls"`
	}
	if err := json.Unmarshal(raw, &rep); err != nil || rep.Format != "tb-raid-walk-report" || len(rep.Pulls) != 1 || rep.Pulls[0].Step != 2 {
		t.Fatalf("json %v %+v", err, rep)
	}
	if _, err := os.Stat(filepath.Join(out, "report.md")); err != nil {
		t.Fatal(err)
	}
	// --json - prints the JSON instead of the Markdown
	code, js, _ := runReport(t, dir, "--json", "-")
	if code != exitOK || !strings.HasPrefix(js, "{") {
		t.Fatalf("--json -: %d %q", code, js)
	}
}

func TestRaidWalkReportReadsFightsFullAndRefuses(t *testing.T) {
	dir := t.TempDir()
	if code, _, errs := runReport(t, dir); code != exitRaidWalkFailed || !strings.Contains(errs, "no combat-fight-*.json") {
		t.Fatalf("empty folder: %d %s", code, errs)
	}
	if err := os.WriteFile(filepath.Join(dir, "fights-full.json"), []byte(`{"3-1":`+reportCmdFight+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errs := runReport(t, dir)
	if code != exitOK || !strings.Contains(out, "3-1 (move)") || !strings.Contains(out, "no run log") {
		t.Fatalf("fights-full.json without a log: %d %s %s", code, out, errs)
	}
	if code, _, _ := runReport(t); code != exitUsage {
		t.Fatalf("no dir: %d", code)
	}
	if code, _, errs := runReport(t, dir, "--spell", "x"); code != exitUsage || !strings.Contains(errs, "not a spell id") {
		t.Fatalf("bad spell: %d %s", code, errs)
	}
	if code, _, _ := runReport(t, dir, "--window", "0s"); code != exitUsage {
		t.Fatalf("bad window: %d", code)
	}
	if code, _, errs := runReport(t, dir, "--log", filepath.Join(dir, "nope.log")); code != exitRaidWalkFailed || errs == "" {
		t.Fatalf("a named log that is missing: %d %s", code, errs)
	}
}

func TestParseSpellIDs(t *testing.T) {
	ids, err := parseSpellIDs([]string{"24024,16508", "24024", " 5 "})
	if err != nil || len(ids) != 3 || ids[0] != 24024 || ids[1] != 16508 || ids[2] != 5 {
		t.Fatalf("%v %v", ids, err)
	}
	if _, err := parseSpellIDs([]string{"0"}); err == nil {
		t.Fatal("spell 0 taken")
	}
}
