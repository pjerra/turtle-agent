package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/session"
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
	if note, err := r.checkForRun(plan, dungeon.CheckOptions{}, false); err != nil || !strings.HasPrefix(note, "not run: ") {
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
	if _, err := r.checkForRun(old, dungeon.CheckOptions{}, false); err == nil || !strings.Contains(err.Error(), "leg 2 (antusul_a3) direct via 1") || !strings.Contains(err.Error(), "pass force") {
		t.Errorf("not refused: %v", err)
	}
	if note, err := r.checkForRun(old, dungeon.CheckOptions{}, true); err != nil || !strings.HasPrefix(note, "forced past 1 of ") {
		t.Errorf("%q %v", note, err)
	}
	// From leg 3 on the failing hop is behind the run.
	if note, err := r.checkForRun(old, dungeon.CheckOptions{FromLeg: 3}, false); err != nil || !strings.HasPrefix(note, "passed") {
		t.Errorf("%q %v", note, err)
	}
	cur, err := dungeon.Load("../../dungeons/zulfarrak.json")
	if err != nil {
		t.Fatal(err)
	}
	if note, err := r.checkForRun(cur, dungeon.CheckOptions{}, false); err != nil || !strings.HasPrefix(note, "passed") {
		t.Errorf("%q %v", note, err)
	}
}

func TestPlanCheckApplies(t *testing.T) {
	pt := &pulltestParam{}
	for _, c := range []struct {
		p    dungeonRunParams
		want bool
	}{{dungeonRunParams{}, true}, {dungeonRunParams{Mode: "commander"}, true}, {dungeonRunParams{Mode: "observe"}, false}, {dungeonRunParams{Pulltest: pt}, false}} {
		if got := planCheckApplies(c.p); got != c.want {
			t.Errorf("%+v: %v", c.p, got)
		}
	}
}

// The gate through dungeon_run: a failing plan is refused, force runs it and says so in plan_check, a passing plan runs with "passed", and
// a pull test or an observed run is not checked.
func TestDungeonRunPlanCheckGate(t *testing.T) {
	dir := zfMmapsDir()
	if dir == "" {
		t.Skip("no map 209 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 209.mmap and the 209*.mmtile tiles")
	}
	plans := t.TempDir()
	for name, src := range map[string]string{"zfold": "../../internal/dungeon/testdata/zulfarrak-c86c1e62ee5a.json", "zf": "../../dungeons/zulfarrak.json"} {
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(plans, name+".json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pl := navmesh.NewPlanner(dir)
	defer pl.Close()
	r := newRigWith(t, session.Options{}, [3]float32{1, 2, 3}, methodOptions{DungeonsDir: plans, Planner: pl, AccountProbeWindow: 50 * time.Millisecond})
	msgOf := func(out map[string]any) string {
		m, _ := out["error"].(map[string]any)["message"].(string)
		return m
	}
	// Refused, with the failing hop named and the way out.
	out := r.call("dungeon_run", `{"name":"zfold"}`)
	if errCode(out) != rpc.CodeInvalidParams || !strings.Contains(msgOf(out), "plan check failed") || !strings.Contains(msgOf(out), "leg 2 (antusul_a3) direct via 1") || !strings.Contains(msgOf(out), "pass force") {
		t.Fatalf("not refused: %v", out)
	}
	if st := r.call("dungeon_status", "{}")["result"].(map[string]any); st["state"] != "idle" {
		t.Fatalf("a refused plan started a run: %v", st)
	}
	endRun := func() {
		t.Helper()
		r.call("dungeon_stop", "{}")
		r.eventually("dungeon_status", "{}", func(m map[string]any) bool { return m["result"].(map[string]any)["running"] == false })
	}
	// force runs it.
	out = r.call("dungeon_run", `{"name":"zfold","force":true}`)
	if out["error"] != nil || !strings.HasPrefix(out["result"].(map[string]any)["plan_check"].(string), "forced past 1 of ") {
		t.Fatalf("force: %v", out)
	}
	endRun()
	// The shipped plan passes.
	out = r.call("dungeon_run", `{"name":"zf"}`)
	if out["error"] != nil || !strings.HasPrefix(out["result"].(map[string]any)["plan_check"].(string), "passed: ") {
		t.Fatalf("passing plan: %v", out)
	}
	endRun()
	// from_leg 3 is past the failing hop; a pull test and an observed run are not checked (whatever else refuses them).
	out = r.call("dungeon_run", `{"name":"zfold","from_leg":3}`)
	if out["error"] != nil || !strings.HasPrefix(out["result"].(map[string]any)["plan_check"].(string), "passed: ") {
		t.Fatalf("from_leg 3: %v", out)
	}
	endRun()
	for _, params := range []string{`{"name":"zfold","mode":"observe"}`, `{"name":"zfold","mode":"commander","pulltest":{"leg":0,"pull_group":0}}`} {
		if out := r.call("dungeon_run", params); out["error"] != nil && strings.Contains(msgOf(out), "plan check") {
			t.Errorf("%s was checked: %v", params, out)
		}
		endRun()
	}
}

// An error inside the check (a map file Detour refuses) stops the run unless force is given, which runs it and notes the error.
func TestDungeonRunForcePassesACheckError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "209.mmap"), []byte("not a Detour params block"), 0o600); err != nil {
		t.Fatal(err)
	}
	pl := navmesh.NewPlanner(dir)
	defer pl.Close()
	r := &dungeonRunner{remote: &commanderService{planner: pl}}
	plan, err := dungeon.Load("../../dungeons/zulfarrak.json")
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.checkForRun(plan, dungeon.CheckOptions{}, false)
	if err == nil {
		t.Skip("built without cgo: the check does not run")
	}
	if !strings.Contains(err.Error(), "plan check: ") || !strings.Contains(err.Error(), "pass force") {
		t.Errorf("%v", err)
	}
	if note, err := r.checkForRun(plan, dungeon.CheckOptions{}, true); err != nil || !strings.HasPrefix(note, "forced past check error: ") {
		t.Errorf("%q %v", note, err)
	}
}
