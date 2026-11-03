package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// A search on stage 1 of the staged fixture finds pairs, and every pair keeps the rules it was searched for: the drag within the cap, the tag
// spot in the shot and outside the wake reach, the camp on the cleared route. Put into the plan, the best pair passes both lints.
func TestCampSearchFindsPointsTheLintAccepts(t *testing.T) {
	p, cs := stagedFixture()
	res, err := CampSearch(p, cs, nil, 0, 1, 1, CampSearchOptions{Top: 5})
	if err != nil {
		t.Fatal(err)
	}
	if res.Camps == 0 || res.Tags == 0 || len(res.Candidates) == 0 || len(res.Candidates) > 5 {
		t.Fatalf("camps %d tags %d candidates %d", res.Camps, res.Tags, len(res.Candidates))
	}
	for i, c := range res.Candidates {
		if c.DragYd > dungeon.MaxCampDragYd || c.ShotYd > campShotYd || c.WakeYd < 0 || c.RouteYd > campRouteYd || c.Bodies != 1 {
			t.Errorf("candidate %d breaks a rule: %+v", i, c)
		}
		if i > 0 && c.DragYd < res.Candidates[i-1].DragYd {
			t.Errorf("candidates are not ranked by drag length: %+v after %+v", c, res.Candidates[i-1])
		}
	}
	best := res.Candidates[0]
	p.Legs[0].PullGroups[0].Stages[0].Camp = best.Camp
	p.Legs[0].PullGroups[0].Stages[0].Tag = []dungeon.Point{best.Tag}
	if got := stageFindings(t, p, cs); len(got) != 0 {
		t.Errorf("the best pair %+v has stage findings: %v", best, got)
	}
	if got, err := CampPullFindings(p, cs, map[int]bool{35: true}); err != nil || len(got) != 0 {
		t.Errorf("the best pair %+v has camp findings: %v %v", best, got, err)
	}
	out := res.Format()
	for _, want := range []string{"camp-search probe", "stage 1", "no mmaps", "\"camp\": {\"x\":", "\"tag\": [{\"x\":"} {
		if !strings.Contains(out, want) {
			t.Errorf("the printed result lacks %q:\n%s", want, out)
		}
	}
}

// The caster rule shapes the camps: with the first stage's pack a caster, no camp is within 40 yd of it; with the caster 20 yd from the whole
// cleared route there is no camp at all and the search says so.
func TestCampSearchKeepsCampsFromCasters(t *testing.T) {
	p, cs := stagedFixture()
	p.CasterEntries = []uint32{10}
	res, err := CampSearch(p, cs, nil, 0, 1, 1, CampSearchOptions{Top: 5})
	if err != nil {
		t.Fatal(err)
	}
	if res.Casters != 1 || len(res.Candidates) == 0 {
		t.Fatalf("casters %d candidates %d", res.Casters, len(res.Candidates))
	}
	for _, c := range res.Candidates {
		if d := dist(c.Camp, cs[0].Pos); d < dungeon.CasterCampYd || c.CasterYd < dungeon.CasterCampYd {
			t.Errorf("camp %v is %.1f yd from the caster (reported %.1f)", c.Camp, d, c.CasterYd)
		}
	}
	// The whole cleared route within 10 yd of (60,0) is 80 yd from a caster standing at (140,0)? no: put the caster beside the route.
	cs[0].Pos = dungeon.Point{X: 70}
	p.Legs[0].PullGroups[0].Positions[0] = cs[0].Pos
	res, err = CampSearch(p, cs, nil, 0, 1, 1, CampSearchOptions{Top: 5})
	if err != nil {
		t.Fatal(err)
	}
	if res.Camps != 0 || len(res.Candidates) != 0 || !strings.Contains(res.Format(), "no candidate") {
		t.Errorf("a caster beside the route leaves no camp: camps %d candidates %d", res.Camps, len(res.Candidates))
	}
}

// A group without stages is searched as one pack; a stage number on it is an error, as is a stage past the list.
func TestCampSearchUnstagedGroupAndBadArguments(t *testing.T) {
	p, cs := stagedFixture()
	p.Legs[0].PullGroups[0].Stages = nil
	p.Legs[0].PullGroups[0].Positions = p.Legs[0].PullGroups[0].Positions[:1]
	p.Legs[0].PullGroups[0].Entries = []uint32{10}
	cs = cs[:1]
	res, err := CampSearch(p, cs, nil, 0, 1, 0, CampSearchOptions{})
	if err != nil || len(res.Candidates) == 0 || res.Staged {
		t.Fatalf("unstaged search: %v %+v", err, res)
	}
	for name, args := range map[string][3]int{"stage on unstaged": {0, 1, 1}, "leg": {3, 1, 0}, "group": {0, 4, 0}} {
		if _, err := CampSearch(p, cs, nil, args[0], args[1], args[2], CampSearchOptions{}); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	p, cs = stagedFixture()
	if _, err := CampSearch(p, cs, nil, 0, 1, 3, CampSearchOptions{}); err == nil || !strings.Contains(err.Error(), "stages 1-2") {
		t.Errorf("stage 3 of 2: %v", err)
	}
}

// The command reads the plan and the world and never writes: the plan file is the same bytes afterwards.
func TestCampSearchCommandIsReadOnly(t *testing.T) {
	dir := t.TempDir()
	hdr := "guid\tentry\tname\tlevel_min\tlevel_max\trank\tfaction\tnpc_flags\tx\ty\tz\twander\tmovement_type\tgroup_leader\tpatrol\tauras\tdetection_range\tflags_extra\n"
	row := func(guid, entry, x, y string) string {
		return strings.Join([]string{guid, entry, "Mob", "60", "60", "1", "14", "0", x, y, "0", "0", "0", "0", "0", "", "20", "0"}, "\t") + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "creatures.tsv"), []byte(hdr+row("1", "10", "100", "0")+row("2", "11", "100", "60")), 0o644); err != nil {
		t.Fatal(err)
	}
	p, _ := stagedFixture()
	p.Map = 309
	p.Version = dungeon.Version
	p.Raid = &dungeon.RaidSpec{Size: 10, Tanks: 2, Healers: 2, DPS: 6}
	b, err := dungeon.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "plan.json")
	if err := os.WriteFile(file, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if code := campSearchFile(file, dir, "", []string{"0", "1", "2"}, 1, 3, 0); code != 0 {
		t.Fatalf("exit %d", code)
	}
	after, err := os.ReadFile(file)
	if err != nil || !reflect.DeepEqual(after, b) {
		t.Fatalf("the plan file changed (%v)", err)
	}
	if code := campSearchFile(file, dir, "", []string{"0"}, 1, 3, 0); code != 2 {
		t.Fatalf("missing arguments: exit %d, want 2", code)
	}
	if code := campSearchFile(file, dir, "", []string{"9", "1"}, 1, 3, 0); code != 1 {
		t.Fatalf("a leg out of range: exit %d, want 1", code)
	}
}
