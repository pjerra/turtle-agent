// Walk-39 fixes (TortoiseBots-audit tests/raid-zg-walk39): the five moving Gurubashi Bat
// Riders (14750) are patrollers. Before walk 39 the leg pulled F1 from its own camp and the
// west camps in an order that let riders walk into another pack's fight: m1-6's step 29
// brought 11 bodies (F1 + F3/F4), and step 43 brought 13 and joined 16 (11368 x10, 11831 x2,
// 14750 x4). These tests pin the MANDOKIR-440 handling the walk-37 patrol rows use: each
// rider's movement.tsv loop is the patrol block of the group that pulls its formation, the
// group is a stand pull, and its stand is in the tank's shot of that loop.
package main

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// zgW39Movers are the 14750 spawns with a creature_movement path (the other six 14750s stand):
// F1 49147, F3 49155, F4 49189, F5 49185 and F6 49130.
var zgW39Movers = []uint64{49147, 49155, 49189, 49185, 49130}

// TestZgW39JeklikBatRidersArePatrollers: every moving Bat Rider's loop is carried by the
// pull group of its formation, that group is a stand pull, and the stand is inside the
// run leader's 28.5 yd shot of the loop (the gate fires as the loop comes round) and inside
// its 60 yd sight of the row's point.
func TestZgW39JeklikBatRidersArePatrollers(t *testing.T) {
	p, err := dungeon.Load(filepath.Join(plansDirT, "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	movement, err := readMovementPaths(filepath.Join("testdata", "map309", "movement.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	leg := zgW35Leg(t, p, "high_priestess_jeklik")
	for _, guid := range zgW39Movers {
		loop, ok := movement[guid]
		if !ok || len(loop) < 2 {
			t.Fatalf("movement.tsv: %d has no loop", guid)
		}
		gi := -1
		for i := range leg.PullGroups {
			if pt := leg.PullGroups[i].Patrol; pt != nil && strings.Contains(pt.Source, fmt.Sprintf("creature_movement %d", guid)) {
				gi = i
			}
		}
		if gi < 0 {
			t.Errorf("no leg-0 pull group carries the %d Bat Rider's loop: the rider walks into another pack with no patrol mark", guid)
			continue
		}
		g := leg.PullGroups[gi]
		if !g.StandPull {
			t.Errorf("pull group %d (Bat Rider %d) is not a stand pull", gi, guid)
		}
		if len(g.Patrol.Path) != len(loop) {
			t.Errorf("pull group %d patrol path has %d points, want %d", gi, len(g.Patrol.Path), len(loop))
			continue
		}
		for k := range loop {
			if d := dist(g.Patrol.Path[k], loop[k]); d > 0.5 {
				t.Errorf("pull group %d patrol path[%d] is %.1f yd from %d's waypoint", gi, k, d, guid)
				break
			}
		}
		if len(g.Approach) == 0 {
			t.Errorf("pull group %d has no stand", gi)
			continue
		}
		stand := g.Approach[len(g.Approach)-1]
		nearest := math.Inf(1)
		for _, q := range loop {
			if d := dist(stand, q); d < nearest {
				nearest = d
			}
		}
		if nearest > zgW37TankShotYd {
			t.Errorf("pull group %d stand is %.1f yd from the nearest waypoint of %d's loop, past the %g yd shot: the gate never fires", gi, nearest, guid, zgW37TankShotYd)
		}
		var cen dungeon.Point
		for _, q := range g.Positions {
			cen.X += q.X
			cen.Y += q.Y
			cen.Z += q.Z
		}
		n := float32(len(g.Positions))
		cen.X, cen.Y, cen.Z = cen.X/n, cen.Y/n, cen.Z/n
		if d := dist(cen, stand); d > dungeon.RunLeaderSeeYd {
			t.Errorf("pull group %d stand is %.1f yd from its point, want <= %d", gi, d, dungeon.RunLeaderSeeYd)
		}
	}
}

// TestZgW39JeklikF1StandClearsTheOtherLanes: walk 39's step 29 fight started on the walk to
// F1's camp: the party crossed the 49135/49155/49189 lanes and the east-turned F3/F4 joined
// F1 (11 bodies). F1's stand is the walked point -12080 -1461 now, 15.6-20.6 yd from F1's
// east lane and >= 30 yd from the three formations that walk it next.
func TestZgW39JeklikF1StandClearsTheOtherLanes(t *testing.T) {
	p, err := dungeon.Load(filepath.Join(plansDirT, "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	movement, err := readMovementPaths(filepath.Join("testdata", "map309", "movement.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	leg := zgW35Leg(t, p, "high_priestess_jeklik")
	var g *dungeon.PullGroup
	for i := range leg.PullGroups {
		if pt := leg.PullGroups[i].Patrol; pt != nil && strings.Contains(pt.Source, "creature_movement 49147") {
			g = &leg.PullGroups[i]
			break
		}
	}
	if g == nil || len(g.Approach) == 0 {
		t.Fatal("no F1 stand pull carrying 49147's loop")
	}
	stand := g.Approach[len(g.Approach)-1]
	nearest := func(guid uint64) float64 {
		best := math.Inf(1)
		for _, q := range movement[guid] {
			if d := dist(stand, q); d < best {
				best = d
			}
		}
		return best
	}
	if d := nearest(49147); d > zgW37TankShotYd {
		t.Errorf("F1 stand is %.1f yd from 49147's loop (want <= %g)", d, zgW37TankShotYd)
	}
	for _, guid := range []uint64{49135, 49155, 49189} {
		if d := nearest(guid); d < 30 {
			t.Errorf("F1 stand is %.1f yd from %d's loop, want >= 30: the lane walks the party", d, guid)
		}
	}
}

// TestZgW39JeklikNorthCampStandPulls49123: walk 39's step-46 pull was refused with "the
// tank could not start moving to a firing position" while 11368 49123 stood 8.8 yd from the
// row centre and the lead bot 53.1 yd from it (stand -12175.4 -1447.4). The F6 formation is
// the north camp's 10-body row now, pulled from the walked point -12250 -1418: 49123 is in
// the tank's shot from there and the stand is inside the run leader's sight of the point.
func TestZgW39JeklikNorthCampStandPulls49123(t *testing.T) {
	p, err := dungeon.Load(filepath.Join(plansDirT, "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	cs, err := readCreatures(filepath.Join("testdata", "map309", "creatures.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	var bat *creature
	for i := range cs {
		if cs[i].GUID == 49123 {
			bat = &cs[i]
		}
	}
	if bat == nil {
		t.Fatal("no spawn 49123")
	}
	leg := zgW35Leg(t, p, "high_priestess_jeklik")
	gi := -1
	for i := range leg.PullGroups {
		for _, q := range leg.PullGroups[i].Positions {
			if dist(q, bat.Pos) < 1 {
				gi = i
			}
		}
	}
	if gi < 0 {
		t.Fatal("no leg-0 pull group owns the 49123 spawn")
	}
	g := leg.PullGroups[gi]
	if !g.StandPull || len(g.Approach) == 0 {
		t.Fatalf("the 49123 row is not a stand pull with a stand")
	}
	stand := g.Approach[len(g.Approach)-1]
	if d := dist(stand, bat.Pos); d > zgW37TankShotYd {
		t.Errorf("the 49123 row's stand is %.1f yd from its spawn, past the %g yd shot", d, zgW37TankShotYd)
	}
	var cen dungeon.Point
	for _, q := range g.Positions {
		cen.X += q.X
		cen.Y += q.Y
		cen.Z += q.Z
	}
	n := float32(len(g.Positions))
	cen.X, cen.Y, cen.Z = cen.X/n, cen.Y/n, cen.Z/n
	if d := dist(cen, stand); d > dungeon.RunLeaderSeeYd {
		t.Errorf("the 49123 row's stand is %.1f yd from its point, want <= %d", d, dungeon.RunLeaderSeeYd)
	}
}
