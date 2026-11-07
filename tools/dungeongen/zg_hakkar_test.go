// Walk-42 fix 1 (TortoiseBots-audit research/zg-bosses/HAKKAR-RESETS.md, spec hakkar-anchor-t1105e Part B): Hakkar's
// script evades him whenever he is below z 45.8 (boss_hakkar.cpp:140-145 of TortoiseBots), and the plan's boss approach
// stood the whole raid 34-50 yd south-west of him, down the slope that crosses that line: 100 of walk 41's 130 Hakkar
// ends and 15 of walk 42's 16 were his own reset. His fight stands on his platform now, 12.7 yd south of his spawn.
package main

import (
	"testing"

	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// TestZgHakkarBossApproachOnHisPlatform pins walk 42's fix 1. The approach is two plan points: the platform ramp's top
// lip (the exported path row the cut used to add there) and the fight stand. The rules:
//   - the stand is a fight stand, not a walk-by: 11-17 yd 2D from his spawn (-11787.4 -1649.34 54.1), and no lower
//     than z 53.0 (the platform; his z 45.8 evade line is under the slope the old stand stood on);
//   - the exported rows put those two points right before his boss row 14834;
//   - on the map-309 navmesh (skipped without the mmaps): the stand is on the mesh floor within 0.6 yd, the walk in
//     from the lip is a complete path, and the walkability ray from the stand to his spawn's z + 2 is clear (the
//     agent's line-of-sight stand-in: it cannot see the core's vmaps, so live sight is still the walk's to confirm).
func TestZgHakkarBossApproachOnHisPlatform(t *testing.T) {
	p, _ := zgLoadPlanAndExport(t)
	leg, _ := zgW37LegByName(t, p, "hakkar")
	if leg.Boss == nil || leg.Boss.Entry != 14834 {
		t.Fatalf("the hakkar leg's boss is %+v, want 14834", leg.Boss)
	}
	boss := leg.Boss.Position
	ap := leg.Boss.Approach
	if len(ap) != 2 {
		t.Fatalf("the boss approach has %d points, want 2 (the ramp lip and the stand): %v", len(ap), ap)
	}
	lip, stand := ap[0], ap[1]
	if d := dist(stand, boss); d < 11 || d > 17 {
		t.Errorf("the stand %.2f %.2f %.2f is %.1f yd from Hakkar %.1f %.1f %.1f, want 11-17 (inside his platform, outside melee)",
			stand.X, stand.Y, stand.Z, d, boss.X, boss.Y, boss.Z)
	}
	if stand.Z < 53.0 {
		t.Errorf("the stand's z %.2f is below the platform's 53.0: the old -11802.733 -1679.4271 52.943153 stood on the SW slope that runs to his z 45.8 evade line", stand.Z)
	}

	res, err := planexport.Build(p, planexport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	bossAt := -1
	for i, s := range res.Rows.Steps {
		if s.Kind == planexport.KindBoss && s.Entry == 14834 {
			bossAt = i
		}
	}
	if bossAt < 2 {
		t.Fatalf("no boss row 14834 with two rows before it in the %d exported steps", len(res.Rows.Steps))
	}
	if got := res.Rows.Steps[bossAt-1]; got.Kind != planexport.KindMove || got.Comment != "boss approach" || dist(rowPoint(got), stand) > 0.01 {
		t.Errorf("the row before the boss row is %s %q at %.2f %.2f %.2f, want the stand %.2f %.2f %.2f",
			got.Kind, got.Comment, got.X, got.Y, got.Z, stand.X, stand.Y, stand.Z)
	}
	if got := res.Rows.Steps[bossAt-2]; got.Kind != planexport.KindMove || got.Comment != "boss approach" || dist(rowPoint(got), lip) > 0.01 {
		t.Errorf("the second row before the boss row is %s %q at %.2f %.2f %.2f, want the lip %.2f %.2f %.2f",
			got.Kind, got.Comment, got.X, got.Y, got.Z, lip.X, lip.Y, lip.Z)
	}

	pl := zgPlannerOrNil(t)
	if pl == nil {
		t.Skip("needs the map-309 mmaps (the floor and the sight ray)")
	}
	defer pl.Close()
	const mapID = 309
	at := navmesh.Vec3{stand.X, stand.Y, stand.Z}
	floors, err := pl.Floors(mapID, stand.X, stand.Y, stand.Z-6, stand.Z+6)
	if err != nil {
		t.Fatal(err)
	}
	best := -1.0
	for _, f := range floors {
		if d := absf(float64(f.Z - stand.Z)); best < 0 || d < best {
			best = d
		}
	}
	if len(floors) == 0 || best > 0.6 {
		t.Errorf("no mesh floor within 0.6 yd of the stand's z %.2f (floors %v): the stand would float or sink", stand.Z, floors)
	}
	if path, err := pl.Plan(mapID, navmesh.Vec3{lip.X, lip.Y, lip.Z}, at); err != nil {
		t.Fatal(err)
	} else if path.Status != navmesh.Complete {
		t.Errorf("the walk from the lip %.2f %.2f %.2f to the stand is %s %s, want complete", lip.X, lip.Y, lip.Z, path.Status, path.Reason)
	}
	if path, err := pl.Plan(mapID, at, navmesh.Vec3{boss.X, boss.Y, boss.Z}); err != nil {
		t.Fatal(err)
	} else if path.Status != navmesh.Complete {
		t.Errorf("the walk from the stand to Hakkar is %s %s, want complete", path.Status, path.Reason)
	}
	ray, err := pl.Raycast(mapID, at, navmesh.Vec3{boss.X, boss.Y, boss.Z + 2})
	if err != nil {
		t.Fatal(err)
	}
	if !ray.Known || !ray.Clear {
		t.Errorf("the ray from the stand to Hakkar's z+2 is known=%v clear=%v (hit %.1f): no line of sight from the fight stand", ray.Known, ray.Clear, ray.Hit)
	}
}

func absf(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
