// The Arlokk leg's route: the walk up the temple once, every consecutive pair a path the run leader can take, and the
// patrol pull reachable (ZG walk 38's ark paused at step 347 waiting for 11352 and its straggler walk to the nearest loop
// point had no path that ended at the point).
package main

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// zgArlokkLeg is the leg's index in dungeons/zul_gurub.json.
const zgArlokkLeg = 4

// zgArlokkDevWalkYd is the walk of the leg before the 2026-11-05 re-route (the dev plan's rows, measured the same way:
// the sum of the navmesh path between consecutive move/pull/boss party points). The re-route must cut it.
const zgArlokkDevWalkYd = 3582.2

// zgArlokkMaxWalkYd caps the re-routed leg, so a later edit that adds a detour is a failure and not just a print.
const zgArlokkMaxWalkYd = 2200.0

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
// missing or over 1.5x its straight distance, and the leg's walk drops against the dev leg.
func TestZulGurubArlokkLegRoute(t *testing.T) {
	p, _ := zgLoadPlanAndExport(t)
	pl := zgPlannerOrNil(t)
	if pl == nil {
		t.Skip("no map 309 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 309.mmap and its tiles")
	}
	defer pl.Close()
	rows := zgArlokkRows(t, p, pl)
	pairs := zgArlokkPairs(t, rows, pl)

	// The last pull row ends the pull route; the gong step and the boss follow it (the arena descent is checked below as a
	// complete path only: it is one object-bound walk the run leader makes itself, and no plan row may carry a via list).
	lastPull := len(pairs) - 1
	for i := len(pairs) - 1; i >= 0; i-- {
		if strings.Contains(pairs[i].to, " (pull ") {
			lastPull = i
			break
		}
	}
	total := 0.0
	for i, pr := range pairs {
		total += pr.length
		if pr.err != nil || pr.path.Status != navmesh.Complete {
			t.Errorf("%s -> %s: no complete navmesh path: %v (%s)", pr.from, pr.to, pr.err, pr.path.Reason)
			continue
		}
		if i > lastPull {
			continue // the gong's arena walk: complete is the rule, the ratio is the run leader's own navmesh
		}
		sd := zgArlokkDist3(pr.a, pr.b)
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
	if open < 0 || open != len(rows)-3 {
		t.Errorf("the gong 180526 is row %d of %d, want the open row just before the boss approach and the boss", open+1, len(rows))
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
	// Its spawn is in the row's radius, so the run leader owns the patroller's formation (the gate's spawnOwned).
	cx, cy, cz := 0.0, 0.0, 0.0
	for _, q := range patrol.Positions {
		cx, cy, cz = cx+float64(q.X), cy+float64(q.Y), cz+float64(q.Z)
	}
	n := float64(len(patrol.Positions))
	if len(patrol.Positions) == 0 {
		t.Fatal("the patrol group has no position")
	}
	cx, cy, cz = cx/n, cy/n, cz/n
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
