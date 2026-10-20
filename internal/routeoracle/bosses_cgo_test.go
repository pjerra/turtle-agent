//go:build cgo

package routeoracle

import (
	"math"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/navmesh/navfixture"
)

// gapMesh is the L corridor with the column cell y 26..30 taken out: the mesh has a break at
// (48, 28) that only a hand hop (a direct step or a GM) crosses.
func gapMesh() []navfixture.Poly {
	var out []navfixture.Poly
	for _, p := range navfixture.LCorridor(0) {
		if p[0][0] == 46 && p[0][1] == 26 {
			continue
		}
		out = append(out, p)
	}
	return out
}

// Leg 2 walks past the gap to the boss at (48, 48). Its stored via at (48, 34) is the break the
// plan crosses (check_skip, so the walk mode skips it); the exact and direct vias at (20, 12)
// and (36, 12) are breaks far from where the live path stops. Leg 3 goes back to (46, 12) from
// where leg 2's path stopped; leg 4 goes on from leg 3's objective.
const gapPlan = `{"version":1,"name":"gap","map":0,"entrance":{"x":12,"y":12,"z":0},"legs":[
 {"name":"hall","to":{"x":46,"y":12,"z":0},
  "via":[{"x":22,"y":12,"z":0}],
  "pull_groups":[
   {"entries":[100],"positions":[{"x":44,"y":12,"z":0}]},
   {"entries":[101],"approach":[{"x":48,"y":40,"z":0}],"positions":[{"x":48,"y":40,"z":0}],"radius":10}]},
 {"name":"stairs","to":{"x":48,"y":20,"z":0},
  "via":[{"x":48,"y":34,"z":0,"check_skip":"fixture"}],
  "boss":{"entry":200,"position":{"x":48,"y":24,"z":0}}},
 {"name":"serpentis","to":{"x":48,"y":44,"z":0},
  "via":[{"x":20,"y":12,"z":0,"exact":true},{"x":36,"y":12,"z":0,"direct":true,"check_skip":"fixture"},{"x":48,"y":34,"z":0,"check_skip":"fixture"}],
  "pull_groups":[{"entries":[102],"positions":[{"x":48,"y":40,"z":0}]}],
  "boss":{"entry":201,"position":{"x":48,"y":48,"z":0}}},
 {"name":"back","to":{"x":46,"y":12,"z":0},
  "via":[{"x":48,"y":16,"z":0}],
  "pull_groups":[{"entries":[103],"positions":[{"x":44,"y":12,"z":0}]}]},
 {"name":"onward","to":{"x":30,"y":12,"z":0},
  "pull_groups":[{"entries":[104],"positions":[{"x":30,"y":12,"z":0}]}]}]}`

func bossesFixtureParts(t *testing.T) (*navmesh.Planner, string, *dungeon.Plan) {
	t.Helper()
	dir := navfixture.WriteDir(t, 0, gapMesh())
	p := navmesh.NewPlanner(dir)
	t.Cleanup(p.Close)
	plan, err := dungeon.Parse("gap.json", []byte(gapPlan))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return p, dir, plan
}

func bossesFixture(t *testing.T) BossesReport {
	t.Helper()
	p, dir, plan := bossesFixtureParts(t)
	rep, err := Bosses(p, dir, plan, ObjectivesPlan)
	if err != nil {
		t.Fatalf("Bosses: %v", err)
	}
	if len(rep.Legs) != 5 {
		t.Fatalf("legs: %d", len(rep.Legs))
	}
	return rep
}

// A leg the mesh cannot finish FAILS and names the stored break at the gap: the walk mode skips
// that break hop, the bosses mode must not. A stored break far from where the path stops is not
// named.
func TestBossesGapFailsTheLegAndNamesTheBreak(t *testing.T) {
	rep := bossesFixture(t)
	for _, i := range []int{0, 1} {
		if rep.Legs[i].Status != "complete" || rep.Legs[i].NonComparable || rep.Legs[i].Failed() {
			t.Errorf("leg %d: %+v", i, rep.Legs[i])
		}
	}
	f := rep.Legs[2]
	if f.Status != string(navmesh.Partial) || !f.Failed() {
		t.Fatalf("leg 2: status %s failed %v, want a failed partial", f.Status, f.Failed())
	}
	if f.Live.EndGapYd < 15 || f.Live.EndGapYd > 25 {
		t.Errorf("leg 2 gap %.1f yd, want ~22 (the path stops at the gap)", f.Live.EndGapYd)
	}
	if len(f.BreakHits) != 1 {
		t.Fatalf("leg 2 break hits: %+v", f.BreakHits)
	}
	h := f.BreakHits[0]
	if h.Via != 2 || h.Kind != "check_skip" || h.At.Y != 34 || h.EndYd > 15 {
		t.Errorf("break hit %+v, want via 2 check_skip at y 34 within 15 yd of the end", h)
	}
	// The failed leg's own metrics stay empty.
	if len(f.Packs) != 0 || len(f.Hand) != 0 || len(f.ZJumps) != 0 {
		t.Errorf("failed leg carries metrics: %+v", f)
	}
	lines := strings.Join(failedBossLegsForTest(rep), "\n")
	if !strings.Contains(lines, "serpentis") || !strings.Contains(lines, "check_skip") {
		t.Errorf("failed lines %q", lines)
	}
}

// failedBossLegsForTest keeps the package test free of the command's text.
func failedBossLegsForTest(rep BossesReport) []string {
	var out []string
	for i := range rep.Legs {
		if rep.Legs[i].Failed() {
			out = append(out, rep.Legs[i].Name+" "+rep.Legs[i].BreakHits[0].Kind)
		}
	}
	return out
}

// The leg after the failed one starts from the last point the failed path reached and is
// non-comparable; it adds nothing to the aggregates; the leg after it (which started on a real
// objective) counts again.
func TestBossesFailedLegDoesNotContaminate(t *testing.T) {
	rep := bossesFixture(t)
	f, nc, next := rep.Legs[2], rep.Legs[3], rep.Legs[4]
	if !nc.NonComparable || nc.NonComparableWhy == "" || !strings.Contains(nc.NonComparableWhy, "leg 2 (serpentis)") {
		t.Errorf("leg 3 not labelled non-comparable: %+v", nc)
	}
	if nc.From != f.Live.End {
		t.Errorf("leg 3 starts at %v, want the failed path's end %v", nc.From, f.Live.End)
	}
	if nc.Status != "complete" || nc.Failed() || nc.Counted() {
		t.Errorf("leg 3: status %s failed %v counted %v", nc.Status, nc.Failed(), nc.Counted())
	}
	if next.NonComparable || !next.Counted() {
		t.Errorf("leg 4 should count again: %+v", next)
	}
	if next.From.X != 46 || next.From.Y < 11 || next.From.Y > 13 {
		t.Errorf("leg 4 starts at %v, want leg 3's objective (46, 12)", next.From)
	}
	// Totals: legs 0, 1 and 4 only.
	tot := rep.Totals()
	if tot.Complete != 3 || tot.Failed != 1 || tot.Continuations != 1 || tot.ContinuationsOK != 1 {
		t.Errorf("totals %+v", tot)
	}
	var packs, on, hand, red, zj int
	var live float64
	for _, i := range []int{0, 1, 4} {
		l := rep.Legs[i]
		packs += len(l.Packs)
		on += l.PacksOnPath
		hand += len(l.Hand)
		red += l.HandRedundant
		zj += len(l.ZJumps)
		live += l.Live.LiveLenYd
	}
	if tot.Sum.Packs != packs || tot.Sum.PacksOn != on || tot.Sum.Hand != hand || tot.Sum.HandRedundant != red || tot.Sum.ZJumps != zj || tot.Sum.LiveYd != live {
		t.Errorf("totals %+v do not equal the counted legs' sums (%d/%d packs, %d/%d hand, %d jumps, %.1f yd)", tot, on, packs, red, hand, zj, live)
	}
	// Leg 3 would have changed the sums had it been counted: its pack sits on its path.
	if nc.PacksOnPath == 0 || len(nc.Hand) == 0 {
		t.Errorf("leg 3 carries no metrics to exclude: %+v", nc)
	}
	s := rep.Summary()
	if !strings.Contains(s, "3 complete, 1 failed, 1 non-comparable (1 reach their objective)") {
		t.Errorf("summary %q", s)
	}
}

// The corridor metrics of a complete leg: a pack whose centre the live path passes is on it, a
// pack far from it is not; a via the path passes is redundant, an approach far from it is not;
// a break via is no hand point.
func TestBossesCorridorMetrics(t *testing.T) {
	rep := bossesFixture(t)
	h := rep.Legs[0]
	if len(h.Packs) != 2 || !h.Packs[0].OnPath || h.Packs[1].OnPath {
		t.Errorf("hall packs: %+v", h.Packs)
	}
	if len(h.Hand) != 2 {
		t.Fatalf("hall hand points: %+v", h.Hand)
	}
	if h.Hand[0].Kind != "via" || !h.Hand[0].Redundant {
		t.Errorf("via (22,12): %+v, want redundant", h.Hand[0])
	}
	if h.Hand[1].Kind != "approach" || h.Hand[1].Redundant || h.Hand[1].NearYd < 20 {
		t.Errorf("approach (48,40): %+v, want far from the live path", h.Hand[1])
	}
	// Leg 1's only via is a break: not a hand point, but listed with the breaks, untouched.
	st := rep.Legs[1]
	if len(st.Hand) != 1 || st.Hand[0].Kind != "to" {
		t.Errorf("stairs hand points %+v: want the to alone (a break via is not one)", st.Hand)
	}
	if len(st.Breaks) != 1 || st.Breaks[0].Touched {
		t.Errorf("stairs breaks %+v", st.Breaks)
	}
	if st.Live.LiveLenYd < 12 {
		t.Errorf("stairs live %.1f yd", st.Live.LiveLenYd)
	}
}

// A planner on a mesh with no mmaps is an error, not a report; a bad plan name is refused.
func TestBossesRefusesABadName(t *testing.T) {
	p := navmesh.NewPlanner("")
	defer p.Close()
	if _, err := Bosses(p, "", &dungeon.Plan{Name: "../escape"}, ObjectivesPlan); err == nil || !strings.Contains(err.Error(), "not a plan name") {
		t.Errorf("err %v", err)
	}
}

// A break far from where the live path stops is still named when it is the missing link: the
// island at (100..120) is reached by the plan's check_skip hop at (102, 102), 70 yd from the
// end of the reachable ground; a decoy break at (30, 12) does not lead to the island and is
// not named. Then the leg after the failed one is planned a second time from the failed leg's
// objective (bridged), so the mesh's other gaps show up.
func TestBossesNamesTheBridgingBreak(t *testing.T) {
	plan := `{"version":1,"name":"island","map":0,"entrance":{"x":12,"y":12,"z":0},"legs":[
	 {"name":"crossing","to":{"x":110,"y":110,"z":0},
	  "via":[{"x":30,"y":12,"z":0,"check_skip":"decoy"},{"x":48,"y":20,"z":0},{"x":102,"y":102,"z":0,"check_skip":"the island"}],
	  "boss":{"entry":300,"position":{"x":110,"y":110,"z":0}}},
	 {"name":"island hall","to":{"x":104,"y":104,"z":0},"boss":{"entry":301,"position":{"x":116,"y":116,"z":0}}},
	 {"name":"voided","to":{"x":300,"y":300,"z":0},"boss":{"entry":302,"position":{"x":300,"y":300,"z":0}}}]}`
	polys := append(navfixture.LCorridor(0), navfixture.Square(100, 100, 120, 120, 0))
	dir := navfixture.WriteDir(t, 0, polys)
	p := navmesh.NewPlanner(dir)
	t.Cleanup(p.Close)
	dp, err := dungeon.Parse("island.json", []byte(plan))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := Bosses(p, dir, dp, ObjectivesPlan)
	if err != nil {
		t.Fatalf("Bosses: %v", err)
	}
	f := rep.Legs[0]
	if !f.Failed() || len(f.BreakHits) != 1 {
		t.Fatalf("leg 0: status %s hits %+v", f.Status, f.BreakHits)
	}
	h := f.BreakHits[0]
	if h.Via != 2 || !h.Bridges || h.EndYd < 40 || !strings.Contains(h.Why, "reaches its foot") {
		t.Errorf("hit %+v, want via 2 named only because it bridges (end %.0f yd away)", h, h.EndYd)
	}
	// Leg 1 starts on the mainland (the failed path's end): non-comparable and failing; planned
	// from leg 0's objective on the island it completes, which the bridged re-plan says.
	n := rep.Legs[1]
	if !n.NonComparable || n.Status == "complete" {
		t.Errorf("leg 1: %+v", n)
	}
	if n.Bridged == nil || n.Bridged.Status != "complete" || n.Bridged.From != f.To || len(n.Bridged.Hand) != 1 {
		t.Errorf("leg 1 bridged: %+v", n.Bridged)
	}
	tot := rep.Totals()
	if tot.Complete != 0 || tot.Failed != 1 || tot.Continuations != 2 || tot.BridgedComplete != 1 || tot.Sum.LiveYd != 0 || tot.Bridged.Hand != 1 {
		t.Errorf("totals %+v", tot)
	}
	// The void leg is non-comparable too, and its bridged plan fails as well.
	if v := rep.Legs[2]; !v.NonComparable || v.Bridged == nil || v.Bridged.Status == "complete" {
		t.Errorf("leg 2: %+v", v)
	}
}

// Boss-only objectives: the chain is entrance -> boss 1 -> boss 2. The gap plan's legs 0 (hall),
// 1 (stairs, a boss) and 2 (serpentis, a boss) give two objectives; the hall is folded into the
// stairs row, its to point (46, 12) is a hand point and NOT an objective, and the legs after the
// last boss (3, 4) are not planned.
func TestBossOnlyChainFoldsBosslessLegs(t *testing.T) {
	p, dir, plan := bossesFixtureParts(t)
	rep, err := Bosses(p, dir, plan, ObjectivesBosses)
	if err != nil {
		t.Fatalf("Bosses: %v", err)
	}
	if rep.Objectives != ObjectivesBosses || len(rep.Legs) != 2 || len(rep.Unplanned) != 2 || rep.Unplanned[0] != 3 || rep.Unplanned[1] != 4 {
		t.Fatalf("objectives %s legs %d unplanned %v", rep.Objectives, len(rep.Legs), rep.Unplanned)
	}
	a := rep.Legs[0]
	if a.Name != "hall+stairs" || a.Leg != 1 || len(a.Legs) != 2 || a.Objective != "boss" || a.To.Y != 24 {
		t.Errorf("row 0: %+v", a)
	}
	if a.From.X != 12 || a.From.Y != 12 || a.Status != "complete" {
		t.Errorf("row 0 starts %v status %s", a.From, a.Status)
	}
	// The hall's to point (46, 12) and the stairs' to (48, 20) are hand points here (neither is
	// an objective); the hall's far approach too. In the plan mode the hall's to IS its objective.
	var to, sawHallTo int
	for _, h := range a.Hand {
		if h.Kind == "to" {
			to++
			if h.Leg == 0 && h.At.X == 46 {
				sawHallTo++
			}
		}
	}
	if to != 2 || sawHallTo != 1 {
		t.Errorf("row 0 to points among the hand points: %d (hall's: %d), hand %+v", to, sawHallTo, a.Hand)
	}
	if len(a.Packs) != 2 || a.Packs[0].Leg != 0 || a.Packs[1].Leg != 0 {
		t.Errorf("row 0 packs %+v, want the hall's two groups", a.Packs)
	}
	plain, err := Bosses(p, dir, plan, ObjectivesPlan)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range plain.Legs[0].Hand {
		if h.Kind == "to" {
			t.Errorf("plan mode counted the hall's objective to point as a hand point: %+v", h)
		}
	}
	if plain.Legs[0].Objective != "to" {
		t.Errorf("plan mode leg 0 objective %s", plain.Legs[0].Objective)
	}
	// Row 1: serpentis fails at the gap, names the break (stored leg 2, via 2).
	f := rep.Legs[1]
	if !f.Failed() || f.Leg != 2 || len(f.BreakHits) != 1 || f.BreakHits[0].Leg != 2 || f.BreakHits[0].Via != 2 {
		t.Errorf("row 1: status %s hits %+v", f.Status, f.BreakHits)
	}
	tot := rep.Totals()
	if tot.Complete != 1 || tot.Failed != 1 || tot.Sum.Packs != 2 || tot.Sum.PacksOn != 2 {
		t.Errorf("totals %+v", tot)
	}
	if s := rep.Summary(); !contains(s, "2 boss objectives: 1 complete, 1 failed") || !contains(s, "2 legs after the last boss not planned") {
		t.Errorf("summary %q", s)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// Boss-only chain after a failure: the boss after the failed one is non-comparable and bridged
// from the failed boss's position, as in the plan mode.
func TestBossOnlyContinuationAfterAFailedBoss(t *testing.T) {
	plan := `{"version":1,"name":"island","map":0,"entrance":{"x":12,"y":12,"z":0},"legs":[
	 {"name":"crossing","to":{"x":60,"y":60,"z":0},
	  "via":[{"x":48,"y":20,"z":0},{"x":102,"y":102,"z":0,"check_skip":"the island"}],
	  "boss":{"entry":300,"position":{"x":110,"y":110,"z":0}}},
	 {"name":"island hall","to":{"x":104,"y":104,"z":0},"boss":{"entry":301,"position":{"x":116,"y":116,"z":0}}}]}`
	polys := append(navfixture.LCorridor(0), navfixture.Square(100, 100, 120, 120, 0))
	dir := navfixture.WriteDir(t, 0, polys)
	p := navmesh.NewPlanner(dir)
	t.Cleanup(p.Close)
	dp, err := dungeon.Parse("island.json", []byte(plan))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rep, err := Bosses(p, dir, dp, ObjectivesBosses)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Legs) != 2 || !rep.Legs[0].Failed() || len(rep.Legs[0].BreakHits) != 1 {
		t.Fatalf("legs %+v", rep.Legs)
	}
	n := rep.Legs[1]
	if !n.NonComparable || n.Bridged == nil || n.Bridged.Status != "complete" || n.Bridged.From != rep.Legs[0].To {
		t.Errorf("row 1: %+v", n)
	}
	if tot := rep.Totals(); tot.Complete != 0 || tot.Failed != 1 || tot.Continuations != 1 || tot.Sum.LiveYd != 0 {
		t.Errorf("totals %+v", tot)
	}
	if _, err := Bosses(p, dir, dp, "boss"); err == nil {
		t.Error("an unknown objectives value was accepted")
	}
}

// Both modes judge a pack by the same definition: a group's position within its radius plus 18
// yd of the live route. Two groups of radius 5 sit 22 and 24 yd off the corridor: on (<= 23) and
// off (> 23), in the walk mode and in the bosses mode alike.
func TestPackDefinitionIsTheSameInBothModes(t *testing.T) {
	plan := `{"version":1,"name":"packs","map":0,"entrance":{"x":12,"y":12,"z":0},"legs":[
	 {"name":"hall","to":{"x":46,"y":12,"z":0},"pull_groups":[
	  {"entries":[1],"radius":5,"positions":[{"x":30,"y":34,"z":0}]},
	  {"entries":[2],"radius":5,"positions":[{"x":30,"y":36,"z":0}]}]}]}`
	dir := navfixture.WriteDir(t, 0, navfixture.LCorridor(0))
	p := navmesh.NewPlanner(dir)
	t.Cleanup(p.Close)
	dp, err := dungeon.Parse("packs.json", []byte(plan))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	walk, err := Oracle(p, dir, dp)
	if err != nil {
		t.Fatal(err)
	}
	live, err := Bosses(p, dir, dp, ObjectivesPlan)
	if err != nil {
		t.Fatal(err)
	}
	w, b := walk.Legs[0].Pulls, live.Legs[0].Packs
	if len(w) != 2 || len(b) != 2 {
		t.Fatalf("walk %d packs, bosses %d", len(w), len(b))
	}
	for i, want := range []bool{true, false} {
		if w[i].OnPath != want || b[i].OnPath != want {
			t.Errorf("group %d: walk %v bosses %v, want %v (near %.1f / %.1f yd)", i, w[i].OnPath, b[i].OnPath, want, w[i].NearYd, b[i].NearYd)
		}
	}
}

// A pull group with no position and no hold has nothing to walk to: the walk leaves it out
// instead of walking to the NaN centroid of nothing, and neither mode counts it in a pack total.
func TestEmptyPullGroupIsSkippedAndCounted(t *testing.T) {
	plan := &dungeon.Plan{Name: "empty", Entrance: dungeon.Point{X: 12, Y: 12}, Legs: []dungeon.Leg{{
		Name: "hall", To: dungeon.Point{X: 46, Y: 12},
		PullGroups: []dungeon.PullGroup{{Entries: []uint32{1}}, {Entries: []uint32{2}, Positions: []dungeon.Point{{X: 44, Y: 12}}}},
	}}}
	dir := navfixture.WriteDir(t, 0, navfixture.LCorridor(0))
	p := navmesh.NewPlanner(dir)
	t.Cleanup(p.Close)
	walk, err := Oracle(p, dir, plan)
	if err != nil {
		t.Fatalf("Oracle: %v", err)
	}
	if walk.EmptyGroups != 1 || len(walk.Legs[0].Pulls) != 1 || walk.Legs[0].Pulls[0].Group != 1 || walk.Legs[0].Status != "complete" {
		t.Errorf("walk: empty %d pulls %+v status %s", walk.EmptyGroups, walk.Legs[0].Pulls, walk.Legs[0].Status)
	}
	for _, s := range walk.Legs[0].Segments {
		if math.IsNaN(float64(s.At.X)) || s.Kind == "pull" && s.Group == 0 {
			t.Errorf("a hop to the empty group: %+v", s)
		}
	}
	live, err := Bosses(p, dir, plan, ObjectivesPlan)
	if err != nil {
		t.Fatalf("Bosses: %v", err)
	}
	if live.EmptyGroups != 1 || len(live.Legs[0].Packs) != 1 || live.Legs[0].Packs[0].Group != 1 {
		t.Errorf("bosses: empty %d packs %+v", live.EmptyGroups, live.Legs[0].Packs)
	}
	if !contains(walk.Summary(), "1 empty pull groups skipped") || !contains(live.Summary(), "1 empty pull groups skipped") {
		t.Errorf("summaries %q / %q", walk.Summary(), live.Summary())
	}
}
