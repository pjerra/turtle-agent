package main

import (
	"bytes"
	"strings"
	"testing"
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
