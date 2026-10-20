package routeoracle

import (
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// The pack-on-route definition both modes share, at its edges: a position counts when it is
// within the group's radius plus CorridorSlackYd (18) of the route horizontally and no more than
// CorridorMaxDZYd (8) above or below it. Each case sits just either side of an edge, so a
// threshold moved by a yard (the slack dropped, doubled, or the radius left out) fails.
func TestGroupOnRouteBoundaries(t *testing.T) {
	route := []Vec3{{0, 0, 0}, {100, 0, 0}}
	pos := func(y, z float32) []dungeon.Point { return []dungeon.Point{{X: 50, Y: y, Z: z}} }
	cases := []struct {
		name   string
		radius float32
		at     []dungeon.Point
		on     bool
	}{
		{"radius 10: 27.9 yd out is on", 10, pos(27.9, 0), true},
		{"radius 10: 28.1 yd out is off", 10, pos(28.1, 0), false},
		{"default radius 25: 42.9 yd out is on", 0, pos(42.9, 0), true},
		{"default radius 25: 43.1 yd out is off", 0, pos(43.1, 0), false},
		{"radius 10: 7.9 yd above is on", 10, pos(0, 7.9), true},
		{"radius 10: 8.1 yd above is off", 10, pos(0, 8.1), false},
		{"radius 10: 8.1 yd below is off", 10, pos(0, -8.1), false},
		{"any one position is enough", 10, []dungeon.Point{{X: 50, Y: 90, Z: 0}, {X: 20, Y: 27.9, Z: 0}}, true},
		{"none of two positions", 10, []dungeon.Point{{X: 50, Y: 90, Z: 0}, {X: 20, Y: 28.1, Z: 0}}, false},
	}
	for _, c := range cases {
		g := &dungeon.PullGroup{Radius: c.radius, Positions: c.at}
		got, ok := GroupOnRoute(0, g, route)
		if !ok || got.OnPath != c.on {
			t.Errorf("%s: ok %v on %v (near %.1f dz %.1f), want %v", c.name, ok, got.OnPath, got.NearYd, got.DZYd, c.on)
		}
	}
}

// A hold-only group is judged by its spot; a group with neither a position nor a hold is empty:
// no result, so it is in no denominator.
func TestGroupOnRouteHoldAndEmptyGroups(t *testing.T) {
	route := []Vec3{{0, 0, 0}, {100, 0, 0}}
	hold := &dungeon.PullGroup{Radius: 10, Hold: &dungeon.Hold{Spot: dungeon.Point{X: 50, Y: 27.9}}}
	if got, ok := GroupOnRoute(2, hold, route); !ok || !got.OnPath || got.Group != 2 {
		t.Errorf("hold-only: %+v ok %v", got, ok)
	}
	if _, ok := GroupOnRoute(0, &dungeon.PullGroup{Entries: []uint32{1}}, route); ok {
		t.Error("an empty group has a pack result")
	}
	if n := emptyGroups(&dungeon.Plan{Legs: []dungeon.Leg{{PullGroups: []dungeon.PullGroup{{}, *hold}}, {PullGroups: []dungeon.PullGroup{{}}}}}); n != 2 {
		t.Errorf("empty groups %d, want 2", n)
	}
}
