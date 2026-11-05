package raidwalk

import (
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// cubPlan is one leg with one pull group of entry 51344 at the origin, whose plan patrol loops 10 yd around it.
func cubPlan() *dungeon.Plan {
	loop := []dungeon.Point{{X: 10}, {Y: 10}, {X: -10}, {Y: -10}}
	return &dungeon.Plan{Legs: []dungeon.Leg{{PullGroups: []dungeon.PullGroup{{
		Entries:   []uint32{51344},
		Positions: []dungeon.Point{{}},
		Patrol:    &dungeon.Patrol{Entries: []uint32{51344}, Path: loop, Source: "creature_movement 51344"},
	}}}}}
}

// cubRows is the run's rows: a move (step 1), the group's pull at the origin (step 2), a move on (step 3).
func cubRows() planexport.Rows {
	return planexport.Rows{Steps: []planexport.StepRow{
		{Kind: planexport.KindMove, X: -20},
		{Kind: planexport.KindPull, Entries: []uint32{51344}},
		{Kind: planexport.KindMove, X: -40},
	}}
}

// The revive point sits 1 yd off the loop, so the patrol's path blocks it whenever the patrol counts.
var cubRevive = dungeon.Point{X: 11}

func TestPlanPatrolLivePackBlocksItsPath(t *testing.T) {
	patrols := PlanQuietPatrols(cubPlan())
	if len(patrols) != 1 || patrols[0].Pack == nil {
		t.Fatalf("patrols %+v, want one patrol tied to its pack", patrols)
	}
	step := PullStepOf(cubRows(), *patrols[0].Pack)
	if step != 2 {
		t.Fatalf("pull step %d, want 2", step)
	}
	// Not pulled yet, the run still before the pull, units at full health: live.
	if PatrolPackDone(step, false, 1, []int{100, 100}) {
		t.Fatal("a pack not yet pulled is done")
	}
	// Pulled, but the run is still at the pull step: the pack may stand.
	if PatrolPackDone(step, true, step, []int{100}) {
		t.Fatal("a pack the run has not moved past is done")
	}
	ok, reason := QuietAt(nil, patrols, cubRevive, NewQuietIdleReads(), nil, quietNow)
	if ok || !strings.Contains(reason, "creature_movement 51344") || !strings.Contains(reason, "passes") {
		t.Fatalf("quiet %v, reason %q: the live pack's path must block", ok, reason)
	}
}

func TestPlanPatrolPulledAndKilledDoesNotBlock(t *testing.T) {
	patrols := PlanQuietPatrols(cubPlan())
	step := PullStepOf(cubRows(), *patrols[0].Pack)
	// The run pulled the pack at step 2 and is at step 3: the pack is done.
	if !PatrolPackDone(step, true, 3, nil) {
		t.Fatal("a pulled pack the run is past is not done")
	}
	// Frames saw every unit of the pack dead, with no pull in this run: done too.
	if !PatrolPackDone(step, false, 1, []int{0, 0}) {
		t.Fatal("a pack whose every seen unit is dead is not done")
	}
	// One unit still alive in the frames: not done.
	if PatrolPackDone(step, false, 1, []int{0, 40}) {
		t.Fatal("a pack with a live unit is done")
	}
	// No frames saw the pack and no pull: no evidence, not done.
	if PatrolPackDone(step, false, 1, nil) {
		t.Fatal("a pack with no evidence is done")
	}

	// The caller's exemption (applyPlanPatrolState) lifts the path block.
	patrols[0].Exempt = true
	if ok, reason := QuietAt(nil, patrols, cubRevive, NewQuietIdleReads(), nil, quietNow); !ok {
		t.Fatalf("an exempt done pack still blocks: %q", reason)
	}
}

func TestPlanPatrolUnresolvedSaysSoInTheReason(t *testing.T) {
	// Two pull groups walk the same path: no one pack stands for it, so it has no pack.
	plan := cubPlan()
	g := plan.Legs[0].PullGroups[0]
	g.Entries = []uint32{51345}
	plan.Legs[0].PullGroups = append(plan.Legs[0].PullGroups, g)
	patrols := PlanQuietPatrols(plan)
	if len(patrols) != 1 || patrols[0].Pack != nil {
		t.Fatalf("patrols %+v, want one patrol with no pack", patrols)
	}
	patrols[0].Note = "its pull group cannot be tied to its units: blocks by path"
	ok, reason := QuietAt(nil, patrols, cubRevive, NewQuietIdleReads(), nil, quietNow)
	if ok || !strings.Contains(reason, "(its pull group cannot be tied to its units") {
		t.Fatalf("quiet %v, reason %q: the unresolved patrol must still block and say so", ok, reason)
	}
}

func TestPullStepOfNeedsOneMatchingRow(t *testing.T) {
	pack := *PlanQuietPatrols(cubPlan())[0].Pack
	rows := cubRows()
	if got := PullStepOf(rows, pack); got != 2 {
		t.Fatalf("step %d, want 2", got)
	}
	// No pull row for the pack: 0.
	if got := PullStepOf(planexport.Rows{Steps: rows.Steps[:1]}, pack); got != 0 {
		t.Fatalf("step %d with no pull row, want 0", got)
	}
	// Two pull rows of the pack at its centre: ambiguous, 0.
	dup := planexport.Rows{Steps: append(append([]planexport.StepRow{}, rows.Steps...), rows.Steps[1])}
	if got := PullStepOf(dup, pack); got != 0 {
		t.Fatalf("step %d with two matching rows, want 0", got)
	}
	// A pull row a long way off the centre is another pack's.
	far := planexport.Rows{Steps: []planexport.StepRow{{Kind: planexport.KindPull, X: 50, Entries: []uint32{51344}}}}
	if got := PullStepOf(far, pack); got != 0 {
		t.Fatalf("step %d for a row 50 yd off, want 0", got)
	}
}
