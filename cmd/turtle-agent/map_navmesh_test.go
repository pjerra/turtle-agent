//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/navmesh/navfixture"
	"github.com/pjerra/turtle-agent/internal/session"
)

// A hall of 60 x 60 yd with a gallery over its middle, on map 3.
func hallMmaps(t *testing.T) string {
	t.Helper()
	return navfixture.WriteDir(t, 3, []navfixture.Poly{navfixture.Square(0, 0, 60, 60, 0), navfixture.Square(20, 20, 40, 40, 30)})
}

func TestMapCmdSplitsFloorsAndDrawsTheNavmesh(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	var bw strings.Builder
	for i := 0; i < 30; i++ {
		z := 0.0
		if i >= 15 {
			z = 30
		}
		line, _ := json.Marshal(map[string]any{"t": float64(base.Add(time.Duration(i)*5*time.Second).UnixNano()) / 1e9,
			"bots": []map[string]any{{"name": "Cod", "map": 3, "x": 10 + float64(i), "y": 25 + float64(i)/3, "z": z}}})
		bw.Write(line)
		bw.WriteByte('\n')
	}
	bwPath := filepath.Join(dir, "bw.jsonl")
	os.WriteFile(bwPath, []byte(bw.String()), 0o600)
	out := filepath.Join(dir, "hall.svg")
	var stdout, stderr strings.Builder
	code := run(t.Context(), []string{"map", "--botwatch", bwPath, "--mmaps", hallMmaps(t), "--out", out, "--html", "--pad", "5"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	for _, name := range []string{"hall-f1.svg", "hall-f2.svg", "hall.html"} {
		if !strings.Contains(stdout.String(), name) {
			t.Errorf("%s not written: %s", name, stdout.String())
		}
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("the unsplit name was written for a map of two floors")
	}
	lower := xmlOK(t, filepath.Join(dir, "hall-f1.svg"))
	upper := xmlOK(t, filepath.Join(dir, "hall-f2.svg"))
	for name, svg := range map[string]string{"lower": lower, "upper": upper} {
		if !strings.Contains(svg, `class="terrain ground"`) || !strings.Contains(svg, "walkable ground") {
			t.Errorf("%s: no navmesh background", name)
		}
	}
	if !strings.Contains(lower, "floor 1") || !strings.Contains(upper, "floor 2") || strings.Count(lower, `<path d="M`) == strings.Count(upper, `<path d="M`) && lower == upper {
		t.Error("the floors are not told apart")
	}
	// --floors off keeps one image; explicit heights split where asked.
	if code := run(t.Context(), []string{"map", "--botwatch", bwPath, "--mmaps", hallMmaps(t), "--out", filepath.Join(dir, "one.svg"), "--floors", "off"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("floors off: %d %s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "one.svg")); err != nil {
		t.Error("--floors off did not write one image")
	}
	stdout.Reset()
	if code := run(t.Context(), []string{"map", "--botwatch", bwPath, "--mmaps", hallMmaps(t), "--out", filepath.Join(dir, "cut.svg"), "--floors", "5,20"}, &stdout, &stderr); code != exitOK || !strings.Contains(stdout.String(), "cut-f3.svg") {
		t.Fatalf("explicit cuts: %d %s %s", code, stdout.String(), stderr.String())
	}
	// A map with no tile there still draws, without a background, and says why.
	var errs strings.Builder
	if code := run(t.Context(), []string{"map", "--botwatch", bwPath, "--mmaps", hallMmaps(t), "--map", "4", "--out", filepath.Join(dir, "x.svg")}, &stdout, &errs); code != exitUsage {
		t.Errorf("map 4 has no positions: %d", code)
	}
}

func TestMapSnapshotUsesTheLivePlannerAndSplitsFloors(t *testing.T) {
	planner := navmesh.NewPlanner(navfixture.WriteDir(t, 0, []navfixture.Poly{navfixture.Square(0, 0, 60, 60, 0), navfixture.Square(20, 20, 40, 40, 30)}))
	t.Cleanup(planner.Close)
	r := newRigWith(t, session.Options{Planner: planner}, [3]float32{10, 10, 0}, methodOptions{Planner: planner})
	res := r.call("map_snapshot", `{"pad":10}`)["result"].(map[string]any)
	svg := res["svg"].(string)
	if res["background"] != true || !strings.Contains(svg, `class="terrain ground"`) || len(res["floors"].([]any)) != 2 || !strings.Contains(svg, "floor 1") {
		t.Fatalf("%v", res)
	}
	up := r.call("map_snapshot", `{"pad":10,"floor":1}`)["result"].(map[string]any)
	if !strings.Contains(up["svg"].(string), "floor 2") || up["floor"] != float64(1) {
		t.Fatalf("%v", up["floors"])
	}
	if off := r.call("map_snapshot", `{"pad":10,"background":false}`)["result"].(map[string]any); off["background"] != false || len(off["floors"].([]any)) != 1 {
		t.Fatalf("%v", off)
	}
}
