package dungeon

import (
	"fmt"
	"math"
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

// The opening stand of leg 0 (r19-19z37 live run 1, stopped at step 4): the party stands at 1994 1615, a lower-floor pocket
// with no upper polygon in its formation ring, not at 1987.5 1600 where the slot lookup found the walkway above (z 92-95) and
// the two ranged bots climbed the west ramp while idle after the fight. The five corridor pull groups all have radius 10
// (r20-19z39 run 2: group 0's radius 25 owned both Quilguards of the next group and the first fight began at the corridor
// mouth), group 4 carries the corridor regroup move at 2000 1635, and the two pulls that outran the run leader's 8 approach
// hops (39 and 42) carry stands on the hall floor within 60 yd of their packs.
func TestRazorfenOpeningStandStaysOffTheLedge(t *testing.T) {
	p := loadPlan(t, rfkPlan)
	l := rfkLeg(t, p, "roogug")
	if got := fmt.Sprintf("%.1f %.1f %.1f", l.To.X, l.To.Y, l.To.Z); got != "1994.0 1615.0 83.2" {
		t.Errorf("roogug to = %s, want 1994.0 1615.0 83.2", got)
	}
	want := []struct {
		group  int
		radius float32
	}{
		{0, 10}, // Handler 4530
		{1, 10}, // Quilguard pair
		{2, 10}, // Geomancer 4520
		{3, 10}, // second Quilguard pair
		{5, 10}, // Handler 4530 + Quilguard pair: the default was 19.3
	}
	for _, c := range want {
		if got := l.PullGroups[c.group].Radius; got != c.radius {
			t.Errorf("roogug pull group %d radius %v, want %v", c.group, got, c.radius)
		}
	}
	g := l.PullGroups[4]
	if len(g.Approach) != 1 || g.Approach[0].X != 2000 || g.Approach[0].Y != 1635 || g.Approach[0].Z != 81 {
		t.Errorf("roogug pull group 4 approach %+v, want the corridor regroup point 2000 1635 81", g.Approach)
	}
	// Pull 39's pack (run 2 stopped on it at 61.0 yd): the stand 2110 1600 82.1 is 42.0 yd from it on the hall floor (the
	// corridor the tank pulled it from); pull 42's pack (4440) has the same stand (run 3 refused the room's stand 2180 1604:
	// no walkable path) and radius 10, 73 yd from the pack because the room east of x about 2135 is another pocket.
	for _, c := range []struct {
		name    string
		group   int
		x, y, z float32
		pack    Point
		maxYd   float64
	}{
		{"step 39's 4532", 21, 2110, 1600, 82.1, l.PullGroups[21].Positions[0], 60},
		{"step 42's 4440", 24, 2110, 1600, 82.1, l.PullGroups[24].Positions[0], 80},
	} {
		g := l.PullGroups[c.group]
		if len(g.Approach) != 1 {
			t.Errorf("roogug pull group %d (%s) approach %+v, want one stand point", c.group, c.name, g.Approach)
			continue
		}
		a := g.Approach[0]
		if a.X != c.x || a.Y != c.y || a.Z != c.z {
			t.Errorf("roogug pull group %d (%s) approach %+v, want %.0f %.0f %.1f", c.group, c.name, a, c.x, c.y, c.z)
		}
		dx, dy, dz := float64(a.X-c.pack.X), float64(a.Y-c.pack.Y), float64(a.Z-c.pack.Z)
		if d := math.Sqrt(dx*dx + dy*dy + dz*dz); d > c.maxYd {
			t.Errorf("roogug pull group %d (%s): the stand is %.1f yd from the pack, want at most %.0f", c.group, c.name, d, c.maxYd)
		}
	}
	if got := l.PullGroups[24].Radius; got != 10 {
		t.Errorf("roogug pull group 24 (4440) radius %v, want the explicit 10", got)
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
