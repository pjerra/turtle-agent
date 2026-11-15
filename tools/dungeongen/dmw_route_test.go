// The Dire Maul West route's order (issue #1542, route-order audit 2026-11-12): the walk visits each pack in the leg
// whose boss-to-boss road passes it instead of clearing "the clusters nearest each boss", and it drops the packs that
// road leaves out. The pins are the plan's own data (boss order, each leg's anchor and packs, the dropped set) and the
// navmesh walk the exported rows take, so a later edit that moves a pack back, adds a detour or drops one is a failure
// and not just a print.
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

// dmwPlan is the plan under test.
const dmwPlan = "../../dungeons/dire_maul_west.json"

// dmwMaxWalkYd caps the re-ordered walk: 5,940.4 yd measured here between consecutive exported rows (6,005.4 with the
// entrance -> first row hop) against the 3,871.6 yd straight route through the plan's required stops (the five crystals
// and the six bosses). origin/dev's plan measures 15,677.3 yd here. 5% of headroom for a cut point that moves.
const dmwMaxWalkYd = 6240.0

// dmwBacktracks is the route-order audit's backtrack metric (tests/route-audit-1112/order/ORDER.md): the walk is
// sampled every 4 yd, a sample is a revisit within 10 yd (5 yd in height) of a sample walked at least 60 yd of route
// earlier, and a run of revisits at least 40 yd long (gaps of at most 15 yd joined) is a backtrack. The audit measured
// 29 backtracks / 7,849 yd on the deployed rows; origin/dev's plan measures 31 / 9,111.5 here. dmwBacktracks logs each
// one, and the test checks that every one turns round after a required stop (a boss row or a crystal hold row) or is one
// of the two dmwPackDetours: the only re-walked ground the order leaves is a dead end it had to enter.
const dmwBacktrackPins = 8

// dmwMaxBacktrackYd caps the re-walked distance the 8 backtracks cost: 1,181.7 yd measured here.
const dmwMaxBacktrackYd = 1250.0

// dmwDeployedPacks is the deployed plan's group count: the legs' pulls below plus the dropped ones.
const dmwDeployedPacks = 93

// dmwBossOrder is the plan's boss order, unchanged by the re-order: Tendris Warpwood, Revanchion, Illyanna Ravenoak,
// Magister Kalendris, Immol'thar, Prince Tortheldrin.
var dmwBossOrder = []uint32{11489, 14690, 11488, 11487, 11496, 11486}

// dmwLegAnchors pins every leg's `to` (the first point its walk takes after the previous stop) and its vias, in leg
// order: tendris_warpwood, revanchion, illyanna_ravenoak, magister_kalendris, immol'thar, prince_tortheldrin. Five `to`s
// moved in the re-order and leg 4 keeps its first 20 vias; every point is a row, via or approach point of the deployed
// export and the report says which.
var dmwLegAnchors = []struct {
	to   string
	vias []string
}{
	{"-91.25,208.19,-4.99", []string{}},
	{"21.60,420.00,-4.40", []string{}},
	{"91.00,522.67,-4.22", []string{}},
	{"8.96,538.05,28.74", []string{}},
	{"46.13,500.53,-23.22", []string{"73.70,539.64,-3.61", "88.00,522.67,-3.52", "95.93,473.30,-4.20", "97.33,464.53,-4.32", "97.87,460.53,-4.32", "85.33,448.00,-3.52", "77.87,433.33,-3.52", "77.60,432.80,-3.52", "70.13,414.67,-3.52", "69.60,414.13,-3.52", "40.53,385.87,-3.52", "37.07,384.00,-3.52", "36.27,382.40,-3.52", "34.40,376.00,-3.52", "25.07,376.00,-3.52", "22.40,379.20,-3.52", "22.40,412.27,-3.52", "24.80,450.93,-22.12", "25.07,453.33,-23.32", "42.67,490.67,-23.22"}},
	{"41.52,765.23,-23.12", []string{}},
}

// dmwLegPacks is every retained pack per leg, in plan order: its entries (with their multiplicity, in the plan's order)
// and every position, two decimals. A pack that moves to another leg, changes its entries or moves 0.01 yd fails here.
// A hold group rides with the guard pull before it.
var dmwLegPacks = [][]string{
	{ // leg 0 tendris_warpwood
		"11459@-91.25,208.19,-4.99",
		"11459@-83.62,221.46,-4.99",
		"14303,11458,14303,14303@-3.94,188.81,-3.30 -13.17,196.21,-3.48 -21.57,191.87,-3.30 -12.65,188.14,-3.30",
		"11458,14303,11458@-28.50,260.29,-9.12 -23.32,267.78,-8.55 -18.02,262.64,-8.55",
		"11458,14303,14303@-27.35,291.05,-7.23 -17.73,295.44,-8.41 -18.36,283.64,-8.43",
		"11483,11483,11483,11483@28.10,277.30,-8.03 6.10,277.30,-8.63 17.10,266.30,-6.50 17.10,288.30,-8.12",
		"11483@16.79,277.15,-8.47",
		"14303,11458,11458@50.27,294.15,-8.10 55.06,284.92,-7.68 46.69,284.90,-8.20",
		"14303,14303,11458@78.93,264.66,-8.28 70.43,265.37,-8.16 77.97,271.79,-8.07",
		"11462@86.29,300.30,-8.09",
		"11475,11472,11471,11472@95.68,472.55,-4.40 104.24,462.89,-4.40 115.82,465.84,-4.40 103.35,475.39,-4.40",
		"11469,14398,11470@121.78,484.35,28.60 116.50,487.87,28.60 121.67,490.02,28.60",
		"11477,11469,11470,11476,14398@96.40,483.05,28.60 92.39,489.25,28.60 92.53,484.79,28.60 96.26,491.80,28.60 96.08,487.04,28.60",
		"11473@105.90,400.45,28.60",
		"11476,11477,11477,11476,11477@106.26,392.55,28.60 108.51,397.56,28.60 111.51,392.62,28.60 114.50,397.90,28.60 114.63,387.43,28.60",
		"11476,11476,11477,11477,11476@114.86,394.74,28.60 117.44,392.47,28.60 120.56,386.87,28.60 120.75,397.85,28.60 123.16,392.23,28.60",
		"11473@119.79,407.49,28.60",
		"11483,11483,11483,11483@103.70,428.02,28.60 119.30,428.02,28.60 119.30,443.58,28.60 103.70,443.58,28.60",
		"11483@111.85,436.08,28.60",
		"11473@105.61,531.06,28.60",
		"11471,11475,11472@123.91,445.73,-4.39 118.56,435.10,-4.39 130.98,433.28,-4.38",
		"11476,11476,11470,14398,11469@43.49,505.77,-3.57 49.85,505.73,-3.57 47.47,510.75,-3.57 46.42,505.89,-3.57 45.56,510.94,-3.57",
	},
	{ // leg 1 revanchion
		"11471,11472,11472@101.06,499.01,-4.40 95.59,509.64,-4.40 108.10,514.16,-4.40",
		"11472,11472,11471,11475@71.82,549.05,-4.39 60.18,548.88,-4.39 68.26,562.32,-4.39 66.92,538.41,-4.39",
	},
	{ // leg 2 illyanna_ravenoak
		"11469,14398,11470@58.05,527.64,28.60 61.37,531.77,28.60 64.44,527.80,28.60",
		"11477,11470,11470,11476,11469@-15.35,529.90,28.60 -22.16,525.38,28.60 -17.62,525.30,28.60 -25.20,529.97,28.60 -19.83,529.81,28.60",
		"11476,11477,11470,14398,11469@-42.04,560.30,28.61 -52.35,560.21,28.61 -49.70,565.46,28.61 -47.15,560.33,28.61 -44.75,564.93,28.61",
		"11469,11470,11470@-61.69,530.37,28.60 -62.10,523.11,28.60 -67.69,527.42,28.60",
		"11470,11477,11469,14398,11476@-64.55,478.15,28.60 -69.01,472.46,28.60 -69.20,476.34,28.60 -64.72,474.00,28.60 -68.93,480.43,28.60",
		"11483,11483,11483,11483@-85.30,456.00,28.60 -96.30,445.00,28.60 -74.30,445.00,28.60 -85.30,434.00,28.60",
		"11483@-85.19,444.67,28.60",
		"11469,11470,11476,11477,11470@-109.18,528.54,28.61 -104.87,530.52,28.61 -104.81,535.23,28.61 -105.14,526.12,28.61 -109.12,532.84,28.61",
		"11473@-88.40,556.50,28.60",
		"11473@-76.11,536.42,28.60",
	},
	{ // leg 3 magister_kalendris
		"11469,11470,14398@7.69,565.72,28.60 -0.18,565.59,28.61 3.59,561.34,28.60",
		"11473@36.12,556.75,28.61",
		"11477,11476,11469,14398,11470@74.03,564.90,28.61 82.99,565.71,28.61 80.33,569.69,28.61 78.79,565.41,28.61 76.43,569.72,28.61",
		"11470,11476,11469,11477,11470@98.66,569.96,28.61 106.26,569.16,28.61 102.72,566.89,28.61 99.14,564.29,28.61 102.26,572.03,28.61",
		"11473@116.42,531.90,-4.40",
		"11475@136.17,553.80,-4.39",
		"11472@137.05,570.23,-4.39",
		"11467,11471@128.64,561.76,-4.31 122.46,561.71,-4.40",
		"11473@115.77,567.99,-4.40",
		"11472,11471,11472@94.53,558.99,-4.40 87.54,561.63,-4.40 96.34,546.58,-4.40",
		"11472,11471,11475@43.81,564.20,-4.39 30.67,560.20,-4.39 43.43,550.97,-4.39",
	},
	{ // leg 4 immol'thar
		"11473@53.42,508.31,-23.30",
		"11475,11475,11471@33.09,556.94,-23.30 30.41,549.86,-23.30 24.32,555.27,-23.30",
		"11471,11475,11475@-14.13,556.35,-23.30 -23.64,555.54,-23.30 -19.07,561.49,-23.30",
		"11473@-44.40,601.34,-23.30",
		"14400,14399,14400,14400@105.19,724.41,-24.58 105.19,724.41,-24.58 105.19,724.41,-24.58 105.19,724.41,-24.58",
		"14400,14399,14400,14400@-38.51,631.68,-24.68 -38.51,631.68,-24.68 -38.51,631.68,-24.68 -38.51,631.68,-24.68",
		"11484@-17.64,697.86,-24.58",
		"11484@-17.64,697.86,-24.58",
		"11484@-17.64,697.86,-24.58",
		"11484@-17.64,697.86,-24.58",
		"11484@-17.64,697.86,-24.58",
		"11484@-17.64,697.86,-24.58",
		"11483,11483,11483,11483@-137.98,705.02,-24.58 -122.42,720.58,-24.58 -137.98,720.58,-24.58 -122.42,705.02,-24.58",
		"11483@-130.41,712.95,-24.58",
		"14400,14399,14400,14400@-186.57,741.16,-24.58 -186.57,741.16,-24.58 -186.57,741.16,-24.58 -186.57,741.16,-24.58",
		"14400,14399,14400,14400@-136.11,928.03,-24.58 -136.11,928.03,-24.58 -136.11,928.03,-24.58 -136.11,928.03,-24.58",
		"14400,14399,14400,14400@57.20,929.95,-24.58 57.20,929.95,-24.58 57.20,929.95,-24.58 57.20,929.95,-24.58",
		"11483,11483,11483,11483@62.92,882.38,-24.58 62.92,866.82,-24.58 78.48,866.82,-24.58 78.48,882.38,-24.58",
		"11483@71.10,874.45,-24.58",
		"11484@46.99,881.84,-24.58",
		"11484@46.99,881.84,-24.58",
		"11484@46.99,881.84,-24.58",
		"11484@46.99,881.84,-24.58",
	},
	{ // leg 5 prince_tortheldrin
	},
}

// dmwDropped is the exact set of packs the re-order drops: its own entry and positions. Each was in the deployed plan;
// none is woken by the re-ordered rows (trash-check: no unplanned body pulled).
var dmwDropped = []struct {
	where string
	pack  string
}{
	{"deployed leg 0 tendris_warpwood pull group 10", "11459@-81.84,331.39,-4.99"},
	{"deployed leg 0 tendris_warpwood pull group 11", "11459@-92.07,344.01,-4.99"},
	{"deployed leg 0 tendris_warpwood pull group 12", "11458,11458,14303@-86.28,365.48,-3.48 -94.35,355.74,-3.60 -93.78,364.88,-3.48"},
	{"deployed leg 0 tendris_warpwood pull group 13", "11475,11472,11471,11473,11473@-97.43,467.99,-4.40 -99.75,478.88,-4.40 -89.14,476.42,-4.40 -70.35,462.70,-4.40 -103.25,463.17,-4.40"},
	{"deployed leg 0 tendris_warpwood pull group 14", "11471,11472,11475,11473@-101.06,516.33,-4.40 -87.37,518.15,-4.40 -76.29,519.17,-4.40 -70.30,532.08,-4.40"},
	{"deployed leg 0 tendris_warpwood pull group 15", "11472,11472,11471,11475@-61.21,549.37,-4.40 -46.12,549.57,-4.40 -54.81,538.25,-4.40 -54.22,561.82,-4.40"},
	{"deployed leg 0 tendris_warpwood pull group 16", "11469,11470,11476,11476,14398@-30.04,510.33,-3.57 -26.23,510.59,-3.57 -32.32,505.08,-3.57 -23.16,505.80,-3.57 -27.39,505.58,-3.57"},
	{"deployed leg 0 tendris_warpwood pull group 17", "11476,11476,11469,14398,11470@10.14,502.46,-3.57 2.20,502.47,-3.57 4.54,508.11,-3.57 6.40,503.15,-3.57 7.72,507.91,-3.57"},
	{"deployed leg 0 tendris_warpwood pull group 30", "14303,11458,14303@122.69,349.51,-4.99 123.56,357.67,-3.48 115.00,351.80,-4.99"},
	{"deployed leg 0 tendris_warpwood pull group 31", "11459@117.02,341.16,-4.99"},
	{"deployed leg 0 tendris_warpwood pull group 32", "11459@111.65,331.10,-4.99"},
	{"deployed leg 0 tendris_warpwood pull group 33", "11459@113.19,220.72,-4.99"},
	{"deployed leg 0 tendris_warpwood pull group 34", "11459@120.35,208.97,-4.99"},
	{"deployed leg 0 tendris_warpwood pull group 35", "11458,11458,14303@112.43,205.31,-4.99 120.71,206.37,-4.90 120.28,202.46,-4.99"},
	{"deployed leg 2 illyanna_ravenoak pull group 0", "11469,11476,14398,11470,11476@-30.04,510.33,-3.57 -23.16,505.80,-3.57 -27.39,505.58,-3.57 -26.23,510.59,-3.57 -32.32,505.08,-3.57"},
	{"deployed leg 2 illyanna_ravenoak pull group 1", "11471,11472,11472@-14.02,561.80,-4.40 -13.33,548.14,-4.39 -13.39,539.40,-4.40"},
	{"deployed leg 2 illyanna_ravenoak pull group 3", "11472,11475,11471,11472@-61.21,549.37,-4.40 -54.22,561.82,-4.40 -54.81,538.25,-4.40 -46.12,549.57,-4.40"},
	{"deployed leg 2 illyanna_ravenoak pull group 4", "11473@-70.30,532.08,-4.40"},
	{"deployed leg 2 illyanna_ravenoak pull group 5", "11471,11472,11475@-101.06,516.33,-4.40 -87.37,518.15,-4.40 -76.29,519.17,-4.40"},
	{"deployed leg 2 illyanna_ravenoak pull group 6", "11475,11472,11471@-97.43,467.99,-4.40 -99.75,478.88,-4.40 -89.14,476.42,-4.40"},
	{"deployed leg 2 illyanna_ravenoak pull group 14", "11473@-101.39,570.07,-4.39"},
	{"deployed leg 3 magister_kalendris pull group 2", "11472,11472,11471,11475@71.82,549.05,-4.39 60.18,548.88,-4.39 68.26,562.32,-4.39 66.92,538.41,-4.39"},
	{"deployed leg 3 magister_kalendris pull group 6", "11471,11472,11472@101.06,499.01,-4.40 95.59,509.64,-4.40 108.10,514.16,-4.40"},
	{"deployed leg 3 magister_kalendris pull group 11", "14400,14399,14400,14400@-38.51,631.68,-24.68 -38.51,631.68,-24.68 -38.51,631.68,-24.68 -38.51,631.68,-24.68"},
	{"deployed leg 5 prince_tortheldrin pull group 0", "14400,14399,14400,14400@105.19,724.41,-24.58 105.19,724.41,-24.58 105.19,724.41,-24.58 105.19,724.41,-24.58"},
}

// dmwBacktrackPackLoops is how many of the 8 backtracks leave no boss row and no crystal hold row inside their loop: the
// hall-middle pack's in-and-out (the pack stands 20 yd above the corridor the walk must take, so it is pulled from the hall
// floor and that is a 113 yd detour). The other seven hold a boss or a crystal hold.
const dmwBacktrackPackLoops = 1

// dmwPackDetours is the two packs a backtrack may turn round over that turn round at a pack and not at a boss or a crystal: the hall-middle
// pack the z -23 corridor walk passes ~20 yd below (it must be pulled or the corridor's walk wakes it) and the balcony
// north-arm pack 35 yd off the balcony line (aggro 24-25, so the walk cannot leave it alive). Each is pinned by the point
// its loop turns at.
var dmwPackDetours = []struct {
	x, y, z float64
	why     string
}{
	{46.6, 507.8, -3.6, "the hall-middle pack above the z -23 corridor"},
	{-47.2, 562.2, 28.6, "the balcony north-arm pack"},
}

// TestDireMaulWestRouteOrder pins the plan's route data and the navmesh walk its exported rows take. It fails on
// origin/dev's plan (93 groups, 15,677.3 yd, 31 backtracks) and on any later edit that puts a pack back in a leg its walk
// does not pass, moves an anchor, or walks ground twice.
func TestDireMaulWestRouteOrder(t *testing.T) {
	p, err := dungeon.Load(dmwPlan)
	if err != nil {
		t.Fatal(err)
	}
	dmwPlanPins(t, p)

	pl := dmwPlannerOrNil(t)
	if pl == nil {
		// The plan's own data is pinned above; only the walk, its complete paths and the backtracks need the mesh.
		t.Log("no map 429 navmesh files: the walk bound, the per-hop complete-path check and the backtrack pins are not measured (set TURTLE_AGENT_MMAPS to a folder holding 429.mmap and its tiles)")
		return
	}
	defer pl.Close()

	// The rows the migration walks: plan-export's --gm-cross-direct --floor-aware --approach-hops cut.
	res, err := planexport.Build(p, planexport.Options{ApproachHops: true, GMCrossDirect: true, Cut: zgMandokirExportCut(pl)})
	if err != nil {
		t.Fatal(err)
	}
	var stops []dungeon.Point
	var kinds []string
	for _, s := range res.Rows.Steps {
		if !dmwPartyRows(s.Kind) {
			continue
		}
		stops = append(stops, dungeon.Point{X: s.X, Y: s.Y, Z: s.Z})
		kinds = append(kinds, s.Kind)
	}
	if len(stops) < 2 {
		t.Fatalf("%d exported party rows, want the walk", len(stops))
	}
	entrance := dungeon.Point{X: p.Entrance.X, Y: p.Entrance.Y, Z: p.Entrance.Z}
	hop := make([]float64, len(stops))
	straight := make([]float64, len(stops))
	prev := entrance
	for i, s := range stops {
		what := "entrance -> row 1"
		if i > 0 {
			what = fmt.Sprintf("row %d -> %d", i, i+1)
		}
		hop[i] = dmwPath(t, pl, p.Map, prev, s, what)
		straight[i] = math.Hypot(math.Hypot(float64(s.X-prev.X), float64(s.Y-prev.Y)), float64(s.Z-prev.Z))
		prev = s
	}
	var walk float64
	for i := 1; i < len(stops); i++ {
		walk += hop[i]
	}
	t.Logf("dire_maul_west: %d rows, %d packs, %.1f yd between the exported rows (%.1f with the entrance hop), %.1f yd straight",
		len(stops), dmwPacks(p), walk, walk+hop[0], dmwStraight(p))
	if walk > dmwMaxWalkYd {
		t.Errorf("dire_maul_west walks %.1f yd between the exported rows (cap %.0f; origin/dev 15,677.3): a pack back in a leg its walk does not pass, an anchor moved off the route or a detour added shows up here", walk, dmwMaxWalkYd)
	}
	runs, yd := dmwBacktracks(stops, hop, straight)
	t.Logf("dire_maul_west: %d backtracks, %.1f yd re-walked (origin/dev: 31 / 9,111.5)", len(runs), yd)
	if len(runs) != dmwBacktrackPins {
		t.Errorf("dire_maul_west has %d backtracks (%.1f yd), want %d (the 2026-11-15 route order)", len(runs), yd, dmwBacktrackPins)
	}
	if yd > dmwMaxBacktrackYd {
		t.Errorf("dire_maul_west re-walks %.1f yd, cap %.0f (measured 1,181.7): the order walks ground twice beyond the dead ends", yd, dmwMaxBacktrackYd)
	}
	dmwDeadEnds(t, runs, stops, kinds, hop)
}

// dmwRun is one backtrack: the samples' cumulative route distance, and the loop (route walked since the walk was last
// within 10 yd of the same spot).
type dmwRun struct {
	start, end, loop float64
}

// dmwDeadEnds checks that every backtrack leaves a required stop: the rows walked inside its loop include a boss row, a
// crystal hold row, or one of the two dmwPackDetours the walk has to pull. hop is the navmesh length of each row hop.
func dmwDeadEnds(t *testing.T, runs []dmwRun, stops []dungeon.Point, kinds []string, hop []float64) {
	t.Helper()
	cum := make([]float64, len(stops)) // the same cumulative distance dmwBacktracks samples at: the entrance hop first
	cum[0] = hop[0]
	for i := 1; i < len(stops); i++ {
		cum[i] = cum[i-1] + hop[i]
	}
	rowAt := func(yd float64) int { // the last row at or before yd
		i := 0
		for j := range cum {
			if cum[j] <= yd+0.01 {
				i = j
			}
		}
		return i
	}
	loopholes := 0
	for _, r := range runs {
		from, to := rowAt(r.start-r.loop), rowAt(r.start)
		required, pack := false, false
		for i := from; i <= to; i++ {
			if kinds[i] == planexport.KindBoss || kinds[i] == planexport.KindHold {
				required = true
			}
			if kinds[i] != planexport.KindPull {
				continue
			}
			for _, d := range dmwPackDetours {
				if math.Hypot(math.Hypot(float64(stops[i].X)-d.x, float64(stops[i].Y)-d.y), float64(stops[i].Z)-d.z) <= 2 {
					pack = true
					t.Logf("backtrack %.1f yd turns round over the pack at %.1f %.1f %.1f: %s", r.end-r.start, stops[i].X, stops[i].Y, stops[i].Z, d.why)
				}
			}
		}
		switch {
		case required:
		case pack:
			loopholes++
		default:
			t.Errorf("backtrack %.1f yd (loop %.1f) walks ground twice with no boss row, hold row or pinned pack inside its loop: the order entered a dead end it did not need", r.end-r.start, r.loop)
		}
	}
	if loopholes != dmwBacktrackPackLoops {
		t.Errorf("%d backtracks leave no boss or crystal hold inside their loop, want %d: a pack detour came or went (the walk cannot pass those two packs alive)", loopholes, dmwBacktrackPackLoops)
	}
}

// dmwPlanPins checks the plan's own data: the boss order, each leg's anchor and vias, its packs, and the dropped set.
// It needs no navmesh.
func dmwPlanPins(t *testing.T, p *dungeon.Plan) {
	t.Helper()
	if len(p.Legs) != len(dmwLegAnchors) {
		t.Fatalf("%d legs, want %d (the boss order's legs)", len(p.Legs), len(dmwLegAnchors))
	}
	var bosses []uint32
	for _, l := range p.Legs {
		if l.Boss == nil {
			continue
		}
		bosses = append(bosses, l.Boss.Entry)
	}
	if !slices.Equal(bosses, dmwBossOrder) {
		t.Errorf("boss order %v, want %v (Tendris, Revanchion, Illyanna, Kalendris, Immol'thar, Tortheldrin)", bosses, dmwBossOrder)
	}
	for i, l := range p.Legs {
		want := dmwLegAnchors[i]
		if got := dmwPointText(l.To); got != want.to {
			t.Errorf("leg %d (%s) to %s, want %s: the anchors are deployed rows the walk takes first (the report says which)", i, l.Name, got, want.to)
		}
		vias := make([]string, len(l.Via))
		for j, w := range l.Via {
			vias[j] = dmwPointText(w.Point)
		}
		if !slices.Equal(vias, want.vias) {
			t.Errorf("leg %d (%s) vias %v, want %v: leg 4 keeps the deployed 30-point route from Kalendris to Immol'thar's hall", i, l.Name, vias, want.vias)
		}
		packs := make([]string, len(l.PullGroups))
		for j, g := range l.PullGroups {
			packs[j] = dmwPackText(g)
		}
		if want := dmwLegPacks[i]; !slices.Equal(packs, want) {
			t.Errorf("leg %d (%s) packs\n got  %v\n want %v\n(the 2026-11-15 order: each leg holds the packs its walk passes, in walk order)", i, l.Name, packs, want)
		}
	}
	if n := dmwPacks(p) + len(dmwDropped); n != dmwDeployedPacks {
		t.Errorf("%d retained packs + %d dropped = %d, want the deployed plan's %d groups: the re-order moves and drops, it adds nothing", dmwPacks(p), len(dmwDropped), n, dmwDeployedPacks)
	}
	// A dropped pack may not come back in the same leg it was dropped from (two deployed groups can carry the same entries
	// and positions, so the text alone does not say which one is gone).
	for _, d := range dmwDropped {
		var leg int
		if _, err := fmt.Sscanf(d.where, "deployed leg %d", &leg); err != nil {
			t.Fatalf("dropped pack %q: %v", d.where, err)
		}
		for _, g := range p.Legs[leg].PullGroups {
			if dmwPackText(g) == d.pack {
				t.Errorf("dropped pack %s (%s) is back in leg %d", d.pack, d.where, leg)
			}
		}
	}
}

// dmwPacks counts the plan's pull groups.
func dmwPacks(p *dungeon.Plan) int {
	n := 0
	for _, l := range p.Legs {
		n += len(l.PullGroups)
	}
	return n
}

// dmwStraight is the plan's straight-line length, the floor the audit compares the walk to: the exported rows' party
// points, hop by hop.
func dmwStraight(p *dungeon.Plan) float64 {
	pts, err := routePoints(p)
	if err != nil {
		return 0
	}
	var yd float64
	for i := 1; i < len(pts); i++ {
		yd += math.Hypot(math.Hypot(float64(pts[i].px-pts[i-1].px), float64(pts[i].py-pts[i-1].py)), float64(pts[i].pz-pts[i-1].pz))
	}
	return yd
}

// dmwPointText is a point at the audit's precision.
func dmwPointText(p dungeon.Point) string {
	return fmt.Sprintf("%.2f,%.2f,%.2f", p.X, p.Y, p.Z)
}

// dmwPackText is a pack's identity: its entries (in plan order, multiplicity kept) and every position.
func dmwPackText(g dungeon.PullGroup) string {
	entries := make([]string, len(g.Entries))
	for i, e := range g.Entries {
		entries[i] = fmt.Sprint(e)
	}
	pts := make([]string, len(g.Positions))
	for i, p := range g.Positions {
		pts[i] = dmwPointText(p)
	}
	return strings.Join(entries, ",") + "@" + strings.Join(pts, " ")
}

// dmwPartyRows are the rows that put the party somewhere: an interaction row (open, loot) is not a waypoint.
func dmwPartyRows(kind string) bool {
	switch kind {
	case planexport.KindMove, planexport.KindMoveDirect, planexport.KindMoveHopExact, planexport.KindPull, planexport.KindBoss, planexport.KindHold:
		return true
	}
	return false
}

// dmwPlannerOrNil is the map-429 navmesh when the machine has the server's mmaps, nil otherwise.
func dmwPlannerOrNil(t *testing.T) *navmesh.Planner {
	t.Helper()
	home, _ := os.UserHomeDir()
	for _, dir := range []string{os.Getenv("TURTLE_AGENT_MMAPS"), filepath.Join(home, "turtle-agent-mmaps")} {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "429.mmap")); err != nil {
			continue
		}
		if tiles, _ := filepath.Glob(filepath.Join(dir, "429*.mmtile")); len(tiles) == 0 {
			continue
		}
		return navmesh.NewPlanner(dir)
	}
	return nil
}

// dmwPath is the navmesh path a->b; a missing or partial path fails the test, it is not zero.
func dmwPath(t *testing.T, pl *navmesh.Planner, mapID uint32, a, b dungeon.Point, what string) float64 {
	t.Helper()
	path, err := pl.Plan(mapID, navmesh.Vec3{a.X, a.Y, a.Z}, navmesh.Vec3{b.X, b.Y, b.Z})
	if err != nil || path.Status != navmesh.Complete {
		t.Errorf("%s: no complete navmesh path %.1f %.1f %.1f -> %.1f %.1f %.1f: %v (%s %s)",
			what, a.X, a.Y, a.Z, b.X, b.Y, b.Z, err, path.Status, path.Reason)
		return 0
	}
	return float64(path.Length)
}

// dmwBacktracks counts the audit's backtracks on the walk: samples every 4 yd along the straight line between rows,
// cumulative length the navmesh hop; a revisit is within 10 yd (5 yd in height) of a sample at least 60 yd of route
// earlier; a run of revisits at least 40 yd long (gaps of at most 15 yd joined) is a backtrack, and its yards are the
// run's own. hop and straight are the navmesh and straight lengths of each row hop, entrance first.
func dmwBacktracks(stops []dungeon.Point, hop, straight []float64) ([]dmwRun, float64) {
	type sample struct {
		x, y, z, cum, loop float64
	}
	samples := make([]sample, 0, len(stops)*8)
	samples = append(samples, sample{float64(stops[0].X), float64(stops[0].Y), float64(stops[0].Z), hop[0], 0})
	cum := hop[0]
	for i := 1; i < len(stops); i++ {
		a, b := stops[i-1], stops[i]
		n := int(math.Ceil(straight[i] / 4))
		if n < 1 {
			n = 1
		}
		for k := 1; k < n; k++ {
			f := float64(k) / float64(n)
			samples = append(samples, sample{
				float64(a.X) + float64(b.X-a.X)*f,
				float64(a.Y) + float64(b.Y-a.Y)*f,
				float64(a.Z) + float64(b.Z-a.Z)*f,
				cum + hop[i]*f,
				0,
			})
		}
		cum += hop[i]
		samples = append(samples, sample{float64(b.X), float64(b.Y), float64(b.Z), cum, 0})
	}
	type cell struct{ x, y int }
	grid := map[cell][]int{}
	revisit := make([]bool, len(samples))
	for i := range samples {
		q := &samples[i]
		gx, gy := int(math.Floor(q.x/10)), int(math.Floor(q.y/10))
		for dx := -1; dx <= 1; dx++ {
			for dy := -1; dy <= 1; dy++ {
				for _, j := range grid[cell{gx + dx, gy + dy}] {
					o := samples[j]
					if q.cum-o.cum >= 60 && math.Abs(o.z-q.z) <= 5 &&
						(o.x-q.x)*(o.x-q.x)+(o.y-q.y)*(o.y-q.y) <= 100 {
						revisit[i] = true
						if q.loop == 0 || o.cum > q.loop {
							q.loop = o.cum
						}
					}
				}
			}
		}
		grid[cell{gx, gy}] = append(grid[cell{gx, gy}], i)
	}
	var runs []dmwRun
	yd := 0.0
	start, end, loop := 0.0, 0.0, 0.0
	inRun := false
	for i := range samples {
		if !revisit[i] {
			continue
		}
		if inRun && samples[i].cum-end <= 15 {
			end = samples[i].cum
			continue
		}
		if inRun && end-start >= 40 {
			runs = append(runs, dmwRun{start: start, end: end, loop: start - loop})
			yd += end - start
		}
		start, end, loop, inRun = samples[i].cum, samples[i].cum, samples[i].loop, true
	}
	if inRun && end-start >= 40 {
		runs = append(runs, dmwRun{start: start, end: end, loop: start - loop})
		yd += end - start
	}
	return runs, yd
}
