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
	got, leg, _, _, err := PlaceStart(rows, []string{"from", "leg", "two"})
	if err != nil {
		t.Fatal(err)
	}
	if got != 13 || leg != "two" {
		t.Fatalf("start %d (%q), want 13 (two)", got, leg)
	}
	// the leg number finds the same leg
	if got, _, _, _, err := PlaceStart(rows, []string{"from", "leg", "1"}); err != nil || got != 13 {
		t.Fatalf("by number: %d, %v", got, err)
	}
}

func TestPlaceStartRefusesNoQuietMove(t *testing.T) {
	// A leg past the first (leg 1 of two) with no quiet move row refuses, exit 2 (the old wording is kept).
	// The zero leg's own move is pinned by its pull, so the backward search finds no quiet row at all.
	rows := placeRows([]planexport.LegRow{{Plan: "t", Leg: 0, Name: "zero"}, {Plan: "t", Leg: 1, Name: "one"}},
		placeMove(0, 0, 1000, 0),
		placeRow(planexport.KindPull, 0, 0, 1005, 0, 0, 15111),
		placeMove(1, 0, 100, 0),
		placeMove(1, 0, 105, 0),
		placeRow(planexport.KindPull, 1, 0, 110, 0, 0, 15111),
	)
	_, _, _, _, err := PlaceStart(rows, []string{"from", "leg", "one"})
	if err == nil || !strings.Contains(err.Error(), "no quiet move row") {
		t.Fatalf("err %v", err)
	}
}

func TestPlaceStartFallsBackToEarlierLeg(t *testing.T) {
	// Leg "two" is not the first leg and starts with a pull: no move row of its own stands before its
	// first pull. Leg "one" ends with a quiet move row (step 4, 110 yd from the pull), so the start falls
	// back to it (#370). Leg "zero" holds one pinned move so leg "one" is not the door leg (H1).
	rows := placeRows([]planexport.LegRow{{Plan: "t", Leg: 0, Name: "zero"}, {Plan: "t", Leg: 1, Name: "one"}, {Plan: "t", Leg: 2, Name: "two"}},
		placeMove(0, 0, 10, 0),
		placeRow(planexport.KindPull, 0, 0, 15, 0, 0, 15111),
		placeMove(1, 0, 20, 0),
		placeMove(1, 0, 30, 0),
		placeRow(planexport.KindPull, 2, 0, 140, 0, 0, 15111),
	)
	got, leg, fallback, _, err := PlaceStart(rows, []string{"from", "leg", "two"})
	if err != nil {
		t.Fatal(err)
	}
	if got != 4 || leg != "one" || fallback != "two" {
		t.Fatalf("start %d (%q), want 4 (one)", got, leg)
	}
}

func TestPlaceStartFirstLegIsDoorStart(t *testing.T) {
	// H1: Jeklik is leg 0, so the backward search never runs: --place --from leg one behaves like a door
	// start, with no .go. The ZG cmd-level test pins the real case (leg high_priestess_jeklik -> step 1).
	rows := placeRows([]planexport.LegRow{{Plan: "t", Leg: 0, Name: "one"}, {Plan: "t", Leg: 1, Name: "two"}},
		placeMove(0, 0, 10, 0),
		placeMove(0, 0, 20, 0),
		placeRow(planexport.KindPull, 1, 0, 1000, 0, 0, 15111),
	)
	got, leg, fallback, door, err := PlaceStart(rows, []string{"from", "leg", "one"})
	if err != nil {
		t.Fatal(err)
	}
	if !door || got != 1 || leg != "one" || fallback != "" {
		t.Fatalf("start %d (%q fallback %q door %v), want step 1 (one) as a door start", got, leg, fallback, door)
	}
	if _, _, _, door, err := PlaceStart(rows, []string{"from", "leg", "two"}); err != nil || door {
		t.Fatalf("later leg two: step start, door %v, err %v", door, err)
	}
}

func TestPlaceStartRefusesFallbackBeforeBossOrHold(t *testing.T) {
	// H2: the reviewer's probe plan, past a door leg: leg one moves at y=10/20/30, a boss/hold at 100, a
	// pinned move at 105 (5 yd from leg two's pull at 110), leg two's pull at 110 and boss at 200. The
	// LAST fallback candidate walking back is the pinned move at step 6, then the boss/hold row itself
	// (not a move), then the quiet move at step 4 — which stands before the boss/hold, so the raid
	// would re-fight it and the fallback is refused. Leg zero's move is pinned by its own early pull,
	// so it cannot absorb the case either (H1 stays a door start).
	mk := func(secondKind string) planexport.Rows {
		return placeRows([]planexport.LegRow{{Plan: "t", Leg: 0, Name: "zero"}, {Plan: "t", Leg: 1, Name: "one"}, {Plan: "t", Leg: 2, Name: "two"}},
			placeMove(0, 0, 10, 0),
			placeRow(planexport.KindPull, 0, 0, 15, 0, 0, 15111),
			placeMove(1, 0, 20, 0),
			placeMove(1, 0, 30, 0),
			placeRow(secondKind, 1, 0, 100, 0, 0, 11347),
			placeMove(1, 0, 105, 0),
			placeRow(planexport.KindPull, 2, 0, 110, 0, 0, 15111),
			placeRow(planexport.KindBoss, 2, 0, 200, 0, 0, 11347),
		)
	}
	for _, kind := range []string{planexport.KindBoss, planexport.KindHold} {
		rows := mk(kind)
		_, _, _, _, err := PlaceStart(rows, []string{"from", "leg", "two"})
		if err == nil {
			t.Fatalf("%s between: placed, want the exit-2 refusal", kind)
		}
		if kind == planexport.KindHold {
			// A hold is an event row: the move leading into it refuses like the in-leg loop (L1).
			if !strings.Contains(err.Error(), "event at step 5") {
				t.Fatalf("hold between: err %v", err)
			}
			continue
		}
		if !strings.Contains(err.Error(), "no quiet move row") || !strings.Contains(err.Error(), "re-fight") {
			t.Fatalf("%s between: err %v", kind, err)
		}
	}
}

func TestPlaceStartFallbackResolvesUntilFromTargetLeg(t *testing.T) {
	// H2: only moves and trash between the fallback row and leg two's first row, so the fallback is placed;
	// the until resolves from leg two's first row, not from the fallback start. Leg two is not the first
	// leg (leg zero holds one far move), so PlaceStart takes the fallback path.
	rows := placeRows([]planexport.LegRow{{Plan: "t", Leg: 0, Name: "zero"}, {Plan: "t", Leg: 1, Name: "one"}, {Plan: "t", Leg: 2, Name: "two"}},
		placeMove(0, 0, 10, 0),
		placeRow(planexport.KindPull, 0, 0, 15, 0, 0, 15111),
		placeRow(planexport.KindPull, 1, 0, 900, 0, 0, 15111),
		placeMove(1, 0, 1000, 0),
		placeMove(1, 0, 1010, 0),
		placeMove(1, 0, 1020, 0),
		placeRow(planexport.KindPull, 2, 0, 2000, 0, 0, 15111),
		placeRow(planexport.KindBoss, 2, 0, 2005, 0, 0, 11347),
	)
	got, leg, fallback, door, err := PlaceStart(rows, []string{"from", "leg", "two"})
	if err != nil || door {
		t.Fatal(err)
	}
	if got != 6 || leg != "one" || fallback != "two" {
		t.Fatalf("start %d (%q fallback %q), want 6 (one)", got, leg, fallback)
	}
	base := PlaceUntilBase(rows, fallback, got)
	if base != 7 {
		t.Fatalf("until base %d, want leg two's first row 7", base)
	}
	// The boss until (used for --until boss and boss-done) names leg two's boss, from the target leg's
	// first row. (The guard refuses every fallback a leg-one boss between the rows would hijack, so the
	// from-start answer coincides here; the base pins the target leg regardless.)
	stop, err := PlaceStop(rows, []string{"until", "boss"}, base)
	if err != nil || stop != 8 {
		t.Fatalf("until boss stop %d, err %v, want leg two's boss row 8", stop, err)
	}
	if clause, boss, err := BossDoneUntil(rows, base); err != nil || boss != 8 {
		t.Fatalf("boss-done from base: clause %q boss %d, err %v, want boss step 8", clause, boss, err)
	}
	if PlaceUntilBase(rows, "", got) != got || PlaceUntilBase(rows, "nope", got) != got {
		t.Fatal("no fallback: the base is the start")
	}
}

func TestPlaceStartRefusesFallbackIntoEvent(t *testing.T) {
	// H2/L1: an event row in the fallback region refuses like the in-leg loop, it is not skipped. The only
	// fallback candidate stands one hold before leg two.
	rows := placeRows([]planexport.LegRow{{Plan: "t", Leg: 0, Name: "one"}, {Plan: "t", Leg: 1, Name: "two"}},
		placeMove(0, 0, 10, 0),
		placeMove(0, 0, 20, 0),
		placeMove(0, 0, 30, 0),
		placeRow(planexport.KindHold, 0, 0, 1000, 0, 0),
		placeRow(planexport.KindPull, 1, 0, 1005, 0, 0, 15111),
	)
	_, _, _, _, err := PlaceStart(rows, []string{"from", "leg", "two"})
	if err == nil || !strings.Contains(err.Error(), "event at step 4") {
		t.Fatalf("err %v", err)
	}
}

func TestPlaceStartRefusesNoQuietInEarlierLegs(t *testing.T) {
	// Every move row stands within 50 yd of a later pull, in the target leg and the earlier one: the backward
	// search finds no quiet row at all, so the exit-2 refusal stays (#370).
	rows := placeRows([]planexport.LegRow{{Plan: "t", Leg: 0, Name: "one"}, {Plan: "t", Leg: 1, Name: "two"}},
		placeMove(0, 0, 100, 0),
		placeMove(0, 0, 105, 0),
		placeRow(planexport.KindPull, 0, 0, 110, 0, 0, 15111),
		placeRow(planexport.KindPull, 1, 0, 115, 0, 0, 15111),
	)
	_, _, _, _, err := PlaceStart(rows, []string{"from", "leg", "two"})
	if err == nil || !strings.Contains(err.Error(), "no quiet move row") {
		t.Fatalf("err %v", err)
	}
}

// A pull row is a stand (walk 32): step 2 is accepted at its own point. A boss or hold row still refuses, and so
// does a pull row with no point of its own when no move row stands before it.
func TestPlaceStartRefusesNonStandRows(t *testing.T) {
	rows := placeRows(nil,
		placeMove(0, 0, 100, 0),
		placeRow(planexport.KindPull, 0, 0, 110, 0, 0, 15111),
		placeRow(planexport.KindBoss, 0, 0, 120, 0, 0, 11347),
	)
	if got, _, _, _, err := PlaceStart(rows, []string{"from", "step", "2"}); err != nil || got != 2 {
		t.Fatalf("pull row: %d, %v", got, err)
	}
	if _, _, _, _, err := PlaceStart(rows, []string{"from", "step", "3"}); err == nil || !strings.Contains(err.Error(), "is a boss row") {
		t.Fatalf("boss row: %v", err)
	}
	if _, _, _, _, err := PlaceStart(rows, []string{"from", "step", "9"}); err == nil || !strings.Contains(err.Error(), "3 steps") {
		t.Fatalf("out of range: %v", err)
	}
	firstPull := placeRows(nil, placeRow(planexport.KindPull, 0, 0, 0, 0, 10, 15111))
	if _, _, _, _, err := PlaceStart(firstPull, []string{"from", "step", "1"}); err == nil || !strings.Contains(err.Error(), "no move row stands before it") {
		t.Fatalf("a point-less pull row with no move before it: %v", err)
	}
}

func TestPlaceStartRefusesEventHold(t *testing.T) {
	rows := placeRows(nil,
		placeMove(0, 0, 100, 0),
		placeMove(0, 0, 105, 0),
		placeRow(planexport.KindHold, 0, 0, 110, 0, 0),
	)
	// step 2 is the move leading into the hold at step 3: the server would move the start back.
	if _, _, _, _, err := PlaceStart(rows, []string{"from", "step", "2"}); err == nil || !strings.Contains(err.Error(), "event at step 3") {
		t.Fatalf("err %v", err)
	}
	if _, _, _, _, err := PlaceStart(rows, []string{"from", "step", "3"}); err == nil || !strings.Contains(err.Error(), "is a hold row") {
		t.Fatalf("hold row: %v", err)
	}
}

func TestPlaceClearCorridorPicksBehindOnly(t *testing.T) {
	steps := []planexport.StepRow{
		placeMove(0, 0, -100, 0),
		placeRow(planexport.KindPull, 0, 20, 50, 0, 2, 15111),   // 20 yd from the route: in
		placeRow(planexport.KindPull, 0, 200, 50, 0, 2, 15112),  // 200 yd out (beyond the 150 yd sweep)
		placeRow(planexport.KindPull, 0, 20, 50, 100, 2, 15113), // 100 yd above the route (beyond the 60 z sweep)
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
	// Behind is every pull row before the start, not only the corridor's circles: the rows outside the sweep are
	// BEHIND rows too, so a unit they spawn is owned there. The move and the AHEAD pull are not.
	if len(cp.Behind) != 3 {
		t.Fatalf("behind %+v", cp.Behind)
	}
	for i, want := range steps[1:4] {
		b := cp.Behind[i]
		if b.Step != i+2 || b.X != want.X || b.Y != want.Y || b.Z != want.Z || b.Radius != want.Radius ||
			len(b.Entries) != 1 || b.Entries[0] != want.Entries[0] {
			t.Fatalf("behind %d %+v, want row %d of %v", i, b, i+2, want)
		}
	}
}

// BehindOwns is the positive ownership test (review round 3): the row must hold the entry, and the home must lie within
// its radius plus PlaceOwnYd, 2D, within placeAheadDz of its height.
func TestPlaceBehindOwns(t *testing.T) {
	p := PlacePlan{Behind: []BehindRow{{Step: 7, X: 0, Y: 0, Z: 10, Radius: 8, Entries: []uint32{15111}}}}
	if k, ok := p.BehindOwns(15111, 8+PlaceOwnYd, 0, 10); !ok || k != 7 {
		t.Fatalf("a home at the row's radius + %d yd: %d, %v", PlaceOwnYd, k, ok)
	}
	if _, ok := p.BehindOwns(15111, 8+PlaceOwnYd+0.5, 0, 10); ok {
		t.Fatal("a home beyond the row's radius + 6 yd is owned")
	}
	if _, ok := p.BehindOwns(15111, 0, 0, 10+placeAheadDz); !ok {
		t.Fatal("a home at the z window's edge is not owned")
	}
	if _, ok := p.BehindOwns(15111, 0, 0, 10+placeAheadDz+0.5); ok {
		t.Fatal("a home above the z window is owned")
	}
	if _, ok := p.BehindOwns(15112, 0, 0, 10); ok {
		t.Fatal("an entry the row does not hold is owned")
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
	want := []int{89, 91, 94, 96, 99, 87, 105} // corridor circles first (route order), then the M1 sweep (step order)
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
	step, leg, fallback, _, err := PlaceStart(rows, []string{"from", "leg", "high_priest_thekal"})
	if err != nil {
		t.Fatal(err)
	}
	if step != 231 || leg != "high_priest_thekal" || fallback != "" {
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
// hit to shorten an explicit until to `until step <stop>`, and to refuse a boss-done run it would cut before.
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

// F5 (D8): with the deployed-style ZG rows (408 steps from planexport.Build: the Arlokk gong step (use_object 180526) adds 2 rows to the 405 below: walk 26b's pack-1-first Marli order,
// walk 28/29's Arlokk terrace split and walk 29's legs 5/6, plus walk 31's 23 added approach rows (the over-140 yd hops,
// the canal rim, the floored G0 midpoint and the step-366 split), and legs 7/8 from route-regen, which drops the
// approach rows whose routes could not be kept clear; it was 397 before walk 31, 407 before walk 29's fix 6 dropped the
// 14 fish-only Hakkar-outer groups and leg 8's entrance-pack detour and added the Mandokir patrol/chain rows; walk 33's
// water fix made it 405: leg 5's five river vias are the seven of the dry route round the gorge and leg 6 loses its river
// via; walk 34 adds the Jin'do group-12 midpoint approach row, 408; the Jin'do fix drops the pit pull group and its
// approach row and nets one fewer row elsewhere on leg 6, 406; fix 1 makes groups 8/9 stand pulls with south hops and
// moves the group-11 stand south past the pit, 408; walk 35 drops the Arlokk middle-east-Panther pull and its approach row, 406; walk 36
// merges the two north hall packs into one pull (one fewer group), pulls the honour guard before them and drops the -11603 -1229 pull
// beside the respawn pit (one fewer group and its three approach rows), 399; the Mandokir 440 fix makes group 0 a stand pull (no approach-path rows) and deletes group 1's first approach point, 398; walk 38 stages the Jin'do hall (two more approach rows on the lone mistresses, one fewer plain-pull approach) and splits Jeklik's patrol camps (two more rows), 402; the Arlokk re-route's authored approaches, its z 60 formation row and the split top pack, 420; the Arlokk route fix round 1 walks the mesh's loop and pulls each pack from a single stand (one approach row each) and authors the arena descent in the gong step's stand and the boss approach, 414; combined, both row sets are in one plan (walk 38's hall rows, review round 2's undo included, the Arlokk leg's loop and descent), 416), a placed
// The Arlokk route fix round 2 and the later rounds bring it to 416 (t1105c). t1105e: 417, Hakkar's approach is two
// plan points now (the ramp lip -11807.651 -1691.3049 52.111286 and the stand -11790 -1662 53.11; the exported rows are
// 694, one fewer, because the lip replaces a path row the cutter used to add). Walk 71 fast (t1106k): 416, leg 8's
// entry drops its second via and `to` is the placement row the placed walk already starts on. Route B1 (2026-11-13): 421,
// leg 7's four lake pulls take a ring stand and its ring groups 12, 16 and 20 a stand each (+7 rows) and the two shore
// groups are dropped (-2 pull rows).
// `--until none` run from Marli stops before the segment on leg 6 that passes Venoxis, and the shortened corridor
// clears clean.

func TestPlaceBossGuardZGMarliUntilNone(t *testing.T) {
	plan := zgPlacePlan(t)
	res, err := planexport.Build(plan, planexport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	rows := res.Rows
	if len(rows.Steps) != 421 {
		t.Fatalf("the fixture has %d steps, want 421", len(rows.Steps))
	}
	start, _, _, _, err := PlaceStart(rows, []string{"from", "leg", "high_priestess_marli"})
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

// M1 (PLACE-W26 §3): the landing sweep takes BEHIND pull rows near the start point, even when the corridor
// does not reach them (leftover patrols whose loop reaches the landing).
func TestPlaceClearSweepsBehindRowsNearTheStart(t *testing.T) {
	steps := []planexport.StepRow{
		placeRow(planexport.KindPull, 0, 68, 4, 71, 1, 11352),   // A: 68 yd out, dz -29: sweep
		placeRow(planexport.KindPull, 0, 160, 0, 100, 1, 11353), // B: 160 yd out: too far
		placeRow(planexport.KindPull, 0, 100, 0, 170, 1, 11354), // C: dz +70: too high
		placeRow(planexport.KindBoss, 0, 60, 0, 100, 1, 12345),  // D: boss at 60 yd: never swept
		placeRow(planexport.KindPull, 0, 20, 20, 100, 2, 15111), // E: in the corridor: one corridor circle
		placeMove(0, 0, 0, 100),                                 // 6: the start
		placeMove(0, 0, 10, 100),                                // 7..16: the route west along x=0 at the start's height
		placeMove(0, 0, 20, 100),
		placeMove(0, 0, 30, 100),
		placeMove(0, 0, 40, 100),
		placeMove(0, 0, 50, 100),
		placeMove(0, 0, 60, 100),
		placeMove(0, 0, 70, 100),
		placeMove(0, 0, 80, 100),
		placeMove(0, 0, 90, 100),
		placeMove(0, 0, 100, 100),
		placeRow(planexport.KindPull, 0, 10, 110, 100, 2, 15114), // 17: AHEAD
	}
	rows := placeRows(nil, steps...)
	cp := PlaceClear(rows, nil, 6, 17)
	if cp.Refusal != "" {
		t.Fatalf("refused: %q", cp.Refusal)
	}
	byStep := map[int][]ClearCircle{}
	for _, c := range cp.Circles {
		byStep[c.Step] = append(byStep[c.Step], c)
	}
	// A: exactly one sweep circle, radius 1 + slack (5) + sweep slack (10) = 16.
	if len(byStep[1]) != 1 {
		t.Fatalf("row 1 circles %+v, want one sweep circle", byStep[1])
	}
	if a := byStep[1][0]; !a.Sweep || a.Radius != 16 {
		t.Fatalf("row 1 circle %+v, want Sweep with radius 16", a)
	}
	// B, C, D: no circles and no refusal.
	for _, s := range []int{2, 3, 4} {
		if len(byStep[s]) != 0 {
			t.Fatalf("row %d circles %+v, want none", s, byStep[s])
		}
	}
	// E: exactly one corridor circle, not Sweep.
	if len(byStep[5]) != 1 {
		t.Fatalf("row 5 circles %+v, want one corridor circle", byStep[5])
	}
	if e := byStep[5][0]; e.Sweep {
		t.Fatalf("row 5 circle %+v is a sweep circle, want corridor", e)
	}
}

// A sweep row whose circle overlaps an AHEAD row splits per unit, with Sweep set on the split circles.
func TestPlaceClearSweepSplitsOnAheadOverlap(t *testing.T) {
	// The stop (step 4) sits before the AHEAD row: the route runs west and never comes near row 1, so row 1
	// is a sweep row, while the AHEAD set still sees row 5 (it runs to the last row, not the stop).
	rows := placeRows(nil,
		placeRow(planexport.KindPull, 0, 60, 0, 95, 1, 11352),  // 1: BEHIND sweep row (60 yd from the start)
		placeMove(0, 0, 0, 100),                                // 2: the start
		placeMove(0, -10, 0, 100),                              // 3: the route west
		placeMove(0, -20, 0, 100),                              // 4: the stop
		placeRow(planexport.KindPull, 0, 52, 0, 95, 10, 11352), // 5: AHEAD, overlaps the r-16 sweep circle
	)
	plan := &dungeon.Plan{Legs: []dungeon.Leg{{Name: "one", PullGroups: []dungeon.PullGroup{
		{Entries: []uint32{11352}, Positions: []dungeon.Point{{X: 55, Y: 2, Z: 95}, {X: 65, Y: -2, Z: 95}}},
	}}}}
	cp := PlaceClear(rows, plan, 2, 4)
	if cp.Refusal != "" {
		t.Fatalf("refused: %q", cp.Refusal)
	}
	if len(cp.Circles) != 1 {
		t.Fatalf("circles %+v, want one per-unit sweep circle", cp.Circles)
	}
	c := cp.Circles[0]
	if !c.PerUnit || !c.Sweep || c.Radius != PlaceUnitYd || c.Unit != 11352 || c.Step != 1 || c.X != 65 || c.Y != -2 {
		t.Fatalf("circle %+v", c)
	}
	if len(cp.Notes) != 1 || !strings.Contains(cp.Notes[0], "lies in AHEAD row 5") || !strings.Contains(cp.Notes[0], "11352") {
		t.Fatalf("notes %v", cp.Notes)
	}
}

// aqLikeRows is a fixture shaped like the AQ20 case: leg 0 holds an event row (a hold, radius 40) at (0,100); leg 1's
// own walk passes it (a hall on the way), then its first pull stands far from it; leg 1's boss closes the leg.
func aqLikeRows() planexport.Rows {
	steps := []planexport.StepRow{placeRow(planexport.KindHold, 0, 0, 100, 0, 40, 15341)} // step 1: the event row
	steps = append(steps,
		placeMove(1, 0, 200, 0),                               // step 2: the leg's first row, 100 yd from the event
		placeMove(1, 0, 150, 0),                               // step 3: walks back through the hall's edge
		placeMove(1, 0, 110, 0),                               // step 4: 10 yd from the event row
		placeMove(1, 0, 150, 0),                               // step 5
		placeMove(1, 0, 300, 0),                               // step 6: the quiet start
		placeRow(planexport.KindPull, 1, 0, 380, 0, 5, 15111), // step 7: the first pull
		placeMove(1, 0, 340, 0),                               // step 8
		placeMove(1, 0, 330, 0),                               // step 9
		placeRow(planexport.KindBoss, 1, 0, 500, 0, 0, 15370), // step 10: the leg's boss
	)
	return placeRows([]planexport.LegRow{{Plan: "t", Leg: 0, Name: "hall"}, {Plan: "t", Leg: 1, Name: "buru"}}, steps...)
}

// AQ20 prep: placing at a later leg's start is allowed although that leg's own walk passes an earlier leg's event row
// (the placement teleports the raid, and the start point is far from the event).
func TestPlaceBossGuardAllowsEarlierLegEventOnTheStartLegsWalk(t *testing.T) {
	rows := aqLikeRows()
	start, _, _, _, err := PlaceStart(rows, []string{"from", "leg", "buru"})
	if err != nil {
		t.Fatal(err)
	}
	if start != 6 {
		t.Fatalf("start %d, want 6", start)
	}
	stop := len(rows.Steps)
	until, routeEnd, _, refusal, _ := PlaceBossGuard(rows, start, stop)
	if refusal != "" {
		t.Fatalf("the earlier leg's event row refused the placement: %q", refusal)
	}
	if until != stop || routeEnd != stop {
		t.Fatalf("until %d, routeEnd %d, want the stop %d", until, routeEnd, stop)
	}
	// A start at step 2 (the leg's first row) passes the event row on its own leg's walk (step 4 is 10 yd from it): still allowed.
	if _, _, _, refusal, _ := PlaceBossGuard(rows, 2, stop); refusal != "" {
		t.Fatalf("start 2 refused: %q", refusal)
	}
	if cp := PlaceClear(rows, nil, 2, stop); cp.Refusal != "" {
		t.Fatalf("PlaceClear refused the earlier leg's event row: %q", cp.Refusal)
	}
	if cp := PlaceClear(rows, nil, start, routeEnd); cp.Refusal != "" {
		t.Fatalf("PlaceClear refused the earlier leg's event row: %q", cp.Refusal)
	}
}

// AQ20 prep: the safety stays. A start POINT within the guard of an earlier leg's event row is refused, and so is any
// start in the event row's own leg whose route passes it (placing at Rajaxx itself).
func TestPlaceBossGuardStillRefusesNearTheEventRow(t *testing.T) {
	rows := aqLikeRows()
	stop := len(rows.Steps)
	// Step 4 stands 10 yd from the event row, in the next leg: the start point itself is not clear.
	if _, _, _, refusal, hit := PlaceBossGuard(rows, 4, stop); refusal == "" || !hit {
		t.Fatalf("start 4 (10 yd from the event row) was not refused: %q", refusal)
	}
	if cp := PlaceClear(rows, nil, 4, stop); cp.Refusal == "" {
		t.Fatal("PlaceClear did not refuse a start point next to the earlier leg's event row")
	}
	// The event row's own leg: a start in leg 0 whose route passes the hold row is refused as before.
	steps := []planexport.StepRow{
		placeRow(planexport.KindHold, 0, 0, 100, 0, 40, 15341), // step 1
		placeMove(0, 0, 20, 0),                                 // step 2
		placeMove(0, 0, 90, 0),                                 // step 3
		placeMove(0, 0, 160, 0),                                // step 4
	}
	own := placeRows([]planexport.LegRow{{Plan: "t", Leg: 0, Name: "rajaxx"}}, steps...)
	start := 2
	_, _, _, refusal, _ := PlaceBossGuard(own, start, len(own.Steps))
	if refusal == "" {
		t.Fatal("placing at the event row's own leg was not refused")
	}
	if cp := PlaceClear(own, nil, start, len(own.Steps)); cp.Refusal == "" {
		t.Fatal("PlaceClear did not refuse a route past the event row of its own leg")
	}
}

// Walk 32: --place --from "step N" refused a pull row (steps 237, 560), so the operator had to find a move row by
// hand. A pull row is a stand the raid may be placed on: at its own point, or at the nearest move row before it when
// the row has no point. Anything else still refuses.
func TestPlaceStartAcceptsAPullRow(t *testing.T) {
	rows := placeRows([]planexport.LegRow{{Plan: "t", Leg: 0, Name: "one"}},
		placeMove(0, 0, 10, 0),
		placeRow(planexport.KindPull, 0, 0, 20, 0, 10, 15111),
		placeMove(0, 0, 30, 0),
		placeRow(planexport.KindPull, 0, 0, 0, 0, 10, 15112),
		placeRow(planexport.KindHold, 0, 0, 40, 0, 0),
	)
	for _, c := range []struct {
		from []string
		want int
	}{
		{[]string{"from", "step", "2"}, 2}, // the pull row's own stand point
		{[]string{"from", "step", "4"}, 3}, // a pull row without a point: the move row before it
	} {
		got, leg, fallback, door, err := PlaceStart(rows, c.from)
		if err != nil || got != c.want || leg != "" || fallback != "" || door {
			t.Errorf("PlaceStart(%v) = %d %q %q %v %v, want step %d", c.from, got, leg, fallback, door, err, c.want)
		}
		if p, ok := PlacePoint(rows, got); !ok || p.X != 0 || p.Y != float32(0)+float32(c.want*10) {
			t.Errorf("PlaceStart(%v): point %+v %v", c.from, p, ok)
		}
	}
	if _, _, _, _, err := PlaceStart(rows, []string{"from", "step", "5"}); err == nil || !strings.Contains(err.Error(), "hold row") {
		t.Errorf("a hold row was accepted: %v", err)
	}
	if _, _, _, _, err := PlaceStart(rows, []string{"from", "step", "1"}); err != nil {
		t.Errorf("a move row was refused: %v", err)
	}
}

// Walk 39: a restart resumed at the furthest step a run reached, and every wipe at a boss reached the boss row. The
// walk refused "--place --from step N: step N is a boss row" with exit 2 and the autopilot escalated the id (Arlokk
// 390, Hakkar 733, Jin'do 555, Mar'li 202, Mandokir 469, Venoxis 174). The resume start steps back to the nearest row
// of the same leg the walk takes, and to the leg itself when the leg has no such row before the boss.
func TestPlaceResumeStartStepsBackFromABossRow(t *testing.T) {
	rows := placeRows([]planexport.LegRow{
		{Plan: "t", Leg: 0, Name: "one"},
		{Plan: "t", Leg: 1, Name: "two"},
		{Plan: "t", Leg: 2, Name: "three"},
	},
		placeMove(0, 0, 10, 0),
		placeMove(0, 0, 20, 0),
		placeMove(1, 0, 30, 0),                               // 3
		placeRow(planexport.KindBoss, 1, 0, 35, 0, 0, 14515), // 4: the boss row the wipe reached
		placeMove(1, 0, 40, 0),                               // 5
		placeRow(planexport.KindBoss, 2, 0, 50, 0, 0, 11380), // 6: a leg whose boss is its first point
		placeMove(2, 0, 60, 0),                               // 7
	)
	if _, _, _, _, err := PlaceStart(rows, []string{"from", "step", "4"}); err == nil || !strings.Contains(err.Error(), "is a boss row") {
		t.Fatalf("the fixture's boss row is accepted as a start: %v", err)
	}
	for _, c := range []struct {
		step int
		want string
	}{
		{4, "step 3"}, // the nearest earlier move row, same leg
		{5, "step 5"}, // a row the walk takes is kept
		{3, "step 3"},
		{6, "leg 2"}, // no row before the boss in its leg: the leg's own start
	} {
		got, ok := PlaceResumeStart(rows, c.step)
		if !ok || got != c.want {
			t.Errorf("PlaceResumeStart(%d) = %q %v, want %q", c.step, got, ok, c.want)
		}
	}
	if _, ok := PlaceResumeStart(rows, 0); ok {
		t.Error("step 0 has no resume start")
	}
	if _, ok := PlaceResumeStart(rows, len(rows.Steps)+1); ok {
		t.Error("a step past the rows has no resume start")
	}
}

// Walk 46 Jin'do: every id wiped at the boss row (step 518) and the autopilot resumed from the reached step minus
// one, the last boss-approach row 25.2 yd from the boss, so the placement pulled him. A wipe at the goal's boss row or
// at one of its approach rows resumes from the goal's own start instead.
func TestPlaceResumeGoalStartFromABossOrItsApproach(t *testing.T) {
	approach := func(leg int, x, y, z float32) planexport.StepRow {
		s := placeMove(leg, x, y, z)
		s.Comment = "boss approach"
		return s
	}
	rows := placeRows([]planexport.LegRow{
		{Plan: "t", Leg: 0, Name: "one"},
		{Plan: "t", Leg: 1, Name: "two"},
		{Plan: "t", Leg: 2, Name: "three"},
	},
		placeMove(0, 0, 10, 0),
		placeMove(1, 0, 20, 0),                               // 2
		placeMove(1, 0, 30, 0),                               // 3: the goal's start
		placeRow(planexport.KindPull, 1, 0, 35, 0, 10, 777),  // 4: trash
		placeMove(1, 0, 40, 0),                               // 5
		approach(1, 0, 45, 0),                                // 6
		approach(1, 0, 50, 0),                                // 7
		placeRow(planexport.KindBoss, 1, 0, 55, 0, 0, 14515), // 8
		placeMove(2, 0, 60, 0),                               // 9
		placeRow(planexport.KindBoss, 2, 0, 70, 0, 0, 11380), // 10
	)
	for _, c := range []struct {
		name  string
		step  int
		start string
		want  string
		ok    bool
	}{
		{"the boss row", 8, "step 3", "step 3", true},
		{"the last approach row", 7, "step 3", "step 3", true},
		{"the first approach row", 6, "step 3", "step 3", true},
		{"a leg start", 8, "leg two", "leg two", true},
		{"a trash row keeps the reached step", 4, "step 3", "", false},
		{"a plain move row keeps the reached step", 5, "step 3", "", false},
		{"no goal start", 8, "", "", false},
		{"a start in another leg", 10, "step 3", "", false},
		{"a start after the reached step", 7, "step 9", "", false},
		{"a boss between the start and the step", 10, "step 8", "", false},
		{"a step outside the rows", 99, "step 3", "", false},
	} {
		got, ok := PlaceResumeGoalStart(rows, c.step, c.start)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: PlaceResumeGoalStart(%d, %q) = %q %v, want %q %v", c.name, c.step, c.start, got, ok, c.want, c.ok)
		}
	}
}

// A wipe that ends at a boss row, or the row before it, is a wipe at that boss: the autopilot launches the next
// attempt in a fresh instance (walk 44 hk7m-3, walk 46 hk4m-3). Any other step, or a step outside the rows, is not.
func TestPlaceStepAtBoss(t *testing.T) {
	rows := placeRows([]planexport.LegRow{{Plan: "t", Leg: 0, Name: "one"}},
		placeMove(0, 0, 10, 0),                               // 1
		placeMove(0, 0, 20, 0),                               // 2
		placeMove(0, 0, 30, 0),                               // 3: the row before the boss
		placeRow(planexport.KindBoss, 0, 0, 35, 0, 0, 14515), // 4: the boss row
		placeMove(0, 0, 40, 0),                               // 5
		placeMove(0, 0, 50, 0),                               // 6
	)
	for _, c := range []struct {
		step int
		want bool
	}{{1, false}, {2, false}, {3, true}, {4, true}, {5, false}, {6, false}, {0, false}, {7, false}} {
		if got := PlaceStepAtBoss(rows, c.step); got != c.want {
			t.Errorf("PlaceStepAtBoss(%d) = %v, want %v", c.step, got, c.want)
		}
	}
}

// A wipe on a boss's hold row (a boss fought on the spot) resumes from the goal's start like one on a boss row; the
// hold row of a plain event does not.
func TestPlaceResumeGoalStartFromABossHoldRow(t *testing.T) {
	bossHold := placeRow(planexport.KindHold, 0, 0, 50, 0, 5, 14515)
	bossHold.Comment = "boss hold: the event's mobs are fought on the spot"
	event := placeRow(planexport.KindHold, 0, 0, 70, 0, 5, 777)
	event.Comment = "hold: an event"
	rows := placeRows([]planexport.LegRow{{Plan: "t", Leg: 0, Name: "one"}},
		placeMove(0, 0, 10, 0),
		placeMove(0, 0, 30, 0),
		bossHold, // 3
		event,    // 4
	)
	if got, ok := PlaceResumeGoalStart(rows, 3, "step 1"); !ok || got != "step 1" {
		t.Errorf("boss hold row: %q %v, want step 1", got, ok)
	}
	if got, ok := PlaceResumeGoalStart(rows, 4, "step 1"); ok {
		t.Errorf("event hold row resumed at %q", got)
	}
}

// A boss fought on the spot is a hold row commented "boss hold": a wipe there is a wipe at that boss too.
func TestPlaceStepAtBossHoldRow(t *testing.T) {
	hold := placeRow(planexport.KindHold, 0, 0, 35, 0, 5, 14515)
	hold.Comment = "boss hold: fought on the spot"
	event := placeRow(planexport.KindHold, 0, 0, 60, 0, 5, 777)
	event.Comment = "hold: an event"
	rows := placeRows([]planexport.LegRow{{Plan: "t", Leg: 0, Name: "one"}},
		placeMove(0, 0, 30, 0), // 1: the row before the boss hold
		hold,                   // 2
		placeMove(0, 0, 50, 0), // 3
		event,                  // 4
	)
	for _, c := range []struct {
		step int
		want bool
	}{{1, true}, {2, true}, {3, false}, {4, false}} {
		if got := PlaceStepAtBoss(rows, c.step); got != c.want {
			t.Errorf("PlaceStepAtBoss(%d) = %v, want %v", c.step, got, c.want)
		}
	}
}
