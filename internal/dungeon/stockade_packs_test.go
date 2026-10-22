package dungeon

import (
	"fmt"
	"testing"
)

// The pack fixes of the Stormwind Stockade draft (2026-10-22; ~/TortoiseBots-audit/PREP-STOCKADE-2026-10-22.md, section "Fixes applied"), made from the world
// export of map 34 and the Deadmines r12/r14 lessons, before any live run. Each test fails when its part is taken out of the plan.
const stockadePlan = "../../dungeons/stormwind_stockade.json"

func stockadeLeg(t *testing.T, p *Plan, name string) Leg {
	t.Helper()
	for _, l := range p.Legs {
		if l.Name == name {
			return l
		}
	}
	t.Fatalf("no leg %s", name)
	return Leg{}
}

// Every trash entry and three of the bosses flee at 15 % health (EventAI); the stunners and the Kicker are the CC entries, Hamhock the one caster.
func TestStockadeUnitListsComeFromTheCensus(t *testing.T) {
	p := loadPlan(t, stockadePlan)
	for _, c := range []struct {
		name      string
		got, want string
	}{
		{"fleers", fmt.Sprint(p.Fleers), "[1663 1666 1706 1707 1708 1711 1715 1716]"},
		{"cc_priority", fmt.Sprint(p.CCPriority), "[1711 1715 1706]"},
		{"cc_always", fmt.Sprint(p.CCAlways), "[1711]"},
		{"kill_first", fmt.Sprint(p.KillFirst), "[1717]"},
	} {
		if c.got != c.want {
			t.Errorf("%s %s, want %s", c.name, c.got, c.want)
		}
	}
}

// Bruegal Ironknuckle stands 5.7 yd from a pack member and the packs chain at about 10 yd, so he cannot be left out of the block: he is a member of the
// pull group he stands in (no longer a skipped rare that the pull drags in), and the pack east of him is two pairs, not one group of four.
func TestStockadeBruegalIsPartOfThePullNotASkip(t *testing.T) {
	p := loadPlan(t, stockadePlan)
	l := stockadeLeg(t, p, "targorr_the_dread")
	for _, e := range l.Skip {
		if e == 1720 {
			t.Errorf("leg 0 still skips 1720: he is in a pull group")
		}
	}
	n := 0
	for i, g := range l.PullGroups {
		for _, e := range g.Entries {
			if e == 1720 {
				n++
				if len(g.Entries) != 4 || g.Radius != 11 {
					t.Errorf("pull group %d with Bruegal: entries %v radius %v, want 4 entries and radius 11", i, g.Entries, g.Radius)
				}
			}
		}
		if len(g.Entries) > 4 {
			t.Errorf("pull group %d has %d entries", i, len(g.Entries))
		}
	}
	if n != 1 {
		t.Errorf("1720 is in %d pull groups, want 1", n)
	}
}

// The packs that stand with a boss are the boss's first, not pulls of their own: pulling them is the boss fight (and Dextren's, Hamhock's guards are a
// creature_group). Hamhock's guards are not his first because the first of a boss row reaches 30 yd, which holds the Insurgent of Bazil's pair.
func TestStockadeBossPacksAreTheBossFight(t *testing.T) {
	p := loadPlan(t, stockadePlan)
	for _, c := range []struct {
		leg   string
		boss  uint32
		first string
		pulls int
	}{
		{"targorr_the_dread", 1696, "[1706 1707]", 14},
		{"dextren_ward", 1663, "[1706 1708]", 8},
		{"kam_deepfury", 1666, "[1715 1708]", 9},
		{"hamhock", 1717, "[]", 3},
		{"bazil_thredd", 1716, "[1715 1711]", 0},
	} {
		l := stockadeLeg(t, p, c.leg)
		if l.Boss == nil || l.Boss.Entry != c.boss {
			t.Errorf("leg %s: boss %+v, want %d", c.leg, l.Boss, c.boss)
			continue
		}
		if got := fmt.Sprint(l.Boss.First); got != c.first {
			t.Errorf("leg %s: boss first %s, want %s", c.leg, got, c.first)
		}
		if len(l.PullGroups) != c.pulls {
			t.Errorf("leg %s: %d pull groups, want %d", c.leg, len(l.PullGroups), c.pulls)
		}
		// No pull group of the leg may hold a unit that stands with the boss (within 12 yd), but Dextren's last pull: the Inmates 8 yd from him that
		// cannot be pulled without him (radius 7, pulled after everything else of the leg).
		for i, g := range l.PullGroups {
			if c.boss == 1663 && i == len(l.PullGroups)-1 {
				continue
			}
			for _, q := range g.Positions {
				if d := dist3(q, l.Boss.Position); d < 12 {
					t.Errorf("leg %s pull group %d: a member stands %.1f yd from the boss", c.leg, i, d)
				}
			}
		}
	}
	h := stockadeLeg(t, p, "hamhock")
	if h.Boss.Tactics == nil || len(h.Boss.Tactics.Interrupt) != 1 || h.Boss.Tactics.Interrupt[0].String() != "Chain Lightning" {
		t.Errorf("Hamhock tactics %+v: want the interrupt on Chain Lightning", h.Boss.Tactics)
	}
}

// Dextren's cell is the last pull of its leg, after the two packs that lie away from it (his Intimidating Shout is a fear), and the Inmates pulled in
// front of him have a radius that does not reach him. Kam's neighbours (18-24 yd) are pulled before him. Bazil's leg begins 35 yd from him.
func TestStockadePullOrderKeepsTheBossesCellsLast(t *testing.T) {
	p := loadPlan(t, stockadePlan)
	d := stockadeLeg(t, p, "dextren_ward")
	last := d.PullGroups[len(d.PullGroups)-1]
	if dist3(last.Positions[0], d.Boss.Position) > 15 || last.Radius > 8 {
		t.Errorf("dextren_ward: the last pull is %v radius %v, want the Inmates in front of Dextren with radius <= 8", last.Positions[0], last.Radius)
	}
	for i, g := range d.PullGroups[:len(d.PullGroups)-1] {
		if dist3(g.Positions[0], d.Boss.Position) < 25 && i > 4 {
			t.Errorf("dextren_ward: pull group %d (%v) stands within 25 yd of Dextren and is not last", i, g.Positions[0])
		}
	}
	k := stockadeLeg(t, p, "kam_deepfury")
	for i, g := range k.PullGroups {
		if g.Radius == 0 {
			t.Errorf("kam_deepfury: pull group %d has no radius", i)
		}
	}
	z := stockadeLeg(t, p, "bazil_thredd")
	if d := dist3(z.To, z.Boss.Position); d < 25 {
		t.Errorf("bazil_thredd begins %.1f yd from Bazil, want at least 25", d)
	}
}
