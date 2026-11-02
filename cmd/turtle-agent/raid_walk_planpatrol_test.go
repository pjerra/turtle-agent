package main

import (
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// planPatrolPlan is one pull group of entry 51344 at the origin with a plan patrol looping 10 yd around it.
func planPatrolPlan() *dungeon.Plan {
	loop := []dungeon.Point{{X: 10}, {Y: 10}, {X: -10}, {Y: -10}}
	return &dungeon.Plan{Legs: []dungeon.Leg{{PullGroups: []dungeon.PullGroup{{
		Entries:   []uint32{51344},
		Positions: []dungeon.Point{{}},
		Patrol:    &dungeon.Patrol{Entries: []uint32{51344}, Path: loop, Source: "creature_movement 51344"},
	}}}}}
}

// planPatrolRows: a move (step 1), the group's pull (step 2), a move on (step 3).
func planPatrolRows() planexport.Rows {
	return planexport.Rows{Steps: []planexport.StepRow{
		{Kind: planexport.KindMove, X: -20},
		{Kind: planexport.KindPull, Entries: []uint32{51344}},
		{Kind: planexport.KindMove, X: -40},
	}}
}

// planPatrolWalk is a walk at the run's step (0: no status yet) with the frames' hostiles and pulled steps given.
func planPatrolWalk(plan *dungeon.Plan, rows planexport.Rows, step int, pulled []int, hostiles map[string]hostileState) *walk {
	w := &walk{rows: rows, hostiles: hostiles, pulledSteps: map[int]bool{}}
	w.quietPatrols = nil
	if step > 0 {
		w.lastStatus, w.haveStatus = tbm.RunStatus{Plan: plan.Name, Step: step}, true
	}
	for _, s := range pulled {
		w.pulledSteps[s] = true
	}
	return w
}

// livePatrol: one 51344 unit at full health in the frames.
var livePatrol = map[string]hostileState{"a": {entry: 51344, hppct: 100}}

// deadPatrol: the same unit seen dead.
var deadPatrol = map[string]hostileState{"a": {entry: 51344, hppct: 0}}

func onlyPlanPatrol(t *testing.T, w *walk, plan *dungeon.Plan) []raidwalk.QuietPatrol {
	t.Helper()
	patrols := raidwalk.PlanQuietPatrols(plan)
	if len(patrols) != 1 {
		t.Fatalf("patrols %+v, want one plan patrol", patrols)
	}
	w.applyPlanPatrolState(patrols)
	return patrols
}

func TestPlanPatrolLivePackStillBlocks(t *testing.T) {
	plan := planPatrolPlan()
	w := planPatrolWalk(plan, planPatrolRows(), 1, nil, livePatrol)
	p := onlyPlanPatrol(t, w, plan)[0]
	if p.Exempt || p.Note != "" {
		t.Fatalf("live pack: exempt %v note %q, want a plain blocker", p.Exempt, p.Note)
	}
}

func TestPlanPatrolPulledAndKilledDoesNotBlock(t *testing.T) {
	plan := planPatrolPlan()
	// Pulled at step 2, the run now at step 3, the unit dead.
	w := planPatrolWalk(plan, planPatrolRows(), 3, []int{2}, deadPatrol)
	p := onlyPlanPatrol(t, w, plan)[0]
	if !p.Exempt {
		t.Fatal("a pulled and killed pack still blocks the revive point")
	}
	ok, reason := raidwalk.QuietAt(nil, []raidwalk.QuietPatrol{p}, dungeon.Point{X: 11}, raidwalk.NewQuietIdleReads(), nil, time.Time{})
	if !ok {
		t.Fatalf("revive point not quiet with the pack done: %q", reason)
	}
}

func TestPlanPatrolDeadByFramesDoesNotBlock(t *testing.T) {
	plan := planPatrolPlan()
	// Never pulled in this run, but the frames saw its only unit dead.
	w := planPatrolWalk(plan, planPatrolRows(), 1, nil, deadPatrol)
	if p := onlyPlanPatrol(t, w, plan)[0]; !p.Exempt {
		t.Fatal("a pack the frames saw all dead still blocks")
	}
}

func TestPlanPatrolPulledButRunNotPastStillBlocks(t *testing.T) {
	plan := planPatrolPlan()
	// Pulled at step 2, but the run is still on it and the unit is alive: the pack may stand.
	w := planPatrolWalk(plan, planPatrolRows(), 2, []int{2}, livePatrol)
	if p := onlyPlanPatrol(t, w, plan)[0]; p.Exempt {
		t.Fatal("a pack the run has not moved past is exempt")
	}
}

func TestPlanPatrolUnresolvedRowKeepsBlockingAndSaysSo(t *testing.T) {
	plan := planPatrolPlan()
	// The rows have no pull row for the group (a cut plan): the pulled route is closed, and the unit is alive.
	rows := planexport.Rows{Steps: []planexport.StepRow{{Kind: planexport.KindMove, X: -20}}}
	w := planPatrolWalk(plan, rows, 3, []int{2}, livePatrol)
	p := onlyPlanPatrol(t, w, plan)[0]
	if p.Exempt || !strings.Contains(p.Note, "not in the plan's rows") {
		t.Fatalf("exempt %v note %q: an unresolved pull row must keep blocking and say so", p.Exempt, p.Note)
	}
}

func TestPlanPatrolWithoutOneGroupKeepsBlockingAndSaysSo(t *testing.T) {
	plan := planPatrolPlan()
	// A second pull group walks the same path: no one pack stands for it.
	g := plan.Legs[0].PullGroups[0]
	g.Entries = []uint32{51345}
	plan.Legs[0].PullGroups = append(plan.Legs[0].PullGroups, g)
	w := planPatrolWalk(plan, planPatrolRows(), 3, []int{2}, deadPatrol)
	p := onlyPlanPatrol(t, w, plan)[0]
	if p.Exempt || !strings.Contains(p.Note, "cannot be tied to its units") {
		t.Fatalf("exempt %v note %q: a patrol with no pack must keep blocking and say so", p.Exempt, p.Note)
	}
}
