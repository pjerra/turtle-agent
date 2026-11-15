// The Karazhan Crypt route's order: the walk visits each pack on the boss-to-boss road it belongs to instead of sweeping
// the upper hall, the entrance hall and the crypt's wings from a fixed grid (issue #1542, the route-order audit of
// 2026-11-12, ~/TortoiseBots-audit/tests/route-audit-1112/order). The pins are the walk, the pack order leg by leg, the
// dropped packs, the moved anchors and the backtracks, so a later edit that adds a detour, drops a pack or re-walks a
// corridor is a failure and not just a print.
package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

const (
	// kcPlan is the plan under test.
	kcPlan = "../../dungeons/karazhan_crypt.json"
	// kcDevWalkYd is origin/dev's plan measured here (the same party rows, the same navmesh sums): 8,755.1 yd with 21
	// backtracks (3,070.7 yd re-walked), for a 1,900 yd straight route. On its exported rows it is 9,198.6 yd. The
	// re-order cuts it to the cap below.
	kcDevWalkYd = 8755.1
	// kcMaxWalkYd caps the re-ordered walk: 5,861.8 yd measured here (6,159.3 on the exported rows), so 5 % of headroom
	// for a cut point or a merge point that moves.
	kcMaxWalkYd = 6150.0
	// kcMaxBacktracks is the number of runs of re-walked ground the audit's rule finds (10 yd xy, 5 yd z, at least 60 yd
	// of route earlier, a run of at least 40 yd): 7 here, 1,156.3 yd, against origin/dev's 21 and 3,070.7 yd.
	kcMaxBacktracks = 7
)

// kcBosses is the plan's boss order: leg 3 (the dive) has none, and that is part of the route.
var kcBosses = [7]uint32{91916, 92935, 91919, 0, 91920, 91929, 91917}

// kcAnchors are the legs whose `to` is the leg's own first pack row (the exporter's centre of it): the walk arrives there
// before the first pull, so anything else made it climb back over the packs that stand in between.
var kcAnchors = map[int]dungeon.Point{
	0: {X: -11103.0, Y: -1751.17, Z: 44.46},
	1: {X: -11228.2, Y: -1822.99, Z: 4.25},
	2: {X: -11196.05, Y: -1671.825, Z: 12.18},
	3: {X: -11039.6, Y: -1560.82, Z: 27.31},
}

// kcDive are the dive leg's first shaft pack's approach rows: the deployed leg's `to` (the dry point on the south ramp,
// above the 3.046 water surface) and its pg0 approach (the shaft floor). The dive is unchanged by the re-order; it now
// starts after the middle level's corridor pulls.
var kcDive = [2]dungeon.Point{
	{X: -11219.96, Y: -1586.82, Z: 4.13},
	{X: -11221.3, Y: -1560.0, Z: -45.9},
}

// kcExempt are the points whose hops the navmesh cannot answer on purpose: the dive's two rows (water to water through
// the off-mesh dive link), the slope pack's stand under the corridor (-11213.8 -1632 -34.3, plan-check's re-anchored
// failure on origin/dev too) and leg 6's two check_skip swim rows (S1, S2: the mmaps have no swim volume).
var kcExempt = []dungeon.Point{
	kcDive[0], kcDive[1],
	{X: -11213.8, Y: -1632.0, Z: -34.3},
	{X: -11211.0, Y: -1516.0, Z: -3.0},
	{X: -11205.0, Y: -1514.5, Z: -0.5},
}

var kcLegPacks = [7][]string{
	{ // archlich_enkhraz
		"91925@-11103.0,-1751.2,44.5",
		"91925@-11091.4,-1747.0,43.6",
		"91924@-11092.4,-1728.7,36.9",
		"91925@-11099.8,-1721.1,36.7",
		"91925+91924@-11110.0,-1717.5,36.4",
		"91924@-11103.1,-1694.4,31.3",
		"91924@-11088.8,-1708.0,34.9",
		"91924@-11074.0,-1720.0,36.1",
		"91924+91925@-11069.9,-1740.0,34.3",
		"91924@-11052.7,-1751.1,32.7",
		"91924@-11042.7,-1739.5,31.4",
		"91924@-11031.9,-1751.5,31.1",
		"91924@-11025.6,-1730.8,29.6",
		"91924@-11013.6,-1721.7,28.0",
		"91924@-11050.8,-1688.0,27.2",
		"91923@-11059.8,-1740.6,19.3",
		"91925@-11087.9,-1737.0,16.1",
		"91925@-11089.6,-1749.3,15.9",
		"91925@-11117.0,-1740.0,9.0",
		"91923@-11107.4,-1727.1,8.3",
		"91923@-11132.2,-1714.7,4.6",
		"91925@-11137.4,-1700.4,3.6",
		"91925@-11113.0,-1699.3,4.3",
		"91925@-11088.5,-1698.2,4.3",
		"91925@-11088.7,-1668.4,4.3",
		"91924@-11101.7,-1673.2,3.7",
		"91926@-11117.7,-1677.3,6.8",
		"91924@-11151.0,-1679.1,6.2",
		"91924@-11168.2,-1674.2,8.1",
		"91924@-11182.4,-1685.5,4.3",
		"91924@-11205.8,-1685.1,4.4",
		"91925@-11195.7,-1701.2,4.3",
		"91925@-11210.0,-1727.9,4.3",
		"91925@-11223.6,-1728.8,4.3",
		"91925@-11222.7,-1759.0,4.3",
		"91925@-11232.2,-1777.3,4.3",
		"91925@-11198.9,-1776.4,4.3",
		"91925@-11209.7,-1758.6,4.3",
	},
	{ // guard_captain_gort
		"91925@-11228.2,-1823.0,4.3",
		"91924+91924@-11242.6,-1811.0,4.3",
		"91924+91924@-11244.5,-1791.9,4.3",
		"91925@-11239.7,-1702.4,4.3",
		"91925@-11256.6,-1702.9,4.3",
		"91924@-11244.7,-1689.7,3.8",
		"91924@-11234.6,-1656.0,3.1",
		"91924@-11225.1,-1672.5,6.2",
	},
	{ // commander_andreon
		"91926+91924@-11196.0,-1671.8,12.2",
		"91926@-11118.3,-1630.8,5.9",
		"91924@-11126.5,-1618.5,4.0",
		"91925@-11104.2,-1600.7,4.2",
		"91924@-11102.6,-1615.9,3.2",
		"91925@-11090.4,-1629.9,4.2",
		"91924@-11104.6,-1637.5,3.7",
		"91924@-11118.8,-1649.5,1.3",
		"91924@-11129.9,-1669.0,7.7",
		"91924@-11040.7,-1680.3,27.5",
		"91925@-11029.0,-1669.8,27.8",
		"91925@-11061.3,-1670.6,27.4",
		"91924@-11090.6,-1677.3,28.4",
		"91925@-11103.5,-1670.6,28.2",
		"91923@-11109.9,-1625.7,28.0",
		"91910+91911@-11094.2,-1618.6,28.3",
		"91923@-11078.2,-1625.9,28.0",
		"91925@-11088.1,-1598.8,28.2",
		"91923@-11076.7,-1586.4,29.4",
		"91924@-11083.0,-1578.3,29.6",
		"91924@-11109.5,-1573.4,27.4",
		"91924@-11111.0,-1558.8,25.6",
		"91924@-11077.2,-1560.1,27.2",
		"91924@-11058.1,-1579.5,27.8",
		"91924+91910+91924+91924@-11033.1,-1529.4,33.1",
		"91923+91923@-11023.3,-1510.5,30.1",
		"91923@-11049.6,-1516.1,32.1",
		"91918@-11064.7,-1514.1,29.6",
		"91924@-11072.8,-1495.3,29.1",
		"91924@-11055.2,-1488.3,29.1",
		"91924@-11070.7,-1483.5,31.0",
		"91924@-11093.4,-1471.5,31.1",
		"91924@-11095.2,-1495.3,28.1",
		"91923@-11112.7,-1522.4,27.6",
		"91912@-11102.8,-1544.5,27.6",
		"91910+91913@-11096.6,-1523.5,28.3",
		"91912@-11090.0,-1543.7,27.8",
		"91923@-11080.8,-1518.4,28.0",
		"91910@-11064.6,-1529.0,28.4",
	},
	{ // marrowspike_dive
		"91924@-11039.6,-1560.8,27.3",
		"91924@-11017.2,-1558.8,28.1",
		"91924@-11015.5,-1577.9,27.9",
		"91924@-11031.1,-1581.2,28.4",
		"91925@-11056.1,-1597.7,28.0",
		"91925@-11024.4,-1595.9,28.0",
		"91923@-11014.3,-1623.5,28.0",
		"91911+91913@-11030.6,-1617.1,28.3",
		"91918+91911+91923@-11057.0,-1620.7,28.0",
		"91924@-11154.6,-1619.3,4.1",
		"91924@-11141.1,-1633.2,8.9",
		"91925@-11199.0,-1604.7,4.3",
		"91924+91926@-11187.2,-1623.9,5.8",
		"91924@-11173.9,-1632.7,7.3",
		"91926+91924+91926@-11167.0,-1651.3,1.3",
		"91924@-11191.5,-1652.7,0.7",
		"91926@-11211.5,-1653.4,1.5",
		"91924@-11199.0,-1631.8,6.1",
		"91924@-11213.6,-1630.4,9.3",
		"91926@-11225.6,-1634.0,10.7",
		"91925@-11241.5,-1604.7,4.3",
		"91932@-11219.7,-1575.8,-1.5",
		"91932@-11207.9,-1553.2,-44.7",
		"91932@-11237.8,-1554.0,-41.2",
		"91932@-11210.3,-1604.6,-42.7",
		"91932@-11233.0,-1606.0,-42.8",
		"91925+91918+91925@-11219.4,-1638.7,-31.3",
	},
	{ // marrowspike
		"91923@-11217.0,-1718.2,-29.9",
		"91912@-11230.5,-1686.8,-28.5",
		"91912@-11204.4,-1685.9,-28.5",
		"91912@-11183.4,-1706.8,-28.5",
		"91915@-11181.7,-1737.1,-28.5",
		"91912@-11181.3,-1767.5,-28.5",
		"91915@-11194.2,-1796.4,-28.5",
		"91923@-11215.1,-1778.0,-29.9",
		"91915@-11238.1,-1797.7,-28.5",
	},
	{ // hivaxxis
		"91923@-11173.3,-1741.9,-15.4",
		"91923@-11161.5,-1727.5,-28.5",
		"91923@-11160.3,-1754.0,-28.5",
		"91924@-11137.5,-1763.8,-29.7",
		"91924+91924@-11115.8,-1771.1,-28.5",
		"91924@-11120.4,-1754.3,-28.5",
		"91923@-11107.3,-1742.5,-28.5",
		"91923@-11129.1,-1720.0,-29.7",
		"91923@-11143.1,-1695.1,-28.5",
		"91912@-11250.4,-1708.9,-28.5",
		"91915@-11250.3,-1738.7,-28.5",
		"91912@-11249.0,-1769.3,-28.5",
		"91923@-11159.5,-1696.2,-28.5",
		"91930+91930+91930+91930@-11206.6,-1743.9,-15.6",
		"91930@-11229.1,-1746.9,-15.4",
		"91930+91930@-11242.7,-1758.7,-15.7",
	},
	{ // corpsemuncher
		"91932@-11219.5,-1512.1,-1.5",
		"91911@-11222.1,-1472.6,3.6",
		"91924+91926+91924@-11241.7,-1452.1,2.3",
		"91924+91926+91924@-11209.9,-1452.2,1.8",
		"91910@-11177.9,-1451.0,1.2",
		"91926+91924+91924@-11189.2,-1433.9,1.1",
		"91924+91926+91924@-11187.7,-1401.8,-0.9",
	},
}

// kcDropped are the packs the re-order drops: the walk never wakes them (trash-check's unplanned pack set is the
// same as the deployed rows'), they stand off the boss-to-boss road, behind a wall or on another deck.
var kcDropped = []string{
	"91923@-11241.9,-1655.8,-28.5",             // deployed leg 4 pull group 21
	"91925@-11130.2,-1655.3,-28.5",             // deployed leg 4 pull group 22
	"91924+91924@-11137.8,-1803.5,-28.7",       // deployed leg 4 pull group 23
	"91924+91924+91923@-11166.5,-1803.5,-28.5", // deployed leg 4 pull group 24
	"91923@-11178.0,-1479.0,3.6",               // deployed leg 6 pull group 1
	"91924+91926@-11256.7,-1434.1,1.4",         // deployed leg 6 pull group 7
	"91924+91926@-11260.4,-1417.0,-0.0",        // deployed leg 6 pull group 8
	"91924@-11259.6,-1394.2,-0.1",              // deployed leg 6 pull group 9
	"91910@-11266.7,-1453.2,2.7",               // deployed leg 6 pull group 10
	"91923@-11265.4,-1481.7,3.6",               // deployed leg 6 pull group 11
}

// TestKarazhanCryptRouteOrder pins the pack order of plan karazhan_crypt leg by leg, the dropped set, the moved anchors
// and the walk: every consecutive pair of rows has a complete navmesh path (bar the dive's and the swim's water hops),
// the walk stays under kcMaxWalkYd, and every re-walked run but one leaves a boss's dead end. It fails on origin/dev's
// plan, whose legs sweep the same halls from a fixed grid: 8,755.1 yd there with 21 backtracks, and the leg-2 pack the
// plan pulls last comes before its first one here.
func TestKarazhanCryptRouteOrder(t *testing.T) {
	p, err := dungeon.Load(kcPlan)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Legs) != len(kcBosses) {
		t.Fatalf("%d legs, want %d (one per boss and the dive leg, in boss order)", len(p.Legs), len(kcBosses))
	}
	for i, want := range kcBosses {
		got := uint32(0)
		if p.Legs[i].Boss != nil {
			got = p.Legs[i].Boss.Entry
		}
		if got != want {
			t.Errorf("leg %d (%s) boss entry %d, want %d: the boss order is part of the route", i, p.Legs[i].Name, got, want)
		}
	}
	packs := 0
	for i, leg := range p.Legs {
		packs += len(leg.PullGroups)
		want := kcLegPacks[i]
		got := make([]string, 0, len(leg.PullGroups))
		for _, g := range leg.PullGroups {
			got = append(got, kcPackID(g))
		}
		if !kcSamePacks(got, want) {
			at := 0
			for at < len(got) && at < len(want) && kcSamePack(got[at], want[at]) {
				at++
			}
			t.Errorf("leg %d (%s): %d packs, want %d; first difference at pack %d:\n got %v\nwant %v",
				i, leg.Name, len(got), len(want), at, short(got, at), short(want, at))
		}
	}
	for _, d := range kcDropped {
		for i, leg := range p.Legs {
			for _, g := range leg.PullGroups {
				if kcSamePack(kcPackID(g), d) {
					t.Errorf("pack %s is back in leg %d (%s): the re-order drops it (the walk never wakes it)", d, i, leg.Name)
				}
			}
		}
	}
	for leg, want := range kcAnchors {
		got := p.Legs[leg].To
		if math.Abs(float64(got.X-want.X)) > 0.05 || math.Abs(float64(got.Y-want.Y)) > 0.05 || math.Abs(float64(got.Z-want.Z)) > 0.05 {
			t.Errorf("leg %d `to` %.2f %.2f %.2f, want %.2f %.2f %.2f (its first pack's row)",
				leg, got.X, got.Y, got.Z, want.X, want.Y, want.Z)
		}
	}
	if dive, ok := kcDiveGroup(p); !ok || len(dive.Approach) != 2 ||
		math.Abs(float64(dive.Approach[0].X-kcDive[0].X)) > 0.05 ||
		math.Abs(float64(dive.Approach[1].Z-kcDive[1].Z)) > 0.05 {
		t.Errorf("the dive group's approach %v, want the dry ramp point then the shaft floor %v: the dive starts dry",
			dive.Approach, kcDive)
	}

	pl := kcPlannerOrNil(t)
	if pl == nil {
		t.Skip("no map 800 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 800.mmap and its tiles")
	}
	defer pl.Close()

	res, err := planexport.Build(p, planexport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var rows []dungeon.Point
	kinds := map[int]string{}
	var bossRows []int
	for _, s := range res.Rows.Steps {
		if !kcPartyRows(s.Kind) {
			continue
		}
		rows = append(rows, dungeon.Point{X: s.X, Y: s.Y, Z: s.Z})
		kinds[len(rows)-1] = s.Kind
		if s.Kind == planexport.KindBoss {
			bossRows = append(bossRows, len(rows)-1)
		}
	}
	if len(rows) < 2 {
		t.Fatalf("%d party rows, want the walk", len(rows))
	}
	// The walk: every consecutive pair has a complete path (the water hops excepted), and the sum is the route.
	cum := make([]float64, len(rows))
	for i := 1; i < len(rows); i++ {
		a, b := rows[i-1], rows[i]
		path, err := pl.Plan(p.Map, navmesh.Vec3{a.X, a.Y, a.Z}, navmesh.Vec3{b.X, b.Y, b.Z})
		exempt := kcIsExempt(a) || kcIsExempt(b)
		if err != nil || path.Status != navmesh.Complete {
			if !exempt {
				t.Errorf("row %d->%d: no complete navmesh path %.1f %.1f %.1f -> %.1f %.1f %.1f: %v (%s %s)",
					i, i+1, a.X, a.Y, a.Z, b.X, b.Y, b.Z, err, path.Status, path.Reason)
			}
			cum[i] = cum[i-1] + kcStraight(a, b)
			continue
		}
		cum[i] = cum[i-1] + float64(path.Length)
	}
	total := cum[len(cum)-1]
	walked := kcSamples(t, pl, p.Map, rows, cum)
	runs := kcBacktracks(walked)
	t.Logf("karazhan_crypt walk %.1f yd (origin/dev %.1f), %d packs, %d backtracks, %.1f yd re-walked",
		total, kcDevWalkYd, packs, len(runs), kcRewalked(runs))
	if total > kcMaxWalkYd {
		t.Errorf("the walk is %.1f yd (cap %.0f, origin/dev %.1f): a leg walks back over its own packs; put each pack in the leg whose walk passes it",
			total, kcMaxWalkYd, kcDevWalkYd)
	}
	if len(runs) > kcMaxBacktracks {
		t.Errorf("%d backtracks (cap %d, origin/dev 18): %v", len(runs), kcMaxBacktracks, kcRunSteps(runs, cum, rows))
	}
	// Every re-walked run but the crypt's east wing leaves a boss's dead end: a boss row sits between the earlier pass
	// and the turn (the party had to come back the way it went in). The exception is the east wing of the lower crypt
	// (x -11210..-11160, y -1770..-1695): one corridor in and out, and the packs there wake the walk, so it is walked
	// twice. Any other run without a boss is a route defect.
	for _, r := range runs {
		if kcBossInLoop(r, cum, bossRows) {
			continue
		}
		if math.Abs(r.x+11174.0) < 40 && math.Abs(r.y+1695.0) < 40 {
			t.Logf("backtrack at %.0f %.0f %.0f (%.1f yd): the lower crypt's east wing, one corridor in and out", r.x, r.y, r.z, r.yards)
			continue
		}
		t.Errorf("backtrack at %.0f %.0f %.0f, %.1f yd re-walked, with no boss inside its loop: only a boss's dead end may be walked twice",
			r.x, r.y, r.z, r.yards)
	}
}

// kcPackID is a pack's identity in kcLegPacks: its entries and the pull row's centre (the mean of its positions), which
// is the point the exporter writes and the walk visits.
func kcPackID(g dungeon.PullGroup) string {
	var sx, sy, sz float64
	for _, p := range g.Positions {
		sx += float64(p.X)
		sy += float64(p.Y)
		sz += float64(p.Z)
	}
	n := float64(len(g.Positions))
	entries := ""
	for i, e := range g.Entries {
		if i > 0 {
			entries += "+"
		}
		entries += fmt.Sprint(e)
	}
	return fmt.Sprintf("%s@%.1f,%.1f,%.1f", entries, sx/n, sy/n, sz/n)
}

// short is a slice of at most 5 identities from at, for a readable failure.
func short(ids []string, at int) []string {
	if at+5 < len(ids) {
		return ids[at : at+5]
	}
	if at < len(ids) {
		return ids[at:]
	}
	return nil
}

// kcDiveGroup is the dive leg's 91932 pack that carries the dive (its approach is the dry point and the shaft floor).
func kcDiveGroup(p *dungeon.Plan) (dungeon.PullGroup, bool) {
	for _, g := range p.Legs[3].PullGroups {
		if len(g.Approach) == 2 && len(g.Entries) == 1 && g.Entries[0] == 91932 {
			return g, true
		}
	}
	return dungeon.PullGroup{}, false
}

// kcSamePacks compares two pack identity lists; kcSamePack one pair.
func kcSamePacks(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if !kcSamePack(got[i], want[i]) {
			return false
		}
	}
	return true
}

// kcSamePack compares "entries@x,y,z" identities: the entries exactly, the centre within kcCentreTol (the plan's points
// are float32, so a mean on a .05 boundary prints one tenth either way).
func kcSamePack(a, b string) bool {
	const tol = 0.25
	ae, ac, ok1 := kcSplitPack(a)
	be, bc, ok2 := kcSplitPack(b)
	if !ok1 || !ok2 || ae != be {
		return a == b
	}
	for i := range ac {
		if math.Abs(ac[i]-bc[i]) > tol {
			return false
		}
	}
	return true
}

// kcSplitPack splits "entries@x,y,z".
func kcSplitPack(s string) (string, [3]float64, bool) {
	var c [3]float64
	at := strings.LastIndex(s, "@")
	if at < 0 {
		return s, c, false
	}
	parts := strings.Split(s[at+1:], ",")
	if len(parts) != 3 {
		return s, c, false
	}
	for i, p := range parts {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return s, c, false
		}
		c[i] = v
	}
	return s[:at], c, true
}

// kcPartyRows are the rows that put the party somewhere: an interaction row (open, loot) is not a waypoint.
func kcPartyRows(kind string) bool {
	switch kind {
	case planexport.KindMove, planexport.KindMoveDirect, planexport.KindMoveHopExact, planexport.KindPull, planexport.KindBoss, planexport.KindHold:
		return true
	}
	return false
}

// kcIsExempt is a point whose hops the navmesh cannot answer on purpose (kcExempt).
func kcIsExempt(p dungeon.Point) bool {
	for _, e := range kcExempt {
		if math.Abs(float64(p.X-e.X)) < 0.6 && math.Abs(float64(p.Y-e.Y)) < 0.6 && math.Abs(float64(p.Z-e.Z)) < 0.6 {
			return true
		}
	}
	return false
}

// kcStraight is the straight-line distance a hop with no complete path is counted as.
func kcStraight(a, b dungeon.Point) float64 {
	dx, dy, dz := float64(a.X-b.X), float64(a.Y-b.Y), float64(a.Z-b.Z)
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

// kcSample is one point of the walked route, every kcSampleStep yards, with the route's distance there.
type kcSample struct {
	x, y, z float64
	c       float64
}

// kcRun is one run of re-walked ground: where it starts and ends on the route, the earlier pass it overlaps and the
// yards it re-walks.
type kcRun struct {
	i0, i1   int
	c0, c1   float64
	loop     float64 // the route distance of the earlier pass the run's deepest sample re-walks
	loopFrom float64
	x, y, z  float64
	yards    float64
}

// The audit's backtrack rule (route-order audit 2026-11-12, order/order.py): a sample is a revisit when it is within
// kcRevisitYd (xy) and kcFloorYd (z) of a sample walked at least kcEarlierYd of route earlier; a backtrack is a run of
// revisit samples at least kcRunMinYd long (gaps of up to kcJoinYd joined). A hop the navmesh cannot path is sampled on
// its straight line, as the audit did.
const (
	kcSampleStep = 4.0
	kcRevisitYd  = 10.0
	kcFloorYd    = 5.0
	kcEarlierYd  = 60.0
	kcRunMinYd   = 40.0
	kcJoinYd     = 15.0
)

// kcSamples walks every row pair's navmesh polyline and samples it every kcSampleStep yd.
func kcSamples(t *testing.T, pl *navmesh.Planner, mapID uint32, rows []dungeon.Point, cum []float64) []kcSample {
	t.Helper()
	var out []kcSample
	for i := 1; i < len(rows); i++ {
		a, b := rows[i-1], rows[i]
		base := cum[i-1]
		path, err := pl.Plan(mapID, navmesh.Vec3{a.X, a.Y, a.Z}, navmesh.Vec3{b.X, b.Y, b.Z})
		var pts []navmesh.Vec3
		if err == nil && path.Status == navmesh.Complete && len(path.Points) >= 2 {
			pts = path.Points
		} else {
			pts = []navmesh.Vec3{{a.X, a.Y, a.Z}, {b.X, b.Y, b.Z}}
		}
		run := 0.0
		for k := 0; k < len(pts)-1; k++ {
			p, q := pts[k], pts[k+1]
			seg := math.Sqrt(math.Pow(float64(q[0]-p[0]), 2) + math.Pow(float64(q[1]-p[1]), 2) + math.Pow(float64(q[2]-p[2]), 2))
			if seg <= 0 {
				continue
			}
			n := int(math.Ceil(seg / kcSampleStep))
			for s := 0; s < n; s++ {
				f := float64(s) / float64(n)
				out = append(out, kcSample{
					x: float64(p[0]) + float64(q[0]-p[0])*f,
					y: float64(p[1]) + float64(q[1]-p[1])*f,
					z: float64(p[2]) + float64(q[2]-p[2])*f,
					c: base + run + seg*f,
				})
			}
			run += seg
		}
		out = append(out, kcSample{x: float64(b.X), y: float64(b.Y), z: float64(b.Z), c: cum[i]})
	}
	return out
}

// kcBacktracks finds the runs of re-walked ground in the samples.
func kcBacktracks(S []kcSample) []kcRun {
	type cell struct{ x, y int }
	grid := map[cell][]int{}
	best := make([]float64, len(S))
	first := make([]float64, len(S))
	for i := range best {
		best[i], first[i] = -1, -1
	}
	for i, s := range S {
		cx, cy := int(math.Floor(s.x/kcRevisitYd)), int(math.Floor(s.y/kcRevisitYd))
		for dx := -1; dx <= 1; dx++ {
			for dy := -1; dy <= 1; dy++ {
				for _, j := range grid[cell{cx + dx, cy + dy}] {
					t := S[j]
					if s.c-t.c < kcEarlierYd || math.Abs(t.z-s.z) > kcFloorYd {
						continue
					}
					if math.Hypot(t.x-s.x, t.y-s.y) > kcRevisitYd {
						continue
					}
					if t.c > best[i] {
						best[i] = t.c
					}
					if first[i] < 0 || t.c < first[i] {
						first[i] = t.c
					}
				}
			}
		}
		grid[cell{cx, cy}] = append(grid[cell{cx, cy}], i)
	}
	var runs []kcRun
	cur := -1
	for i, s := range S {
		if best[i] < 0 {
			continue
		}
		if cur >= 0 && s.c-S[runs[cur].i1].c <= kcJoinYd {
			r := &runs[cur]
			r.i1 = i
			r.c1 = s.c
			if s.c-best[i] >= r.loop {
				r.loop = s.c - best[i]
				r.loopFrom = best[i]
			}
			continue
		}
		runs = append(runs, kcRun{i0: i, i1: i, c0: s.c, c1: s.c, loop: s.c - best[i], loopFrom: best[i], x: s.x, y: s.y, z: s.z})
		cur = len(runs) - 1
	}
	var out []kcRun
	for _, r := range runs {
		r.yards = r.c1 - r.c0
		if r.yards < kcRunMinYd {
			continue
		}
		out = append(out, r)
	}
	return out
}

// kcRewalked is the yards the runs re-walk.
func kcRewalked(runs []kcRun) float64 {
	total := 0.0
	for _, r := range runs {
		total += r.yards
	}
	return total
}

// kcBossInLoop reports whether a boss row lies between the earlier pass and the run's end: the party went in, killed
// the boss and came back the same way (a dead end).
func kcBossInLoop(r kcRun, cum []float64, bossRows []int) bool {
	for _, b := range bossRows {
		if cum[b] >= r.loopFrom && cum[b] <= r.c1 {
			return true
		}
	}
	return false
}

// kcRunSteps is the runs as (row, row, yards) for a failure message: the rows the run starts and ends between.
func kcRunSteps(runs []kcRun, cum []float64, rows []dungeon.Point) []string {
	var out []string
	for _, r := range runs {
		from, to := 0, 0
		for i, c := range cum {
			if c <= r.c0 {
				from = i + 1
			}
			if c <= r.c1 {
				to = i + 1
			}
		}
		out = append(out, fmt.Sprintf("%d-%d (%.1f yd at %.0f %.0f %.0f)", from, to, r.yards, r.x, r.y, r.z))
	}
	return out
}

// kcPlannerOrNil is the map-800 navmesh when the machine has the server's mmaps, nil otherwise.
func kcPlannerOrNil(t *testing.T) *navmesh.Planner {
	t.Helper()
	home, _ := os.UserHomeDir()
	for _, dir := range []string{os.Getenv("TURTLE_AGENT_MMAPS"), filepath.Join(home, "turtle-agent-mmaps")} {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "800.mmap")); err != nil {
			continue
		}
		if tiles, _ := filepath.Glob(filepath.Join(dir, "800*.mmtile")); len(tiles) == 0 {
			continue
		}
		return navmesh.NewPlanner(dir)
	}
	return nil
}
