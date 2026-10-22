package dungeon

import (
	"fmt"
	"math"
	"testing"
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

// The foundry spiral comes down at (-220 -594), 15 yd from the Engineers on the z 21 terrace. The pulls of the spiral's own camp are made from the
// spiral: the Craftsman under it (stand on the spiral at z 34.8), then the two Engineers of the terrace (z 36.5), and only then the floor.
func TestDeadminesGilnidCampIsPulledFromTheSpiralFirst(t *testing.T) {
	p := loadPlan(t, deadminesPlan)
	gl := dmLegSteps(p, "gilnid")
	ij, j := dmGroupAt(t, gl, 1731, Point{-198.926, -603.779, 19.3022}) // spawn 79247, under the spiral
	ie, ef := dmGroupAt(t, gl, 622, Point{-207.48, -585.729, 20.9768})  // spawn 79212, the terrace Engineer
	_, f := dmGroupAt(t, gl, 622, Point{-197.997, -582.979, 20.9769})   // spawn 79209
	if ie != ij+1 {
		t.Errorf("the terrace Engineers are pull group %d, the Craftsman under the spiral %d: they follow it at once", ie, ij)
	}
	if fmt.Sprint(ef.Entries) != "[622 622]" || len(f.Positions) != 2 {
		t.Errorf("the two terrace Engineers are one pull group of two 622: %v", ef.Entries)
	}
	for _, g := range []PullGroup{j, ef} {
		if len(g.Approach) == 0 || g.Approach[len(g.Approach)-1].Z < 30 {
			t.Errorf("group %v: the stand is on the spiral (z 30 or more), got %v", g.Entries, g.Approach)
		}
	}
	// Every group from there to the boss keeps its stand 17 yd (the aggro reach at level 22 against level 19) from every spawn pulled later, boss and
	// escort included, in 3-D: no walk or stand starts a fight no pull order planned.
	var later []Point
	for _, g := range gl.PullGroups[ij:] {
		later = append(later, g.Positions...)
	}
	later = append(later, gl.Boss.Position)
	later = append(later, Point{-178.319, -581.496, 19.3112}) // the boss's Craftsman, spawn 79211
	for k, g := range gl.PullGroups[ij:] {
		if len(g.Approach) == 0 {
			t.Errorf("gilnid pull group %d (%v) has no stand", ij+k, g.Entries)
			continue
		}
		stand := g.Approach[len(g.Approach)-1]
		own := map[Point]bool{}
		for _, q := range g.Positions {
			own[q] = true
		}
		earlier := map[Point]bool{}
		for _, h := range gl.PullGroups[ij : ij+k] {
			for _, q := range h.Positions {
				earlier[q] = true
			}
		}
		for _, q := range later {
			if own[q] || earlier[q] {
				continue
			}
			if d := dist3(stand, q); d < 17 {
				t.Errorf("gilnid pull group %d (%v): the stand %v is %.1f yd (3-D) from the spawn at %v pulled later", ij+k, g.Entries, stand, d, q)
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

// The Harvester room: the golem 2582936 (spawn at -81.7 -583.4) is no longer pulled with the Craftsman and the Engineer 7-11 yd west of it (r12 step 90:
// 10 units engaged, the boss dragged in), the Taskmaster 4417 of the room is pulled wherever it is on its 20 yd beat, and the first group of the room counts the
// patrolling golem 2582923, which walks to its door.
func TestDeadminesHarvesterRoomPacksAreSplit(t *testing.T) {
	p := loadPlan(t, deadminesPlan)
	hv := dmLegSteps(p, "harvester")
	ic, c := dmGroupAt(t, hv, 1731, Point{-92.5465, -578.331, 51.2296})
	ie, _ := dmGroupAt(t, hv, 622, Point{-88.1863, -579.469, 51.2296})
	ig, g := dmGroupAt(t, hv, 61962, Point{-81.7283, -583.432, 51.2296})
	if ic != ie || fmt.Sprint(c.Entries) != "[1731 622]" {
		t.Errorf("the Craftsman and the Engineer are one group of two: %d %d %v", ic, ie, c.Entries)
	}
	if ig != ic+1 || fmt.Sprint(g.Entries) != "[61962]" {
		t.Errorf("the golem is the group after them, alone: %d %v", ig, g.Entries)
	}
	for _, gg := range []PullGroup{c, g} {
		if len(gg.Approach) == 0 || gg.Approach[len(gg.Approach)-1].X > -100 {
			t.Errorf("group %v is pulled from the west side of the room (x below -100): %v", gg.Entries, gg.Approach)
		}
	}
	it, tm := dmGroupAt(t, hv, 4417, Point{-95.9344, -569.47, 51.2296})
	if it >= ic || tm.Radius < 20 {
		t.Errorf("the Taskmaster is pulled before the south pairs, over its whole beat: group %d radius %g", it, tm.Radius)
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
