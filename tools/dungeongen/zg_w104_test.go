// Walk-104 fix (TortoiseBots-audit briefs-omp/zg-route-T29.md; tests/raid-zg-walk104 fz6d/fz5c): the Jin'do hall's
// west packs — the 49373-49382 cluster (group 5), the 49363 camp (group 6) and the 1173082 four (group 7) — were a
// plain pull at the camp itself (groups 5 and 7: their only approach point WAS the pack's centre) or a stand inside
// the camp's detection (group 6's stand -11581 -1334.8 stood 18.6 yd from its nearest unit 49370 and 2.6 yd above
// it), so the walking party woke them with no CC orders out. fz6d-1: at step 471 the tank engaged a Mistress "on
// arrival (0.0 yd)", the 24 bodies of the three camps plus three summoned Corrupted Infernals were in by 22:47:05
// and 20 bots died in 88 s; fz6d-3 474/475 repeated it ("engaged 10 over 6", 5 dead). All three are stand pulls now,
// each from a walked floor point out of every other hall pack's reach, and every walk in stays out of the detection
// of the pack it approaches. On origin/dev the test fails at the first group ("is not a stand pull").
package main

import (
	"fmt"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
)

// zgW104OtherYd is the clearance a stand keeps from every pack it does not pull: 20 yd detection plus the 3 yd
// spread the walk-38 stands use (zgLaterYd + zgPatrolSpreadYd), the rule the Jeklik ramp fix was held to.
const zgW104OtherYd = 23.0

// zgW104OwnWalkYd is the clearance the walk to a stand keeps from its own pack: past the 20 yd detection, so the
// party does not wake the pack before it stands and the row's CC orders can go out.
const zgW104OwnWalkYd = 21.0

// TestZgW104JindoHallWestPacksOneAtATime pins the walk-104 fix: the hall's three west packs are stand pulls whose
// stands keep 19.5-28 yd from their own pack (in the tank's shot, out of its detection), >= 23 yd from every other
// pack of the leg and from the hall's unowned spawns (the pit's Sacrificed Trolls, the north-west cluster), and
// >= Jin'do's route reach; the walks in keep >= 21 yd from the pack they approach and >= 23 from the rest. With the
// mmaps it also checks line of sight from every stand to every unit of its pack and a complete navmesh path in.
func TestZgW104JindoHallWestPacksOneAtATime(t *testing.T) {
	p, cs := zgLoadPlanAndExport(t)
	leg, legNo := zgW37LegByName(t, p, "jindo_the_hexxer")
	packs := zgPacks(t, *leg, cs)
	owned := map[uint64]bool{}
	for gi := range packs {
		for _, c := range packs[gi] {
			owned[c.GUID] = true
		}
	}
	var jindo *creature
	for i := range cs {
		if cs[i].Entry == 11380 {
			jindo = &cs[i]
		}
	}
	if jindo == nil {
		t.Fatal("no Jin'do spawn in the map-309 export")
	}
	bossReach := routeReach(p, *jindo)

	want := []struct {
		group int
		guids []uint64
	}{
		{5, []uint64{49373, 49374, 49375, 49376, 49377, 49378, 49379, 49380, 49381, 49382}}, // the 49373-49382 cluster
		{6, []uint64{49363, 49364, 49365, 49366, 49367, 49368, 49369, 49370, 49371, 49372}}, // the 49363 camp
		{7, []uint64{1173082, 1173083, 1173084, 1173085}},                                   // the 1173082 four
	}
	stands := map[int]dungeon.Point{}
	for _, w := range want {
		where := fmt.Sprintf("leg 6 pull group %d", w.group)
		if w.group >= len(leg.PullGroups) {
			t.Errorf("%s: the leg has only %d pull groups", where, len(leg.PullGroups))
			continue
		}
		g := leg.PullGroups[w.group]
		got := map[uint64]bool{}
		for _, c := range packs[w.group] {
			got[c.GUID] = true
		}
		for _, guid := range w.guids {
			if !got[guid] {
				t.Errorf("%s does not hold %d: the hall's west packs must stay whole and in their own group", where, guid)
			}
		}
		if len(got) != len(w.guids) {
			t.Errorf("%s holds %d units, want its own %d", where, len(got), len(w.guids))
		}
		if !g.StandPull {
			t.Errorf("%s (%d units) is not a stand pull: the party walks to the camp and wakes it on the way (walk 104 step 471: the tank engaged a Mistress at 0.0 yd, 24 bodies, 20 dead)", where, len(got))
			continue
		}
		if len(g.Approach) == 0 {
			t.Errorf("%s is a stand pull with no approach", where)
			continue
		}
		stand := g.Approach[len(g.Approach)-1]
		stands[w.group] = stand
		// Its own pack: out of its detection on the walk in, inside the tank's shot so PatrolStandPullGate pulls
		// from the stand instead of falling back to a plain pull (zgW37TankShotYd).
		if own, who := zgMin3(stand, packs[w.group]); own < zgOwnMinYd {
			t.Errorf("%s: its stand %.1f %.1f %.1f is %.1f yd from %s %d, want >= %g: the party wakes the pack on the walk to the stand", where, stand.X, stand.Y, stand.Z, own, who.Name, who.Entry, zgOwnMinYd)
		}
		far := 0.0
		for _, c := range packs[w.group] {
			if d := dist(stand, c.Pos); d > far {
				far = d
			}
		}
		if far > zgOwnMaxYd {
			t.Errorf("%s: its stand is %.1f yd from the pack's farthest unit, past the %g yd shot: the row plain-pulls to the camp", where, far, zgOwnMaxYd)
		}
		// Every other pack of the leg, at the detection plus the raid's spread.
		for gi := range packs {
			if gi == w.group {
				continue
			}
			if d, who := zgMin3(stand, packs[gi]); d < zgW104OtherYd {
				t.Errorf("%s: its stand %.1f %.1f %.1f is %.1f yd from pull group %d's %s %d, want >= %g: the fight there brings that pack in", where, stand.X, stand.Y, stand.Z, d, gi, who.Name, who.Entry, zgW104OtherYd)
			}
		}
		// The hall's hostiles no row pulls: the pit's ten Sacrificed Trolls (10 s respawn) and the north-west
		// cluster 49387-49396, plus Jin'do himself at his own route reach.
		for _, c := range cs {
			if !zgHostile(c) || owned[c.GUID] {
				continue
			}
			yd := zgW104OtherYd
			if c.Entry == 11380 {
				yd = bossReach
			}
			if d := dist(stand, c.Pos); d < yd {
				t.Errorf("%s: its stand %.1f %.1f %.1f is %.1f yd from %s %d, which no row pulls, want >= %g", where, stand.X, stand.Y, stand.Z, d, c.Name, c.GUID, yd)
			}
		}
	}

	// The walks in: the party's own party points of the exported leg, so the segments are the ones the run leader
	// walks (a stand pull's party point is its stand; a plain pull's is its approach point).
	parties := zgParties(t, p, legNo)
	for _, w := range want {
		stand, ok := stands[w.group]
		if !ok {
			continue
		}
		at := -1
		for i := range parties {
			if parties[i].Turn && parties[i].Group == w.group {
				at = i
			}
		}
		if at < 0 {
			t.Errorf("leg 6 pull group %d: no exported pull row", w.group)
			continue
		}
		// The walk in starts where the party stood for the previous pull row (a stand pull's party point is its
		// stand, a plain pull's its approach point): the party's own points, so the segment is the one it walks.
		from, ok := dungeon.Point{}, false
		for i := at - 1; i >= 0; i-- {
			if parties[i].Turn {
				from, ok = parties[i].P, true
				break
			}
		}
		if !ok {
			t.Errorf("leg 6 pull group %d: no row before it to walk in from", w.group)
			continue
		}
		walk := zgSamplesAlong(from, stand, zgSampleStep)
		label := fmt.Sprintf("leg 6 pull group %d: the walk from %.1f %.1f to its stand", w.group, from.X, from.Y)
		if d, q, who := zgSampleMin(walk, packs[w.group]); d < zgW104OwnWalkYd {
			t.Errorf("%s passes %.1f yd from its own %s %d at %.1f %.1f, want >= %g (the pack wakes before the pull)", label, d, who.Name, who.Entry, q.X, q.Y, zgW104OwnWalkYd)
		}
		// The packs pulled later are still alive on this walk (the ones before it are the party's own ground: group
		// 4's camp is where it stood).
		if d, q, who := zgSampleMin(walk, zgLater(packs, w.group)); d < zgW104OtherYd {
			t.Errorf("%s passes %.1f yd from %s %d of a later group at %.1f %.1f, want >= %g", label, d, who.Name, who.Entry, q.X, q.Y, zgW104OtherYd)
		}
	}

	// With the server's mmaps: line of sight from every stand to every unit of its pack (what the pull needs) and a
	// complete navmesh path to the stand (what the walk needs).
	pl := zgPlannerOrNil(t)
	if pl == nil {
		t.Log("no map 309 navmesh files (set TURTLE_AGENT_MMAPS to a folder holding 309.mmap and its tiles): the line-of-sight and path checks are skipped")
		return
	}
	defer pl.Close()
	for _, w := range want {
		stand, ok := stands[w.group]
		if !ok {
			continue
		}
		sv := navmesh.Vec3{stand.X, stand.Y, stand.Z}
		for _, c := range packs[w.group] {
			ray, err := pl.Raycast(p.Map, sv, navmesh.Vec3{c.Pos.X, c.Pos.Y, c.Pos.Z})
			if err != nil {
				t.Fatalf("leg 6 pull group %d: raycast to %s %d: %v", w.group, c.Name, c.GUID, err)
			}
			if !ray.Known || !ray.Clear {
				t.Errorf("leg 6 pull group %d: no line of sight from its stand %.1f %.1f %.1f to %s %d at %.1f %.1f: the pull never goes out", w.group, stand.X, stand.Y, stand.Z, c.Name, c.GUID, c.Pos.X, c.Pos.Y)
			}
		}
		for i := range parties {
			if !parties[i].Turn || parties[i].Group != w.group || i == 0 {
				continue
			}
			a, has := dungeon.Point{}, false
			for k := i - 1; k >= 0; k-- {
				if parties[k].Turn {
					a, has = parties[k].P, true
					break
				}
			}
			if !has {
				continue
			}
			path, err := pl.Plan(p.Map, navmesh.Vec3{a.X, a.Y, a.Z}, sv)
			if err != nil {
				t.Fatalf("leg 6 pull group %d: navmesh path: %v", w.group, err)
			}
			if path.Status != navmesh.Complete {
				t.Errorf("leg 6 pull group %d: no complete navmesh path from %.1f %.1f %.1f to its stand %.1f %.1f %.1f (%s %s): the walk pauses there", w.group, a.X, a.Y, a.Z, stand.X, stand.Y, stand.Z, path.Status, path.Reason)
			}
		}
	}
}
