package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

// routeNav is a flat mesh (testNav) whose paths bend through a corner, so that a path is not the straight line; block names the goals it cannot
// path. plans counts the Plan calls: a cached route asks the planner nothing.
type routeNav struct {
	testNav
	plans *atomic.Int64
	block func(to navmesh.Vec3) bool
}

func (n routeNav) Plan(_ uint32, from, to navmesh.Vec3) (navmesh.Path, error) {
	n.plans.Add(1)
	if n.block != nil && n.block(to) {
		return navmesh.Path{Status: navmesh.NoPath, Reason: "Detour found no path", Points: []navmesh.Vec3{}}, nil
	}
	mid := navmesh.Vec3{(from[0] + to[0]) / 2, (from[1]+to[1])/2 + 3, 5}
	return navmesh.Path{Status: navmesh.Complete, Points: []navmesh.Vec3{from, mid, to}, Length: 1.2 * hypot3(from, to)}, nil
}

func hypot3(a, b navmesh.Vec3) float32 {
	dx, dy := float64(a[0]-b[0]), float64(a[1]-b[1])
	return float32(sqrt(dx*dx + dy*dy))
}

func sqrt(v float64) float64 {
	z := v
	if z == 0 {
		return 0
	}
	for i := 0; i < 40; i++ {
		z = (z + v/z) / 2
	}
	return z
}

func getRoute(t *testing.T, r *editRig, path string) (int, RouteDoc, string) {
	t.Helper()
	got := r.do(http.MethodGet, path, nil, nil)
	var doc RouteDoc
	if got.code == 200 {
		if err := json.Unmarshal([]byte(got.body), &doc); err != nil {
			t.Fatalf("%s: not a route: %v\n%s", path, err, got.body)
		}
	}
	return got.code, doc, got.body
}

func TestRouteIsTracedOncePerPlanVersion(t *testing.T) {
	var plans atomic.Int64
	r := newEditRig(t, routeNav{plans: &plans})
	rs := r.p.route
	if rs == nil {
		t.Fatal("the editor's navmesh is not the route's")
	}
	code, doc, body := getRoute(t, r, "/api/plan/edit1/route")
	if code != 200 || len(doc.Segs) == 0 || doc.SHA256 != r.base || doc.Mmaps != "/mmaps/test" {
		t.Fatalf("first request: %d sha %s mmaps %q\n%.300s", code, doc.SHA256, doc.Mmaps, body)
	}
	after := plans.Load()
	if after == 0 || rs.computes.Load() != 1 {
		t.Fatalf("the first request traced %d times with %d planner calls", rs.computes.Load(), after)
	}
	// the same version again, and the editor's route with no ops, are lookups: no trace, no planner call
	for _, path := range []string{"/api/plan/edit1/route", "/api/plan/edit1/route", "/api/edit/edit1/route"} {
		if code, _, _ := getRoute(t, r, path); code != 200 {
			t.Fatalf("%s: %d", path, code)
		}
	}
	if rs.computes.Load() != 1 || plans.Load() != after {
		t.Errorf("a cached route was traced again: %d traces, %d planner calls (was %d)", rs.computes.Load(), plans.Load(), after)
	}
	// a new version of the plan file is a new trace
	b, _ := os.ReadFile(filepath.Join(r.src, "edit1.json"))
	must(t, os.WriteFile(filepath.Join(r.src, "edit1.json"), []byte(strings.Replace(string(b), `"x":20,`, `"x":25,`, 1)), 0o644))
	_, doc2, _ := getRoute(t, r, "/api/plan/edit1/route")
	if rs.computes.Load() != 2 || doc2.SHA256 == doc.SHA256 {
		t.Errorf("the changed file: %d traces, sha %s (was %s)", rs.computes.Load(), doc2.SHA256, doc.SHA256)
	}
	// another navmesh folder is another trace of the same plan
	rs.mmaps = "/mmaps/other"
	_, doc3, _ := getRoute(t, r, "/api/plan/edit1/route")
	if rs.computes.Load() != 3 || doc3.Mmaps != "/mmaps/other" {
		t.Errorf("another mmaps folder: %d traces, mmaps %q", rs.computes.Load(), doc3.Mmaps)
	}
}

func TestRouteRequestsAtTheSameTimeShareOneTrace(t *testing.T) {
	var plans atomic.Int64
	r := newEditRig(t, routeNav{plans: &plans})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := r.do(http.MethodGet, "/api/plan/edit1/route", nil, nil); got.code != 200 {
				t.Errorf("%d", got.code)
			}
		}()
	}
	wg.Wait()
	if n := r.p.route.computes.Load(); n != 1 {
		t.Errorf("%d traces for 8 requests of one plan", n)
	}
}

func TestRouteFlagsASegmentThePlannerCannotPath(t *testing.T) {
	var plans atomic.Int64
	// the second via point of leg one: the planner has no path to it
	r := newEditRig(t, routeNav{plans: &plans, block: func(to navmesh.Vec3) bool { return to[0] == 40 && to[1] == 0 }})
	code, doc, body := getRoute(t, r, "/api/plan/edit1/route")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var red []RouteSegView
	for _, sg := range doc.Segs {
		if !sg.OK {
			red = append(red, sg)
		}
	}
	if len(red) != 1 || doc.Summary.Failed != 1 {
		t.Fatalf("%d failed segments (summary %d), want 1: %+v", len(red), doc.Summary.Failed, red)
	}
	sg := red[0]
	if sg.Part != "via 1" || sg.Reason != "unreachable" || len(sg.Pts) != 0 || !strings.Contains(sg.Text, "CANNOT BE PATHED") || !strings.Contains(sg.Detail, "Detour found no path") {
		t.Errorf("the failed segment: %+v", sg)
	}
	// it keeps its two ends so that the page can draw the chord; the end is the via point (40, 0) in the drawing's plane
	if sg.B.X != 0 || sg.B.Y != -40 {
		t.Errorf("the chord ends at %+v, want the via point (0, -40) of the drawing", sg.B)
	}
	// the other segments are drawn on the navmesh: more than the two ends
	for _, s := range doc.Segs {
		if s.OK && s.Kind == "via" && len(s.Pts) < 3 {
			t.Errorf("segment %q has no navmesh path: %+v", s.Part, s.Pts)
		}
	}
}

func TestRouteHasTheWalkBetweenLegsAndNumbersTheStops(t *testing.T) {
	var plans atomic.Int64
	r := newEditRig(t, routeNav{plans: &plans})
	_, doc, _ := getRoute(t, r, "/api/plan/edit1/route")
	var kinds []string
	for i, st := range doc.Stops {
		if st.Seq != i+1 {
			t.Errorf("stop %d is numbered %d", i, st.Seq)
		}
		kinds = append(kinds, st.Kind)
	}
	if got, want := strings.Join(kinds, " "), "via via via stage pack stage boss"; got != want {
		t.Fatalf("stops %q, want %q", got, want)
	}
	if l := doc.Stops[0].Label; l != "walk to waypoint 1 of One" {
		t.Errorf("label %q", l)
	}
	if l := doc.Stops[4].Label; !strings.HasPrefix(l, "pull pack 1:") {
		t.Errorf("label %q", l)
	}
	if l := doc.Stops[6].Label; l != "boss 6001 of Two" {
		t.Errorf("label %q", l)
	}
	// the second leg's first walk starts where the first leg's last walk ended
	var last0 RouteSegView
	var first1 *RouteSegView
	for i, sg := range doc.Segs {
		if sg.Leg == 0 {
			last0 = sg
		}
		if sg.Leg == 1 && first1 == nil {
			first1 = &doc.Segs[i]
		}
	}
	if first1 == nil || !first1.Link || first1.Stop != 6 || first1.A != last0.B || len(first1.Pts) < 3 {
		t.Fatalf("no walk between the legs: %+v after %+v", first1, last0)
	}
	if doc.Summary.Links != 2 || doc.Summary.Stops != 7 || doc.Summary.Segments != len(doc.Segs) || doc.Summary.Length <= 0 {
		t.Errorf("summary %+v", doc.Summary)
	}
	for _, sg := range doc.Segs {
		if sg.Kind == "direct" && (len(sg.Pts) != 2 || !strings.Contains(sg.Text, "direct step")) {
			t.Errorf("the direct hop is a straight line: %+v", sg)
		}
	}
}

func TestRouteNeedsANavmeshAndOnlyReads(t *testing.T) {
	p := NewPoller(Options{ConfigDir: t.TempDir(), PlansDirs: []string{t.TempDir()}})
	srv := httptest.NewServer(Handler(p, "", nil))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/plan/edit1/route")
	must(t, err)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("no navmesh: %d", resp.StatusCode)
	}
	var plans atomic.Int64
	r := newEditRig(t, routeNav{plans: &plans})
	for _, path := range []string{"/api/plan/edit1/route", "/api/edit/edit1/route"} {
		got := r.do(http.MethodPost, path, []byte(`{}`), map[string]string{"Content-Type": "application/json", "Authorization": "Bearer " + editToken})
		if path == "/api/plan/edit1/route" && got.code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s: %d", path, got.code)
		}
		if path == "/api/edit/edit1/route" && got.code != http.StatusNotFound { // the editor's POST paths are the plan names only
			t.Errorf("POST %s: %d %s", path, got.code, got.body)
		}
	}
	if code, _, _ := getRoute(t, r, "/api/plan/nosuch/route"); code != http.StatusNotFound {
		t.Errorf("an unknown plan: %d", code)
	}
}

func TestRouteOfAnEditUsesTheOpsAndWritesNothing(t *testing.T) {
	var plans atomic.Int64
	r := newEditRig(t, routeNav{plans: &plans})
	_, base, _ := getRoute(t, r, "/api/plan/edit1/route")
	before := tree(t, r.edits) + "|" + tree(t, r.src)
	ops, _ := json.Marshal([]map[string]any{move(0, 0, 21, 8)})
	path := "/api/edit/edit1/route?ops=" + url.QueryEscape(string(ops))
	code, doc, body := getRoute(t, r, path)
	if code != 200 || !doc.Edited || doc.SHA256 == base.SHA256 {
		t.Fatalf("%d edited %v sha %s (file %s)\n%.300s", code, doc.Edited, doc.SHA256, base.SHA256, body)
	}
	if doc.Stops[0].X == base.Stops[0].X && doc.Stops[0].Y == base.Stops[0].Y {
		t.Errorf("the moved via point is where it was: %+v", doc.Stops[0])
	}
	// the stop moved by the ops, in the drawing's plane: game (21, 8) is drawing (-8, -21)
	if doc.Stops[0].X != -8 || doc.Stops[0].Y != -21 {
		t.Errorf("stop 1 at %v,%v, want -8,-21", doc.Stops[0].X, doc.Stops[0].Y)
	}
	n := r.p.route.computes.Load()
	if code, _, _ := getRoute(t, r, path); code != 200 || r.p.route.computes.Load() != n {
		t.Errorf("the same edit again: %d, %d traces (was %d)", code, r.p.route.computes.Load(), n)
	}
	if after := tree(t, r.edits) + "|" + tree(t, r.src); after != before {
		t.Errorf("a route request wrote: %s -> %s", before, after)
	}
	for name, q := range map[string]string{"not JSON": "ops=" + url.QueryEscape("{"), "a point off the plan": "ops=" + url.QueryEscape(`[{"leg":0,"kind":"move","index":0,"x":99999,"y":0}]`), "no such leg": "ops=" + url.QueryEscape(`[{"leg":9,"kind":"move","index":0,"x":1,"y":1}]`)} {
		if code, _, body := getRoute(t, r, "/api/edit/edit1/route?"+q); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
}
