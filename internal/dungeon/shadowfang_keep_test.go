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

// TestShadowfangKeepStep39SplitAndStair holds the 2026-10-23 data fix (r25 shadowfang_keep slot5, the step-39 pull that
// wiped twice): the south court's six-unit pull (one row, radius 22.1, 12 units in the radius) is two pulls of three with
// explicit stands clear of every later pack, the rat is out of it, and the walk up to the upper court goes through the
// stair's real top as approach points. Every pin fails when a piece is cut.
func TestShadowfangKeepStep39SplitAndStair(t *testing.T) {
	p, err := Load("../../dungeons/shadowfang_keep.json")
	if err != nil {
		t.Fatal(err)
	}
	l2 := p.Legs[2]
	south, north := l2.PullGroups[4], l2.PullGroups[5]
	if len(south.Entries) != 3 || south.Radius != 8 || len(south.Approach) != 1 {
		t.Fatalf("south half: %v radius %g approach %v", south.Entries, south.Radius, south.Approach)
	}
	if south.Entries[0] != 3854 || south.Entries[1] != 3854 || south.Entries[2] != 61970 {
		t.Fatalf("south half entries: %v", south.Entries)
	}
	if len(north.Entries) != 3 || north.Radius != 8 || len(north.Approach) != 1 {
		t.Fatalf("north half: %v radius %g approach %v", north.Entries, north.Radius, north.Approach)
	}
	// The next group's Wolfguard (at -254.049 2355.36) is the north half's now; no other group lists it.
	for gi, g := range l2.PullGroups {
		for _, q := range g.Positions {
			if gi != 5 && groundDist(q, Point{X: -254.049, Y: 2355.36, Z: 96.8226}) < 1 {
				t.Fatalf("leg 2 group %d still lists the folded-in Wolfguard at %v", gi, q)
			}
			// The rat the run leader walked its tank 14.6 yd for is in no pull of the leg.
			if groundDist(q, Point{X: -250.311, Y: 2352.27, Z: 96.8225}) < 1 {
				t.Fatalf("leg 2 group %d still lists the rat at %v", gi, q)
			}
		}
	}
	// Both stands keep the 20-yd aggro reach clear of every spawn of every group pulled later (the run's wipe shape).
	for _, half := range []int{4, 5} {
		stand := l2.PullGroups[half].Approach[0]
		for gi := half + 1; gi < len(l2.PullGroups); gi++ {
			for _, q := range l2.PullGroups[gi].Positions {
				if d := groundDist(stand, q); d < laterGroupClearYd {
					t.Fatalf("the south/north stand %v is %.1f yd from the later group %d's spawn %v", stand, d, gi, q)
				}
			}
		}
	}
	// The stair: the upper court's pull carries the walk up the server's own path (turn, top, landing), in order and
	// within the run leader's reach of the pull's centre.
	stair := l2.PullGroups[2].Approach
	want := []Point{{X: -287.2, Y: 2305.1, Z: 90.7}, {X: -284.3, Y: 2320.8, Z: 96.6}, {X: -275.5, Y: 2321.7, Z: 96.2}}
	if len(stair) != len(want) {
		t.Fatalf("pull group 3 approach: %v", stair)
	}
	for i := range want {
		if stair[i] != want[i] {
			t.Fatalf("stair point %d: %v, want %v", i, stair[i], want[i])
		}
	}
	if d := groundDist(stair[len(stair)-1], PackCentre(l2.PullGroups[2])); d > RunLeaderReachYd {
		t.Fatalf("the stair landing is %.1f yd from the pull's centre (over the run leader's reach)", d)
	}
	if z := stair[0].Z; z <= l2.To.Z || z >= stair[1].Z {
		t.Fatalf("the stair points do not climb: %v", stair)
	}
}

// TestShadowfangKeepFenrusStandAndVoidwalkers holds the 2026-10-24 data fix (r36 shadowfang_keep slotA + r30 slotB):
// the boss step 304 found Fenrus 'alive 0.0 yd from his spot' with the party at z95 while he stands at z128.8 behind
// the shut Sorcerer's Gate, and the tank could not reach him from -174 2175 z129. The boss approach walks the party
// up to his own floor and ends on the ranged stand -152 2168 z128.7 (16.4 yd from him, line of sight through the
// gate arch), and the after_boss waits hold the party until the 4 voidwalkers (4627) are dead and the gate reads
// active. Every pin fails when a piece is cut.
func TestShadowfangKeepFenrusStandAndVoidwalkers(t *testing.T) {
	p, err := Load("../../dungeons/shadowfang_keep.json")
	if err != nil {
		t.Fatal(err)
	}
	leg := p.Legs[7]
	if leg.Name != "fenrus_the_devourer" || leg.Boss == nil || leg.Boss.Entry != 4274 {
		t.Fatalf("leg 7: %q boss %+v", leg.Name, leg.Boss)
	}
	want := []Point{{X: -145.07, Y: 2182.13, Z: 110.3}, {X: -150, Y: 2175, Z: 128.27}, {X: -152, Y: 2168, Z: 128.69}}
	got := leg.Boss.Approach
	if len(got) != len(want) {
		t.Fatalf("fenrus boss approach: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("fenrus approach point %d: %v, want %v", i, got[i], want[i])
		}
	}
	// The stand ends on Fenrus' own floor, 15-25 yd from him: a ranged stand, not a melee walk-up.
	stand := got[len(got)-1]
	if dz := stand.Z - leg.Boss.Position.Z; dz < -1 || dz > 1 {
		t.Fatalf("the stand %v is off Fenrus' floor (his z %g)", stand, leg.Boss.Position.Z)
	}
	if d := dist3(stand, leg.Boss.Position); d < 15 || d > 25 {
		t.Fatalf("the stand %v is %.1f yd from Fenrus: a 15-25 yd ranged stand", stand, d)
	}
	// The approach climbs: the last point is two floors above the leg's low start.
	if got[len(got)-1].Z-got[0].Z < 10 {
		t.Fatalf("the approach does not climb to his floor: %v", got)
	}
	if len(leg.AfterBoss) != 2 {
		t.Fatalf("after_boss steps: %d", len(leg.AfterBoss))
	}
	hg := leg.AfterBoss[0].WaitFor
	if hg == nil || hg.HostilesGone == nil || hg.HostilesGone.Center == nil {
		t.Fatalf("after_boss[0] is no hostiles_gone wait with a centre: %+v", leg.AfterBoss[0].WaitFor)
	}
	// The voidwalker spawns (-147.1 2163.2, -155.4 2172.8, -148.9 2180.9, -140.2 2175.3, z128.5-128.7) all lie in the radius.
	for _, v := range []Point{{X: -147.059, Y: 2163.19, Z: 128.696}, {X: -155.352, Y: 2172.78, Z: 128.448}, {X: -148.869, Y: 2180.86, Z: 128.448}, {X: -140.203, Y: 2175.26, Z: 128.448}} {
		if d := dist3(*hg.HostilesGone.Center, v); d > float64(hg.HostilesGone.Radius) {
			t.Fatalf("voidwalker spawn %v is %.1f yd from the wait centre (radius %g)", v, d, hg.HostilesGone.Radius)
		}
	}
	gate := leg.AfterBoss[1].WaitFor
	if gate == nil || gate.GameObject == nil || gate.GameObject.Entry != 18972 || gate.GameObject.State != "active" {
		t.Fatalf("after_boss[1] is no wait for the Sorcerer's Gate 18972 active: %+v", leg.AfterBoss[1].WaitFor)
	}
}
