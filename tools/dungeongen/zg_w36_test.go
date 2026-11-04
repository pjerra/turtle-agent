// Walk-36 fixes (TortoiseBots-audit tests/raid-zg-walk36): the Jin'do hall's stand pulls (items 1 and 2), the
// two water strands (item 3) and the boss's authored stand (item 5). Run step N is the deployed 753-row export
// (20261105101000_world.sql, plan 2db51e2); leg 6 is 10 rows further in the current export (run 543 = dev 553).
//
// The reach used here is the route-aggro model's own (routeReach: AggroRadius at the plan's party level plus the
// raid spread and the spawn's wander), the same function the route check measures with.
package main

import (
	"math"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
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

// zgW36HallGroups lists the leg's pull groups that fight in Jin'do's hall: a group with a position within 45 yd of
// the hall's centre (-11540 -1262 78).
func zgW36HallGroups(p *dungeon.Plan, leg *dungeon.Leg) []int {
	hall := dungeon.Point{X: -11540, Y: -1262, Z: 78}
	var out []int
	for gi, g := range leg.PullGroups {
		for _, q := range g.Positions {
			if dist(q, hall) <= 45 {
				out = append(out, gi)
				break
			}
		}
	}
	return out
}

// zgW36Centroid is a pull group's row point (the exporter's own rule: the centroid of its positions).
func zgW36Centroid(g dungeon.PullGroup) dungeon.Point {
	var c dungeon.Point
	for _, q := range g.Positions {
		c.X += q.X
		c.Y += q.Y
		c.Z += q.Z
	}
	n := float32(len(g.Positions))
	return dungeon.Point{X: c.X / n, Y: c.Y / n, Z: c.Z / n}
}

// zgW36Order is a group's place in the plan's pull order (a later leg and a later group pull later).
func zgW36Order(leg int, group int) int { return leg*1000 + group }

// zgW36Owner maps every spawn to the order of the first group that pulls it (dungeon.PullGroup.Positions and radius,
// the exporter's ownership rule).
func zgW36Owner(p *dungeon.Plan, cs []creature) map[uint64]int {
	own := map[uint64]int{}
	for li := range p.Legs {
		leg := &p.Legs[li]
		for gi := range leg.PullGroups {
			g := leg.PullGroups[gi]
			o := zgW36Order(li, gi)
			c := zgW36Centroid(g)
			for _, sp := range cs {
				if have, ok := own[sp.GUID]; ok && have <= o {
					continue
				}
				for _, e := range g.Entries {
					if e != sp.Entry {
						continue
					}
					if dist(c, sp.Pos) <= float64(g.Radius) {
						own[sp.GUID] = o
					}
				}
			}
		}
	}
	return own
}

func max32(a, b float32) float32 {
	if a > b {
		return b
	}
	return a
}

// zgW36ApproachPoints is every point the party stands on for the group: its approach points (the last of them is the
// stand) and the row point a plain fallback pull would walk to.
func zgW36ApproachPoints(g dungeon.PullGroup) []dungeon.Point {
	out := append([]dungeon.Point(nil), g.Approach...)
	if !g.StandPull || len(g.Approach) == 0 {
		out = append(out, zgW36Centroid(g))
	}
	return out
}

// TestZgW36JindoHallStandsKeepTheBossReach: walk 36 f/f3 (10:01, 11:03, 12:33) and c3 (11:35) had the group-8 stand
// pull (run step 543, stand -11560 -1280) engage Jin'do: the stand and its approach stand 25.2-28.4 yd from the
// honour guard's western units (reach 30) and the guard lives 8-18 yd from the boss, so the guard's fight brings him
// in (f3 11:03:44 "engaged 15 over 6" and "boss 11380 engaged at step 543"). It fails today: the group-8 stand is
// 25.2 yd from -11528.5 -1270.3 (14825, reach 30), and the guard's own stand is 17.5 yd from group 9's units.
//
// The rule: every point a hall group stands on keeps the route-aggro reach of every hostile it does not pull, and
// of Jin'do (reach 33 at party level 60), measured 3D as the check measures it.
func TestZgW36JindoHallStandsKeepTheBossReach(t *testing.T) {
	p, err := dungeon.Load(zgPlan)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := readCreatures("testdata/map309/creatures.tsv")
	if err != nil {
		t.Fatal(err)
	}
	leg := zgW36JindoLeg(t, p)
	boss := leg.Boss.Position
	var jindo *creature
	for i := range cs {
		if cs[i].Entry == 11380 {
			jindo = &cs[i]
		}
	}
	if jindo == nil {
		t.Fatal("no Jin'do spawn in the map-309 export")
	}
	// The route-aggro model's own reach: AggroRadius at the plan's party level (58) plus the raid spread.
	bossReach := routeReach(p, *jindo)
	if math.Abs(bossReach-35) > 0.01 {
		t.Fatalf("Jin'do's reach is %.1f yd, want 35 (25 aggro at party level 58 + 10 raid spread)", bossReach)
	}
	groups := zgW36HallGroups(p, leg)
	if len(groups) < 2 {
		t.Fatalf("%d hall pull groups, want the north packs and the honour guard", len(groups))
	}
	owner := zgW36Owner(p, cs)
	pl := zgPlannerOrNil(t)
	if pl == nil {
		t.Skip("needs the map-309 mmaps (the stand line-of-sight check)")
	}
	defer pl.Close()
	li := 6
	for _, gi := range groups {
		g := leg.PullGroups[gi]
		for pi, q := range zgW36ApproachPoints(g) {
			what := "approach"
			if pi == len(zgW36ApproachPoints(g))-1 {
				what = "stand"
			}
			for _, c := range cs {
				if o, ok := owner[c.GUID]; ok && o <= zgW36Order(li, gi) {
					continue // this pull's own pack, or one pulled before it
				}
				if !hostileMob(c, map[int]bool{35: true}, nil) || c.FlagsExtra&worldspawn.NoAggro != 0 {
					continue
				}
				if c.Wander > 50 {
					continue // the export's wander typo (11356 yd): planlint caps it
				}
				reach := routeReach(p, c)
				if reach <= 0 {
					continue
				}
				if d := dist(q, c.Pos); d < reach {
					t.Errorf("leg 6 pull group %d %s (%.1f %.1f %.1f) stands %.1f yd from %s %d (guid %d, %.1f %.1f %.1f): the reach is %.1f yd and the run has not pulled it: the fight would drag it in",
						gi, what, q.X, q.Y, q.Z, d, c.Name, c.Entry, c.GUID, c.Pos.X, c.Pos.Y, c.Pos.Z, reach)
				}
			}
			if d := dist(q, boss); d < bossReach {
				t.Errorf("leg 6 pull group %d %s (%.1f %.1f %.1f) stands %.1f yd from Jin'do 11380: the reach is %.1f yd",
					gi, what, q.X, q.Y, q.Z, d, bossReach)
			}
		}
		// A stand pull's stand must see its pack: with no line of sight the run leader searches a firing stand 25-35 yd
		// from the pack and moves the party there (walk 36 f3 11:03:23 "no LOS from the authored stand after 20 s;
		// searching a firing stand"), which is how the party entered the guard's reach.
		if g.StandPull && len(g.Approach) > 0 {
			stand := g.Approach[len(g.Approach)-1]
			seen := 0
			for _, u := range g.Positions {
				r, err := pl.Raycast(309, navmesh.Vec3{stand.X, stand.Y, stand.Z + 1.5}, navmesh.Vec3{u.X, u.Y, u.Z + 1})
				if err == nil && r.Known && r.Clear {
					seen++
				}
			}
			if seen == 0 {
				t.Errorf("leg 6 pull group %d's stand (%.1f %.1f %.1f) sees none of its %d units: the run leader searches a firing stand and walks the party into the next pack",
					gi, stand.X, stand.Y, stand.Z, len(g.Positions))
			}
		}
	}
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

var _ = planexport.KindMove
