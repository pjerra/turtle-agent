// The Stratholme undead route's order: the walk pulls each pack on the boss-to-boss road it belongs to instead of running
// the draft's northern lanes twice (Sagiroth/TortoiseBots#1542, route-order audit 2026-11-12). The pins are the route
// itself (each leg's packs in the walk's order, the dropped packs, the leg anchors and the boss order), the navmesh walk
// between every two consecutive exported rows, and the backtracks that are left: every one of them leaves a boss's dead
// end. It fails on origin/dev's plan (4,964.6 yd walked, 12 backtracks, one of them with no boss in its loop).
package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// stPlan is the plan under test.
const stPlan = "../../dungeons/stratholme_undead.json"

// stDevWalkYd is origin/dev's plan measured the same way (the sum of the navmesh path between consecutive party rows of an
// uncut export, entrance -> first row included): 4,964.6 yd over 95 rows, because its order runs the northern lane twice.
const stDevWalkYd = 4964.6

// stMaxWalkYd caps the walk these rows take: 2,502.9 yd measured here against a 1,250 yd straight route, 6% of headroom for
// a cut point that moves. The deployed rows walk 5,126.5 yd (the fresh floor-aware export 5,134.4).
const stMaxWalkYd = 2660.0

// stBacktracks is how many runs of re-walked ground the route has left: one, 114.5 yd, the way back out of Anastari's
// ziggurat - the dead end the boss stands in (the loop runs from step 20 over her boss row at step 44). origin/dev's plan
// has 12 (1,381.3 yd re-walked), seven of them without a boss inside the loop.
const stBacktracks = 1

// stBossEntries is the boss order the walk keeps, leg by leg.
var stBossEntries = []uint32{10435, 10437, 10438, 10436, 10440, 10440}

// stAnchors is every leg's `to` point: the first point after the previous boss, each an existing row of the deployed plan.
var stAnchors = [][3]float64{
	{3636.06, -3636.82, 138.218},       // leg 0 magistrate_barthilas
	{3681.2666, -3611.2, 138.25056},    // leg 1 nerub'enkan
	{3882.6665, -3541.3333, 138.17378}, // leg 2 maleki_the_pallid
	{4028.0664, -3644, 135.19165},      // leg 3 baroness_anastari
	{3945.5186, -3645.2283, 137.74643}, // leg 4 baron_rivendare
	{4032.79, -3333.02, 115.061},       // leg 5 baron_rivendare_room
}

// stPacks is every leg's packs in the walk's order, each as its unit entries and the stand the rows pull from (the mean of
// its positions, 0.1 yd). A pack that moves legs or a reorder of a leg fails it.
var stPacks = [][]string{
	// leg 0 magistrate_barthilas
	{
		"10381,10391,10390,10391,10390,10382,10381 @3642.6,-3637.1",
	},
	// leg 1 nerub'enkan
	{
		"10381,10382,10382,10390,10391,10390,10391 @3688.1,-3598.3",
		"10391,10382,10390,10390,10390,10391,10381 @3710.5,-3610.4",
		"10407,10406,10406 @3807.7,-3650.7",
		"10413,10463 @3828.5,-3646.9",
		"10406,10406 @3842.1,-3634.6",
		"10463 @3836.7,-3619.6",
		"10413,10464 @3815.0,-3626.9",
		"10408 @3824.2,-3617.3",
		"10406,10407,10407 @3832.5,-3590.9",
		"10400,10398 @3853.6,-3612.3",
		"10463,10412 @3857.7,-3586.8",
		"10464,10413 @3872.4,-3566.0",
		"10412,10398,10398 @3854.7,-3557.1",
		"10399,10399,10399,10399,10399 @3838.9,-3498.3",
	},
	// leg 2 maleki_the_pallid
	{
		"10408,10400,10398,10413,10398,10400 @3877.7,-3533.1",
		"10398,10398,10400 @3954.9,-3565.9",
		"10407,10407,10406 @3970.6,-3546.6",
		"10464 @3972.0,-3567.6",
		"10405,10405 @3972.2,-3587.6",
		"10463 @4001.5,-3630.3",
		"10400,10400,10398,10398 @4022.6,-3617.6",
		"10399,10399,10399,10399,10399 @4059.2,-3668.1",
	},
	// leg 3 baroness_anastari
	{
		"10400,10398,10398 @4001.6,-3640.7",
		"10463,10406,10405 @3974.2,-3645.9",
		"10408,10406,10407,10407 @3949.8,-3641.4",
		"10400,10405,10400 @3898.5,-3665.4",
		"10408,10406,10406,10407,10412 @3893.2,-3682.2",
		"10400,10398,10400 @3887.8,-3709.8",
		"10463 @3875.7,-3701.4",
		"10464,10412 @3867.8,-3683.0",
		"10463,10413,10406,10405 @3846.5,-3666.6",
		"10406,10405 @3822.4,-3683.7",
		"10463,10400,10400,10398 @3835.8,-3695.7",
		"10399,10399,10399,10399,10399 @3847.4,-3750.1",
	},
	// leg 4 baron_rivendare
	{
		"10412,10407,10405 @3980.4,-3623.1",
		"10412,10413,10464 @3991.8,-3593.2",
		"10405,10406,10406 @4008.6,-3594.1",
		"10463,10412 @3996.2,-3561.1",
		"10408,10400,10400 @3988.9,-3541.9",
		"10406,10406,10405 @4022.4,-3558.4",
		"10464 @4014.2,-3540.6",
		"10405,10407,10405,10405 @4017.7,-3520.4",
		"10408 @4048.2,-3520.0",
		"10409,10408,10809 @4058.9,-3530.3",
		"10411 @4067.6,-3534.4",
		"10405,10406 @4056.4,-3516.3",
		"10417 @4089.6,-3431.3",
		"10416 @4098.0,-3398.0",
		"10417 @4084.0,-3379.8",
		"10417 @4072.0,-3401.0",
		"10416 @4054.0,-3413.3",
		"10417 @4052.9,-3432.7",
		"10417 @4040.2,-3443.8",
		"10417 @4022.8,-3424.4",
		"10416 @4009.2,-3434.7",
		"10417 @3995.3,-3425.2",
		"10417 @3998.9,-3403.3",
		"10416 @3990.8,-3381.9",
		"10417 @4012.4,-3401.5",
	},
	// leg 5 baron_rivendare_room
	{},
}

// stDropped is every pack the route leaves out, by the deployed plan's leg and pull group, its units and its stand. None of
// them may be a pull group of the plan: the walk does not pass them.
var stDropped = []string{
	"10381,10381,10391,10391,10381,10390,10390,10414 @3631.9,-3519.1",       // leg 0 pull group 3
	"10405,10405 @3602.8,-3513.5",                                           // leg 0 pull group 4
	"10391,10390,10382 @3629.1,-3494.8",                                     // leg 0 pull group 5
	"10405,10405,10405 @3644.1,-3474.2",                                     // leg 0 pull group 6
	"10381,10382,10391,10405,10405,10390 @3613.4,-3491.0",                   // leg 0 pull group 7
	"10390,10382,10390,10381,10391,10391,10390,10393,10411 @3589.9,-3473.1", // leg 0 pull group 8
	"10390,10391,10381,10391,10382,10405,10405,10390,10382 @3542.7,-3420.9", // leg 0 pull group 9
	"10390,10391,10390,10382,10381,10382,10391 @3549.5,-3378.7",             // leg 0 pull group 10
	"10381,10382,10391,10391,10381,10390,10390 @3561.1,-3317.7",             // leg 0 pull group 11
	"10390,10390,10382,10382,10382,10391,10391 @3573.8,-3345.8",             // leg 0 pull group 12
	"10381,10391,10391,10381,10390,10390,10381 @3658.4,-3500.7",             // leg 0 pull group 13
	"10391,10381,10391,10382,10390,10382,10390 @3690.9,-3488.9",             // leg 0 pull group 14
	"10405 @3720.4,-3467.6",                                                 // leg 1 pull group 0
	"10405,10405 @3729.4,-3455.7",                                           // leg 1 pull group 1
	"10405 @3715.4,-3438.7",                                                 // leg 1 pull group 2
	"10405,10390,10391,10381,10391,10381,10390,10381 @3694.2,-3393.4",       // leg 1 pull group 3
	"10405,10405 @3690.0,-3412.1",                                           // leg 1 pull group 4
	"10382,10390,10381,10382,10391,10390,10391 @3676.0,-3357.7",             // leg 1 pull group 5
	"10390,10391,10382,10391,10390,10382,10382 @3663.0,-3324.8",             // leg 1 pull group 6
	"10390,10390,10381,10391,10405,10381,10391,10382 @3682.1,-3293.9",       // leg 1 pull group 7
	"10405,10405 @3672.1,-3264.6",                                           // leg 1 pull group 8
	"10414 @3715.0,-3257.7",                                                 // leg 1 pull group 9
	"10382,10390,10391,10391,10381,10382,10390 @3732.0,-3262.4",             // leg 1 pull group 10
}

// stPlannerOrNil is the map-329 navmesh when the machine has the server's mmaps, nil otherwise.
func stPlannerOrNil(t *testing.T) *navmesh.Planner {
	t.Helper()
	home, _ := os.UserHomeDir()
	for _, dir := range []string{os.Getenv("TURTLE_AGENT_MMAPS"), filepath.Join(home, "turtle-agent-mmaps")} {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "329.mmap")); err != nil {
			continue
		}
		if tiles, _ := filepath.Glob(filepath.Join(dir, "329*.mmtile")); len(tiles) == 0 {
			continue
		}
		return navmesh.NewPlanner(dir)
	}
	return nil
}

// stPackID is a pull group written the way stPacks and stDropped pin it.
func stPackID(g dungeon.PullGroup) string {
	var n int
	var sum [3]float64
	for _, p := range g.Positions {
		sum[0] += float64(p.X)
		sum[1] += float64(p.Y)
		sum[2] += float64(p.Z)
		n++
	}
	ents := make([]string, len(g.Entries))
	for i, e := range g.Entries {
		ents[i] = fmt.Sprint(e)
	}
	if n == 0 {
		return strings.Join(ents, ",")
	}
	return fmt.Sprintf("%s @%.1f,%.1f", strings.Join(ents, ","), sum[0]/float64(n), sum[1]/float64(n))
}

// stPartyRows are the rows that put the party somewhere: an interaction row (open, loot) is not a waypoint.
func stPartyRows(kind string) bool {
	switch kind {
	case planexport.KindMove, planexport.KindMoveDirect, planexport.KindMoveHopExact, planexport.KindPull, planexport.KindBoss, planexport.KindHold:
		return true
	}
	return false
}

// TestStratholmeUndeadRouteOrder pins the route and the walk the exported rows take.
func TestStratholmeUndeadRouteOrder(t *testing.T) {
	p, err := dungeon.Load(stPlan)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Legs) != len(stBossEntries) {
		t.Fatalf("%d legs, want %d (one per boss, in boss order)", len(p.Legs), len(stBossEntries))
	}
	for i, leg := range p.Legs {
		if leg.Boss == nil {
			t.Errorf("leg %d (%s) has no boss: the boss order is part of the route", i, leg.Name)
			continue
		}
		if leg.Boss.Entry != stBossEntries[i] {
			t.Errorf("leg %d (%s) boss %d, want %d: the boss order is part of the route", i, leg.Name, leg.Boss.Entry, stBossEntries[i])
		}
		got := []string{}
		for _, g := range leg.PullGroups {
			got = append(got, stPackID(g))
		}
		if i < len(stPacks) && !slices.Equal(got, stPacks[i]) {
			t.Errorf("leg %d (%s) pulls\n  %s\nwant\n  %s", i, leg.Name, strings.Join(got, "\n  "), strings.Join(stPacks[i], "\n  "))
		}
		want := stAnchors[i]
		if math.Abs(float64(leg.To.X)-want[0]) > 0.01 || math.Abs(float64(leg.To.Y)-want[1]) > 0.01 || math.Abs(float64(leg.To.Z)-want[2]) > 0.01 {
			t.Errorf("leg %d (%s) walks to %.4f %.4f %.4f first, want %.4f %.4f %.4f: a `to` deep inside the leg walks past its packs and back",
				i, leg.Name, leg.To.X, leg.To.Y, leg.To.Z, want[0], want[1], want[2])
		}
		if len(leg.Via) != 0 {
			t.Errorf("leg %d (%s) carries %d via points, want none: every leg starts on the walk", i, leg.Name, len(leg.Via))
		}
	}
	for _, drop := range stDropped {
		for i, leg := range p.Legs {
			for _, g := range leg.PullGroups {
				if stPackID(g) == drop {
					t.Errorf("leg %d (%s) still pulls %s: the route does not pass it", i, leg.Name, drop)
				}
			}
		}
	}

	pl := stPlannerOrNil(t)
	if pl == nil {
		t.Skip("no map 329 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 329.mmap and its tiles")
	}
	defer pl.Close()

	res, err := planexport.Build(p, planexport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var stops []dungeon.Point
	var bossIdx []int
	for _, s := range res.Rows.Steps {
		if !stPartyRows(s.Kind) {
			continue
		}
		stops = append(stops, dungeon.Point{X: s.X, Y: s.Y, Z: s.Z})
		if s.Kind == planexport.KindBoss {
			bossIdx = append(bossIdx, len(stops)-1)
		}
	}
	if len(stops) < 2 {
		t.Fatalf("%d party rows, want the walk", len(stops))
	}
	total := stPath(t, pl, p.Map, p.Entrance, stops[0], "entrance")
	for i := 1; i < len(stops); i++ {
		total += stPath(t, pl, p.Map, stops[i-1], stops[i], fmt.Sprintf("row %d", i+1))
	}
	t.Logf("stratholme_undead walk %.1f yd (dev %.1f, straight 1250, %d rows)", total, stDevWalkYd, len(stops))
	if total > stMaxWalkYd {
		t.Errorf("the walk is %.1f yd (dev %.1f, cap %.0f): the order runs a lane twice again, put each pack in the leg whose walk passes it",
			total, stDevWalkYd, stMaxWalkYd)
	}

	bt := stBacktracksAlong(t, pl, p.Map, p.Entrance, stops, bossIdx)
	t.Logf("%d backtracks: %v", len(bt), bt)
	if len(bt) != stBacktracks {
		t.Errorf("%d backtracks (%v), want %d: the only re-walked ground is the way out of a boss's dead end", len(bt), bt, stBacktracks)
	}
	for _, b := range bt {
		if !b.boss {
			t.Errorf("backtrack at step %d (%.0f yd re-walked, loop %.0f yd): no boss row inside the loop; the walk crosses its own road outside a boss's dead end",
				b.step, b.yards, b.loop)
		}
	}
}

// stPath is the navmesh path a->b; a missing or partial path fails the test, it is not zero.
func stPath(t *testing.T, pl *navmesh.Planner, mapID uint32, a, b dungeon.Point, what string) float64 {
	t.Helper()
	path, err := pl.Plan(mapID, navmesh.Vec3{a.X, a.Y, a.Z}, navmesh.Vec3{b.X, b.Y, b.Z})
	if err != nil || path.Status != navmesh.Complete {
		t.Errorf("%s: no complete navmesh path %.1f %.1f %.1f -> %.1f %.1f %.1f: %v (%s %s)",
			what, a.X, a.Y, a.Z, b.X, b.Y, b.Z, err, path.Status, path.Reason)
		return 0
	}
	return float64(path.Length)
}

// stBacktrack is one run of re-walked ground: the route samples within 10 yd (5 yd in height) of a sample walked at least 60
// yd earlier, a run of them at least 40 yd long.
type stBacktrack struct {
	step  int     // the row the run starts at
	yards float64 // route yards the run covers
	loop  float64 // route yards since the walk was last at that spot
	boss  bool    // a boss row lies inside the loop
}

// stBacktracksAlong samples the walk every 4 yd and returns its backtracks.
func stBacktracksAlong(t *testing.T, pl *navmesh.Planner, mapID uint32, entrance dungeon.Point, stops []dungeon.Point, bossIdx []int) []stBacktrack {
	t.Helper()
	type sample struct {
		x, y, z, cum float64
		step         int
	}
	var s []sample
	var bosses []float64
	cum := 0.0
	prev := entrance
	for i, to := range stops {
		path, err := pl.Plan(mapID, navmesh.Vec3{prev.X, prev.Y, prev.Z}, navmesh.Vec3{to.X, to.Y, to.Z})
		if err != nil || path.Status != navmesh.Complete {
			s = append(s, sample{x: float64(to.X), y: float64(to.Y), z: float64(to.Z), cum: cum, step: i + 1})
			prev = to
			continue
		}
		var last navmesh.Vec3
		first := true
		emit := func(v navmesh.Vec3) {
			if !first {
				cum += math.Sqrt(math.Pow(float64(v[0]-last[0]), 2) + math.Pow(float64(v[1]-last[1]), 2) + math.Pow(float64(v[2]-last[2]), 2))
				s = append(s, sample{x: float64(v[0]), y: float64(v[1]), z: float64(v[2]), cum: cum, step: i + 1})
			}
			last, first = v, false
		}
		for k := range path.Points {
			emit(path.Points[k])
			if k < len(path.Ground) {
				for _, g := range path.Ground[k] {
					emit(g)
				}
			}
		}
		for _, b := range bossIdx {
			if b == i {
				bosses = append(bosses, cum)
			}
		}
		prev = to
	}
	// A revisit is a sample within 10 yd (5 yd in height) of one walked at least 60 yd earlier.
	const gap, reach, dz, minRun = 60.0, 10.0, 5.0, 40.0
	rev := make([]bool, len(s))
	loopFrom := make([]float64, len(s))
	grid := map[[2]int64][]int{}
	key := func(x, y float64) [2]int64 {
		return [2]int64{int64(math.Floor(x / reach)), int64(math.Floor(y / reach))}
	}
	for i := range s {
		k := key(s[i].x, s[i].y)
		best, found := 0.0, false
		for dx := int64(-1); dx <= 1; dx++ {
			for dy := int64(-1); dy <= 1; dy++ {
				for _, j := range grid[[2]int64{k[0] + dx, k[1] + dy}] {
					if s[j].cum > s[i].cum-gap {
						continue
					}
					if math.Hypot(s[j].x-s[i].x, s[j].y-s[i].y) <= reach && math.Abs(s[j].z-s[i].z) <= dz {
						if !found || s[j].cum > best {
							best, found = s[j].cum, true
						}
					}
				}
			}
		}
		if found {
			rev[i], loopFrom[i] = true, best
		}
		grid[k] = append(grid[k], i)
	}
	var out []stBacktrack
	for i := 0; i < len(s); {
		if !rev[i] {
			i++
			continue
		}
		j, since := i, loopFrom[i]
		for j+1 < len(s) && rev[j+1] && s[j+1].cum-s[j].cum <= 15 {
			j++
			if loopFrom[j+1] < since {
				since = loopFrom[j+1]
			}
		}
		if yards := s[j].cum - s[i].cum; yards >= minRun {
			boss := false
			for _, b := range bosses {
				if b > since && b <= s[j].cum {
					boss = true
					break
				}
			}
			out = append(out, stBacktrack{step: s[i].step, yards: yards, loop: s[i].cum - since, boss: boss})
		}
		i = j + 1
	}
	return out
}
