package planlint

import (
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// routeNav is fakeNav with a scripted offline path per hop (straight until
// told otherwise). The server path answers like the default scout straight
// line, so reach/arrival stay clean and only the route rule under test fires.
type routeNav struct {
	fakeNav
	plan func(from, to navmesh.Vec3) (navmesh.Path, error)
}

func (r routeNav) Plan(_ uint32, from, to navmesh.Vec3) (navmesh.Path, error) {
	if r.plan != nil {
		return r.plan(from, to)
	}
	return navmesh.Path{Status: navmesh.Complete, Points: []navmesh.Vec3{from, to},
		Length: float32(dist3(point3(from), point3(to)))}, nil
}

func (r routeNav) ServerPath(_ uint32, from, to navmesh.Vec3) (navmesh.ServerPath, error) {
	return navmesh.ServerPath{Type: navmesh.PathNormal, Points: []navmesh.Vec3{from, to}, ActualEnd: to}, nil
}

func straightLen(from, to navmesh.Vec3) navmesh.Path {
	return navmesh.Path{Status: navmesh.Complete, Points: []navmesh.Vec3{from, to},
		Length: float32(dist3(point3(from), point3(to)))}
}

// legged returns a path whose corners run from->via->to; length is the sum of
// the legs, so a via far off the straight line inflates the ratio.
func legged(from, via, to navmesh.Vec3) navmesh.Path {
	return navmesh.Path{Status: navmesh.Complete, Points: []navmesh.Vec3{from, via, to},
		Length: float32(dist3(point3(from), point3(via)) + dist3(point3(via), point3(to)))}
}

// routeRows builds steps in legs: legOf[i] is step i's leg. Every step is a
// positioned move; legs only matter to backtrack. Points are shifted 1000 yd
// east: the origin (0,0,0) reads as "no place on the map".
func routeRows(legs []int, pts ...dungeon.Point) planexport.Rows {
	steps := make([]planexport.StepRow, len(pts))
	for i, p := range pts {
		steps[i] = planexport.StepRow{Kind: planexport.KindMove, X: p.X + 1000, Y: p.Y, Z: p.Z}
		steps[i].Leg, steps[i].Seq = legs[i], i
	}
	return planexport.Rows{Plan: planexport.PlanRow{Plan: "t", Map: 1}, Legs: []planexport.LegRow{{Plan: "t"}}, Steps: steps}
}
func legsOf(n, leg int) []int {
	ls := make([]int, n)
	for i := range ls {
		ls[i] = leg
	}
	return ls
}
func routeFindings(t *testing.T, rows planexport.Rows, nav Nav) []Finding {
	t.Helper()
	fs, err := Lint(rows, nav, Options{Map: 1, Level: 60})
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func countRoute(t *testing.T, fs []Finding, lint string) int {
	t.Helper()
	n := 0
	for _, f := range fs {
		if f.Lint == lint {
			n++
		}
	}
	return n
}

// A navmesh detour twice the straight distance (20 yd straight, 40 yd path)
// warns; at over 3x it errors.
func TestRoutePathRatioWarn(t *testing.T) {
	rows := routeRows(legsOf(2, 0), dungeon.Point{X: 0, Y: 0}, dungeon.Point{X: 20, Y: 0})
	nav := routeNav{}
	nav.plan = func(from, to navmesh.Vec3) (navmesh.Path, error) {
		return legged(from, navmesh.Vec3{1010, 17.3, 0}, to), nil // ~20+20 legs over 20 straight
	}
	fs := routeFindings(t, rows, nav)
	if n := countRoute(t, fs, LintPathRatio); n != 1 {
		t.Fatalf("want 1 %s finding, got %d: %v", LintPathRatio, n, fs)
	}
	if fs[0].Severity != SeverityWarning {
		t.Fatalf("ratio ~2x must warn, got severity %q (%v)", fs[0].Severity, fs)
	}
}

func TestRoutePathRatioError(t *testing.T) {
	rows := routeRows(legsOf(2, 0), dungeon.Point{X: 0, Y: 0}, dungeon.Point{X: 20, Y: 0})
	nav := routeNav{}
	nav.plan = func(from, to navmesh.Vec3) (navmesh.Path, error) {
		return legged(from, navmesh.Vec3{1010, 30, 0}, to), nil // ~31.6+31.6 over 20
	}
	fs := routeFindings(t, rows, nav)
	found := false
	for _, f := range fs {
		if f.Lint == LintPathRatio && f.Severity == SeverityError {
			found = true
		}
	}
	if !found {
		t.Fatalf("ratio >3x must error: %v", fs)
	}
}

// A short hop (under 15 yd straight) never ratio-fires, however bent.
func TestRoutePathRatioIgnoresShortHops(t *testing.T) {
	rows := routeRows(legsOf(2, 0), dungeon.Point{X: 0, Y: 0}, dungeon.Point{X: 10, Y: 0})
	nav := routeNav{}
	nav.plan = func(from, to navmesh.Vec3) (navmesh.Path, error) {
		return legged(from, navmesh.Vec3{1005, 30, 0}, to), nil
	}
	if n := countRoute(t, routeFindings(t, rows, nav), LintPathRatio); n != 0 {
		t.Fatalf("short hops must not ratio-fire, got %d", n)
	}
}

// No complete navmesh path between two rows is an error, without mmaps or
// with a partial one.
func TestRouteNoPath(t *testing.T) {
	rows := routeRows(legsOf(2, 0), dungeon.Point{X: 0, Y: 0}, dungeon.Point{X: 40, Y: 0})
	nav := routeNav{}
	nav.plan = func(from, to navmesh.Vec3) (navmesh.Path, error) {
		return navmesh.Path{Status: navmesh.NoPath}, nil
	}
	fs := routeFindings(t, rows, nav)
	found := false
	for _, f := range fs {
		if f.Lint == LintNoPath && f.Severity == SeverityError {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing path must error: %v", fs)
	}
}

// Without an offline planner (no mmaps) the geometry-only rules still fire.
func TestRouteRulesRunWithoutPathNav(t *testing.T) {
	rows := routeRows(legsOf(2, 0), dungeon.Point{X: 0, Y: 0}, dungeon.Point{X: 100, Y: 0})
	fs := routeFindings(t, rows, fakeNav{})
	if n := countRoute(t, fs, LintLongHop); n != 1 {
		t.Fatalf("long_hop must fire without mmaps, got %d: %v", n, fs)
	}
	for _, f := range fs {
		if f.Lint == LintPathRatio || f.Lint == LintNoPath || f.Lint == LintZJump {
			t.Fatalf("path rules must not fire without mmaps: %v", fs)
		}
	}
}

// A 20 yd hop climbing 21 yd with a straight-line path (steeper than 1:1) is
// a zjump error; the same climb along a gradually ascending path is clean.
func TestRouteZJump(t *testing.T) {
	mk := func() planexport.Rows {
		return routeRows(legsOf(2, 0), dungeon.Point{X: 0, Y: 0, Z: 50}, dungeon.Point{X: 20, Y: 0, Z: 71})
	}
	steep := routeNav{} // straight path climbs steeper than 1:1
	fs := routeFindings(t, mk(), steep)
	found := false
	for _, f := range fs {
		if f.Lint == LintZJump && f.Severity == SeverityError {
			found = true
		}
	}
	if !found {
		t.Fatalf("steeper-than-1:1 climb over 6 yd must zjump-error: %v", fs)
	}

	gradual := routeNav{}
	gradual.plan = func(from, to navmesh.Vec3) (navmesh.Path, error) {
		// Switchbacks: each corner gains 3 yd over ~6.6 yd of run.
		pts := []navmesh.Vec3{from}
		for k := 1; k <= 6; k++ {
			f := float32(k) / 7
			y := float32(6)
			if k%2 == 0 {
				y = -6
			}
			pts = append(pts, navmesh.Vec3{from[0] + (to[0]-from[0])*f, y, from[2] + (to[2]-from[2])*f})
		}
		pts = append(pts, to)
		var length float32
		for k := 1; k < len(pts); k++ {
			length += float32(dist3(point3(pts[k-1]), point3(pts[k])))
		}
		return navmesh.Path{Status: navmesh.Complete, Points: pts, Length: length}, nil
	}
	if n := countRoute(t, routeFindings(t, mk(), gradual), LintZJump); n != 0 {
		t.Fatalf("gradual climb must not zjump-fire")
	}
}

// A hop of 5 yd up is clean, however steep the mesh path.
func TestRouteZJumpIgnoresSmallDz(t *testing.T) {
	rows := routeRows(legsOf(2, 0), dungeon.Point{X: 0, Y: 0, Z: 50}, dungeon.Point{X: 21, Y: 0, Z: 55})
	if n := countRoute(t, routeFindings(t, rows, routeNav{}), LintZJump); n != 0 {
		t.Fatalf("dz under 6 yd must not zjump-fire")
	}
}

// Three returns within 10 yd of a row 3+ steps back inside any 10-row window
// warn; two do not.
func TestRouteBacktrack(t *testing.T) {
	pts := []dungeon.Point{
		{X: 0, Y: 0}, {X: 20, Y: 0}, {X: 40, Y: 0}, {X: 60, Y: 0},
		{X: 2, Y: 1},   // back near row 0 (gap 4)
		{X: 65, Y: 0},  // away
		{X: 21, Y: -1}, // back near row 1 (gap 5)
		{X: 80, Y: 0},  // away
		{X: 41, Y: 1},  // back near row 2 (gap 6): third return
	}
	rows := routeRows(legsOf(len(pts), 0), pts...)
	fs := routeFindings(t, rows, routeNav{})
	if n := countRoute(t, fs, LintBacktrack); n != 1 {
		t.Fatalf("want 1 %s finding, got %d: %v", LintBacktrack, n, fs)
	}
	var bt *Finding
	for i := range fs {
		if fs[i].Lint == LintBacktrack {
			bt = &fs[i]
		}
	}
	if bt == nil {
		t.Fatalf("no backtrack finding in %v", fs)
	}
	if bt.Severity != SeverityWarning {
		t.Fatalf("backtrack must warn, got %q", bt.Severity)
	}
	if !strings.Contains(bt.Detail, "zig-zag") {
		t.Fatalf("backtrack detail must say zig-zag: %q", bt.Detail)
	}
}

func TestRouteBacktrackNeedsThree(t *testing.T) {
	pts := []dungeon.Point{
		{X: 0, Y: 0}, {X: 20, Y: 0}, {X: 40, Y: 0}, {X: 60, Y: 0},
		{X: 2, Y: 1},   // back near row 0
		{X: 65, Y: 0},  // away
		{X: 21, Y: -1}, // back near row 1: only two
		{X: 80, Y: 0},
	}
	rows := routeRows(legsOf(len(pts), 0), pts...)
	if n := countRoute(t, routeFindings(t, rows, routeNav{}), LintBacktrack); n != 0 {
		t.Fatalf("two returns must not backtrack-fire")
	}
}

// Crossing into a new leg resets the window: returns to an older leg's rows
// do not count.
func TestRouteBacktrackResetsAtLeg(t *testing.T) {
	pts := []dungeon.Point{
		{X: 0, Y: 0}, {X: 20, Y: 0}, {X: 40, Y: 0}, {X: 60, Y: 0},
		{X: 2, Y: 1}, {X: 65, Y: 0}, {X: 21, Y: -1}, {X: 80, Y: 0}, {X: 41, Y: 1},
	}
	legs := legsOf(len(pts), 0)
	legs[8] = 1 // third return starts a new leg
	rows := routeRows(legs, pts...)
	if n := countRoute(t, routeFindings(t, rows, routeNav{}), LintBacktrack); n != 0 {
		t.Fatalf("a new leg must reset the backtrack window")
	}
}

// A straight hop past the leader's sight warns, without mmaps.
func TestRouteLongHop(t *testing.T) {
	rows := routeRows(legsOf(2, 0), dungeon.Point{X: 0, Y: 0}, dungeon.Point{X: 61, Y: 0})
	fs := routeFindings(t, rows, fakeNav{})
	found := false
	for _, f := range fs {
		if f.Lint == LintLongHop && f.Severity == SeverityWarning {
			found = true
		}
	}
	if !found {
		t.Fatalf("61 yd hop must long_hop-warn: %v", fs)
	}
	if n := countRoute(t, routeFindings(t, routeRows(legsOf(2, 0), dungeon.Point{X: 0, Y: 0}, dungeon.Point{X: 60, Y: 0}), fakeNav{}), LintLongHop); n != 0 {
		t.Fatalf("60 yd is within sight and must not fire")
	}
}
