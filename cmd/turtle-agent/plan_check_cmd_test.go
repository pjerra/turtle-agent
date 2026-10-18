package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/opcode"
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

// Without a planner, or without navmesh files for the map, the plan cannot be checked: dungeon_run refuses it unless force is given, which
// runs it and says so.
func TestPlanCheckFailsClosedWithoutNavmesh(t *testing.T) {
	plan, err := dungeon.Load("../../dungeons/zulfarrak.json")
	if err != nil {
		t.Fatal(err)
	}
	noFiles := navmesh.NewPlanner(t.TempDir())
	defer noFiles.Close()
	for name, r := range map[string]*dungeonRunner{
		"no planner": {dir: "../../dungeons"},
		"no map":     {dir: "../../dungeons", remote: &commanderService{planner: noFiles}},
	} {
		res, err := r.planCheck(planCheckParams{Name: "zulfarrak"})
		if err != nil || res.Checked || res.Skipped == "" || res.Issues == nil {
			t.Fatalf("%s: %v %+v", name, err, res)
		}
		if _, err := r.checkForRun(plan, dungeon.CheckOptions{}, false); err == nil || errCodeOf(err) != rpc.CodeInvalidParams || !strings.Contains(err.Error(), "plan check could not run: ") || !strings.Contains(err.Error(), "pass force to run anyway") {
			t.Errorf("%s: not refused: %v", name, err)
		}
		if note, err := r.checkForRun(plan, dungeon.CheckOptions{}, true); err != nil || !strings.HasPrefix(note, "forced past plan check could not run: ") {
			t.Errorf("%s: %q %v", name, note, err)
		}
	}
}

func errCodeOf(err error) int {
	var re *rpc.Error
	if errors.As(err, &re) {
		return re.Code
	}
	return 0
}

// The plan_check method and the command refuse a start the plan does not have.
func TestPlanCheckBadStart(t *testing.T) {
	r := &dungeonRunner{dir: "../../dungeons"}
	leg, group := legParam{index: 99}, 99
	if _, err := r.planCheck(planCheckParams{Name: "zulfarrak", FromLeg: &leg}); err == nil || errCodeOf(err) != rpc.CodeInvalidParams {
		t.Errorf("from_leg 99: %v", err)
	}
	zero := legParam{index: 0}
	if _, err := r.planCheck(planCheckParams{Name: "zulfarrak", FromLeg: &zero, FromPullGroup: &group}); err == nil || errCodeOf(err) != rpc.CodeInvalidParams || !strings.Contains(err.Error(), "pull group 99") {
		t.Errorf("from_pull_group 99: %v", err)
	}
	// With no navmesh the start is still validated (no engine is needed for it) when a planner exists.
	noFiles := navmesh.NewPlanner(t.TempDir())
	defer noFiles.Close()
	r.remote = &commanderService{planner: noFiles}
	if _, err := r.planCheck(planCheckParams{Name: "zulfarrak", FromLeg: &zero, FromPullGroup: &group}); err == nil || errCodeOf(err) != rpc.CodeInvalidParams {
		t.Errorf("from_pull_group 99 with a planner: %v", err)
	}
	var out, errOut bytes.Buffer
	for _, args := range [][]string{{"--from-leg", "99"}, {"--from-leg", "0", "--from-pull-group", "99"}} {
		code := planCheckCmd(append([]string{"--dir", "../../dungeons", "--mmaps", t.TempDir()}, append(args, "zulfarrak")...), &out, &errOut)
		if code == exitOK || !strings.Contains(errOut.String(), "bad start") {
			t.Errorf("%v: exit %d: %s", args, code, errOut.String())
		}
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
	if note, err := r.checkForRun(old, dungeon.CheckOptions{}, true); err != nil || !strings.HasPrefix(note, "forced past plan check failed, 1 of ") {
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
	}{
		{dungeonRunParams{Mode: "commander"}, true}, {dungeonRunParams{Mode: "remote"}, true},
		{dungeonRunParams{}, false}, {dungeonRunParams{Mode: "leader"}, false}, {dungeonRunParams{Mode: "group"}, false},
		{dungeonRunParams{Mode: "observe"}, false}, {dungeonRunParams{Mode: "commander", Pulltest: pt}, false},
	} {
		if got := planCheckApplies(c.p); got != c.want {
			t.Errorf("%+v: %v", c.p, got)
		}
	}
}

// stubNav is a flat navmesh for the gate tests (no mmaps needed): every point has floor and every path is straight, except the points in off.
type stubNav struct{ off map[dungeon.Point]bool }

func (s stubNav) Plan(_ uint32, from, to navmesh.Vec3) (navmesh.Path, error) {
	if s.off[dungeon.Point{X: to[0], Y: to[1], Z: to[2]}] {
		return navmesh.Path{Status: navmesh.NoPath, Reason: "goal is not on the navmesh", Points: []navmesh.Vec3{}}, nil
	}
	return navmesh.Path{Status: navmesh.Complete, Points: []navmesh.Vec3{from, to}, Length: 1}, nil
}

func (s stubNav) Raycast(uint32, navmesh.Vec3, navmesh.Vec3) (navmesh.Ray, error) {
	return navmesh.Ray{Known: true, Clear: true}, nil
}

func (s stubNav) Nearest(_ uint32, p navmesh.Vec3) (navmesh.Vec3, bool, error) {
	return p, !s.off[dungeon.Point{X: p[0], Y: p[1], Z: p[2]}], nil
}

const gatePlan = `{"version":1,"name":"%s","status":"%s","map":0,"entrance":{"x":5,"y":5,"z":5},"legs":[{"name":"a","to":{"x":%d,"y":5,"z":5},"pull_groups":[{"entries":[299],"positions":[{"x":6,"y":5,"z":5}]}]}]}`

// gateRig is a rig whose plan check walks stubNav (the point 99 5 5 is off its mesh): plans ok (live-checked), bad (live-checked, its `to`
// is off the mesh) and mappy (mapped, the same fault). Checks counts the navmeshes the check asked for.
func gateRig(t *testing.T, checks *int) *agentRig {
	t.Helper()
	dir := t.TempDir()
	for name, doc := range map[string]string{
		"ok":    fmt.Sprintf(gatePlan, "ok", "live-checked", 7),
		"bad":   fmt.Sprintf(gatePlan, "bad", "live-checked", 99),
		"mappy": fmt.Sprintf(gatePlan, "mappy", "mapped (not live-checked)", 99),
		// A point nobody has mapped yet (needs_walk) on a leg, a pull group and a boss.
		"walkleg":   `{"version":1,"name":"walkleg","status":"live-checked","map":0,"entrance":{"x":5,"y":5,"z":5},"legs":[{"name":"a","needs_walk":true,"to":{"x":7,"y":5,"z":5},"pull_groups":[{"entries":[299],"positions":[{"x":6,"y":5,"z":5}]}]}]}`,
		"walkgroup": `{"version":1,"name":"walkgroup","status":"live-checked","map":0,"entrance":{"x":5,"y":5,"z":5},"legs":[{"name":"a","to":{"x":7,"y":5,"z":5},"pull_groups":[{"entries":[299],"needs_walk":true,"positions":[{"x":6,"y":5,"z":5}]}]}]}`,
		"walkboss":  `{"version":1,"name":"walkboss","status":"live-checked","map":0,"entrance":{"x":5,"y":5,"z":5},"legs":[{"name":"a","to":{"x":7,"y":5,"z":5},"boss":{"entry":9,"needs_walk":true,"position":{"x":6,"y":5,"z":5}}}]}`,
		// An encounter of the other team: the driver looks at it before it skips the fight, so its unreachable look refuses the run.
		// Pull group 1 gathers inside pull group 0 (TestDungeonRunForceStartIsTheUnsafeStartGates).
		"crowd": `{"version":1,"name":"crowd","status":"live-checked","map":0,"entrance":{"x":5,"y":5,"z":5},"legs":[{"name":"l","to":{"x":0,"y":5,"z":5},"pull_groups":[{"entries":[1,1,1],"positions":[{"x":130,"y":5,"z":5},{"x":115,"y":5,"z":5},{"x":100,"y":5,"z":5}]},{"entries":[2],"positions":[{"x":200,"y":5,"z":5}]}]}]}`,
		"teamy": `{"version":1,"name":"teamy","status":"live-checked","map":0,"entrance":{"x":5,"y":5,"z":5},"legs":[{"name":"a","to":{"x":7,"y":5,"z":5},"pull_groups":[{"entries":[299],"team":"horde","positions":[{"x":99,"y":5,"z":5}]}]}]}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	nav := func() (dungeon.Navmesh, func(), error) {
		*checks++
		return stubNav{off: map[dungeon.Point]bool{{X: 99, Y: 5, Z: 5}: true}}, func() {}, nil
	}
	return newRigWith(t, session.Options{}, [3]float32{1, 2, 3}, methodOptions{DungeonsDir: dir, PlanCheckNav: nav, AccountProbeWindow: 50 * time.Millisecond})
}

// The gate on the commander driver, with no navmesh files needed: a live-checked plan with a failing point is refused, force runs it; a
// passing plan runs; a plan that is not live-checked runs with the findings as a warning. Leader mode, observe and a pull test are not gated.
func TestDungeonRunPlanCheckGateWithAStubNavmesh(t *testing.T) {
	var checks int
	r := gateRig(t, &checks)
	run := func(params string) map[string]any {
		out := r.call("dungeon_run", params)
		if out["error"] == nil {
			r.call("dungeon_stop", "{}")
			r.eventually("dungeon_status", "{}", func(m map[string]any) bool { return m["result"].(map[string]any)["running"] == false })
		}
		return out
	}
	noteOf := func(out map[string]any) string {
		if out["error"] != nil {
			t.Fatalf("%v", out)
		}
		s, _ := out["result"].(map[string]any)["plan_check"].(string)
		return s
	}
	const cmdr = `,"mode":"commander","lead_bot":"Tanky","preflight":"off"}`
	out := run(`{"name":"bad"` + cmdr)
	msg, _ := out["error"].(map[string]any)["message"].(string)
	if errCode(out) != rpc.CodeInvalidParams || !strings.Contains(msg, "plan check failed") || !strings.Contains(msg, "leg 0 (a) to 99.0 5.0 5.0: off the mesh") || !strings.Contains(msg, "fix the plan, or pass force") {
		t.Fatalf("not refused: %v", out)
	}
	if out := run(`{"name":"teamy"` + cmdr); !strings.Contains(fmt.Sprint(out["error"]), "[horde only") {
		t.Errorf("an other-team encounter that cannot be looked at was not refused: %v", out)
	}
	for _, name := range []string{"walkleg", "walkgroup", "walkboss"} {
		if out := run(`{"name":"` + name + `"` + cmdr); !strings.Contains(fmt.Sprint(out["error"]), "needs a mapping walk") {
			t.Errorf("%s: a needs_walk point was not refused: %v", name, out)
		}
	}
	if note := noteOf(run(`{"name":"bad","force":true` + cmdr)); !strings.HasPrefix(note, "forced past plan check failed, 1 of ") {
		t.Errorf("force: %q", note)
	}
	if note := noteOf(run(`{"name":"ok"` + cmdr)); !strings.HasPrefix(note, "passed: ") {
		t.Errorf("ok: %q", note)
	}
	if note := noteOf(run(`{"name":"mappy"` + cmdr)); !strings.HasPrefix(note, "warning (the plan is mapped, not live-checked, so it runs): plan check failed, 1 of ") {
		t.Errorf("draft: %q", note)
	}
	// Not gated: leader mode (the agent's own walk), a pull test, an observed run (whatever else they refuse).
	before := checks
	if note := noteOf(run(`{"name":"bad"}`)); note != "" {
		t.Errorf("leader mode was checked: %q", note)
	}
	for _, params := range []string{`{"name":"bad","mode":"observe","lead_bot":"Tanky"}`, `{"name":"bad","pulltest":{"leg":0,"pull_group":0},"mode":"commander","lead_bot":"Tanky"}`} {
		if out := run(params); out["error"] != nil && strings.Contains(out["error"].(map[string]any)["message"].(string), "plan check") {
			t.Errorf("%s was checked: %v", params, out)
		}
	}
	if checks != before {
		t.Errorf("%d checks for runs that are not gated", checks-before)
	}
}

// A second dungeon_run is refused for the run going, before it pays for a plan check.
func TestDungeonRunBusyBeforeThePlanCheck(t *testing.T) {
	var checks int
	r := gateRig(t, &checks)
	r.groupWith("Gorm") // a party that is not in view: the first rest gate waits for him
	if out := r.call("dungeon_run", `{"name":"ok"}`); out["error"] != nil {
		t.Fatalf("%v", out)
	}
	r.expect(opcode.CMSGMessagechat) // .gm visible on
	before := checks
	out := r.call("dungeon_run", `{"name":"bad","mode":"commander","lead_bot":"Tanky","preflight":"off"}`)
	if msg, _ := out["error"].(map[string]any)["message"].(string); errCode(out) != rpc.CodeGame || !strings.Contains(msg, "dungeon_stop it first") {
		t.Errorf("%v", out)
	}
	if checks != before {
		t.Errorf("the busy run paid for %d plan checks", checks-before)
	}
	r.call("dungeon_stop", "{}")
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
	if !strings.Contains(err.Error(), "plan check could not run: error in the check: ") || !strings.Contains(err.Error(), "pass force") {
		t.Errorf("%v", err)
	}
	if note, err := r.checkForRun(plan, dungeon.CheckOptions{}, true); err != nil || !strings.HasPrefix(note, "forced past plan check could not run: error in the check: ") {
		t.Errorf("%q %v", note, err)
	}
}

// Through dungeon_run on a rig with no planner or navmesh: a live-checked plan cannot be checked, so a commander run is refused without
// force and runs with it, saying why; a leader-mode run is not gated at all.
func TestDungeonRunWithoutNavmeshNeedsForce(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ok.json"), []byte(fmt.Sprintf(gatePlan, "ok", "live-checked", 7)), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newRigWith(t, session.Options{}, [3]float32{1, 2, 3}, methodOptions{DungeonsDir: dir, AccountProbeWindow: 50 * time.Millisecond})
	const cmdr = `"mode":"commander","lead_bot":"Tanky","preflight":"off"`
	out := r.call("dungeon_run", `{"name":"ok",`+cmdr+`}`)
	msg, _ := out["error"].(map[string]any)["message"].(string)
	if errCode(out) != rpc.CodeInvalidParams || !strings.Contains(msg, "plan check could not run: ") || !strings.Contains(msg, "mmaps_dir") || !strings.Contains(msg, "pass force") {
		t.Fatalf("%v", out)
	}
	out = r.call("dungeon_run", `{"name":"ok","force":true,`+cmdr+`}`)
	if out["error"] != nil || !strings.HasPrefix(out["result"].(map[string]any)["plan_check"].(string), "forced past plan check could not run: ") {
		t.Fatalf("%v", out)
	}
	r.call("dungeon_stop", "{}")
	r.eventually("dungeon_status", "{}", func(m map[string]any) bool { return m["result"].(map[string]any)["running"] == false })
	// Leader mode walks with the agent's own movement: no check, so no refusal for the missing navmesh.
	if out := r.call("dungeon_run", `{"name":"ok"}`); out["error"] != nil || out["result"].(map[string]any)["plan_check"] != nil {
		t.Fatalf("leader mode: %v", out)
	}
}

// A pull group number means nothing without its leg: the command and the method refuse it alone.
func TestPlanCheckFromPullGroupNeedsFromLeg(t *testing.T) {
	r := &dungeonRunner{dir: "../../dungeons"}
	group := 0
	if _, err := r.planCheck(planCheckParams{Name: "zulfarrak", FromPullGroup: &group}); err == nil || errCodeOf(err) != rpc.CodeInvalidParams || !strings.Contains(err.Error(), "needs from_leg") {
		t.Errorf("method: %v", err)
	}
	var out, errOut bytes.Buffer
	if code := planCheckCmd([]string{"--dir", "../../dungeons", "--mmaps", t.TempDir(), "--from-pull-group", "0", "zulfarrak"}, &out, &errOut); code != exitUsage || !strings.Contains(errOut.String(), "needs --from-leg") {
		t.Errorf("command: exit %d: %s", code, errOut.String())
	}
}

// The unsafe-start gate through dungeon_run's own params (no driver options set by hand): force is the plan check's and
// does not pass the gate, force_start does, and force_start does not pass the plan check. Pull group 1 of "crowd"
// gathers at 145 5 5, 15 yd from pull group 0, which is alive in the fresh instance prepare.fresh asks for.
func TestDungeonRunForceStartIsTheUnsafeStartGates(t *testing.T) {
	var checks int
	r := gateRig(t, &checks)
	const start = `"mode":"commander","lead_bot":"Tanky","preflight":"off","from_leg":0,"from_pull_group":1,"prepare":{"fresh":true}`
	const refusal = "from_pull_group 1: the gather spot 145 5 5 is 15 yd from pull group 0 of leg 0 (a safe spot needs 30), alive in a fresh instance: pass force_start to start there anyway"
	report := func(ok func(rep map[string]any) bool) map[string]any {
		out := r.eventually("dungeon_status", "{}", func(m map[string]any) bool {
			rep, _ := m["result"].(map[string]any)["report"].(map[string]any)
			return rep != nil && ok(rep)
		})
		return out["result"].(map[string]any)["report"].(map[string]any)
	}
	stop := func() {
		r.call("dungeon_stop", "{}")
		r.eventually("dungeon_status", "{}", func(m map[string]any) bool { return m["result"].(map[string]any)["running"] == false })
	}
	// force alone: the plan check passes, the gate refuses (the run fails with the refusal).
	if out := r.call("dungeon_run", `{"name":"crowd","force":true,`+start+`}`); out["error"] != nil {
		t.Fatalf("%v", out)
	}
	if rep := report(func(rep map[string]any) bool { return rep["state"] == "failed" }); rep["error"] != refusal {
		t.Fatalf("force passed the unsafe-start gate: %v", rep["error"])
	}
	stop()
	// force_start: the gate lets it through with a note.
	if out := r.call("dungeon_run", `{"name":"crowd","force_start":true,`+start+`}`); out["error"] != nil {
		t.Fatalf("%v", out)
	}
	report(func(rep map[string]any) bool {
		return strings.Contains(fmt.Sprint(rep["notes"]), "force_start: starting there anyway")
	})
	stop()
	// force_start on a plan the plan check refuses: still refused, by the plan check.
	out := r.call("dungeon_run", `{"name":"bad","force_start":true,"mode":"commander","lead_bot":"Tanky","preflight":"off"}`)
	if msg, _ := out["error"].(map[string]any)["message"].(string); errCode(out) != rpc.CodeInvalidParams || !strings.Contains(msg, "plan check failed") {
		t.Fatalf("force_start passed the plan check: %v", out)
	}
}

// Over the RPC: dungeon_run refuses force_start in leader and observe mode (commander mode takes it, see
// TestDungeonRunForceStartIsTheUnsafeStartGates).
func TestDungeonRunRefusesForceStartOutsideCommanderMode(t *testing.T) {
	var checks int
	r := gateRig(t, &checks)
	for _, params := range []string{`{"name":"ok","force_start":true}`, `{"name":"ok","mode":"leader","force_start":true}`, `{"name":"ok","mode":"observe","lead_bot":"Tanky","force_start":true}`} {
		out := r.call("dungeon_run", params)
		if msg, _ := out["error"].(map[string]any)["message"].(string); errCode(out) != rpc.CodeInvalidParams || !strings.Contains(msg, "force_start") || !strings.Contains(msg, "commander mode only") {
			t.Errorf("%s: %v", params, out)
			if out["error"] == nil {
				r.call("dungeon_stop", "{}")
			}
		}
	}
}

// run_meta (with server_trace) and force_start validated together: run_meta goes with every mode, force_start with
// commander mode only, and each refusal names its own option.
func TestDungeonRunRunMetaAndForceStartTogether(t *testing.T) {
	const meta = `"run_meta":{"model":"opus","effort":"high","run":"t193","server_trace":true}`
	for _, c := range []struct {
		p      dungeonRunParams
		remote bool
		bad    string // "" accepted
	}{
		{dungeonRunParams{Mode: "commander", LeadBot: "Tanky", ForceStart: true, RunMeta: &dungeon.RunMeta{Model: "opus", ServerTrace: true}}, true, ""},
		{dungeonRunParams{Mode: "leader", RunMeta: &dungeon.RunMeta{Model: "opus", ServerTrace: true}}, false, ""},
		{dungeonRunParams{Mode: "observe", LeadBot: "Tanky", RunMeta: &dungeon.RunMeta{Model: "opus", ServerTrace: true}}, true, ""},
		{dungeonRunParams{Mode: "leader", ForceStart: true, RunMeta: &dungeon.RunMeta{Model: "opus", ServerTrace: true}}, false, "force_start"},
		{dungeonRunParams{Mode: "observe", LeadBot: "Tanky", ForceStart: true, RunMeta: &dungeon.RunMeta{Model: "opus", ServerTrace: true}}, true, "force_start"},
	} {
		remote, err := checkMode(c.p)
		if c.bad == "" && (err != nil || remote != c.remote) || c.bad != "" && (err == nil || !strings.Contains(err.Error(), c.bad) || strings.Contains(err.Error(), "run_meta")) {
			t.Errorf("checkMode %+v: %v %v", c.p, remote, err)
		}
	}
	var checks int
	r := gateRig(t, &checks)
	// Over the RPC: force_start refused outside commander mode though run_meta is fine; a bad run_meta is refused for
	// itself in commander mode though force_start is fine; both good in commander mode starts the run.
	for _, c := range []struct{ params, want string }{
		{`{"name":"ok","mode":"leader","force_start":true,` + meta + `}`, "force_start"},
		{`{"name":"ok","mode":"observe","lead_bot":"Tanky","force_start":true,` + meta + `}`, "force_start"},
		{`{"name":"ok","mode":"commander","lead_bot":"Tanky","preflight":"off","force_start":true,"run_meta":{"server_trace":true}}`, "run_meta.server_trace needs"},
	} {
		out := r.call("dungeon_run", c.params)
		if msg, _ := out["error"].(map[string]any)["message"].(string); errCode(out) != rpc.CodeInvalidParams || !strings.Contains(msg, c.want) {
			t.Errorf("%s: %v", c.params, out)
		}
	}
	out := r.call("dungeon_run", `{"name":"ok","mode":"commander","lead_bot":"Tanky","preflight":"off","force_start":true,`+meta+`}`)
	if out["error"] != nil || out["result"].(map[string]any)["started"] != true {
		t.Fatalf("commander with run_meta and force_start: %v", out)
	}
	r.call("dungeon_stop", "{}")
	r.eventually("dungeon_status", "{}", func(m map[string]any) bool { return m["result"].(map[string]any)["running"] == false })
}
