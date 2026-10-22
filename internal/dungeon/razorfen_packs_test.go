package dungeon

import (
	"fmt"
	"testing"
)

// The pack fixes of the Razorfen Kraul draft (2026-10-22; ~/TortoiseBots-audit/PREP-RFK-2026-10-22.md, "Fixes applied"), made from the
// world export of map 47 and the Deadmines r12/r14 + Stockade lessons, before any live run. Each test fails when its part is taken out.
const rfkPlan = "../../dungeons/razorfen_kraul.json"

func rfkLeg(t *testing.T, p *Plan, name string) Leg {
	t.Helper()
	for _, l := range p.Legs {
		if l.Name == name {
			return l
		}
	}
	t.Fatalf("no leg %s", name)
	return Leg{}
}

// Every humanoid of the map flees at low health (EventAI command 47); the Ward Guardian (Healing Wave) and the Acolyte
// (Renew, Mana Burn) are the healers and go into cc_priority with the two CC-casters (Enveloping Winds, Earthgrab Totem).
func TestRazorfenUnitListsComeFromTheCensus(t *testing.T) {
	p := loadPlan(t, rfkPlan)
	for _, c := range []struct {
		name      string
		got, want string
	}{
		{"fleers", fmt.Sprint(p.Fleers), "[4435 4436 4437 4438 4440 4515 4516 4517 4518 4519 4520 4522 4523 4525 4530 4531 4532 4623 4842 6035]"},
		{"cc_priority", fmt.Sprint(p.CCPriority), "[4427 4515 4522 4440]"},
		{"cc_always", fmt.Sprint(p.CCAlways), "[4427]"},
		{"kill_first", fmt.Sprint(p.KillFirst), "[]"},
	} {
		if c.got != c.want {
			t.Errorf("%s %s, want %s", c.name, c.got, c.want)
		}
	}
}

// The packs that stand with a boss are the boss row's first, not pulls of their own: Roogug's two Defenders and two Adepts (8 yd
// from him, a pull of radius 23.6 took the boss in), Jargba's Acolyte and Groundshaker (6-9 yd), the two Death's Head Priests
// under Charlga's den (the boss is 2 yd from the priest pull, 20 yd up). The two Spearhides 5-6 yd from Ramtusk are his first.
func TestRazorfenBossPacksAreTheBossFight(t *testing.T) {
	p := loadPlan(t, rfkPlan)
	for _, c := range []struct {
		leg   string
		boss  uint32
		first string
		pulls int
	}{
		{"roogug", 6168, "[4516 4442]", 32},
		{"aggem_thorncurse", 4424, "[]", 20},
		{"death_speaker_jargba", 4428, "[4515 4523]", 8},
		{"overlord_ramtusk", 4420, "[4438]", 14},
		{"rotthorn", 62503, "[]", 17},
		{"agathelos_the_raging", 4422, "[]", 10},
		{"charlga_razorflank", 4421, "[4517]", 22},
	} {
		l := rfkLeg(t, p, c.leg)
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
	}
}

// Earthcaller Halmgar 4842 (a rank-2 caster the leg 3 pull of radius 10 dragged in from 2 yd) is a member of that pull group and
// no longer a skipped unit of the leg; the trio east of him has a radius that stops short of him (his 13 yd) and of its own mate
// pack (16 yd). The Blind Hunter 4425 (rank 2, standing on the last bat pull's own position) is a member of that group.
func TestRazorfenRaresAreOwnedNotSkipped(t *testing.T) {
	p := loadPlan(t, rfkPlan)
	r := rfkLeg(t, p, "overlord_ramtusk")
	if len(r.Skip) != 0 {
		t.Errorf("overlord_ramtusk still skips %v: both units are owned now", r.Skip)
	}
	n := 0
	for i, g := range r.PullGroups {
		for _, e := range g.Entries {
			if e == 4842 {
				n++
				if len(g.Entries) != 2 || len(g.Positions) != 2 || g.Radius != 10 {
					t.Errorf("overlord_ramtusk pull group %d with Halmgar: entries %v radius %v, want 2 entries, 2 positions, radius 10", i, g.Entries, g.Radius)
				}
			}
		}
	}
	if n != 1 {
		t.Errorf("4842 is in %d pull groups of overlord_ramtusk, want 1", n)
	}
	// the Earthbreaker + Stalker + Sage trio: radius 12 keeps the mate Earthbreaker (16 yd) and Halmgar (14 yd) out
	g := r.PullGroups[len(r.PullGroups)-8]
	if g.Entries[0] != 4525 || len(g.Entries) != 3 || g.Radius != 12 {
		t.Errorf("overlord_ramtusk trio: entries %v radius %v, want [4525 6035 4518] radius 12", g.Entries, g.Radius)
	}
	c := rfkLeg(t, p, "charlga_razorflank")
	n = 0
	for i, g := range c.PullGroups {
		for _, e := range g.Entries {
			if e == 4425 {
				n++
				if len(g.Entries) != 2 || len(g.Positions) != 2 || g.Radius != 10 {
					t.Errorf("charlga_razorflank pull group %d with the Blind Hunter: entries %v radius %v, want 2 entries, 2 positions, radius 10", i, g.Entries, g.Radius)
				}
			}
		}
	}
	if n != 1 {
		t.Errorf("4425 is in %d pull groups of charlga_razorflank, want 1", n)
	}
}

// The overlapping clusters of the rotthorn leg are one pull each, not two pulls that take each other's mates as uncounted
// joiners (the Blood of Agamaggan and its Agam'ar stand 4 yd apart, each thicket pair 2-4 yd), and the plateau packs' radii do
// not reach across the 28 yd of floor between them.
func TestRazorfenBrambleClustersAreSinglePulls(t *testing.T) {
	p := loadPlan(t, rfkPlan)
	r := rfkLeg(t, p, "rotthorn")
	// the two thicket merges share entries, positions and radius: two groups of [62502 62501 62501] at radius 10
	n := 0
	for _, g := range r.PullGroups {
		if fmt.Sprint(g.Entries) == "[62502 62501 62501]" && g.Radius == 10 {
			n++
			if len(g.Positions) != 3 {
				t.Errorf("rotthorn thicket group: %d positions, want 3", len(g.Positions))
			}
		}
	}
	if n != 2 {
		t.Errorf("rotthorn: %d thicket pulls of [62502 62501 62501] radius 10, want 2", n)
	}
	for _, c := range []struct {
		entries string
		pos     int
		radius  float32
	}{
		{"[4514 4541]", 2, 10},
		{"[62502 62501]", 2, 12},
		{"[4512 4514 4541]", 3, 12},
	} {
		n := 0
		for _, g := range r.PullGroups {
			if fmt.Sprint(g.Entries) == c.entries {
				n++
				if len(g.Positions) != c.pos || g.Radius != c.radius {
					t.Errorf("rotthorn group %v: %d positions radius %v, want %d and %v", g.Entries, len(g.Positions), g.Radius, c.pos, c.radius)
				}
			}
		}
		if n != 1 {
			t.Errorf("rotthorn: %v is %d pull groups, want 1", c.entries, n)
		}
	}
}
