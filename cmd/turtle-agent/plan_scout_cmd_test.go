package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// scoutNet is a flat, complete navmesh for the plan-scout command's tests.
type scoutNet struct{}

func (scoutNet) Floors(uint32, float32, float32, float32, float32) ([]navmesh.Floor, error) {
	return nil, nil
}
func (scoutNet) Nearest(_ uint32, p navmesh.Vec3) (navmesh.Vec3, bool, error) { return p, true, nil }
func (scoutNet) Raycast(uint32, navmesh.Vec3, navmesh.Vec3) (navmesh.Ray, error) {
	return navmesh.Ray{Known: true, Clear: true}, nil
}
func (scoutNet) ServerPath(_ uint32, from, to navmesh.Vec3) (navmesh.ServerPath, error) {
	return navmesh.ServerPath{Type: navmesh.PathNormal, Points: []navmesh.Vec3{from, to}, ActualEnd: to,
		Length: float32(math.Hypot(float64(to[0]-from[0]), float64(to[1]-from[1])))}, nil
}
func (scoutNet) Plan(_ uint32, from, to navmesh.Vec3) (navmesh.Path, error) {
	return navmesh.Path{Status: navmesh.Complete, Points: []navmesh.Vec3{from, to}}, nil
}
func (scoutNet) Close() {}

// useScoutNet injects the fake navmesh for one test.
func useScoutNet(t *testing.T) {
	t.Helper()
	old := planScoutOpen
	planScoutOpen = func(string) planScoutNav { return scoutNet{} }
	t.Cleanup(func() { planScoutOpen = old })
}

// writePlanFile writes a plan the scout can check: a via, a to, a pull group with an approach and a boss-free route.
func writePlanFile(t *testing.T, dir, name string, to float32) string {
	t.Helper()
	p := &dungeon.Plan{Version: 1, Name: name, Map: 1, Entrance: dungeon.Point{}, Legs: []dungeon.Leg{{
		Name: "one",
		To:   dungeon.Point{X: to, Y: 0, Z: 0},
		PullGroups: []dungeon.PullGroup{{
			Entries: []uint32{100}, Positions: []dungeon.Point{{X: to + 20, Y: 0, Z: 0}},
			Approach: []dungeon.Point{{X: to + 10, Y: 0, Z: 0}},
		}},
	}}}
	b, err := dungeon.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeMigration writes a migration with one plan of the given steps.
func writeMigration(t *testing.T, dir string, steps ...planexport.StepRow) string {
	t.Helper()
	for i := range steps {
		steps[i].Plan, steps[i].Leg, steps[i].Seq = "scout_m", 0, i
	}
	rows := planexport.Rows{Plan: planexport.PlanRow{Plan: "scout_m", Map: 1},
		Legs: []planexport.LegRow{{Plan: "scout_m", Leg: 0, Name: "one"}}, Steps: steps}
	path := filepath.Join(dir, "migration.sql")
	if err := os.WriteFile(path, []byte(planexport.WriteSQL([]planexport.Rows{rows})), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPlanScoutUsageAndExitCodes(t *testing.T) {
	useScoutNet(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "out")

	for _, args := range [][]string{
		{"plan-scout"},
		{"plan-scout", "Bad Name"},
		{"plan-scout", "missing_plan", "--dir", dir},
	} {
		if code, _, _ := runCLI(args...); code != exitUsage {
			t.Fatalf("%v: code %d, want %d", args, code, exitUsage)
		}
	}
	if code, _, errOut := runCLI("plan-scout", "x", "--live", "tortoise-test2"); code != exitLivePhaseRefused || !strings.Contains(errOut, "not in this build") {
		t.Fatalf("--live: code %d, %q", code, errOut)
	}
	if code, _, _ := runCLI("plan-scout", "x", "--slot", "3"); code != exitSlotRefused {
		t.Fatalf("--slot 3: code %d", code)
	}

	// A passing plan: 0.
	writePlanFile(t, dir, "scout_pass", 40)
	if code, _, errOut := runCLI("plan-scout", "scout_pass", "--dir", dir, "--out", out, "--mmaps", dir); code != exitOK {
		t.Fatalf("passing plan: code %d, %q", code, errOut)
	}
	// A 150 yd hop in a migration: 27.
	sql := writeMigration(t, dir, planexport.StepRow{Kind: planexport.KindMove, X: 1}, planexport.StepRow{Kind: planexport.KindMove, X: 150})
	if code, outText, _ := runCLI("plan-scout", "scout_m", "--sql", sql, "--dir", dir, "--out", out, "--mmaps", dir); code != exitPlanScoutFail || !strings.Contains(outText, "hop_len") {
		t.Fatalf("150 yd hop: code %d out %q", code, outText)
	}
	// A 121 yd hop is a warning: 0 by default, 27 with --fail-on warn.
	sql = writeMigration(t, dir, planexport.StepRow{Kind: planexport.KindMove, X: 1}, planexport.StepRow{Kind: planexport.KindMove, X: 122})
	if code, _, _ := runCLI("plan-scout", "scout_m", "--sql", sql, "--dir", dir, "--out", out, "--mmaps", dir); code != exitOK {
		t.Fatalf("121 yd hop: code %d", code)
	}
	if code, _, _ := runCLI("plan-scout", "scout_m", "--sql", sql, "--dir", dir, "--out", out, "--mmaps", dir, "--fail-on", "warn"); code != exitPlanScoutFail {
		t.Fatalf("121 yd hop --fail-on warn: code %d", code)
	}
}

func TestPlanScoutNoNavmesh(t *testing.T) {
	useScoutNet(t)
	planScoutOpen = func(string) planScoutNav { return navmesh.NewPlanner("") }
	dir := t.TempDir()
	writePlanFile(t, dir, "scout_pass", 40)
	code, _, errOut := runCLI("plan-scout", "scout_pass", "--dir", dir, "--out", filepath.Join(dir, "out"), "--mmaps", dir)
	if code != exitPlanScoutNoNavmesh || !strings.Contains(errOut, "navmesh") {
		t.Fatalf("code %d, %q", code, errOut)
	}
}

func TestPlanScoutFromUntil(t *testing.T) {
	useScoutNet(t)
	dir := t.TempDir()
	// Two legs: the second walks 10 yd.
	p := &dungeon.Plan{Version: 1, Name: "scout_two", Map: 1, Entrance: dungeon.Point{}, Legs: []dungeon.Leg{
		{Name: "one", To: dungeon.Point{X: 40}, PullGroups: []dungeon.PullGroup{{Entries: []uint32{1}, Positions: []dungeon.Point{{X: 60}}}}},
		{Name: "two", To: dungeon.Point{X: 50}, PullGroups: []dungeon.PullGroup{{Entries: []uint32{1}, Positions: []dungeon.Point{{X: 70}}}}},
	}}
	b, err := dungeon.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scout_two.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	if code, _, errOut := runCLI("plan-scout", "scout_two", "--dir", dir, "--out", out, "--mmaps", dir, "--from", "one", "--until", "one"); code != exitOK {
		t.Fatalf("code %d, %q", code, errOut)
	}
	b, err = os.ReadFile(filepath.Join(out, "scout-scout_two.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Steps []struct {
			Leg int `json:"leg"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Steps) == 0 {
		t.Fatal("no steps")
	}
	for _, s := range rep.Steps {
		if s.Leg != 0 {
			t.Fatalf("leg %d in a leg-0 report", s.Leg)
		}
	}
}

func TestPlanScoutApplyWritesCopyOnly(t *testing.T) {
	useScoutNet(t)
	dir := t.TempDir()
	src := writePlanFile(t, dir, "scout_pass", 40)
	before, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	applyTo := filepath.Join(dir, "patched")
	if code, _, errOut := runCLI("plan-scout", "scout_pass", "--dir", dir, "--out", out, "--mmaps", dir, "--apply-fixes", applyTo); code != exitOK {
		t.Fatalf("code %d, %q", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(applyTo, "scout_pass.json")); err != nil {
		t.Fatalf("no copy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(applyTo, "scout_pass.diff")); err != nil {
		t.Fatalf("no diff: %v", err)
	}
	after, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("the plan file was written in place")
	}
	// Without --apply-fixes no patched copy is written anywhere.
	if _, err := os.Stat(filepath.Join(out, "scout_pass.json")); err == nil {
		t.Fatal("a patched copy was written without --apply-fixes")
	}
	// A DIR under a dungeons/ folder is refused.
	bad := filepath.Join(dir, "dungeons", "patched")
	if code, _, _ := runCLI("plan-scout", "scout_pass", "--dir", dir, "--out", out, "--mmaps", dir, "--apply-fixes", bad); code != exitUsage {
		t.Fatalf("a dungeons/ DIR: code %d", code)
	}
}
