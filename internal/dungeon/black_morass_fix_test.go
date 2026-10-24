package dungeon

import (
	"math"
	"testing"
)

// The bm-fix1 landing fix (2026-10-24; the r28d first run stopped at step 1): leg 0's `to`
// (-1961.5 6797 26.5) is a real spawn row on the mesh, but the live navmesh path from the
// landing (-2002.5 6575.3 -154.9) is 2,678 yd, not the 289 yd straight line. Leg 0 now walks
// the landing to its `to` on via points. Each test fails when its part is taken out of the plan.
const blackMorassPlan = "../../dungeons/black_morass.json"

// Leg 0 walks the landing to its `to` on via points: every hop from the entrance is short
// (at most 55 yd, under the run leader's 60 yd view and the exporter's 60 yd cut limit) and
// the last via hands to the `to` itself. The `to` stays the exact spawn row.
func TestBlackMorassLeg0WalksTheLandingToItsTo(t *testing.T) {
	p := loadPlan(t, blackMorassPlan)
	if len(p.Legs) != 4 {
		t.Fatalf("%d legs, want 4", len(p.Legs))
	}
	leg := p.Legs[0]
	if len(leg.Via) != 69 {
		t.Fatalf("leg 0 has %d via points, want 69", len(leg.Via))
	}
	prev := p.Entrance
	for i, w := range leg.Via {
		d := math.Sqrt(math.Pow(float64(w.X-prev.X), 2) + math.Pow(float64(w.Y-prev.Y), 2) + math.Pow(float64(w.Z-prev.Z), 2))
		if d > 55 {
			t.Errorf("via %d is %.0f yd from the point before it, want at most 55", i, d)
		}
		if w.Direct || w.Exact || w.GMCross || w.CheckSkip != "" {
			t.Errorf("via %d is not a plain walked waypoint (direct/exact/gm_cross/check_skip)", i)
		}
		prev = w.Point
	}
	last := leg.Via[len(leg.Via)-1].Point
	if d := math.Sqrt(math.Pow(float64(leg.To.X-last.X), 2) + math.Pow(float64(leg.To.Y-last.Y), 2) + math.Pow(float64(leg.To.Z-last.Z), 2)); d > 55 {
		t.Errorf("the to is %.0f yd from the last via, want at most 55", d)
	}
	if leg.To.X != -1961.5 || leg.To.Y != 6797 || leg.To.Z != 26.5253 {
		t.Errorf("the to is %v, want the exact spawn row -1961.5 6797 26.5253", leg.To)
	}
	g0 := leg.PullGroups[0].Positions[0]
	if g0 != leg.To {
		t.Errorf("pull group 0 is at %v, want the to point itself", g0)
	}
}

// The fix changes nothing else: the leg order, the pull counts and bosses, and the `to`
// points of the other legs stay as the prep left them.
func TestBlackMorassFixChangesNothingElse(t *testing.T) {
	p := loadPlan(t, blackMorassPlan)
	wantLegs := []string{"antnormi", "medivh", "time-lord_epochronos", "chronar"}
	wantPulls := []int{131, 23, 66, 32}
	wantBoss := []uint32{65125, 50105, 65116, 65113}
	for i, l := range p.Legs {
		if l.Name != wantLegs[i] {
			t.Errorf("leg %d is %q, want %q", i, l.Name, wantLegs[i])
		}
		if len(l.PullGroups) != wantPulls[i] {
			t.Errorf("leg %s has %d pull groups, want %d", l.Name, len(l.PullGroups), wantPulls[i])
		}
		if l.Boss == nil || l.Boss.Entry != wantBoss[i] {
			t.Errorf("leg %s has boss %+v, want entry %d", l.Name, l.Boss, wantBoss[i])
		}
		if i > 0 && len(l.Via) != 0 {
			t.Errorf("leg %s has %d via points, want none (only leg 0 walks the landing)", l.Name, len(l.Via))
		}
	}
}
