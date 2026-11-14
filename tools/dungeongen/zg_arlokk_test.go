// The Arlokk leg's route: the walk up the temple once, every consecutive pair a path the run leader can take, and the
// patrol pull reachable (ZG walk 38's ark paused at step 347 waiting for 11352 and its straggler walk to the nearest loop
// point had no path that ended at the point).
package main

import (
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
)

// zgArlokkLeg is the leg's index in dungeons/zul_gurub.json.
const zgArlokkLeg = 4

// zgArlokkDevWalkYd is the walk of the leg before the 2026-11-05 re-route (the dev plan's rows, measured the same way:
// the sum of the navmesh path between consecutive move/pull/boss party points). The re-route must cut it.
const zgArlokkDevWalkYd = 3582.2

// zgArlokkMaxWalkYd caps the re-routed leg, so a later edit that adds a detour is a failure and not just a print. It is
// 2160 rather than the re-route's 2100 because the gong step's stand moved onto the gong's own floor (walk 100b, see
// zgArlokkGongApproachYd): the party walks the arena descent once more, 2119.0 yd measured, and no shorter walk reaches
// the gong from the temple's terrace (the leg's 2029.0 yd stand could not be rung).
const zgArlokkMaxWalkYd = 2160.0

// zgArlokkGongApproachYd caps the one hop of the leg that may exceed 1.5x its straight distance: the last pull to the
// gong step's stand. The stand stands on the gong's own floor 3.0 yd from the goober (walk 100b: its old stand, 69.4 yd
// away on the floor above, left every open order "stopped short of the object"), and the arena's only entrance is the
// temple's east ramp: the party walks 167.7 yd of navmesh path over 50.6 yd straight to it (3.31x), and the run leader
// walks it in its 40 yd approach hops (DungeonRunPolicy's ApproachHop) instead of one cut hop. Every other pair keeps 1.5x.
const zgArlokkGongApproachYd = 175.0

// zgArlokkPatrolShotYd is the tank's shot the stand-pull gate waits for a patroller in (TortoiseBots
// DungeonRunPolicy.h's kStandPullShotYd): a stand must lie within it of a loop point for the gate to fire.
const zgArlokkPatrolShotYd = 28.5

// zgArlokkRows exports the Arlokk leg with the flags the live migrations use (plan-export --floor-aware --approach-hops
// --mmaps, plus the live data flags), so the test sees the rows the run leader walks.
func zgArlokkRows(t *testing.T, p *dungeon.Plan, pl *navmesh.Planner) []planexport.StepRow {
	t.Helper()
	res, err := planexport.Build(p, planexport.Options{ApproachHops: true, Cut: zgW37ExportCut(pl), GMCrossDirect: true,
		DropCompanionsLoot: true, CarrierChests: true})
	if err != nil {
		t.Fatal(err)
	}
	var rows []planexport.StepRow
	for _, r := range res.Rows.Steps {
		if r.Leg == zgArlokkLeg {
			rows = append(rows, r)
		}
	}
	if len(rows) == 0 {
		t.Fatal("no Arlokk rows")
	}
	return rows
}

// zgArlokkWalkRows are the rows that put the party somewhere. An interaction row (open, loot) is not a waypoint: the run
// leader walks to the object itself, and the plan has no via list for a step.
func zgArlokkWalkRow(kind string) bool {
	switch kind {
	case planexport.KindMove, planexport.KindMoveDirect, planexport.KindMoveHopExact, planexport.KindPull, planexport.KindBoss, planexport.KindHold:
		return true
	}
	return false
}

// zgArlokkParty is where the party stands for row i: a stand-pull's stand (the move row right before the pull in the
// same leg), every other row's own point.
func zgArlokkParty(rows []planexport.StepRow, i int) dungeon.Point {
	s := rows[i]
	if (s.Kind == planexport.KindPull || s.Kind == planexport.KindBoss) && s.Flags&planexport.FlagStandPull != 0 && i > 0 {
		prev := rows[i-1]
		if prev.Leg == s.Leg && (prev.Kind == planexport.KindMove || prev.Kind == planexport.KindMoveDirect || prev.Kind == planexport.KindMoveHopExact) {
			return dungeon.Point{X: prev.X, Y: prev.Y, Z: prev.Z}
		}
	}
	return dungeon.Point{X: s.X, Y: s.Y, Z: s.Z}
}

func zgArlokkDist3(a, b dungeon.Point) float64 {
	dx, dy, dz := float64(b.X-a.X), float64(b.Y-a.Y), float64(b.Z-a.Z)
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

// zgArlokkPair is one consecutive pair of the walk and its navmesh path.
type zgArlokkPair struct {
	a, b   dungeon.Point
	path   navmesh.Path
	length float64
	err    error
	from   string
	to     string
}

func zgArlokkPairs(t *testing.T, rows []planexport.StepRow, pl *navmesh.Planner) []zgArlokkPair {
	t.Helper()
	var out []zgArlokkPair
	prev := -1
	for i := range rows {
		if !zgArlokkWalkRow(rows[i].Kind) {
			continue
		}
		if prev < 0 {
			prev = i
			continue
		}
		a, b := zgArlokkParty(rows, prev), zgArlokkParty(rows, i)
		path, err := pl.Plan(p309, navmesh.Vec3{a.X, a.Y, a.Z}, navmesh.Vec3{b.X, b.Y, b.Z})
		out = append(out, zgArlokkPair{a: a, b: b, path: path, length: float64(path.Length), err: err,
			from: fmt.Sprintf("row %d (%s %q)", prev+1, rows[prev].Kind, rows[prev].Comment),
			to:   fmt.Sprintf("row %d (%s %q)", i+1, rows[i].Kind, rows[i].Comment)})
		prev = i
	}
	return out
}

const p309 = 309

// TestZulGurubArlokkLegRoute is the brief's rule: no consecutive pair of the leg's walk is a hop whose navmesh path is
// missing or over 1.5x its straight distance (the last pull -> gong stand hop, the arena descent, is capped separately at
// zgArlokkGongApproachYd), and the leg's walk drops against the dev leg.
func TestZulGurubArlokkLegRoute(t *testing.T) {
	p, _ := zgLoadPlanAndExport(t)
	pl := zgPlannerOrNil(t)
	if pl == nil {
		t.Skip("no map 309 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 309.mmap and its tiles")
	}
	defer pl.Close()
	rows := zgArlokkRows(t, p, pl)
	pairs := zgArlokkPairs(t, rows, pl)

	// The gong step's stand, so the pair that ends on it is told apart from the leg's own hops.
	var gongStand *dungeon.Point
	for _, st := range p.Legs[zgArlokkLeg].Steps {
		if st.UseObject != nil && st.UseObject.Stand != nil {
			gongStand = st.UseObject.Stand
		}
	}
	if gongStand == nil {
		t.Fatal("the Arlokk leg has no use_object stand")
	}

	// Every pair is checked, the last pull->gong stand hop included: that one hop is the arena descent (capped at
	// zgArlokkGongApproachYd), every other pair keeps 1.5x, so the run leader walks no uncut detour of its own.
	total := 0.0
	for _, pr := range pairs {
		total += pr.length
		if pr.err != nil || pr.path.Status != navmesh.Complete {
			t.Errorf("%s -> %s: no complete navmesh path: %v (%s)", pr.from, pr.to, pr.err, pr.path.Reason)
			continue
		}
		sd := zgArlokkDist3(pr.a, pr.b)
		if zgArlokkDist3(pr.b, *gongStand) < 1.0 {
			if pr.length > zgArlokkGongApproachYd {
				t.Errorf("%s -> %s: %.1f yd of path over %.1f yd straight (want <= %.0f yd: the arena descent to the gong)",
					pr.from, pr.to, pr.length, sd, zgArlokkGongApproachYd)
			}
			fmt.Printf("gong approach: %.1f yd of path over %.1f yd straight (%.2fx, cap %.0f yd)\n", pr.length, sd,
				pr.length/sd, zgArlokkGongApproachYd)
			continue
		}
		if pr.length > 1.5*sd {
			t.Errorf("%s -> %s: %.1f yd of path over %.1f yd straight (%.2fx, want <= 1.5x) at %.1f %.1f %.1f -> %.1f %.1f %.1f",
				pr.from, pr.to, pr.length, sd, pr.length/sd, pr.a.X, pr.a.Y, pr.a.Z, pr.b.X, pr.b.Y, pr.b.Z)
		}
	}
	fmt.Printf("Arlokk leg walk: %.1f yd -> %.1f yd (%.2fx of dev, %d rows, dev %.1f yd)\n", zgArlokkDevWalkYd, total, total/zgArlokkDevWalkYd, len(rows), zgArlokkDevWalkYd)
	if total >= zgArlokkDevWalkYd {
		t.Errorf("the leg's walk is %.1f yd, not below the dev leg's %.1f", total, zgArlokkDevWalkYd)
	}
	if total > zgArlokkMaxWalkYd {
		t.Errorf("the leg's walk is %.1f yd (cap %.0f)", total, zgArlokkMaxWalkYd)
	}

	// The leg still has every pack. The 10-body 71 pack is two stand pulls now (4 and 6 bodies), so the count is one more.
	leg := p.Legs[zgArlokkLeg]
	if got := len(leg.PullGroups); got != 26 {
		t.Errorf("%d pull groups, want 26 (the original 25, with the 10-body pack split in two)", got)
	}
	var split4, split6 bool
	for _, g := range leg.PullGroups {
		if len(g.Positions) == 0 || g.Positions[0].Z < 70 {
			continue
		}
		if g.StandPull && len(g.Entries) == 4 && g.MaxBodies == 4 {
			split4 = true
		}
		if g.StandPull && len(g.Entries) == 6 && g.MaxBodies == 6 {
			split6 = true
		}
	}
	if !split4 || !split6 {
		t.Error("the 71 terrace's 10-body pack is not two stand pulls of 4 and 6 bodies")
	}
	// The gong row and the boss row stay last.
	if rows[len(rows)-1].Kind != planexport.KindBoss || rows[len(rows)-1].Entry != 14515 {
		t.Errorf("the last row is %s %d, want the boss 14515", rows[len(rows)-1].Kind, rows[len(rows)-1].Entry)
	}
	open := -1
	for i, r := range rows {
		if r.Kind == planexport.KindOpen && r.Entry == 180526 {
			open = i
		}
	}
	if open < 0 {
		t.Fatalf("no gong open row 180526")
	}
	// The gong is opened right after the last pull; the boss approach's arena descent and the boss follow it.
	for i := open + 1; i < len(rows)-1; i++ {
		if rows[i].Kind != planexport.KindMove || rows[i].Comment != "boss approach" {
			t.Errorf("row %d (%s %q) stands between the gong 180526 (row %d) and the boss, want only the boss approach", i+1, rows[i].Kind, rows[i].Comment, open+1)
		}
	}
	if open+1 >= len(rows) {
		t.Errorf("the gong 180526 is the last row")
	}
}

// TestZulGurubArlokkApproachesClearOfLaterPacks: no authored stand or approach point of the leg's pull groups stands
// within 20 yd (3D) of a spawn of a pack that a later pull group owns. The temple's floors do not stop the proximity
// wake (walk 25: the move to a z 60 approach 13.3 yd 3D from 51354 on the z 71 terrace woke four packs), so the check
// is 3D and not floor-aware: the landing's old last approach point (-11502.2 -1625.7 60.4) lay 13.1 yd under the top's
// east six, and the party's arrival woke them before their pull row. A point is compared with the spawns no earlier row
// owns: a group's own pack and the packs already pulled are out (the party walks into them on purpose).
func TestZulGurubArlokkApproachesClearOfLaterPacks(t *testing.T) {
	p, err := dungeon.Load(filepath.Join(plansDirT, "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	const want = 20.0
	leg := p.Legs[zgArlokkLeg]
	type spawn struct {
		at    dungeon.Point
		group int
	}
	var pulled []spawn
	n := 0
	for gi, g := range leg.PullGroups {
		var later []spawn
		for j := gi + 1; j < len(leg.PullGroups); j++ {
			for _, q := range leg.PullGroups[j].Positions {
				own, dead := false, false
				for _, q2 := range g.Positions {
					if q == q2 {
						own = true
					}
				}
				for _, s := range pulled {
					if s.at == q {
						dead = true
					}
				}
				if own || dead {
					continue
				}
				later = append(later, spawn{at: q, group: j})
			}
		}
		for ai, a := range g.Approach {
			for _, s := range later {
				if d := zgArlokkDist3(a, s.at); d < want {
					n++
					t.Errorf("group %d approach %d (%.1f %.1f %.1f) stands %.1f yd from a later group's spawn %.1f %.1f %.1f (group %d): want %.0f yd (3D)",
						gi, ai, a.X, a.Y, a.Z, d, s.at.X, s.at.Y, s.at.Z, s.group, want)
				}
			}
		}
		for _, q := range g.Positions {
			pulled = append(pulled, spawn{at: q, group: gi})
		}
	}
	if n == 0 {
		t.Logf("every stand and approach point of the leg's %d pull groups is %.0f yd (3D) clear of the packs a later row pulls", len(leg.PullGroups), want)
	}
}

// TestZulGurubArlokkPatrolStandInShot: the 21 patrol's stand must be inside the tank's shot of the patrol's loop and the
// loop point must be reachable from it. ZG walk 38's ark paused at step 347 with the patrol 70 yd out and no path that
// ended at the nearest loop point.
func TestZulGurubArlokkPatrolStandInShot(t *testing.T) {
	p, _ := zgLoadPlanAndExport(t)
	pl := zgPlannerOrNil(t)
	if pl == nil {
		t.Skip("no map 309 navmesh files")
	}
	defer pl.Close()
	leg := p.Legs[zgArlokkLeg]
	var patrol *dungeon.PullGroup
	for i := range leg.PullGroups {
		if leg.PullGroups[i].Patrol != nil {
			patrol = &leg.PullGroups[i]
			break
		}
	}
	if patrol == nil {
		t.Fatal("no patrol pull group in the Arlokk leg")
	}
	if !patrol.StandPull || len(patrol.Approach) == 0 {
		t.Fatalf("the patrol group is not a stand pull with an approach: %+v", *patrol)
	}
	stand := patrol.Approach[len(patrol.Approach)-1]
	if len(patrol.Positions) == 0 {
		t.Fatal("the patrol group has no position")
	}
	spawnOK := false
	// The loop itself: a point in the tank's shot, reachable and in line of sight.
	best, bestD := dungeon.Point{}, math.Inf(1)
	for _, q := range patrol.Patrol.Path {
		d := zgArlokkDist3(stand, q)
		if d < bestD {
			best, bestD = q, d
		}
		if d > zgArlokkPatrolShotYd {
			continue
		}
		path, err := pl.Plan(p309, navmesh.Vec3{stand.X, stand.Y, stand.Z}, navmesh.Vec3{q.X, q.Y, q.Z})
		if err != nil || path.Status != navmesh.Complete {
			continue
		}
		if ray, err := pl.Raycast(p309, navmesh.Vec3{stand.X, stand.Y, stand.Z + 1.5}, navmesh.Vec3{q.X, q.Y, q.Z + 1}); err == nil && ray.Known && ray.Clear {
			spawnOK = true
		}
	}
	if bestD > zgArlokkPatrolShotYd {
		t.Errorf("the patrol stand %.1f %.1f %.1f is %.1f yd from the nearest loop point, want within %.1f", stand.X, stand.Y, stand.Z, bestD, zgArlokkPatrolShotYd)
	}
	if !spawnOK {
		t.Errorf("no loop point within %.1f yd of the patrol stand %.1f %.1f %.1f has a complete path and line of sight", zgArlokkPatrolShotYd, stand.X, stand.Y, stand.Z)
	}
	t.Logf("patrol stand %.1f %.1f %.1f: nearest loop point %.1f yd (%.1f %.1f %.1f), radius %.0f", stand.X, stand.Y, stand.Z, bestD, best.X, best.Y, best.Z, patrol.Radius)
	// The exported row carries the patrol entry, so raid-walk's --gm-clear clears the patroller too.
	rows := zgArlokkRows(t, p, pl)
	found := false
	for _, r := range rows {
		if r.Kind == planexport.KindPull && r.Flags&planexport.FlagStandPull != 0 && len(r.PatrolEntries) > 0 {
			for _, e := range r.PatrolEntries {
				if e == 11352 {
					found = true
				}
			}
			if r.Radius < 60 {
				t.Errorf("the patrol row's radius is %.0f, want >= 60 (its spawn 91444 lies 54 yd 3D from the row point)", r.Radius)
			}
		}
	}
	if !found {
		t.Error("no exported pull row carries the patrol entry 11352: --gm-clear cannot clear it")
	}
}

// TestZulGurubArlokkRowsOnTheMesh: every row of the Arlokk leg is on the navmesh, on its own floor. Walk 38's stuck
// bots (research/zg-w38/arlokk-stuck.tsv) hovered at approach/stand points on the z 71 terrace and the west stair that
// the old rows put between terraces or on a ledge; a row more than 2 yd off a polygon, or more than 1.5 yd above or
// below the polygon under it, is a route-caused hover (the run leader cannot stand the party there).
func TestZulGurubArlokkRowsOnTheMesh(t *testing.T) {
	p, _ := zgLoadPlanAndExport(t)
	pl := zgPlannerOrNil(t)
	if pl == nil {
		t.Skip("no map 309 navmesh files")
	}
	defer pl.Close()
	rows := zgArlokkRows(t, p, pl)
	for i, r := range rows {
		if !zgArlokkWalkRow(r.Kind) {
			continue
		}
		// The point the party is sent to: a stand pull's stand (the move row before it), every other row's own point.
		// A stand pull's row point is its pack's centroid, which may lie over a hole in the terrace under the pack.
		at := zgArlokkParty(rows, i)
		near, ok, err := pl.Nearest(p309, navmesh.Vec3{at.X, at.Y, at.Z})
		if err != nil || !ok {
			t.Errorf("row %d (%s %q) at %.1f %.1f %.1f: no navmesh polygon", i+1, r.Kind, r.Comment, r.X, r.Y, r.Z)
			continue
		}
		dx, dy, dz := float64(near[0]-at.X), float64(near[1]-at.Y), float64(near[2]-at.Z)
		if d := math.Sqrt(dx*dx + dy*dy + dz*dz); d > 2 {
			t.Errorf("row %d (%s %q) party point %.1f %.1f %.1f is %.2f yd from the navmesh (poly at %.1f %.1f %.1f)", i+1, r.Kind, r.Comment, at.X, at.Y, at.Z, d, near[0], near[1], near[2])
		}
		if math.Abs(float64(near[2]-at.Z)) > 1.5 {
			t.Errorf("row %d (%s %q) party point %.1f %.1f %.1f is %.1f yd above or below the polygon under it (%.1f): not on its own floor", i+1, r.Kind, r.Comment, at.X, at.Y, at.Z, math.Abs(float64(near[2]-at.Z)), near[2])
		}
	}
}

// zgCommanderUseRangeYd is the distance a commander open order needs to stand inside: TortoiseBots RemoteCommanderTasks
// InUseRange is the core's INTERACTION_DISTANCE (5 yd) less kInteractMargin (0.5 yd). The run leader's task for the row
// walks the lead bot to a point 1.5 yd short of the object and starts the act only inside it.
const zgCommanderUseRangeYd = 4.5

// TestZulGurubArlokkGongStandInReach: the gong step's stand must lie inside the commander's gameobject use range of the
// goober, so the open order starts where the party stands instead of walking the leader to the object itself.
// Walk 100b (2026-11-14): the stand was the ring's north-east bend -11502.9 -1609.1 49.625404, 69.4 yd from 180526 and
// 8.3 yd above it on the floor above, and every attempt paused at "cmd open|unreachable|The bot stopped short of the
// object and cannot get closer." with the lead bot on the arena floor 70 yd out. On dev this test fails by 64.9 yd.
func TestZulGurubArlokkGongStandInReach(t *testing.T) {
	p, err := dungeon.Load(filepath.Join(plansDirT, "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	var near, stand *dungeon.Point
	for _, st := range p.Legs[zgArlokkLeg].Steps {
		if st.UseObject != nil && st.UseObject.Entry == 180526 {
			near, stand = st.UseObject.Near, st.UseObject.Stand
		}
	}
	if near == nil || stand == nil {
		t.Fatal("the Arlokk leg's gong step 180526 has no near and stand points")
	}
	if d := zgArlokkDist3(*stand, *near); d > zgCommanderUseRangeYd {
		t.Errorf("the gong 180526 stand %.2f %.2f %.2f is %.2f yd from it (at %.2f %.2f %.2f), beyond the commander's %.1f yd use range: the open order would have to walk the leader to the object",
			stand.X, stand.Y, stand.Z, d, near.X, near.Y, near.Z, zgCommanderUseRangeYd)
	}
}

// TestZulGurubArlokkGongStepAfterIsAPlaceStart: the gong's open row is no place start (the placement accepts a move row
// with a point or a pull row, TortoiseBots raidwalk PlaceStart), so walk 100b's autopilot resumed at the stand row before
// it — `"step 346" is not a place start: resuming from "step 345"` — which is right only while that stand is on the
// gong's own floor. The row after the open (the boss approach move) is a place start too, so a resume there works.
func TestZulGurubArlokkGongStepAfterIsAPlaceStart(t *testing.T) {
	p, _ := zgLoadPlanAndExport(t)
	pl := zgPlannerOrNil(t)
	if pl == nil {
		t.Skip("no map 309 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 309.mmap and its tiles")
	}
	defer pl.Close()
	res, err := planexport.Build(p, planexport.Options{ApproachHops: true, Cut: zgW37ExportCut(pl), GMCrossDirect: true,
		DropCompanionsLoot: true, CarrierChests: true})
	if err != nil {
		t.Fatal(err)
	}
	rows := res.Rows
	open := -1
	for i, r := range rows.Steps {
		if r.Kind == planexport.KindOpen && r.Entry == 180526 {
			open = i
		}
	}
	if open < 1 || open+1 >= len(rows.Steps) {
		t.Fatalf("the gong open row is %d of %d rows", open+1, len(rows.Steps))
	}
	words := func(n int) []string { return []string{"from", "step", strconv.Itoa(n)} }
	if _, _, _, _, err := raidwalk.PlaceStart(rows, words(open+1)); err == nil {
		t.Errorf("step %d (the gong open row) is a place start: the placement would place the party on an open row", open+1)
	}
	resume, ok := raidwalk.PlaceResumeStart(rows, open+1)
	if !ok || resume != fmt.Sprintf("step %d", open) {
		t.Errorf("a resume at the gong open row %d starts at %q, want %q (the stand row before it)", open+1, resume,
			fmt.Sprintf("step %d", open))
	}
	if step, _, _, _, err := raidwalk.PlaceStart(rows, words(open)); err != nil || step != open {
		t.Errorf("step %d (the gong's stand move) is no place start: %v (%d)", open, err, step)
	}
	if step, _, _, _, err := raidwalk.PlaceStart(rows, words(open+2)); err != nil || step != open+2 {
		t.Errorf("step %d (the row after the gong, a boss approach move) is no place start: %v (%d)", open+2, err, step)
	}
}
