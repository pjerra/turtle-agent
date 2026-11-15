// Route batch B6a (TortoiseBots-audit tests/route-audit-1112, BATCHES.md "## B6a"): the static point fixes of
// crescent_grove, hateforge_quarry, black_morass and blackrock_depths. Each plan test pins one sample of the rows the
// batch moved, so a later edit that drops the fix fails here. The points are the audit's own floor and dry-mesh
// answers (AUDIT.md, per-plan defect tables), checked here without mmaps: the coordinates are plan data. The last
// test builds the deployed cut with the mmaps and lints it: no water, off-floor or boss_aggro finding may come back.
package main

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/plancut"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/planlint"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

// b6aLoad loads a plan file.
func b6aLoad(t *testing.T, name string) *dungeon.Plan {
	t.Helper()
	p, err := dungeon.Load("../../dungeons/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// b6aApproach returns a pull group's approach points (a zero-length slice when it has none).
func b6aApproach(p *dungeon.Plan, leg, group int) []dungeon.Point {
	return p.Legs[leg].PullGroups[group].Approach
}

func b6aDist(a, b dungeon.Point) float64 {
	return math.Sqrt(float64((a.X-b.X)*(a.X-b.X) + (a.Y-b.Y)*(a.Y-b.Y) + (a.Z-b.Z)*(a.Z-b.Z)))
}

func b6aNear(t *testing.T, what string, got dungeon.Point, x, y, z float32) {
	t.Helper()
	if got.X != x || got.Y != y || got.Z != z {
		t.Errorf("%s: %.2f %.2f %.2f, want %.2f %.2f %.2f", what, got.X, got.Y, got.Z, x, y, z)
	}
}

// The four leg-1 water pulls and the leg-2 approach that stood 1.9 yd off its floor take dry, on-floor stands, the
// leg-0 boss gets the three approach points its 199.9 yd hop needed, and the leg-4 boss its floored stand.
func TestCrescentGroveB6aStands(t *testing.T) {
	p := b6aLoad(t, "crescent_grove")
	// leg 1 pull groups 48, 72, 73, 86 (the packs stood in the pond and the hop into them was the swim line).
	want := map[int]dungeon.Point{
		48: {X: 236.14, Y: 128.81, Z: 228.92},
		72: {X: 152.222, Y: 51.41, Z: 228.646},
		73: {X: 153.59, Y: 120.74, Z: 228.495},
		86: {X: 303.05, Y: 144.37, Z: 229.524},
	}
	for group, pt := range want {
		a := b6aApproach(p, 1, group)
		if len(a) != 1 {
			t.Fatalf("leg 1 pull group %d: %d approach points, want 1", group, len(a))
		}
		b6aNear(t, "leg 1 group "+itoa(group), a[0], pt.X, pt.Y, pt.Z)
	}
	// leg 2 pull group 49's second approach stood 5.8 yd under its floor: snapped to the navmesh floor.
	a := b6aApproach(p, 2, 49)
	if len(a) != 3 {
		t.Fatalf("leg 2 group 49: %d approach points, want 3", len(a))
	}
	b6aNear(t, "leg 2 group 49 approach[1]", a[1], -285.1, -96.3, 272.8)
	// leg 0 boss: the 199.9 yd hop (over the move cap) walks the floor at 269.1, 268.7 and 276.2.
	b := p.Legs[0].Boss
	if b == nil || len(b.Approach) != 3 {
		t.Fatalf("leg 0 boss: %v approach points, want 3", b)
	}
	b6aNear(t, "leg 0 boss approach[0]", b.Approach[0], 389.12, -190.71, 269.1)
	b6aNear(t, "leg 0 boss approach[2]", b.Approach[2], 295.4, -156.0, 276.2)
	// leg 4 boss: its approach stood 3.3 yd short and 2.8 yd under the floor.
	b4 := p.Legs[4].Boss
	if b4 == nil || len(b4.Approach) != 1 {
		t.Fatalf("leg 4 boss: %v approach points, want 1", b4)
	}
	b6aNear(t, "leg 4 boss approach", b4.Approach[0], -672.84, -364.1, 351.9)
}

// The two off-floor approach corners snap to their floors and the leg-4 stand leaves Engineer Figgles' 14 yd script
// aggro; the long hops get the approach points that split them.
func TestHateforgeQuarryB6aStands(t *testing.T) {
	p := b6aLoad(t, "hateforge_quarry")
	a := b6aApproach(p, 0, 30)
	if len(a) != 2 {
		t.Fatalf("leg 0 group 30: %d approach points, want 2", len(a))
	}
	b6aNear(t, "leg 0 group 30 approach[1]", a[1], -8257.4, -3500.0, 174.7)
	a6 := b6aApproach(p, 2, 6)
	if len(a6) != 1 {
		t.Fatalf("leg 2 group 6: %d approach points, want 1", len(a6))
	}
	b6aNear(t, "leg 2 group 6 approach", a6[0], -8226.0, -3473.9, 140.9)
	a7 := b6aApproach(p, 2, 7)
	if len(a7) != 1 {
		t.Fatalf("leg 2 group 7: %d approach points, want 1", len(a7))
	}
	b6aNear(t, "leg 2 group 7 approach", a7[0], -8188.0, -3482.4, 121.5)
	// leg 4 group 19: the last stand is 21.9 yd from the boss's spawn, outside its 14 yd script aggro.
	a19 := b6aApproach(p, 4, 19)
	if len(a19) != 4 {
		t.Fatalf("leg 4 group 19: %d approach points, want 4", len(a19))
	}
	boss := dungeon.Point{X: -8119.55, Y: -3462.48, Z: 170.622}
	if d := b6aDist(a19[3], boss); d < 14 {
		t.Errorf("leg 4 group 19 stand is %.1f yd from boss 60736 (script aggro 14)", d)
	}
	// leg 4 group 14: the 149.8 yd hop is split on the floor of the bridge between the two corners.
	a14 := b6aApproach(p, 4, 14)
	if len(a14) != 1 {
		t.Fatalf("leg 4 group 14: %d approach points, want 1", len(a14))
	}
	b6aNear(t, "leg 4 group 14 approach", a14[0], -8286.33, -3374.8, 170.7)
}

// The two vias that walked into Chronar's and Epochronos' aggro move out of it, the four pond packs get dry stands, and
// the leg-2 walk after the boss splits on the bridge floor.
func TestBlackMorassB6aStands(t *testing.T) {
	p := b6aLoad(t, "black_morass")
	v := p.Legs[0].Via
	if len(v) < 20 {
		t.Fatalf("leg 0: %d via points", len(v))
	}
	b6aNear(t, "leg 0 via 19", v[19].Point, -1474.0, 6992.0, -231.0)
	b6aNear(t, "leg 0 via 49", v[49].Point, -1410.0, 6980.0, -14.2)
	b6aNear(t, "leg 0 via 50", v[50].Point, -1460.0, 7010.0, -13.5)
	want := map[int]dungeon.Point{
		11: {X: -1754.52, Y: 7055.57, Z: 18.0124},
		65: {X: -1940.09, Y: 7222.22, Z: 17.5342},
		66: {X: -1887.64, Y: 7239.88, Z: 17.3332},
		67: {X: -1871.96, Y: 7238.86, Z: 17.4776},
	}
	for group, pt := range want {
		a := b6aApproach(p, 0, group)
		if len(a) != 1 {
			t.Fatalf("leg 0 group %d: %d approach points, want 1", group, len(a))
		}
		b6aNear(t, "leg 0 group "+itoa(group), a[0], pt.X, pt.Y, pt.Z)
	}
	// The leg-2 walk after Medivh split on the bridge floor at 22.4.
	v2 := p.Legs[2].Via
	if len(v2) != 2 {
		t.Fatalf("leg 2: %d via points, want 2", len(v2))
	}
	b6aNear(t, "leg 2 via 0", v2[0].Point, -1941.33, 7082.67, 18.96)
	b6aNear(t, "leg 2 via 1", v2[1].Point, -1864.67, 7054.0, 22.4)
}

// The stand before High Interrogator Gerstahn's packs moves out of his 12 yd script aggro, and the three off-floor
// stands snap to their floors.
func TestBlackrockDepthsB6aStands(t *testing.T) {
	p := b6aLoad(t, "blackrock_depths")
	a29 := b6aApproach(p, 0, 29)
	if len(a29) != 1 {
		t.Fatalf("leg 0 group 29: %d approach points, want 1", len(a29))
	}
	b6aNear(t, "leg 0 group 29 approach", a29[0], 294.0, -150.0, -70.4)
	boss := dungeon.Point{X: 310.6, Y: -146.3, Z: -70.3}
	if d := b6aDist(a29[0], boss); d < 12 {
		t.Errorf("leg 0 group 29 stand is %.1f yd from boss 9018 (script aggro 12)", d)
	}
	a25 := b6aApproach(p, 1, 25)
	if len(a25) != 1 {
		t.Fatalf("leg 1 group 25: %d approach points, want 1", len(a25))
	}
	b6aNear(t, "leg 1 group 25 approach", a25[0], 614.5, -94.5, -45.7)
	a46 := b6aApproach(p, 8, 46)
	if len(a46) != 1 {
		t.Fatalf("leg 8 group 46: %d approach points, want 1", len(a46))
	}
	b6aNear(t, "leg 8 group 46 approach", a46[0], 1369.1, -429.9, -94.5)
	// leg 8 group 57: the 204.6 yd hop is split at the bridge floor.
	a57 := b6aApproach(p, 8, 57)
	if len(a57) != 3 {
		t.Fatalf("leg 8 group 57: %d approach points, want 3", len(a57))
	}
	b6aNear(t, "leg 8 group 57 approach[1]", a57[1], 1396.6, -452.4, -93.0)
}

// b6aMmaps is the server's navmesh when it holds every one of the plans' maps ($TURTLE_AGENT_MMAPS or
// ~/turtle-agent-mmaps, as the other dungeongen checks use); the test skips without it.
func b6aMmaps(t *testing.T, maps []uint32) *navmesh.Planner {
	t.Helper()
	home, _ := os.UserHomeDir()
	for _, dir := range []string{os.Getenv("TURTLE_AGENT_MMAPS"), filepath.Join(home, "turtle-agent-mmaps")} {
		if dir == "" {
			continue
		}
		have := true
		for _, m := range maps {
			n := fmt.Sprintf("%03d", m)
			if _, err := os.Stat(filepath.Join(dir, n+".mmap")); err != nil {
				have = false
				break
			}
			if tiles, _ := filepath.Glob(filepath.Join(dir, n+"*.mmtile")); len(tiles) == 0 {
				have = false
				break
			}
		}
		if !have {
			continue
		}
		pl := navmesh.NewPlanner(dir)
		t.Cleanup(pl.Close)
		return pl
	}
	skipMissingMmaps(t, fmt.Sprintf("no navmesh files for maps %v: set TURTLE_AGENT_MMAPS to a folder holding their .mmap and .mmtile files", maps))
	return nil
}

// The deployed cut of every B6a plan (plan-export --allow-draft --gm-cross-direct --floor-aware --approach-hops, this tree's
// cutter) carries no water, off-floor or boss_aggro finding: the three classes the batch's point fixes remove. These are the
// counts plan-check --floor-aware --approach-hops prints for the same tree; a plan edit or a cutter change that brings one back
// fails here, which the coordinate pins above cannot see.
func TestB6aDeployedCutLintCounts(t *testing.T) {
	var plans []*dungeon.Plan
	var maps []uint32
	for _, name := range []string{"crescent_grove", "hateforge_quarry", "black_morass", "blackrock_depths"} {
		p := b6aLoad(t, name)
		plans = append(plans, p)
		maps = append(maps, p.Map)
	}
	pl := b6aMmaps(t, maps)
	for _, p := range plans {
		name := p.Name
		world, _, err := worldspawn.Load(filepath.Join("testdata", fmt.Sprintf("map%d", p.Map)), p.Map)
		if err != nil {
			t.Fatalf("%s: the map %d world export: %v", name, p.Map, err)
		}
		res, err := planexport.Build(p, planexport.Options{Source: "plan-check", Cut: plancut.Cut(pl, true), ApproachHops: true,
			Floor: plancut.FloorAt(pl), GMCrossDirect: true})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		fs, err := planlint.Lint(res.Rows, pl, planlint.Options{Map: p.Map, Level: 60, World: world,
			Raid: p.IsRaid(), KillFirst: p.KillFirst, PullCeiling: p.PullCeiling})
		if errors.Is(err, navmesh.ErrNoEngine) {
			skipMissingMmaps(t, "built without cgo: no navmesh engine")
		}
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		counts := planlint.Counts(fs)
		for _, lint := range []string{planlint.LintWater, planlint.LintOffFloor, planlint.LintBossAggro} {
			if counts[lint] == 0 {
				continue
			}
			var lines []string
			for _, f := range fs {
				if f.Lint == lint {
					lines = append(lines, f.String())
				}
			}
			t.Errorf("%s: %d %s findings on the deployed cut, want 0:\n%s", name, counts[lint], lint, strings.Join(lines, "\n"))
		}
	}
}
