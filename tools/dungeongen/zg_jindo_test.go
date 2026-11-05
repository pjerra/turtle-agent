// The Jin'do fixes (spec TortoiseBots-audit/specs/jindo-mechanics.md §6, plan worker step 1, and the
// walk-38 review rounds): the plan's kill_first leads with the Brain Wash Totem 15112 and the Powerful
// Healing Ward 14987, the hall's east side (the honour guard, the north camp's east four and the two lone
// mistresses) is its own four gm-clearable pull groups again (round 2 undid the boss-row fold; each pull is
// over its original ceiling by a pinned known limit, see the plan note's WALK 38 REVIEW R2), and every leg-6
// route segment to the boss row clears each of the ten Sacrificed Troll spawns (guids 49078-49087, the
// dropped pit pull group: respawn 10 s) by the route-aggro model's own reach (routeReach: AggroRadius at the
// plan's party level plus the raid spread and the spawn's wander). The pit pull group is gone.
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

// TestZgJindoHallIsItsOwnPullGroups: the walk-38 review round 2 (A-1) undoes the boss-row fold. The hall's
// east side is four pull groups again, each gm-clearable on its own: the honour guard's eight (49421's
// formation, max_bodies 14), the north camp (the east four 1173078-81 and the mistresses 49383-86,
// max_bodies 10), the lone 49403 and the lone 49402 (max_bodies 1 each). Their stands keep the walk-38-fix-1
// staging (35.9-36.6 yd from Jin'do, outside his 35 yd route reach), and the Jin'do boss row carries no First
// list and no radius. The geometry that makes each pull exceed its own ceiling and wake him is in the plan
// note's WALK 38 REVIEW R2; zg_w38_test.go pins the body counts with the run leader's real assist model (H1),
// the lone mistresses' ownership (A-6) and every pre-boss stand's distance from him.
func TestZgJindoHallIsItsOwnPullGroups(t *testing.T) {
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
	var jindo *creature
	for i := range cs {
		if cs[i].Entry == 11380 {
			jindo = &cs[i]
		}
	}
	if jindo == nil {
		t.Fatal("no Jin'do spawn in the map-309 export")
	}
	bossReach := routeReach(p, *jindo)
	if math.Abs(bossReach-35) > 0.01 {
		t.Fatalf("Jin'do's reach is %.1f yd, want 35 (25 aggro at party level 58 + 10 raid spread)", bossReach)
	}
	packs := zgPacks(t, *leg, cs)
	want := []struct {
		group int
		guids []uint64
		max   int
	}{
		{8, []uint64{49421, 49573, 49574, 49575, 49576, 49577, 49609, 49650}, 14},         // the honour guard's eight
		{9, []uint64{49383, 49384, 49385, 49386, 1173078, 1173079, 1173080, 1173081}, 10}, // the north camp
		{10, []uint64{49403}, 1},
		{11, []uint64{49402}, 1},
	}
	hall := map[uint64]bool{}
	for _, w := range want {
		for _, guid := range w.guids {
			hall[guid] = true
		}
		if w.group >= len(leg.PullGroups) {
			t.Errorf("the leg has %d pull groups, not the hall's group %d", len(leg.PullGroups), w.group)
			continue
		}
		g := leg.PullGroups[w.group]
		if !g.StandPull {
			t.Errorf("pull group %d (the hall's east side) is not a stand pull: the party would walk to the pack", w.group)
		}
		if int(g.MaxBodies) != w.max {
			t.Errorf("pull group %d: max_bodies is %d, want its original ceiling %d (do not raise a ceiling to fit the known limit)", w.group, g.MaxBodies, w.max)
		}
		got := map[uint64]bool{}
		for _, c := range packs[w.group] {
			got[c.GUID] = true
		}
		for _, guid := range w.guids {
			if !got[guid] {
				t.Errorf("pull group %d does not hold %d: the hall's east side must be its own gm-clearable pack", w.group, guid)
			}
		}
		if len(got) != len(w.guids) {
			t.Errorf("pull group %d holds %d units, want its own %d", w.group, len(got), len(w.guids))
		}
		if len(g.Approach) == 0 {
			t.Errorf("pull group %d has no stand", w.group)
			continue
		}
		stand := g.Approach[len(g.Approach)-1]
		if d := dist(stand, jindo.Pos); d < bossReach {
			t.Errorf("pull group %d's stand %.1f %.1f %.1f is %.1f yd from Jin'do, want >= %g: no pre-boss stand reaches him", w.group, stand.X, stand.Y, stand.Z, d, bossReach)
		}
	}
	// No other pull group holds the hall's east side: the fold's boss-row scope is gone, and the packs are not
	// split across rows (a GM clear of one group must kill one whole pack).
	for gi := range leg.PullGroups {
		if gi == 8 || gi == 9 || gi == 10 || gi == 11 {
			continue
		}
		for _, c := range packs[gi] {
			if hall[c.GUID] {
				t.Errorf("pull group %d also holds %s %d: the hall's east side belongs to groups 8-11 only", gi, c.Name, c.GUID)
			}
		}
	}
	if leg.Boss == nil || leg.Boss.Entry != 11380 {
		t.Fatalf("the leg's boss is %+v, want Jin'do 11380", leg.Boss)
	}
	if len(leg.Boss.First) != 0 || leg.Boss.Radius != 0 {
		t.Errorf("the Jin'do boss row carries First %v and radius %g: the hall's east side is its own pull groups, not the boss's adds", leg.Boss.First, leg.Boss.Radius)
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
