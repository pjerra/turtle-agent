package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
)

func TestPlanCheckCmdUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"zulfarrak"}, {"--mmaps", "x"}, {"--mmaps", "x", "Bad Name"}} {
		var out, errOut bytes.Buffer
		if code := planCheckCmd(args, &out, &errOut); code != exitUsage {
			t.Errorf("%v: exit %d (%s)", args, code, errOut.String())
		}
	}
}

// Without a planner the plan check does not run and the run goes ahead, with the reason in the result.
func TestPlanCheckWithoutNavmesh(t *testing.T) {
	r := &dungeonRunner{dir: "../../dungeons"}
	res, err := r.planCheck(planCheckParams{Name: "zulfarrak"})
	if err != nil || res.Checked || !strings.Contains(res.Skipped, "mmaps_dir") || res.Issues == nil {
		t.Fatalf("%v %+v", err, res)
	}
	plan, err := dungeon.Load("../../dungeons/zulfarrak.json")
	if err != nil {
		t.Fatal(err)
	}
	if note, err := r.checkForRun(plan, 0, false); err != nil || !strings.HasPrefix(note, "not run: ") {
		t.Errorf("%q %v", note, err)
	}
}

func zfMmapsDir() string {
	home, _ := os.UserHomeDir()
	for _, d := range []string{os.Getenv("TURTLE_AGENT_MMAPS"), filepath.Join(home, "tb-ci", "mmaps-turtle209")} {
		if d == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(d, "209.mmap")); err == nil {
			return d
		}
	}
	return ""
}

// dungeon_run refuses a plan with a failing point unless force is set (the old Zul'Farrak plan fails at leg 2's first hop), and takes the
// shipped one.
func TestDungeonRunRefusesFailingPlan(t *testing.T) {
	dir := zfMmapsDir()
	if dir == "" {
		t.Skip("no map 209 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 209.mmap and the 209*.mmtile tiles")
	}
	pl := navmesh.NewPlanner(dir)
	defer pl.Close()
	r := &dungeonRunner{remote: &commanderService{planner: pl}}
	old, err := dungeon.Load("../../internal/dungeon/testdata/zulfarrak-c86c1e62ee5a.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.checkForRun(old, 0, false); err == nil || !strings.Contains(err.Error(), "leg 2 (antusul_a3) direct via 1") || !strings.Contains(err.Error(), "pass force") {
		t.Errorf("not refused: %v", err)
	}
	if note, err := r.checkForRun(old, 0, true); err != nil || !strings.HasPrefix(note, "forced past 1 of ") {
		t.Errorf("%q %v", note, err)
	}
	// From leg 3 on the failing hop is behind the run.
	if note, err := r.checkForRun(old, 3, false); err != nil || !strings.HasPrefix(note, "passed") {
		t.Errorf("%q %v", note, err)
	}
	cur, err := dungeon.Load("../../dungeons/zulfarrak.json")
	if err != nil {
		t.Fatal(err)
	}
	if note, err := r.checkForRun(cur, 0, false); err != nil || !strings.HasPrefix(note, "passed") {
		t.Errorf("%q %v", note, err)
	}
}
