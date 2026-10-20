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
	words, err := r(226)
	if err != nil || words != "from step 207" {
		t.Fatalf("%q %v", words, err)
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
	words, err := observeStartFrom(p, 2, nil, 0, nil)(0)
	if err != nil || words != "from 1" {
		t.Fatalf("leg 2: %q %v", words, err)
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
	words, err := observeStartFrom(p, 2, &group, 0, nil)(rows.StepCount())
	if err != nil || words != "from step "+strconv.Itoa(groupOfLeg2) {
		t.Fatalf("leg 2 group 0: %q (want step %d) %v", words, groupOfLeg2, err)
	}
	if _, err := observeStartFrom(p, 2, &group, 0, nil)(rows.StepCount() + 3); err == nil || !strings.Contains(err.Error(), "observe.from_step") {
		t.Fatalf("a deployed plan of another row count: %v", err)
	}
	if _, err := observeStartFrom(p, 2, &group, 0, nil)(0); err == nil || !strings.Contains(err.Error(), "did not list the plan's step count") {
		t.Fatalf("no listed count: %v", err)
	}
}
