// Walk-35 water bounds (TortoiseBots-audit tests/raid-zg-walk35, item 1) and the
// Mar'li-stand bound (item 3).
//
// Run step N is export index N-1 (the run numbers from 1). The water strands sit
// on walk-back/ghost walks, not on plan rows: the stuck targets are live hop
// corners, 16+ yd from any row. No authored dry reroute exists, so these pin the
// current state instead of a fix (see TestZgW35WalkBackWaterBounds). The Mar'li
// stand likewise cannot move (see TestZgW35MarliStandKeeps41YdFromPack0).
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
