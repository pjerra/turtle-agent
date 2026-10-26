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

// A plan with a leg whose companions_loot is false is refused when named, listed and skipped by --all-live (the batch goes on), and
// exported with --drop-companions-loot.
func TestPlanExportCompanionsLootFalse(t *testing.T) {
	dir := t.TempDir()
	plans := t.TempDir()
	live := `{"version":1,"name":"livecave","status":"live-checked","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[{"name":"a","to":{"x":1,"y":0,"z":0},"boss":{"entry":1,"position":{"x":5,"y":0,"z":0}}}]}`
	grave := strings.NewReplacer("livecave", "gravecave", `"name":"a",`, `"name":"a","companions_loot":false,`).Replace(live)
	for n, js := range map[string]string{"livecave": live, "gravecave": grave} {
		if err := os.WriteFile(filepath.Join(plans, n+".json"), []byte(js), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if code, _, errs := runExport("--dir", plans, "--out", dir, "gravecave"); code != exitUsage || !strings.Contains(errs, "companions_loot is false") {
		t.Fatalf("named: %d %q", code, errs)
	}
	code, out, errs := runExport("--dir", plans, "--out", dir, "--stamp", "20261014190200", "--all-live")
	if code != exitOK || !strings.Contains(out, "livecave") || strings.Contains(out, "gravecave: map") || !strings.Contains(errs, "gravecave: skipped") {
		t.Fatalf("--all-live: %d %q %q", code, out, errs)
	}
	if code, out, _ := runExport("--dir", plans, "--out", dir, "--stamp", "20261014190300", "--drop-companions-loot", "gravecave"); code != exitOK || !strings.Contains(out, "gravecave: map 70") {
		t.Fatalf("--drop-companions-loot: %d %q", code, out)
	}
	if code, _, errs := runExport("--dir", plans, "--out", dir, "--stamp", "20261014190400", "--all-live", "gravecave"); code != exitUsage {
		t.Fatalf("%d %q", code, errs)
	}
}

// A plan with a step the run leader cannot do after its first leg is exported up to that leg, with the note on stderr, and --all-live goes on
// (exit 0); a plan whose first leg has one is refused when named and fails the batch.
func TestPlanExportCutsAtAStepTheRunLeaderCannotDo(t *testing.T) {
	dir := t.TempDir()
	plans := t.TempDir()
	walk := `{"name":"a","to":{"x":1,"y":0,"z":0},"boss":{"entry":1,"position":{"x":5,"y":0,"z":0}}}`
	talk := `{"name":"b","to":{"x":10,"y":0,"z":0},"steps":[{"gossip":{"npc":7607,"near":{"x":12,"y":0,"z":0},"text":"blow","by":"tank"}}]}`
	mk := func(name string, legs ...string) string {
		return `{"version":1,"name":"` + name + `","status":"live-checked","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[` + strings.Join(legs, ",") + `]}`
	}
	for n, js := range map[string]string{"cutcave": mk("cutcave", walk, talk), "wholecave": mk("wholecave", walk)} {
		if err := os.WriteFile(filepath.Join(plans, n+".json"), []byte(js), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	code, out, errs := runExport("--dir", plans, "--out", dir, "--stamp", "20261014190500", "--all-live")
	if code != exitOK || !strings.Contains(out, "cutcave: map 70, 1 legs") || !strings.Contains(out, "cutcave: CUT: 1 of 2 legs exported") || !strings.Contains(out, "wholecave: map 70") ||
		!strings.Contains(errs, "cutcave: plan cutcave: exported legs 0-0 (a) only; cut before leg 1 (b)") || strings.Contains(errs, "wholecave") {
		t.Fatalf("--all-live: %d %q %q", code, out, errs)
	}
	first := filepath.Join(plans, "firstcave.json")
	if err := os.WriteFile(first, []byte(mk("firstcave", talk, walk)), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := runExport("--dir", plans, "--out", dir, "--stamp", "20261014190600", "firstcave"); code != exitUsage || !strings.Contains(errs, "leg 0 (b)") || !strings.Contains(errs, "gossip (NPC 7607") {
		t.Fatalf("first leg: %d %q", code, errs)
	}
}

// --skip-legs leaves a named leg out (the shipped Zul'Farrak plan then reaches the End Door and Ukorz past Bly), and a name that is no leg
// of the plan is refused before anything is written.
func TestPlanExportSkipLegs(t *testing.T) {
	dir := t.TempDir()
	code, out, errs := runExport("--dir", "../../dungeons", "--drop-companions-loot", "--skip-legs", "bly", "--stamp", "20261021005900", "--rev", "abc12345", "--out", dir, "zulfarrak")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if !strings.Contains(out, "zulfarrak: map 209, 17 legs") || strings.Contains(out, "CUT") {
		t.Fatalf("output %q", out)
	}
	data, err := os.ReadFile(filepath.Join(dir, "20261021005900_world.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "'gossip', 7607,") || !strings.Contains(string(data), "'ukorz'") || strings.Contains(string(data), "'bly'") {
		t.Fatal("the rows lack the Weegli gossip or Ukorz, or still have Bly")
	}
	if !strings.Contains(errs, "leg 16 (bly): left out of the export by the exporter's skip-legs option") {
		t.Fatalf("stderr %q", errs)
	}
	code, _, errs = runExport("--dir", "../../dungeons", "--drop-companions-loot", "--skip-legs", "bli", "--stamp", "20261021005901", "--out", dir, "zulfarrak")
	if code == exitOK || !strings.Contains(errs, `skip-legs names "bli"`) {
		t.Fatalf("a misspelt leg: %d %q", code, errs)
	}
	if _, err := os.Stat(filepath.Join(dir, "20261021005901_world.sql")); err == nil {
		t.Fatal("a file was written for a refused export")
	}
}

// --carrier-chests needs --drop-companions-loot (refused alone); with it, the zumrah leg's 31 Shallow Grave steps are 31 triples of
// move, loot (flag 256) and sweep pull in the rows, before the trigger and the boss.
func TestPlanExportCarrierChests(t *testing.T) {
	dir := t.TempDir()
	if code, _, errs := runExport("--dir", "../../dungeons", "--skip-legs", "bly", "--carrier-chests", "--stamp", "20261021200000", "--out", dir, "zulfarrak"); code != exitUsage || !strings.Contains(errs, "--carrier-chests needs --drop-companions-loot") {
		t.Fatalf("without --drop-companions-loot: %d %q", code, errs)
	}
	if code, _, errs := runExport("--dir", "../../dungeons", "--skip-legs", "bly", "--carrier-chests", "--drop-companions-loot", "--stamp", "20261021200100", "--rev", "abc12345", "--out", dir, "zulfarrak"); code != exitOK {
		t.Fatalf("exit %d: %s", code, errs)
	}
	data, err := os.ReadFile(filepath.Join(dir, "20261021200100_world.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "'loot', 128403,") {
		t.Fatal("no grave loot row in the migration")
	}
	rows, err := planexport.Parse(string(data))
	if err != nil {
		t.Fatal(err)
	}
	r := rows[0]
	n := 0
	for _, s := range r.Steps {
		if s.Entry == 128403 {
			n++
			if s.Flags&planexport.FlagGoneIsDone == 0 {
				t.Fatalf("a grave loot row without flag 256: %+v", s)
			}
		}
	}
	if n != 31 {
		t.Fatalf("%d grave loot rows, want 31", n)
	}
}

// --with-optional writes a named optional leg into the rows (the shipped Zul'Farrak plan's gahzrilla: the gong as a use_on row) with the
// warning that the Mallet is the operator's to give; a name that is no optional leg is refused before anything is written.
func TestPlanExportWithOptional(t *testing.T) {
	dir := t.TempDir()
	code, out, errs := runExport("--dir", "../../dungeons", "--drop-companions-loot", "--with-optional", "gahzrilla", "--stamp", "20261021180000", "--rev", "abc12345", "--out", dir, "zulfarrak")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if !strings.Contains(out, "zulfarrak: map 209, 18 legs") || strings.Contains(out, "CUT") {
		t.Fatalf("output %q", out)
	}
	data, err := os.ReadFile(filepath.Join(dir, "20261021180000_world.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "'use_on', 141832, 1650.9, 1171.9, 10.9,") || !strings.Contains(string(data), "'gahzrilla'") {
		t.Fatal("the rows lack the gong's use_on or the gahzrilla leg")
	}
	if !strings.Contains(errs, "leg 9 (gahzrilla): optional, in the rows by --with-optional: give Mallet of Zul'Farrak (item 9240) to the lead bot") {
		t.Fatalf("stderr %q", errs)
	}
	code, _, errs = runExport("--dir", "../../dungeons", "--drop-companions-loot", "--with-optional", "courtyard", "--stamp", "20261021180001", "--out", dir, "zulfarrak")
	if code == exitOK || !strings.Contains(errs, `with-optional names "courtyard", which is no optional leg`) {
		t.Fatalf("a leg that is not optional: %d %q", code, errs)
	}
	if _, err := os.Stat(filepath.Join(dir, "20261021180001_world.sql")); err == nil {
		t.Fatal("a file was written for a refused export")
	}
}

// By default the optional leg is written with its need row; --without-optional leaves it out with the leave-out warning.
func TestPlanExportWithoutOptional(t *testing.T) {
	dir := t.TempDir()
	code, out, errs := runExport("--dir", "../../dungeons", "--drop-companions-loot", "--stamp", "20261021180002", "--rev", "abc12345", "--out", dir, "zulfarrak")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if !strings.Contains(out, "zulfarrak: map 209, 18 legs") {
		t.Fatalf("output %q", out)
	}
	data, err := os.ReadFile(filepath.Join(dir, "20261021180002_world.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "('zulfarrak', 9, 9240, 1, 0, 0, 'Mallet of Zul''Farrak', '')") {
		t.Fatal("the rows lack the gahzrilla need row")
	}
	code, out, errs = runExport("--dir", "../../dungeons", "--drop-companions-loot", "--without-optional", "--stamp", "20261021180003", "--rev", "abc12345", "--out", dir, "zulfarrak")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if !strings.Contains(out, "zulfarrak: map 209, 17 legs") {
		t.Fatalf("output %q", out)
	}
	data, err = os.ReadFile(filepath.Join(dir, "20261021180003_world.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "'gahzrilla'") {
		t.Fatal("the rows keep the gahzrilla leg with --without-optional")
	}
	if !strings.Contains(errs, "leg 9 (gahzrilla): optional (needs Mallet of Zul'Farrak (item 9240), which the run leader's bots do not carry): left out of the export") {
		t.Fatalf("stderr %q", errs)
	}
}
