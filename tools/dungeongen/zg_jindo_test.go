// The Jin'do fixes (spec TortoiseBots-audit/specs/jindo-mechanics.md §6, plan worker step 1, and the
// walk-38 review): the plan's kill_first leads with the Brain Wash Totem 15112 and the Powerful Healing
// Ward 14987, the hall's east side (the honour guard, the north camp's east four and the two lone
// mistresses) is the boss row's add list and radius rather than pull rows (the 10 yd family assist of any
// guard pull wakes them, and they live 6-17 yd from Jin'do), and every leg-6 route segment to the boss row
// clears each of the ten Sacrificed Troll spawns (guids 49078-49087, the dropped pit pull group: respawn
// 10 s) by the route-aggro model's own reach (routeReach: AggroRadius at the plan's party level plus the
// raid spread and the spawn's wander). The pit pull group is gone.
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

// TestZgJindoGuardIsTheBossRowsAdds: the walk-38 review's H1/H2. The honour guard's eight (formation
// 49421) and the two lone Withered Mistresses 49402/49403 stand 6-17 yd from Jin'do. The guard's nearest
// unit is 5.9 yd from 49402, so the 10 yd family assist (CreatureFamilyAssistanceRadius) of any guard pull
// wakes the mistresses, and their assist reaches Jin'do from a party point inside their own 22+10 yd reach:
// with the real assist model no guard stand keeps them out (rows 56/58/61/64 of the walk-38 export brought
// him). So no pre-boss row pulls them: the plan has no pull group for 49421/49402/49403, and the Jin'do
// boss row's First list and radius scope them, i.e. they are pulled with him. zg_w38_test.go pins the
// exported rows (the boss row owns each unit; no earlier row brings him or more bodies than its ceiling).
func TestZgJindoGuardIsTheBossRowsAdds(t *testing.T) {
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
	for gi := range leg.PullGroups {
		for _, m := range packs[gi] {
			lead := m.GroupLeader
			if lead == 0 {
				lead = m.GUID
			}
			if lead == 49421 || m.GUID == 49402 || m.GUID == 49403 {
				t.Errorf("pull group %d holds %s %d: the hall's east side is pulled with Jin'do by the boss row, never before it", gi, m.Name, m.GUID)
			}
		}
	}
	if leg.Boss == nil || leg.Boss.Entry != 11380 {
		t.Fatalf("the leg's boss is %+v, want Jin'do 11380", leg.Boss)
	}
	first := map[uint32]bool{}
	for _, e := range leg.Boss.First {
		first[e] = true
	}
	for _, e := range []uint32{14825, 14882, 14883} {
		if !first[e] {
			t.Errorf("the Jin'do boss row's First list is %v, missing %d: the guard and mistresses are not scoped to his row", leg.Boss.First, e)
		}
	}
	if leg.Boss.Radius < 47 {
		t.Errorf("the Jin'do boss row's radius is %g, want >= 47 (the north camp's 49385 stands 45.8 yd from him and comes with him)", leg.Boss.Radius)
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
