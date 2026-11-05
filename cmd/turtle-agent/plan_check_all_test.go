package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// routeGateReport is the canned plan-check --json report the stub bin prints:
// one baselined route error (step 3 no_path), one new one (step 5 zjump), and
// a route warning (step 7 long_hop) that never fails the gate.
const routeGateReport = `[{"plan":"gateplan","map":309,"points":8,"issues":[],"warnings":[],"exempt":[],"run_leader":[],"lints":[` +
	`{"step":3,"leg":0,"kind":"move","lint":"no_path","point":{"x":1,"y":2,"z":3},"detail":"no complete navmesh path","severity":"error"},` +
	`{"step":5,"leg":0,"kind":"move","lint":"zjump","point":{"x":4,"y":5,"z":6},"detail":"6.6 yd down","severity":"error"},` +
	`{"step":7,"leg":0,"kind":"move","lint":"long_hop","point":{"x":7,"y":8,"z":9},"detail":"67 yd straight hop","severity":"warning"}` +
	`],"lint_counts":{"no_path":1,"zjump":1,"long_hop":1}}]`

// routeGateRig writes a plan dir with one live-checked plan, a stub bin that
// prints routeGateReport (exit 11, as plan-check does with error findings),
// and a baseline holding only the step-3 no_path. It returns the dir and the
// script's runner.
func routeGateRig(t *testing.T) (dir, bin, baseline string) {
	t.Helper()
	dir = t.TempDir()
	plan := `{"version":1,"name":"gateplan","status":"live-checked","map":309,"entrance":{"x":0,"y":0,"z":0},"legs":[]}`
	if err := os.WriteFile(filepath.Join(dir, "gateplan.json"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	// The stub's canned report lives outside the plan dir: the gate checks
	// every *.json it finds there.
	assets := t.TempDir()
	report := filepath.Join(assets, "report.json")
	bin = filepath.Join(assets, "stubbin")
	stub := "#!/usr/bin/env bash\ncat " + report + "\nexit 11\n"
	if err := os.WriteFile(report, []byte(routeGateReport), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	// plan-check --json must decode: the stub's report is the gate's only input.
	var probe []map[string]any
	if err := json.Unmarshal([]byte(routeGateReport), &probe); err != nil {
		t.Fatal(err)
	}
	baseline = filepath.Join(dir, "route-baseline.txt")
	if err := os.WriteFile(baseline, []byte("gateplan 3 0 no_path\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, bin, baseline
}

func runGate(t *testing.T, dir, bin, baseline string, extra ...string) (int, string) {
	t.Helper()
	script := filepath.Join("..", "..", "tools", "ci", "plan-check-all.sh")
	args := append([]string{script, "--dir", dir, "--mmaps", dir, "--bin", bin, "--baseline", baseline}, extra...)
	cmd := exec.Command("bash", args...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if ex, ok := err.(*exec.ExitError); ok {
			code = ex.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	return code, string(out)
}

// A new error-severity route finding (not in the baseline) fails the gate;
// the baselined one and the route warning do not.
func TestPlanCheckAllRouteBaselineFailsNewErrors(t *testing.T) {
	dir, bin, baseline := routeGateRig(t)
	code, out := runGate(t, dir, bin, baseline)
	if code != 11 {
		t.Fatalf("exit %d, want 11: %s", code, out)
	}
	if !strings.Contains(out, "FAIL gateplan: 1 new route lint error(s)") {
		t.Fatalf("want the new zjump named: %s", out)
	}
	if strings.Contains(out, "no_path") && strings.Contains(out, "FAIL gateplan: 1 new route lint error(s)") {
		// The FAIL line names only the new finding; no_path stays in WARN.
		t.Fatalf("baselined no_path must not fail: %s", out)
	}
}

// After --refresh-baseline the same report passes: every route error is known.
func TestPlanCheckAllRouteRefreshBaselinePasses(t *testing.T) {
	dir, bin, baseline := routeGateRig(t)
	if code, out := runGate(t, dir, bin, baseline, "--refresh-baseline"); code != 0 {
		t.Fatalf("refresh exit %d, want 0: %s", code, out)
	}
	code, out := runGate(t, dir, bin, baseline)
	if code != 0 {
		t.Fatalf("exit %d after refresh, want 0: %s", code, out)
	}
	if !strings.Contains(out, "WARN gateplan") {
		t.Fatalf("want the route warning still reported: %s", out)
	}
}

// --strict-lints fails even the baselined route error.
func TestPlanCheckAllRouteStrictFailsBaselined(t *testing.T) {
	dir, bin, baseline := routeGateRig(t)
	if code, out := runGate(t, dir, bin, baseline, "--refresh-baseline"); code != 0 {
		t.Fatalf("refresh exit %d, want 0: %s", code, out)
	}
	code, out := runGate(t, dir, bin, baseline, "--strict-lints")
	if code != 11 {
		t.Fatalf("strict exit %d, want 11: %s", code, out)
	}
}
