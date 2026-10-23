package dungeon

import (
	"fmt"
	"math"
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
// the healer (6392 Holdout Medic) and the caster (6329 Irradiated Pillager with Radiation Bolt) are the CC entries, and 6392 is the one to
// CC in every pack; kill_first the mid-fight summoners and the AoE machines (6220 Irradiated Horror summons Remote-Controlled Golems,
// 7849 Mobile Alert System summons two Mechanized Sentries, 6226 Flame Spray, 6227 Hail Storm), plus 6223 Leprous Defender: the first live
// run's step 25 (2026-10-22) showed its ranged Shoot/Multi-Shot killing the tank while the party was split, and a kill_first entry is never
// crowd-controlled, so it is no longer in cc_priority. No entry is in two lists: the loader refuses that.
func TestGnomereganUnitListsComeFromTheCensus(t *testing.T) {
	p := loadPlan(t, gnomereganPlan)
	for _, c := range []struct {
		name      string
		got, want string
	}{
		{"fleers", fmt.Sprint(p.Fleers), "[6206 6207 6211 6391 6392 6407]"},
		{"cc_priority", fmt.Sprint(p.CCPriority), "[6392 6329]"},
		{"cc_always", fmt.Sprint(p.CCAlways), "[6392]"},
		{"kill_first", fmt.Sprint(p.KillFirst), "[6220 6226 6227 7849 6223]"},
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
	if n := len(d.PullGroups); n != 24 {
		t.Errorf("dark_iron_ambassador: %d pull groups, want 24", n)
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
		{"crowd_pummeler_9-60", 1, 8},
		{"crowd_pummeler_9-60", 10, 9},
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

// The step-25 fix (r20-19z39 slot 4, 2026-10-22; GNOMEREGAN-STEP25-2026-10-22.md): leg 0's cave chain (the 6223 Leprous Defender and 6233
// Mechanized Sentry camps at x -517..-427, y 226..273, z -211.5..-207.8) is pulled from its east end (-427 239) westwards, one camp per step,
// radius 8, and every group carries an approach point on its own cave floor 18-23 yd from the pack with every member inside the run leader's
// 24.5 yd shot range. The first live run pulled the chain's west end from the Holdout landing (z -171.8, 21-36 yd above the packs): 'to firing
// stand 1/2 for 6233 ... 24.5 yd from it; was 55.7 yd, los no', then the tank walked the whole cave with the healer and mage 60-70 yd behind
// and the whole cave joined the step. The two same-camp pairs are one group each: 6223+6233 stand 10.1 yd apart and the two 6233 rows 6.7 yd,
// so a radius 8 on either row still counts the other camp's mobs as the step's living units (DungeonRunPolicy.h:514) and the fight is one.
func TestGnomereganTheCaveChainIsPulledFromTheEastOnItsOwnFloor(t *testing.T) {
	p := loadPlan(t, gnomereganPlan)
	l := gnomereganLeg(t, p, "viscous_fallout")
	var chain []PullGroup
	var at []int
	for i, g := range l.PullGroups {
		if len(g.Positions) == 0 {
			continue
		}
		pos := g.Positions[0]
		if pos.X >= -520 && pos.X <= -420 && pos.Y >= 220 && pos.Y <= 280 && pos.Z >= -213 && pos.Z <= -206 {
			chain = append(chain, g)
			at = append(at, i)
		}
	}
	if len(chain) != 7 {
		t.Fatalf("%d cave-chain groups, want 7 (6223+6233, 6223, the 3 x 6233, 6233 x2, 6223+6233, 6223, 6223+6233)", len(chain))
	}
	if at[0] != 23 {
		t.Errorf("the chain starts at pull group %d, want 23 (right after the Holdout groups)", at[0])
	}
	for k := 1; k < len(at); k++ {
		if at[k] != at[k-1]+1 {
			t.Errorf("the chain is not contiguous: %v", at)
			break
		}
	}
	// East to west: the first camp is the one at -427 239 (its positions include -427.26 238.57); from the second on the centres move west.
	first := groupCentre(chain[0])
	for _, pos := range chain[0].Positions {
		if groundDist(pos, Point{X: -427.3, Y: 238.6}) <= 1.5 {
			goto east
		}
	}
	t.Errorf("the first cave pull is %.1f %.1f, not the east-end camp at -427.3 238.6", first.X, first.Y)
east:
	last := groupCentre(chain[len(chain)-1])
	if last.X > -510 {
		t.Errorf("the last cave pull is at x %.1f, want the west end (x <= -510)", last.X)
	}
	for k := 1; k < len(chain); k++ {
		c, prev := groupCentre(chain[k]), groupCentre(chain[k-1])
		if c.X >= prev.X {
			t.Errorf("pull group %d (x %.1f) is not west of pull group %d (x %.1f)", at[k], c.X, at[k-1], prev.X)
		}
	}
	for k, g := range chain {
		if g.Radius != 8 {
			t.Errorf("pull group %d: radius %g, want 8 (one camp per step)", at[k], g.Radius)
		}
		if len(g.Approach) != 1 {
			t.Errorf("pull group %d: %d approach points, want 1", at[k], len(g.Approach))
			continue
		}
		stand := g.Approach[0]
		c := groupCentre(g)
		if dz := float64(stand.Z - c.Z); dz < -3 || dz > 3 {
			t.Errorf("pull group %d: the stand z %.1f is %.1f yd off the pack's z %.1f (another floor)", at[k], stand.Z, dz, c.Z)
		}
		if d := groundDist(stand, c); d < 18 || d > 23 {
			t.Errorf("pull group %d: the stand is %.1f yd from the pack's centre, want 18-23", at[k], d)
		}
		for _, pos := range g.Positions {
			if d := groundDist(stand, pos); d > 24.5 {
				t.Errorf("pull group %d: the stand is %.1f yd from a member at %.1f %.1f, outside the 24.5 yd shot range", at[k], d, pos.X, pos.Y)
			}
		}
	}
}

// The Alarm-a-bomb 7897 patrols the corridor x -573..-557, y 74..126 (spawn 30134 at -573.6 126.1) and cannot be attacked: r23 slot 4
// (2026-10-23, 0 deaths) stopped at step 181 (leg 0 pull group 66, the 6329/6211/6329 pack at -575 40) because the run leader's own
// approach hop ended at -566.8 78.8, inside that corridor, and the gather then failed four times on Varcus and Helmina, whom the bomb
// held in combat at -561 110. The group's stand walks the party to -572 58, 16.0 yd south of the corridor and on the route to the pack;
// every other approach point of the plan stays out of the corridor too (the transport waypoints the exporter cuts are not stands).
func TestGnomereganTheStep181StandIsOutOfTheBombCorridor(t *testing.T) {
	p := loadPlan(t, gnomereganPlan)
	l := gnomereganLeg(t, p, "viscous_fallout")
	if len(l.PullGroups) <= 66 {
		t.Fatalf("viscous_fallout: only %d pull groups", len(l.PullGroups))
	}
	g := l.PullGroups[66]
	if got := fmt.Sprint(g.Entries); got != "[6329 6211 6329]" {
		t.Fatalf("pull group 66: entries %s, want the step-181 pack [6329 6211 6329]", got)
	}
	if len(g.Approach) != 1 {
		t.Fatalf("pull group 66: %d approach points, want 1 (the step-181 stand)", len(g.Approach))
	}
	stand := g.Approach[0]
	if want := (Point{X: -572, Y: 58, Z: -198.75}); stand != want {
		t.Errorf("pull group 66: stand %+v, want %+v", stand, want)
	}
	// The corridor: x -573..-557, y 74..126. 15 yd clear of it in the ground plane.
	corridor := func(x, y float64) float64 {
		dx := math.Max(0, math.Max(-573-x, x+557))
		dy := math.Max(0, math.Max(74-y, y-126))
		return math.Hypot(dx, dy)
	}
	if d := corridor(float64(stand.X), float64(stand.Y)); d < 15 {
		t.Errorf("pull group 66: the stand is %.1f yd from the bomb's corridor, want at least 15", d)
	}
	// The stand is in the run leader's sight of the pack (60 yd) and on its own floor, close enough for the pull.
	c := groupCentre(g)
	if d := groundDist(stand, c); d < 15 || d > 60 {
		t.Errorf("pull group 66: the stand is %.1f yd from the pack's centre, want 15-60", d)
	}
	if dz := float64(stand.Z - c.Z); dz < -3 || dz > 3 {
		t.Errorf("pull group 66: the stand z %.1f is %.1f yd off the pack's z %.1f (another floor)", stand.Z, dz, c.Z)
	}
	for _, pos := range g.Positions {
		if d := groundDist(stand, pos); d > 24.5 {
			t.Errorf("pull group 66: the stand is %.1f yd from a member at %.1f %.1f, outside the 24.5 yd shot range", d, pos.X, pos.Y)
		}
	}
	// No other stand of the plan sits in the corridor.
	for _, leg := range p.Legs {
		for i, group := range leg.PullGroups {
			for _, a := range group.Approach {
				if d := corridor(float64(a.X), float64(a.Y)); d == 0 {
					t.Errorf("leg %s pull group %d: the approach %+v lies in the bomb's corridor", leg.Name, i, a)
				}
			}
		}
	}
}
