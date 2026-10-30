package planprobe

import (
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// fakeNav scripts the navmesh: a nil query keeps the neutral answer (a complete path, one floor at the asked height, the point
// itself walkable).
type fakeNav struct {
	server func(from, to navmesh.Vec3) (navmesh.ServerPath, error)
	plan   func(from, to navmesh.Vec3) (navmesh.Path, error)
	floors func(x, y, lo, hi float32) ([]navmesh.Floor, error)
	near   func(p navmesh.Vec3) (navmesh.Vec3, bool, error)
}

func (f *fakeNav) ServerPath(_ uint32, from, to navmesh.Vec3) (navmesh.ServerPath, error) {
	if f.server != nil {
		return f.server(from, to)
	}
	return navmesh.ServerPath{Type: navmesh.PathNormal, ActualEnd: to, Length: float32(dist3(from, to))}, nil
}

func (f *fakeNav) Plan(_ uint32, from, to navmesh.Vec3) (navmesh.Path, error) {
	if f.plan != nil {
		return f.plan(from, to)
	}
	return navmesh.Path{Status: navmesh.Complete, Points: []navmesh.Vec3{from, to}, Length: float32(dist3(from, to))}, nil
}

func (f *fakeNav) Floors(_ uint32, x, y, lo, hi float32) ([]navmesh.Floor, error) {
	if f.floors != nil {
		return f.floors(x, y, lo, hi)
	}
	return []navmesh.Floor{{Z: (lo + hi) / 2, Flags: navmesh.NavGround}}, nil
}

func (f *fakeNav) Nearest(_ uint32, p navmesh.Vec3) (navmesh.Vec3, bool, error) {
	if f.near != nil {
		return f.near(p)
	}
	return p, true, nil
}

func stepAt(leg, seq int, kind string, x, y, z float32) planexport.StepRow {
	return planexport.StepRow{Plan: "probe", Leg: leg, Seq: seq, Kind: kind, X: x, Y: y, Z: z}
}

func moveAt(leg, seq int, x, y, z float32) planexport.StepRow {
	return stepAt(leg, seq, planexport.KindMove, x, y, z)
}

func rowsOf(steps ...planexport.StepRow) planexport.Rows {
	return planexport.Rows{Plan: planexport.PlanRow{Plan: "probe", Map: 249}, Steps: steps}
}

func probeOne(t *testing.T, nav Nav, rows planexport.Rows) Segment {
	t.Helper()
	rep, err := Probe(rows, nav, nil, Options{})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if len(rep.Segments) != 1 {
		t.Fatalf("got %d segments, want 1: %+v", len(rep.Segments), rep.Segments)
	}
	return rep.Segments[0]
}

// TestProbeFlagsRowUnderTheFloor is walk 3's step 26: the row lies 7.9 yd under the only floor over it.
func TestProbeFlagsRowUnderTheFloor(t *testing.T) {
	nav := &fakeNav{floors: func(x, y, lo, hi float32) ([]navmesh.Floor, error) {
		return []navmesh.Floor{{Z: -66.7, Flags: navmesh.NavGround}}, nil
	}}
	seg := probeOne(t, nav, rowsOf(moveAt(0, 0, 10, 10, 0), moveAt(0, 1, -149.46112, -210.48434, -74.62397)))
	if seg.Verdict != VerdictBad {
		t.Fatalf("verdict = %s (%v), want bad", seg.Verdict, seg.Reasons)
	}
	if got, want := seg.Point.FloorDZ, float32(7.92); got < want-0.05 || got > want+0.05 {
		t.Fatalf("floor_dz = %v, want %v", got, want)
	}
	r := strings.Join(seg.Reasons, "; ")
	if !strings.Contains(r, "-74.62") || !strings.Contains(r, "-66.70") {
		t.Fatalf("reason does not name both heights: %q", r)
	}
}

// TestProbePullEndOffMeshWithinRadiusIsOk: the party walks to within pull range, not onto the pack: an off-mesh end within the
// row's radius is the pull working, and a pull row's height is not held to the floor.
func TestProbePullEndOffMeshWithinRadiusIsOk(t *testing.T) {
	nav := &fakeNav{
		server: func(from, to navmesh.Vec3) (navmesh.ServerPath, error) {
			return navmesh.ServerPath{Type: navmesh.PathNormal, ActualEnd: navmesh.Vec3{0, 35, 0}, Length: 20}, nil
		},
		floors: func(x, y, lo, hi float32) ([]navmesh.Floor, error) {
			return []navmesh.Floor{{Z: -10, Flags: navmesh.NavGround}}, nil
		},
	}
	b := stepAt(0, 1, planexport.KindPull, 0, 20, 0)
	b.Radius = 15
	seg := probeOne(t, nav, rowsOf(moveAt(0, 0, 0, 0, 1), b))
	if seg.Server.Verdict != "off-mesh" {
		t.Fatalf("server verdict = %s, want off-mesh", seg.Server.Verdict)
	}
	if seg.Verdict != VerdictOK {
		t.Fatalf("verdict = %s (%v), want ok", seg.Verdict, seg.Reasons)
	}
}

// TestProbeWaterAndLavaYards: the sampled path adds up the steps on water and on lava/slime.
func TestProbeWaterAndLavaYards(t *testing.T) {
	nav := &fakeNav{
		plan: func(from, to navmesh.Vec3) (navmesh.Path, error) {
			return navmesh.Path{Status: navmesh.Complete, Length: 8,
				Points: []navmesh.Vec3{{0, 0, 0}, {4, 0, 0}, {8, 0, 0}},
				Ground: [][]navmesh.Vec3{{{2, 0, 0}}, {{6, 0, 0}}}}, nil
		},
		floors: func(x, y, lo, hi float32) ([]navmesh.Floor, error) {
			switch {
			case x == 2:
				return []navmesh.Floor{{Z: 0, Flags: navmesh.NavWater}}, nil
			case x == 6:
				return []navmesh.Floor{{Z: 0, Flags: navmesh.NavMagma | navmesh.NavGround}}, nil
			}
			return []navmesh.Floor{{Z: 0, Flags: navmesh.NavGround}}, nil
		},
	}
	seg := probeOne(t, nav, rowsOf(moveAt(0, 0, 0, 0, 1), moveAt(0, 1, 8, 0, 0)))
	if seg.WaterYd != 2 || seg.LavaYd != 2 {
		t.Fatalf("water %v yd, lava %v yd; want 2 and 2", seg.WaterYd, seg.LavaYd)
	}
	reasons := strings.Join(seg.Reasons, "; ")
	if !strings.Contains(reasons, "water") || !strings.Contains(reasons, "lava") {
		t.Fatalf("reasons = %q, want water and lava", reasons)
	}
}

// TestProbeSteepAndDrop: a 10 yd drop over 1 yd of ground is steep, and the drop is a warning.
func TestProbeSteepAndDrop(t *testing.T) {
	nav := &fakeNav{floors: func(x, y, lo, hi float32) ([]navmesh.Floor, error) {
		return []navmesh.Floor{{Z: -9, Flags: navmesh.NavGround}}, nil
	}}
	seg := probeOne(t, nav, rowsOf(moveAt(0, 0, 0, 0, 1), moveAt(0, 1, 1, 0, -9)))
	if !seg.Steep || seg.MaxDrop != 10 {
		t.Fatalf("steep %v, max_drop %v; want true and 10", seg.Steep, seg.MaxDrop)
	}
	reasons := strings.Join(seg.Reasons, "; ")
	if !strings.Contains(reasons, "steep") || !strings.Contains(reasons, "drop 10.0 yd") {
		t.Fatalf("reasons = %q, want steep and drop", reasons)
	}
}

// TestProbeMultiFloorNote: two surfaces 12 yd apart over the row's (x, y) are two floors, and the plan says z decides.
func TestProbeMultiFloorNote(t *testing.T) {
	nav := &fakeNav{floors: func(x, y, lo, hi float32) ([]navmesh.Floor, error) {
		return []navmesh.Floor{{Z: 12, Flags: navmesh.NavGround}, {Z: 0, Flags: navmesh.NavGround}}, nil
	}}
	rep, err := Probe(rowsOf(moveAt(0, 0, 0, 0, 1), moveAt(0, 1, 10, 0, 0)), nav, nil, Options{})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	seg := rep.Segments[0]
	if seg.Point.FloorsHere != 2 {
		t.Fatalf("floors_here = %d, want 2", seg.Point.FloorsHere)
	}
	reasons := strings.Join(seg.Reasons, "; ")
	if !strings.Contains(reasons, "multi-floor x,y") {
		t.Fatalf("reasons = %q, want the multi-floor warning", reasons)
	}
	if !strings.Contains(strings.Join(rep.Notes, "\n"), "multi-floor map") {
		t.Fatalf("notes = %q, want the multi-floor note", rep.Notes)
	}
}

// TestProbeRatioWarn: the ratio is the server's path length over the straight line (not the offline plan's length).
func TestProbeRatioWarn(t *testing.T) {
	nav := &fakeNav{server: func(from, to navmesh.Vec3) (navmesh.ServerPath, error) {
		return navmesh.ServerPath{Type: navmesh.PathNormal, ActualEnd: to, Length: float32(2 * dist3(from, to))}, nil
	}}
	seg := probeOne(t, nav, rowsOf(moveAt(0, 0, 0, 0, 1), moveAt(0, 1, 10, 0, 0)))
	if seg.Ratio != 2 {
		t.Fatalf("ratio = %v, want 2", seg.Ratio)
	}
	if seg.Verdict != VerdictWarn {
		t.Fatalf("verdict = %s (%v), want warn", seg.Verdict, seg.Reasons)
	}
}

// TestProbeSkipsUnpositionedRows: a rest row (0, 0, 0) between two moves is no end; the segment runs from the move before it to
// the move after it, and keeps the run leader's step numbers.
func TestProbeSkipsUnpositionedRows(t *testing.T) {
	rep, err := Probe(rowsOf(
		moveAt(0, 0, 1, 0, 0),
		stepAt(0, 1, planexport.KindRest, 0, 0, 0),
		moveAt(0, 2, 5, 0, 0),
	), &fakeNav{}, nil, Options{})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if len(rep.Segments) != 1 {
		t.Fatalf("got %d segments, want 1", len(rep.Segments))
	}
	if seg := rep.Segments[0]; seg.From.Step != 1 || seg.To.Step != 3 {
		t.Fatalf("segment steps %d->%d, want 1->3", seg.From.Step, seg.To.Step)
	}
}

// TestProbeStepNumbersAcrossLegs: the step number is the row's index in the whole plan + 1, so leg 1's first row is step
// len(leg 0) + 1.
func TestProbeStepNumbersAcrossLegs(t *testing.T) {
	rep, err := Probe(rowsOf(
		moveAt(0, 0, 0, 0, 1),
		moveAt(1, 0, 10, 0, 0),
		moveAt(1, 1, 20, 0, 0),
	), &fakeNav{}, nil, Options{})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if len(rep.Segments) != 2 {
		t.Fatalf("got %d segments, want 2", len(rep.Segments))
	}
	if seg := rep.Segments[0]; seg.To.Leg != 1 || seg.To.Step != 2 {
		t.Fatalf("leg 1's first segment ends %d/%d, want leg 1 step 2", seg.To.Leg, seg.To.Step)
	}
	if seg := rep.Segments[1]; seg.From.Step != 2 || seg.To.Step != 3 {
		t.Fatalf("second segment %d->%d, want 2->3", seg.From.Step, seg.To.Step)
	}
}
