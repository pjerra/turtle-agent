package aggroest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

func pt(x, y, z float32) dungeon.Point { return dungeon.Point{X: x, Y: y, Z: z} }

func pt3(x, y, z float32) dungeon.Point { return pt(x, y, z) }

func alwaysSees(a, b dungeon.Point) bool { return true }

// The TB constants this package mirrors (PullAggroEstimatePolicy.h):
// kAggroRingYd = 20, kAggroRingHops = 2, kAggroFloorDz = 12, walk 3,
// stand/fight 3 party / 10 raid (ai::kSpreadSettledDistance), tank walk 1.5.
func TestConstantsMirrorTheRunLeader(t *testing.T) {
	if RingYd != 20 || RingHops != 2 || FloorDz != 12 || WalkSpreadYd != 3 ||
		StandSpreadPartyYd != 3 || StandSpreadRaidYd != 10 || TankSpreadYd != 1.5 ||
		MaxPullBodies != 80 {
		t.Fatalf("constants %v %v %v %v %v %v %v %v: want 20 2 12 3 3 10 1.5 80",
			RingYd, RingHops, FloorDz, WalkSpreadYd, StandSpreadPartyYd, StandSpreadRaidYd, TankSpreadYd, MaxPullBodies)
	}
}

func TestSharedFixtureListsTheNineCases(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "aggroest_cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Cases []struct {
			Name string `json:"name"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ring_calls_from_every_member_not_only_the_target",
		"ring_two_hops_then_stops",
		"ring_needs_sight_and_floor_band",
		"walk_proximity_wakes_a_pack_off_the_stand",
		"fight_point_uses_the_raid_spread",
		"drag_sweep_calls_along_the_way_back",
		"members_carry_guids_and_reasons",
		"old_estimator_unchanged",
		"pull_ceiling_bodies",
	}
	if len(fx.Cases) != len(want) {
		t.Fatalf("%d cases, want %d", len(fx.Cases), len(want))
	}
	for i, w := range want {
		if fx.Cases[i].Name != w {
			t.Fatalf("case %d is %q, want %q", i, fx.Cases[i].Name, w)
		}
	}
}

// The target's pack: two mobs at x=0 and x=8, same faction. Pack B is one mob
// 15 yd from the pack's second member (x=8) and 23 yd from the target,
// same faction. Old rule (target-only, 10 yd): 2 bodies. New: 3, B is Ring1.
func TestRingCallsFromEveryMemberNotOnlyTheTarget(t *testing.T) {
	mobs := []Mob{
		{Guid: 1, Pos: pt(0, 0, 0), Level: 60, DetectionYd: 20, Faction: 14},
		{Guid: 2, Pos: pt(8, 0, 0), Level: 60, DetectionYd: 20, Faction: 14, GroupID: 1},
		{Guid: 3, Pos: pt(23, 0, 0), Level: 60, DetectionYd: 20, Faction: 14},
	}
	mobs[1].GroupID = 100
	// Pack A = {0, 1} by group: give both the same GroupID instead.
	mobs[0].GroupID, mobs[1].GroupID = 100, 100
	res := Estimate(mobs, 0, Geometry{Stand: pt(200, 0, 0)}, Params{PartyLevel: 60, Rate: 1, RingYd: 20, RingHops: 2, FloorDz: 12, Sees: alwaysSees})
	if res.Bodies != 3 {
		t.Fatalf("bodies %d, want 3", res.Bodies)
	}
	if got := whyOf(res, 2); got != WhyRing1 {
		t.Fatalf("B reason %v, want Ring1", got)
	}
}

// A -> B (18 yd) -> C (18 yd) -> D (18 yd), same faction, no packs: bodies
// A+B+C, D out. With RingHops 3, D joins.
func TestRingTwoHopsThenStops(t *testing.T) {
	mk := func(hops int) Result {
		mobs := []Mob{
			{Guid: 1, Pos: pt(0, 0, 0), Level: 60, DetectionYd: 20, Faction: 14},
			{Guid: 2, Pos: pt(18, 0, 0), Level: 60, DetectionYd: 20, Faction: 14},
			{Guid: 3, Pos: pt(36, 0, 0), Level: 60, DetectionYd: 20, Faction: 14},
			{Guid: 4, Pos: pt(54, 0, 0), Level: 60, DetectionYd: 20, Faction: 14},
		}
		return Estimate(mobs, 0, Geometry{Stand: pt(200, 0, 0)}, Params{PartyLevel: 60, Rate: 1, RingYd: 20, RingHops: hops, FloorDz: 12, Sees: alwaysSees})
	}
	if got := mk(2); got.Bodies != 3 {
		t.Fatalf("2 hops: bodies %d, want 3", got.Bodies)
	}
	if got := mk(3); got.Bodies != 4 {
		t.Fatalf("3 hops: bodies %d, want 4", got.Bodies)
	}
	// A 10 yd ring stops after the first hop (Arlokk pin: the observed
	// cross-pack wakes are 11.0-19.5 yd apart, outside the server's 10 yd
	// family-assist ring).
	mobs10 := []Mob{
		{Guid: 1, Pos: pt(0, 0, 0), Level: 60, DetectionYd: 20, Faction: 14},
		{Guid: 2, Pos: pt(18, 0, 0), Level: 60, DetectionYd: 20, Faction: 14},
		{Guid: 3, Pos: pt(36, 0, 0), Level: 60, DetectionYd: 20, Faction: 14},
		{Guid: 4, Pos: pt(54, 0, 0), Level: 60, DetectionYd: 20, Faction: 14},
	}
	if got := Estimate(mobs10, 0, Geometry{Stand: pt(200, 0, 0)}, Params{PartyLevel: 60, Rate: 1, RingYd: 10, RingHops: 2, FloorDz: 12, Sees: alwaysSees}); got.Bodies != 1 {
		t.Fatalf("ring 10: bodies %d, want 1", got.Bodies)
	}
}

// B at 15 yd but out of sight: out. B at 15 yd with dz 13: out. dz 11: in
// (Arlokk pin: G12 z71.4 -> G15 z60.4).
func TestRingNeedsSightAndFloorBand(t *testing.T) {
	mk := func(blind bool, bz float32) Result {
		mobs := []Mob{
			{Guid: 1, Pos: pt(0, 0, 0), Level: 60, DetectionYd: 20, Faction: 14},
			{Guid: 2, Pos: pt3(15, 0, bz), Level: 60, DetectionYd: 20, Faction: 14},
		}
		p := Params{PartyLevel: 60, Rate: 1, RingYd: 20, RingHops: 2, FloorDz: 12, Sees: alwaysSees}
		if blind {
			p.Sees = func(a, b MobPoint) bool { return false }
		}
		return Estimate(mobs, 0, Geometry{Stand: pt(200, 0, 0)}, p)
	}
	if got := mk(true, 0); got.Bodies != 1 {
		t.Fatalf("blind: bodies %d, want 1", got.Bodies)
	}
	if got := mk(false, 13); got.Bodies != 1 {
		t.Fatalf("dz 13: bodies %d, want 1", got.Bodies)
	}
	if got := mk(false, 11); got.Bodies != 2 {
		t.Fatalf("dz 11: bodies %d, want 2", got.Bodies)
	}
}

// Stand 40 yd from pack C; a walk sample 21 yd from C's level-60 unit at party
// level 60 with walk spread 3: C joins (Walk). Sample at 23.1 yd: out.
// (Arlokk step 317 pin: 21.3 yd.)
func TestWalkProximityWakesAPackOffTheStand(t *testing.T) {
	mk := func(wx float32) Result {
		mobs := []Mob{
			{Guid: 1, Pos: pt(0, 0, 0), Level: 60, DetectionYd: 20, Faction: 14},
			{Guid: 2, Pos: pt(100, 0, 0), Level: 60, DetectionYd: 20, Faction: 15},
		}
		g := Geometry{
			Walk: []MobPoint{pt(wx, 0, 0)}, WalkSpreadYd: 3,
			Stand: pt(140, 0, 0), StandSpreadYd: 3,
		}
		return Estimate(mobs, 0, g, Params{PartyLevel: 60, Rate: 1, RingYd: 20, RingHops: 2, FloorDz: 12, Sees: alwaysSees})
	}
	// C's reach at 60 v 60 is 20; spread 3 wakes at <= 23. 21 yd: in.
	if got := mk(79); got.Bodies != 2 {
		t.Fatalf("21 yd: bodies %d, want 2 (C woke)", got.Bodies)
	} else if whyOf(got, 1) != WhyWalk {
		t.Fatalf("21 yd: C reason %v, want Walk", whyOf(got, 1))
	}
	// 23.1 yd: out.
	if got := mk(76.9); got.Bodies != 1 {
		t.Fatalf("23.1 yd: bodies %d, want 1", got.Bodies)
	}
}

// Fight point 28 yd from a level-60 mob (reach 20): spread 3 out, spread 10 in.
func TestFightPointUsesTheRaidSpread(t *testing.T) {
	mk := func(spread float32) Result {
		mobs := []Mob{
			{Guid: 1, Pos: pt(0, 0, 0), Level: 60, DetectionYd: 20, Faction: 14},
			{Guid: 2, Pos: pt(100, 0, 0), Level: 60, DetectionYd: 20, Faction: 15},
		}
		g := Geometry{
			Stand: pt(200, 0, 0), StandSpreadYd: 3,
			Fight: pt3(128, 0, 0), HasFight: true, FightSpreadYd: spread,
		}
		return Estimate(mobs, 0, g, Params{PartyLevel: 60, Rate: 1, RingYd: 20, RingHops: 2, FloorDz: 12, Sees: alwaysSees})
	}
	if got := mk(3); got.Bodies != 1 {
		t.Fatalf("spread 3: bodies %d, want 1", got.Bodies)
	}
	if got := mk(10); got.Bodies != 2 {
		t.Fatalf("spread 10: bodies %d, want 2", got.Bodies)
	} else if whyOf(got, 1) != WhyFight {
		t.Fatalf("spread 10: reason %v, want Fight", whyOf(got, 1))
	}
}

// No proximity; a pack D 15 yd off the drag polyline's midpoint joins (Drag);
// the same pack 25 yd off stays out.
func TestDragSweepCallsAlongTheWayBack(t *testing.T) {
	mk := func(dy float32) Result {
		mobs := []Mob{
			{Guid: 1, Pos: pt(0, 0, 0), Level: 60, DetectionYd: 20, Faction: 14},
			{Guid: 2, Pos: pt3(50, dy, 0), Level: 60, DetectionYd: 20, Faction: 14},
		}
		g := Geometry{
			Stand: pt(200, 0, 0),
			Drag:  []MobPoint{pt(0, 0, 0), pt(100, 0, 0)},
		}
		return Estimate(mobs, 0, g, Params{PartyLevel: 60, Rate: 1, RingYd: 20, RingHops: 2, FloorDz: 12, Sees: alwaysSees})
	}
	if got := mk(15); got.Bodies != 2 {
		t.Fatalf("15 yd off the drag: bodies %d, want 2", got.Bodies)
	} else if whyOf(got, 1) != WhyDrag {
		t.Fatalf("15 yd off the drag: reason %v, want Drag", whyOf(got, 1))
	}
	if got := mk(25); got.Bodies != 1 {
		t.Fatalf("25 yd off the drag: bodies %d, want 1", got.Bodies)
	}
}

// The member list is insertion order; the target is first with Target.
func TestMembersCarryGuidsAndReasons(t *testing.T) {
	mobs := []Mob{
		{Guid: 7, Pos: pt(0, 0, 0), Level: 60, DetectionYd: 20, Faction: 14, GroupID: 9},
		{Guid: 8, Pos: pt(5, 0, 0), Level: 60, DetectionYd: 20, Faction: 14, GroupID: 9},
	}
	res := Estimate(mobs, 0, Geometry{Stand: pt(200, 0, 0)}, Params{PartyLevel: 60, Rate: 1, RingYd: 20, RingHops: 2, FloorDz: 12, Sees: alwaysSees})
	if len(res.Members) != 2 {
		t.Fatalf("%d members, want 2", len(res.Members))
	}
	if res.Members[0].Guid != 7 || res.Members[0].Why != WhyTarget {
		t.Fatalf("first %+v, want guid 7 Target", res.Members[0])
	}
	if res.Members[1].Guid != 8 || res.Members[1].Why != WhyPack {
		t.Fatalf("second %+v, want guid 8 Pack", res.Members[1])
	}
}

// The old estimator is untouched: with no geometry, only the target's pack joins.
func TestOldEstimatorUnchanged(t *testing.T) {
	mobs := []Mob{
		{Guid: 1, Pos: pt(0, 0, 0), Level: 60, DetectionYd: 20, Faction: 14},
		{Guid: 2, Pos: pt(15, 0, 0), Level: 60, DetectionYd: 20, Faction: 14},
	}
	res := Estimate(mobs, 0, Geometry{Stand: pt(200, 0, 0)}, Params{PartyLevel: 60, Rate: 1, RingYd: 20, RingHops: 2, FloorDz: 12, Sees: alwaysSees})
	if res.Bodies != 2 {
		t.Fatalf("ring off geometry but in range: bodies %d, want 2", res.Bodies)
	}
	far := []Mob{
		{Guid: 1, Pos: pt(0, 0, 0), Level: 60, DetectionYd: 20, Faction: 14},
		{Guid: 2, Pos: pt(50, 0, 0), Level: 60, DetectionYd: 20, Faction: 15},
	}
	if got := Estimate(far, 0, Geometry{Stand: pt(200, 0, 0)}, Params{PartyLevel: 60, Rate: 1, RingYd: 20, RingHops: 2, FloorDz: 12, Sees: alwaysSees}); got.Bodies != 1 {
		t.Fatalf("far mob: bodies %d, want 1", got.Bodies)
	}
}

func TestPullCeilingBodies(t *testing.T) {
	if got := PullCeilingBodies(0, 6); got != 7 {
		t.Fatalf("(0,6) = %d, want 7", got)
	}
	if got := PullCeilingBodies(27, 6); got != 27 {
		t.Fatalf("(27,6) = %d, want 27", got)
	}
	if got := PullCeilingBodies(0, 0); got != 0 {
		t.Fatalf("(0,0) = %d, want 0", got)
	}
}

func whyOf(r Result, idx int) Why {
	for _, m := range r.Members {
		if m.Index == idx {
			return m.Why
		}
	}
	return Why(-1)
}
