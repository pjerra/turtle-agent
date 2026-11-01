package raidwalk

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// placeRow is one creature step row of a place fixture: its point, the pull's radius and its entries.
func placeRow(kind string, leg int, x, y, z, radius float32, entries ...uint32) planexport.StepRow {
	return planexport.StepRow{Kind: kind, Leg: leg, X: x, Y: y, Z: z, Radius: radius, Entries: entries}
}

// placeMove is a positioned move row.
func placeMove(leg int, x, y, z float32) planexport.StepRow {
	return planexport.StepRow{Kind: planexport.KindMove, Leg: leg, X: x, Y: y, Z: z}
}

// placeRows numbers the rows of a fixture and wraps them in a plan named t (map 309).
func placeRows(legs []planexport.LegRow, steps ...planexport.StepRow) planexport.Rows {
	for i := range steps {
		steps[i].Seq = i + 1
		steps[i].Plan = "t"
	}
	return planexport.Rows{Plan: planexport.PlanRow{Plan: "t", Map: 309}, Legs: legs, Steps: steps}
}

// placeRoute is n move rows along +y from (0,10,0), the corridor's route. No point of a fixture may sit at the origin:
// the rows treat (0,0,0) as a row without a position (planexport's own rule).
func placeRoute(leg, n int) []planexport.StepRow {
	out := make([]planexport.StepRow, 0, n)
	for i := range n {
		out = append(out, placeMove(leg, 0, float32(10+i*10), 0))
	}
	return out
}

func TestPlaceStartStagesBeforeFirstPull(t *testing.T) {
	// leg 0: steps 1-9; leg 1: moves 10-14 at (0,40)..(0,100), then its first pull at (0,140): 40 yd from step 14 and
	// 55 yd from step 13, so the staged start is 13 and not 14.
	steps := []planexport.StepRow{placeMove(0, 0, -200, 0)}
	for i := range 8 {
		steps = append(steps, placeMove(0, 0, float32(-180+i*10), 0))
	}
	for _, y := range []float32{40, 55, 70, 85, 100} {
		steps = append(steps, placeMove(1, 0, y, 0))
	}
	steps = append(steps, placeRow(planexport.KindPull, 1, 0, 140, 0, 0, 15111))
	rows := placeRows([]planexport.LegRow{{Plan: "t", Leg: 0, Name: "one"}, {Plan: "t", Leg: 1, Name: "two"}}, steps...)
	if len(rows.Steps) != 15 {
		t.Fatalf("fixture has %d steps", len(rows.Steps))
	}
	got, leg, err := PlaceStart(rows, []string{"from", "leg", "two"})
	if err != nil {
		t.Fatal(err)
	}
	if got != 13 || leg != "two" {
		t.Fatalf("start %d (%q), want 13 (two)", got, leg)
	}
	// the leg number finds the same leg
	if got, _, err := PlaceStart(rows, []string{"from", "leg", "1"}); err != nil || got != 13 {
		t.Fatalf("by number: %d, %v", got, err)
	}
}

func TestPlaceStartRefusesNoQuietMove(t *testing.T) {
	rows := placeRows([]planexport.LegRow{{Plan: "t", Leg: 0, Name: "one"}},
		placeMove(0, 0, 100, 0),
		placeMove(0, 0, 105, 0),
		placeRow(planexport.KindPull, 0, 0, 110, 0, 0, 15111),
	)
	_, _, err := PlaceStart(rows, []string{"from", "leg", "one"})
	if err == nil || !strings.Contains(err.Error(), "no quiet move row") {
		t.Fatalf("err %v", err)
	}
}

func TestPlaceStartRefusesPullRow(t *testing.T) {
	rows := placeRows(nil,
		placeMove(0, 0, 100, 0),
		placeRow(planexport.KindPull, 0, 0, 110, 0, 0, 15111),
		placeRow(planexport.KindBoss, 0, 0, 120, 0, 0, 11347),
	)
	if _, _, err := PlaceStart(rows, []string{"from", "step", "2"}); err == nil || !strings.Contains(err.Error(), "is a pull row") {
		t.Fatalf("pull row: %v", err)
	}
	if _, _, err := PlaceStart(rows, []string{"from", "step", "3"}); err == nil || !strings.Contains(err.Error(), "is a boss row") {
		t.Fatalf("boss row: %v", err)
	}
	if _, _, err := PlaceStart(rows, []string{"from", "step", "9"}); err == nil || !strings.Contains(err.Error(), "3 steps") {
		t.Fatalf("out of range: %v", err)
	}
}

func TestPlaceStartRefusesEventHold(t *testing.T) {
	rows := placeRows(nil,
		placeMove(0, 0, 100, 0),
		placeMove(0, 0, 105, 0),
		placeRow(planexport.KindHold, 0, 0, 110, 0, 0),
	)
	// step 2 is the move leading into the hold at step 3: the server would move the start back.
	if _, _, err := PlaceStart(rows, []string{"from", "step", "2"}); err == nil || !strings.Contains(err.Error(), "event at step 3") {
		t.Fatalf("err %v", err)
	}
	if _, _, err := PlaceStart(rows, []string{"from", "step", "3"}); err == nil || !strings.Contains(err.Error(), "is a hold row") {
		t.Fatalf("hold row: %v", err)
	}
}

func TestPlaceClearCorridorPicksBehindOnly(t *testing.T) {
	steps := []planexport.StepRow{
		placeMove(0, 0, -100, 0),
		placeRow(planexport.KindPull, 0, 20, 50, 0, 2, 15111),  // 20 yd from the route: in
		placeRow(planexport.KindPull, 0, 60, 50, 0, 2, 15112),  // 60 yd out
		placeRow(planexport.KindPull, 0, 20, 50, 20, 2, 15113), // 20 yd above the route
	}
	steps = append(steps, placeRoute(0, 11)...)                                  // steps 5..15: the route
	steps = append(steps, placeRow(planexport.KindPull, 0, 10, 60, 0, 2, 15114)) // step 16: AHEAD
	rows := placeRows(nil, steps...)
	cp := PlaceClear(rows, nil, 5, 16)
	if cp.Refusal != "" {
		t.Fatalf("refused: %q", cp.Refusal)
	}
	if len(cp.Circles) != 1 {
		t.Fatalf("circles %+v", cp.Circles)
	}
	if c := cp.Circles[0]; c.Step != 2 || c.Radius != 7 || c.X != 20 || c.Y != 50 {
		t.Fatalf("circle %+v", c)
	}
	if len(cp.Notes) != 0 {
		t.Fatalf("notes %v", cp.Notes)
	}
}

func TestPlaceClearSplitsOnAheadOverlap(t *testing.T) {
	rows := placeRows(nil,
		placeRow(planexport.KindPull, 0, 0, 100, 0, 17, 15111, 11340), // 1: BEHIND, circle r 22
		placeMove(0, 0, 108, 0), // 2: the start
		placeRow(planexport.KindPull, 0, 0, 116, 0, 10, 15111, 11340), // 3: AHEAD, circle r 13 at 16 yd
	)
	plan := &dungeon.Plan{Legs: []dungeon.Leg{{Name: "one", PullGroups: []dungeon.PullGroup{
		{Entries: []uint32{15111, 11340}, Positions: []dungeon.Point{{X: 0, Y: 100, Z: 0}, {X: 0, Y: 108, Z: 0}}},
	}}}}
	cp := PlaceClear(rows, plan, 2, 3)
	if cp.Refusal != "" {
		t.Fatalf("refused: %q", cp.Refusal)
	}
	if len(cp.Circles) != 1 {
		t.Fatalf("circles %+v", cp.Circles)
	}
	c := cp.Circles[0]
	if !c.PerUnit || c.Radius != PlaceUnitYd || c.Unit != 15111 || c.Step != 1 || c.X != 0 || c.Y != 100 {
		t.Fatalf("circle %+v", c)
	}
	if len(cp.Notes) != 1 || !strings.Contains(cp.Notes[0], "lies in AHEAD row 3") || !strings.Contains(cp.Notes[0], "11340") {
		t.Fatalf("notes %v", cp.Notes)
	}
}

func TestPlaceClearRefusesBossNearRoute(t *testing.T) {
	steps := []planexport.StepRow{placeRow(planexport.KindBoss, 0, 20, 50, 0, 5, 12345)}
	steps = append(steps, placeRoute(0, 11)...)
	rows := placeRows(nil, steps...)
	cp := PlaceClear(rows, nil, 2, 12)
	if cp.Refusal == "" || !strings.Contains(cp.Refusal, "boss/event row 1 of leg 0") {
		t.Fatalf("refusal %q", cp.Refusal)
	}
	if len(cp.Circles) != 0 {
		t.Fatalf("circles %+v", cp.Circles)
	}
}

func TestPlaceClearNeverBossRows(t *testing.T) {
	// Inside the 45-yd corridor but beyond the 30-yd guard (40 yd out, r 2): neither cleared nor a refusal.
	steps := []planexport.StepRow{
		placeRow(planexport.KindBoss, 0, 40, 50, 0, 2, 12345),
		placeRow(planexport.KindHold, 0, -40, 50, 0, 2),
	}
	steps = append(steps, placeRoute(0, 11)...)
	rows := placeRows(nil, steps...)
	cp := PlaceClear(rows, nil, 3, 13)
	if cp.Refusal != "" {
		t.Fatalf("refused: %q", cp.Refusal)
	}
	if len(cp.Circles) != 0 {
		t.Fatalf("circles %+v", cp.Circles)
	}
}

// zgPlaceRows is the ZG fixture: the deployed plan's rows cut to the steps the place of leg 3 needs (1..274 of the
// 1095-step export of dungeons/zul_gurub.json, exported for the spec with --approach-hops).
func zgPlaceRows(t *testing.T) planexport.Rows {
	t.Helper()
	raw, err := os.ReadFile("testdata/zg-place-rows.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows planexport.Rows
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows.Steps) != 274 {
		t.Fatalf("the fixture has %d steps", len(rows.Steps))
	}
	return rows
}

func zgPlacePlan(t *testing.T) *dungeon.Plan {
	t.Helper()
	p, err := dungeon.Load("../../dungeons/zul_gurub.json")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPlaceClearZGLeg3(t *testing.T) {
	rows := zgPlaceRows(t)
	plan := zgPlacePlan(t)
	cp := PlaceClear(rows, plan, 231, 274)
	if cp.Refusal != "" {
		t.Fatalf("refused: %q", cp.Refusal)
	}
	var steps []int
	for _, c := range cp.Circles {
		if len(steps) == 0 || steps[len(steps)-1] != c.Step {
			steps = append(steps, c.Step)
		}
	}
	want := []int{89, 91, 94, 96, 99}
	if len(steps) != len(want) {
		t.Fatalf("circles from steps %v, want %v (notes %v)", steps, want, cp.Notes)
	}
	for i := range want {
		if steps[i] != want[i] {
			t.Fatalf("circles from steps %v, want %v", steps, want)
		}
	}
	// The AHEAD circles carry their row's entries: the quiet's own judgement tells a unit of a later leg (which stays)
	// from a leftover of a leg the raid skips (which must go).
	var entries []uint32
	for _, a := range cp.Ahead {
		if a.Step == 234 {
			entries = a.Entries
		}
	}
	if len(entries) != 2 || entries[0] != 15111 || entries[1] != 11340 {
		t.Fatalf("the AHEAD circle of step 234 carries %v, want [15111 11340]", entries)
	}
	if k, own := (PlacePlan{Ahead: cp.Ahead}).AheadOwns(15111, -11873.62, -1891.77, 64.1); !own || k != 234 {
		t.Fatalf("AheadOwns(15111 at step 234's point) = %d, %v", k, own)
	}
	if _, own := (PlacePlan{Ahead: cp.Ahead}).AheadOwns(2914, -11873.62, -1891.77, 64.1); own {
		t.Fatal("AheadOwns owns an entry no AHEAD row holds")
	}
}

func TestPlaceStartZGLeg3Stages(t *testing.T) {
	rows := zgPlaceRows(t)
	step, leg, err := PlaceStart(rows, []string{"from", "leg", "high_priest_thekal"})
	if err != nil {
		t.Fatal(err)
	}
	if step != 231 || leg != "high_priest_thekal" {
		t.Fatalf("start %d (%q), want 231", step, leg)
	}
	if p, ok := PlacePoint(rows, step); !ok || p.X != -11919.999 || p.Z != 61.340786 {
		t.Fatalf("point %+v %v", p, ok)
	}
	// the leg's own first creature row is step 234: the staged start stands 50+ yd from it
	if d := dist2f(rows.Steps[230].X, rows.Steps[230].Y, rows.Steps[233].X, rows.Steps[233].Y); d < PlaceStartClearYd {
		t.Fatalf("step 231 is %.1f yd from step 234", d)
	}
}

func TestPlaceStopClauses(t *testing.T) {
	rows := placeRows(nil,
		placeMove(0, 0, 0, 0),
		placeRow(planexport.KindBoss, 0, 0, 10, 0, 0, 11347),
		placeMove(1, 0, 20, 0),
		placeRow(planexport.KindPull, 1, 0, 30, 0, 0, 15111),
	)
	if n, err := PlaceStop(rows, []string{"until", "boss"}, 1); err != nil || n != 2 {
		t.Fatalf("until boss: %d, %v", n, err)
	}
	if n, err := PlaceStop(rows, []string{"until", "boss"}, 3); err == nil {
		t.Fatalf("until boss past the boss: %d, %v", n, err)
	}
	if n, err := PlaceStop(rows, []string{"until", "step", "3"}, 1); err != nil || n != 3 {
		t.Fatalf("until step: %d, %v", n, err)
	}
	if n, err := PlaceStop(rows, nil, 1); err != nil || n != 4 {
		t.Fatalf("none: %d, %v", n, err)
	}
	rows.Legs = []planexport.LegRow{{Plan: "t", Leg: 1, Name: "two"}}
	if n, err := PlaceStop(rows, []string{"until", "leg", "two"}, 1); err != nil || n != 3 {
		t.Fatalf("until leg: %d, %v", n, err)
	}
}

func TestPlaceStopRefusesBeforeStart(t *testing.T) {
	rows := placeRows(
		[]planexport.LegRow{{Plan: "t", Leg: 0, Name: "one"}, {Plan: "t", Leg: 1, Name: "two"}},
		placeMove(0, 0, 10, 0),
		placeRow(planexport.KindPull, 0, 0, 20, 0, 2, 15111),
		placeMove(1, 0, 30, 0),
	)
	if _, err := PlaceStop(rows, []string{"until", "step", "1"}, 3); err == nil || !strings.Contains(err.Error(), "before the start") {
		t.Fatalf("a stop before the start: %v", err)
	}
	if _, err := PlaceStop(rows, []string{"until", "leg", "one"}, 3); err == nil || !strings.Contains(err.Error(), "before the start") {
		t.Fatalf("an until leg before the start: %v", err)
	}
	if n, err := PlaceStop(rows, []string{"until", "step", "3"}, 3); err != nil || n != 3 {
		t.Fatalf("a stop at the start: %d, %v", n, err)
	}
}

// The rows renumber legs when an optional leg is left out of the export (planexport's outLeg counts only written legs):
// leg "three" is row leg 1, but plan.Legs[1] is the optional leg "two". The split must find "three"'s pull groups by
// NAME, never by the row number (review round 1, item 4).
func TestPlaceClearGroupPositionsByLegName(t *testing.T) {
	rows := placeRows(
		[]planexport.LegRow{{Plan: "t", Leg: 0, Name: "one"}, {Plan: "t", Leg: 1, Name: "three"}},
		placeRow(planexport.KindPull, 1, 0, 100, 0, 17, 15111, 11340), // 1: BEHIND, circle r 22
		placeMove(1, 0, 108, 0), // 2: the start
		placeRow(planexport.KindPull, 1, 0, 116, 0, 10, 15111, 11340), // 3: AHEAD
	)
	plan := &dungeon.Plan{Legs: []dungeon.Leg{
		{Name: "one"},
		{Name: "two", PullGroups: []dungeon.PullGroup{ // the optional leg left out: same entry, one position
			{Entries: []uint32{15111}, Positions: []dungeon.Point{{X: 0, Y: 100, Z: 0}}},
		}},
		{Name: "three", PullGroups: []dungeon.PullGroup{
			{Entries: []uint32{15111, 11340}, Positions: []dungeon.Point{{X: 0, Y: 100, Z: 0}, {X: 0, Y: 108, Z: 0}}},
		}},
	}}
	cp := PlaceClear(rows, plan, 2, 3)
	if cp.Refusal != "" {
		t.Fatalf("refused: %q", cp.Refusal)
	}
	if len(cp.Circles) != 1 {
		t.Fatalf("circles %+v", cp.Circles)
	}
	if c := cp.Circles[0]; !c.PerUnit || c.Step != 1 || c.Unit != 15111 || c.X != 0 || c.Y != 100 {
		t.Fatalf("circle %+v", c)
	}
	// "three" has two positions: the second (entry 11340 at 0,108) lies inside the AHEAD circle and is dropped. The
	// leg "two" group would have produced no note at all.
	if len(cp.Notes) != 1 || !strings.Contains(cp.Notes[0], "11340") || !strings.Contains(cp.Notes[0], "lies in AHEAD row 3") {
		t.Fatalf("notes %v", cp.Notes)
	}
}

// A BEHIND circle overlaps a creature row beyond the until stop: the AHEAD set runs to the LAST row of the plan, so
// the row still splits the circle (review round 1, item 6).
func TestPlaceClearAheadBeyondStop(t *testing.T) {
	rows := placeRows(nil,
		placeRow(planexport.KindPull, 0, 0, 100, 0, 17, 15111, 11340), // 1: BEHIND
		placeMove(0, 0, 108, 0), // 2: the start
		placeMove(0, 0, 112, 0), // 3: the stop
		placeRow(planexport.KindPull, 0, 0, 116, 0, 10, 15111, 11340), // 4: AHEAD, beyond the stop
	)
	plan := &dungeon.Plan{Legs: []dungeon.Leg{{Name: "one", PullGroups: []dungeon.PullGroup{
		{Entries: []uint32{15111, 11340}, Positions: []dungeon.Point{{X: 0, Y: 100, Z: 0}, {X: 0, Y: 108, Z: 0}}},
	}}}}
	cp := PlaceClear(rows, plan, 2, 3)
	if cp.Refusal != "" {
		t.Fatalf("refused: %q", cp.Refusal)
	}
	if len(cp.Ahead) != 1 || cp.Ahead[0].Step != 4 {
		t.Fatalf("ahead %+v", cp.Ahead)
	}
	if len(cp.Circles) != 1 || !cp.Circles[0].PerUnit {
		t.Fatalf("circles %+v", cp.Circles)
	}
}

// F5 (D8): a later leg's route comes within the guard of a BEHIND boss row: the run shortens its until to stop before
// the offending segment, and the corridor's route ends one positioned step earlier.
func TestPlaceBossGuardShortensInLaterLeg(t *testing.T) {
	steps := []planexport.StepRow{placeRow(planexport.KindBoss, 0, 20, 150, 0, 0, 12345)} // step 1
	steps = append(steps, placeRoute(1, 10)...)                                           // steps 2..11: leg 1, (0,10)..(0,100)
	steps = append(steps, placeMove(2, 20, 140, 0))                                       // step 12: leg 2 starts near the boss
	for i := 1; i <= 4; i++ {
		steps = append(steps, placeMove(2, 20, float32(140+i*10), 0)) // steps 13..16: along +y past the boss
	}
	rows := placeRows([]planexport.LegRow{
		{Plan: "t", Leg: 0, Name: "one"}, {Plan: "t", Leg: 1, Name: "two"}, {Plan: "t", Leg: 2, Name: "three"},
	}, steps...)
	start, stop := 5, len(rows.Steps)
	until, routeEnd, note, refusal, hit := PlaceBossGuard(rows, start, stop)
	if refusal != "" {
		t.Fatalf("refused: %q", refusal)
	}
	if !hit {
		t.Fatal("the guard did not report the segment")
	}
	if until != 12 || routeEnd != 11 {
		t.Fatalf("until %d, routeEnd %d, want 12 and 11", until, routeEnd)
	}
	if !strings.Contains(note, "row 1 of leg 0") {
		t.Fatalf("note %q", note)
	}
	// The corridor ends before the offending segment: PlaceClear accepts routeEnd and refuses until.
	if cp := PlaceClear(rows, nil, start, routeEnd); cp.Refusal != "" {
		t.Fatalf("PlaceClear(routeEnd) refused: %q", cp.Refusal)
	}
	if cp := PlaceClear(rows, nil, start, until); cp.Refusal == "" {
		t.Fatal("PlaceClear(until) did not refuse")
	}
}

// H1 (D8): the guard's hit is on the LAST segment (the stop step itself), so until stays the stop: the caller needs
// hit to shorten an `--until none` run to `until step <stop>`, and to refuse a boss-done run it would cut before.
// The reviewer's fixture: leg 0's boss at (20,150), leg 1's moves, and leg 2's last row at (20,140).
func TestPlaceBossGuardShortensAHitOnTheLastSegment(t *testing.T) {
	steps := []planexport.StepRow{placeRow(planexport.KindBoss, 0, 20, 150, 0, 0, 12345)} // step 1
	steps = append(steps, placeRoute(1, 10)...)                                           // steps 2..11: leg 1, (0,10)..(0,100)
	steps = append(steps, placeMove(2, 20, 140, 0))                                       // step 12: leg 2's last row, 10 yd from the boss
	rows := placeRows([]planexport.LegRow{
		{Plan: "t", Leg: 0, Name: "one"}, {Plan: "t", Leg: 1, Name: "two"}, {Plan: "t", Leg: 2, Name: "three"},
	}, steps...)
	start, stop := 5, len(rows.Steps)
	until, routeEnd, note, refusal, hit := PlaceBossGuard(rows, start, stop)
	if refusal != "" {
		t.Fatalf("refused: %q", refusal)
	}
	if !hit {
		t.Fatal("the guard did not report the hit on the last segment")
	}
	if until != stop || routeEnd != stop-1 {
		t.Fatalf("until %d, routeEnd %d, want the stop %d and the step before it %d", until, routeEnd, stop, stop-1)
	}
	if !strings.Contains(note, "row 1 of leg 0") || !strings.Contains(note, "stops before step 12") {
		t.Fatalf("note %q", note)
	}
	// The caller's shortening (`until step 12`) stops the run before step 12; the corridor ends at step 11. Without
	// that, the final segment is walked and PlaceClear(until) refuses.
	if cp := PlaceClear(rows, nil, start, routeEnd); cp.Refusal != "" {
		t.Fatalf("PlaceClear(routeEnd) refused: %q", cp.Refusal)
	}
	if cp := PlaceClear(rows, nil, start, until); cp.Refusal == "" {
		t.Fatal("PlaceClear(until) did not refuse: the last segment is within the guard")
	}
}

// F5 (D8): a BEHIND boss row next to a segment of the start step's own leg refuses as before (no shortening).
func TestPlaceBossGuardRefusesInStartLeg(t *testing.T) {
	steps := []planexport.StepRow{
		placeRow(planexport.KindBoss, 0, 0, 30, 0, 0, 12345), // step 1: on the route
		placeMove(0, 0, 10, 0),
		placeMove(0, 0, 20, 0),
		placeMove(0, 0, 30, 0), // step 4: the start, at the boss's point
		placeMove(0, 0, 40, 0),
	}
	rows := placeRows(nil, steps...)
	start, stop := 4, len(rows.Steps)
	until, routeEnd, _, refusal, hit := PlaceBossGuard(rows, start, stop)
	if !hit {
		t.Fatal("the guard did not report the segment")
	}
	if until != stop || routeEnd != stop {
		t.Fatalf("until %d, routeEnd %d, want %d (no shortening)", until, routeEnd, stop)
	}
	want := PlaceClear(rows, nil, start, stop).Refusal
	if want == "" {
		t.Fatal("the fixture's PlaceClear does not refuse")
	}
	if refusal != want {
		t.Fatalf("refusal %q, want the PlaceClear text %q", refusal, want)
	}
}

// F5 (D8): with the deployed-style ZG rows (401 steps from planexport.Build), a placed `--until none` run from Marli
// stops before the segment on leg 6 that passes Venoxis, and the shortened corridor clears clean.
func TestPlaceBossGuardZGMarliUntilNone(t *testing.T) {
	plan := zgPlacePlan(t)
	res, err := planexport.Build(plan, planexport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	rows := res.Rows
	if len(rows.Steps) != 401 {
		t.Fatalf("the fixture has %d steps, want 401", len(rows.Steps))
	}
	start, _, err := PlaceStart(rows, []string{"from", "leg", "high_priestess_marli"})
	if err != nil {
		t.Fatal(err)
	}
	stop := len(rows.Steps)
	until, routeEnd, note, refusal, hit := PlaceBossGuard(rows, start, stop)
	if refusal != "" {
		t.Fatalf("refused: %q", refusal)
	}
	if !hit {
		t.Fatal("the guard did not report the segment")
	}
	if until <= start {
		t.Fatalf("until %d is not past the start %d", until, start)
	}
	jindo := 0
	for _, l := range rows.Legs {
		if l.Name == "jindo_the_hexxer" {
			jindo = l.Leg
		}
	}
	if jindo == 0 {
		t.Fatal("the rows have no jindo_the_hexxer leg")
	}
	if rows.Steps[until-1].Leg != jindo {
		t.Fatalf("until step %d is leg %d, want the jindo_the_hexxer leg %d", until, rows.Steps[until-1].Leg, jindo)
	}
	ven, venLeg := 0, 0
	for i, s := range rows.Steps {
		if s.Kind == planexport.KindBoss && s.Entry == 14507 {
			ven, venLeg = i+1, s.Leg
			break
		}
	}
	if ven == 0 {
		t.Fatal("the rows have no Venoxis (14507) boss row")
	}
	if !strings.Contains(note, fmt.Sprintf("row %d of leg %d", ven, venLeg)) {
		t.Fatalf("the note %q does not name the Venoxis row %d of leg %d", note, ven, venLeg)
	}
	if cp := PlaceClear(rows, plan, start, routeEnd); cp.Refusal != "" {
		t.Fatalf("PlaceClear(routeEnd=%d) refused: %q", routeEnd, cp.Refusal)
	}
}

func TestPlaceSkipsUnionsTheLegs(t *testing.T) {
	plan := &dungeon.Plan{Legs: []dungeon.Leg{
		{Skip: []uint32{14758, 15091}},
		{Skip: []uint32{15091, 14758}},
		{Skip: []uint32{962}},
	}}
	if got := SkipWords(PlaceSkips(plan)); got != "962,14758,15091" {
		t.Fatalf("skips %q", got)
	}
	if SkipWords(PlaceSkips(nil)) != "" {
		t.Fatal("a nil plan gives skips")
	}
}
