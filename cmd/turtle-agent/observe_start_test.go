package main

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// startPlan is three legs, the middle one optional: the run leader's leg numbers skip it.
const startPlan = `{"version":1,"name":"Start","status":"mapped (test)","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"first","to":{"x":10,"y":0,"z":0},"pull_groups":[
  {"entries":[100],"positions":[{"x":20,"y":0,"z":0}],"approach":[{"x":15,"y":0,"z":0}]},
  {"entries":[101],"positions":[{"x":25,"y":0,"z":0}]}]},
 {"name":"gong","to":{"x":0,"y":0,"z":0},"optional":{"item":9240,"name":"Mallet"},"steps":[{"use_object":{"entry":900,"near":{"x":2,"y":0,"z":0},"requires_item":{"entry":9240}}}],"boss":{"entry":7,"position":{"x":5,"y":0,"z":0}}},
 {"name":"third","to":{"x":30,"y":0,"z":0},"pull_groups":[{"entries":[102],"positions":[{"x":40,"y":0,"z":0}]}]}]}`

func startTestPlan(t *testing.T) *dungeon.Plan {
	t.Helper()
	p, err := dungeon.Parse("start.json", []byte(startPlan))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestObserveStartFromStep(t *testing.T) {
	p := startTestPlan(t)
	r := observeStartFrom(p, 0, nil, 207, nil)
	ps, err := r(226)
	if err != nil || ps.Words != "from step 207" || ps.Step != 207 {
		t.Fatalf("%+v %v", ps, err)
	}
	if _, err := r(206); err == nil || !strings.Contains(err.Error(), "206 steps") {
		t.Fatalf("a step past the end: %v", err)
	}
	if _, err := r(0); err != nil { // an older list without a count does not bound it
		t.Fatalf("no listed count: %v", err)
	}
	if observeStartFrom(p, 0, nil, 0, nil) != nil {
		t.Fatal("a run from the first step has a resolver")
	}
}

// from_leg starts at the leg's first step; the number is the run leader's (an optional leg takes none), and no step count is needed.
func TestObserveStartFromLeg(t *testing.T) {
	p := startTestPlan(t)
	ps, err := observeStartFrom(p, 2, nil, 0, nil)(0)
	if err != nil || ps.Words != "from leg third" || ps.LegName != "third" || ps.Leg != 2 || ps.LegNumber != 1 || !ps.HavePoint || ps.Point != (dungeon.Point{X: 30, Y: 0, Z: 0}) {
		t.Fatalf("leg 2: %+v %v", ps, err)
	}
	if observeStartFrom(p, 0, nil, 0, nil) != nil {
		t.Fatal("leg 0 is the first step: it has a resolver")
	}
	if _, err := observeStartFrom(p, 1, nil, 0, nil)(0); err == nil || !strings.Contains(err.Error(), "optional") {
		t.Fatalf("the optional leg: %v", err)
	}
	if _, err := observeStartFrom(p, 9, nil, 0, nil)(0); err == nil || !strings.Contains(err.Error(), "numbers 0-2") {
		t.Fatalf("a leg the plan does not have: %v", err)
	}
}

// from_pull_group maps to the step number of the group's first row, but only when the rows the agent rebuilds have the step count the
// server lists (the deployed rows may have been exported with other options); else it asks for from_step.
func TestObserveStartFromPullGroup(t *testing.T) {
	p := startTestPlan(t)
	res, err := planexport.Build(p, planexport.Options{Source: "test", GMCrossDirect: true, DropCompanionsLoot: true})
	if err != nil {
		t.Fatal(err)
	}
	rows := res.Rows
	groupOfLeg2, ok := rows.GroupStep(p, 2, 0)
	if !ok {
		t.Fatal("leg 2 group 0 has no step")
	}
	group := 0
	ps, err := observeStartFrom(p, 2, &group, 0, nil)(rows.StepCount())
	if err != nil || ps.Words != "from step "+strconv.Itoa(groupOfLeg2) || ps.Step != groupOfLeg2 || ps.ExpectLeg != "third" || !ps.HavePoint {
		t.Fatalf("leg 2 group 0: %+v (want step %d, leg third) %v", ps, groupOfLeg2, err)
	}
	if _, err := observeStartFrom(p, 2, &group, 0, nil)(rows.StepCount() + 3); err == nil || !strings.Contains(err.Error(), "observe.from_step") {
		t.Fatalf("a deployed plan of another row count: %v", err)
	}
	if _, err := observeStartFrom(p, 2, &group, 0, nil)(0); err == nil || !strings.Contains(err.Error(), "did not list the plan's step count") {
		t.Fatalf("no listed count: %v", err)
	}
}

// A leg name with an apostrophe cannot go on the command line: the number goes, and the start message's leg name is the only check then.
func TestObserveStartLegNameFallsBackToTheNumber(t *testing.T) {
	p := startTestPlan(t)
	p.Legs[2].Name = "high_priestess_mar'li"
	ps, err := observeStartFrom(p, 2, nil, 0, nil)(0)
	if err != nil || ps.Words != "from 1" || ps.LegName != "" || ps.LegNumber != 1 {
		t.Fatalf("%+v %v", ps, err)
	}
	// A duplicate name (case-insensitively) is not unique: the number goes too.
	p = startTestPlan(t)
	p.Legs[2].Name = "FIRST"
	ps, err = observeStartFrom(p, 2, nil, 0, nil)(0)
	if err != nil || ps.Words != "from 1" || ps.LegName != "" {
		t.Fatalf("duplicate name: %+v %v", ps, err)
	}
}

// A from_step start knows the plan leg of that step when the rows are reproduced: the start reply's leg is checked against it.
func TestObserveStartFromStepKnowsItsLeg(t *testing.T) {
	p := startTestPlan(t)
	res, err := planexport.Build(p, planexport.Options{Source: "test", GMCrossDirect: true, DropCompanionsLoot: true})
	if err != nil {
		t.Fatal(err)
	}
	n, ok := res.Rows.GroupStep(p, 2, 0)
	if !ok {
		t.Fatal("no group step")
	}
	ps, err := observeStartFrom(p, 0, nil, n, nil)(res.Rows.StepCount())
	if err != nil || ps.Step != n || ps.ExpectLeg != "third" || !ps.HavePoint {
		t.Fatalf("%+v %v", ps, err)
	}
	// Without the server's step count the point and the leg are unknown: the start still goes (the place check then asks for
	// allow_far_start).
	ps, err = observeStartFrom(p, 0, nil, n, nil)(0)
	if err != nil || ps.ExpectLeg != "" || ps.HavePoint {
		t.Fatalf("no count: %+v %v", ps, err)
	}
}

// farPlan has a 200 yd hop between its legs: with cut hops the second leg's FIRST ROW is a walk row, not the leg's own first point.
const farPlan = `{"version":1,"name":"Far","status":"mapped (test)","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","to":{"x":100,"y":0,"z":0},"pull_groups":[{"entries":[100],"positions":[{"x":120,"y":0,"z":0}]}]},
 {"name":"b","to":{"x":300,"y":0,"z":0},"pull_groups":[{"entries":[101],"positions":[{"x":320,"y":0,"z":0}]}]}]}`

// from_leg places at the leg's first ROW (the same point as from_step <that row>), not at the plan's first point of the leg.
func TestObserveStartFromLegUsesTheFirstRow(t *testing.T) {
	p, err := dungeon.Parse("far.json", []byte(farPlan))
	if err != nil {
		t.Fatal(err)
	}
	cut := func(_ uint32, from, to dungeon.Point) ([]dungeon.Point, error) {
		if to.X-from.X > 100 {
			return []dungeon.Point{{X: 150, Y: 5, Z: 0}, {X: 220, Y: 5, Z: 0}}, nil
		}
		return nil, nil
	}
	res, err := planexport.Build(p, planexport.Options{Source: "test", GMCrossDirect: true, DropCompanionsLoot: true, Cut: cut})
	if err != nil {
		t.Fatal(err)
	}
	rows := res.Rows
	// the first row of leg 1 is the cut walk row at x 150, not the plan's To (300)
	var first int
	for i, s := range rows.Steps {
		if s.Leg == 1 {
			first = i + 1
			break
		}
	}
	at, ok := legFirstRowIn([]planexport.Rows{rows}, rows.StepCount(), 1)
	want, _ := rows.StepPoint(first)
	if !ok || at != want || at.X != 150 {
		t.Fatalf("first row %+v ok %v, want %+v (step %d)", at, ok, want, first)
	}
	// a server count none of the option sets reproduce: unknown
	if _, ok := legFirstRowIn([]planexport.Rows{rows}, rows.StepCount()+1, 1); ok {
		t.Fatal("a point for rows that are not the server's")
	}
	if _, ok := legFirstRowIn([]planexport.Rows{rows}, 0, 1); ok {
		t.Fatal("a point without the server's count")
	}
	// two option sets with the same count that disagree on the row: unknown, not a guess
	other := planexport.Rows{Steps: slices.Clone(rows.Steps)}
	other.Legs = rows.Legs
	other.Steps[first-1].X += 30
	if _, ok := legFirstRowIn([]planexport.Rows{rows, other}, rows.StepCount(), 1); ok {
		t.Fatal("ambiguous option sets gave a point")
	}
}

// Through the resolver: with the server's step count the plan's rows reproduce, from_leg's point is the first row's and not marked;
// without it the plan's leg point goes, marked so that the Eluna placement refuses it.
func TestObserveStartFromLegPointSource(t *testing.T) {
	p := startTestPlan(t)
	res, err := planexport.Build(p, planexport.Options{Source: "test", GMCrossDirect: true, DropCompanionsLoot: true})
	if err != nil {
		t.Fatal(err)
	}
	ps, err := observeStartFrom(p, 2, nil, 0, nil)(res.Rows.StepCount())
	if err != nil || !ps.HavePoint || ps.PointFromPlan {
		t.Fatalf("with the count: %+v %v", ps, err)
	}
	for i, s := range res.Rows.Steps {
		if s.Leg == ps.LegNumber {
			if want, _ := res.Rows.StepPoint(i + 1); ps.Point != want {
				t.Fatalf("point %+v, first row %+v", ps.Point, want)
			}
			break
		}
	}
	ps, err = observeStartFrom(p, 2, nil, 0, nil)(0)
	if err != nil || !ps.HavePoint || !ps.PointFromPlan {
		t.Fatalf("without the count: %+v %v", ps, err)
	}
}
