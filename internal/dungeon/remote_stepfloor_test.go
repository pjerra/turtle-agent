package dungeon

import (
	"context"
	"math"
	"testing"
)

// slopeFake is a fakeRemote whose game plans the straight path and knows a floor: the plain 100 yd walk of the test below runs along
// z = 0, the floor rises 0.05 per yd of x. withFloors turns on the floor lookup, withSnap the nearest-floor snap.
type slopeFake struct {
	*fakeRemote
	withFloors, withSnap bool
}

func slopeZ(x float32) float32 { return x * 0.05 }

func (g slopeFake) PlanPath(_ context.Context, _ uint32, from, to Point) ([]Point, error) {
	return []Point{from, to}, nil
}

type slopeFloors struct{ slopeFake }

func (g slopeFloors) FloorsAt(_ context.Context, _ uint32, x, y, z float32) ([]float32, bool) {
	return []float32{slopeZ(x), slopeZ(x) - 30}, true // a second floor far below is never the nearest
}

type slopeSnap struct{ slopeFake }

func (g slopeSnap) SnapToFloor(_ context.Context, _ uint32, p Point) (Point, bool) {
	return Point{p.X, p.Y, slopeZ(p.X)}, true
}

func slopeWalk(t *testing.T, g interface {
	RemoteGame
	clock() Clock
}) []string {
	t.Helper()
	plan := remotePlan(Leg{Name: "a", To: Point{90, 0, 0}})
	if _, err := runRemote(t, g, plan, opts(), nil); err != nil {
		t.Fatal(err)
	}
	return nil
}

func moveZs(t *testing.T, orders []string) (xs, zs []float64) {
	t.Helper()
	for _, o := range orders {
		p, ok := moveArgsPoint(splitArgs(o))
		if !ok {
			t.Fatalf("bad move %q", o)
		}
		xs, zs = append(xs, float64(p.X)), append(zs, float64(p.Z))
	}
	return
}

func splitArgs(o string) []string {
	f := []string{}
	cur := ""
	for _, c := range o + " " {
		if c == ' ' {
			if cur != "" {
				f = append(f, cur)
			}
			cur = ""
			continue
		}
		cur += string(c)
	}
	return f[2:] // drop "cmd move"
}

// A walk step is given the height of the navmesh floor at its x, y, not the height of the straight line to the next point.
func TestRemoteWalkStepIsOnTheFloor(t *testing.T) {
	g := slopeFloors{slopeFake{fakeRemote: newFakeRemote(t)}}
	g.lead().Z = 0
	slopeWalk(t, g)
	xs, zs := moveZs(t, g.ordersOf("cmd move"))
	if len(xs) < 2 {
		t.Fatalf("moves %v", g.ordersOf("cmd move"))
	}
	for i := range xs[:len(xs)-1] { // the last order is the plan's own point
		if math.Abs(zs[i]-float64(slopeZ(float32(xs[i])))) > 0.02 {
			t.Fatalf("step %d at x %.1f has z %.2f, the floor is %.2f", i, xs[i], zs[i], slopeZ(float32(xs[i])))
		}
	}
}

// Without a floor lookup the step keeps the straight line's height, as before.
func TestRemoteWalkStepWithoutTilesKeepsTheOldPoint(t *testing.T) {
	g := slopeFake{fakeRemote: newFakeRemote(t)}
	g.lead().Z = 0
	slopeWalk(t, g)
	_, zs := moveZs(t, g.ordersOf("cmd move"))
	for i, z := range zs {
		if z != 0 {
			t.Fatalf("step %d has z %.2f: %v", i, z, g.ordersOf("cmd move"))
		}
	}
}

// An unreachable step is sent again on the floor with the same leg; the next refusal falls back to halving the leg.
func TestRemoteUnreachableStepIsResentSnappedBeforeHalving(t *testing.T) {
	g := slopeSnap{slopeFake{fakeRemote: newFakeRemote(t)}}
	g.lead().Z = 0
	g.cmdErr = map[string][]string{"move": {"unreachable", "unreachable"}}
	slopeWalk(t, g)
	xs, zs := moveZs(t, g.ordersOf("cmd move"))
	if len(xs) < 4 {
		t.Fatalf("moves %v", g.ordersOf("cmd move"))
	}
	if xs[1] != xs[0] || math.Abs(zs[1]-float64(slopeZ(float32(xs[0])))) > 0.02 || zs[0] != 0 {
		t.Fatalf("the resend: %v", g.ordersOf("cmd move"))
	}
	// the refusal of the resend is not answered with a second resend: the walk falls back to its old way (a shorter leg, the next step)
	if zs[2] != 0 || xs[2] > xs[1] {
		t.Fatalf("a second refusal was resent again: %v", g.ordersOf("cmd move"))
	}
}

// With no floor to snap to, the resend is raised 3 yd.
func TestRemoteUnreachableStepWithNoFloorIsRaised(t *testing.T) {
	g := slopeFake{fakeRemote: newFakeRemote(t)}
	g.lead().Z = 0
	g.cmdErr = map[string][]string{"move": {"unreachable"}}
	slopeWalk(t, g)
	xs, zs := moveZs(t, g.ordersOf("cmd move"))
	if xs[1] != xs[0] || math.Abs(zs[1]-zs[0]-3) > 0.01 {
		t.Fatalf("the resend: %v", g.ordersOf("cmd move"))
	}
}
