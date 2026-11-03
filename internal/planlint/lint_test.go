package planlint

import (
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

// fakeNav answers the lints' queries: a flat ground at the point unless a query is scripted.
type fakeNav struct {
	floors  func(x, y, lo, hi float32) ([]navmesh.Floor, error)
	nearest func(p navmesh.Vec3) (navmesh.Vec3, bool, error)
	ray     func(from, to navmesh.Vec3) (navmesh.Ray, error)
}

func (f fakeNav) Floors(_ uint32, x, y, lo, hi float32) ([]navmesh.Floor, error) {
	if f.floors != nil {
		return f.floors(x, y, lo, hi)
	}
	return nil, nil
}

func (f fakeNav) Nearest(_ uint32, p navmesh.Vec3) (navmesh.Vec3, bool, error) {
	if f.nearest != nil {
		return f.nearest(p)
	}
	return p, true, nil
}

func (f fakeNav) Raycast(_ uint32, from, to navmesh.Vec3) (navmesh.Ray, error) {
	if f.ray != nil {
		return f.ray(from, to)
	}
	return navmesh.Ray{Known: true, Clear: true}, nil
}

func move(x, y, z float32) planexport.StepRow {
	return planexport.StepRow{Kind: planexport.KindMove, X: x, Y: y, Z: z}
}

func pull(x, y, z float32, entries ...uint32) planexport.StepRow {
	return planexport.StepRow{Kind: planexport.KindPull, X: x, Y: y, Z: z, Radius: 3, Entries: entries}
}

func rowsOf(steps ...planexport.StepRow) planexport.Rows {
	for i := range steps {
		steps[i].Leg, steps[i].Seq = 0, i
	}
	return planexport.Rows{Plan: planexport.PlanRow{Plan: "t", Map: 1}, Legs: []planexport.LegRow{{Plan: "t"}}, Steps: steps}
}

func lintOne(t *testing.T, rows planexport.Rows, nav Nav, world *worldspawn.World) []Finding {
	t.Helper()
	fs, err := Lint(rows, nav, Options{Map: 1, Level: 60, World: world})
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func wantOnly(t *testing.T, fs []Finding, lint string) {
	t.Helper()
	c := Counts(fs)
	if c[lint] != 1 || len(fs) != 1 {
		t.Fatalf("findings %v, want one %s", fs, lint)
	}
}

// A point on a NavWater floor.
func TestLintWater(t *testing.T) {
	nav := fakeNav{floors: func(x, y, lo, hi float32) ([]navmesh.Floor, error) {
		return []navmesh.Floor{{Z: 0, Flags: navmesh.NavWater}}, nil
	}}
	wantOnly(t, lintOne(t, rowsOf(move(0, 0, 0)), nav, nil), LintWater)
}

// A point on plain ground but under the water surface of a nearby NavWater floor (the lake bed the mmap generator flags dry).
func TestLintWaterUnderNearbySurface(t *testing.T) {
	nav := fakeNav{floors: func(x, y, lo, hi float32) ([]navmesh.Floor, error) {
		if x == navmesh.LiquidNearYd && lo <= 5 && hi >= 5 {
			return []navmesh.Floor{{Z: 5, Flags: navmesh.NavGround | navmesh.NavWater}}, nil
		}
		return nil, nil
	}, nearest: func(p navmesh.Vec3) (navmesh.Vec3, bool, error) {
		return p, true, nil
	}}
	fs := lintOne(t, rowsOf(move(0, 0, 0)), nav, nil)
	wantOnly(t, fs, LintWater)
	if !strings.Contains(fs[0].Detail, "NavWater floor stands") {
		t.Fatalf("detail %q does not name the nearby surface", fs[0].Detail)
	}
}

// A point 2 yd above the nearest floor.
func TestLintOffFloor(t *testing.T) {
	nav := fakeNav{nearest: func(p navmesh.Vec3) (navmesh.Vec3, bool, error) {
		return navmesh.Vec3{p[0], p[1], p[2] - 2}, true, nil
	}}
	wantOnly(t, lintOne(t, rowsOf(move(0, 0, 0)), nav, nil), LintOffFloor)
}

// A point 1.5 yd from a mesh edge.
func TestLintEdge(t *testing.T) {
	nav := fakeNav{ray: func(navmesh.Vec3, navmesh.Vec3) (navmesh.Ray, error) {
		return navmesh.Ray{Known: true, Hit: 1.5}, nil
	}}
	wantOnly(t, lintOne(t, rowsOf(move(0, 0, 0)), nav, nil), LintEdge)
}

// A point with another floor 5 yd above it (and within 4.5 yd across).
func TestLintLedge(t *testing.T) {
	nav := fakeNav{floors: func(x, y, lo, hi float32) ([]navmesh.Floor, error) {
		if x == 0 && y == 0 {
			return []navmesh.Floor{{Z: 0, Flags: navmesh.NavGround}}, nil
		}
		return []navmesh.Floor{{Z: 5, Flags: navmesh.NavGround}}, nil
	}}
	wantOnly(t, lintOne(t, rowsOf(move(0, 0, 0)), nav, nil), LintLedge)
}

// A hop of 1 yd between two rows.
func TestLintShortHop(t *testing.T) {
	wantOnly(t, lintOne(t, rowsOf(move(10, 0, 0), move(11, 0, 0)), fakeNav{}, nil), LintShortHop)
}

func worldOf(spawns ...worldspawn.Spawn) *worldspawn.World {
	return &worldspawn.World{Map: 1, Spawns: spawns}
}

// The move before a pull stands 10 yd from a hostile unit no row pulls: inside its 20 yd aggro plus 8.
func TestLintAggro(t *testing.T) {
	w := worldOf(worldspawn.Spawn{GUID: 1, Entry: 11, Name: "guard", LevelMax: 60, DetectionYd: 20, Faction: 14,
		Pos: dungeon.Point{X: 10, Y: 0, Z: 0}})
	wantOnly(t, lintOne(t, rowsOf(move(0, 0, 0), pull(50, 0, 0, 999)), fakeNav{}, w), LintAggro)
}

// A point 2 yd from a patrol lane whose patroller no row pulls.
func TestLintPatrol(t *testing.T) {
	w := &worldspawn.World{Map: 1,
		Spawns: []worldspawn.Spawn{{GUID: 7, Entry: 77, Name: "walker", LevelMax: 60, DetectionYd: 20, Faction: 14}},
		Patrols: []worldspawn.Patrol{{ID: "7", SpawnGUIDs: []uint64{7}, Entries: []uint32{77}, Name: "walker",
			Path: []dungeon.Point{{X: 0, Y: 0, Z: 0}, {X: 0, Y: 20, Z: 0}, {X: 20, Y: 20, Z: 0}, {X: 20, Y: 0, Z: 0}}}}}
	wantOnly(t, lintOne(t, rowsOf(move(2, 5, 0)), fakeNav{}, w), LintPatrol)
}

// A point on open ground away from every unit passes.
func TestLintPasses(t *testing.T) {
	w := worldOf(worldspawn.Spawn{GUID: 1, Entry: 11, Name: "far", LevelMax: 60, DetectionYd: 20, Faction: 14,
		Pos: dungeon.Point{X: 300, Y: 0, Z: 0}})
	if fs := lintOne(t, rowsOf(move(0, 0, 0), move(40, 0, 0)), fakeNav{}, w); len(fs) != 0 {
		t.Fatalf("findings %v", fs)
	}
}
