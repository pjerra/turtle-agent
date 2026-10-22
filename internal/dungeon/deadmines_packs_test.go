package dungeon

import (
	"fmt"
	"math"
	"testing"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

// The pack-size fixes of Deadmines r12-19z29 / r14-19z32 (2026-10-22; ~/TortoiseBots-audit/DEADMINES-PACKS-2026-10-22.md). The packs of the Harvester room and
// the Gilnid ramp were 2-10 times bigger than the plan's group math: units no pull row owned (the patrolling Engineer 622 spawn 79213, the patrolling
// golem 2582923), the Summon Remote-Controlled Golem adds, runners dragging a fight into the next camp, and walks over uncleared ground (the walk down
// the foundry spiral to the firing stand of the Craftsman under it engaged the Engineers at the spiral's foot). Each test fails when its part of the
// fix is taken out of the plan.

func dmGroupAt(t *testing.T, l Leg, entry uint32, at Point) (int, PullGroup) {
	t.Helper()
	for i, g := range l.PullGroups {
		for k, p := range g.Positions {
			if g.Entries[k] == entry && dist3(p, at) < 0.06 {
				return i, g
			}
		}
	}
	t.Fatalf("leg %s: no pull group holds %d at %v", l.Name, entry, at)
	return -1, PullGroup{}
}

// Humanoid Engineers and Craftsmen are sheepable and every fight of legs 5-7 began with no crowd-control order: 622 and 1731 lead cc_priority, and the
// Engineer that summons the golems (622) is cc_always. 622 is kill_first too, which the loader allows only with cc_always (one is sheeped, the others
// are killed first).
func TestDeadminesCCListsNameTheEngineers(t *testing.T) {
	p := loadPlan(t, deadminesPlan)
	if fmt.Sprint(p.CCPriority) != "[622 1731 4418 1729 1732]" {
		t.Errorf("cc_priority %v: 622 and 1731 come first, then the old 4418 1729 1732", p.CCPriority)
	}
	if fmt.Sprint(p.CCAlways) != "[622]" {
		t.Errorf("cc_always %v: want [622], the Engineer that summons the golems", p.CCAlways)
	}
}

// The foundry spiral comes down at (-220 -594), 15 yd from the Engineers on the z 21 terrace, and its surface (z 32-34) lies directly above the
// Craftsman 79247 on the pit floor. The two terrace Engineers are pulled first, from the terrace stand on the spiral (z 36.5, over the open pit);
// the Craftsman under the spiral is pulled after them from a floor stand (a stand on the spiral has no sight of him: the run leader would walk the
// tank down the 57 yd spiral to the first sighted point, the spiral's foot, 15 yd from the Engineers: the r14 step-118 wipe).
func TestDeadminesGilnidCampIsPulledFromTheSpiralFirst(t *testing.T) {
	p := loadPlan(t, deadminesPlan)
	gl := dmLegSteps(p, "gilnid")
	ij, j := dmGroupAt(t, gl, 1731, Point{-198.926, -603.779, 19.3022}) // spawn 79247, under the spiral
	ie, ef := dmGroupAt(t, gl, 622, Point{-207.48, -585.729, 20.9768})  // spawn 79212, the terrace Engineer
	_, f := dmGroupAt(t, gl, 622, Point{-197.997, -582.979, 20.9769})   // spawn 79209
	if ie != ij-1 {
		t.Errorf("the terrace Engineers are pull group %d, the Craftsman under the spiral %d: the Engineers come first, at once before him", ie, ij)
	}
	if fmt.Sprint(ef.Entries) != "[622 622]" || len(f.Positions) != 2 {
		t.Errorf("the two terrace Engineers are one pull group of two 622: %v", ef.Entries)
	}
	if len(ef.Approach) == 0 || ef.Approach[len(ef.Approach)-1].Z < 30 {
		t.Errorf("the Engineers are shot from the spiral (z 30 or more), got %v", ef.Approach)
	}
	if len(j.Approach) == 0 || j.Approach[len(j.Approach)-1].Z > 25 {
		t.Errorf("the Craftsman under the spiral is shot from the floor (z 25 or less), got %v", j.Approach)
	}
	// Every group from the Engineers to the boss keeps its stand 17 yd (the aggro reach at level 22 against level 19) from every spawn pulled later,
	// boss and escort included, in 3-D: no walk or stand starts a fight no pull order planned.
	var later []Point
	for _, g := range gl.PullGroups[ie:] {
		later = append(later, g.Positions...)
	}
	later = append(later, gl.Boss.Position)
	later = append(later, Point{-178.319, -581.496, 19.3112}) // the boss's Craftsman, spawn 79211
	for k, g := range gl.PullGroups[ie:] {
		if len(g.Approach) == 0 {
			t.Errorf("gilnid pull group %d (%v) has no stand", ie+k, g.Entries)
			continue
		}
		stand := g.Approach[len(g.Approach)-1]
		own := map[Point]bool{}
		for _, q := range g.Positions {
			own[q] = true
		}
		earlier := map[Point]bool{}
		for _, h := range gl.PullGroups[ie : ie+k] {
			for _, q := range h.Positions {
				earlier[q] = true
			}
		}
		for _, q := range later {
			if own[q] || earlier[q] {
				continue
			}
			if d := dist3(stand, q); d < 17 {
				t.Errorf("gilnid pull group %d (%v): the stand %v is %.1f yd (3-D) from the spawn at %v pulled later", ie+k, g.Entries, stand, d, q)
			}
		}
	}
}

// dmSightBlocked is the navmesh sight proxy of the cold review of 2026-10-22: the eye-height line (1.7 yd) from a stand to a target is blocked when it
// passes below a walkable surface (a ramp) that lies above the line yet below the stand's eye. It sees no walls and no objects, so it is not a
// line-of-sight test; it finds the case that failed (the spiral above the Craftsman 79247).
func dmSightBlocked(t *testing.T, pl *navmesh.Planner, a, b Point) bool {
	t.Helper()
	const eye = 1.7
	n := int(math.Ceil(groundDist(a, b) / 0.5))
	hi := math.Max(float64(a.Z), float64(b.Z)) + eye + 0.5
	lo := math.Min(float64(a.Z), float64(b.Z)) - 0.5
	for i := 1; i <= n; i++ {
		f := float64(i) / float64(n)
		x, y := float64(a.X)+f*float64(b.X-a.X), float64(a.Y)+f*float64(b.Y-a.Y)
		lz := float64(a.Z) + eye + f*float64(b.Z-a.Z)
		fs, err := pl.Floors(36, float32(x), float32(y), float32(lo), float32(hi))
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range fs {
			if q.Flags&navmesh.NavGround != 0 && float64(q.Z) > lz+0.3 && float64(q.Z) < float64(a.Z)+eye+0.5 && float64(q.Z) > float64(b.Z)+0.5 {
				return true
			}
		}
	}
	return false
}

// The stand of every Gilnid pull has a clear navmesh sight line to the nearest unit it pulls, and the old spiral stand of the Craftsman 79247 does not
// (the proxy's own check).
func TestDeadminesGilnidStandsSeeTheirTargets(t *testing.T) {
	dir := mapMmaps(36)
	if dir == "" {
		t.Skip("no map 36 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 036.mmap and the 036*.mmtile tiles")
	}
	pl := navmesh.NewPlanner(dir)
	defer pl.Close()
	p := loadPlan(t, deadminesPlan)
	gl := dmLegSteps(p, "gilnid")
	if !dmSightBlocked(t, pl, Point{-195, -598, 34.8}, Point{-198.926, -603.779, 19.3022}) {
		t.Errorf("the sight proxy does not see the spiral above the Craftsman 79247 any more")
	}
	for i, g := range gl.PullGroups {
		if len(g.Approach) == 0 {
			continue
		}
		stand := g.Approach[len(g.Approach)-1]
		for _, q := range g.Positions {
			if d := dist3(stand, q); d < 30 && dmSightBlocked(t, pl, stand, q) {
				t.Errorf("gilnid pull group %d (%v): the stand %v has no navmesh sight line to the unit at %v (%.1f yd)", i, g.Entries, stand, q, d)
			}
		}
	}
}

// Steps 119-124 of the r14 run were instant "pack cleared" rows (the patrolling Engineer 79213 pulled with a radius of 3 round its spawn point and
// gone, the camp already dead in the step-118 melee), so Gilnid's pull took the camp with him. The ring is now pulled one camp at a time before the
// boss: the Craftsman and the patrolling Engineer together in the west, the pair at the west wall, the terrace Craftsman, the two northern pairs
// (79197/79198, never in the plan, and 79196/79201, moved up from the cannon_a leg) and the Craftsman beside Gilnid.
func TestDeadminesGilnidRingIsPulledBeforeTheBoss(t *testing.T) {
	p := loadPlan(t, deadminesPlan)
	gl := dmLegSteps(p, "gilnid")
	ia, ab := dmGroupAt(t, gl, 1731, Point{-234.991, -575.816, 19.3065}) // spawn 79218
	ib, _ := dmGroupAt(t, gl, 622, Point{-232.502, -569.785, 19.3065})   // spawn 79213, the patrolling Engineer
	if ia != ib || fmt.Sprint(ab.Entries) != "[1731 622]" || ab.Patrol == nil {
		t.Errorf("the west Craftsman and the patrolling Engineer 79213 are one pull group with its patrol: %d %d %v", ia, ib, ab.Entries)
	}
	k2, _ := dmGroupAt(t, gl, 1731, Point{-210.095, -552.754, 19.3065})  // spawn 79197
	k2b, _ := dmGroupAt(t, gl, 622, Point{-207.133, -551.25, 19.3065})   // spawn 79198
	k1, _ := dmGroupAt(t, gl, 622, Point{-187.23, -549.39, 19.3})        // spawn 79196
	k1b, _ := dmGroupAt(t, gl, 1731, Point{-186.878, -553.565, 19.3065}) // spawn 79201
	g, _ := dmGroupAt(t, gl, 1731, Point{-178.397, -565.429, 19.3065})   // spawn 79204
	if k2 != k2b || k1 != k1b || !(ia < k2 && k2 < k1 && k1 < g && g == len(gl.PullGroups)-1) {
		t.Errorf("the northern pairs and the Craftsman beside Gilnid are pulled in the order west, north-west, north-east, boss side: %d %d %d %d %d %d", ia, k2, k2b, k1, k1b, g)
	}
	// The pair moved up from the cannon_a leg is not pulled twice.
	ca := dmLegSteps(p, "cannon_a")
	for _, cg := range ca.PullGroups {
		for k, pos := range cg.Positions {
			if (cg.Entries[k] == 622 || cg.Entries[k] == 1731) && dist3(pos, Point{-187.23, -549.39, 19.3}) < 3 {
				t.Errorf("cannon_a still pulls the pair at %v that the gilnid leg pulls before the boss", pos)
			}
		}
	}
	// Nothing of the ring is a radius-3 pull on a patrol's spawn point any more.
	for i, g := range gl.PullGroups {
		if g.Radius > 0 && g.Radius < 5 {
			t.Errorf("gilnid pull group %d (%v) has radius %g: a pull row that small is cleared at once when its unit is on its beat", i, g.Entries, g.Radius)
		}
	}
}

// distToSegment3 is the 3-D distance from p to the segment a-b.
func distToSegment3(p, a, b Point) float64 {
	ab := [3]float64{float64(b.X - a.X), float64(b.Y - a.Y), float64(b.Z - a.Z)}
	ap := [3]float64{float64(p.X - a.X), float64(p.Y - a.Y), float64(p.Z - a.Z)}
	l2 := ab[0]*ab[0] + ab[1]*ab[1] + ab[2]*ab[2]
	u := 0.0
	if l2 > 0 {
		u = math.Max(0, math.Min(1, (ap[0]*ab[0]+ap[1]*ab[1]+ap[2]*ab[2])/l2))
	}
	return math.Sqrt(math.Pow(ap[0]-u*ab[0], 2) + math.Pow(ap[1]-u*ab[1], 2) + math.Pow(ap[2]-u*ab[2], 2))
}

// The Harvester room (cold review of 2026-10-22): the golem 2582936 stands 6.5 yd from the Engineer 2583041 and has the same faction, so it assists:
// the south-middle pack is one pull of three (the joiner estimate counts 3), from the west stand that keeps 18 yd (3-D) from the beat of the ledge
// Overseer 2583051 (z 57.8, aggro reach 15-16 yd) and from the boss; the Taskmaster 4417 is pulled over the west half of its beat only (its
// radius-22 disc reached 7 yd from the Harvester, whose family assist reach is 10 yd); and the first group of the room counts the patrolling golem
// 2582923, which walks to its door.
func TestDeadminesHarvesterRoomPacksAreCountedHonestly(t *testing.T) {
	p := loadPlan(t, deadminesPlan)
	hv := dmLegSteps(p, "harvester")
	ic, c := dmGroupAt(t, hv, 1731, Point{-92.5465, -578.331, 51.2296})
	ie, _ := dmGroupAt(t, hv, 622, Point{-88.1863, -579.469, 51.2296})
	ig, _ := dmGroupAt(t, hv, 61962, Point{-81.7283, -583.432, 51.2296})
	if ic != ie || ic != ig || fmt.Sprint(c.Entries) != "[1731 622 61962]" {
		t.Errorf("the Craftsman, the Engineer and the golem that assists them are one group of three: %d %d %d %v", ic, ie, ig, c.Entries)
	}
	if len(c.Approach) == 0 {
		t.Fatalf("the south-middle pack has no stand")
	}
	stand := c.Approach[len(c.Approach)-1]
	overseer := [2]Point{{-92.5058, -583.909, 57.8392}, {-64.5507, -584.832, 57.8392}} // the beat of spawn 2583051
	if d := distToSegment3(stand, overseer[0], overseer[1]); d < 18 {
		t.Errorf("the stand %v is %.1f yd (3-D) from the beat of the ledge Overseer 2583051: 18 wanted (aggro reach 15-16)", stand, d)
	}
	boss := Point{-66.8989, -570.032, 51.2299}
	if d := dist3(stand, boss); d < 18 {
		t.Errorf("the stand %v is %.1f yd from the Harvester", stand, d)
	}
	it, tm := dmGroupAt(t, hv, 4417, Point{-95.9344, -569.47, 51.2296})
	if it >= ic {
		t.Errorf("the Taskmaster is pulled before the south pack: %d %d", it, ic)
	}
	// No point of the Taskmaster's pull disc reaches within 12 yd of the Harvester (the family assist reach is 10).
	if tm.Radius > 12 {
		t.Errorf("the Taskmaster's pull radius is %g: at most 12", tm.Radius)
	}
	if reach := groundDist(tm.Positions[0], boss) - float64(tm.Radius); reach < 12 {
		t.Errorf("the Taskmaster's pull disc comes within %.1f yd of the Harvester: 12 wanted", reach)
	}
	_, first := dmGroupAt(t, hv, 622, Point{-152.739, -570.464, 51.2296})
	if !slicesContains(first.Entries, 61962) || first.Patrol == nil || !slicesContains(first.Patrol.Entries, 61962) {
		t.Errorf("the room's first group counts the patrolling golem 2582923: %v %+v", first.Entries, first.Patrol)
	}
}

// Sneed's door: the patrolling Woodcarver 79207 walks to within 10 yd of the door stand every 47.5 s, so the group that owns its beat is pulled first.
func TestDeadminesSneedPatrollerIsPulledFirst(t *testing.T) {
	p := loadPlan(t, deadminesPlan)
	sn := dmLegSteps(p, "sneed")
	i, g := dmGroupAt(t, sn, 641, Point{-277.256, -494.085, 49.5858})
	if i != 0 || g.Patrol == nil || math.Abs(float64(g.Radius)-19) > 0.01 {
		t.Errorf("the Woodcarver group with the patrol of 79207 (radius 19) is the leg's first pull: index %d radius %g patrol %v", i, g.Radius, g.Patrol != nil)
	}
}
