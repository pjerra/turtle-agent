package dungeon

import (
	"fmt"
	"testing"
)

// The pack fixes of the Gnomeregan draft (2026-10-22; ~/TortoiseBots-audit/PREP-GNOMEREGAN-2026-10-22.md, section "Fixes applied"), made from the
// world export of map 90 and the Deadmines r12 lessons, before any live run. Each test fails when its part is taken out of the plan.
const gnomereganPlan = "../../dungeons/gnomeregan.json"

func gnomereganLeg(t *testing.T, p *Plan, name string) Leg {
	t.Helper()
	for _, l := range p.Legs {
		if l.Name == name {
			return l
		}
	}
	t.Fatalf("no leg %s", name)
	return Leg{}
}

// The map's EventAI (tortoise-wow sql/base creature_ai_events, read 2026-10-22): 6206, 6207, 6211, 6391, 6392 and 6407 flee at 15 % health;
// the healer (6392 Holdout Medic) and the casters (6329 Irradiated Pillager with Radiation Bolt, 6223 Leprous Defender with Shoot/Multi-Shot)
// are the CC entries, and 6392 is the one to CC in every pack; kill_first the mid-fight summoners and the AoE machines (6220 Irradiated
// Horror summons Remote-Controlled Golems, 7849 Mobile Alert System summons two Mechanized Sentries, 6226 Flame Spray, 6227 Hail Storm).
// No entry is in two lists: the loader refuses that.
func TestGnomereganUnitListsComeFromTheCensus(t *testing.T) {
	p := loadPlan(t, gnomereganPlan)
	for _, c := range []struct {
		name      string
		got, want string
	}{
		{"fleers", fmt.Sprint(p.Fleers), "[6206 6207 6211 6391 6392 6407]"},
		{"cc_priority", fmt.Sprint(p.CCPriority), "[6392 6329 6223]"},
		{"cc_always", fmt.Sprint(p.CCAlways), "[6392]"},
		{"kill_first", fmt.Sprint(p.KillFirst), "[6220 6226 6227 7849]"},
	} {
		if c.got != c.want {
			t.Errorf("%s %s, want %s", c.name, c.got, c.want)
		}
	}
}

// The Dark Iron Ambassador's guard, the Dark Iron Agent that stands at his own spot (-718.1 565.2 -289.1) and patrols his 12-point loop with him,
// is the boss's first, not a pull of its own: pulling the old pull group 9 pulled the boss with it. No pull group of any leg may hold a position
// within 20 yd (3-D) of its leg's boss: that is the boss fight, not a pull (Viscous Fallout, the Electrocutioner, the Crowd Pummeler and
// Thermaplugg have no pull inside their sight; the draft's 2-D near misses 0.1/0.2 are 54 yd above the Fallout, 2.7 is 44 yd below the Pummeler).
func TestGnomereganTheAgentsAtTheAmbassadorAreHisFirst(t *testing.T) {
	p := loadPlan(t, gnomereganPlan)
	if len(p.Legs) != 5 {
		t.Fatalf("%d legs, want 5", len(p.Legs))
	}
	d := gnomereganLeg(t, p, "dark_iron_ambassador")
	if d.Boss == nil || d.Boss.Entry != 6228 {
		t.Fatalf("dark_iron_ambassador: boss %+v, want 6228", d.Boss)
	}
	if got := fmt.Sprint(d.Boss.First); got != "[6212]" {
		t.Errorf("dark_iron_ambassador: boss first %s, want [6212]", got)
	}
	if n := len(d.PullGroups); n != 26 {
		t.Errorf("dark_iron_ambassador: %d pull groups, want 26", n)
	}
	if last := d.PullGroups[len(d.PullGroups)-1]; last.Entries[0] != 6212 || len(last.Entries) != 1 {
		t.Errorf("dark_iron_ambassador: the last pull is %v, want the Agent in front of him", last.Entries)
	}
	for _, l := range p.Legs {
		if l.Boss == nil {
			continue
		}
		for i, g := range l.PullGroups {
			// The Agent pulled in front of the Ambassador (17.9 yd, inside his 20 yd sight) is the leg's LAST pull, the boss-approach fight:
			// the Stockade Dextren pattern. Every other group stays out of the boss's sight.
			if l.Name == "dark_iron_ambassador" && i == len(l.PullGroups)-1 {
				continue
			}
			for _, q := range g.Positions {
				if dz := dist3(q, l.Boss.Position); dz < 20 {
					t.Errorf("leg %s pull group %d: a member stands %.1f yd from the boss", l.Name, i, dz)
				}
			}
		}
	}
}

// The oversized leper packs (1.34: eleven members in a 17-unit chain, 2.11: nine, 2.2/2.9: eight) stay one pull each: their members are one
// camp (the farthest two members are within the chain range of each other) and 7603/6222/6224 call for help on aggro, so a split would only
// hand the run leader rows that all read "pack joined". The census that pins this is PREP-GNOMEREGAN-2026-10-22.md section 3, items 1-2:
// listed, not fixed, first-run watch list.
func TestGnomereganOversizedPacksStayAsTheCensusListsThem(t *testing.T) {
	p := loadPlan(t, gnomereganPlan)
	for _, c := range []struct {
		leg  string
		n    int
		want int
	}{
		{"electrocutioner_6000", 34, 11},
		{"electrocutioner_6000", 19, 6},
		{"crowd_pummeler_9-60", 2, 8},
		{"crowd_pummeler_9-60", 11, 9},
		{"mekgineer_thermaplugg", 3, 7},
	} {
		l := gnomereganLeg(t, p, c.leg)
		if c.n >= len(l.PullGroups) {
			t.Fatalf("%s: only %d pull groups", c.leg, len(l.PullGroups))
		}
		if got := len(l.PullGroups[c.n].Entries); got != c.want {
			t.Errorf("%s pull group %d: %d entries, want %d", c.leg, c.n, got, c.want)
		}
	}
}
