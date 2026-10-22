package dungeon

import (
	"testing"
)

// TestShadowfangKeepPackFixes holds the pack-census fixes of PREP-SFK-2026-10-22.md ("Fixes applied") to the
// shadowfang_keep draft: the boss-side packs are the boss rows' first (Rethilgore's cell, Razorclaw's yard pair,
// Odo's guards from leg 0), the Wolfguard inside Silverlaine's west pull is its entry, the court's east half is
// three pulls, and the radii are cut to the groups' own members. Every pin fails when its fix is cut.
func TestShadowfangKeepPackFixes(t *testing.T) {
	p, err := Load("../../dungeons/shadowfang_keep.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Legs) != 8 {
		t.Fatalf("legs: %d", len(p.Legs))
	}
	l0, l1, l2, l4 := p.Legs[0], p.Legs[1], p.Legs[2], p.Legs[4]
	if n := len(l0.PullGroups); n != 4 {
		t.Fatalf("leg 0 pull groups: %d (the guards' pull and the cell tail's are gone)", n)
	}
	if first := l0.Boss.First; len(first) != 2 || first[0] != 3861 || first[1] != 3851 {
		t.Fatalf("rethilgore boss first: %v", first)
	}
	// The gallery pull keeps its four and no spawn of the cell tail; radius 14.
	g1 := l0.PullGroups[1]
	if len(g1.Positions) != 4 || g1.Radius != 14 {
		t.Fatalf("leg 0 pull group 2: %d positions, radius %g", len(g1.Positions), g1.Radius)
	}
	for _, q := range g1.Positions {
		if q.Z < 86 {
			t.Fatalf("leg 0 pull group 2 keeps a cell-tail spawn at z %.1f", q.Z)
		}
	}
	// The guards (3866, 3868) are in no pull group of any leg and are Odo's boss row's entries.
	for li, l := range p.Legs {
		for _, g := range l.PullGroups {
			for _, e := range g.Entries {
				if e == 3866 || e == 3868 {
					t.Fatalf("leg %d: a pull group lists Odo's guard %d", li, e)
				}
			}
		}
	}
	if first := p.Legs[4].Boss.First; len(first) != 2 || first[0] != 3866 || first[1] != 3868 {
		t.Fatalf("odo boss first: %v", first)
	}
	// Leg 1: the pack in front of Razorclaw is his boss row's entries (a rat and two Servitors), no pull of its own.
	if first := l1.Boss.First; len(first) != 2 || first[0] != 3875 || first[1] != 2110 {
		t.Fatalf("razorclaw boss first: %v", first)
	}
	if n := len(l1.PullGroups); n != 16 {
		t.Fatalf("leg 1 pull groups: %d", n)
	}
	merged := l1.PullGroups[4]
	if len(merged.Entries) != 3 || merged.Radius != 8 {
		t.Fatalf("leg 1 merged yard pull: entries %v radius %g", merged.Entries, merged.Radius)
	}
	pen := l1.PullGroups[14]
	if pen.Radius != 17 {
		t.Fatalf("steed pen radius: %g", pen.Radius)
	}
	// Leg 2: the court's east half is three pulls (4/4/8), the west Darksouls' radius 11.
	if n := len(l2.PullGroups); n != 21 {
		t.Fatalf("leg 2 pull groups: %d", n)
	}
	if l2.PullGroups[0].Radius != 27 {
		t.Fatalf("leg 2 west court radius: %g", l2.PullGroups[0].Radius)
	}
	if l2.PullGroups[2].Radius != 11 {
		t.Fatalf("leg 2 darksoul trio radius: %g", l2.PullGroups[2].Radius)
	}
	for gi, want := range map[int]int{7: 14, 8: 18, 9: 24} {
		if l2.PullGroups[gi].Radius != float32(want) {
			t.Fatalf("leg 2 court pull %d radius: %g", gi, l2.PullGroups[gi].Radius)
		}
	}
	// Leg 4: the cut radii.
	if l4.PullGroups[1].Radius != 12 || l4.PullGroups[3].Radius != 19 ||
		l4.PullGroups[8].Radius != 17 || l4.PullGroups[9].Radius != 11 || l4.PullGroups[13].Radius != 10 {
		t.Fatalf("leg 4 radii: %g %g %g %g %g", l4.PullGroups[1].Radius, l4.PullGroups[3].Radius,
			l4.PullGroups[8].Radius, l4.PullGroups[9].Radius, l4.PullGroups[13].Radius)
	}
	// The plan-level lists from the census.
	if len(p.CCPriority) != 3 || p.CCPriority[0] != 3855 || p.CCPriority[1] != 61970 || p.CCPriority[2] != 10000 {
		t.Fatalf("cc_priority: %v", p.CCPriority)
	}
	if len(p.Fleers) != 0 || len(p.CCAlways) != 0 || len(p.KillFirst) != 0 {
		t.Fatalf("fleers/cc_always/kill_first: %v %v %v", p.Fleers, p.CCAlways, p.KillFirst)
	}
}
