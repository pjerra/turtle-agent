//go:build cgo

package main

import (
	"testing"

	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/navmesh/navfixture"
	"github.com/pjerra/turtle-agent/internal/session"
)

// path_to and walk_path over JSON-RPC on synthetic tiles: the L-shaped corridor on map 0,
// entered at its start (12, 12, 0).
func TestPathToAndWalkPathMethods(t *testing.T) {
	planner := navmesh.NewPlanner(navfixture.WriteDir(t, 0, navfixture.LCorridor(0)))
	t.Cleanup(planner.Close)
	r := newRigAt(t, session.Options{Planner: planner}, [3]float32{12, 12, 0})

	out := r.call("path_to", `{"x":48,"y":48,"z":0}`)
	res, ok := out["result"].(map[string]any)
	if !ok || res["status"] != "complete" || res["engine"] != "detour" || res["map"] != float64(0) {
		t.Fatalf("path_to %v", out)
	}
	pts := res["points"].([]any)
	if len(pts) != 3 || pts[1].([]any)[0] != float64(46) || pts[1].([]any)[1] != float64(14) {
		t.Fatalf("points %v", pts)
	}
	if out := r.call("path_to", `{"x":200,"y":200,"z":0}`); out["result"].(map[string]any)["status"] != "no_path" {
		t.Fatalf("off the mesh %v", out)
	}

	out = r.call("walk_path", `{"x":19,"y":12,"z":0}`) // 7 yd, one second at the default speed
	res, ok = out["result"].(map[string]any)
	if !ok || res["result"] != "arrived" || res["dist_left"].(float64) > 0.01 || len(res["trace"].([]any)) != 2 {
		t.Fatalf("walk_path %v", out)
	}
	if st := r.call("status", "{}")["result"].(map[string]any); st["x"] != float64(19) || st["y"] != float64(12) {
		t.Fatalf("status %v", st)
	}
}
