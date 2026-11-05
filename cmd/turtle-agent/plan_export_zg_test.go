package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// zgW38MMaps is the folder with the server's map-309 mmaps: TURTLE_AGENT_MMAPS, else ~/turtle-agent-mmaps. A
// machine without them skips the test (the cut needs the navmesh).
func zgW38MMaps(t *testing.T) string {
	t.Helper()
	home, _ := os.UserHomeDir()
	for _, dir := range []string{os.Getenv("TURTLE_AGENT_MMAPS"), filepath.Join(home, "turtle-agent-mmaps")} {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "309.mmap")); err != nil {
			continue
		}
		if tiles, _ := filepath.Glob(filepath.Join(dir, "309*.mmtile")); len(tiles) == 0 {
			continue
		}
		return dir
	}
	t.Skip("needs the map-309 mmaps (TURTLE_AGENT_MMAPS or ~/turtle-agent-mmaps)")
	return ""
}

// TestPlanExportZulGurubHakkarOuterRowsWalkable is the walk-38 review's M4, on the REAL export: it runs this
// package's plan-export (the cutter that writes the shipped migration) on the shipped Zul'Gurub plan with the
// live flags, then checks the hakkar_outer leg's rows. Walk 38 f2 paused at step 601 "no walkable path toward
// -11752.0 -1741.9 14.3": the approach cut had put a point 4.1 yd under the floor on the straight line between
// the group-5 and group-6 pulls, and the run leader could not path to it. On dev (before the cutter keeps a
// single point as the last approach row) the export carries that row, so this fails there.
func TestPlanExportZulGurubHakkarOuterRowsWalkable(t *testing.T) {
	mmaps := zgW38MMaps(t)
	dir := t.TempDir()
	code, _, errs := runExport("--dir", "../../dungeons", "--mmaps", mmaps, "--approach-hops", "--floor-aware",
		"--gm-cross-direct", "--stamp", "20261105190000", "--out", dir, "zul_gurub")
	if code != exitOK {
		t.Fatalf("plan-export exited %d: %s", code, errs)
	}
	data, err := os.ReadFile(filepath.Join(dir, "20261105190000_world.sql"))
	if err != nil {
		t.Fatal(err)
	}
	plans, err := planexport.Parse(string(data))
	if err != nil || len(plans) != 1 {
		t.Fatalf("parse: %v (%d plans)", err, len(plans))
	}
	leg := -1
	for _, l := range plans[0].Legs {
		if l.Name == "hakkar_outer" {
			leg = int(l.Leg)
		}
	}
	if leg < 0 {
		t.Fatal("no hakkar_outer leg in the export")
	}
	var steps []planexport.StepRow
	for _, s := range plans[0].Steps {
		if int(s.Leg) == leg {
			steps = append(steps, s)
		}
	}
	if len(steps) < 10 {
		t.Fatalf("%d hakkar_outer rows, want the leg's row set", len(steps))
	}
	pl := navmesh.NewPlanner(mmaps)
	defer pl.Close()
	for i, s := range steps {
		// the bad row: 4.1 yd under the floor on the group-5 -> group-6 line
		if dx, dy := float64(s.X+11752.0), float64(s.Y+1741.9); dx*dx+dy*dy < 9 && s.Z < 10.5 {
			t.Errorf("hakkar_outer row %d (%s) is the walk-38 step-601 point %.1f %.1f %.1f, under the floor: the run leader cannot path to it", s.Seq, s.Comment, s.X, s.Y, s.Z)
		}
		if i == 0 {
			continue
		}
		a, b := steps[i-1], s
		path, err := pl.Plan(309, navmesh.Vec3{a.X, a.Y, a.Z}, navmesh.Vec3{b.X, b.Y, b.Z})
		if err != nil {
			t.Errorf("hakkar_outer rows %d -> %d: %v", a.Seq, b.Seq, err)
			continue
		}
		if path.Status != navmesh.Complete {
			t.Errorf("hakkar_outer rows %d (%.1f %.1f %.1f) -> %d (%.1f %.1f %.1f): no complete navmesh path (%s %s)",
				a.Seq, a.X, a.Y, a.Z, b.Seq, b.X, b.Y, b.Z, path.Status, path.Reason)
		}
	}
}
