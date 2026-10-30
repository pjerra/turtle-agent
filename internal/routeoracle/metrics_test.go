package routeoracle

import (
	"math"
	"testing"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

func TestNearestPolyline(t *testing.T) {
	cases := []struct {
		name string
		pts  []Vec3
		p    Vec3
		want NearPoint
	}{
		{"mid segment", []Vec3{{0, 0, 0}, {10, 0, 0}}, Vec3{5, 3, 0}, NearPoint{Dist3D: 3, Dist2D: 3, At: Vec3{5, 0, 0}}},
		{"past the end", []Vec3{{0, 0, 0}, {10, 0, 0}}, Vec3{14, 0, 0}, NearPoint{Dist3D: 4, Dist2D: 4, At: Vec3{10, 0, 0}}},
		{"before the start", []Vec3{{0, 0, 0}, {10, 0, 0}}, Vec3{-4, 0, 0}, NearPoint{Dist3D: 4, Dist2D: 4, At: Vec3{0, 0, 0}}},
		{"second segment wins", []Vec3{{0, 0, 0}, {10, 0, 0}, {10, 10, 0}}, Vec3{11, 6, 0}, NearPoint{Dist3D: 1, Dist2D: 1, At: Vec3{10, 6, 0}}},
		{"straight above", []Vec3{{0, 0, 0}, {10, 0, 0}}, Vec3{5, 0, 7}, NearPoint{Dist3D: 7, Dist2D: 0, DZ: 7, At: Vec3{5, 0, 0}}},
		{"single point", []Vec3{{2, 3, 1}}, Vec3{2, 3, 1}, NearPoint{Dist3D: 0, Dist2D: 0, At: Vec3{2, 3, 1}}},
		{"empty polyline", nil, Vec3{0, 0, 0}, NearPoint{Dist3D: -1}},
	}
	for _, c := range cases {
		got := NearestPolyline(c.pts, c.p)
		if (got.Dist3D < 0) != (c.want.Dist3D < 0) {
			t.Errorf("%s: got Dist3D %v, want %v", c.name, got.Dist3D, c.want.Dist3D)
			continue
		}
		if got.Dist3D < 0 {
			continue
		}
		if math.Abs(got.Dist3D-c.want.Dist3D) > 1e-6 || math.Abs(got.Dist2D-c.want.Dist2D) > 1e-6 || math.Abs(got.DZ-c.want.DZ) > 1e-6 {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestNearestPolylineEmpty(t *testing.T) {
	if n := NearestPolyline(nil, Vec3{1, 2, 3}); n.Dist3D >= 0 {
		t.Errorf("empty polyline: %+v", n)
	}
}

func TestPolylineLength(t *testing.T) {
	cases := []struct {
		name string
		pts  []Vec3
		want float64
	}{
		{"one point", []Vec3{{0, 0, 0}}, 0},
		{"two points", []Vec3{{0, 0, 0}, {3, 4, 0}}, 5},
		{"three points", []Vec3{{0, 0, 0}, {3, 4, 0}, {3, 4, 12}}, 17},
	}
	for _, c := range cases {
		if got := PolylineLength(c.pts); math.Abs(got-c.want) > 1e-6 {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestZJumps(t *testing.T) {
	cases := []struct {
		name    string
		samples []Vec3
		want    []float64 // the jumps' sizes, in order
	}{
		{"flat", []Vec3{{0, 0, 0}, {4, 0, 0}, {8, 0, 0}}, nil},
		{"slope under the limit", []Vec3{{0, 0, 0}, {4, 0, 7}, {8, 0, 14}}, nil},
		{"one drop", []Vec3{{0, 0, 0}, {4, 0, 0}, {8, 0, -12}, {12, 0, -12}}, []float64{12}},
		{"one rise", []Vec3{{0, 0, 0}, {4, 0, 9}, {8, 0, 9}}, []float64{9}},
		{"exactly at the limit is no jump", []Vec3{{0, 0, 0}, {4, 0, 8}, {8, 0, 8}}, nil},
	}
	for _, c := range cases {
		got := ZJumps(c.samples)
		if len(got) != len(c.want) {
			t.Errorf("%s: %d jumps (%v), want %d", c.name, len(got), got, len(c.want))
			continue
		}
		for i := range got {
			if math.Abs(got[i].DropYd-c.want[i]) > 1e-6 {
				t.Errorf("%s: jump %d: %v, want %v", c.name, i, got[i].DropYd, c.want[i])
			}
		}
	}
}

func TestZJumpsAtIsTheLowerSample(t *testing.T) {
	got := ZJumps([]Vec3{{0, 0, 20}, {4, 0, 4}, {8, 0, 4}})
	if len(got) != 1 || got[0].At != (Vec3{4, 0, 4}) {
		t.Errorf("drop: %+v, want the lower sample at At", got)
	}
	got = ZJumps([]Vec3{{0, 0, 0}, {4, 0, 12}, {8, 0, 12}})
	if len(got) != 1 || got[0].At != (Vec3{0, 0, 0}) {
		t.Errorf("rise: %+v, want the lower sample at At", got)
	}
}

func TestOnCorridor(t *testing.T) {
	cases := []struct {
		name   string
		near   NearPoint
		radius float64
		want   bool
	}{
		{"on the path", NearPoint{Dist3D: 0, Dist2D: 0}, 25, true},
		{"just inside the radius", NearPoint{Dist3D: 42, Dist2D: 42, DZ: 1}, 25, true},
		{"too far horizontally", NearPoint{Dist3D: 44, Dist2D: 44, DZ: 0}, 25, false},
		{"a floor above", NearPoint{Dist3D: 20, Dist2D: 3, DZ: 20}, 25, false},
		{"no path at all", NearPoint{Dist3D: -1}, 25, false},
	}
	for _, c := range cases {
		if got := OnCorridor(c.near, c.radius); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRedundant(t *testing.T) {
	cases := []struct {
		name string
		near NearPoint
		want bool
	}{
		{"on the path", NearPoint{Dist3D: 0}, true},
		{"just inside", NearPoint{Dist3D: RedundantYd}, true},
		{"just outside", NearPoint{Dist3D: RedundantYd + 0.1}, false},
		{"no path", NearPoint{Dist3D: -1}, false},
	}
	for _, c := range cases {
		if got := Redundant(c.near); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPathSamples(t *testing.T) {
	p := navmesh.Path{Points: []Vec3{{0, 0, 0}, {10, 0, 0}}, Ground: [][]Vec3{{{4, 0, 1}, {8, 0, 2}}}}
	got := PathSamples(p)
	want := []Vec3{{0, 0, 0}, {4, 0, 1}, {8, 0, 2}, {10, 0, 0}}
	if len(got) != len(want) {
		t.Fatalf("got %d samples, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sample %d: %v, want %v", i, got[i], want[i])
		}
	}
	if got := PathSamples(navmesh.Path{}); got != nil {
		t.Errorf("empty path: %v", got)
	}
}
