// The Jin'do fixes (spec TortoiseBots-audit/specs/jindo-mechanics.md §6, plan worker step 1):
// the plan's kill_first leads with the Brain Wash Totem 15112 and the Powerful Healing Ward 14987,
// the honour-guard group is a stand pull shot from its approach 42 yd from Jin'do, and every leg-6
// route segment from group 8's approach to the boss row clears each of the ten Sacrificed Troll
// spawns (guids 49078-49087, the dropped pit pull group: respawn 10 s) by the route-aggro model's
// own reach (routeReach: AggroRadius at the plan's party level plus the raid spread and the
// spawn's wander). The pit pull group is gone.
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

// TestZgJindoRouteClearOfTrolls: every leg-6 route segment from group 8's approach (the old
// group-9 slot, the Jin'do proof placement) to the boss row clears EACH of the ten Sacrificed
// Troll spawns (guids 49078-49087) by the route-aggro model's own reach (routeReach, sampled
// every routeAggroStep yd like RouteIntoLaterPacks). The pit-centre rule it replaces measured
// the wrong thing: the group-8 approach passed ~21.1 yd from the nearest troll spawn while
// keeping 22 yd from the pit centre, and the model (aggro radius + 10 yd raid spread + wander)
// flags it. It fails today on any segment the walk bends past the pit.
func TestZgJindoRouteClearOfTrolls(t *testing.T) {
	p, err := dungeon.Load(zgPlan)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := readCreatures("testdata/map309/creatures.tsv")
	if err != nil {
		t.Fatal(err)
	}
	var trolls []creature
	for _, c := range cs {
		if c.Entry == 14826 {
			trolls = append(trolls, c)
		}
	}
	if len(trolls) != 10 {
		t.Fatalf("%d Sacrificed Troll spawns, want 10 (guids 49078-49087)", len(trolls))
	}
	res, err := planexport.Build(p, planexport.Options{})
	if err != nil {
		t.Fatalf("plan-export: %v", err)
	}
	// The walked line is the model's party-point walk, not the raw rows: a stand-pull
	// pull row is fought from its stand (the move row right before it, FlagStandPull),
	// so its party point is the stand's point (routePoints' rule; the clearance below
	// still calls the model's routeReach per spawn, it does not re-implement it).
	type pt struct{ x, y, z float64 }
	var walk []pt
	started := false
	steps := res.Rows.Steps
	for si, s := range steps {
		if s.Leg != 6 {
			continue
		}
		if s.Kind == planexport.KindMove && s.Comment == "pull group 8 approach" {
			started = true
		}
		if !started {
			continue
		}
		px, py, pz := float64(s.X), float64(s.Y), float64(s.Z)
		if (s.Kind == planexport.KindPull || s.Kind == planexport.KindBoss) && s.Flags&planexport.FlagStandPull != 0 && si > 0 {
			if prev := steps[si-1]; prev.Leg == s.Leg &&
				(prev.Kind == planexport.KindMove || prev.Kind == planexport.KindMoveDirect || prev.Kind == planexport.KindMoveHopExact) {
				px, py, pz = float64(prev.X), float64(prev.Y), float64(prev.Z)
			}
		}
		walk = append(walk, pt{px, py, pz})
		if s.Kind == planexport.KindBoss {
			break
		}
	}
	if !started {
		t.Fatal("no pull group 8 approach row in the exported leg 6 (the Jin'do proof placement)")
	}
	for i := 0; i+1 < len(walk); i++ {
		a, b := walk[i], walk[i+1]
		if a == b {
			continue
		}
		dx, dy, dz := b.x-a.x, b.y-a.y, b.z-a.z
		n := int(math.Ceil(math.Sqrt(dx*dx+dy*dy+dz*dz) / routeAggroStep))
		if n < 1 {
			n = 1
		}
		for _, c := range trolls {
			reach := routeReach(p, c)
			best := math.MaxFloat64
			for k := 0; k <= n; k++ {
				f := float64(k) / float64(n)
				sx, sy, sz := a.x+f*dx, a.y+f*dy, a.z+f*dz
				ddx, ddy, ddz := float64(c.Pos.X)-sx, float64(c.Pos.Y)-sy, float64(c.Pos.Z)-sz
				if d := math.Sqrt(ddx*ddx + ddy*ddy + ddz*ddz); d < best {
					best = d
				}
			}
			if best < reach {
				t.Errorf("leg 6 segment %d->%d passes %.1f yd from %s (guid %d) at %.1f %.1f %.1f, want >= %.1f (the route-aggro reach)",
					i, i+1, best, c.Name, c.GUID, c.Pos.X, c.Pos.Y, c.Pos.Z, reach)
			}
		}
	}
}
