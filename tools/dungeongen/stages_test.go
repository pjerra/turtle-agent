package main

import (
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// stagedFixture is a raid plan of one staged group: formation A (guid 1, entry 10) at (100,0) peeled by stage 1, formation B (guid 2, entry
// 11) at (100,60) peeled by stage 2. Each tag spot stands 23 yd from its pack (in the shot, outside the tank's 21.5 yd wake reach + 1), each
// camp lies on the cleared route (the leg's `to` is stage 1's camp) and stage 2's camp is 7 yd from stage 1's.
func stagedFixture() (*dungeon.Plan, []creature) {
	p := &dungeon.Plan{Name: "probe", Map: 1, Kind: dungeon.KindRaid, LevelBand: []int{60, 60}, Entrance: dungeon.Point{},
		Legs: []dungeon.Leg{{
			Name: "a", To: dungeon.Point{X: 60},
			PullGroups: []dungeon.PullGroup{{
				Entries: []uint32{10, 11}, Positions: []dungeon.Point{{X: 100}, {X: 100, Y: 60}}, Radius: 10,
				Stages: []dungeon.Stage{
					{Peel: []uint32{1}, Tag: []dungeon.Point{{X: 77}}, Camp: dungeon.Point{X: 60}},
					{Peel: []uint32{2}, Tag: []dungeon.Point{{X: 100, Y: 37}}, Camp: dungeon.Point{X: 65, Y: 5}},
				},
			}},
		}}}
	cs := []creature{
		{GUID: 1, Entry: 10, Name: "A", LevelMax: 60, Faction: 14, DetectionRange: 20, Pos: dungeon.Point{X: 100}},
		{GUID: 2, Entry: 11, Name: "B", LevelMax: 60, Faction: 14, DetectionRange: 20, Pos: dungeon.Point{X: 100, Y: 60}},
	}
	return p, cs
}

func stageFindings(t *testing.T, p *dungeon.Plan, cs []creature) []string {
	t.Helper()
	got, err := StageFindings(p, cs, nil, map[int]bool{35: true})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func hasFinding(got []string, parts ...string) bool {
	for _, s := range got {
		ok := true
		for _, p := range parts {
			ok = ok && strings.Contains(s, p)
		}
		if ok {
			return true
		}
	}
	return false
}

// The fixture is clean: no stage finding, and the camp chains the stages export as pass the camp lint (shot, body pull, tank walk, cleared
// route) too: rules 4 and 5 of the stage lint are CampPullFindings' on the stage rows.
func TestStageFixtureIsClean(t *testing.T) {
	p, cs := stagedFixture()
	if got := stageFindings(t, p, cs); len(got) != 0 {
		t.Fatalf("a clean staged group has findings: %v", got)
	}
	camped, err := CampPullFindings(p, cs, map[int]bool{35: true})
	if err != nil || len(camped) != 0 {
		t.Fatalf("camp lint on the stage rows: %v %v", camped, err)
	}
}

// Rule 1: a stage's camp stands at least 40 yd (3D) from every caster spawn of its own and the later stages' formations. A camp 39.9 yd from
// an 11830 spawn is an error; 40.0 is fine. An earlier stage's caster (dead by then) is no concern, and a caster entry not in the plan's
// list is none.
func TestStageCasterDistance(t *testing.T) {
	p, cs := stagedFixture()
	p.CasterEntries = []uint32{11830}
	cs[0].Entry = 11830 // formation A is a caster stage 1 pulls
	p.Legs[0].PullGroups[0].Entries = []uint32{11830, 11}
	for _, tc := range []struct {
		campX float32
		bad   bool
	}{{60.1, true}, {60, false}} {
		p.Legs[0].PullGroups[0].Stages[0].Camp = dungeon.Point{X: tc.campX}
		p.Legs[0].PullGroups[0].Stages[0].Tag = []dungeon.Point{{X: tc.campX + 17}}
		got := stageFindings(t, p, cs)
		if bad := hasFinding(got, "caster", "stage 1/2"); bad != tc.bad {
			t.Errorf("camp at x=%g (%.1f yd from the caster): finding %v, want %v: %v", tc.campX, 100-tc.campX, bad, tc.bad, got)
		}
	}
	// Stage 1's caster is not stage 2's concern, but a later stage's caster is stage 1's: B as the caster, 39.9 yd from stage 1's camp.
	p, cs = stagedFixture()
	p.CasterEntries = []uint32{11}
	cs[1].Pos = dungeon.Point{X: 60, Y: 39.9}
	p.Legs[0].PullGroups[0].Positions[1] = cs[1].Pos
	p.Legs[0].PullGroups[0].Stages[1].Tag = []dungeon.Point{{X: 60, Y: 17}}
	p.Legs[0].PullGroups[0].Stages[1].Camp = dungeon.Point{X: 60, Y: 5}
	if got := stageFindings(t, p, cs); !hasFinding(got, "stage 1/2", "caster", "guid 2") {
		t.Errorf("a later stage's caster 39.9 yd from stage 1's camp is no finding: %v", got)
	}
	cs[1].Pos = dungeon.Point{X: 60, Y: 40}
	p.Legs[0].PullGroups[0].Positions[1] = cs[1].Pos
	if got := stageFindings(t, p, cs); hasFinding(got, "stage 1/2", "caster") {
		t.Errorf("40.0 yd is far enough: %v", got)
	}
}

// Rule 2: the tag spot wakes exactly the stage's formations. Formations 16.8 yd apart (inside the 20 yd ring): a stage that peels one
// pulls the other with it, and the finding names the other's leader and the Ring1 reason. The second stage, which peels the other, is clean
// once the first is dead.
func TestStageOnePackPerTag(t *testing.T) {
	p, cs := stagedFixture()
	cs[1].Pos = dungeon.Point{X: 100, Y: 16.8}
	p.Legs[0].PullGroups[0].Positions[1] = cs[1].Pos
	p.Legs[0].PullGroups[0].Radius = 30
	got := stageFindings(t, p, cs)
	if !hasFinding(got, "stage 1/2", "guid 2", "Ring1") {
		t.Fatalf("stage 1 wakes formation B through the ring and nothing says so: %v", got)
	}
	if hasFinding(got, "stage 2/2", "tag spot wakes") {
		t.Fatalf("stage 2 (A dead) wakes nothing foreign: %v", got)
	}
	// Past the ring (21 yd) the stages split cleanly.
	cs[1].Pos = dungeon.Point{X: 100, Y: 21}
	p.Legs[0].PullGroups[0].Positions[1] = cs[1].Pos
	p.Legs[0].PullGroups[0].Stages[1].Tag = []dungeon.Point{{X: 100, Y: -2}}
	p.Legs[0].PullGroups[0].Stages[1].Camp = dungeon.Point{X: 65, Y: 5}
	if got := stageFindings(t, p, cs); hasFinding(got, "Ring1") || hasFinding(got, "tag spot wakes") {
		t.Fatalf("formations 21 yd apart are two packs: %v", got)
	}
}

// A formation the tag cannot bring (the stage peels two formations, the ring reaches only one) is a finding too: the pull row would wait for
// units that never come.
func TestStageOwnFormationNotWoken(t *testing.T) {
	p, cs := stagedFixture()
	p.Legs[0].PullGroups[0].Stages = []dungeon.Stage{
		{Peel: []uint32{1, 2}, Tag: []dungeon.Point{{X: 77}}, Camp: dungeon.Point{X: 60}},
	}
	if got := stageFindings(t, p, cs); !hasFinding(got, "stage 1/1", "led by 2", "not woken") {
		t.Fatalf("a peeled formation 60 yd from the tag, never woken: %v", got)
	}
}

// Rule 3: the camp's fight point (raid spread 10) wakes no later stage's or later pull group's spawn: reach 20 + 10 + wander.
func TestStageCampClearOfLaterPacks(t *testing.T) {
	p, cs := stagedFixture()
	// Stage 2's pack moved to stand 29 yd from stage 1's camp (inside 30), then 31 yd (outside).
	for _, tc := range []struct {
		y   float32
		bad bool
	}{{29, true}, {31, false}} {
		cs[1].Pos = dungeon.Point{X: 60, Y: tc.y}
		p.Legs[0].PullGroups[0].Positions[1] = cs[1].Pos
		p.Legs[0].PullGroups[0].Stages[1].Tag = []dungeon.Point{{X: 60, Y: tc.y - 23}}
		p.Legs[0].PullGroups[0].Stages[1].Camp = dungeon.Point{X: 64, Y: 2}
		got := stageFindings(t, p, cs)
		if bad := hasFinding(got, "stage 1/2", "camp wakes", "guid 2"); bad != tc.bad {
			t.Errorf("later pack %g yd from the camp: finding %v, want %v: %v", tc.y, bad, tc.bad, got)
		}
	}
	// A later pull group's spawn counts the same.
	p, cs = stagedFixture()
	p.Legs[0].PullGroups = append(p.Legs[0].PullGroups, dungeon.PullGroup{Entries: []uint32{12}, Positions: []dungeon.Point{{X: 60, Y: -25}}, Radius: 5})
	cs = append(cs, creature{GUID: 3, Entry: 12, Name: "C", LevelMax: 60, Faction: 14, DetectionRange: 20, Pos: dungeon.Point{X: 60, Y: -25}})
	if got := stageFindings(t, p, cs); !hasFinding(got, "stage 1/2", "camp wakes", "guid 3") {
		t.Errorf("a later pull group 25 yd from the camp is no finding: %v", got)
	}
}

// Every formation of the group is peeled by exactly one stage, each peel guid is a formation leader of the group, and the tag spot stands
// within the run leader's sight of its stage.
func TestStageFormationsPeeledOnce(t *testing.T) {
	p, cs := stagedFixture()
	p.Legs[0].PullGroups[0].Stages = p.Legs[0].PullGroups[0].Stages[:1]
	if got := stageFindings(t, p, cs); !hasFinding(got, "led by 2", "peeled by no stage") {
		t.Errorf("formation B is in the group and peeled by no stage: %v", got)
	}
	p, cs = stagedFixture()
	cs = append(cs, creature{GUID: 5, Entry: 10, Name: "A2", LevelMax: 60, Faction: 14, DetectionRange: 20, Pos: dungeon.Point{X: 103}, GroupLeader: 4},
		creature{GUID: 4, Entry: 10, Name: "A1", LevelMax: 60, Faction: 14, DetectionRange: 20, Pos: dungeon.Point{X: 101}, GroupLeader: 4})
	p.Legs[0].PullGroups[0].Stages[0].Peel = []uint32{5}
	if got := stageFindings(t, p, cs); !hasFinding(got, "peel 5", "not a formation leader", "4") {
		t.Errorf("a member's guid as the peel: %v", got)
	}
	p, cs = stagedFixture()
	p.Legs[0].PullGroups[0].Stages[0].Peel = []uint32{99}
	if _, err := StageFindings(p, cs, nil, map[int]bool{35: true}); err == nil || !strings.Contains(err.Error(), "99") {
		t.Errorf("an unknown peel guid is an error naming it: %v", err)
	}
	p, cs = stagedFixture()
	p.Legs[0].PullGroups[0].Positions = []dungeon.Point{{X: 100}}
	p.Legs[0].PullGroups[0].Radius = 10
	if got := stageFindings(t, p, cs); !hasFinding(got, "peel 2", "not in the group") {
		t.Errorf("a leader 60 yd off the group's positions: %v", got)
	}
	p, cs = stagedFixture()
	p.Legs[0].PullGroups[0].Stages[0].Tag = []dungeon.Point{{X: 36}}
	p.Legs[0].PullGroups[0].Stages[0].Camp = dungeon.Point{X: 60}
	// plan-export refuses such a stage too, so the lint reports it and the export error follows.
	if got, err := StageFindings(p, cs, nil, map[int]bool{35: true}); err == nil || !hasFinding(got, "stage 1/2", "tag spot", "64.0 yd") {
		t.Errorf("a tag spot 64 yd from its stage: %v (export error %v)", got, err)
	}
}

// A plan without a staged group has no stage findings and needs no export (the lint is a no-op on today's plans).
func TestStageFindingsNoStagesIsQuiet(t *testing.T) {
	p, cs := stagedFixture()
	p.Legs[0].PullGroups[0].Stages = nil
	if got, err := StageFindings(p, cs, nil, nil); err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
}

// Rules 4 and 5 reach the stage rows through CampPullFindings: a tag spot 18 yd from its pack body-pulls it, and a camp 90 yd off the
// cleared route is not on it.
func TestStageRowsAreCampLinted(t *testing.T) {
	p, cs := stagedFixture()
	p.Legs[0].PullGroups[0].Stages[0].Tag = []dungeon.Point{{X: 82}}
	got, err := CampPullFindings(p, cs, map[int]bool{35: true})
	if err != nil || !hasFinding(got, "body-pulls its own pack", "stage 1/2") {
		t.Fatalf("a tag 18 yd from the pack: %v %v", got, err)
	}
	p, cs = stagedFixture()
	p.Legs[0].PullGroups[0].Stages[1].Camp = dungeon.Point{X: 100, Y: 90}
	got, err = CampPullFindings(p, cs, map[int]bool{35: true})
	if err != nil || !hasFinding(got, "not on the cleared route", "stage 2/2") {
		t.Fatalf("a camp 90 yd off the route: %v %v", got, err)
	}
}
