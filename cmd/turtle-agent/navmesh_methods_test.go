//go:build cgo

package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

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

// --reconnect keeps the navmesh: the reconnected session gets the same planner, so
// path_to still plans (here: the start is off the fixture mesh) instead of answering that
// mmaps_dir is not set.
func TestReconnectKeepsTheNavmesh(t *testing.T) {
	setReconnectTimings(t, 50*time.Millisecond, 300*time.Second)
	tk := reconnectTicket()
	authScript(t, tk)
	cfg, done := worldSessions(t, tk, dropAfter(200*time.Millisecond), logoutClean)
	raw, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dir := navfixture.WriteDir(t, 0, navfixture.LCorridor(0))
	if err := os.WriteFile(cfg, []byte(strings.Replace(string(raw), `"rpc":`, `"mmaps_dir":"`+dir+`","rpc":`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr syncBuffer
	codeC := make(chan int, 1)
	go func() {
		codeC <- run(context.Background(), []string{"login", "--config", cfg, "--reconnect", "2", "--stay", "2s", "harness", "Harnessb"}, &bytes.Buffer{}, &stderr)
	}()
	addr := rpcAddr(t, &stderr)
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(stderr.String(), "reconnect: in world again"); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("no reconnect: %q", stderr.String())
		}
	}
	out := rpcCall(t, addr, "path_to", `{"x":48,"y":48,"z":0}`)
	res, ok := out["result"].(map[string]any)
	if !ok || res["status"] != "no_path" || !strings.Contains(res["reason"].(string), "start") {
		t.Fatalf("path_to after the reconnect: %v", out)
	}
	if code := <-codeC; code != exitOK {
		t.Fatalf("code %d, stderr %q", code, stderr.String())
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSessionOptionsCarryThePlanner(t *testing.T) {
	p := navmesh.NewPlanner(t.TempDir())
	if o := sessionOptions("Harnessb", nil, nil, []string{"ACC"}, "pw", p, nil); o.Planner != p || o.RedactAnywhere[0] != "pw" {
		t.Fatalf("%+v", o)
	}
	if o := sessionOptions("Harnessb", nil, nil, nil, "pw", nil, nil); o.Planner != nil {
		t.Fatal("a nil planner must stay a nil interface")
	}
}
