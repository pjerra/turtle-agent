package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Generate puts a warning in the leg notes for a boss that is an NPC or in a faction the caller named friendly.
func TestGenerateWarnsAboutBossNPCs(t *testing.T) {
	o := Options{Name: "uldaman", Map: 70, Bosses: []uint32{6906, 6910}, Cluster: 12, NoteRange: 20, IgnoreFactions: map[int]bool{35: true}, Friendly: map[int]bool{122: true}}
	p, err := run(filepath.Join("testdata", "uldaman"), o)
	if err != nil {
		t.Fatal(err)
	}
	n := p.Legs[0].Notes
	if len(n) < 2 || !strings.Contains(n[0], "WARNING: boss 6906 (Baelog) has npc_flags 2 (questgiver)") || !strings.Contains(n[1], "faction 122") {
		t.Fatalf("%q", n)
	}
	for _, x := range p.Legs[1].Notes {
		if strings.Contains(x, "WARNING") {
			t.Fatalf("Revelosh is a mob: %q", p.Legs[1].Notes)
		}
	}
}

// -strict turns warnings into exit status 1; a clean plan stays 0, and without it warnings do not fail.
func TestCheckFileStrict(t *testing.T) {
	dir := t.TempDir()
	plan := filepath.Join(dir, "p.json")
	if err := os.WriteFile(plan, []byte(`{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"to":{"x":0,"y":0,"z":0},"boss":{"entry":6906,"position":{"x":1,"y":0,"z":0}}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join("testdata", "uldaman")
	if got := checkFile(plan, data, "alliance", "122", "", "", false, "35", false, false); got != 0 {
		t.Fatalf("warnings without -strict: %d", got)
	}
	if got := checkFile(plan, data, "alliance", "122", "", "", true, "35", false, false); got != 1 {
		t.Fatalf("warnings with -strict: %d", got)
	}
	clean := filepath.Join("..", "..", "dungeons", "uldaman.json")
	// The committed plan has no creature-check warning, and its dense pulls
	// (no max_bodies sized yet) warn without failing -strict; but leg 7 pull
	// 1 owns no mob in the export, and that no-target finding fails strict
	// (see TestCheckFileNoEligibleMobsFailsStrict). -pull-size-strict fails
	// on the over-ceiling rows either way.
	if got := checkFile(clean, data, "alliance", "122", "2748,4857", "", true, "35", false, false); got != 1 {
		t.Fatalf("the committed plan under -strict: %d, want 1 (its no-target row fails it)", got)
	}
	if got := checkFile(clean, data, "alliance", "122", "2748,4857", "", false, "35", true, false); got != 1 {
		t.Fatalf("the committed plan under -pull-size-strict: %d, want 1 (its over-ceiling rows fail it)", got)
	}
}

// A world the loader refuses, or a plan the exporter refuses, fails the
// check even without -strict: a lint that cannot read its inputs is an
// error, not an empty report.
func TestCheckFileFailsOnWorldOrExportErrors(t *testing.T) {
	dir := t.TempDir()
	plan := filepath.Join(dir, "p.json")
	if err := os.WriteFile(plan, []byte(`{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":1,"y":0,"z":0}]}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	// No creatures.tsv: worldspawn.Load refuses, and the check fails strict or not.
	if got := checkFile(plan, data, "", "", "", "", false, "35", false, false); got != 1 {
		t.Fatalf("missing world without strict: %d, want 1", got)
	}
	if got := checkFile(plan, data, "", "", "", "", true, "35", false, false); got != 1 {
		t.Fatalf("missing world with strict: %d, want 1", got)
	}
	// A world that loads but a plan the exporter refuses (kill_first over the
	// row column width: dungeon.Load passes, planexport.Build refuses) fails
	// the same way.
	long := "10000"
	for e := 10001; e <= 10060; e++ {
		long += fmt.Sprintf(",%d", e)
	}
	js := `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"kill_first":[` + long + `],"legs":[
 {"to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":1,"y":0,"z":0}]}]}]}`
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(js), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := checkFile(bad, filepath.Join("testdata", "uldaman"), "", "", "", "", false, "35", false, false); got != 1 {
		t.Fatalf("refused export without strict: %d, want 1", got)
	}
}

// A planlint.Creature export with no eligible mobs (header only) still reports every
// pull row as "no target", and -strict fails on it.
func TestCheckFileNoEligibleMobsFailsStrict(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join("testdata", "uldaman")
	for _, f := range []string{"entrances.tsv", "gameobjects.tsv"} {
		b, err := os.ReadFile(filepath.Join(src, f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	creatures, err := os.ReadFile(filepath.Join(src, "creatures.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	header := strings.SplitN(string(creatures), "\n", 2)[0] + "\n"
	if err := os.WriteFile(filepath.Join(dir, "creatures.tsv"), []byte(header), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(dir, "p.json")
	if err := os.WriteFile(plan, []byte(`{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":1,"y":0,"z":0}]}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := checkFile(plan, dir, "", "", "", "", false, "35", false, false); got != 0 {
		t.Fatalf("no-target warning without strict: %d, want 0 (warnings do not fail)", got)
	}
	if got := checkFile(plan, dir, "", "", "", "", true, "35", false, false); got != 1 {
		t.Fatalf("no-target finding with strict: %d, want 1", got)
	}
}

// An over-ceiling pull row warns without failing -strict: only
// -pull-size-strict opts into the ceiling. (A no-target row still fails
// strict; see TestCheckFileNoEligibleMobsFailsStrict.)
func TestCheckFilePullSizeStrictOnlyFailsOverCeiling(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join("testdata", "uldaman")
	for _, f := range []string{"entrances.tsv", "gameobjects.tsv"} {
		b, err := os.ReadFile(filepath.Join(src, f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	creatures, err := os.ReadFile(filepath.Join(src, "creatures.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	header := strings.SplitN(string(creatures), "\n", 2)[0] + "\n"
	// Two mobs of one entry 15 yd apart: the second joins through the ring
	// and the row (ceiling 1) warns, with no creature-check warning and a
	// target of its own.
	rows := header +
		"1\t1\tOne\t60\t60\t0\t14\t0\t30\t0\t0\t0\t0\t0\t0\t\n" +
		"2\t1\tTwo\t60\t60\t0\t14\t0\t45\t0\t0\t0\t0\t0\t0\t\n"
	if err := os.WriteFile(filepath.Join(dir, "creatures.tsv"), []byte(rows), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(dir, "p.json")
	if err := os.WriteFile(plan, []byte(`{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":30,"y":0,"z":0}],"max_bodies":1}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := checkFile(plan, dir, "", "", "", "", false, "35", false, false); got != 0 {
		t.Fatalf("over-ceiling warning without strict: %d, want 0 (warnings do not fail)", got)
	}
	if got := checkFile(plan, dir, "", "", "", "", true, "35", false, false); got != 0 {
		t.Fatalf("over-ceiling warning with -strict: %d, want 0 (only -pull-size-strict opts in)", got)
	}
	if got := checkFile(plan, dir, "", "", "", "", false, "35", true, false); got != 1 {
		t.Fatalf("over-ceiling warning with -pull-size-strict: %d, want 1", got)
	}
}
