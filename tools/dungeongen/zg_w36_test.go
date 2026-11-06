// Walk-36 fixes (TortoiseBots-audit tests/raid-zg-walk36): the two water strands (item 3) and the boss's
// authored stand (item 5; the hall's own stand rules are in zg_w38_test.go now, the walk-38 review pulled the
// hall's east side with the boss row). Run step N is the deployed 753-row export
// (20261105101000_world.sql, plan 2db51e2); leg 6 is 10 rows further in the current export (run 543 = dev 553).
//
// The reach used here is the route-aggro model's own (routeReach: AggroRadius at the plan's party level plus the
// raid spread and the spawn's wander), the same function the route check measures with.
package main

import (
	"fmt"
	"math"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// zgW36JindoLeg finds the Jin'do leg.
func zgW36JindoLeg(t *testing.T, p *dungeon.Plan) *dungeon.Leg {
	t.Helper()
	for i := range p.Legs {
		if p.Legs[i].Name == "jindo_the_hexxer" {
			return &p.Legs[i]
		}
	}
	t.Fatal("no jindo_the_hexxer leg")
	return nil
}

// TestZgW36JindoNoPullBesideTheRespawnPit: walk 36 d 09:40:05 and e3 11:48:19 pulled the -11603 -1229 pack (run step
// 552) as "a plain pull" (its stand is 41-45 yd out, past the run leader's 28.5 yd shot), and the party ended 10.9 yd
// from a Sacrificed Troll (d 09:40:30 "Halgrim takes 14826 off the healer (10.9 yd)"): the fight sat inside the pit's
// reach and the pit's 14826s respawn every 10 s (tw_world_creature.sql 49078-49087), so they kept coming until d
// 09:54:16 and e3 11:48:21 paused with "a fight lasted more than 180 s (kept on by nothing hostile engaged)"; 20 dead
// in d2/e3. The pack's stands cannot be fixed: in shot (26-28 yd, with LOS) only the hall's east side works, and the
// navmesh walk there passes 18.4 yd from a troll; the pack's north and west sides have no floor. The pack is 80+ yd
// from the corridor and off the boss route, so its pull is dropped. This fails before the drop.
func TestZgW36JindoNoPullBesideTheRespawnPit(t *testing.T) {
	p, err := dungeon.Load(zgPlan)
	if err != nil {
		t.Fatal(err)
	}
	leg := zgW36JindoLeg(t, p)
	pack := dungeon.Point{X: -11602.5, Y: -1230.4, Z: 79.2}
	for gi, g := range leg.PullGroups {
		for _, q := range g.Positions {
			if dist(q, pack) <= 5 {
				t.Errorf("leg 6 pull group %d pulls the -11603 -1229 pack (a position at %.1f %.1f %.1f): its fight ends inside the respawning pit's reach (walk 36 d/e3, 20 dead, two 180 s stalls)",
					gi, q.X, q.Y, q.Z)
			}
		}
	}
}

// TestZgW36JindoBossStandSeesHim: walk 36 f3-r1 11:57:14 "no LOS from the authored stand; holding", 11:57:30
// "searching a firing stand", 11:57:35 "to firing stand 1 for 11380 ... (24.5 yd from it; was 32.1 yd, los yes)": the
// boss row's authored stand (-11545.6 -1289, 30.6 yd from him) cannot see Jin'do, so the run leader searched, the
// fight drifted (the lead chased a spell target to -11582.9 -1251.2 90, the pit's upper rim: frames seq 3079) and at
// 12:00:32 the boss begin paused with "no walkable path toward the boss ... no safe stand 35.0-25.0 yd from it the
// lead bot has a path to (the lead bot 72.1 yd from it)". The stand must be in the run leader's own window (25-35 yd)
// with line of sight; it fails today on the 30.6 yd stand with none.
func TestZgW36JindoBossStandSeesHim(t *testing.T) {
	p, err := dungeon.Load(zgPlan)
	if err != nil {
		t.Fatal(err)
	}
	leg := zgW36JindoLeg(t, p)
	if leg.Boss == nil || len(leg.Boss.Approach) == 0 {
		t.Fatal("the jindo leg has no boss or no approach")
	}
	stand := leg.Boss.Approach[len(leg.Boss.Approach)-1]
	boss := leg.Boss.Position
	d := dist(stand, boss)
	if d < 25 || d > 35 {
		t.Errorf("the boss stand %.1f %.1f %.1f is %.1f yd from Jin'do, want 25-35 (the run leader's own safe-stand window)", stand.X, stand.Y, stand.Z, d)
	}
	pl := zgPlannerOrNil(t)
	if pl == nil {
		t.Skip("needs the map-309 mmaps (the stand line-of-sight check)")
	}
	defer pl.Close()
	r, err := pl.Raycast(309, navmesh.Vec3{stand.X, stand.Y, stand.Z + 1.5}, navmesh.Vec3{boss.X, boss.Y, boss.Z + 1})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Known || !r.Clear {
		t.Errorf("the boss stand %.1f %.1f %.1f does not see Jin'do (ray known %v clear %v, hit %.1f yd): the run leader searches a firing stand and the fight drifts",
			stand.X, stand.Y, stand.Z, r.Known, r.Clear, r.Hit)
	}
}

// TestZgW36WalkStrandsStayOffTheWater: the two walk-36 water strands are on the run leader's own walks, not on plan
// rows. (a) c3-r2 12:36:24, resumed after a Jin'do wipe: step 536 (leg 6 group 2 approach) "the lead bot is stuck
// 32.3 yd from -11750.1 -1249.6 107.6 (lead in water: yes; cmd move path type 0x81 ...) on the way toward the pack at
// -11614.4 -1282.1 79.4 ... the approach hops got no nearer than 172.6 yd"; frames.jsonl puts the lead at
// -11782.1 -1249.2 103.4, the terrace lake above the hall. (b) b2 11:26 and b2-r1 11:44: step 440 (leg 5 approach
// path -12287.3 -1835.9 131.6) "stuck 37.8/39.8 yd from -11947.2 -1390.2 68.6 / -11944.3 -1353.9 62.3 (lead in
// water: yes)".
//
// No plan row or hop crosses either pool: the nearest hop's server path keeps 20+ yd (a) and 24+ yd (b) from it (the
// deployed 20261105101000 rows, which the cut splits further, keep 0 hops within 20 yd of (a) and one at 28.3 yd of
// (b)). The tests pin those clearances, so a row moved into a pool fails here. The fix is outside the plan:
// see the report (the core's ghost run and the run leader's walk-back choose their own navmesh path).
func TestZgW36WalkStrandsStayOffTheWater(t *testing.T) {
	p, err := dungeon.Load(zgPlan)
	if err != nil {
		t.Fatal(err)
	}
	res, err := planexport.Build(p, planexport.Options{ApproachHops: true})
	if err != nil {
		t.Fatal(err)
	}
	pl := zgPlannerOrNil(t)
	if pl == nil {
		t.Skip("needs the map-309 mmaps (the walk paths under test)")
	}
	defer pl.Close()
	spans := []struct {
		name  string
		at    dungeon.Point
		minYd float64
	}{
		{"the hall's terrace lake (c3-r2 12:36, walk 35 f3)", dungeon.Point{X: -11750.1, Y: -1249.6, Z: 107.6}, 20},
		{"the entrance pool (b2 11:26, b2-r1 11:44)", dungeon.Point{X: -11944.3, Y: -1353.9, Z: 62.3}, 24},
	}
	for _, sp := range spans {
		best := math.Inf(1)
		where := ""
		var prev *planexport.StepRow
		prevN := 0
		for i := range res.Rows.Steps {
			s := &res.Rows.Steps[i]
			if s.X == 0 && s.Y == 0 && s.Z == 0 {
				continue
			}
			if prev != nil && prev.Leg == s.Leg {
				path, perr := pl.Plan(309, navmesh.Vec3{prev.X, prev.Y, prev.Z}, navmesh.Vec3{s.X, s.Y, s.Z})
				if perr == nil && path.Status == navmesh.Complete {
					for _, q := range path.Points {
						if d := dist(sp.at, dungeon.Point{X: q[0], Y: q[1], Z: q[2]}); d < best {
							best, where = d, fmt.Sprintf("rows %d->%d (%q -> %q)", prevN, i+1, prev.Comment, s.Comment)
						}
					}
				}
			}
			prev, prevN = s, i+1
		}
		if best < sp.minYd {
			t.Errorf("%s: %s passes %.1f yd from the water, want >= %.0f: the plan must not route a walk through the pool (the strands are the run leader's own walks, not plan rows)", sp.name, where, best, sp.minYd)
		}
	}
}

var _ = planexport.KindMove
