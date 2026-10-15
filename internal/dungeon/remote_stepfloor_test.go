package dungeon

import (
	"context"
	"math"
	"strings"
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

// slopeFloors lists the floor of the slope (and one far below), shifted by off.
type slopeFloors struct {
	slopeFake
	off float32
}

func (g slopeFloors) FloorsAt(_ context.Context, _ uint32, x, y, z float32) ([]float32, bool) {
	return []float32{slopeZ(x) + g.off, slopeZ(x) + g.off - 30}, true
}

// flatBelow lists only a floor 5 yd under the flat path.
type flatBelow struct{ slopeFake }

func (g flatBelow) FloorsAt(_ context.Context, _ uint32, _, _, _ float32) ([]float32, bool) {
	return []float32{-5}, true
}

type gameT interface {
	RemoteGame
	clock() Clock
}

// slopeWalk walks 90 yd to z endZ (the slope's own height there is 4.5) and returns the report.
func slopeWalk(t *testing.T, g gameT, endZ float32) Report {
	t.Helper()
	rep, err := runRemote(t, g, remotePlan(Leg{Name: "a", To: Point{90, 0, endZ}}), opts(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func moveZs(t *testing.T, orders []string) (xs, zs []float64) {
	t.Helper()
	for _, o := range orders {
		p, ok := moveArgsPoint(strings.Fields(o)[2:])
		if !ok {
			t.Fatalf("bad move %q", o)
		}
		xs, zs = append(xs, float64(p.X)), append(zs, float64(p.Z))
	}
	return
}

// A walk step is given the height of the navmesh floor at its x, y, not the height of the straight line to the next point.
func TestRemoteWalkStepIsOnTheFloor(t *testing.T) {
	g := slopeFloors{slopeFake: slopeFake{fakeRemote: newFakeRemote(t)}, off: -2}
	g.lead().Z = 0
	slopeWalk(t, g, slopeZ(90))
	xs, zs := moveZs(t, g.ordersOf("cmd move"))
	if len(xs) < 2 {
		t.Fatalf("moves %v", g.ordersOf("cmd move"))
	}
	for i := range xs[:len(xs)-1] { // the last order is the plan's own point
		if want := float64(slopeZ(float32(xs[i])) - 2); math.Abs(zs[i]-want) > 0.02 {
			t.Fatalf("step %d at x %.1f has z %.2f, the floor is %.2f", i, xs[i], zs[i], want)
		}
	}
}

// Without a floor lookup the step keeps the straight line's height, as before.
func TestRemoteWalkStepWithoutTilesKeepsTheOldPoint(t *testing.T) {
	g := slopeFake{fakeRemote: newFakeRemote(t)}
	g.lead().Z = 0
	slopeWalk(t, g, 0)
	_, zs := moveZs(t, g.ordersOf("cmd move"))
	for i, z := range zs {
		if z != 0 {
			t.Fatalf("step %d has z %.2f: %v", i, z, g.ordersOf("cmd move"))
		}
	}
}

// The floor of a step is picked in the height band of the path being cut, and the floor above wins a tie.
func TestNearestFloorKeepsToTheBandAndPrefersAbove(t *testing.T) {
	if z, ok := nearestFloor([]float32{-4, 4}, 0, -9, 9); !ok || z != 4 {
		t.Fatalf("a tie went to %v", z)
	}
	if z, ok := nearestFloor([]float32{-6, 7}, 0, -3, 3); ok {
		t.Fatalf("floors outside the band: %v", z)
	}
	if z, ok := nearestFloor([]float32{-4.5, -1.5}, -3, -9, 9); !ok || z != -1.5 {
		t.Fatalf("nearest: %v", z)
	}
}

// A deck below the path's heights (a floor 5 yd under the flat path) is not where a step goes.
func TestRemoteWalkStepIgnoresALevelBelowThePath(t *testing.T) {
	g := flatBelow{slopeFake{fakeRemote: newFakeRemote(t)}}
	g.lead().Z = 0
	slopeWalk(t, g, 0)
	_, zs := moveZs(t, g.ordersOf("cmd move"))
	for _, z := range zs {
		if z != 0 {
			t.Fatalf("a step went to z %.1f: %v", z, g.ordersOf("cmd move"))
		}
	}
}

// An unreachable mid-walk step that missed its floor by more than 2.5 yd is sent again at the floor's height, same x and y; a second
// refusal is not resent again.
func TestRemoteUnreachableStepIsResentOnItsFloor(t *testing.T) {
	// the floor is 5 yd above the flat path's band, so the step is cut at z 0 and refused; the resend finds the floor
	g := slopeFloors{slopeFake: slopeFake{fakeRemote: newFakeRemote(t)}, off: 5}
	g.lead().Z = 0
	g.cmdErr = map[string][]string{"move": {"unreachable", "unreachable"}}
	rep := slopeWalk(t, g, 0)
	xs, zs := moveZs(t, g.ordersOf("cmd move"))
	if len(xs) < 4 || zs[0] != 0 || xs[1] != xs[0] || math.Abs(zs[1]-float64(slopeZ(float32(xs[0]))+5)) > 0.02 {
		t.Fatalf("the resend: %v", g.ordersOf("cmd move"))
	}
	if zs[2] != 0 || strings.Count(notesOf(rep), "sent again") != 1 {
		t.Fatalf("a second refusal was resent again: %v\n%s", g.ordersOf("cmd move"), notesOf(rep))
	}
}

// A step already on its floor is not raised: the walk goes to a shorter step at once, in the careful walk too (hops of CarefulHop).
func TestRemoteUnreachableStepOnItsFloorHalvesTheHop(t *testing.T) {
	g := slopeFloors{slopeFake: slopeFake{fakeRemote: newFakeRemote(t)}}
	g.lead().Z = 0
	g.cmdErr = map[string][]string{"move": {"unreachable"}}
	rep := slopeWalk(t, g, 0)
	xs, zs := moveZs(t, g.ordersOf("cmd move"))
	if strings.Contains(notesOf(rep), "sent again") || math.Abs(zs[1]-float64(slopeZ(float32(xs[1])))) > 0.02 || !(xs[1] < xs[0]) {
		t.Fatalf("moves %v\n%s", g.ordersOf("cmd move"), notesOf(rep))
	}
}

// With no floor known at all the resend is raised 3 yd; the plan's own end point is never resent.
func TestRemoteUnreachableStepWithNoFloorIsRaisedButNotTheEndPoint(t *testing.T) {
	g := slopeFake{fakeRemote: newFakeRemote(t)}
	g.lead().Z = 0
	g.cmdErr = map[string][]string{"move": {"unreachable"}}
	slopeWalk(t, g, 0)
	xs, zs := moveZs(t, g.ordersOf("cmd move"))
	if xs[1] != xs[0] || math.Abs(zs[1]-zs[0]-3) > 0.01 {
		t.Fatalf("the resend: %v", g.ordersOf("cmd move"))
	}
	g = slopeFake{fakeRemote: newFakeRemote(t)}
	g.cmdErr = map[string][]string{"move": {"unreachable"}}
	rep, err := runRemote(t, g, remotePlan(Leg{Name: "a", To: Point{20, 0, 0}}), opts(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, zs = moveZs(t, g.ordersOf("cmd move"))
	if zs[1] != 0 || strings.Contains(notesOf(rep), "sent again") {
		t.Fatalf("the end point was resent raised: %v", g.ordersOf("cmd move"))
	}
}
