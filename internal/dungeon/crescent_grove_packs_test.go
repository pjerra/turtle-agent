package dungeon

import (
	"fmt"
	"math"
	"testing"
)

// The pack fixes of the Crescent Grove draft (2026-10-23; ~/TortoiseBots-audit/PREP-CRESCENT-GROVE-2026-10-23.md,
// section "Fixes applied"), made from the world export of map 802 before any live run. Each test fails when its part is
// taken out of the plan.
const crescentGrovePlan = "../../dungeons/crescent_grove.json"

func crescentLeg(t *testing.T, p *Plan, name string) Leg {
	t.Helper()
	for _, l := range p.Legs {
		if l.Name == name {
			return l
		}
	}
	t.Fatalf("no leg %s", name)
	return Leg{}
}

func distYd(a, b Point) float64 {
	return math.Sqrt(float64((a.X-b.X)*(a.X-b.X) + (a.Y-b.Y)*(a.Y-b.Y) + (a.Z-b.Z)*(a.Z-b.Z)))
}

// The census of map 802: the only polymorphable healer is the Shaman 92101, the heal-curse/fear/mana-burn/nuke casters
// are the kill_first list, the demons, elementals and undead that Polymorph cannot take are no_cc, and no entry has an
// EventAI flee.
func TestCrescentGroveUnitListsComeFromTheCensus(t *testing.T) {
	p := loadPlan(t, crescentGrovePlan)
	for _, c := range []struct {
		name      string
		got, want string
	}{
		{"fleers", fmt.Sprint(p.Fleers), "[]"},
		{"cc_priority", fmt.Sprint(p.CCPriority), "[92101]"},
		{"cc_always", fmt.Sprint(p.CCAlways), "[92101]"},
		{"kill_first", fmt.Sprint(p.KillFirst), "[92124 92123 92119 92125 92126 92114 92121]"},
		{"no_cc", fmt.Sprint(p.NoCC), "[92112 92113 92116 92117 92118 92120 92122 92127]"},
	} {
		if c.got != c.want {
			t.Errorf("%s %s, want %s", c.name, c.got, c.want)
		}
	}
}

// The 14 Deer 883 are critters (faction 31, friendly): no pull group may name one.
func TestCrescentGroveHasNoCrittersInThePulls(t *testing.T) {
	p := loadPlan(t, crescentGrovePlan)
	groups := 0
	for _, l := range p.Legs {
		for i, g := range l.PullGroups {
			groups++
			for _, e := range g.Entries {
				if e == 883 {
					t.Errorf("leg %s pull group %d names Deer 883", l.Name, i)
				}
			}
			if len(g.Entries) > 5 {
				t.Errorf("leg %s pull group %d has %d members", l.Name, i, len(g.Entries))
			}
		}
	}
	if groups != 299 {
		t.Errorf("%d pull groups, want 299", groups)
	}
}

// A boss sees 20 yd: no pull member may stand within 25 yd of any of the five bosses. The Elders 92105/92106 stood 11 yd
// from Grovetender Engryss and are his first instead; the Deer pull 9 yd from Keeper Ranathos is gone.
func TestCrescentGroveNoPackStandsWithABoss(t *testing.T) {
	p := loadPlan(t, crescentGrovePlan)
	bosses := map[uint32]Point{}
	for _, l := range p.Legs {
		if l.Boss != nil {
			bosses[l.Boss.Entry] = l.Boss.Position
		}
	}
	if len(bosses) != 5 {
		t.Fatalf("%d bosses", len(bosses))
	}
	for _, l := range p.Legs {
		for i, g := range l.PullGroups {
			for _, q := range g.Positions {
				for entry, b := range bosses {
					if d := distYd(q, b); d < 25 {
						t.Errorf("leg %s pull group %d: a member stands %.1f yd from boss %d", l.Name, i, d, entry)
					}
				}
			}
		}
	}
	e := crescentLeg(t, p, "grovetender_engryss")
	if e.Boss == nil || fmt.Sprint(e.Boss.First) != "[92105 92106]" {
		t.Errorf("grovetender_engryss first %+v, want [92105 92106]", e.Boss)
	}
	for i, g := range e.PullGroups {
		for _, q := range g.Positions {
			if distYd(q, e.Boss.Position) < 25 {
				t.Errorf("grovetender_engryss pull group %d still stands with the boss", i)
			}
		}
	}
}

// The two Roaming Felguards 92120 patrol (creature_movement 2566199 and 2566320): the pack at 2566320 is one group of
// four (it was two overlapping pulls) and both carry the path and an estimated period. No other group has a patrol.
func TestCrescentGrovePatrolsAreOwned(t *testing.T) {
	p := loadPlan(t, crescentGrovePlan)
	n := 0
	for _, l := range p.Legs {
		for i, g := range l.PullGroups {
			if g.Patrol == nil {
				continue
			}
			n++
			if len(g.Patrol.Path) < 2 || g.Patrol.PeriodS <= 0 || !g.Patrol.PeriodEstimate {
				t.Errorf("leg %s pull group %d patrol %+v", l.Name, i, g.Patrol)
			}
		}
	}
	if n != 2 {
		t.Errorf("%d patrol blocks, want 2", n)
	}
	for i, g := range crescentLeg(t, p, "high_priestess_a'lathea").PullGroups {
		if g.Patrol != nil {
			if len(g.Entries) != 4 || len(g.Positions) != 4 || g.Radius != 10 {
				t.Errorf("leg 2 pull group %d: the patrol pack is %v radius %v, want 4 entries and radius 10", i, g.Entries, g.Radius)
			}
		}
	}
}

// The radius cuts: each group that the exporter's default radius (farthest member + 10) let reach the next pack keeps
// its own members only.
func TestCrescentGrovePullRadiiKeepTheNeighbourOut(t *testing.T) {
	p := loadPlan(t, crescentGrovePlan)
	for _, c := range []struct {
		leg  string
		x, y float32
		r    float32
	}{
		{"grovetender_engryss", 512.039, -96.566, 10},
		{"grovetender_engryss", 505.417, -123.434, 10},
		{"grovetender_engryss", 528.389, -119.464, 10},
		{"grovetender_engryss", 310.477, -29.7596, 10},
		{"grovetender_engryss", 215.456, -52.8876, 10},
		{"keeper_ranathos", 285.171, 60.2619, 13},
		{"keeper_ranathos", 312.26, 260.564, 10},
		{"keeper_ranathos", 201.794, 216.942, 16},
		{"fenektis_the_deceiver", -502.81, -76.615, 14},
	} {
		found := false
		for _, g := range crescentLeg(t, p, c.leg).PullGroups {
			if len(g.Positions) > 0 && g.Positions[0].X == c.x && g.Positions[0].Y == c.y {
				found = true
				if g.Radius != c.r {
					t.Errorf("leg %s group at %g %g: radius %v, want %v", c.leg, c.x, c.y, g.Radius, c.r)
				}
			}
		}
		if !found {
			t.Errorf("leg %s: no group at %g %g", c.leg, c.x, c.y)
		}
	}
}

// The bosses' interruptible casts (spell data: cast time, interrupt flags, prevention 1).
func TestCrescentGroveBossTactics(t *testing.T) {
	p := loadPlan(t, crescentGrovePlan)
	for _, c := range []struct {
		leg  string
		want []string
	}{
		{"keeper_ranathos", []string{"Wrath", "Regrowth"}},
		{"high_priestess_a'lathea", []string{"Starfire"}},
		{"master_raxxieth", []string{"Shadow Bolt", "Flamestrike"}},
	} {
		l := crescentLeg(t, p, c.leg)
		if l.Boss == nil || l.Boss.Tactics == nil || len(l.Boss.Tactics.Interrupt) != len(c.want) {
			t.Errorf("leg %s tactics %+v, want %v", c.leg, l.Boss, c.want)
			continue
		}
		for i, s := range l.Boss.Tactics.Interrupt {
			if s.String() != c.want[i] {
				t.Errorf("leg %s interrupt %d %s, want %s", c.leg, i, s, c.want[i])
			}
		}
	}
	for _, name := range []string{"grovetender_engryss", "fenektis_the_deceiver"} {
		if l := crescentLeg(t, p, name); l.Boss != nil && l.Boss.Tactics != nil {
			t.Errorf("leg %s: no interruptible boss cast exists, tactics %+v", name, l.Boss.Tactics)
		}
	}
}
