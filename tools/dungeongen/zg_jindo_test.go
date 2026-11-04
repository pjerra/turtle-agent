// The Jin'do fixes (spec TortoiseBots-audit/specs/jindo-mechanics.md §6, plan worker step 1):
// the plan's kill_first leads with the Brain Wash Totem 15112 and the Powerful Healing Ward 14987,
// the honour-guard group is a stand pull shot from its approach 42 yd from Jin'do, and every leg-6
// route point from group 8's approach (the old group-9 slot) to the boss row keeps 22 yd (2D) from
// the pit centre (-11583, -1251). The pit pull group (Sacrificed Trolls, respawn 10 s) is gone.
package main

import (
	"math"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// TestZgJindoKillFirstTotemWardFirst: the plan's kill_first opens with the Brain Wash Totem 15112
// (a charmed bot kills raid members) and the Powerful Healing Ward 14987 (3 333 hp/s out-heals the
// raid's 2.2-3.0 k dps), then the old order unchanged.
func TestZgJindoKillFirstTotemWardFirst(t *testing.T) {
	p, err := dungeon.Load(zgPlan)
	if err != nil {
		t.Fatal(err)
	}
	want := []uint32{15112, 14987, 14750, 15146, 11830, 14883, 11353}
	if len(p.KillFirst) != len(want) {
		t.Fatalf("kill_first is %v, want %v (the Jin'do totem and ward first)", p.KillFirst, want)
	}
	for i, e := range want {
		if p.KillFirst[i] != e {
			t.Fatalf("kill_first is %v, want %v (the Jin'do totem and ward first)", p.KillFirst, want)
		}
	}
}

// TestZgJindoGuardIsStandPull: the honour-guard group (the 49421 formation with 49402/49403,
// now group 10 after the pit group's drop) is a stand pull with max_bodies 10, shot from its
// approach (-11558, -1276, 78.06), 42.2 yd from Jin'do, so the fight happens there and not 13 yd
// from him. It fails today (plain pull, fought where it stands 10-20 yd from Jin'do).
func TestZgJindoGuardIsStandPull(t *testing.T) {
	p, err := dungeon.Load(zgPlan)
	if err != nil {
		t.Fatal(err)
	}
	var leg *dungeon.Leg
	for i := range p.Legs {
		if p.Legs[i].Name == "jindo_the_hexxer" {
			leg = &p.Legs[i]
		}
	}
	if leg == nil {
		t.Fatal("no jindo_the_hexxer leg")
	}
	cs, err := readCreatures("testdata/map309/creatures.tsv")
	if err != nil {
		t.Fatal(err)
	}
	packs := zgPacks(t, *leg, cs)
	found := -1
	for gi, g := range leg.PullGroups {
		leads := map[uint64]bool{}
		for _, m := range packs[gi] {
			lead := m.GroupLeader
			if lead == 0 {
				lead = m.GUID
			}
			leads[lead] = true
		}
		if !leads[49421] || (!leads[49402] && !leads[49403]) {
			continue
		}
		found = gi
		if !g.StandPull {
			t.Errorf("pull group %d (the honour guard) is not a stand pull: it must be shot from its approach, not fought where it stands 10-20 yd from Jin'do", gi)
		}
		if g.MaxBodies != 10 {
			t.Errorf("pull group %d (the honour guard) has max_bodies %d, want 10", gi, g.MaxBodies)
		}
		if len(g.Approach) == 0 {
			t.Errorf("pull group %d (the honour guard) has no approach (no stand to shoot from)", gi)
			continue
		}
		stand := g.Approach[len(g.Approach)-1]
		jindo := dungeon.Point{X: -11515.8, Y: -1275.48, Z: 79.66}
		if d := dist(stand, jindo); d < 35 {
			t.Errorf("pull group %d (the honour guard) stand %.1f %.1f %.1f is %.1f yd from Jin'do, want >= 35 (shot from 42 yd, not fought 13 yd from him)", gi, stand.X, stand.Y, stand.Z, d)
		}
	}
	if found < 0 {
		t.Fatal("no pull group owns the 49421 honour-guard formation (with 49402/49403)")
	}
}

// TestZgJindoRouteClearOfPit: every leg-6 route point from group 8's approach (the old group-9 slot,
// the Jin'do proof placement) to the boss row keeps at least 22 yd (2D) from the pit centre
// (-11583.0, -1251.0): troll aggro 18 + 4 margin. It fails today on the old group-12 approach hop
// (-11576, -1232) at 20.2 yd, and the straight walk to it bottoms at 18.7 yd.
func TestZgJindoRouteClearOfPit(t *testing.T) {
	p, err := dungeon.Load(zgPlan)
	if err != nil {
		t.Fatal(err)
	}
	res, err := planexport.Build(p, planexport.Options{})
	if err != nil {
		t.Fatalf("plan-export: %v", err)
	}
	pit := dungeon.Point{X: -11583.0, Y: -1251.0}
	const pitClearYd = 22.0
	started := false
	for _, s := range res.Rows.Steps {
		if s.Leg != 6 {
			continue
		}
		if s.Kind == planexport.KindMove && s.Comment == "pull group 8 approach" {
			started = true
		}
		if !started {
			continue
		}
		if d := math.Hypot(float64(s.X-pit.X), float64(s.Y-pit.Y)); d < pitClearYd {
			t.Errorf("leg 6 seq %d (%s %s at %.1f %.1f %.1f) is %.1f yd from the pit centre, want >= %g",
				s.Seq, s.Kind, s.Comment, s.X, s.Y, s.Z, d, pitClearYd)
		}
		if s.Kind == planexport.KindBoss {
			break
		}
	}
	if !started {
		t.Fatal("no pull group 8 approach row in the exported leg 6 (the Jin'do proof placement)")
	}
}
