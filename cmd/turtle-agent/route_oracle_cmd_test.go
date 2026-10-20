package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/navmesh/navfixture"
)

// The command's flags and failures, without navmesh: the oracle's own tests cover the
// report over a synthetic mesh (internal/routeoracle).
func TestRouteOracleCmdFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
		code int
		want string // a prefix of stderr
	}{
		{"no mmaps", []string{"--out", t.TempDir()}, exitUsage, routeOracleUsage},
		{"no out", []string{"--mmaps", "/nowhere"}, exitUsage, routeOracleUsage},
		{"a stray argument", []string{"--mmaps", "/nowhere", "--out", t.TempDir(), "extra"}, exitUsage, routeOracleUsage},
		{"a bad plan name", []string{"--mmaps", "/nowhere", "--out", t.TempDir(), "--plan", "Wailing"}, exitUsage, "route-oracle: \"Wailing\" is not a plan name"},
		{"a missing plan file", []string{"--mmaps", "/nowhere", "--out", t.TempDir(), "--plan", "nowhere"}, exitRouteOracle, "route-oracle: "},
	}
	for _, c := range cases {
		var stdout, stderr bytes.Buffer
		if got := routeOracleCmd(c.args, &stdout, &stderr); got != c.code {
			t.Errorf("%s: exit %d, want %d (stderr %q)", c.name, got, c.code, stderr.String())
		}
		if c.want != "" && !strings.HasPrefix(stderr.String(), c.want) {
			t.Errorf("%s: stderr %q, want it to start with %q", c.name, stderr.String(), c.want)
		}
	}
}

// The plan JSON's own name never reaches a path: a plan file whose name is a traversal
// ("../escape") is refused, and nothing is written outside --out. (With the check removed,
// the report would be written to the parent of --out as escape.json, and this test's
// look-outside assertion would fail.)
func TestRouteOracleRefusesATraversalName(t *testing.T) {
	dungeons := t.TempDir()
	outParent := t.TempDir()
	out := filepath.Join(outParent, "out")
	plan := `{"version":1,"name":"../escape","map":0,"entrance":{"x":12,"y":12,"z":0},"legs":[
	 {"name":"hall","to":{"x":46,"y":12,"z":0},"boss":{"entry":1,"position":{"x":48,"y":48,"z":0}}}]}`
	if err := os.WriteFile(filepath.Join(dungeons, "escape.json"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := routeOracleCmd([]string{"--plans", dungeons, "--mmaps", "/nowhere", "--out", out, "--plan", "escape"}, &stdout, &stderr)
	if code != exitRouteOracle {
		t.Fatalf("exit %d, want %d (stderr %q)", code, exitRouteOracle, stderr.String())
	}
	if !strings.Contains(stderr.String(), "not a plan name") {
		t.Errorf("stderr %q, want the name refusal", stderr.String())
	}
	// Nothing outside --out: no report in the parent (the traversal target) and no --out made.
	if _, err := os.Stat(filepath.Join(outParent, "escape.json")); err == nil {
		t.Fatal("the report escaped to the parent of --out")
	}
	if _, err := os.Stat(out); err == nil {
		if es, err := os.ReadDir(out); err == nil && len(es) > 0 {
			t.Errorf("--out holds files for a refused plan: %v", es)
		}
	}
}

// A plan file whose name differs from the file it is read from is refused: the report's
// name is the requested one, so the pairing must hold.
func TestRouteOracleRefusesANameMismatch(t *testing.T) {
	dungeons := t.TempDir()
	plan := `{"version":1,"name":"corridor","map":0,"entrance":{"x":12,"y":12,"z":0},"legs":[
	 {"name":"hall","to":{"x":46,"y":12,"z":0},"boss":{"entry":1,"position":{"x":48,"y":48,"z":0}}}]}`
	if err := os.WriteFile(filepath.Join(dungeons, "lie.json"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := routeOracleCmd([]string{"--mmaps", "/nowhere", "--out", t.TempDir(), "--plans", dungeons, "--plan", "lie"}, &stdout, &stderr); code != exitRouteOracle {
		t.Fatalf("exit %d (stderr %q)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), `says name "corridor", want "lie"`) {
		t.Errorf("stderr %q", stderr.String())
	}
}

// A run over a fixture mesh end to end, with --plans: the two files land under the
// requested name. (cgo: the fixture mesh is planned through the vendored Detour.)
func TestRouteOracleWritesTheFixtureReports(t *testing.T) {
	dungeons := t.TempDir()
	out := t.TempDir()
	plan := `{"version":1,"name":"corridor","map":0,"entrance":{"x":12,"y":12,"z":0},"legs":[
	 {"name":"hall","to":{"x":46,"y":12,"z":0},"pull_groups":[{"entries":[100],"positions":[{"x":44,"y":12,"z":0}]}]}]}`
	if err := os.WriteFile(filepath.Join(dungeons, "corridor.json"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	mmaps := navfixture.WriteDir(t, 0, navfixture.LCorridor(0))
	var stdout, stderr bytes.Buffer
	if code := routeOracleCmd([]string{"--plans", dungeons, "--mmaps", mmaps, "--out", out, "--plan", "corridor"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit %d (stderr %q)", code, stderr.String())
	}
	for _, f := range []string{"corridor.json", "corridor.txt"} {
		b, err := os.ReadFile(filepath.Join(out, f))
		if err != nil || len(b) < 50 {
			t.Errorf("%s: err %v len %d", f, err, len(b))
		}
	}
}
