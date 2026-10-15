package dungeon

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

// fakeNav is a flat world: every point has floor, a path is the straight line, except where the test says otherwise.
type fakeNav struct {
	off     map[Point]bool        // points with no floor
	partial map[Point]Point       // goal -> where its path ends instead (status partial)
	cut     func(a, b Point) bool // the ray from a to b leaves the mesh
	detour  map[Point]float32     // goal -> path length (a path longer than the line)
}

func (f fakeNav) Plan(_ uint32, from, to navmesh.Vec3) (navmesh.Path, error) {
	g := Point{to[0], to[1], to[2]}
	if f.off[g] {
		return navmesh.Path{Status: navmesh.NoPath, Reason: "goal is not on the navmesh (no walkable polygon within 5 yd)", Points: []navmesh.Vec3{}}, nil
	}
	end := to
	st := navmesh.Complete
	if e, ok := f.partial[g]; ok {
		end, st = navmesh.Vec3{e.X, e.Y, e.Z}, navmesh.Partial
	}
	p := navmesh.Path{Status: st, Points: []navmesh.Vec3{from, end}, Length: dist3D(from, end)}
	if l, ok := f.detour[g]; ok {
		p.Length = l
	}
	return p, nil
}

func dist3D(a, b navmesh.Vec3) float32 {
	return float32(math.Sqrt(float64((a[0]-b[0])*(a[0]-b[0]) + (a[1]-b[1])*(a[1]-b[1]) + (a[2]-b[2])*(a[2]-b[2]))))
}

func (f fakeNav) Raycast(_ uint32, from, to navmesh.Vec3) (navmesh.Ray, error) {
	a, b := Point{from[0], from[1], from[2]}, Point{to[0], to[1], to[2]}
	if f.cut != nil && f.cut(a, b) {
		return navmesh.Ray{Known: true, Hit: 1}, nil
	}
	return navmesh.Ray{Known: true, Clear: true}, nil
}

func (f fakeNav) Nearest(_ uint32, p navmesh.Vec3) (navmesh.Vec3, bool, error) {
	if f.off[Point{p[0], p[1], p[2]}] {
		return navmesh.Vec3{}, false, nil
	}
	return p, true, nil
}

// checkLegs runs the check on a plan made of the given legs (built by hand: Parse would refuse the faults under test).
func checkLegs(t *testing.T, nav Navmesh, from int, legs ...Leg) *PlanCheck {
	t.Helper()
	res, err := CheckPlan(&Plan{Name: "t", Map: 1, Legs: legs}, nav, from)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func issueReasons(is []PlanIssue) string {
	var out []string
	for _, i := range is {
		out = append(out, fmt.Sprintf("%d/%s/%d/%s", i.Leg, i.Part, i.PullGroup, i.Reason))
	}
	return strings.Join(out, " ")
}

func pg(x, y float32) PullGroup {
	return PullGroup{Entries: []uint32{1}, Positions: []Point{{x, y, 0}}}
}

func TestPlanCheckReasons(t *testing.T) {
	nav := fakeNav{
		off:     map[Point]bool{{10, 200, 0}: true},
		partial: map[Point]Point{{10, 150, 0}: {10, 110, 0}}, // 40 yd short: past checkPackReach
	}
	res := checkLegs(t, nav, 0, Leg{
		Name: "a", To: Point{10, 0, 0},
		Via:        []Waypoint{{Point: Point{5, 0, 0}}, {Point: Point{8, 0, 0}, Direct: true}, {Point: Point{40, 0, 0}, Direct: true}},
		PullGroups: []PullGroup{pg(10, 30), pg(10, 90), pg(10, 150)},
		Boss:       &Boss{Entry: 9, Position: Point{10, 200, 0}},
	})
	// 8,0 is a needless direct hop (a straight path of the same length), 40,0 is 32 yd across: too far. Group 3's path ends 40 yd short of
	// it. The boss stands off the mesh.
	if got, want := issueReasons(res.Issues), "0/direct via 2/0/too far 0/pull group look/3/unreachable 0/boss look/0/off the mesh"; got != want {
		t.Errorf("issues %q, want %q", got, want)
	}
	if got, want := issueReasons(res.Warnings), "0/direct via 1/0/needless direct"; got != want {
		t.Errorf("warnings %q, want %q", got, want)
	}
	if res.OK() || res.Points != 8 {
		t.Errorf("ok %v points %d", res.OK(), res.Points)
	}
	if s := res.Issues[1].String(); !strings.Contains(s, "leg 0 (a) pull group 3 of 3") {
		t.Errorf("numbered like dungeon_status: %s", s)
	}
}

func TestPlanCheckDirectHops(t *testing.T) {
	leg := Leg{To: Point{20, 0, 0}, Via: []Waypoint{{Point: Point{10, 0, 0}, Direct: true}, {Point: Point{20, 0, 0}, Direct: true}}}
	// A wall between the two hops: the ray from 10,0 to 20,0 leaves the mesh.
	cut := func(a, b Point) bool { return a.X >= 10 && b.X >= 20 }
	res := checkLegs(t, fakeNav{cut: cut}, 0, leg)
	if got, want := issueReasons(res.Issues), "0/direct via 1/0/no line of sight"; got != want {
		t.Errorf("issues %q, want %q", got, want)
	}
	// A path that winds (twice the line) is a hop the navmesh does not join straight: no warning.
	res = checkLegs(t, fakeNav{detour: map[Point]float32{{10, 0, 0}: 25, {20, 0, 0}: 25}}, 0, leg)
	if !res.OK() || len(res.Warnings) != 0 {
		t.Errorf("issues %q warnings %q", issueReasons(res.Issues), issueReasons(res.Warnings))
	}
}

func TestPlanCheckFromLeg(t *testing.T) {
	nav := fakeNav{off: map[Point]bool{{10, 0, 0}: true, {20, 0, 0}: true}}
	res := checkLegs(t, nav, 1, Leg{Name: "a", To: Point{10, 0, 0}}, Leg{Name: "b", To: Point{20, 0, 0}})
	if got := issueReasons(res.Issues); got != "1/to/0/off the mesh" {
		t.Errorf("%q", got)
	}
}

func TestPlanCheckExemptHop(t *testing.T) {
	leg := Leg{To: Point{40, 0, 0}, Via: []Waypoint{{Point: Point{10, 0, 0}}, {Point: Point{40, 0, 0}, Direct: true, CheckSkip: "a hole in the mesh"}}}
	res := checkLegs(t, fakeNav{}, 0, leg)
	if !res.OK() || len(res.Exempt) != 1 || res.Exempt[0].Reason != ReasonSkipped || res.Exempt[0].Detail != "a hole in the mesh" || len(res.Warnings) != 0 {
		t.Errorf("%+v", res)
	}
	if res.Points != 2 { // via 0 and `to`
		t.Errorf("points %d", res.Points)
	}
}

// The run starts at from_leg / from_pull_group: what comes before is not checked, the pull groups after it are. An explicit group 0 starts
// at the leg's first group (the leg's own walk is skipped); none given checks the walk too.
func TestPlanCheckFromPullGroup(t *testing.T) {
	nav := fakeNav{off: map[Point]bool{{5, 0, 0}: true, {10, 30, 0}: true, {10, 90, 0}: true}}
	leg := Leg{Name: "a", To: Point{5, 0, 0}, PullGroups: []PullGroup{pg(10, 30), pg(10, 90)}}
	zero, one := 0, 1
	for _, c := range []struct {
		from *int
		want string
	}{
		{nil, "0/to/0/off the mesh 0/pull group look/1/off the mesh 0/pull group look/2/off the mesh"},
		{&zero, "0/pull group look/1/off the mesh 0/pull group look/2/off the mesh"},
		{&one, "0/pull group look/2/off the mesh"},
	} {
		res, err := CheckPlanWith(&Plan{Name: "t", Map: 1, Legs: []Leg{leg}}, nav, CheckOptions{FromPullGroup: c.from})
		if err != nil {
			t.Fatal(err)
		}
		if got := issueReasons(res.Issues); got != c.want {
			t.Errorf("from group %v: %q, want %q", c.from, got, c.want)
		}
	}
}

// A start the plan does not have is an error, not an empty pass.
func TestPlanCheckRefusesABadStart(t *testing.T) {
	plan := &Plan{Name: "t", Map: 1, Legs: []Leg{{Name: "a", To: Point{5, 0, 0}, PullGroups: []PullGroup{pg(10, 30)}}, {Name: "b", To: Point{9, 0, 0}}}}
	two, neg, one := 2, -1, 1
	for _, opt := range []CheckOptions{{FromLeg: 2}, {FromLeg: -1}, {FromPullGroup: &two}, {FromPullGroup: &neg}, {FromLeg: 1, FromPullGroup: &one}, {FromLeg: 1, FromPullGroup: new(int)}} {
		if _, err := CheckPlanWith(plan, fakeNav{}, opt); !errors.Is(err, ErrCheckStart) {
			t.Errorf("%+v: %v", opt, err)
		}
	}
	if _, err := CheckPlanWith(plan, fakeNav{}, CheckOptions{FromLeg: 1}); err != nil {
		t.Error(err)
	}
}

// The tuned look counts: a pack off the mesh whose path ends 17 yd short of it is in view from the end with the default arrival slack (3),
// not with look_arrive 10 (the lead bot may stop 10 yd short of the look point, and the view is 25).
func TestPlanCheckTunedLook(t *testing.T) {
	nav := fakeNav{partial: map[Point]Point{{0, 60, 0}: {0, 43, 0}}}
	plan := &Plan{Name: "t", Map: 1, Legs: []Leg{{To: Point{0, 0, 0}, PullGroups: []PullGroup{pg(0, 60)}}}}
	for _, c := range []struct {
		opt  CheckOptions
		want string
	}{{CheckOptions{}, ""}, {CheckOptions{LookArrive: 10}, "0/pull group look/1/too far"}, {CheckOptions{LookStandoff: 12}, ""}} {
		res, err := CheckPlanWith(plan, nav, c.opt)
		if err != nil {
			t.Fatal(err)
		}
		if got := issueReasons(res.Issues); got != c.want {
			t.Errorf("%+v: %q, want %q", c.opt, got, c.want)
		}
	}
}

// Velratha's pull group 9 (ZF batch 2, Haiku s2): the pack at 1636 1105 stands off the mesh, the path ends at 1625.6 1109.3, and the look
// point 21 yd back from there was 31 yd from the pack (the live move ended at 1605.47 1104.17, the run stopped on "could not get a view").
// LookPoint moves it up the path to the first point within the view.
func TestLookPointStaysWithinView(t *testing.T) {
	pack := Point{1636, 1105, 9}
	path := []Point{{1591.76, 1085.92, 9.2}, {1606.93, 1105.33, 8.9}, {1625.6, 1109.33, 9.6}}
	old, ok := StandoffPoint(path, LookStandoff)
	if !ok || math.Abs(float64(old.X-1605.475)) > 1 || math.Abs(float64(old.Y-1104.167)) > 1 || dist3(old, pack) < 29 {
		t.Fatalf("the old look point %v (ok %v) should be the live 1605.47 1104.17, 30 yd from the pack", old, ok)
	}
	view := lookView(LookStandoff, lookArrive)
	if view != 21 {
		t.Fatalf("view %v", view)
	}
	got, ok := LookPoint(path, pack, LookStandoff, view)
	// The lead bot may stop lookArrive short of the point and lookAt drops frames beyond ClearSeeRadius: the point leaves room for both.
	if !ok || dist3(got, pack) > ClearSeeRadius-lookArrive || dist3(got, pack) < float64(view)-1 {
		t.Errorf("the look point %v is %.1f yd from the pack (ok %v): want %v, at most %d", got, dist3(got, pack), ok, view, ClearSeeRadius-lookArrive)
	}
	if got.X < old.X { // on the path and nearer its end than the old point
		t.Errorf("%v is not past %v", got, old)
	}
	// A pack on a complete path keeps the old standoff point; a look point already in view is left alone.
	onPath := Point{1625.6, 1109.33, 9.6}
	if want, _ := StandoffPoint(path, LookStandoff); func() Point { p, _ := LookPoint(path, onPath, LookStandoff, view); return p }() != want {
		t.Errorf("a pack at the path's end moved the look point")
	}
	near := Point{1622, 1108, 9.6}
	if want, _ := StandoffPoint(path, LookStandoff); func() Point { p, _ := LookPoint(path, near, LookStandoff, view); return p }() != want {
		t.Errorf("a look point in view moved")
	}
	// A path that never comes within view ends at its end; a short path does not move.
	far := Point{1700, 1300, 9}
	if p, ok := LookPoint(path, far, LookStandoff, view); !ok || p != path[2] {
		t.Errorf("out of view everywhere: %v %v", p, ok)
	}
	if _, ok := LookPoint(path[1:], pack, 40, view); ok {
		t.Errorf("a path shorter than the standoff should not move")
	}
	// The tuned look: a longer arrival slack pulls the point nearer, a standoff under the margin stays the limit.
	if v := lookView(LookStandoff, 8); v != 16 {
		t.Errorf("look_arrive 8: view %v", v)
	}
	if v := lookView(15, lookArrive); v != 15 {
		t.Errorf("look_standoff 15: view %v", v)
	}
}

// oldLook is the look point before LookPoint: the standoff counted back from the path's end.
func oldLook(path []Point, _ Point, standoff, _ float32) (Point, bool) {
	return StandoffPoint(path, standoff)
}

func zfNav(t *testing.T) *navmesh.Planner {
	t.Helper()
	dir := zulfarrakMmaps()
	if dir == "" {
		t.Skip("no map 209 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 209.mmap and the 209*.mmtile tiles")
	}
	pl := navmesh.NewPlanner(dir)
	t.Cleanup(pl.Close)
	return pl
}

func skipNoEngine(t *testing.T, err error) {
	t.Helper()
	if errors.Is(err, navmesh.ErrNoEngine) {
		t.Skip("built without cgo: no navmesh engine")
	}
	if err != nil {
		t.Fatal(err)
	}
}

// The shipped Zul'Farrak plan passes the plan check with nothing to warn of (issue #187).
func TestZulfarrakPlanCheck(t *testing.T) {
	pl := zfNav(t)
	for _, f := range []string{zulfarrakPlan, zulfarrakExtra} {
		p := loadPlan(t, f)
		res, err := CheckPlan(p, pl, 0)
		skipNoEngine(t, err)
		if !res.OK() || len(res.Warnings) != 0 {
			t.Errorf("%s:\n%s\n%s", p.Name, res.Summary(), res.WarningSummary())
		}
		if p.Name == "zulfarrak" && res.Points < 100 {
			t.Errorf("only %d points checked", res.Points)
		}
	}
}

// The plan as it shipped before the check (testdata, sha256 c86c1e62ee5a...) fails it where the batch-2 runs stopped, and warns of the direct hops
// that the navmesh joins. With the look point of that time (oldLook) Velratha's pull group 9 is "too far"; LookPoint passes it.
func TestZulfarrakOldPlanFailsCheck(t *testing.T) {
	pl := zfNav(t)
	old := loadPlan(t, filepath.Join("testdata", "zulfarrak-c86c1e62ee5a.json"))
	res, err := checkPlan(old, pl, CheckOptions{}, oldLook)
	skipNoEngine(t, err)
	var gp9 int
	for _, is := range res.Issues {
		if is.Leg == 8 && is.PullGroup == 9 && is.PullGroups == 12 && is.Reason == ReasonTooFar {
			gp9++
		}
	}
	if gp9 != 5 { // its five positions, each looked at from the same stand
		t.Errorf("pull group 9 of 12 fails %d times as too far, want 5:\n%s", gp9, res.Summary())
	}
	if !strings.Contains(res.Summary(), "1605.5 1104.2") {
		t.Errorf("the look point should be the live 1605.5 1104.2:\n%s", res.Summary())
	}
	// The hop to 1433.9 804.7 needed a ray the mesh cannot give (it bends round the ledge), and the plain walk to it is complete.
	if !strings.Contains(res.Summary(), "leg 2 (antusul_a3) direct via 1 1433.9 804.7 10.3: no line of sight") {
		t.Errorf("leg 2's first direct hop should fail:\n%s", res.Summary())
	}
	wantWarn := map[string]bool{"1/direct via 1": true, "1/direct via 2": true, "2/direct via 2": true, "2/direct via 3": true, "4/direct via 1": true, "4/direct via 2": true}
	for _, w := range res.Warnings {
		delete(wantWarn, fmt.Sprintf("%d/%s", w.Leg, w.Part))
	}
	if len(wantWarn) != 0 {
		t.Errorf("no needless-direct warning for %v:\n%s", wantWarn, res.WarningSummary())
	}
	// With LookPoint the group passes; the rest of the old plan's failure is the leg 2 hop alone.
	res, err = CheckPlan(old, pl, 0)
	skipNoEngine(t, err)
	if len(res.Issues) != 1 || res.Issues[0].Leg != 2 {
		t.Errorf("after the look point fix:\n%s", res.Summary())
	}
}

// planCheckSkip lists the shipped plans that fail the plan check today, with the failing points counted when the check came in. Issue #187
// follows them up one by one (most are drafts from the world export, whose points were never walked; Uldaman's two direct hops over the scorpid pit are exempt in the plan itself (check_skip)). Take a plan off the list when it is fixed;
// Zul'Farrak is not on it.
var planCheckSkip = map[string]bool{
	"black_morass":          true,
	"blackrock_depths":      true,
	"blackwing_lair":        true,
	"crescent_grove":        true,
	"deadmines":             true,
	"dire_maul_east":        true,
	"dragonmaw_retreat":     true,
	"gilneas_city":          true,
	"gnomeregan":            true,
	"hateforge_quarry":      true,
	"karazhan_crypt":        true,
	"lower_blackrock_spire": true,
	"lower_karazhan_halls":  true,
	"maraudon_orange":       true,
	"maraudon_purple":       true,
	"molten_core":           true,
	"naxxramas":             true,
	"ragefire_chasm":        true,
	"scholomance":           true,
	"shadowfang_keep":       true,
	"stormwrought_ruins":    true,
	"stratholme_live":       true,
	"sunken_temple":         true,
	"timbermaw_hold":        true,
	"upper_karazhan_halls":  true,
	"wailing_caverns":       true,
	"windhorn_canyon":       true,
	"zul_gurub":             true,
}

// Every shipped plan whose map has navmesh files here passes the plan check, except the ones listed in planCheckSkip.
func TestShippedPlansPassPlanCheck(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(plansDir, "*.json"))
	var missing, failing []string
	checked := 0
	for _, f := range files {
		p, err := Load(f)
		if err != nil {
			t.Fatal(err)
		}
		dir := mapMmaps(p.Map)
		if dir == "" {
			missing = append(missing, p.Name)
			continue
		}
		pl := navmesh.NewPlanner(dir)
		res, err := CheckPlan(p, pl, 0)
		pl.Close()
		skipNoEngine(t, err)
		checked++
		switch {
		case res.OK() && planCheckSkip[p.Name]:
			t.Errorf("%s passes the plan check now: remove %s from planCheckSkip", p.Name, p.Name)
		case res.OK():
		case planCheckSkip[p.Name]:
			failing = append(failing, fmt.Sprintf("%s (%d of %d)", p.Name, len(res.Issues), res.Points))
		default:
			t.Errorf("%s: %d of %d points fail:\n%s", p.Name, len(res.Issues), res.Points, res.Summary())
		}
	}
	if checked == 0 {
		t.Skip("no navmesh files for any shipped plan's map here")
	}
	t.Logf("%d plans checked; known failing (planCheckSkip, #187): %s; no navmesh files for %s", checked, strings.Join(failing, ", "), strings.Join(missing, ", "))
}
