package main

import (
	"fmt"
	"math"
	"slices"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
)

// Walk 107 st7b (strict, 2026-11-15 02:45Z) wiped on the bloodlord_mandokir leg's
// step 419, and walk 104 fz5c-1 stalled on the same row. The row pulled the lone
// Vilebranch Speaker 11391 and, 9 s later, the party was fighting Bloodlord
// Mandokir 11382 and Ohgan 14988 next to its spawn, with the 49270 pack pulled in
// 4 s after that: 26 dead.
//
// The cause is the encounter's own script (boss_mandokir.cpp): while 11391 lives,
// Mandokir carries PACIFIED|SPAWNING|NOT_SELECTABLE|IMMUNE_TO_PLAYER on his
// platform; the moment 11391 dies, CheckVilebranchState (:200) clears those flags,
// sets his home to the stairs' foot (-12195 -1948 130, 1.7 yd from the speaker's
// row point), walks him there and MoveInLineOfSight (:360) attacks any player
// within 40 yd. Killing the speaker starts the boss fight, so every row after it is
// fought with Mandokir awake in the party's lap.
//
// This test pins the order that keeps one pack at a time: the speaker's group is
// the leg's LAST pull (after the 49270 camp and the 14821 raptors), its stand is
// out of the speaker's wake and inside the tank's shot with a clear ray to it, the
// two camps' stands keep the walk between them out of the live speaker's wake
// (they are pulled while it lives), and the boss's stand stays outside his aggro
// and inside the run leader's sight.
func TestZgW107MandokirSpeakerIsTheLastPull(t *testing.T) {
	p, cs := zgLoadPlanAndExport(t)
	leg := zgMandokirLeg(t, p)
	if len(leg.PullGroups) != 20 {
		t.Fatalf("the leg has %d pull groups, want 20 (this test reads their order)", len(leg.PullGroups))
	}
	// The groups by a spawn that does not move in this fix: the speaker's entry,
	// the 49270 camp's Blood Drinker spawn and the raptor formation's west raptor.
	near := func(g dungeon.PullGroup, at dungeon.Point) bool {
		for _, q := range g.Positions {
			if dist(q, at) <= 0.7 {
				return true
			}
		}
		return false
	}
	speaker, camp, raptors := -1, -1, -1
	for i, g := range leg.PullGroups {
		switch {
		case slices.Contains(g.Entries, 11391):
			speaker = i
		case slices.Contains(g.Entries, 11353) && near(g, dungeon.Point{X: -12186.9, Y: -1977, Z: 133.16}):
			camp = i
		case len(g.Entries) == 6 && near(g, dungeon.Point{X: -12160.1, Y: -1952.02, Z: 134.91}):
			raptors = i
		}
	}
	if speaker < 0 || camp < 0 || raptors < 0 {
		t.Fatalf("the leg's groups are %d (camp) and %d (raptors) and %d (the Vilebranch Speaker), want all three", camp, raptors, speaker)
	}
	// The speaker's death wakes Mandokir: it must be the last thing the leg pulls.
	if speaker != len(leg.PullGroups)-1 {
		t.Fatalf("the leg pulls the Vilebranch Speaker 11391 at pull group %d of %d, want the last one: its death is Mandokir's wake (boss_mandokir.cpp CheckVilebranchState), so every later row is fought with the boss in the party's lap (walk 107 st7b: step 419, 26 dead)", speaker, len(leg.PullGroups))
	}
	if camp >= speaker || raptors >= speaker {
		t.Errorf("the camps are pulled at groups %d (49270) and %d (14821), want both before the speaker's group %d", camp, raptors, speaker)
	}
	speakerSpawn := dungeon.Point{X: -12196.3, Y: -1948.37, Z: 130.36}
	stand := func(gi int) dungeon.Point {
		t.Helper()
		g := leg.PullGroups[gi]
		if len(g.Approach) == 0 {
			t.Fatalf("pull group %d has no stand (approach point)", gi)
		}
		return g.Approach[len(g.Approach)-1]
	}
	// The speaker's stand: the walked floor 25.1 yd from it, inside the tank's
	// 28 yd shot and outside the 21 yd wake a level-61 spawn wakes a level-60 raid
	// at, with a clear ray (the old stand -12216 -1930 132.62 had none: walk 107
	// step 419 "no LOS from the authored stand; holding").
	sStand := stand(speaker)
	if d := dist(sStand, dungeon.Point{X: -12188, Y: -1972, Z: 132.1}); d > 0.2 {
		t.Errorf("the speaker's stand is %.1f %.1f %.1f, want -12188 -1972 132.1 (%.1f yd away)", sStand.X, sStand.Y, sStand.Z, d)
	}
	if d := dist(sStand, speakerSpawn); d < 19.5 || d > 28.0 {
		t.Errorf("the speaker's stand is %.1f yd from its spawn, want 19.5-28 (outside its wake, inside the tank's shot)", d)
	}
	// The raptors' stand: the walk from the 49270 camp has to stay out of the live
	// speaker's wake (the packs are pulled before the speaker dies), and its hop
	// from the camp row's point (the centroid the exporter writes) is legal.
	rStand := stand(raptors)
	var campRow dungeon.Point
	for _, q := range leg.PullGroups[camp].Positions {
		campRow.X += q.X
		campRow.Y += q.Y
		campRow.Z += q.Z
	}
	n := float32(len(leg.PullGroups[camp].Positions))
	campRow = dungeon.Point{X: campRow.X / n, Y: campRow.Y / n, Z: campRow.Z / n}
	if d := dist(rStand, dungeon.Point{X: -12177, Y: -1978, Z: 133.5}); d > 0.2 {
		t.Errorf("the raptors' stand is %.1f %.1f %.1f, want -12177 -1978 133.5 (%.1f yd away)", rStand.X, rStand.Y, rStand.Z, d)
	}
	if d := dist(rStand, speakerSpawn); d < 23 {
		t.Errorf("the raptors' stand is %.1f yd from the live speaker's spawn, want >= 23 (it was -12180 -1966 130.84, 20.8 yd: the party wakes the speaker while the raptors are still up)", d)
	}
	if d := dist(rStand, campRow); d < 8 {
		t.Errorf("the raptors' stand is %.1f yd from the 49270 camp's row point, want >= 8 (a shorter hop is one arrival radius, planlint short hop)", d)
	}
	if d := dist(campRow, dungeon.Point{X: -12185.82, Y: -1979.19, Z: 133.67}); d > 0.05 {
		t.Errorf("the 49270 camp's row point is %.1f %.1f %.1f, want the unchanged centroid -12185.82 -1979.19 133.67", campRow.X, campRow.Y, campRow.Z)
	}
	// The boss's stand: outside his aggro plus planlint's 8 yd margin (31 yd) and
	// inside the run leader's 60 yd sight of the row, on the arena floor he fights
	// on after the speaker dies.
	bossSpawn := dungeon.Point{X: -12167.8, Y: -1927.25, Z: 153.73}
	if leg.Boss == nil {
		t.Fatal("the leg has no boss")
	}
	if d := dist(leg.Boss.Position, bossSpawn); d > 0.05 {
		t.Errorf("the boss row's position is %.1f %.1f %.1f, want the unchanged spawn row -12167.8 -1927.25 153.73", leg.Boss.Position.X, leg.Boss.Position.Y, leg.Boss.Position.Z)
	}
	if len(leg.Boss.Approach) == 0 {
		t.Fatal("the boss row has no stand: the party would be left at the speaker's row point, 54 yd from him, in hop range only")
	}
	bStand := leg.Boss.Approach[len(leg.Boss.Approach)-1]
	if d := dist(bStand, dungeon.Point{X: -12188, Y: -1956, Z: 130.6}); d > 0.2 {
		t.Errorf("the boss's stand is %.1f %.1f %.1f, want -12188 -1956 130.6 (%.1f yd away)", bStand.X, bStand.Y, bStand.Z, d)
	}
	if d := dist(bStand, bossSpawn); d < 31 || d > 50 {
		t.Errorf("the boss's stand is %.1f yd from his spawn, want 31-50 (outside his aggro plus the 8 yd margin, inside the run leader's sight)", d)
	}
	// The walks, on the server's navmesh when this machine has it: the camps are
	// pulled while the speaker lives, so the party's walk between their stands and
	// the walk into the speaker's stand keep out of its wake; the stand sees it.
	pl := zgPlannerOrNil(t)
	if pl == nil {
		t.Log("no map-309 mmaps on this machine: the ray and the two walks are not measured")
		return
	}
	defer pl.Close()
	if ray, err := pl.Raycast(309, navmesh.Vec3{sStand.X, sStand.Y, sStand.Z + 1.6}, navmesh.Vec3{speakerSpawn.X, speakerSpawn.Y, speakerSpawn.Z + 1.6}); err != nil {
		t.Fatalf("raycast: %v", err)
	} else if !ray.Clear {
		t.Errorf("the speaker's stand has no clear ray to its spawn (hit at %.1f yd): the run leader holds and pullbacks, which is what walked the tank into the boss's reach in walk 107", ray.Hit)
	}
	for _, w := range []struct {
		what string
		from dungeon.Point
	}{
		{"49270 camp's stand", stand(camp)},
		{"raptors' stand", rStand},
	} {
		if d, err := zgWalkClosest(pl, w.from, sStand, speakerSpawn); err != nil {
			t.Fatalf("%s: %v", w.what, err)
		} else if d < 23 {
			t.Errorf("the walk from the %s %.1f %.1f %.1f to the speaker's stand passes %.1f yd of the live speaker's spawn, want >= 23: the party wakes it on the way", w.what, w.from.X, w.from.Y, w.from.Z, d)
		}
	}
	if d, err := zgWalkClosest(pl, stand(camp), rStand, speakerSpawn); err != nil {
		t.Fatalf("49270 camp's stand to the raptors' stand: %v", err)
	} else if d < 23 {
		t.Errorf("the walk from the 49270 camp's stand %.1f %.1f %.1f to the raptors' stand passes %.1f yd of the live speaker's spawn, want >= 23 (it was 20.8 before the raptors' stand moved): the party wakes the speaker while the raptors are still up", stand(camp).X, stand(camp).Y, stand(camp).Z, d)
	}
	_ = cs
}

// zgWalkClosest is the closest the navmesh walk from one point to another passes
// a third: the path's corners and its ground samples, not the straight line.
func zgWalkClosest(pl *navmesh.Planner, from, to, watch dungeon.Point) (float64, error) {
	path, err := pl.Plan(309, navmesh.Vec3{from.X, from.Y, from.Z}, navmesh.Vec3{to.X, to.Y, to.Z})
	if err != nil {
		return 0, err
	}
	if path.Status != navmesh.Complete {
		return 0, fmt.Errorf("the walk %s %.1f %.1f %.1f -> %.1f %.1f %.1f is %s (%s)", "is not complete", from.X, from.Y, from.Z, to.X, to.Y, to.Z, path.Status, path.Reason)
	}
	closest := math.Inf(1)
	for _, v := range path.Points {
		if d := dist(dungeon.Point{X: v[0], Y: v[1], Z: v[2]}, watch); d < closest {
			closest = d
		}
	}
	for _, g := range path.Ground {
		for _, v := range g {
			if d := dist(dungeon.Point{X: v[0], Y: v[1], Z: v[2]}, watch); d < closest {
				closest = d
			}
		}
	}
	return closest, nil
}

// Walk 110 st10d wiped twice on bloodlord_mandokir step 418 (pull group 17):
// the authored approach -12216 -1974 132.56 had no line of sight to the 5-mob
// camp, forcing a 20 s hold and a fallback to firing stand 1 (-12211.1 -1974.6
// 132.5), after which the plain pull pushed east into group 18 (14821 x6) and
// group 19 (11391, waking Mandokir 11382). Moving the stand to firing stand 1
// and marking it stand_pull holds the raid there with clear sight.
func TestZgW110MandokirGroup17StandPullAndSight(t *testing.T) {
	p, _ := zgLoadPlanAndExport(t)
	leg := zgMandokirLeg(t, p)
	if len(leg.PullGroups) < 20 {
		t.Fatalf("the leg has %d pull groups, want at least 20", len(leg.PullGroups))
	}
	g16, g17, g18, g19 := leg.PullGroups[16], leg.PullGroups[17], leg.PullGroups[18], leg.PullGroups[19]
	if !g17.StandPull {
		t.Errorf("leg 5 pull group 17 (step 418) has stand_pull=false, want true so the raid holds at its firing stand instead of pushing into group 18 (14821) and group 19 (11391)")
	}
	if len(g17.Approach) == 0 {
		t.Fatal("leg 5 pull group 17 has no approach")
	}
	stand := g17.Approach[len(g17.Approach)-1]
	own := math.Inf(1)
	for _, q := range g17.Positions {
		if d := dist(stand, q); d < own {
			own = d
		}
	}
	if own < 19.5 || own > 28.0 {
		t.Errorf("leg 5 pull group 17 stand %.1f %.1f %.1f is %.1f yd from its nearest unit, want 19.5-28.0", stand.X, stand.Y, stand.Z, own)
	}
	for _, q := range g18.Positions {
		if d := dist(stand, q); d < 23 {
			t.Errorf("leg 5 pull group 17 stand %.1f %.1f %.1f is %.1f yd from group 18 (14821), want >= 23", stand.X, stand.Y, stand.Z, d)
		}
	}
	for _, q := range g19.Positions {
		if d := dist(stand, q); d < 23 {
			t.Errorf("leg 5 pull group 17 stand %.1f %.1f %.1f is %.1f yd from group 19 (11391), want >= 23", stand.X, stand.Y, stand.Z, d)
		}
	}
	pl := zgPlannerOrNil(t)
	if pl == nil {
		t.Log("no map-309 mmaps on this machine: the ray and walk are not measured")
		return
	}
	defer pl.Close()
	// 11830 Hakkari Priest in group 17 is at index 1 (-12187, -1978.89, 133.47).
	priest := g17.Positions[1]
	if ray, err := pl.Raycast(309, navmesh.Vec3{stand.X, stand.Y, stand.Z + 1.6}, navmesh.Vec3{priest.X, priest.Y, priest.Z + 1.6}); err != nil {
		t.Fatalf("raycast: %v", err)
	} else if !ray.Clear {
		t.Errorf("leg 5 pull group 17 stand %.1f %.1f %.1f has no clear ray to 11830 (hit at %.1f yd)", stand.X, stand.Y, stand.Z, ray.Hit)
	}
	prev := g16.Approach[len(g16.Approach)-1]
	if path, err := pl.Plan(309, navmesh.Vec3{prev.X, prev.Y, prev.Z}, navmesh.Vec3{stand.X, stand.Y, stand.Z}); err != nil {
		t.Fatalf("plan: %v", err)
	} else if path.Status != navmesh.Complete {
		t.Errorf("walk from group 16 stand %.1f %.1f %.1f to group 17 stand %.1f %.1f %.1f is %s (%s)", prev.X, prev.Y, prev.Z, stand.X, stand.Y, stand.Z, path.Status, path.Reason)
	}
}
