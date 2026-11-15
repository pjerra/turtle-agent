// T31 (TortoiseBots-audit tests/bug-hunt-1113/TICKETS.md): ZG walk 107 paused at step 117, the pull of the merged
// Mad Servant camp (7 x 15111 + the Blood Priest 11340, the WALK 19/20 row). Its authored stand was
// -11919.2 -1843.6 58.8, the D camp's old stand: 40.9-49.4 yd from every member (the run leader's own read in the
// walk's 117/689 rows), so the tank could never shoot from it, and the server read no line of sight from it either
// ("no LOS from the authored stand; holding" for 300 s out of combat, raid-zg-walk107-aux/step117-st7a.log). Walk
// 104 pulled the same row from the run leader's own firing stand at the same place; its frames show the tank there.
//
// The stand is the tank's position in walk 104: tests/raid-zg-walk104/fz6a-2/frames.jsonl seq 2012
// (2026-11-14T18:32:14Z), -11899.3 -1849.1 61.3, the tick its run.log (CET) orders "pulling 15111 0xf130003b0700bf9e
// (order 1; shot from where it stands)" after "to firing stand 1 for 15111 ... (24.5 yd from it; was 44.8 yd, los
// yes)". That log line is the server's own line of sight (the agent has no vmaps; the navmesh ray below is only its
// stand-in), so the walk is the LOS proof; the rays pin the same answer where the mesh can.
package main

import (
	"math"
	"slices"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
)

// zgVenoxis117Stand is the walk-104 tank position the row's stand moved to (fz6a-2/frames.jsonl seq 2012).
var zgVenoxis117Stand = dungeon.Point{X: -11899.3, Y: -1849.1, Z: 61.3}

// zgVenoxis117ShotYd is the shot the run leader's stand rule reads at the plan's level band (leadShotYd 28.5 minus
// the stand-pull margin; walk 107's own note lists the members "out of the 28.5 yd shot").
const zgVenoxis117ShotYd = 28.5

// zgVenoxis117Pack is the row's pack: entries 7 x 15111 + 11340 over the eight spawns of the merged D + A camps.
func zgVenoxis117Pack(t *testing.T, leg *dungeon.Leg) *dungeon.PullGroup {
	t.Helper()
	for i := range leg.PullGroups {
		g := &leg.PullGroups[i]
		servants := 0
		for _, e := range g.Entries {
			if e == 15111 {
				servants++
			}
		}
		if servants == 7 && slices.Contains(g.Entries, 11340) && len(g.Positions) == 8 {
			return g
		}
	}
	t.Fatal("the leg has no 7 x 15111 + 11340 pull group (the merged D + A camp)")
	return nil
}

// TestZgVenoxis117PullStandInShot: the merged camp's stand is the walk-104 firing stand, in the run leader's shot of
// the pack and on the walked floor, so the tank's pull fires there instead of holding for 300 s.
func TestZgVenoxis117PullStandInShot(t *testing.T) {
	p, _ := zgLoadPlanAndExport(t)
	leg, _ := zgW37LegByName(t, p, "high_priest_venoxis")
	g := zgVenoxis117Pack(t, leg)
	if len(g.Approach) < 2 {
		t.Fatalf("the row has %d approach points; the last is the stand", len(g.Approach))
	}
	stand := g.Approach[len(g.Approach)-1]
	// (a) the pin: the walk-104 tank position, not a point invented for the plan.
	if d := dist(stand, zgVenoxis117Stand); d > 0.1 {
		t.Errorf("the stand %.1f %.1f %.1f is %.2f yd from the walk-104 firing stand %.1f %.1f %.1f (fz6a-2 frames seq 2012), want the walked point",
			stand.X, stand.Y, stand.Z, d, zgVenoxis117Stand.X, zgVenoxis117Stand.Y, zgVenoxis117Stand.Z)
	}
	// (b) the shot: at least one member of the row's own pack stands inside the run leader's 28.5 yd shot, or the
	// pull can never fire from the authored stand (walk 107: every member 40.9-49.4 yd out, 300 s out of combat).
	nearest := math.Inf(1)
	for _, q := range g.Positions {
		if d := dist(stand, q); d < nearest {
			nearest = d
		}
	}
	if nearest > zgVenoxis117ShotYd {
		t.Errorf("the stand %.1f %.1f %.1f is %.1f yd from the nearest member of its own pack, want <= %g: no shot from it (walk 107 held 300 s out of combat)",
			stand.X, stand.Y, stand.Z, nearest, zgVenoxis117ShotYd)
	}
	// (c) the mesh: the stand is on the floor the party walks, the walk from the approach point before it completes,
	// and every member inside the shot has a clear ray from it.
	pl := zgPlannerOrNil(t)
	if pl == nil {
		t.Skip("the floor, the walk and the rays need the map-309 mmaps")
	}
	defer pl.Close()
	at := navmesh.Vec3{stand.X, stand.Y, stand.Z}
	near, ok, err := pl.Nearest(309, at)
	if err != nil {
		t.Fatalf("nearest mesh point to the stand: %v", err)
	}
	if !ok {
		t.Errorf("the stand %.1f %.1f %.1f is off the map 309 mesh", stand.X, stand.Y, stand.Z)
	} else if d := dist(stand, dungeon.Point{X: near[0], Y: near[1], Z: near[2]}); d > 1.5 {
		t.Errorf("the nearest mesh floor is %.2f yd from the stand %.1f %.1f %.1f (%.1f %.1f %.1f), want <= 1.5: the run leader cannot stand there",
			d, stand.X, stand.Y, stand.Z, near[0], near[1], near[2])
	}
	from := g.Approach[len(g.Approach)-2]
	path, err := pl.Plan(309, navmesh.Vec3{from.X, from.Y, from.Z}, at)
	if err != nil {
		t.Fatalf("path to the stand: %v", err)
	}
	if path.Status != navmesh.Complete {
		t.Errorf("no complete navmesh path from the approach point %.1f %.1f %.1f to the stand %.1f %.1f %.1f: %s %s",
			from.X, from.Y, from.Z, stand.X, stand.Y, stand.Z, path.Status, path.Reason)
	}
	for _, q := range g.Positions {
		if d := dist(stand, q); d > zgVenoxis117ShotYd {
			continue // out of the shot: the stand is authored for the members it can reach
		}
		ray, err := pl.Raycast(309, at, navmesh.Vec3{q.X, q.Y, q.Z})
		if err != nil {
			t.Fatalf("ray to %.1f %.1f %.1f: %v", q.X, q.Y, q.Z, err)
		}
		// session.LineClear's rule: clear, or stopping within its 1.5 yd end slack.
		clear := ray.Known && (ray.Clear || ray.Hit >= float32(dist(stand, q))-1.5)
		if !clear {
			t.Errorf("no line of sight from the stand %.1f %.1f %.1f to the pack member %.1f %.1f %.1f (%.1f yd, in shot): the navmesh ray stops after %.1f yd",
				stand.X, stand.Y, stand.Z, q.X, q.Y, q.Z, dist(stand, q), ray.Hit)
		}
	}
}
