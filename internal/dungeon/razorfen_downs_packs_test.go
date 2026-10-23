package dungeon

import (
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"strconv"
	"testing"
)

// The pack fixes of the Razorfen Downs draft (2026-10-23; ~/TortoiseBots-audit/PREP-RAZORFEN-DOWNS-2026-10-23.md, section "Fixes applied"), made from the
// world export of map 129, Turtle's RFD update and the Deadmines r12/r14 lessons, before any live run. Each test fails when its part is taken out of the plan.
const razorfenDownsPlan = "../../dungeons/razorfen_downs.json"

const razorfenDownsSpawns = "../../tools/dungeongen/testdata/map129/creatures.tsv"

func razorfenDownsLeg(t *testing.T, p *Plan, name string) Leg {
	t.Helper()
	for _, l := range p.Legs {
		if l.Name == name {
			return l
		}
	}
	t.Fatalf("no leg %s", name)
	return Leg{}
}

// The EventAI fleers, the polymorphable humanoid casters, the undead no Polymorph lands on, and the killers of the fight: Ragglesnout (Heal, Dominate
// Mind), the Summoner (Summon Shadowcaster), the Freezing Spirit (Frost Nova) and the Frostweaver (Frostbolt). A kill_first entry must not be in
// cc_priority (the loader refuses both).
func TestRazorfenDownsUnitListsComeFromTheCensus(t *testing.T) {
	p := loadPlan(t, razorfenDownsPlan)
	for _, c := range []struct {
		name      string
		got, want string
	}{
		{"fleers", fmt.Sprint(p.Fleers), "[7332 7335 7337]"},
		{"cc_priority", fmt.Sprint(p.CCPriority), "[62676 7337 7335 62677]"},
		{"cc_always", fmt.Sprint(p.CCAlways), "[]"},
		{"no_cc", fmt.Sprint(p.NoCC), "[7327 7328 7329 7332 7334 7341 7342 7343 7344 7345 7346 7347 7348 7352 7353]"},
		{"kill_first", fmt.Sprint(p.KillFirst), "[7354 7342 7353 7341]"},
	} {
		if c.got != c.want {
			t.Errorf("%s %s, want %s", c.name, c.got, c.want)
		}
	}
}

// 2110 Black Rat and 4076 Roach are civilians (flags_extra 2, 1 and 8 hp, factions 31 and 188): the draft pulled them as pack members, including pulls of
// nothing but a rat. No group lists one.
func TestRazorfenDownsCrittersAreOutOfEveryPull(t *testing.T) {
	p := loadPlan(t, razorfenDownsPlan)
	for li, l := range p.Legs {
		for gi, g := range l.PullGroups {
			for _, e := range g.Entries {
				if e == 2110 || e == 4076 {
					t.Errorf("leg %d pull group %d lists the critter %d", li, gi, e)
				}
			}
			if len(g.Entries) == 0 {
				t.Errorf("leg %d pull group %d is empty", li, gi)
			}
		}
	}
}

// The four bosses, in clear order, with no boss.first: the packs that stand with Rakameg (four Worshippers 13-17 yd from him) stay the leg's last pull
// instead, because the loader refuses a pull of a boss.first entry and the boss joins that fight anyway.
func TestRazorfenDownsBossesAndPullCounts(t *testing.T) {
	p := loadPlan(t, razorfenDownsPlan)
	for _, c := range []struct {
		leg   string
		boss  uint32
		pulls int
	}{
		{"mordresh_fire_eye", 7357, 10},
		{"glutton", 8567, 30},
		{"death_prophet_rakameg", 62679, 19},
		{"amnennar_the_coldbringer", 7358, 20},
	} {
		l := razorfenDownsLeg(t, p, c.leg)
		if l.Boss == nil || l.Boss.Entry != c.boss {
			t.Errorf("leg %s: boss %+v, want %d", c.leg, l.Boss, c.boss)
			continue
		}
		if len(l.Boss.First) != 0 {
			t.Errorf("leg %s: boss first %v, want none", c.leg, l.Boss.First)
		}
		if len(l.PullGroups) != c.pulls {
			t.Errorf("leg %s: %d pull groups, want %d", c.leg, len(l.PullGroups), c.pulls)
		}
	}
	// Rakameg's doorway pack is the last pull of its leg (the boss row follows it).
	r := razorfenDownsLeg(t, p, "death_prophet_rakameg")
	last := r.PullGroups[len(r.PullGroups)-1]
	for _, e := range last.Entries {
		if e != 62676 {
			t.Errorf("death_prophet_rakameg: the last pull lists %d, want the four Worshippers 62676", e)
		}
	}
}

// Ragglesnout (7354, rank 2, rare) stands on a Splinterbone Warrior of leg 3's pack at 2413.0 1025.9 37.9: he was a skipped unit the pull dragged in. He
// is a member of that one group now, and no leg skips him.
func TestRazorfenDownsRagglesnoutIsInThePullNotASkip(t *testing.T) {
	p := loadPlan(t, razorfenDownsPlan)
	for li, l := range p.Legs {
		for _, e := range l.Skip {
			if e == 7354 {
				t.Errorf("leg %d still skips 7354: he is in a pull group", li)
			}
		}
	}
	n := 0
	for li, l := range p.Legs {
		for gi, g := range l.PullGroups {
			for _, e := range g.Entries {
				if e == 7354 {
					n++
					if li != 3 || gi != 13 {
						t.Errorf("7354 is in leg %d pull group %d, want leg 3 group 13", li, gi)
					}
					if len(g.Entries) != 6 {
						t.Errorf("the Ragglesnout pull has %d entries, want 6", len(g.Entries))
					}
				}
			}
		}
	}
	if n != 1 {
		t.Errorf("7354 is in %d pull groups, want 1", n)
	}
}

// Every pull is small enough to fight and its radius covers its own members: no group has more than six entries, every group has a radius of at least its
// farthest member from the centre, and every position is a spawn row of the group's entry (no invented point; the draft had one 5.2 yd off, leg 3 group 6,
// now spawn 42725).
func TestRazorfenDownsPullsAndPositions(t *testing.T) {
	p := loadPlan(t, razorfenDownsPlan)
	spawns := razorfenDownsSpawnsByEntry(t)
	for li, l := range p.Legs {
		for gi, g := range l.PullGroups {
			if len(g.Entries) > 6 {
				t.Errorf("leg %d pull group %d has %d entries", li, gi, len(g.Entries))
			}
			if g.Radius <= 0 {
				t.Errorf("leg %d pull group %d has no radius", li, gi)
			}
			c := PackCentre(g)
			far := 0.0
			for _, q := range g.Positions {
				d := math.Sqrt(float64((q.X-c.X)*(q.X-c.X) + (q.Y-c.Y)*(q.Y-c.Y) + (q.Z-c.Z)*(q.Z-c.Z)))
				if d > far {
					far = d
				}
			}
			if float64(g.Radius)+0.05 < far {
				t.Errorf("leg %d pull group %d: radius %.1f does not cover its farthest member (%.1f yd)", li, gi, g.Radius, far)
			}
			for i, q := range g.Positions {
				e := g.Entries[i]
				ok := false
				for _, s := range spawns[e] {
					if math.Hypot(float64(s.X-q.X), float64(s.Y-q.Y)) <= 0.05 && math.Abs(float64(s.Z-q.Z)) <= 0.05 {
						ok = true
						break
					}
				}
				if !ok {
					t.Errorf("leg %d pull group %d: no spawn of %d stands at %v", li, gi, e, q)
				}
			}
		}
	}
}

// The Glutton patrols the whole Spiral of Thorns room (8567, 351 yd loop, ~140 s) and the draft pulled five one-unit packs at his feet: every pull that
// stands within 15 yd of his spawn carries a patrol block, so the agent's driver waits for him to walk on or pulls him alone.
func TestRazorfenDownsGluttonPatrolIsOnTheRoomPulls(t *testing.T) {
	p := loadPlan(t, razorfenDownsPlan)
	l := razorfenDownsLeg(t, p, "glutton")
	seen := 0
	for gi, g := range l.PullGroups {
		c := PackCentre(g)
		if math.Hypot(float64(c.X-l.Boss.Position.X), float64(c.Y-l.Boss.Position.Y)) > 15 {
			continue
		}
		seen++
		if g.Patrol == nil || len(g.Patrol.Entries) != 1 || g.Patrol.Entries[0] != 8567 {
			t.Errorf("glutton pull group %d stands %.1f yd from the boss with patrol %+v, want his patrol",
				gi, math.Hypot(float64(c.X-l.Boss.Position.X), float64(c.Y-l.Boss.Position.Y)), g.Patrol)
			continue
		}
		if len(g.Patrol.Path) < 2 || g.Patrol.PeriodS <= 0 || !g.Patrol.PeriodEstimate {
			t.Errorf("glutton pull group %d: patrol path %d points, period %v, estimate %v", gi, len(g.Patrol.Path), g.Patrol.PeriodS, g.Patrol.PeriodEstimate)
		}
	}
	if seen == 0 {
		t.Errorf("no pull group stands within 15 yd of the Glutton: the room pulls are gone")
	}
}

// razorfenDownsSpawnsByEntry reads the map-129 spawn export: entry -> spawn points.
func razorfenDownsSpawnsByEntry(t *testing.T) map[uint32][]Point {
	t.Helper()
	f, err := os.Open(razorfenDownsSpawns)
	if err != nil {
		t.Fatalf("spawn export: %v", err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.Comma = '\t'
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatalf("spawn export: %v", err)
	}
	if len(rows) < 2 {
		t.Fatalf("spawn export: %d rows", len(rows))
	}
	col := map[string]int{}
	for i, name := range rows[0] {
		col[name] = i
	}
	out := map[uint32][]Point{}
	for _, row := range rows[1:] {
		entry, err := strconv.ParseUint(row[col["entry"]], 10, 32)
		if err != nil {
			continue
		}
		x, err1 := strconv.ParseFloat(row[col["x"]], 32)
		y, err2 := strconv.ParseFloat(row[col["y"]], 32)
		z, err3 := strconv.ParseFloat(row[col["z"]], 32)
		if err1 != nil || err2 != nil || err3 != nil {
			t.Fatalf("spawn row %v: %v %v %v", row, err1, err2, err3)
		}
		e := uint32(entry)
		out[e] = append(out[e], Point{X: float32(x), Y: float32(y), Z: float32(z)})
	}
	return out
}
