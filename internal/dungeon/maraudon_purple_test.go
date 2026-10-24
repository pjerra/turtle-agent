package dungeon

import (
	"math"
	"testing"
)

// TestMaraudonPurpleWipeLegSplit holds the 2026-10-24 data fix (r28b
// maraudon_purple slotA: the run walked its approach into the patrolling slime
// pair at 762 -181 around steps 70-74 and fought the sludge, the lurker and the
// satyr mix as one 6-8 unit pack, wiping twice): leg 0 groups 35-39 are 4 pulls
// in route order, radius 5, one approach stand each on the pack's floor 15-25 yd
// from the pack and outside the later packs' aggro reach. The slime pair goes
// first with its own patrol block (source 54580, pulled first or waited out);
// the lone lurker is folded into the satyr mix. Every pin fails when a piece is
// cut.
func TestMaraudonPurpleWipeLegSplit(t *testing.T) {
	p, err := Load("../../dungeons/maraudon_purple.json")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(p.Legs[0].PullGroups); n != 71 {
		t.Fatalf("leg 0 pull groups: %d, want 71", n)
	}
	l0 := p.Legs[0]
	slime, sisters, sludge, mix := l0.PullGroups[35], l0.PullGroups[36], l0.PullGroups[37], l0.PullGroups[38]
	if len(slime.Entries) != 2 || slime.Entries[0] != 12221 || slime.Entries[1] != 12221 || slime.Radius != 5 {
		t.Fatalf("slime pair: %v radius %g", slime.Entries, slime.Radius)
	}
	if len(sisters.Entries) != 2 || sisters.Entries[0] != 11794 || sisters.Entries[1] != 11794 || sisters.Radius != 5 {
		t.Fatalf("sisters: %v radius %g", sisters.Entries, sisters.Radius)
	}
	if len(sludge.Entries) != 4 || sludge.Radius != 5 {
		t.Fatalf("sludge: %v radius %g", sludge.Entries, sludge.Radius)
	}
	for _, e := range sludge.Entries {
		if e != 12222 {
			t.Fatalf("sludge entries: %v", sludge.Entries)
		}
	}
	wantMix := []uint32{12216, 11791, 11791, 12216, 12217, 12223}
	if len(mix.Entries) != 6 || mix.Radius != 5 {
		t.Fatalf("mix: %v radius %g", mix.Entries, mix.Radius)
	}
	for i, e := range wantMix {
		if mix.Entries[i] != e {
			t.Fatalf("mix entries: %v, want %v", mix.Entries, wantMix)
		}
	}
	// The lone lurker's spawn is the mix's now; no other group of the leg lists it.
	for gi, g := range l0.PullGroups {
		for _, q := range g.Positions {
			if groundDist(q, Point{X: 765.097, Y: -112.538, Z: -57.4167}) < 1 && gi != 38 {
				t.Fatalf("leg 0 group %d still lists the folded-in lurker at %v", gi, q)
			}
		}
	}
	// Each of the four carries its patrol block and exactly one approach stand.
	for i, g := range []PullGroup{slime, sisters, sludge, mix} {
		if g.Patrol == nil {
			t.Fatalf("leg 0 group %d has no patrol block", 35+i)
		}
		if len(g.Approach) != 1 {
			t.Fatalf("leg 0 group %d approach: %v", 35+i, g.Approach)
		}
	}
	if got := slime.Patrol.Entries; len(got) != 1 || got[0] != 12221 {
		t.Fatalf("slime patrol entries: %v", got)
	}
	if got := mix.Patrol.Entries; len(got) != 1 || got[0] != 12237 {
		t.Fatalf("mix patrol entries: %v", got)
	}
	wantStands := []Point{
		{X: 775, Y: -170, Z: -56.6},
		{X: 812.5, Y: -152.5, Z: -74.9},
		{X: 775, Y: -162.5, Z: -56.3},
		{X: 765, Y: -135, Z: -56.3},
	}
	groups := []PullGroup{slime, sisters, sludge, mix}
	for i, g := range groups {
		if g.Approach[0] != wantStands[i] {
			t.Fatalf("leg 0 group %d stand: %v, want %v", 35+i, g.Approach[0], wantStands[i])
		}
	}
	// Each stand is 15-25 yd from its own pack (nearest member) and 20+ yd from
	// every pack pulled later in the leg.
	for i, g := range groups {
		stand := g.Approach[0]
		own := math.MaxFloat64
		for _, q := range g.Positions {
			if d := groundDist(stand, q); d < own {
				own = d
			}
		}
		if own < 15 || own > 25 {
			t.Fatalf("leg 0 group %d stand is %.1f yd from its pack, want 15-25", 35+i, own)
		}
		for gj := 35 + i + 1; gj < len(l0.PullGroups); gj++ {
			for _, q := range l0.PullGroups[gj].Positions {
				if d := groundDist(stand, q); d < laterGroupClearYd {
					t.Fatalf("leg 0 group %d stand %v is %.1f yd from later group %d's spawn %v", 35+i, stand, d, gj, q)
				}
			}
		}
	}
	// The mix's Corruptor and the Sisters are covered at plan level (no per-group
	// kill order exists): kill_first 12217, cc_priority 11794.
	found := false
	for _, e := range p.KillFirst {
		if e == 12217 {
			found = true
		}
	}
	if !found {
		t.Fatalf("plan kill_first %v does not cover the mix's Corruptor 12217", p.KillFirst)
	}
	found = false
	for _, e := range p.CCPriority {
		if e == 11794 {
			found = true
		}
	}
	if !found {
		t.Fatalf("plan cc_priority %v does not cover the Sisters 11794", p.CCPriority)
	}
}
