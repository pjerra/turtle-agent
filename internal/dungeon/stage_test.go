package dungeon

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

// stageNav is the spec's stage_fake: fakeNav plus a ServerPath method so the
// offline check replays the server's path query. noPath forces PathNoPath
// for a from->to pair (keyed "from->to" on exact Point values).
type stageNav struct {
	fakeNav
	noPath map[string]bool
}

func stagePairKey(f, t Point) string { return fmt.Sprintf("%v->%v", f, t) }

func (s *stageNav) ServerPath(_ uint32, from, to navmesh.Vec3) (navmesh.ServerPath, error) {
	f, t := Point{from[0], from[1], from[2]}, Point{to[0], to[1], to[2]}
	if s.noPath != nil && s.noPath[stagePairKey(f, t)] {
		return navmesh.ServerPath{Type: navmesh.PathNoPath, ActualEnd: from}, nil
	}
	return navmesh.ServerPath{Type: navmesh.PathNormal, Points: []navmesh.Vec3{from, to}, ActualEnd: to}, nil
}

// mkStagePlan builds a synthetic two-leg plan on map 1 with every point on the x axis.
func mkStagePlan() *Plan {
	return &Plan{
		Name:     "t",
		Map:      1,
		Entrance: Point{X: 0, Y: 0, Z: 0},
		Legs: []Leg{
			{Name: "a", To: Point{X: 60, Y: 0, Z: 0}},
			{Name: "b", To: Point{X: 120, Y: 0, Z: 0}},
		},
	}
}

// 1. The leg's first point stands clear of every pack: OK, kind leg_first, Point == F.
func TestStageLegFirstPointSafe(t *testing.T) {
	p := mkStagePlan()
	nav := &stageNav{}
	res, err := StageLeg(p, 1, nav, StageOptions{ClearedThrough: -1})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Kind != "leg_first" {
		t.Fatalf("not staged: %+v", res)
	}
	if res.Point != res.First {
		t.Errorf("Point %v != First %v", res.Point, res.First)
	}
}

// 2. The G1 case: a leg-0 boss 8 yd from leg 1's To. R1 refuses; the text
// names the boss; no before_leg proposal; earlier_leg proposes leg 0.
func TestStageRefusesALiveBossAtTheFirstPoint(t *testing.T) {
	p := mkStagePlan()
	p.Legs[0].Boss = &Boss{Entry: 7795, Position: Point{X: 112, Y: 0, Z: 0}}
	nav := &stageNav{}
	res, err := StageLeg(p, 1, nav, StageOptions{ClearedThrough: -1})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatalf("staged into a live boss: %+v", res)
	}
	if !strings.Contains(res.Refusal, "cannot be staged in a fresh instance") {
		t.Errorf("no R1 text: %q", res.Refusal)
	}
	if !strings.Contains(res.Refusal, "the boss of leg 0 a (entry 7795)") {
		t.Errorf("boss not named: %q", res.Refusal)
	}
	if res.Proposal != nil && res.Proposal.Kind == "before_leg" {
		t.Errorf("before_leg proposal for an R1 refusal: %+v", res.Proposal)
	}
	if res.Proposal == nil || res.Proposal.Kind != "earlier_leg" || res.Proposal.Leg != 0 {
		t.Errorf("no earlier_leg leg 0 proposal: %+v", res.Proposal)
	}
}

// 3. The BRD case: an own pull group stands at F. Refused with
// "pass use_safe_point"; the proposal is before_leg >= 20 yd back with NO
// earlier_leg proposal; with UseSafePoint it stages before_leg >= 20 yd
// from the pack.
func TestStageBacksOffFromAnOwnPack(t *testing.T) {
	p := mkStagePlan()
	p.Legs[1].PullGroups = []PullGroup{{Entries: []uint32{9319}, Positions: []Point{{X: 120, Y: 0, Z: 0}}}}
	nav := &stageNav{}
	res, err := StageLeg(p, 1, nav, StageOptions{ClearedThrough: -1})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatalf("staged onto an own pack: %+v", res)
	}
	if res.Proposal.BackYd < 20 {
		t.Errorf("back %.0f yd, want >= 20: %+v", res.Proposal.BackYd, res.Proposal)
	}
	if strings.Contains(res.Refusal, "Nearest earlier leg") {
		t.Errorf("before_leg found, yet the earlier-leg search ran: %q", res.Refusal)
	}
	ok, err := StageLeg(p, 1, nav, StageOptions{ClearedThrough: -1, UseSafePoint: true})
	if err != nil {
		t.Fatal(err)
	}
	if !ok.OK || ok.Kind != "before_leg" {
		t.Fatalf("not staged with use_safe_point: %+v", ok)
	}
	if dist3(ok.Point, Point{X: 120, Y: 0, Z: 0}) < 20 {
		t.Errorf("staged point too close to the pack: %v", ok.Point)
	}
}

// 4. A skipped pack 25 yd from F: the point passes the own check but the
// walk check fails, so the chosen point is >= 30 yd from the skipped pack.
func TestStageBackOffMeetsTheSkippedDistance(t *testing.T) {
	p := mkStagePlan()
	p.Legs[0].PullGroups = []PullGroup{{Entries: []uint32{5649}, Positions: []Point{{X: 120 + 25, Y: 0, Z: 0}}}}
	nav := &stageNav{}
	res, err := StageLeg(p, 1, nav, StageOptions{ClearedThrough: -1})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK && res.Kind == "leg_first" {
		t.Fatalf("leg_first 25 yd from a skipped pack: %+v", res)
	}
	if res.OK {
		if d := dist3(res.Point, Point{X: 145, Y: 0, Z: 0}); d < 30 {
			t.Errorf("staged point %.0f yd from skipped pack, want >= 30: %+v", d, res)
		}
		return
	}
	if res.Proposal == nil {
		t.Fatalf("refused with no proposal: %q", res.Refusal)
	}
	if d := dist3(res.Proposal.Point, Point{X: 145, Y: 0, Z: 0}); d < 30 {
		t.Errorf("proposal %.0f yd from skipped pack, want >= 30: %+v", d, res.Proposal)
	}
}

// 5. The G3 case: ServerPath F->N answers PathNoPath. Refused with
// "does not reach its next point"; Force does not pass it (warning added).

func TestStageRefusesAFirstPointCutOffFromItsNextPoint(t *testing.T) {
	p := mkStagePlan()
	nav := &stageNav{noPath: map[string]bool{stagePairKey(Point{X: 120, Y: 0, Z: 0}, Point{X: 150, Y: 0, Z: 0}): true}}
	p.Legs[1].PullGroups = []PullGroup{{Entries: []uint32{1}, Positions: []Point{{X: 150, Y: 0, Z: 0}}}}
	res, err := StageLeg(p, 1, nav, StageOptions{ClearedThrough: -1})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatalf("staged a cut-off leg: %+v", res)
	}
	if !strings.Contains(res.Refusal, "does not reach its next point") {
		t.Errorf("no R2 text: %q", res.Refusal)
	}
	forced, err := StageLeg(p, 1, nav, StageOptions{ClearedThrough: -1, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if forced.OK {
		t.Fatalf("force passed a cut-off leg: %+v", forced)
	}
	found := false
	for _, w := range forced.Warnings {
		if strings.Contains(w, "force does not pass a leg whose first point is cut off") {
			found = true
		}
	}
	if !found {
		t.Errorf("no force warning: %+v", forced.Warnings)
	}
}

// The reviewer's case: test-5 setup (ServerPath F->N PathNoPath) with
// FirstRow set to Via[0] itself. N falls through to the runLeaderWalk
// search and the walk F->N fails: refused R2.
func TestStageRefusesCutOffFirstRowOnVia(t *testing.T) {
	p := mkStagePlan()
	p.Legs[1].Via = []Waypoint{{Point: Point{X: 100, Y: 0, Z: 0}}}
	fr := Point{X: 100, Y: 0, Z: 0}
	nav := &stageNav{noPath: map[string]bool{stagePairKey(Point{X: 100, Y: 0, Z: 0}, Point{X: 120, Y: 0, Z: 0}): true}}
	res, err := StageLeg(p, 1, nav, StageOptions{ClearedThrough: -1, FirstRow: &fr})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatalf("staged a cut-off leg_first row: %+v", res)
	}
	if !strings.Contains(res.Refusal, "does not reach its next point") {
		t.Errorf("no R2 text: %q", res.Refusal)
	}
}

// 6. The first back-off candidate's ServerPath to F is PathNoPath: it is
// skipped and the second one is chosen. The own pack stands 18 yd past F,
// so the x=115 candidate passes the own-pack check and only the step-4
// reach fault rejects it (drop that fault and this test fails: 115 is
// chosen).
func TestStageSkipsACandidateOnAnotherFloor(t *testing.T) {
	p := mkStagePlan()
	p.Legs[1].PullGroups = []PullGroup{{Entries: []uint32{9319}, Positions: []Point{{X: 138, Y: 0, Z: 0}}}}
	nav := &stageNav{noPath: map[string]bool{stagePairKey(Point{X: 115, Y: 0, Z: 0}, Point{X: 120, Y: 0, Z: 0}): true}}
	res, err := StageLeg(p, 1, nav, StageOptions{ClearedThrough: -1, UseSafePoint: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("refused: %q", res.Refusal)
	}
	if res.Point.X > 114.5 {
		t.Errorf("chose the cut-off candidate %v", res.Point)
	}
}

// 7. Case 2 with ClearedThrough 0: the leg-0 boss counts as cleared -> OK leg_first.
func TestStageClearedThroughDropsSkippedPacks(t *testing.T) {
	p := mkStagePlan()
	p.Legs[0].Boss = &Boss{Entry: 7795, Position: Point{X: 112, Y: 0, Z: 0}}
	nav := &stageNav{}
	res, err := StageLeg(p, 1, nav, StageOptions{ClearedThrough: 0})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Kind != "leg_first" {
		t.Fatalf("not staged with cleared_through 0: %+v", res)
	}
}

// 8. Case 3 with Force: OK forced, Warnings[0] is the refusal.
func TestStageForceStagesAtTheFirstPoint(t *testing.T) {
	p := mkStagePlan()
	p.Legs[1].PullGroups = []PullGroup{{Entries: []uint32{9319}, Positions: []Point{{X: 120, Y: 0, Z: 0}}}}
	nav := &stageNav{}
	plain, err := StageLeg(p, 1, nav, StageOptions{ClearedThrough: -1})
	if err != nil {
		t.Fatal(err)
	}
	if plain.OK {
		t.Fatalf("staged onto an own pack: %+v", plain)
	}
	res, err := StageLeg(p, 1, nav, StageOptions{ClearedThrough: -1, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Kind != "forced" {
		t.Fatalf("not forced: %+v", res)
	}
	if len(res.Warnings) == 0 || res.Warnings[0] != plain.Refusal {
		t.Errorf("Warnings[0] %q != refusal %q", firstStr(res.Warnings), plain.Refusal)
	}
}

func firstStr(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	return ss[0]
}

// 9. A skipped group's patrol path point 10 yd from F -> R1 naming "the patrol of pull group".
func TestStagePatrolPathCounts(t *testing.T) {
	p := mkStagePlan()
	p.Legs[0].PullGroups = []PullGroup{{
		Entries:   []uint32{5649},
		Positions: []Point{{X: 300, Y: 0, Z: 0}},
		Patrol:    &Patrol{Entries: []uint32{5650}, Path: []Point{{X: 130, Y: 0, Z: 0}}},
	}}
	nav := &stageNav{}
	res, err := StageLeg(p, 1, nav, StageOptions{ClearedThrough: -1})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatalf("staged with a patrol at the first point: %+v", res)
	}
	if !strings.Contains(res.Refusal, "the patrol of pull group 1 of leg 0 a (entry 5650)") {
		t.Errorf("patrol not named: %q", res.Refusal)
	}
}

// 10. A navmesh without the server query cannot replay the server's path:
// error "cannot replay the server's path query".
func TestStageNeedsTheServerQuery(t *testing.T) {
	p := mkStagePlan()
	_, err := StageLeg(p, 1, fakeNav{}, StageOptions{ClearedThrough: -1})
	if err == nil || !strings.Contains(err.Error(), "cannot replay the server's path query") {
		t.Fatalf("want the server-query error, got %v", err)
	}
}

// 11. FirstRow set 50 yd before Via[0]: First == FirstRow, and N == LegFirstPoint.
func TestStageFirstRowIsUsed(t *testing.T) {
	p := mkStagePlan()
	p.Legs[1].Via = []Waypoint{{Point: Point{X: 100, Y: 0, Z: 0}}}
	fr := Point{X: 50, Y: 0, Z: 0}
	nav := &stageNav{}
	res, err := StageLeg(p, 1, nav, StageOptions{ClearedThrough: -1, FirstRow: &fr})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("refused: %q", res.Refusal)
	}
	if res.First != fr {
		t.Errorf("First %v != FirstRow %v", res.First, fr)
	}
}

// TestStageWalkPassesASkippedPack: the candidate is clean but the server
// walk to the first point passes 10 yd from a skipped pack. The walk
// check (step 5) must refuse: no back-off point's walk avoids the pack.
func TestStageWalkPassesASkippedPack(t *testing.T) {
	p := mkStagePlan()
	p.Legs[0].PullGroups = []PullGroup{{Entries: []uint32{5649}, Positions: []Point{{X: 105, Y: 15, Z: 0}}}}
	nav := &stageNav{}
	res, err := StageLeg(p, 1, nav, StageOptions{ClearedThrough: -1})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatalf("staged a walk past a skipped pack: %+v", res)
	}
	if !strings.Contains(res.Refusal, "no point up to 120 yd back") {
		t.Errorf("no back-off-exhausted refusal: %q", res.Refusal)
	}
}
