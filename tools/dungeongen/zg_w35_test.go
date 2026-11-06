// Walk-35 fixes (TortoiseBots-audit tests/raid-zg-walk35): the water bounds
// (item 1), the Jeklik/Arlokk patroller pulls (item 2) and the Mar'li-stand
// bound (item 3).
//
// Run step N is export index N-1 (the run numbers from 1). The water strands sit
// on walk-back/ghost walks, not on plan rows (see TestZgW35WalkBackWaterBounds).
package main

import (
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// zgW35Leg finds the named leg.
func zgW35Leg(t *testing.T, p *dungeon.Plan, name string) *dungeon.Leg {
	t.Helper()
	for i := range p.Legs {
		if p.Legs[i].Name == name {
			return &p.Legs[i]
		}
	}
	t.Fatalf("no %s leg", name)
	return nil
}

// Walk-35 water bounds (item 1): the strands sit on walk-back/ghost walks, not on
// plan rows (run step N is export index N-1; the stuck targets are live hop
// corners, 16+ yd from any row). No authored dry reroute exists: the Venoxis lake
// holds the packs (legs 1/6 canal rows swim 26-61 yd), the S1 pond sits in a
// steep-walled basin (PlanNoWater detours 900 yd via Venoxis), the canal rows are
// dry bridges with the river 28-38 yd below (the lead fell in), and the plateau
// lake (-11740 -1240) and the door (-11916 -1243, dry) have no plan rows within
// 40 yd. These tests pin the implicated forward segments' probe water yards, so
// any change that makes the walks wetter fails here. They need the map-309 mmaps
// (zgPlannerOrNil) and skip without them.
//
// Spots (TortoiseBots-audit tests/raid-zg-walk35/WATER.txt): e3 step 204 (the
// Mar'li walk-back via the Venoxis lake), a4 step 205 (same), f3 step 580 (the
// Jin'do walk-back; the plateau lake has no rows, so S1, the wettest forward
// segment of the Jin'do walk, stands in for it).
func TestZgW35WalkBackWaterBounds(t *testing.T) {
	pl := zgPlannerOrNil(t)
	if pl == nil {
		t.Skip("needs the map-309 mmaps")
	}
	defer pl.Close()
	// (from, to) plan points of the implicated forward segments and the probe
	// water yards ceiling for each (measured 2026-11-04; the lake rows are wet
	// by design, the pond unavoidable — see above).
	segs := []struct {
		name       string
		a, b       [3]float32
		waterYdMax float32
	}{
		{"venoxis lake approach", [3]float32{-11989.4, -1561.6, 27.4}, [3]float32{-12044.6, -1581.38, 15.03}, 65},
		{"venoxis lake pull-pull", [3]float32{-12044.6, -1581.38, 15.03}, [3]float32{-12018.9, -1584.4, 14.43}, 30},
		{"jindo S1 pond", [3]float32{-11831.533, -1319.2001, 67.27638}, [3]float32{-11756, -1300, 64.62}, 68},
	}
	for _, s := range segs {
		path, err := pl.Plan(309, s.a, s.b)
		if err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if path.Status != "complete" {
			t.Errorf("%s: plan status %v (want complete)", s.name, path.Status)
		}
		var wet float32
		for i := 1; i < len(path.Points); i++ {
			a, b := path.Points[i-1], path.Points[i]
			dx, dy, dz := b[0]-a[0], b[1]-a[1], b[2]-a[2]
			d := float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
			mx, my, mz := (a[0]+b[0])/2, (a[1]+b[1])/2, (a[2]+b[2])/2
			fl, err := pl.Floors(309, mx, my, mz-10, mz+10)
			if err != nil {
				t.Fatalf("%s: %v", s.name, err)
			}
			for _, f := range fl {
				if f.Flags&0x08 != 0 {
					wet += d
					break
				}
			}
		}
		if wet > s.waterYdMax {
			t.Errorf("%s: %.1f wet yd (want <= %.1f)", s.name, wet, s.waterYdMax)
		}
	}
}

// TestZgW35MarliStandKeeps41YdFromPack0: walk 35 run-205 (leg-2 group 0, pack 1)
// wiped 3x with 11 engaged over the CC capacity: the east-slope stand stages
// inside pack 1 (15 yd) and 41 yd from pack 0's nearest spawn, and the fight
// drifts south into pack 0. The slope offers no farther staging (NE is off-mesh,
// SW walks into pack 0; probed 2026-11-04), and pack 0's loop threads through
// its own SW cluster, so no per-group stand-pull split stages clear either. This
// pins the stand's clearance: it must not move nearer pack 0.
func TestZgW35MarliStandKeeps41YdFromPack0(t *testing.T) {
	p, err := dungeon.Load(plansDirT + "/zul_gurub.json")
	if err != nil {
		t.Fatal(err)
	}
	cs, err := readCreatures(filepath.Join("testdata", "map309", "creatures.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	leg := zgW35Leg(t, p, "high_priestess_marli")
	if len(leg.PullGroups) < 1 || len(leg.PullGroups[0].Approach) == 0 {
		t.Fatal("leg-2 group 0 has no stand")
	}
	stand := leg.PullGroups[0].Approach[len(leg.PullGroups[0].Approach)-1]
	best := math.Inf(1)
	for _, c := range cs {
		if c.GroupLeader == 49202 || c.GUID == 49202 {
			if d := dist(stand, c.Pos); d < best {
				best = d
			}
		}
	}
	if best < 40 {
		t.Errorf("group-0 stand %.1f %.1f %.1f is %.1f yd from pack 0 (want >= 40: the walk-35 wipes drifted into it at 41)", stand.X, stand.Y, stand.Z, best)
	}
}

// TestZgW35JeklikGroup13KnowsTheBatLoop: walk 35 run-42 (leg-0 group 13, formation
// 49137) paused twice: the walk to the leftover 11368 passes the patrolling 14750
// 49185 at 12.1/0.9 yd, and the run reports "its patrol's loop was not read". The
// group carries 49185's 7-point movement.tsv loop as a patrol block (entries
// [14750], estimated period over the loop length at the unit's walk speed), the
// way the nine roaming Berserkers are handled.
func TestZgW35JeklikGroup13KnowsTheBatLoop(t *testing.T) {
	p, err := dungeon.Load(filepath.Join(plansDirT, "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	movement, err := readMovementPaths(filepath.Join("testdata", "map309", "movement.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	loop, ok := movement[49185]
	if !ok || len(loop) != 7 {
		t.Fatalf("movement.tsv: 49185 has %d waypoints (want 7)", len(loop))
	}
	leg := zgW35Leg(t, p, "high_priestess_jeklik")
	var g *dungeon.PullGroup
	for i := range leg.PullGroups {
		if lg := leg.PullGroups[i]; lg.Patrol != nil && strings.Contains(lg.Patrol.Source, "creature_movement 49185") {
			g = &leg.PullGroups[i]
			break
		}
	}
	if g == nil {
		t.Fatal("no leg-0 pull group carries the 14750 patroller 49185's loop")
	}
	// Walk 37: the row is formation 49185's own camp (six spawns), pulled after F6 (the walk-35 row was
	// the static pack's camp 20 yd east and owned nothing alive; the residue walk then crossed a bat lane).
	if len(g.Positions) != 6 {
		t.Fatalf("the 49185 row has %d positions, want its formation's 6", len(g.Positions))
	}
	if int64(g.Entries[0]) != 14750 {
		t.Fatalf("the 49185 row's first entry is %d, want the patrolling rider 14750", g.Entries[0])
	}
	if g.Patrol == nil {
		t.Fatal("leg-0 group 13 has no patrol block: the 14750 patroller 49185 walks its loop through the pull and the run pauses on the straggler walk")
	}
	if len(g.Patrol.Path) != len(loop) {
		t.Fatalf("group 13 patrol path has %d points (want 49185's %d)", len(g.Patrol.Path), len(loop))
	}
	for i, q := range loop {
		if d := math.Hypot(float64(g.Patrol.Path[i].X-q.X), float64(g.Patrol.Path[i].Y-q.Y)); d > 0.5 {
			t.Fatalf("group 13 patrol path[%d] is %.1f yd from 49185's waypoint (%.1f %.1f)", i, d, q.X, q.Y)
		}
	}
}

// TestZgW35ArlokkTerraceOwnsTheLowerPanther: walk 35 run-310 waited twice for the "patroller" 11365 49345
// (60 s each) then paused: the straggler walk to it crosses its packmate at 0.6 yd. 49345 stands on the z 60
// landing with its formation (formation 49343: four Panthers and the two Blood Drinkers 51594/51595). The
// 2026-11-05 re-route gives that formation its own row (its six spawns, radius 16, max_bodies 6), so the pull
// owns 49345 outright instead of relying on the z 71 terrace row's radius.
func TestZgW35ArlokkTerraceOwnsTheLowerPanther(t *testing.T) {
	p, err := dungeon.Load(filepath.Join(plansDirT, "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	leg := zgW35Leg(t, p, "high_priestess_arlokk")
	// The z 60 landing's formation: its positions include 49345's spawn.
	spawn := dungeon.Point{X: -11496, Y: -1619.55, Z: 60.4075}
	var g *dungeon.PullGroup
	for i := range leg.PullGroups {
		for _, q := range leg.PullGroups[i].Positions {
			if dist(q, spawn) < 0.5 {
				g = &leg.PullGroups[i]
				break
			}
		}
	}
	if g == nil {
		t.Fatalf("no leg-4 group holds 49345's spawn %.1f %.1f %.1f", spawn.X, spawn.Y, spawn.Z)
	}
	var cen dungeon.Point
	for _, q := range g.Positions {
		cen.X += q.X
		cen.Y += q.Y
		cen.Z += q.Z
	}
	n := float32(len(g.Positions))
	cen.X /= n
	cen.Y /= n
	cen.Z /= n
	d3 := math.Sqrt(float64((spawn.X-cen.X)*(spawn.X-cen.X) + (spawn.Y-cen.Y)*(spawn.Y-cen.Y) + (spawn.Z-cen.Z)*(spawn.Z-cen.Z)))
	if d3 > float64(g.Radius) {
		t.Errorf("the z 60 row's radius %g does not own 49345 (3D %.1f yd from the row point): the run waits 60 s for it, then pauses on the straggler walk", g.Radius, d3)
	}
	if len(g.Positions) != 6 || g.MaxBodies != 6 {
		t.Errorf("the z 60 row has %d positions and max_bodies %d, want the formation's 6 and 6", len(g.Positions), g.MaxBodies)
	}
}
