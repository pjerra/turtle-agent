//go:build cgo

package planprobe

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// probePlanner is a planner over the real map 249 tiles ($TURTLE_AGENT_MMAPS or ~/turtle-agent-mmaps); the test skips without
// them.
func probePlanner(t *testing.T) *navmesh.Planner {
	t.Helper()
	home, _ := os.UserHomeDir()
	for _, dir := range []string{os.Getenv("TURTLE_AGENT_MMAPS"), filepath.Join(home, "turtle-agent-mmaps")} {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "249.mmap")); err != nil {
			continue
		}
		if tiles, _ := filepath.Glob(filepath.Join(dir, "249*.mmtile")); len(tiles) == 0 {
			continue
		}
		p := navmesh.NewPlanner(dir)
		t.Cleanup(p.Close)
		return p
	}
	t.Skip("no map 249 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 249.mmap and its tiles")
	return nil
}

// onyxiaFixtures are the deployed Onyxia migrations B1 checks in: the 120000 export's step 26 lies under the floor, the
// 130000 export carries the boss approach points instead.
func onyxiaFixture(t *testing.T, name string) planexport.Rows {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "planexport", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	all, _, err := planexport.ParseMigration(string(b))
	if err != nil {
		t.Fatalf("ParseMigration(%s): %v", name, err)
	}
	if len(all) != 1 {
		t.Fatalf("%s holds %d plans, want 1", name, len(all))
	}
	return all[0]
}

func segmentTo(t *testing.T, rep *Report, step int) Segment {
	t.Helper()
	for _, s := range rep.Segments {
		if s.To.Step == step {
			return s
		}
	}
	t.Fatalf("no segment ends at step %d", step)
	return Segment{}
}

// TestProbeOnyxiaStep26KnownBad is walk 3's failure: the cut row at -149.46 -210.48 -74.62 lies under the floor. The walk's
// log named the floor ~-66.7; the map 249 mesh measures the surface at -67.24, so the gap is 7.39 yd (the row's z is pinned,
// the floor measured).
func TestProbeOnyxiaStep26KnownBad(t *testing.T) {
	rep, err := Probe(onyxiaFixture(t, "onyxias_lair-20261029120000.sql"), probePlanner(t), nil, Options{})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	seg := segmentTo(t, rep, 26)
	if seg.Verdict != VerdictBad {
		t.Fatalf("step 25->26 verdict = %s (%v), want bad", seg.Verdict, seg.Reasons)
	}
	if seg.Point.FloorDZ <= 5 {
		t.Fatalf("floor_dz = %v, want more than 5", seg.Point.FloorDZ)
	}
	pinned := func(name string, got, want float32) {
		t.Helper()
		if got < want-0.5 || got > want+0.5 {
			t.Fatalf("%s = %v, want %v +-0.5", name, got, want)
		}
	}
	pinned("row z", seg.To.Z, -74.62)
	t.Logf("floors at step 26: %v", seg.Point.Floors)
	pinned("floor_dz", seg.Point.FloorDZ, 7.39)
	var floor float32
	for _, f := range seg.Point.Floors {
		if f.Z > seg.To.Z+5 && f.Z < seg.To.Z+9 {
			floor = f.Z
		}
	}
	pinned("floor", floor, -67.24)
	t.Logf("step 25->26: straight %v, server %v %v, end_short %v, floors %v, reasons %v", seg.Straight,
		seg.Server.Type, seg.Server.Verdict, seg.Server.EndShort, seg.Point.Floors, seg.Reasons)
}

// TestProbeOnyxiaStep26FixedIsGood: the 130000 export's approach points sit on the floor and the plan has no bad segment.
func TestProbeOnyxiaStep26FixedIsGood(t *testing.T) {
	rep, err := Probe(onyxiaFixture(t, "onyxias_lair-20261029130000.sql"), probePlanner(t), nil, Options{})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if rep.Counts.Bad != 0 {
		t.Fatalf("%d bad segments, want 0: %+v", rep.Counts.Bad, rep.Segments)
	}
	for _, step := range []int{26, 27, 28} {
		seg := segmentTo(t, rep, step)
		if seg.Verdict == VerdictBad {
			t.Fatalf("step ->%d is bad: %v", step, seg.Reasons)
		}
		if step != 28 && seg.Point.FloorDZ > 1 {
			t.Fatalf("step ->%d floor_dz = %v, want <= 1", step, seg.Point.FloorDZ)
		}
	}
	for _, s := range rep.Segments {
		t.Logf("step %d->%d %s floor_dz %v", s.From.Step, s.To.Step, s.Verdict, s.Point.FloorDZ)
	}
}
