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
		if len(leg.PullGroups[i].Entries) == 2 &&
			leg.PullGroups[i].Entries[0] == 11368 && leg.PullGroups[i].Entries[1] == 14750 &&
			len(leg.PullGroups[i].Positions) == 1 &&
			math.Hypot(float64(leg.PullGroups[i].Positions[0].X+12228), float64(leg.PullGroups[i].Positions[0].Y+1478)) < 1 {
			g = &leg.PullGroups[i]
			break
		}
	}
	if g == nil {
		t.Fatal("no leg-0 group 13 (11368,14750 at -12228 -1478)")
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

// TestZgW35ArlokkTerraceOwnsTheLowerPanther: walk 35 run-310 (leg-4 group 12)
// waited twice for the "patroller" 11365 49345 (60 s each) then paused: the
// straggler walk to it crosses its packmate at 0.6 yd. 49345's spawn stands
// 12.7-13.6 yd (3D over the level gap) from the row point, outside the
// radius-12 pull. Radius 14 owns it (3D 12.7-13.6 < 14), so it fights with the pack.
//
// Radius 14 reaches entry-gated members of three formations: the four lower
// Panthers (formation 49343's 11365s), the four upper-B Shadow Hunters
// (formation 51353's 11339s) and the upper-east pack (formation 51369: four
// 11365s and two 11339s). The row's pull is 14 bodies, so max_bodies must be
// 14: 10 leaves four owned bodies over the run leader's plan ceiling.
func TestZgW35ArlokkTerraceOwnsTheLowerPanther(t *testing.T) {
	p, err := dungeon.Load(filepath.Join(plansDirT, "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	cs, err := readCreatures(filepath.Join("testdata", "map309", "creatures.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	leg := zgW35Leg(t, p, "high_priestess_arlokk")
	var g *dungeon.PullGroup
	for i := range leg.PullGroups {
		if len(leg.PullGroups[i].Positions) == 10 {
			g = &leg.PullGroups[i]
			break
		}
	}
	if g == nil {
		t.Fatal("no leg-4 group 12 (the 10-position terrace cluster)")
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
	// 49345's spawn below the terrace (the run's "spawned 12.7 yd from the centre").
	spawn := dungeon.Point{X: -11496, Y: -1619.55, Z: 60.4075}
	d3 := math.Sqrt(float64((spawn.X-cen.X)*(spawn.X-cen.X) + (spawn.Y-cen.Y)*(spawn.Y-cen.Y) + (spawn.Z-cen.Z)*(spawn.Z-cen.Z)))
	if d3 > float64(g.Radius) {
		t.Errorf("group 12 radius %g does not own 49345 (3D %.1f yd from the row point): the run waits 60 s for it, then pauses on the straggler walk", g.Radius, d3)
	}
	byLeader := map[uint64][]creature{}
	for _, c := range cs {
		if c.GroupLeader != 0 {
			byLeader[c.GroupLeader] = append(byLeader[c.GroupLeader], c)
		}
	}
	entries := map[uint32]bool{}
	for _, e := range g.Entries {
		entries[e] = true
	}
	rowDist := func(c creature) float64 {
		return dist(cen, c.Pos)
	}
	owned := 0
	for _, c := range cs {
		if !entries[c.Entry] {
			continue
		}
		fam := byLeader[c.GroupLeader]
		if len(fam) == 0 {
			fam = []creature{c}
		}
		for _, m := range fam {
			if rowDist(m) <= float64(g.Radius) {
				owned++
				break
			}
		}
	}
	// All four absorbed Panthers (formation 49343's 11365s) are owned.
	for _, guid := range []uint64{49343, 49344, 49345, 49346} {
		var c *creature
		for i := range cs {
			if cs[i].GUID == guid {
				c = &cs[i]
				break
			}
		}
		if c == nil {
			t.Fatalf("no spawn %d in the map-309 export", guid)
		}
		if !entries[c.Entry] || rowDist(*c) > float64(g.Radius) {
			// The formation rule owns through any member in reach: check the pack.
			hit := false
			for _, m := range byLeader[c.GroupLeader] {
				if entries[m.Entry] && rowDist(m) <= float64(g.Radius) {
					hit = true
					break
				}
			}
			if !hit {
				t.Errorf("group 12 (radius %g) does not own its Panther %d at %.1f %.1f %.1f: %.1f yd out", g.Radius, guid, c.Pos.X, c.Pos.Y, c.Pos.Z, rowDist(*c))
			}
		}
	}
	if owned != 14 {
		t.Errorf("group 12 owns %d bodies, want 14 (upper B 4 + upper east 6 + the four lower Panthers)", owned)
	}
	if g.MaxBodies != 14 {
		t.Errorf("group 12 max_bodies %d, want 14 (the row's owned pull size)", g.MaxBodies)
	}
}
