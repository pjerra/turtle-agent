package main

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

// The steps the server refused on Ragefire Chasm (map 389, rfc-live-1d) were 3.1 to 5.5 yd under the mesh floor; the floor the agent's
// planner lists under them is where the walk now puts the step, and the planner accepts that point. Needs the map 389 tiles
// (TURTLE_AGENT_MMAPS or ~/tb-ci/mmaps-turtle389).
func TestRagefireRefusedStepsSnapToTheFloor(t *testing.T) {
	home, _ := os.UserHomeDir()
	dir := ""
	for _, d := range []string{os.Getenv("TURTLE_AGENT_MMAPS"), filepath.Join(home, "tb-ci", "mmaps-turtle389")} {
		if d == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(d, "389.mmap")); err == nil {
			dir = d
			break
		}
	}
	if dir == "" {
		t.Skip("no map 389 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 389.mmap and the 389*.mmtile tiles")
	}
	pl := navmesh.NewPlanner(dir)
	defer pl.Close()
	g := remoteGame{c: &commanderService{planner: pl}}
	// x, y, z sent, and how far under the floor it was (floor_minus_z); refused at more than 3, accepted at 2.64.
	for _, c := range []struct{ x, y, z, under float32 }{
		{-44.65, -39.53, -25.67, 4.16}, {-54.03, -39.04, -25.84, 5.31}, {-49.77, -39.3, -24.25, 3.25}, {-58.95, -38.8, -24.45, 5.54},
	} {
		zs, ok := g.FloorsAt(context.Background(), 389, c.x, c.y, c.z)
		if !ok {
			t.Fatalf("%v: no floors", c)
		}
		best := zs[0]
		for _, z := range zs {
			if math.Abs(float64(z-c.z)) < math.Abs(float64(best-c.z)) {
				best = z
			}
		}
		if math.Abs(float64(best-(c.z+c.under))) > 0.3 {
			t.Errorf("%v: floor %.2f, want about %.2f (floors %v)", c, best, c.z+c.under, zs)
		}
		n, ok, err := pl.Nearest(389, navmesh.Vec3{c.x, c.y, best})
		if err != nil || !ok || math.Abs(float64(n[2]-best)) > 0.3 {
			t.Errorf("%v: the planner does not accept the snapped step: %v %v %v", c, n, ok, err)
		}
	}
}
