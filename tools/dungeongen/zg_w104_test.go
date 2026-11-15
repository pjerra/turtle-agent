// Walk-104 fix (TortoiseBots-audit tests/raid-zg-walk104, runs fz5a-1/fz6a-1): Jeklik's step-28 park sat on the ramp top
// -12102.1 -1472.8 130.05, where four patrol loops meet (the 49135 headhunter 16 yd out, the 49147 bat formation 21.6, the
// 49155 riders 23 and the 49189 riders 30). Group 7 is formation 49147 (Bat Rider leader 49147 + four Bloodseeker Bats) and
// was at point 2 of its loop, 20 s from the party, when the raid arrived, so the step-30 pull fought its own loop under the
// party and every neighbouring loop (12 bats, 4 riders, 2 headhunters, a witch doctor, toads). The row is a stand pull now,
// from the ramp below the loop's turn: the stand holds at 25.7 yd 3D from the loop's point 2 (inside the run leader's 28.5 yd
// shot, outside the 23 yd rule), its radius covers its own loop, and the two new approach points clear the loops of every
// later group of the leg.
package main

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
)

// zgW104Group is the leg's pull group of formation 49147 (0-based, as the file lists it).
const zgW104Group = 7

var (
	// The kept approach points: the foot of the ramp and its first walked point.
	zgW104RampFoot = dungeon.Point{X: -12049.9, Y: -1474.7, Z: 87.25}
	zgW104RampMid  = dungeon.Point{X: -12070.7, Y: -1474.7, Z: 106.35}
	// zgW104WalkedPoint replaces the old ramp-top point -12102.1 -1472.8 130.05 (walk 104's park): the ramp's upper walk,
	// below the plateau floor, out of every plateau loop's 23 yd.
	zgW104WalkedPoint = dungeon.Point{X: -12086.2, Y: -1473.7, Z: 118.3}
	// zgW104Stand replaces the old stand -12057 -1448 129.13 (inside formation 49147's own loop): the ramp shoulder below
	// the loop's turn at its point 2.
	zgW104Stand = dungeon.Point{X: -12093.3, Y: -1476.1, Z: 124.6}
	// zgW104Point2 is formation 49147's movement point 2 (-12098.7 -1451.74 130.774, a 20 s wait).
	zgW104Point2 = dungeon.Point{X: -12098.7, Y: -1451.74, Z: 130.774}
)

// zgW104Yaw is the walk's 23 yd rule: the plateau's loops engage at 20 yd plus the raid's 3 yd spread.
const zgW104Yaw = 23.0

// TestZgW104RampStand is the walk-104 fix. The rules:
//   - group 7 is formation 49147 by its members: one 14750 (the leader, spawn 49147) and four 11368, five positions;
//   - the approach keeps its first two points and its count (4, so step numbers do not move): the third is the walked
//     ramp point and the fourth the stand;
//   - the row is a stand pull of at most 5 bodies, its radius 46 covers the formation's own loop and reaches no other
//     11368/14750 spawn;
//   - the patrol block is the formation's own creature_movement 49147 loop (4 points, the 20 s wait at point 2), so the
//     stand-pull gate reads it;
//   - the stand is 25.7 yd 3D from point 2 (inside the 28.5 yd shot, outside 23) and 25.3 / 32.7 / 39.7 yd 2D from the
//     49135 / 49155 / 49189 loops' nearest waypoints, with line of sight to point 2 (mmaps);
//   - the two new approach points keep 23 yd from the loop and the spawns of every later group of the leg except group 8,
//     the 11352 ramp patroller (49761) the walk GM-clears before step 25: its loop crosses the ramp 4.6-8.8 yd from both
//     points by design, and a dead patroller's loop is dropped by the run leader.
func TestZgW104RampStand(t *testing.T) {
	p, cs := zgLoadPlanAndExport(t)
	leg, _ := zgW37LegByName(t, p, "high_priestess_jeklik")
	if len(leg.PullGroups) <= zgW104Group {
		t.Fatalf("the jeklik leg has %d pull groups, want more than %d", len(leg.PullGroups), zgW104Group)
	}
	g := leg.PullGroups[zgW104Group]

	// (1) The row is formation 49147 by its members.
	bats, riders, leader := 0, 0, false
	for _, e := range g.Entries {
		switch e {
		case 11368:
			bats++
		case 14750:
			riders++
		}
	}
	for _, c := range cs {
		if c.GUID == 49147 && c.Entry == 14750 {
			for _, q := range g.Positions {
				if dist(q, c.Pos) <= 0.7 {
					leader = true
				}
			}
		}
	}
	if len(g.Positions) != 5 || bats != 4 || riders != 1 || !leader {
		t.Fatalf("pull group %d is %v over %d positions (the 49147 leader at its spawn: %v), want formation 49147: one 14750 + four 11368 over five positions",
			zgW104Group, g.Entries, len(g.Positions), leader)
	}

	// (2) The approach: four points, the first two kept, the new walked point and the stand.
	want := []struct {
		name string
		at   dungeon.Point
	}{
		{"the ramp foot", zgW104RampFoot},
		{"the ramp's first walked point", zgW104RampMid},
		{"the walked ramp point", zgW104WalkedPoint},
		{"the stand", zgW104Stand},
	}
	if len(g.Approach) != len(want) {
		t.Fatalf("pull group %d has %d approach points, want %d: the step numbers depend on the count", zgW104Group, len(g.Approach), len(want))
	}
	for k, w := range want {
		if d := dist(g.Approach[k], w.at); d > 0.01 {
			t.Errorf("pull group %d approach[%d] (%.2f %.2f %.2f) is %.2f yd from %s %.2f %.2f %.2f",
				zgW104Group, k, g.Approach[k].X, g.Approach[k].Y, g.Approach[k].Z, d, w.name, w.at.X, w.at.Y, w.at.Z)
		}
	}
	if d := dist(g.Approach[2], zgW104OldRampTop); d <= 1 {
		t.Errorf("pull group %d still parks on the old ramp top %.2f %.2f %.2f (walk 104's wipe): four patrol loops meet there",
			zgW104Group, zgW104OldRampTop.X, zgW104OldRampTop.Y, zgW104OldRampTop.Z)
	}

	// (3) The row: a stand pull of 5 bodies over a radius that covers its own loop.
	if !g.StandPull {
		t.Errorf("pull group %d is not a stand pull: the formation walks its loop under the party, so the pull must wait at the stand", zgW104Group)
	}
	if g.MaxBodies != 5 {
		t.Errorf("pull group %d has max_bodies %d, want 5 (the formation's five bodies, no add)", zgW104Group, g.MaxBodies)
	}
	if math.Abs(float64(g.Radius)-46) > 0.01 {
		t.Errorf("pull group %d has radius %.1f, want 46 (its centre to the far end of its own loop)", zgW104Group, g.Radius)
	}

	// (4) The patrol block is the formation's own loop.
	movement, err := zgReadMovementPaths("testdata/map309/movement.tsv")
	if err != nil {
		t.Fatal(err)
	}
	loop, ok := movement[49147]
	if !ok || len(loop) != 4 {
		t.Fatalf("movement.tsv: 49147 has no 4-point loop (%d points)", len(loop))
	}
	if g.Patrol == nil {
		t.Fatalf("pull group %d has no patrol block: the stand-pull gate cannot wait for the formation's loop", zgW104Group)
	}
	if len(g.Patrol.Entries) != 1 || g.Patrol.Entries[0] != 14750 {
		t.Errorf("pull group %d patrol entries are %v, want [14750]", zgW104Group, g.Patrol.Entries)
	}
	if len(g.Patrol.Path) != len(loop) {
		t.Errorf("pull group %d patrol path has %d points, want %d (creature_movement 49147)", zgW104Group, len(g.Patrol.Path), len(loop))
	} else {
		for k := range loop {
			if d := dist(g.Patrol.Path[k], loop[k]); d > 0.5 {
				t.Errorf("pull group %d patrol path[%d] is %.1f yd from 49147's waypoint", zgW104Group, k, d)
			}
		}
	}
	if math.Abs(float64(g.Patrol.PeriodS)-55.1) > 0.05 {
		t.Errorf("pull group %d patrol period is %.1f s, want 55.1 (87.8 yd at 2.5 yd/s plus the 20 s wait at point 2)", zgW104Group, g.Patrol.PeriodS)
	}
	if !g.Patrol.PeriodEstimate {
		t.Errorf("pull group %d patrol period is not flagged an estimate: no walk has measured the 49147 round", zgW104Group)
	}
	if got := g.Patrol.Source; got == "" || !containsAll(got, "creature_movement 49147", "20 s wait at point 2") {
		t.Errorf("pull group %d patrol source is %q, want creature_movement 49147 with its 20 s wait at point 2", zgW104Group, got)
	}

	// (5) The stand's shot of point 2.
	if d := dist(zgW104Stand, zgW104Point2); math.Abs(d-25.7) > 0.1 {
		t.Errorf("the stand (%.1f %.1f %.1f) is %.2f yd 3D from the formation's point 2, want 25.7", zgW104Stand.X, zgW104Stand.Y, zgW104Stand.Z, d)
	}
	if d := dist(zgW104Stand, zgW104Point2); d > zgW37TankShotYd {
		t.Errorf("the stand is %.1f yd 3D from point 2, past the %g yd shot: the gate never fires", d, zgW37TankShotYd)
	}

	// (6) The neighbouring loops stay out of the 23 yd rule, at the walked distances.
	for _, want := range []struct {
		guid uint64
		yd   float64
	}{
		{49135, 25.3}, // the headhunter loop
		{49155, 32.7}, // the east riders' loop
		{49189, 39.7}, // the far riders' loop
	} {
		l, ok := movement[want.guid]
		if !ok || len(l) == 0 {
			t.Fatalf("movement.tsv: %d has no loop", want.guid)
		}
		nearest := math.Inf(1)
		for _, q := range l {
			if d := math.Hypot(float64(zgW104Stand.X-q.X), float64(zgW104Stand.Y-q.Y)); d < nearest {
				nearest = d
			}
		}
		if math.Abs(nearest-want.yd) > 0.15 {
			t.Errorf("the stand is %.1f yd 2D from the nearest waypoint of %d's loop, want %.1f", nearest, want.guid, want.yd)
		}
		if nearest < zgW104Yaw {
			t.Errorf("the stand is %.1f yd 2D from %d's loop, inside the %g yd rule: the patrol walks the party", nearest, want.guid, zgW104Yaw)
		}
	}

	// (7) The radius: the row's own spawns, and no other 11368/14750 spawn inside it.
	var cen dungeon.Point
	for _, q := range g.Positions {
		cen.X += q.X
		cen.Y += q.Y
		cen.Z += q.Z
	}
	n := float32(len(g.Positions))
	cen.X, cen.Y, cen.Z = cen.X/n, cen.Y/n, cen.Z/n
	own := 0
	var extra []creature
	for _, c := range cs {
		if c.Entry != 11368 && c.Entry != 14750 {
			continue
		}
		if d := dist(cen, c.Pos); d >= float64(g.Radius) {
			continue
		}
		mine := false
		for _, q := range g.Positions {
			if dist(q, c.Pos) <= 0.7 {
				mine = true
			}
		}
		if !mine {
			extra = append(extra, c)
			continue
		}
		own++
	}
	if own != len(g.Positions) {
		t.Errorf("pull group %d's radius %.1f holds %d of its own %d spawns, want all: the pack is scoped by spawn",
			zgW104Group, g.Radius, own, len(g.Positions))
	}
	for _, c := range extra {
		t.Errorf("pull group %d's radius %.1f holds %s %d (%d) at %.1f %.1f %.1f, %.1f yd from the centre, which no position names: the pack would take it",
			zgW104Group, g.Radius, c.Name, c.GUID, c.Entry, c.Pos.X, c.Pos.Y, c.Pos.Z, dist(cen, c.Pos))
	}
	for _, q := range loop {
		if d := dist(cen, q); d > float64(g.Radius) {
			t.Errorf("the row's centre is %.1f yd from 49147's waypoint %.1f %.1f %.1f, past the radius %.1f: the pack is scoped away from part of its loop",
				d, q.X, q.Y, q.Z, g.Radius)
		}
	}

	// (8) The later groups' loops and spawns stay out of the two new approach points' 23 yd, except the GM-cleared 11352.
	packs := zgPacks(t, *leg, cs)
	if zgW104Group+1 >= len(packs) {
		t.Fatalf("the jeklik leg has %d pull groups, want one after group %d", len(packs), zgW104Group)
	}
	gm := zgW104Group + 1 // group 8: the 11352 ramp patroller (49761)
	if len(packs[gm]) == 0 || packs[gm][0].Entry != 11352 || packs[gm][0].GUID != 49761 {
		t.Fatalf("leg group %d is %v, want the 11352 ramp patroller 49761 that the walk GM-clears before step 25", gm, packs[gm])
	}
	byGUID := map[uint64]creature{}
	for _, c := range cs {
		byGUID[c.GUID] = c
	}
	for gi := zgW104Group + 1; gi < len(packs); gi++ {
		if gi == gm {
			continue // GM-cleared in the proof: its loop crosses the ramp by design and a dead patroller's loop is dropped
		}
		pts := map[uint64][]dungeon.Point{}
		for _, c := range packs[gi] {
			pts[c.GUID] = append(pts[c.GUID], c.Pos)
			if l := movement[c.GUID]; len(l) > 0 {
				pts[c.GUID] = append(pts[c.GUID], l...)
			}
		}
		for guid, qs := range pts {
			for _, q := range qs {
				for _, at := range []struct {
					name string
					p    dungeon.Point
				}{{"the walked ramp point", zgW104WalkedPoint}, {"the stand", zgW104Stand}} {
					if d := dist(at.p, q); d < zgW104Yaw {
						who := fmt.Sprintf("%d", guid)
						if c, ok := byGUID[guid]; ok {
							who = fmt.Sprintf("%s %d", c.Name, c.Entry)
						}
						t.Errorf("leg pull group %d's %s at %.1f %.1f %.1f is %.1f yd from %s (%.1f %.1f %.1f), inside the %g yd rule: the pull would draw it",
							gi, who, q.X, q.Y, q.Z, d, at.name, at.p.X, at.p.Y, at.p.Z, zgW104Yaw)
						break
					}
				}
			}
		}
	}

	// (9) The stand's line of sight to point 2 (the shot's own condition).
	pl := zgPlannerOrNil(t)
	if pl == nil {
		t.Log("no map-309 mmaps: the stand's line of sight to point 2 is not checked")
		return
	}
	defer pl.Close()
	r, err := pl.Raycast(309, navmesh.Vec3{zgW104Stand.X, zgW104Stand.Y, zgW104Stand.Z + 1.5}, navmesh.Vec3{zgW104Point2.X, zgW104Point2.Y, zgW104Point2.Z + 1})
	if err != nil || !r.Known || !r.Clear {
		t.Errorf("no line of sight from the stand (%.1f %.1f %.1f) to point 2 (%.1f %.1f %.1f): known %v clear %v (%v)",
			zgW104Stand.X, zgW104Stand.Y, zgW104Stand.Z, zgW104Point2.X, zgW104Point2.Y, zgW104Point2.Z, r.Known, r.Clear, err)
	}
}

// zgW104OldRampTop is walk 104's park: the ramp top where the four plateau loops meet.
var zgW104OldRampTop = dungeon.Point{X: -12102.1, Y: -1472.8, Z: 130.05}

// containsAll reports whether s holds every want.
func containsAll(s string, want ...string) bool {
	for _, w := range want {
		if !strings.Contains(s, w) {
			return false
		}
	}
	return true
}
