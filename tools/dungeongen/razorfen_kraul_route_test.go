// The Razorfen Kraul route's order (issue #1542, route-order audit 2026-11-12): the walk visits each pack in the leg
// whose boss-to-boss road passes it instead of clearing "the clusters nearest each boss", and it drops the six packs
// that road leaves out. The pins are the plan's own data (boss order, each leg's packs, the dropped six, the anchors)
// and the navmesh walk the exported rows take, so a later edit that moves a pack back, adds a detour or drops one is a
// failure and not just a print.
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

// rfkPlan is the plan under test.
const rfkPlan = "../../dungeons/razorfen_kraul.json"

// rfkMaxWalkYd caps the re-ordered walk: 7,727.0 yd measured here between consecutive exported rows, 7,747.2 with the
// entrance -> first row hop, and 6,080.3 yd on the straight-line model the old test used. 2.2% of headroom for a cut
// point that moves. origin/dev's plan measures 17,071.4 yd here (17,111.5 with the entrance hop).
const rfkMaxWalkYd = 7900.0

// rfkBacktracks is the route-order audit's backtrack metric (tests/route-audit-1112/order/ORDER.md): the walk is
// sampled every 4 yd, a sample is a revisit within 10 yd (5 yd in height) of a sample walked at least 60 yd of route
// earlier, and a run of revisits at least 40 yd long (gaps of at most 15 yd joined) is a backtrack. The audit measured
// 13 backtracks / 3,056 yd on the re-ordered rows and 30 / 10,545 on origin/dev's. Each of the 13 is the way out of a
// dead end: the loop it makes holds the boss or the pack the walk leaves, and the shortest navmesh route from that
// element to the row after the retrace passes the retraced stretch (rfkBacktracks logs the count; the proof is in the
// 2026-11-15 round's report and razorfen_kraul-FIX.md).
const rfkBacktrackPins = 13

// rfkMaxBacktrackYd caps the re-walked distance the 13 backtracks cost: 3,056.0 yd measured here.
const rfkMaxBacktrackYd = 3200.0

// rfkDeployedPacks is the deployed plan's pack count (682 rows, 122 pulls): the 116 below plus the six dropped.
const rfkDeployedPacks = 122

// rfkBossOrder is the plan's boss order, unchanged by the re-order: Roogug, Aggem Thorncurse, Death Speaker Jargba,
// Overlord Ramtusk, Rotthorn, Agathelos the Raging, Charlga Razorflank.
var rfkBossOrder = []uint32{6168, 4424, 4428, 4420, 62503, 4422, 4421}

// rfkLegAnchors pins every leg's `to` (the first point its walk takes after the previous boss) and its vias. Seven
// `to`s moved in the re-order (legs 1-7); each is a pull-group position of the deployed plan and the report says
// which. Leg 8 keeps the deployed 26-via climb out of the pit; leg 7 keeps one of its 31 vias (via point 17, the west
// corridor the descent takes) and loses the loop the packs now walk.
var rfkLegAnchors = []struct {
	to   string
	vias []string
}{
	{"1994.00,1615.00,83.20", []string{}},                        // leg 0 roogug: deployed, its one via dropped (the entrance walk passes 1 yd from it)
	{"2001.63,1572.69,81.97", []string{}},                        // leg 1 aggem_thorncurse: deployed leg 1 pull group 15 position
	{"2143.27,1450.44,78.00", []string{}},                        // leg 2 death_speaker_jargba: deployed leg 2 pull group 3 position
	{"2158.89,1549.53,72.02", []string{}},                        // leg 3 overlord_ramtusk: deployed leg 0 pull group 25 position
	{"2177.46,1638.82,81.11", []string{}},                        // leg 4 rotthorn: deployed leg 3 pull group 2 position
	{"2090.83,1774.64,78.23", []string{}},                        // leg 5 agathelos_the_raging: deployed leg 4 pull group 1 position
	{"2143.74,2012.88,65.55", []string{}},                        // leg 6 charlga_den: deployed leg 5 pull group 9 position
	{"2045.97,1564.23,64.79", []string{"2043.20,1479.30,68.90"}}, // leg 7 charlga_pit: deployed leg 0 pull group 16 position; deployed leg 7 via 17
	{
		"2093.10,1767.50,79.80", // leg 8 charlga_razorflank: deployed, and all 26 deployed vias (the pit floor -> boss climb)
		[]string{
			"2117.10,1807.50,48.00", "2084.80,1783.90,54.40", "2054.10,1761.60,58.70", "2048.80,1721.10,60.30",
			"2085.60,1712.30,56.50", "2133.30,1725.90,52.50", "2125.10,1678.70,59.90", "2086.10,1665.60,62.90",
			"2069.30,1634.40,64.80", "2062.40,1589.20,64.00", "2057.60,1567.70,66.20", "2036.80,1536.00,81.20",
			"2030.90,1490.90,70.70", "2061.10,1453.30,68.80", "2104.80,1441.90,64.30", "2112.00,1443.50,65.20",
			"2145.90,1475.50,68.50", "2165.60,1514.70,67.90", "2176.00,1557.30,72.20", "2176.00,1578.70,76.60",
			"2176.00,1600.00,81.20", "2177.30,1604.80,82.90", "2143.70,1638.90,82.10", "2133.90,1649.30,82.90",
			"2124.80,1686.90,81.60", "2102.70,1728.00,78.20",
		},
	},
}

// rfkLegPacks is every retained pack per leg, in plan order: its entries (with their multiplicity, in the plan's order)
// and every position, two decimals. A pack that moves to another leg, changes its entries or moves 0.01 yd fails here.
// The deployed plan's packs are these 116 plus rfkDropped (the six the re-order drops), and nothing else.
var rfkLegPacks = [][]string{
	{ // leg 0 roogug: the entry courtyard and the corridor east to Roogug
		"4530@1987.53,1600.03,79.80",
		"4436,4436@1996.96,1606.41,81.82 2005.06,1597.86,81.31",
		"4520@1996.25,1630.64,80.92",
		"4436,4436@2000.94,1646.68,80.51 2012.39,1646.38,80.42",
		"4520@2019.32,1660.39,82.70",
		"4530,4436,4436@2032.30,1650.53,81.47 2044.06,1648.99,82.00 2048.13,1655.96,81.61",
		"4442@2080.18,1565.26,81.71",
		"4519@2092.09,1556.07,81.28",
		"4519@2103.12,1539.16,87.20",
		"4516,4519@2111.93,1550.17,81.62 2106.03,1551.15,81.62",
		"4442,4519,4516@2101.38,1594.22,80.49 2103.36,1601.37,80.88 2107.68,1609.01,80.77",
		"4516@2115.27,1580.91,81.45",
	},
	{ // leg 1 aggem_thorncurse: back west, the west court, the southern corridor and its east room
		"4530@2001.63,1572.69,81.97",
		"4436@2004.63,1556.32,80.77",
		"4435@2016.10,1550.59,81.22",
		"4435@2004.75,1530.54,81.00",
		"4436@2021.03,1521.25,79.50",
		"4436,4436@2033.56,1530.40,80.65 2029.93,1538.71,81.09",
		"4520@2019.37,1510.51,78.62",
		"4442,4442@2030.60,1498.11,72.99 2020.39,1493.41,74.50",
		"4442@2036.76,1475.77,67.63",
		"4516@2064.30,1459.09,67.47",
		"4523@2078.71,1443.38,66.66",
		"4516@2099.47,1436.66,63.69",
		"4442@2116.94,1439.54,64.02",
		"4442,4442@2124.19,1434.84,64.45 2119.56,1430.03,64.19",
		"4516@2132.69,1461.15,66.35",
		"4442@2152.56,1474.26,67.10",
		"4532@2154.73,1494.14,67.93",
		"4516@2155.97,1500.49,67.51",
		"4442@2178.74,1496.08,73.09",
		"4522@2163.20,1460.34,73.86",
		"4522,4520@2178.63,1474.30,72.36 2183.96,1480.54,72.91",
		"4442,4515@2194.78,1501.70,73.33 2191.84,1498.61,73.43",
		"4442@2182.08,1526.04,71.51",
		"4442@2169.80,1522.93,71.29",
		"4523@2152.30,1539.39,72.97",
		"4442@2152.14,1525.30,72.93",
		"4442@2137.49,1506.01,72.69",
		"4523@2135.65,1493.45,73.08",
		"4522@2122.21,1485.49,73.08",
		"4531@2104.29,1469.47,73.70",
		"4531@2101.57,1450.09,73.90",
	},
	{ // leg 2 death_speaker_jargba: two packs of the east room
		"4523@2143.27,1450.44,78.00",
		"4523,4440@2155.10,1429.69,74.33 2143.63,1430.93,75.92",
	},
	{ // leg 3 overlord_ramtusk: the east ring up to the boss
		"4523@2158.89,1549.53,72.02",
		"4623,4623@2174.52,1571.72,75.02 2181.49,1569.14,74.85",
		"4440@2183.11,1596.42,79.92",
		"4525@2189.81,1608.40,80.40",
		"4525@2182.19,1623.39,81.43",
		"4518,4518@2207.60,1611.83,81.64 2213.49,1602.72,81.31",
	},
	{ // leg 4 rotthorn: the central-east halls and down the west side
		"4440@2177.46,1638.82,81.11",
		"4623,4623@2159.30,1636.00,81.89 2161.15,1628.09,81.70",
		"4532@2146.03,1614.74,81.68",
		"4518,4518@2128.07,1619.58,81.67 2132.76,1613.51,81.84",
		"4532@2138.38,1630.94,81.74",
		"4623,4623@2132.34,1657.14,82.09 2125.63,1658.24,82.39",
		"4525,4842@2117.18,1693.67,80.58 2118.60,1695.30,80.30",
		"4525,6035,4518@2122.63,1707.94,80.50 2133.73,1699.00,80.83 2129.74,1707.63,80.11",
		"4623,4623@2091.51,1747.41,77.67 2088.14,1753.08,77.78",
		"6035,4525,4518@2066.02,1742.89,76.72 2065.17,1733.95,76.63 2072.74,1728.61,77.02",
		"62501@2018.40,1685.54,67.72",
		"62502,62501,62501@1981.83,1699.79,60.31 1978.79,1700.15,59.56 1982.02,1703.46,59.35",
		"62502@1985.56,1754.30,57.99",
		"62502,62501,62501@1990.26,1769.43,55.36 1990.71,1769.31,55.41 1992.70,1771.78,55.20",
		"62502,62501@1994.00,1809.72,50.95 1993.54,1817.08,50.73",
		"62501@1972.67,1800.18,51.61",
		"62502,62501,62501@1996.69,1800.02,79.11 2001.93,1800.87,80.00 1999.02,1797.51,80.27",
	},
	{ // leg 5 agathelos_the_raging: north round the ring
		"4623,4623@2090.83,1774.64,78.23 2094.39,1773.56,78.33",
		"4623,4623@2105.50,1822.47,79.89 2112.36,1822.53,80.54",
		"4623,4623@2116.88,1883.83,71.66 2110.01,1886.77,72.02",
		"4538@2056.44,1920.13,69.47",
		"4538@2062.59,1938.86,64.75",
		"4538,4539@2081.88,1939.52,65.50 2081.03,1945.39,63.94",
		"4538@2078.21,1962.31,63.01",
		"4538@2092.03,1966.71,64.96",
		"4539@2077.04,1981.47,63.46",
		"4427,4519,4427,4625,4625@2067.27,1999.35,64.93 2071.89,2004.52,63.36 2076.73,2009.28,62.52 2063.81,2008.77,64.21 2069.23,2015.30,64.04",
	},
	{ // leg 6 charlga_den: the den east of Agathelos
		"4539@2143.74,2012.88,65.55",
		"4538,4539@2128.13,2000.48,64.27 2131.13,2006.93,64.85",
		"4539,4538@2161.05,1994.36,60.90 2151.72,1988.22,60.00",
		"4538@2188.91,1974.78,55.77",
		"4539,4425@2200.76,1978.19,56.72 2200.80,1978.20,56.70",
		"4623,6035,4440,6035@2156.87,1934.31,65.76 2160.86,1929.84,65.82 2145.96,1931.02,67.29 2161.93,1920.36,66.59",
		"4518@2141.91,1918.87,66.57",
		"4538,4538@2161.92,1961.04,58.56 2164.30,1954.43,58.20",
		"4538@2151.14,1964.97,63.83",
		"4538@2135.60,1978.41,60.17",
		"4539,4538@2104.03,1983.46,64.87 2115.17,1983.69,65.51",
	},
	{ // leg 7 charlga_pit: the descent through the north ring and the west corridor, then the pit
		"4514@2045.97,1564.23,64.79",
		"4514@2067.24,1599.55,62.85",
		"4511,4514@2059.58,1617.29,62.99 2066.60,1614.46,63.15",
		"4511@2065.89,1633.15,64.30",
		"4541@2061.45,1646.19,61.93",
		"4512@2068.10,1660.41,61.25",
		"4514@2080.52,1674.64,61.27",
		"4511@2115.27,1665.87,59.48",
		"4511,4514@2149.51,1685.73,58.41 2149.64,1674.07,58.94",
		"4511@2166.00,1686.35,58.74",
		"4511@2167.05,1701.62,55.06",
		"4514@2152.54,1722.03,52.10",
		"4511@2138.32,1744.36,52.56",
		"4511@2122.66,1738.86,52.81",
		"4512,4511@2069.06,1701.43,57.60 2061.73,1701.93,58.31",
		"4514@2044.24,1709.91,59.72",
		"4514@2032.57,1727.98,60.63",
		"4514,4541@2034.61,1761.75,60.31 2033.69,1758.07,60.36",
		"4511@2046.61,1765.31,59.46",
		"4514@2080.08,1784.59,54.77",
		"4511@2108.24,1802.07,48.24",
		"4511,4541,4514@2122.35,1795.30,47.41 2116.68,1791.75,47.82 2112.07,1785.14,48.39",
		"4511@2137.51,1801.25,47.28",
		"4514@2136.70,1847.94,48.75",
		"4511@2117.48,1851.08,49.42",
		"4511@2091.11,1860.09,51.05",
		"4512,4514,4541@2067.03,1848.41,55.32 2070.68,1850.47,54.85 2070.68,1846.22,55.08",
	},
	{ // leg 8 charlga_razorflank: the climb out of the pit, no pull (its 26 vias are pinned above)
	},
}

// rfkDropped is the exact set of packs the re-order drops: 6 packs, 8 units, all 49-101 yd off every leg's walk (the
// pit's north-west corner and far east wall). Each was in the deployed plan at the leg / pull group named. None is
// woken by the re-ordered rows (trash-check: 0 unplanned body pulls).
var rfkDropped = []struct {
	where string
	pack  string
}{
	{"deployed leg 4 rotthorn pull group 9", "4514,4514@2038.64,1851.96,56.20 2037.19,1861.59,55.96"},
	{"deployed leg 7 charlga_pit pull group 3", "4437@2162.25,1857.61,57.00"},
	{"deployed leg 7 charlga_pit pull group 4", "4437,4437@2161.58,1861.86,56.89 2163.72,1852.09,56.97"},
	{"deployed leg 7 charlga_pit pull group 5", "4517,4437@2193.06,1836.15,60.32 2185.42,1828.12,60.84"},
	{"deployed leg 7 charlga_pit pull group 6", "4437,4437@2195.43,1817.05,62.28 2196.63,1805.74,64.96"},
	{"deployed leg 7 charlga_pit pull group 7", "4437@2184.15,1806.62,64.50"},
}

// TestRazorfenKraulRouteOrder pins the plan's route data and the navmesh walk its exported rows take. It fails on
// origin/dev's plan (122 packs, 17,071.4 yd, 29 backtracks) and on any later edit that puts a pack back in a leg its
// walk does not pass, moves an anchor, or walks ground twice.
func TestRazorfenKraulRouteOrder(t *testing.T) {
	p, err := dungeon.Load(rfkPlan)
	if err != nil {
		t.Fatal(err)
	}
	rfkPlanPins(t, p)

	pl := rfkPlannerOrNil(t)
	if pl == nil {
		// The plan's own data is pinned above; only the walk, its complete paths and the backtracks need the mesh.
		t.Log("no map 47 navmesh files: the walk bound, the per-hop complete-path check and the backtrack pins are not measured (set TURTLE_AGENT_MMAPS to a folder holding 047.mmap and its tiles)")
		return
	}
	defer pl.Close()

	// The rows the live migration walks: plan-export's --floor-aware --approach-hops cut. zgMandokirExportCut is that
	// cutter (the Zul'Gurub Mandokir test's copy of cmd/turtle-agent's floorAwareCut, kept in step with it); the 336
	// rows and 7,727.0 yd below are the route-order audit's own probe numbers.
	res, err := planexport.Build(p, planexport.Options{ApproachHops: true, Cut: zgMandokirExportCut(pl)})
	if err != nil {
		t.Fatal(err)
	}
	var stops []dungeon.Point
	for _, s := range res.Rows.Steps {
		if rfkPartyRows(s.Kind) {
			stops = append(stops, dungeon.Point{X: s.X, Y: s.Y, Z: s.Z})
		}
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
		hop[i] = rfkPath(t, pl, p.Map, prev, s, what)
		straight[i] = math.Hypot(math.Hypot(float64(s.X-prev.X), float64(s.Y-prev.Y)), float64(s.Z-prev.Z))
		prev = s
	}
	var walk float64
	for i := 1; i < len(stops); i++ {
		walk += hop[i]
	}
	t.Logf("razorfen_kraul: %d rows, %d packs, %.1f yd between the exported rows (%.1f with the entrance hop), %.1f yd straight",
		len(stops), rfkPacks(p), walk, walk+hop[0], rfkStraight(p))
	if walk > rfkMaxWalkYd {
		t.Errorf("razorfen_kraul walks %.1f yd between the exported rows (cap %.0f; the re-order measures 7,727.0, origin/dev 17,071.4): a pack back in a leg its walk does not pass, an anchor moved off the route or a detour added shows up here", walk, rfkMaxWalkYd)
	}
	n, yd := rfkBacktracks(stops, hop, straight)
	t.Logf("razorfen_kraul: %d backtracks, %.1f yd re-walked (audit: 13 / 3,056; origin/dev: 29 / 10,563.5 under this metric, 30 / 10,545 in the audit)", n, yd)
	if n != rfkBacktrackPins {
		t.Errorf("razorfen_kraul has %d backtracks (%.1f yd), want %d (the 2026-11-14 route order: each remaining one is the way out of a boss's or the pit's dead end)", n, yd, rfkBacktrackPins)
	}
	if yd > rfkMaxBacktrackYd {
		t.Errorf("razorfen_kraul re-walks %.1f yd, cap %.0f (measured 3,056.0): the order walks ground twice beyond the dead ends", yd, rfkMaxBacktrackYd)
	}
}

// rfkPlanPins checks the plan's own data: the boss order, each leg's anchor and vias, its packs, and the six dropped
// packs. It needs no navmesh.
func rfkPlanPins(t *testing.T, p *dungeon.Plan) {
	t.Helper()
	if len(p.Legs) != len(rfkLegAnchors) {
		t.Fatalf("%d legs, want %d (the boss order's legs)", len(p.Legs), len(rfkLegAnchors))
	}
	var bosses []uint32
	for _, l := range p.Legs {
		if l.Boss == nil {
			continue
		}
		bosses = append(bosses, l.Boss.Entry)
	}
	if !slices.Equal(bosses, rfkBossOrder) {
		t.Errorf("boss order %v, want %v (Roogug, Aggem Thorncurse, Death Speaker Jargba, Overlord Ramtusk, Rotthorn, Agathelos the Raging, Charlga Razorflank)", bosses, rfkBossOrder)
	}
	for i, l := range p.Legs {
		want := rfkLegAnchors[i]
		if got := rfkPointText(l.To); got != want.to {
			t.Errorf("leg %d (%s) to %s, want %s: the anchors are the pull-group positions the re-order walks first (the report says which)", i, l.Name, got, want.to)
		}
		vias := make([]string, len(l.Via))
		for j, w := range l.Via {
			vias[j] = rfkPointText(w.Point)
		}
		if !slices.Equal(vias, want.vias) {
			t.Errorf("leg %d (%s) vias %v, want %v: leg 7 keeps the one west-corridor via, leg 8 the deployed 26-via climb", i, l.Name, vias, want.vias)
		}
		packs := make([]string, len(l.PullGroups))
		for j, g := range l.PullGroups {
			packs[j] = rfkPackText(g)
		}
		if want := rfkLegPacks[i]; !slices.Equal(packs, want) {
			t.Errorf("leg %d (%s) packs\n got  %v\n want %v\n(the 2026-11-14 order: each leg holds the packs its walk passes, in walk order)", i, l.Name, packs, want)
		}
	}
	retained := map[string]bool{}
	for _, l := range p.Legs {
		for _, g := range l.PullGroups {
			retained[rfkPackText(g)] = true
		}
	}
	if len(retained) != 116 {
		t.Errorf("%d distinct packs, want 116 (the six dropped ones below excluded)", len(retained))
	}
	if len(retained)+len(rfkDropped) != rfkDeployedPacks {
		t.Errorf("%d retained + %d dropped packs, want the deployed %d", len(retained), len(rfkDropped), rfkDeployedPacks)
	}
	for _, d := range rfkDropped {
		if retained[d.pack] {
			t.Errorf("dropped pack %s (%s) is back in the plan: the walk passes it 49-101 yd off the route, so the deployed set it is the re-order's own drop", d.pack, d.where)
		}
	}
}

// rfkPacks counts the plan's pull groups.
func rfkPacks(p *dungeon.Plan) int {
	n := 0
	for _, l := range p.Legs {
		n += len(l.PullGroups)
	}
	return n
}

// rfkStraight is the plan's straight-line length, the floor the audit compares the walk to: the exported rows' party
// points, hop by hop.
func rfkStraight(p *dungeon.Plan) float64 {
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

// rfkPointText is a point at the audit's precision.
func rfkPointText(p dungeon.Point) string {
	return fmt.Sprintf("%.2f,%.2f,%.2f", p.X, p.Y, p.Z)
}

// rfkPackText is a pack's identity: its entries (in plan order, multiplicity kept) and every position.
func rfkPackText(g dungeon.PullGroup) string {
	entries := make([]string, len(g.Entries))
	for i, e := range g.Entries {
		entries[i] = fmt.Sprint(e)
	}
	pts := make([]string, len(g.Positions))
	for i, p := range g.Positions {
		pts[i] = rfkPointText(p)
	}
	return strings.Join(entries, ",") + "@" + strings.Join(pts, " ")
}

// rfkPartyRows are the rows that put the party somewhere: an interaction row (open, loot) is not a waypoint.
func rfkPartyRows(kind string) bool {
	switch kind {
	case planexport.KindMove, planexport.KindMoveDirect, planexport.KindMoveHopExact, planexport.KindPull, planexport.KindBoss, planexport.KindHold:
		return true
	}
	return false
}

// rfkPlannerOrNil is the map-47 navmesh when the machine has the server's mmaps, nil otherwise.
func rfkPlannerOrNil(t *testing.T) *navmesh.Planner {
	t.Helper()
	home, _ := os.UserHomeDir()
	for _, dir := range []string{os.Getenv("TURTLE_AGENT_MMAPS"), filepath.Join(home, "turtle-agent-mmaps")} {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "047.mmap")); err != nil {
			continue
		}
		if tiles, _ := filepath.Glob(filepath.Join(dir, "047*.mmtile")); len(tiles) == 0 {
			continue
		}
		return navmesh.NewPlanner(dir)
	}
	return nil
}

// rfkPath is the navmesh path a->b; a missing or partial path fails the test, it is not zero.
func rfkPath(t *testing.T, pl *navmesh.Planner, mapID uint32, a, b dungeon.Point, what string) float64 {
	t.Helper()
	path, err := pl.Plan(mapID, navmesh.Vec3{a.X, a.Y, a.Z}, navmesh.Vec3{b.X, b.Y, b.Z})
	if err != nil || path.Status != navmesh.Complete {
		t.Errorf("%s: no complete navmesh path %.1f %.1f %.1f -> %.1f %.1f %.1f: %v (%s %s)",
			what, a.X, a.Y, a.Z, b.X, b.Y, b.Z, err, path.Status, path.Reason)
		return 0
	}
	return float64(path.Length)
}

// rfkBacktracks counts the audit's backtracks on the walk: samples every 4 yd along the straight line between rows,
// cumulative length the navmesh hop; a revisit is within 10 yd (5 yd in height) of a sample at least 60 yd of route
// earlier; a run of revisits at least 40 yd long (gaps of at most 15 yd joined) is a backtrack, and its yards are the
// run's own. hop and straight are the navmesh and straight lengths of each row hop, entrance first.
func rfkBacktracks(stops []dungeon.Point, hop, straight []float64) (int, float64) {
	type sample struct {
		x, y, z, cum float64
	}
	samples := make([]sample, 0, len(stops)*8)
	samples = append(samples, sample{float64(stops[0].X), float64(stops[0].Y), float64(stops[0].Z), hop[0]})
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
			})
		}
		cum += hop[i]
		samples = append(samples, sample{float64(b.X), float64(b.Y), float64(b.Z), cum})
	}
	type cell struct{ x, y int }
	grid := map[cell][]int{}
	revisit := make([]bool, len(samples))
	for i, q := range samples {
		gx, gy := int(math.Floor(q.x/10)), int(math.Floor(q.y/10))
		for dx := -1; dx <= 1; dx++ {
			for dy := -1; dy <= 1; dy++ {
				for _, j := range grid[cell{gx + dx, gy + dy}] {
					o := samples[j]
					if q.cum-o.cum >= 60 && math.Abs(o.z-q.z) <= 5 &&
						(o.x-q.x)*(o.x-q.x)+(o.y-q.y)*(o.y-q.y) <= 100 {
						revisit[i] = true
					}
				}
			}
		}
		grid[cell{gx, gy}] = append(grid[cell{gx, gy}], i)
	}
	n, yd := 0, 0.0
	start, end := 0.0, 0.0
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
			n++
			yd += end - start
		}
		start, end, inRun = samples[i].cum, samples[i].cum, true
	}
	if inRun && end-start >= 40 {
		n++
		yd += end - start
	}
	return n, yd
}
