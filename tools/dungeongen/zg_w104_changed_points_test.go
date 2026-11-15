// The walk-104 Zul'Gurub route fixes (T25 Arlokk ledge, T28 Hakkar outer lake, T29 Jin'do hall, T31 Venoxis camp,
// T34 Mandokir/Vilebranch Speaker, T35 step 36's bats) all moved or added plan points, and every one of them was
// authored from a walked frame: this test holds that line. Against origin/dev's plan (testdata/zg_walk104_dev_plan.json,
// the plan the six branches started from) it takes every point that is new or moved, and asks the server's own navmesh
// about each one: the nearest floor is within planlint's off-floor rule, no liquid stands at it or above it, and where
// the exporter writes a row for it, the hop from the row before it is a NORMAL (0x1) ground path that ends on it. A
// point moved onto a ledge, into the lake or over a wall fails those checks.
package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/planlint"
)

// zgW104DevPlan is origin/dev's dungeons/zul_gurub.json, the six route fixes' common base.
const zgW104DevPlan = "testdata/zg_walk104_dev_plan.json"

// zgW104MinChangedPoints is how many points the six fixes move or add: a lower count means the comparison stopped
// seeing them (a stale fixture, or a plan that lost a fix), which must fail rather than pass quietly.
const zgW104MinChangedPoints = 60

// zgW104Point is one plan point: Key names where it sits (leg and element), Pt its place in the world.
type zgW104Point struct {
	Key string
	Pt  dungeon.Point
}

// zgW104Points lists every point a plan authors: the leg's waypoints and `to`, its pull groups' positions and approach
// points, the boss, and the step elements that stand somewhere.
func zgW104Points(p *dungeon.Plan) []zgW104Point {
	var out []zgW104Point
	put := func(key string, q dungeon.Point) { out = append(out, zgW104Point{Key: key, Pt: q}) }
	steps := func(listName string, leg int, list []dungeon.Step) {
		for si, s := range list {
			at := fmt.Sprintf("leg[%d].%s[%d]", leg, listName, si)
			if s.UseObject != nil && s.UseObject.Stand != nil {
				put(at+".use_object.stand", *s.UseObject.Stand)
			}
			if s.Ritual != nil {
				put(at+".ritual.position", s.Ritual.Position)
				if s.Ritual.Stand != nil {
					put(at+".ritual.stand", *s.Ritual.Stand)
				}
			}
			if s.Gossip != nil {
				put(at+".gossip.near", s.Gossip.Near)
				if s.Gossip.Stand != nil {
					put(at+".gossip.stand", *s.Gossip.Stand)
				}
			}
			if s.AreaTrigger != nil {
				put(at+".area_trigger.center", s.AreaTrigger.Center)
				put(at+".area_trigger.stand", s.AreaTrigger.Stand)
			}
		}
	}
	for li, leg := range p.Legs {
		for vi, v := range leg.Via {
			put(fmt.Sprintf("leg[%d].via[%d]", li, vi), v.Point)
		}
		put(fmt.Sprintf("leg[%d].to", li), leg.To)
		for gi, g := range leg.PullGroups {
			for pi, q := range g.Positions {
				put(fmt.Sprintf("leg[%d].pull_groups[%d].positions[%d]", li, gi, pi), q)
			}
			for ai, q := range g.Approach {
				put(fmt.Sprintf("leg[%d].pull_groups[%d].approach[%d]", li, gi, ai), q)
			}
		}
		if leg.Boss != nil {
			put(fmt.Sprintf("leg[%d].boss", li), leg.Boss.Position)
		}
		steps("steps", li, leg.Steps)
		steps("after_boss", li, leg.AfterBoss)
	}
	return out
}

// zgW104SamePoint says two points are the same place.
func zgW104SamePoint(a, b dungeon.Point) bool {
	const tol = 0.01
	return math.Abs(float64(a.X-b.X)) <= tol && math.Abs(float64(a.Y-b.Y)) <= tol && math.Abs(float64(a.Z-b.Z)) <= tol
}

// zgW104Dist3 is the 3D distance between two plan points, in yards.
func zgW104Dist3(a, b dungeon.Point) float64 {
	dx, dy, dz := float64(a.X-b.X), float64(a.Y-b.Y), float64(a.Z-b.Z)
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

// zgW104Planner is the server's map-309 navmesh: $TURTLE_AGENT_MMAPS (CI's single folder), the test VM's
// ~/tb-ci/mmaps-turtle309, the server's data/mmaps or ~/turtle-agent-mmaps, as the other real navmesh checks look; the
// test skips without it.
func zgW104Planner(t *testing.T) *navmesh.Planner {
	t.Helper()
	home, _ := os.UserHomeDir()
	for _, dir := range []string{os.Getenv("TURTLE_AGENT_MMAPS"), filepath.Join(home, "tb-ci", "mmaps-turtle309"),
		filepath.Join(home, "tortoise-wow-server", "data", "mmaps"), filepath.Join(home, "turtle-agent-mmaps")} {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "309.mmap")); err != nil {
			continue
		}
		if tiles, _ := filepath.Glob(filepath.Join(dir, "309*.mmtile")); len(tiles) == 0 {
			continue
		}
		p := navmesh.NewPlanner(dir)
		t.Cleanup(p.Close)
		return p
	}
	t.Skip("no map 309 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 309.mmap and its tiles")
	return nil
}

// TestZulGurubWalk104ChangedPointsAreOnTheFloor: every point the six route fixes moved or added stands on the walked
// floor, dry, and the hop from the row before its own row is a NORMAL (0x1) ground path.
func TestZulGurubWalk104ChangedPointsAreOnTheFloor(t *testing.T) {
	pl := zgW104Planner(t)
	p, err := dungeon.Load(filepath.Join(plansDirT, "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	dev, err := dungeon.Load(zgW104DevPlan)
	if err != nil {
		t.Fatalf("%s: %v", zgW104DevPlan, err)
	}
	if dev.Name != p.Name {
		t.Fatalf("%s names the plan %q, want %q", zgW104DevPlan, dev.Name, p.Name)
	}
	before := make(map[string]dungeon.Point)
	for _, q := range zgW104Points(dev) {
		before[q.Key] = q.Pt
	}
	var changed []zgW104Point
	for _, q := range zgW104Points(p) {
		if old, ok := before[q.Key]; !ok || !zgW104SamePoint(old, q.Pt) {
			changed = append(changed, q)
		}
	}
	if len(changed) < zgW104MinChangedPoints {
		t.Fatalf("%d points changed against origin/dev, want at least %d: %s went stale or the fixes are not all in",
			len(changed), zgW104MinChangedPoints, zgW104DevPlan)
	}

	// The rows the exporter writes for the merged plan: a changed point with a row is walked to from the row before it,
	// which is the hop the run leader takes.
	res, err := planexport.Build(p, planexport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	rowOf := func(q dungeon.Point) int {
		for i, s := range res.Rows.Steps {
			if zgW104SamePoint(dungeon.Point{X: s.X, Y: s.Y, Z: s.Z}, q) {
				return i
			}
		}
		return -1
	}

	checked, hopped := 0, 0
	for _, c := range changed {
		pt := navmesh.Vec3{c.Pt.X, c.Pt.Y, c.Pt.Z}
		floor, ok, err := pl.Nearest(p.Map, pt)
		if err != nil {
			t.Fatalf("%s: %v", c.Key, err)
		}
		if !ok {
			t.Errorf("%s %.1f %.1f %.1f: no walkable polygon within 4 yd", c.Key, c.Pt.X, c.Pt.Y, c.Pt.Z)
			continue
		}
		if d := zgW104Dist3(c.Pt, dungeon.Point{X: floor[0], Y: floor[1], Z: floor[2]}); d > planlint.OffFloorYd {
			t.Errorf("%s %.1f %.1f %.1f: the nearest floor %.1f %.1f %.1f is %.1f yd away (planlint's rule is %g yd)",
				c.Key, c.Pt.X, c.Pt.Y, c.Pt.Z, floor[0], floor[1], floor[2], d, planlint.OffFloorYd)
			continue
		}
		liq, err := navmesh.LiquidAt(pl, p.Map, pt)
		if err != nil {
			t.Fatalf("%s: %v", c.Key, err)
		}
		if liq.Wet {
			what := "its own floor is NavWater"
			if !liq.Own {
				what = fmt.Sprintf("a NavWater floor stands %.1f yd above it within %g yd", liq.Z-c.Pt.Z, navmesh.LiquidNearYd)
			}
			t.Errorf("%s %.1f %.1f %.1f: in or under water (%s)", c.Key, c.Pt.X, c.Pt.Y, c.Pt.Z, what)
			continue
		}
		checked++

		i := rowOf(c.Pt)
		if i < 1 {
			continue // a pack's own position, or a point the export does not walk to: the floor check is its check
		}
		prev := res.Rows.Steps[i-1]
		sp, err := pl.ServerPath(p.Map, navmesh.Vec3{prev.X, prev.Y, prev.Z}, pt)
		if err != nil {
			t.Fatalf("%s: %v", c.Key, err)
		}
		if sp.Type&navmesh.PathNormal == 0 {
			t.Errorf("%s row %d (%.1f %.1f %.1f): the server's path from row %d (%.1f %.1f %.1f) is type 0x%x, want NORMAL (0x1)",
				c.Key, i+1, c.Pt.X, c.Pt.Y, c.Pt.Z, i, prev.X, prev.Y, prev.Z, sp.Type)
			continue
		}
		if d := zgW104Dist3(c.Pt, dungeon.Point{X: sp.ActualEnd[0], Y: sp.ActualEnd[1], Z: sp.ActualEnd[2]}); d > 1.0 {
			t.Errorf("%s row %d: the path ends %.1f yd from the point (%.1f %.1f %.1f), want at most 1 yd", c.Key, i+1, d,
				sp.ActualEnd[0], sp.ActualEnd[1], sp.ActualEnd[2])
			continue
		}
		hopped++
	}
	if checked == 0 {
		t.Fatalf("%d changed points, none checked", len(changed))
	}
	if hopped < 20 {
		t.Errorf("%d changed points reached by a row, want at least 20 (are the fixes' rows in the export?)", hopped)
	}
	t.Logf("%d points changed against origin/dev: %d on the floor and dry, %d with a NORMAL path from the row before them",
		len(changed), checked, hopped)
}
