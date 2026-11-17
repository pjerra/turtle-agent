// The route order of maraudon_purple (Sagiroth/TortoiseBots#1542, 2026-11-14): each pack sits on the leg whose walk
// passes it, in the order along that walk, and the packs no leg passes within aggro reach are gone. This test pins the
// order itself, not just its size: the six bosses and their order, every retained pack (its entries and first
// position) on its leg in plan order, the exact set of the 35 dropped packs, the five moved leg anchors, and the walk
// the exported rows take on the map 349 navmesh (one complete path per consecutive party row). The ledge under the
// ring is walked because the deck above the -62 pack at 771 -353 wakes it; its four neighbours the detour passes come
// with it. The measurements and the per-pack tables are in the route-order audit
// (~/TortoiseBots-audit/tests/route-audit-1112/order/maraudon_purple-FIX.md, 2026-11-15).
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

const (
	// maraudonPlan is the plan under test.
	maraudonPlan = "../../dungeons/maraudon_purple.json"

	// maraudonRouteYd caps the routed walk: 9,354.8 yd measured here, the sum of a complete navmesh path between each
	// consecutive party row of the export (the route-order audit measured 9,371.4 yd on its floor-aware export of the
	// same plan, against a 7,168 yd straight route). Room for a cut point that moves, not for a detour: origin/dev's
	// plan exports 35,441.3 yd here (36,333 on its deployed rows).
	maraudonRouteYd = 9600.0
)

// maraudonBosses are the bosses of the six legs, in leg order: lord_vyletongue, celebras_the_cursed, landslide,
// tinkerer_gizlock, rotgrip, princess_theradras. The re-order keeps them and their order.
var maraudonBosses = []struct {
	entry uint32
	at    string
}{
	{12236, "748.87 -219.65 -47.69"},
	{12225, "726.11 77.98 -86.59"},
	{12203, "356.68 -185.46 -59.82"},
	{13601, "134.90 -313.73 -173.55"},
	{13596, "42.08 -65.95 -199.55"},
	{12201, "27.90 83.19 -124.48"},
}

// maraudonAnchors are the legs' `to` points. Leg 0 keeps the deployed point; legs 1-5 moved to the deployed plan's
// own first path row after the previous boss (20261105130000_world.sql steps 425, 531, 687, 990, 1135), so no leg
// walks to a point inside itself and back.
var maraudonAnchors = []string{
	"758.29 -559.52 -33.05",  // lord_vyletongue (unchanged)
	"709.20 -211.50 -47.10",  // celebras_the_cursed
	"659.70 78.10 -87.30",    // landslide
	"302.60 -174.70 -59.80",  // tinkerer_gizlock
	"166.90 -308.10 -172.50", // rotgrip
	"-21.70 -432.90 -195.30", // princess_theradras
}

// maraudonLegs are the packs of each leg in plan order: the walk's own order along that leg. A pack is its entries and
// its first position (see maraudonPackID).
var maraudonLegs = [][]string{
	{ // lord_vyletongue
		"12216,12217,12217,12216@758.29 -559.52 -33.05",
		"11790,11790@745.34 -505.86 -41.32",
		"12217,12216,12216@716.34 -502.58 -36.37",
		"13141@743.32 -468.30 -38.98",
		"12216,12217,11790,12217@763.24 -466.77 -40.90",
		"12216,12216,12216,11790,12217@801.90 -492.69 -39.63",
		"12216,11790,12217,11790,12216@762.21 -522.24 -53.04",
		"13141@830.02 -448.15 -56.25",
		"13141@880.45 -402.98 -51.93",
		"12217,12216,12216,11790,12216,11790@893.26 -406.59 -52.58",
		"13141@880.11 -367.95 -51.97",
		"11792@873.72 -352.43 -51.35",
		"12216,12217,12216,11791,12216,11790@876.26 -337.36 -50.21",
		"12217,12216,11790,12217,11791,12217@830.12 -354.75 -51.97",
		"12217,12216,12216,11791,12216,11790@797.70 -341.59 -51.52",
		"13141@855.32 -382.35 -64.00",
		"12216,11790,12217,12216,11790@791.65 -424.88 -53.70",
		"13141@816.50 -376.74 -59.10",
		"13141@796.36 -373.90 -59.07",
		"13142@771.38 -352.87 -61.57",
		"11791,12217,12216,12217,12216,11790@735.15 -342.54 -50.71",
		"13142@697.09 -372.95 -51.94",
		"11790,12216,12217,12216,11791,11792@674.10 -344.02 -51.55",
		"13142@650.52 -349.33 -51.94",
		"12217,12216,11791,12217,11791@638.00 -308.51 -51.94",
		"13142@639.61 -261.65 -52.78",
		"13141@627.64 -260.40 -53.24",
		"13142@614.71 -206.54 -64.30",
		"13142@656.93 -227.15 -64.31",
		"13141@647.04 -201.60 -64.31",
		"11791,12217,11791,12217,12217,12216@681.37 -191.21 -60.41",
		"13142@692.31 -265.62 -52.81",
		"12216,12217,12216,11791,11791,12216@704.13 -196.93 -47.17",
		"11792,11792@721.57 -211.07 -47.17",
	},
	{ // celebras_the_cursed
		"13142@688.89 -164.71 -48.67",
		"13141@669.35 -152.99 -50.10",
		"13141,13142@633.69 -167.71 -53.59",
		"11791,11791@597.29 -159.13 -55.11",
		"12222,12222@619.28 -131.68 -54.86",
		"12223@617.37 -99.96 -57.42",
		"12222,12222,12222@676.17 -110.75 -55.96",
		"12223@699.16 -89.09 -57.50",
		"12223,12223@740.26 -82.08 -57.42",
		"12216,11791,11791,12216,12217,12223@736.77 -115.00 -56.44",
		"12222,12222,12222,12222@778.73 -145.50 -56.00",
		"12221,12221@763.30 -187.65 -59.70",
		"12221,12221,12221,12221,12221@786.95 -216.83 -64.96",
		"12224@804.20 -200.60 -77.07",
		"12221,12221,12221,12221,12221@837.41 -199.51 -75.10",
		"12219@878.77 -201.89 -75.12",
		"12223@840.59 -174.88 -88.26",
		"11794,11794@874.34 -156.53 -87.20",
		"12224@840.88 -148.74 -88.26",
		"12221,12221,12221,12221@868.95 -133.92 -86.98",
		"12223@848.98 -113.38 -88.59",
		"11794,11794,11794,11794@864.10 -88.35 -87.41",
		"12223@831.27 -94.13 -88.68",
		"12224@812.64 -37.27 -88.20",
		"12221,12221,12221,12221,12221@830.39 -19.80 -86.89",
	},
	{ // landslide
		"13599,13599@561.54 106.60 -96.23",
		"13599,13599,13599,13599@564.10 66.13 -96.31",
		"13599@575.90 40.60 -96.23",
		"13599@557.38 14.81 -96.23",
		"13599@501.70 15.98 -96.23",
		"13599@489.51 70.77 -96.23",
		"11789,11789,11789,11789,11789@452.31 222.89 -94.05",
		"11789,11789,11789,11789,11789,11789@349.45 199.30 -101.85",
		"11789,11789,11789,11789,11789@434.63 176.45 -113.25",
		"12207@309.63 97.90 -131.75",
		"12207@277.12 41.99 -131.01",
		"12207@284.44 25.42 -131.01",
		"12207@248.74 -8.42 -131.01",
		"13323,13323,13323@227.78 -93.71 -129.53",
		"11783,11783,11783,11783,11784@262.97 -97.57 -129.62",
		"13323,13323,13323@225.37 -173.71 -131.12",
		"13323,13323,13323@225.21 -223.85 -131.03",
		"13323,13323,13323@237.79 -263.62 -131.06",
		"11783,11783,11783,11784,11783@222.56 -191.69 -108.88",
		"12206@171.47 -190.02 -110.90",
		"12206@242.46 -105.55 -87.15",
		"12206@297.20 -170.81 -60.00",
		"12206,12206@290.09 -203.26 -59.82",
	},
	{ // tinkerer_gizlock
		"11789,11789,11789,11789,11789,11789@328.71 -262.81 -118.05",
		"11784,11783,11783,11783,11783@315.65 -333.17 -116.84",
		"11789,11789,11789,11789,11789,11789@324.88 -387.97 -124.78",
		"13323,13323,13323,13323@251.93 -389.68 -139.47",
		"11789,11789,11789,11789,11789@250.87 -322.73 -140.62",
		"13323,13323,13323,13323@216.35 -371.05 -160.23",
		"13323,13323,13323,13323@144.84 -363.08 -174.92",
	},
	{ // rotgrip
		"11789,11789,11789,11789,11789,11783,11783,11784,11783,11783@180.16 -305.20 -172.46",
		"11789,11789,11789,11789,11789@128.19 -267.30 -168.44",
		"11789,11789,11789,11789,11789,11789@169.20 -184.76 -171.79",
		"11789,11789,11789,11789,11789,11789@108.67 -187.77 -167.37",
		"12206@76.72 -168.97 -169.87",
		"11783,11783,11783,11783,11784@70.61 -182.68 -169.95",
		"12206@37.73 -254.96 -169.68",
		"11789,11789,11789,11789,11789@-93.29 -252.80 -170.34",
		"11789,11789,11789,11789,11789,11789@-99.70 -312.57 -170.33",
		"12207@15.42 -310.70 -206.93",
		"12207@-4.79 -238.57 -208.68",
		"12207@54.78 -133.37 -204.78",
	},
	{ // princess_theradras
		"13323,13323,13323,13323@150.39 -240.11 -108.77",
		"11784,11784@125.26 -273.10 -108.68",
		"12206,12206@73.01 -197.63 -109.86",
		"12206,12206@-0.30 -119.22 -123.90",
		"12206,12206@21.30 -34.19 -127.81",
	},
}

// maraudonDropped are the 35 packs the re-order drops (74 units): the trash-check of the new rows wakes none of them.
// They are origin/dev's 141 packs minus the 106 retained; none may come back without a leg that walks to it. The
// number is the pack's origin/dev leg and pull group.
var maraudonDropped = []string{
	"12220,12220,12220@889.73 -282.18 -45.84",                  // dev leg 0 group 18
	"12220,12220,12220@859.51 -250.14 -52.67",                  // dev leg 0 group 19
	"12219,12219@852.39 -234.65 -54.56",                        // dev leg 0 group 20
	"12220,12220,12220@874.74 -231.43 -53.43",                  // dev leg 0 group 21
	"12220,12220,12219,12220@893.47 -192.02 -52.51",            // dev leg 0 group 23
	"12220,12220,12219,12220@880.28 -60.39 -59.18",             // dev leg 0 group 29
	"11794,11794@793.15 -160.88 -74.38",                        // dev leg 0 group 36
	"12221,12221,12221,12221@809.47 -257.05 -60.35",            // dev leg 0 group 62
	"12221,12221@817.91 -237.51 -74.52",                        // dev leg 0 group 63
	"11791,11791@616.53 -392.27 -51.94",                        // dev leg 0 group 66
	"11792@607.02 -380.38 -52.02",                              // dev leg 0 group 67
	"13142,13141@617.45 -354.99 -52.02",                        // dev leg 0 group 68
	"11791,11791@580.81 -384.17 -51.94",                        // dev leg 0 group 69
	"11791,11790@594.65 -399.88 -51.94",                        // dev leg 0 group 70
	"12217,12217,12217,11791,11791,12216@755.62 -53.83 -56.11", // dev leg 1 group 0
	"11794,11793,11794,11793@810.53 75.43 -86.46",              // dev leg 1 group 3
	"13599@618.18 27.98 -96.23",                                // dev leg 1 group 4
	"13599@594.19 -0.67 -96.23",                                // dev leg 1 group 5
	"11789,11789,11789,11789,11789@513.20 208.44 -93.97",       // dev leg 1 group 14
	"13599@498.23 -33.25 -96.31",                               // dev leg 2 group 0
	"13599,13599,13599,13599,13599@481.63 -42.24 -96.31",       // dev leg 2 group 1
	"12207@275.03 -58.95 -131.01",                              // dev leg 2 group 5
	"13323,13323,13323@267.63 -159.27 -110.86",                 // dev leg 2 group 9
	"12207@332.75 -90.21 -131.01",                              // dev leg 2 group 14
	"12207@-86.99 -397.82 -189.89",                             // dev leg 3 group 18
	"12206,12206@59.57 -293.16 -107.86",                        // dev leg 3 group 19
	"12207@-26.19 -87.24 -206.45",                              // dev leg 4 group 4
	"13599@84.99 7.09 -131.07",                                 // dev leg 5 group 0
	"13599@101.29 47.04 -131.07",                               // dev leg 5 group 1
	"13599@90.55 84.81 -131.07",                                // dev leg 5 group 2
	"13599@54.83 113.88 -131.07",                               // dev leg 5 group 3
	"13599@13.72 117.55 -131.07",                               // dev leg 5 group 4
	"13599@-18.80 96.24 -131.07",                               // dev leg 5 group 5
	"13599@-44.63 58.08 -131.07",                               // dev leg 5 group 6
	"13599@-29.19 9.89 -131.12",                                // dev leg 5 group 7
}

// TestMaraudonPurpleRouteOrder pins the order: bosses, each leg's packs in plan order, the dropped set and the moved
// anchors on every run, and the walk when the machine has the map 349 navmesh. It fails on origin/dev's plan (141
// packs: 71/15/17/21/7/10 per leg, the old anchors, 35,441.3 yd here; 36,333 on its deployed rows).
func TestMaraudonPurpleRouteOrder(t *testing.T) {
	p, err := dungeon.Load(maraudonPlan)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Legs) != len(maraudonBosses) {
		t.Fatalf("%d legs, want %d (lord_vyletongue, celebras_the_cursed, landslide, tinkerer_gizlock, rotgrip, princess_theradras)",
			len(p.Legs), len(maraudonBosses))
	}

	// The bosses, in leg order.
	for i, want := range maraudonBosses {
		leg := p.Legs[i]
		if leg.Boss == nil {
			t.Errorf("leg %d (%s) has no boss: the boss order is part of the route", i, leg.Name)
			continue
		}
		if leg.Boss.Entry != want.entry {
			t.Errorf("leg %d (%s) boss entry %d, want %d", i, leg.Name, leg.Boss.Entry, want.entry)
		}
		if got := maraudonAt(leg.Boss.Position); got != want.at {
			t.Errorf("leg %d (%s) boss %d at %s, want %s", i, leg.Name, leg.Boss.Entry, got, want.at)
		}
	}

	// The moved anchors.
	for i, leg := range p.Legs {
		if got := maraudonAt(leg.To); got != maraudonAnchors[i] {
			t.Errorf("leg %d (%s) `to` is %s, want %s", i, leg.Name, got, maraudonAnchors[i])
		}
	}

	// Every retained pack, on its leg, in walking order.
	for i, leg := range p.Legs {
		want := maraudonLegs[i]
		got := maraudonPackIDs(leg.PullGroups)
		if len(got) != len(want) {
			t.Errorf("leg %d (%s): %d packs, want %d (the route order of 2026-11-14)", i, leg.Name, len(got), len(want))
			continue
		}
		for j := range want {
			if got[j] != want[j] {
				t.Errorf("leg %d (%s) pack %d: %s, want %s", i, leg.Name, j, got[j], want[j])
			}
		}
	}

	// The dropped set: none of the 40 is back, and the pin is the whole set.
	kept := map[string]bool{}
	for _, leg := range p.Legs {
		for _, id := range maraudonPackIDs(leg.PullGroups) {
			kept[id] = true
		}
	}
	if len(maraudonDropped) != 35 {
		t.Fatalf("the dropped pin is %d packs, want 35", len(maraudonDropped))
	}
	pinned := map[string]bool{}
	for _, id := range maraudonDropped {
		if pinned[id] {
			t.Fatalf("the dropped pin lists %s twice", id)
		}
		pinned[id] = true
		if kept[id] {
			t.Errorf("the dropped pack %s is in the plan: no leg's walk comes within its aggro reach, so it would be pulled from a detour", id)
		}
	}

	pl := maraudonPlannerOrNil(t)
	if pl == nil {
		t.Logf("maraudon_purple: %d packs pinned; no map 349 navmesh files, so the walk is not measured", len(kept))
		return
	}
	defer pl.Close()

	res, err := planexport.Build(p, planexport.Options{})
	if err != nil {
		t.Fatalf("maraudon_purple does not export without a cutter: %v", err)
	}
	var stops []dungeon.Point
	var names []string
	for _, s := range res.Rows.Steps {
		if maraudonPartyRow(s.Kind) {
			stops = append(stops, dungeon.Point{X: s.X, Y: s.Y, Z: s.Z})
			names = append(names, fmt.Sprintf("leg %d seq %d (%s)", s.Leg, s.Seq, s.Kind))
		}
	}
	if len(stops) < 2 {
		t.Fatalf("%d party rows, want the walk", len(stops))
	}
	total := maraudonPath(t, pl, p.Map, p.Entrance, stops[0], "entrance")
	for i := 1; i < len(stops); i++ {
		total += maraudonPath(t, pl, p.Map, stops[i-1], stops[i], names[i])
	}
	t.Logf("maraudon_purple walk %.1f yd on the navmesh over %d hops (%d packs; the route-order audit measured 9,371.4 yd; origin/dev exports 35,441.3 here)",
		total, len(stops), len(kept))
	if total > maraudonRouteYd {
		t.Errorf("the walk is %.1f yd (cap %.0f, the route order of 2026-11-14: 9,354.8 here, 9,371.4 on the audit's export; origin/dev exports 35,441.3 here, 36,333 on its deployed rows): a leg walks away from the packs on its way",
			total, maraudonRouteYd)
	}
}

// maraudonPackID is a pack's identity: its entries and its first position (the point the run leader walks to) to
// 0.01 yd.
func maraudonPackID(g dungeon.PullGroup) string {
	if len(g.Positions) == 0 {
		return fmt.Sprint(g.Entries) + "@no position"
	}
	id := ""
	for i, e := range g.Entries {
		if i > 0 {
			id += ","
		}
		id += fmt.Sprint(e)
	}
	return id + "@" + maraudonAt(g.Positions[0])
}

// maraudonPackIDs is a leg's packs as identities, in plan order.
func maraudonPackIDs(groups []dungeon.PullGroup) []string {
	out := make([]string, len(groups))
	for i, g := range groups {
		out[i] = maraudonPackID(g)
	}
	return out
}

// maraudonAt writes a point the way the pins above read it.
func maraudonAt(p dungeon.Point) string {
	return fmt.Sprintf("%.2f %.2f %.2f", p.X, p.Y, p.Z)
}

// maraudonPartyRow says whether a row puts the party somewhere: an interaction row (open, loot, gossip) is not a
// waypoint of the walk.
func maraudonPartyRow(kind string) bool {
	switch kind {
	case planexport.KindMove, planexport.KindMoveDirect, planexport.KindMoveHopExact, planexport.KindPull, planexport.KindBoss, planexport.KindHold:
		return true
	}
	return false
}

// maraudonPlannerOrNil is the map 349 navmesh when the machine has the server's mmaps ($TURTLE_AGENT_MMAPS or
// ~/turtle-agent-mmaps), nil otherwise.
func maraudonPlannerOrNil(t *testing.T) *navmesh.Planner {
	t.Helper()
	home, _ := os.UserHomeDir()
	for _, dir := range []string{os.Getenv("TURTLE_AGENT_MMAPS"), filepath.Join(home, "turtle-agent-mmaps")} {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "349.mmap")); err != nil {
			continue
		}
		if tiles, _ := filepath.Glob(filepath.Join(dir, "349*.mmtile")); len(tiles) == 0 {
			continue
		}
		return navmesh.NewPlanner(dir)
	}
	return nil
}

// maraudonPath is the navmesh path a->b; a missing or partial path fails the test, it is not zero.
func maraudonPath(t *testing.T, pl *navmesh.Planner, mapID uint32, a, b dungeon.Point, what string) float64 {
	t.Helper()
	path, err := pl.Plan(mapID, navmesh.Vec3{a.X, a.Y, a.Z}, navmesh.Vec3{b.X, b.Y, b.Z})
	if err != nil || path.Status != navmesh.Complete {
		t.Errorf("%s: no complete navmesh path %.1f %.1f %.1f -> %.1f %.1f %.1f: %v (%s %s)",
			what, a.X, a.Y, a.Z, b.X, b.Y, b.Z, err, path.Status, path.Reason)
		return 0
	}
	return float64(path.Length)
}
