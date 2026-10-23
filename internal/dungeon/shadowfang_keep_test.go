package dungeon

import (
	"strings"
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
	if len(p.Legs) != 10 {
		t.Fatalf("legs: %d", len(p.Legs))
	}
	l0, l1, l2, l5, l6 := p.Legs[0], p.Legs[1], p.Legs[2], p.Legs[5], p.Legs[6]
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
	if first := l6.Boss.First; len(first) != 2 || first[0] != 3866 || first[1] != 3868 {
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
	// Leg 5 (Odo's entrance-side pulls, the old leg 4 without its walkway groups): the cut radii.
	if l5.PullGroups[1].Radius != 12 || l5.PullGroups[3].Radius != 19 ||
		l5.PullGroups[8].Radius != 17 || l5.PullGroups[9].Radius != 10 {
		t.Fatalf("leg 5 radii: %g %g %g %g", l5.PullGroups[1].Radius, l5.PullGroups[3].Radius,
			l5.PullGroups[8].Radius, l5.PullGroups[9].Radius)
	}
	if n := len(l5.PullGroups); n != 10 {
		t.Fatalf("leg 5 pull groups: %d (the four walkway groups belong to the leg past the seam)", n)
	}
	if l5.Boss != nil {
		t.Fatalf("leg 5 has a boss: Odo moved past the seam")
	}
	// The plan-level lists from the census.
	if len(p.CCPriority) != 3 || p.CCPriority[0] != 3855 || p.CCPriority[1] != 61970 || p.CCPriority[2] != 10000 {
		t.Fatalf("cc_priority: %v", p.CCPriority)
	}
	if len(p.Fleers) != 0 || len(p.CCAlways) != 0 || len(p.KillFirst) != 0 {
		t.Fatalf("fleers/cc_always/kill_first: %v %v %v", p.Fleers, p.CCAlways, p.KillFirst)
	}
}

// TestShadowfangKeepDoorAndSeam holds the 2026-10-23 data fix (r24 shadowfang_keep slot7, PREP-SFK + NAVMESH-GAPS):
// the Courtyard Door 18895 opens by a gossip step on Deathstalker Adamant 3849 that runs before the courtyard-side
// pulls, and the mesh seam at the stair top is crossed by a move_direct; Odo's walkway pulls and his boss row follow
// the crossing, and the Fenrus, Nandos and Arugal legs come after. Every pin fails when a piece is cut.
func TestShadowfangKeepDoorAndSeam(t *testing.T) {
	p, err := Load("../../dungeons/shadowfang_keep.json")
	if err != nil {
		t.Fatal(err)
	}
	door := p.Legs[4]
	if door.Name != "courtyard_door" || len(door.PullGroups) != 0 || door.Boss != nil {
		t.Fatalf("leg 4: %q, %d pulls, boss %v", door.Name, len(door.PullGroups), door.Boss)
	}
	if len(door.Steps) != 2 {
		t.Fatalf("leg 4 steps: %d", len(door.Steps))
	}
	g := door.Steps[0].Gossip
	if g == nil || g.NPC != 3849 || g.Option == nil || *g.Option != 0 || g.Talker() != GossipByTank {
		t.Fatalf("the door leg's gossip: %+v", g)
	}
	if g.Stand == nil || dist3(*g.Stand, g.Near) > GossipRange {
		t.Fatalf("gossip stand %v vs near %v: over the %g yd range", g.Stand, g.Near, GossipRange)
	}
	w := door.Steps[1].WaitFor
	if w == nil || w.GameObject == nil || w.GameObject.Entry != 18895 || w.GameObject.Spawn != 20835 || w.GameObject.State != "active" || w.TimeoutS != 60 {
		t.Fatalf("the door leg's wait: %+v", w)
	}
	if !strings.HasPrefix(door.Name, "courtyard_door") {
		t.Fatalf("door leg name: %q", door.Name)
	}

	odo := p.Legs[6]
	if odo.Name != "odo_the_blindwatcher" || odo.Boss == nil || odo.Boss.Entry != 4279 {
		t.Fatalf("leg 6: %q boss %+v", odo.Name, odo.Boss)
	}
	if len(odo.Via) != 2 || odo.Via[0].Direct || !odo.Via[1].Direct {
		t.Fatalf("leg 6 via: %+v", odo.Via)
	}
	if !DirectStepOK(odo.Via[0].Point, odo.Via[1].Point) {
		t.Fatalf("the seam crossing %v -> %v is over the direct limits", odo.Via[0].Point, odo.Via[1].Point)
	}
	if d := dist3(odo.Via[0].Point, odo.Via[1].Point); d > 2.5 {
		t.Fatalf("the seam crossing is %.1f yd, not the measured 2.0", d)
	}
	if odo.Via[1].Point != odo.To {
		t.Fatalf("the direct step ends at %v but the leg's to is %v", odo.Via[1].Point, odo.To)
	}
	if n := len(odo.PullGroups); n != 4 {
		t.Fatalf("leg 6 pull groups: %d", n)
	}
	if odo.PullGroups[0].Entries[0] != 3873 || odo.PullGroups[1].Entries[0] != 3872 {
		t.Fatalf("leg 6 walkway pulls: %v %v", odo.PullGroups[0].Entries, odo.PullGroups[1].Entries)
	}
	for i, name := range []string{"fenrus_the_devourer", "wolf_master_nandos", "archmage_arugal"} {
		if p.Legs[7+i].Name != name || p.Legs[7+i].Boss == nil {
			t.Fatalf("leg %d: %q boss %+v", 7+i, p.Legs[7+i].Name, p.Legs[7+i].Boss)
		}
	}
}
