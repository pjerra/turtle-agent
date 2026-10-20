package main

import (
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
	if err != nil || ps.Words != "from step "+strconv.Itoa(groupOfLeg2) || ps.Step != groupOfLeg2 || !ps.HavePoint {
		t.Fatalf("leg 2 group 0: %+v (want step %d) %v", ps, groupOfLeg2, err)
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
