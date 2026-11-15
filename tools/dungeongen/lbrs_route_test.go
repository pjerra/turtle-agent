// The Lower Blackrock Spire route's order: the walk visits each pack on the boss-to-boss road it belongs to instead of
// zig-zagging between the spire's tiers (issue #1542, route-order audit 2026-11-12). The pins are the walk length and the
// pack count, so a later edit that adds a detour or drops a pack is a failure and not just a print.
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

// lbrsDevWalkYd is the walk before the re-order: origin/dev's plan, measured here (the sum of the navmesh path between
// consecutive party points, entrance -> first row included). 16,001.1 yd, because the order switched tiers 17 times; the
// same plan's floor-aware export measures 16,455.5 yd and the deployed rows 16,204.2 yd. The re-order cuts it to the
// bound below.
const lbrsDevWalkYd = 16001.1

// lbrsMaxWalkYd caps the re-ordered walk: 4,754.0 yd measured here against a 2,994 yd straight route (4,830.7 yd on the
// floor-aware export), 9% of headroom for a cut point that moves.
const lbrsMaxWalkYd = 5200.0

// lbrsPacks is the plan's pull-group count: 113 from the round-1 re-order, minus 1 dropped upper-ledge pack (leg 0 pg 20
// at z=43, off the walk), plus 2 from splitting leg 1 pg 11 into its 3 natural 2-mob pairs (round 2, dr41).
const lbrsPacks = 114

// lbrsPlan is the plan under test.
const lbrsPlan = "../../dungeons/lower_blackrock_spire.json"

// lbrsPlannerOrNil is the map-229 navmesh when the machine has the server's mmaps, nil otherwise.
func lbrsPlannerOrNil(t *testing.T) *navmesh.Planner {
	t.Helper()
	home, _ := os.UserHomeDir()
	for _, dir := range []string{os.Getenv("TURTLE_AGENT_MMAPS"), filepath.Join(home, "turtle-agent-mmaps")} {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "229.mmap")); err != nil {
			continue
		}
		if tiles, _ := filepath.Glob(filepath.Join(dir, "229*.mmtile")); len(tiles) == 0 {
			continue
		}
		return navmesh.NewPlanner(dir)
	}
	return nil
}

// lbrsPartyRows are the rows that put the party somewhere: an interaction row (open, loot) is not a waypoint.
func lbrsPartyRows(kind string) bool {
	switch kind {
	case planexport.KindMove, planexport.KindMoveDirect, planexport.KindMoveHopExact, planexport.KindPull, planexport.KindBoss, planexport.KindHold:
		return true
	}
	return false
}

// TestLowerBlackrockSpireRouteOrder pins the pack count and the walk the exported rows take. It fails on origin/dev's plan
// (its walk is 16,001.1 yd against the 2,994 yd straight route) and on any later edit that walks a tier twice.
func TestLowerBlackrockSpireRouteOrder(t *testing.T) {
	p, err := dungeon.Load(lbrsPlan)
	if err != nil {
		t.Fatal(err)
	}
	packs := 0
	for _, l := range p.Legs {
		packs += len(l.PullGroups)
	}
	if packs != lbrsPacks {
		t.Errorf("%d packs, want %d: the re-order moves packs between legs and drops none", packs, lbrsPacks)
	}
	if len(p.Legs) != 7 {
		t.Fatalf("%d legs, want 7 (one per boss, in boss order)", len(p.Legs))
	}
	for i, leg := range p.Legs {
		if leg.Boss == nil {
			t.Errorf("leg %d (%s) has no boss: the boss order is part of the route", i, leg.Name)
		}
	}

	// Round-2 pins (dr41 steps 42, 60, 121): each fails on the round-1 plan.
	for i, pg := range p.Legs[0].PullGroups {
		for _, pos := range pg.Positions {
			if pos.Z > 40 && pos.X < -40 {
				t.Errorf("leg 0 pg %d sits on the z=43 upper ledge (%.1f %.1f %.1f), off the Omokk floor walk (dr41 step 42)", i, pos.X, pos.Y, pos.Z)
			}
		}
	}
	for i, pg := range p.Legs[1].PullGroups {
		if len(pg.Entries) > 5 {
			t.Errorf("leg 1 pg %d has %d entries (want <= 5; split the 6-mob Vosh'gajin platform pack, dr41 step 60)", i, len(pg.Entries))
		}
	}
	if n := len(p.Legs[1].PullGroups); n < 3 {
		t.Fatalf("leg 1 has %d pull_groups, want >= 3", n)
	} else {
		boss := p.Legs[1].Boss.Position
		for _, idx := range []int{n - 2, n - 1} {
			pg := p.Legs[1].PullGroups[idx]
			if len(pg.Approach) == 0 {
				t.Errorf("leg 1 pg %d has no approach stand out of Vosh'gajin's aggro reach", idx)
				continue
			}
			st := pg.Approach[len(pg.Approach)-1]
			dx, dy, dz := st.X-boss.X, st.Y-boss.Y, st.Z-boss.Z
			if dx*dx+dy*dy+dz*dz < 27*27 {
				t.Errorf("leg 1 pg %d stand (%.1f %.1f %.1f) is < 27 yd from boss 9236", idx, st.X, st.Y, st.Z)
			}
		}
	}
	if len(p.Legs[3].PullGroups) == 0 {
		t.Fatal("leg 3 has no pull_groups")
	}
	lastSmolderweb := p.Legs[3].PullGroups[len(p.Legs[3].PullGroups)-1]
	if len(lastSmolderweb.Entries) != 6 || lastSmolderweb.Entries[0] != 10375 {
		t.Errorf("leg 3 last pull_group is %v, want the 6-spider pack [10375 ...] moved from leg 4 (dr41 step 121)", lastSmolderweb.Entries)
	}
	for i, pg := range p.Legs[4].PullGroups {
		if len(pg.Entries) == 6 && pg.Entries[0] == 10375 {
			t.Errorf("leg 4 pg %d still holds the 6-spider pack beside Mother Smolderweb (dr41 step 121)", i)
		}
	}

	pl := lbrsPlannerOrNil(t)
	if pl == nil {
		t.Skip("no map 229 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 229.mmap and its tiles")
	}
	defer pl.Close()

	res, err := planexport.Build(p, planexport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var stops []dungeon.Point
	for _, s := range res.Rows.Steps {
		if lbrsPartyRows(s.Kind) {
			stops = append(stops, dungeon.Point{X: s.X, Y: s.Y, Z: s.Z})
		}
	}
	if len(stops) < 2 {
		t.Fatalf("%d party rows, want the walk", len(stops))
	}
	total := lbrsPath(t, pl, p.Map, p.Entrance, stops[0], "entrance")
	for i := 1; i < len(stops); i++ {
		total += lbrsPath(t, pl, p.Map, stops[i-1], stops[i], fmt.Sprintf("row %d", i+1))
	}
	t.Logf("lower_blackrock_spire walk %.1f yd (dev %.1f, straight 2993.9, %d packs)", total, lbrsDevWalkYd, packs)
	if total > lbrsMaxWalkYd {
		t.Errorf("the walk is %.1f yd (dev %.1f, cap %.0f): the order switches tiers again, put each pack in the leg whose walk passes it",
			total, lbrsDevWalkYd, lbrsMaxWalkYd)
	}
}

// lbrsPath is the navmesh path a->b; a missing or partial path fails the test, it is not zero.
func lbrsPath(t *testing.T, pl *navmesh.Planner, mapID uint32, a, b dungeon.Point, what string) float64 {
	t.Helper()
	path, err := pl.Plan(mapID, navmesh.Vec3{a.X, a.Y, a.Z}, navmesh.Vec3{b.X, b.Y, b.Z})
	if err != nil || path.Status != navmesh.Complete {
		t.Errorf("%s: no complete navmesh path %.1f %.1f %.1f -> %.1f %.1f %.1f: %v (%s %s)",
			what, a.X, a.Y, a.Z, b.X, b.Y, b.Z, err, path.Status, path.Reason)
		return 0
	}
	return float64(path.Length)
}
