package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

// Dropped packs: one shared check for every route-order plan.
//
// A route-order PR (Gnomeregan #471, LBRS #466, B6a #467, Kraul #468, Maraudon #470, and the Stratholme Undead, Dire Maul
// West and Karazhan Crypt branches) rewrites a plan's route and drops the packs the new order walks past. Every other
// route lint in this package counts the packs a row does pull (RouteIntoLaterPacks) or the order's own rows
// (Test<Plan>Order); none of them notices when the new walk still comes within aggro of a pack the order drops, because
// a dropped pack is owned by no row. Only the trash-check of the audit's route report saw it, by hand.
//
// So a route-order branch keeps two things: the dropped set as data, and one test that hands it to droppedPacksAsleep:
//
//	// gnomerDropped are the 89 packs the short route leaves out, by the centre the route audit lists for each.
//	var gnomerDropped = packsAt([][3]float64{
//		{-532, 165, -155}, // ...
//	})
//
//	// TestGnomereganDroppedPacksAsleep walks the short route against them (its passes are pinned in droppedPackKnownPasses).
//	func TestGnomereganDroppedPacksAsleep(t *testing.T) {
//		p, err := dungeon.Load("../../dungeons/gnomeregan.json")
//		if err != nil {
//			t.Fatal(err)
//		}
//		droppedPacksAsleep(t, p, gnomerDropped, "")
//	}
//
// The record is the audit's dropped list (the order's candidate/<plan>.changes.json, or the <PLAN>-FIX.md table): one
// PackRef per dropped pack, at a point inside it (the pack's first position, the centre the audit prints). A dropped
// pack carries no entries in the plan any more, so the centre is what is left of it: droppedPacksAsleep finds the pack's
// spawns in the map's export under tools/dungeongen/testdata/map<id> (the data the trash check reads) as the hostile,
// NO_AGGRO-free spawns within its radius of the centre that no plan row pulls, plus their formation (group_leader). The
// radius is the driver's 25 yd pack rule unless the record sets a smaller one, which is needed only where the plan's own
// packs stand inside it (a dropped pack next to a kept one). A centre that resolves to no spawn fails: the record went
// stale, or its radius is wrong, and a pack that is silently empty would make the test vacuous.
//
// The walk is the plan's live rows (routePoints: the export with DropCompanionsLoot, CarrierChests and GMCrossDirect,
// and a stand pull fought from its stand, as RouteIntoLaterPacks measures them) and the server's own navmesh path per
// hop (Planner.Plan), with the entrance's hop to the first row included. Every segment of every polyline is sampled
// every 2 yd. A sample within a dropped spawn's aggro reach in 3D is a pass: worldspawn.AggroRadius (the core's rule, at
// the plan's lowest level, level_band[0]) plus the spawn's wander, plus the party's 5 yd spread.
//
// A pass is not always a defect a plan can fix (a corridor's lower deck 13 yd under the walk is still outside a
// navmesh's top-down reach), so the plan's record pins how many passes its walk makes (droppedPackKnownPasses, the way
// routeAggroKnownFailing pins every hand-maintained plan's later-pack passes): the test fails on one more, on one fewer
// and on a plan with no line at all, and prints the pass lines to re-pin. A route-order branch adds its plan with the
// number its own audit measured and took (0 when its walk keeps every dropped pack asleep), and the plan test:
//
//	// TestGnomereganDroppedPacksAsleep walks the short route against the packs its order drops.
//	func TestGnomereganDroppedPacksAsleep(t *testing.T) {
//		p, err := dungeon.Load("../../dungeons/gnomeregan.json")
//		if err != nil {
//			t.Fatal(err)
//		}
//		droppedPacksAsleep(t, p, gnomerDropped, "")
//	}
//
// There is no line of sight in this repo: the exports carry no vmap geometry, and the trash check reports the same limit
// ("no LOS: through-wall aggro is over-counted", internal/trashcheck/check.go), so a wall between the route and the pack
// is not modelled: the check over-counts, and a route that still passes the pinned count is clear in the strict sense.
//
// Without the server's navmesh (the mmaps argument, $TURTLE_AGENT_MMAPS, ~/turtle-agent-mmaps or the build VM's
// ~/tb-ci/mmaps-turtle<map>) the check skips: RouteIntoLaterPacks' straight segments are not the walked path, and a walk
// that only looks straight would miss a dropped pack the navmesh path rounds into. TURTLE_AGENT_MMAPS_REQUIRED=1 turns
// that skip into a failure, for a machine that carries the mmaps and must not pass quietly without them.

const (
	// droppedSampleStep is how finely a navmesh path segment is sampled, in yards: the same 2 yd RouteIntoLaterPacks uses.
	droppedSampleStep = 2
	// droppedSpreadYd is the party's spread added to a dropped pack's aggro radius, in yards.
	droppedSpreadYd = 5
	// droppedMmapsEnv names the folder with the server's mmaps, as every other real-mesh test reads it.
	droppedMmapsEnv = "TURTLE_AGENT_MMAPS"
	// droppedMmapsRequiredEnv makes a missing navmesh a failure instead of a skip: a machine that must not pass without
	// the mesh (PR #467's variable) sets it.
	droppedMmapsRequiredEnv = "TURTLE_AGENT_MMAPS_REQUIRED"
)

// PackRef is one dropped pack of a route-order plan: a point inside it (the centre the route audit lists), the radius
// that holds its spawns (0: the driver's 25 yd PackRadius) and an optional name for the failure lines.
type PackRef struct {
	Name    string
	X, Y, Z float64
	Radius  float64
}

// Centre is the pack's recorded point.
func (p PackRef) Centre() dungeon.Point {
	return dungeon.Point{X: float32(p.X), Y: float32(p.Y), Z: float32(p.Z)}
}

// radius is the yards around Centre that hold the pack's spawns.
func (p PackRef) radius() float64 {
	if p.Radius <= 0 {
		return defaultPackRadius
	}
	return p.Radius
}

// label names the pack in a failure line: its name when the record has one, else its centre.
func (p PackRef) label() string {
	if p.Name != "" {
		return p.Name
	}
	return fmt.Sprintf("%.0f %.0f %.0f", p.X, p.Y, p.Z)
}

// packsAt is the compact record form: the centres a route audit lists, in its (x, y, z) order, one PackRef each.
func packsAt(centres [][3]float64) []PackRef {
	out := make([]PackRef, 0, len(centres))
	for _, c := range centres {
		out = append(out, PackRef{X: c[0], Y: c[1], Z: c[2]})
	}
	return out
}

// droppedPass is the closest a walked hop comes to one spawn of a dropped pack.
type droppedPass struct {
	pack   int
	spawn  creature
	reach  float64
	aggro  float64
	wander float64
	dist   float64
	to     dungeon.Point
	row    string
}

// line is the pass as the failure list and the pin print it.
func (f droppedPass) line(plan, pack string) string {
	return fmt.Sprintf("%s: the walk passes %.1f yd from the dropped pack %s: %s, guid %d, entry %d, reach %.1f yd (aggro %.1f + spread %d + wander %.1f), on the hop to %.0f %.0f %.0f (%s)",
		plan, f.dist, pack, f.spawn.Name, f.spawn.GUID, f.spawn.Entry, f.reach, f.aggro, droppedSpreadYd, f.wander,
		f.to.X, f.to.Y, f.to.Z, f.row)
}

// droppedPackKnownPasses is, per plan, how many of its dropped packs' spawns its walk wakes today: the recorded state of
// a route-order plan, as routeAggroKnownFailing records every hand-maintained plan's later-pack passes. A plan in here
// is a plan whose passes were measured and taken; one more or one fewer pass is a failure, and a plan with no line here
// is a failure too, so a route-order branch cannot land without its record.
var droppedPackKnownPasses = map[string]int{
	// 48 on origin/dev's merge of #471 (measured 2026-11-15): the lower deck under Thermaplugg's last corridor (its
	// 6212/6234/6232 packs at z -304, 13 yd below the corridor's -291; the export has no line of sight, so the walk over
	// them counts) plus the Launch Bay's lower floor. They are the route fix's open live-proof item, "The lower deck under
	// the last corridor: 9 units 16.6-19.6 yd (3D) from it" (route-audit-1112/order/GNOMEREGAN-FIX.md): at a 27 party
	// the tool gives those packs 31-34 yd of aggro, and only a live run can say whether the floor keeps them asleep.
	"gnomeregan": 48,
}

// droppedPacksAsleep walks the plan on the server's navmesh and fails when it wakes a dropped pack the plan's record does
// not list. plan is the loaded plan (the export comes from tools/dungeongen/testdata/map<plan.Map>), dropped its dropped
// set, and mmaps a folder with the server's mmaps for the plan's map ("" to use $TURTLE_AGENT_MMAPS, ~/turtle-agent-mmaps
// or the build VM's ~/tb-ci/mmaps-turtle<map>). See the file comment for what the walk, the pack's spawns and the reach
// are.
func droppedPacksAsleep(t *testing.T, plan *dungeon.Plan, dropped []PackRef, mmaps string) {
	t.Helper()
	passes, walked, samples, spawns := droppedPasses(t, plan, dropped, mmaps)
	lines := make([]string, 0, len(passes))
	for _, f := range passes {
		lines = append(lines, f.line(plan.Name, dropped[f.pack].label()))
	}
	known, pinned := droppedPackKnownPasses[plan.Name]
	if !pinned {
		t.Fatalf("%s: no dropped-pack record: add %q: %d to droppedPackKnownPasses with the passes this walk makes (0 is the aim; any number is a measured, taken pass):\n  %s",
			plan.Name, plan.Name, len(passes), strings.Join(lines, "\n  "))
	}
	t.Logf("%s: %d dropped packs, %d of their spawns, %.0f yd of navmesh path sampled every %d yd (%d samples): %d passes (known %d)",
		plan.Name, len(dropped), spawns, walked, droppedSampleStep, samples, len(passes), known)
	if len(passes) == known {
		return
	}
	if len(passes) > known {
		t.Errorf("%s: %d dropped-pack passes, known %d: the walk wakes %d more spawns of the packs the order drops:\n  %s",
			plan.Name, len(passes), known, len(passes)-known, strings.Join(lines, "\n  "))
		return
	}
	t.Errorf("%s: %d dropped-pack passes, known %d: %d passes are gone (a moved pin is fine: re-pin it):\n  %s",
		plan.Name, len(passes), known, known-len(passes), strings.Join(lines, "\n  "))
}

// droppedPasses walks the plan and returns the closest sample of every dropped spawn it wakes, the walked yards, the
// sample count and how many spawns the record resolved.
func droppedPasses(t *testing.T, plan *dungeon.Plan, dropped []PackRef, mmaps string) ([]droppedPass, float64, int, int) {
	t.Helper()
	if len(dropped) == 0 {
		t.Fatalf("%s: the dropped set is empty: a route-order test names the packs its route leaves out", plan.Name)
	}
	dir := filepath.Join("testdata", fmt.Sprintf("map%d", plan.Map))
	cs, err := readCreatures(filepath.Join(dir, "creatures.tsv"))
	if err != nil {
		t.Fatalf("%s: read the map %d export: %v (call this from a test of tools/dungeongen)", plan.Name, plan.Map, err)
	}

	packs := droppedPackSpawns(plan, cs, dropped)
	var spans []droppedPass
	seen := map[uint64]bool{}
	for i, members := range packs {
		if len(members) == 0 {
			t.Errorf("%s: the dropped pack %s at %.0f %.0f %.0f has no spawn in %s that no plan row pulls: the record is stale or its radius (%.0f yd) misses the pack",
				plan.Name, dropped[i].label(), dropped[i].X, dropped[i].Y, dropped[i].Z, dir, dropped[i].radius())
			continue
		}
		for _, c := range members {
			// A spawn can stand inside two recorded packs (a 25 yd radius on a dense floor): it is one pass, counted
			// once, under the first pack that holds it.
			if seen[c.GUID] {
				continue
			}
			seen[c.GUID] = true
			reach := spawnAggroReach(plan, c)
			spans = append(spans, droppedPass{pack: i, spawn: c, reach: reach + droppedSpreadYd,
				aggro: reach - float64(c.Wander), wander: float64(c.Wander)})
		}
	}
	if len(spans) == 0 {
		t.Fatalf("%s: no dropped spawn in %s: the record is stale", plan.Name, dir)
	}

	pl := droppedPlanner(t, plan.Map, mmaps)
	defer pl.Close()

	pts, err := routePoints(plan)
	if err != nil {
		t.Fatalf("%s: export the route: %v", plan.Name, err)
	}
	hops := []dungeon.Point{plan.Entrance}
	var rows []string
	for _, q := range pts {
		at := dungeon.Point{X: q.px, Y: q.py, Z: q.pz}
		if len(hops) != 0 && dist(at, hops[len(hops)-1]) < 0.01 {
			continue
		}
		hops = append(hops, at)
		rows = append(rows, fmt.Sprintf("leg %d row %d (%s)", q.leg, q.seq, q.kind))
	}
	if len(hops) < 2 {
		t.Fatalf("%s: %d party points, want the walk", plan.Name, len(hops))
	}

	best := map[uint64]*droppedPass{}
	walked, samples := 0.0, 0
	for i := 0; i+1 < len(hops); i++ {
		a, b := hops[i], hops[i+1]
		path, err := pl.Plan(plan.Map, navmesh.Vec3{a.X, a.Y, a.Z}, navmesh.Vec3{b.X, b.Y, b.Z})
		if err != nil || path.Status != navmesh.Complete {
			t.Errorf("%s: no complete navmesh path %.1f %.1f %.1f -> %.1f %.1f %.1f: %v (%s %s)",
				plan.Name, a.X, a.Y, a.Z, b.X, b.Y, b.Z, err, path.Status, path.Reason)
			continue
		}
		walked += float64(path.Length)
		for j := 0; j+1 < len(path.Points); j++ {
			from, to := path.Points[j], path.Points[j+1]
			n := int(math.Ceil(dist(dungeon.Point{X: from[0], Y: from[1], Z: from[2]},
				dungeon.Point{X: to[0], Y: to[1], Z: to[2]}) / droppedSampleStep))
			if n < 1 {
				n = 1
			}
			for k := 0; k <= n; k++ {
				f := float32(k) / float32(n)
				at := dungeon.Point{X: from[0] + f*(to[0]-from[0]), Y: from[1] + f*(to[1]-from[1]), Z: from[2] + f*(to[2]-from[2])}
				samples++
				for si := range spans {
					s := &spans[si]
					d := dist(at, s.spawn.Pos)
					if d >= s.reach {
						continue
					}
					if cur, ok := best[s.spawn.GUID]; !ok || d < cur.dist {
						cp := *s
						cp.dist = d
						cp.to, cp.row = b, rows[i]
						best[s.spawn.GUID] = &cp
					}
				}
			}
		}
	}

	out := make([]droppedPass, 0, len(best))
	for _, f := range best {
		out = append(out, *f)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].dist < out[b].dist })
	return out, walked, samples, len(spans)
}

// droppedPackSpawns resolves each dropped pack to the spawns of the export it stands for: the hostile mobs within its
// radius of its centre that no plan row pulls, expanded to their formation (group_leader). A NO_AGGRO spawn is not a
// member: nothing wakes it by proximity.
func droppedPackSpawns(plan *dungeon.Plan, cs []creature, dropped []PackRef) [][]creature {
	pulled := pulledByPlan(plan, cs)
	byLeader := map[uint64][]creature{}
	for _, c := range cs {
		if c.GroupLeader != 0 {
			byLeader[c.GroupLeader] = append(byLeader[c.GroupLeader], c)
		}
	}
	wakeable := func(c creature) bool {
		return !pulled[c.GUID] && hostileMob(c, nil, nil) && c.FlagsExtra&worldspawn.NoAggro == 0
	}
	out := make([][]creature, len(dropped))
	for i, pk := range dropped {
		at, r := pk.Centre(), pk.radius()
		seen := map[uint64]bool{}
		var members []creature
		for _, c := range cs {
			if !wakeable(c) || dist(c.Pos, at) > r {
				continue
			}
			seen[c.GUID] = true
			members = append(members, c)
		}
		for changed := true; changed; {
			changed = false
			for _, c := range members {
				for _, f := range byLeader[c.GUID] {
					if seen[f.GUID] || !wakeable(f) {
						continue
					}
					seen[f.GUID] = true
					members = append(members, f)
					changed = true
				}
			}
		}
		sort.Slice(members, func(a, b int) bool { return members[a].GUID < members[b].GUID })
		out[i] = members
	}
	return out
}

// pulledByPlan marks the spawns the plan's rows pull: a spawn whose entry is in a pull group's entries (or is a boss's,
// its first entries or a phase entry) and that stands within the group's radius of one of its positions, expanded
// forward to the formation (a follower of a pulled spawn is pulled with it). It is the pack rule routeOwnerOf applies,
// read off the plan file instead of the export's rows, so a spawn a route-order plan keeps is never taken for a dropped
// pack's.
func pulledByPlan(plan *dungeon.Plan, cs []creature) map[uint64]bool {
	type row struct {
		entries []uint32
		pos     []dungeon.Point
		radius  float64
	}
	var rows []row
	for _, l := range plan.Legs {
		for _, g := range l.PullGroups {
			rows = append(rows, row{entries: g.Entries, pos: g.Positions, radius: float64(g.Radius)})
		}
		if b := l.Boss; b != nil {
			entries := append([]uint32{b.Entry}, b.First...)
			entries = append(entries, b.PhaseEntries...)
			pos := append([]dungeon.Point{b.Position}, b.Approach...)
			rows = append(rows, row{entries: entries, pos: pos, radius: float64(b.Radius)})
		}
	}
	byLeader := map[uint64][]creature{}
	for _, c := range cs {
		if c.GroupLeader != 0 {
			byLeader[c.GroupLeader] = append(byLeader[c.GroupLeader], c)
		}
	}
	pulled := map[uint64]bool{}
	for _, c := range cs {
		for _, r := range rows {
			rad := r.radius
			if rad == 0 {
				rad = defaultPackRadius
			}
			if !containsEntry(r.entries, c.Entry) || !nearAny(c.Pos, r.pos, rad) {
				continue
			}
			pulled[c.GUID] = true
			break
		}
	}
	queue := make([]uint64, 0, len(pulled))
	for guid := range pulled {
		queue = append(queue, guid)
	}
	for len(queue) > 0 {
		guid := queue[0]
		queue = queue[1:]
		for _, f := range byLeader[guid] {
			if !pulled[f.GUID] {
				pulled[f.GUID] = true
				queue = append(queue, f.GUID)
			}
		}
	}
	return pulled
}

// containsEntry says the entries hold e.
func containsEntry(entries []uint32, e uint32) bool {
	for _, x := range entries {
		if x == e {
			return true
		}
	}
	return false
}

// nearAny says p stands within r yd of one of pos.
func nearAny(p dungeon.Point, pos []dungeon.Point, r float64) bool {
	for _, q := range pos {
		if dist(p, q) <= r {
			return true
		}
	}
	return false
}

// droppedPlanner is the server's navmesh for the map: the folder given (a test's own), else $TURTLE_AGENT_MMAPS, else
// ~/turtle-agent-mmaps, else the build VM's ~/tb-ci/mmaps-turtle<map>, when it holds the map's .mmap and at least one
// tile (the folders internal/dungeon's plan tests read). It skips the test without one, unless
// TURTLE_AGENT_MMAPS_REQUIRED=1 asks for the failure (a machine that must have them).
func droppedPlanner(t *testing.T, mapID uint32, dir string) *navmesh.Planner {
	t.Helper()
	name := fmt.Sprintf("%03d", mapID)
	home, _ := os.UserHomeDir()
	var tried []string
	for _, d := range []string{dir, os.Getenv(droppedMmapsEnv), filepath.Join(home, "turtle-agent-mmaps"),
		filepath.Join(home, "tb-ci", "mmaps-turtle"+name)} {
		if d == "" {
			continue
		}
		tried = append(tried, d)
		if _, err := os.Stat(filepath.Join(d, name+".mmap")); err != nil {
			continue
		}
		if tiles, _ := filepath.Glob(filepath.Join(d, name+"*.mmtile")); len(tiles) == 0 {
			continue
		}
		return navmesh.NewPlanner(d)
	}
	if os.Getenv(droppedMmapsRequiredEnv) == "1" {
		t.Fatalf("no map %d navmesh files and %s=1: %s holds no %s.mmap with its tiles, so the dropped-pack check cannot run",
			mapID, droppedMmapsRequiredEnv, strings.Join(tried, ", "), name)
	}
	t.Skipf("no map %d navmesh files: set %s to a folder holding %s.mmap and its tiles (or %s=1 to fail instead); tried %s",
		mapID, droppedMmapsEnv, name, droppedMmapsRequiredEnv, strings.Join(tried, ", "))
	return nil
}
