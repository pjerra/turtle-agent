package dungeon

import (
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"strconv"
	"testing"
)

// The pack fixes of the Scholomance draft (2026-10-24; ~/TortoiseBots-audit/PREP-SCHOLOMANCE-2026-10-24.md, "Fixes applied"), made from the world
// export of map 289 and the world SQL, before any live run. Each test fails when its part is taken out of the plan.
const scholomancePlan = "../../dungeons/scholomance.json"

const scholomanceSpawns = "../../tools/dungeongen/testdata/map289/creatures.tsv"

type scholomanceSpawn struct {
	entry   uint32
	x, y, z float64
}

func loadScholomanceSpawns(t *testing.T) []scholomanceSpawn {
	t.Helper()
	f, err := os.Open(scholomanceSpawns)
	if err != nil {
		t.Fatalf("the map-289 export: %v", err)
	}
	defer f.Close()
	rd := csv.NewReader(f)
	rd.Comma = '\t'
	rows, err := rd.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	var out []scholomanceSpawn
	for _, r := range rows[1:] { // guid entry name level_min level_max rank faction npc_flags x y z ...
		if len(r) < 11 {
			continue
		}
		entry, err := strconv.Atoi(r[1])
		if err != nil {
			continue
		}
		x, err1 := strconv.ParseFloat(r[8], 64)
		y, err2 := strconv.ParseFloat(r[9], 64)
		z, err3 := strconv.ParseFloat(r[10], 64)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		out = append(out, scholomanceSpawn{entry: uint32(entry), x: x, y: y, z: z})
	}
	if len(out) < 400 {
		t.Fatalf("%d spawns read from %s, want the 407 of map 289", len(out), scholomanceSpawns)
	}
	return out
}

func dist3S(a, b scholomanceSpawn) float64 {
	return math.Sqrt((a.x-b.x)*(a.x-b.x) + (a.y-b.y)*(a.y-b.y) + (a.z-b.z)*(a.z-b.z))
}

// The EventAI fleers (10469, 10477), the polymorphable humanoid casters (best first: the Fear, the Counterspell, the Shadow Bolt, the Handler, the
// Adept), the undead no Polymorph lands on, and the killers (the Acolyte's Dark Mending, the Dark Summoner's Risen Lackey, the Necromancer's Call
// of the Grave/Corpse Explosion). A kill_first entry must not be in cc_priority (the loader refuses both).
func TestScholomanceUnitListsComeFromTheCensus(t *testing.T) {
	p := loadPlan(t, scholomancePlan)
	for _, c := range []struct {
		name      string
		got, want string
	}{
		{"fleers", fmt.Sprint(p.Fleers), "[10469 10477]"},
		{"cc_priority", fmt.Sprint(p.CCPriority), "[10470 10472 10476 11257 10469]"},
		{"cc_always", fmt.Sprint(p.CCAlways), "[]"},
		{"no_cc", fmt.Sprint(p.NoCC), "[10478 10480 10481 10482 10485 10486 10487 10488 10489 10491 10495 10498 10499 10500 10678 11258 11551]"},
		{"kill_first", fmt.Sprint(p.KillFirst), "[10471 11582 10477]"},
	} {
		if c.got != c.want {
			t.Errorf("%s %s, want %s", c.name, c.got, c.want)
		}
	}
}

// 2110 Black Rat and 4075 Rat are civilians (faction 31, 1 and 8 hp): the draft pulled 55 of them as pack members, including pulls of nothing but a
// rat. The Kormok trigger 16047 and Kormok 16118 share Ras Frostwhisper's point; Kormok's spawn has spawn_flags 2 (SPAWN_FLAG_DISABLED) and the
// trigger only spawns him on a spell hit, so that pull is gone too. No group lists any of them.
func TestScholomanceCrittersAndTheDisabledKormokAreOutOfEveryPull(t *testing.T) {
	p := loadPlan(t, scholomancePlan)
	bad := map[uint32]bool{2110: true, 4075: true, 16047: true, 16118: true}
	for li, l := range p.Legs {
		for gi, g := range l.PullGroups {
			if len(g.Entries) == 0 {
				t.Errorf("leg %d pull group %d is empty", li, gi)
			}
			for _, e := range g.Entries {
				if bad[e] {
					t.Errorf("leg %d pull group %d lists %d", li, gi, e)
				}
			}
		}
	}
}

// The leg order is the 2-opt route over the bosses' own positions (Malicia-Ravenian 17 yd, Krastinov-Alexei 4 yd, Polkelt-Illucia 9 yd): the export
// walks 3863 yd instead of 4506 yd. Malicia's leg is the route clear: the landing-to-Malicia walk is 554 yd of navmesh and passes 40 census packs
// within 30 yd (r28c slotC wiped on them five times in one move row); the Vectus and Alexei boss rows carry the adds the rules turn hostile
// (Vectus' Marduk 10433, Alexei's two linked Risen Guards 10489) and Rattlegore's carries its linked Risen Construct 10488.
func TestScholomanceLegsBossesAndPullCounts(t *testing.T) {
	p := loadPlan(t, scholomancePlan)
	want := []struct {
		leg   string
		boss  uint32
		pulls int
		first string
	}{
		{"instructor_malicia", 10505, 40, "[]"},
		{"the_ravenian", 10507, 7, "[]"},
		{"lorekeeper_polkelt", 10901, 4, "[]"},
		{"lady_illucia_barov", 10502, 14, "[]"},
		{"jandice_barov", 10503, 36, "[]"},
		{"rattlegore", 11622, 25, "[10488]"},
		{"vectus", 10432, 18, "[10433]"},
		{"ras_frostwhisper", 10508, 3, "[]"},
		{"doctor_theolen_krastinov", 11261, 2, "[]"},
		{"lord_alexei_barov", 10504, 3, "[10489]"},
	}
	if len(p.Legs) != len(want) {
		t.Fatalf("%d legs, want %d", len(p.Legs), len(want))
	}
	pulls := 0
	for i, c := range want {
		l := p.Legs[i]
		if l.Name != c.leg {
			t.Errorf("leg %d is %q, want %q", i, l.Name, c.leg)
		}
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
		pulls += len(l.PullGroups)
	}
	if pulls != 152 {
		t.Errorf("%d pull groups in all, want 152", pulls)
	}
}

// Leg 0 clears the whole walk from the landing to Malicia: every census pack within 30 yd of the 554-yard navmesh route, one at a time in route
// order, each with an approach whose last point is a stand 15-25 yd from the pack's nearest member on the pack's own floor, and no stand inside a
// pack that is still alive later in the leg (its radius + 5 yd). The first run's step 1 was a single move row through all of them.
func TestScholomanceLeg0ClearsTheRouteToMalicia(t *testing.T) {
	p := loadPlan(t, scholomancePlan)
	leg := p.Legs[0]
	if leg.Name != "instructor_malicia" || leg.Boss == nil || leg.Boss.Entry != 10505 {
		t.Fatalf("leg 0 is %q boss %+v, want instructor_malicia 10505", leg.Name, leg.Boss)
	}
	if len(leg.PullGroups) != 40 {
		t.Fatalf("leg 0 has %d pull groups, want the 40 packs of the route", len(leg.PullGroups))
	}
	members := 0
	for gi, g := range leg.PullGroups {
		members += len(g.Entries)
		if len(g.Approach) < 1 || len(g.Approach) > 4 {
			t.Errorf("leg 0 pull group %d: %d approach points, want 1-4", gi, len(g.Approach))
			continue
		}
		for _, e := range g.Entries {
			if e == 4075 || e == 2110 || e == 16047 || e == 16118 || e == 10505 {
				t.Errorf("leg 0 pull group %d lists %d (a critter, the trigger, Kormok or the boss)", gi, e)
			}
		}
		// The stand is the approach's last point; it must be on the pack's floor, 15-25 yd from its nearest member.
		stand := g.Approach[len(g.Approach)-1]
		nearest := math.Inf(1)
		for _, at := range g.Positions {
			d := math.Sqrt(math.Pow(float64(at.X-stand.X), 2) + math.Pow(float64(at.Y-stand.Y), 2) + math.Pow(float64(at.Z-stand.Z), 2))
			nearest = math.Min(nearest, d)
			if math.Abs(float64(at.Z-stand.Z)) > 6 {
				t.Errorf("leg 0 pull group %d: stand %v is %.1f yd off the pack's floor (member z %.1f)", gi, stand, math.Abs(float64(at.Z-stand.Z)), at.Z)
			}
		}
		if nearest < 15 || nearest > 25 {
			t.Errorf("leg 0 pull group %d: stand is %.1f yd from the nearest member, want 15-25", gi, nearest)
		}
		// The leg's boss is alive for every pull: its aggro is 20 yd, so the stand keeps 25.
		if d := math.Sqrt(math.Pow(float64(stand.X-leg.Boss.Position.X), 2) + math.Pow(float64(stand.Y-leg.Boss.Position.Y), 2) + math.Pow(float64(stand.Z-leg.Boss.Position.Z), 2)); d < 25 {
			t.Errorf("leg 0 pull group %d: stand is %.1f yd from the boss %d, inside its aggro", gi, d, leg.Boss.Entry)
		}
		// No stand inside a pack that is still alive later in the leg.
		for gj := gi + 1; gj < len(leg.PullGroups); gj++ {
			o := leg.PullGroups[gj]
			var ox, oy, oz float64
			for _, at := range o.Positions {
				ox += float64(at.X)
				oy += float64(at.Y)
				oz += float64(at.Z)
			}
			m := float64(len(o.Positions))
			oc := scholomanceSpawn{x: ox / m, y: oy / m, z: oz / m}
			sd := math.Sqrt(math.Pow(float64(stand.X)-oc.x, 2) + math.Pow(float64(stand.Y)-oc.y, 2) + math.Pow(float64(stand.Z)-oc.z, 2))
			if sd < float64(o.Radius)+5 {
				t.Errorf("leg 0 pull group %d: stand is %.1f yd from later pack %d (radius %g + 5)", gi, sd, gj, o.Radius)
			}
		}
	}
	if members != 113 {
		t.Errorf("leg 0 holds %d members, want the 113 of the 40 route packs", members)
	}
	// The first pack is the stair guard and the last is the pair by Malicia's door.
	if got := fmt.Sprint(leg.PullGroups[0].Entries); got != "[10489]" {
		t.Errorf("first leg-0 pack %s, want the stair guard [10489]", got)
	}
	if got := fmt.Sprint(leg.PullGroups[len(leg.PullGroups)-1].Entries); got != "[10469 10472]" {
		t.Errorf("last leg-0 pack %s, want [10469 10472]", got)
	}
}

// Every group is at most six members on one floor, and its radius covers exactly its own members: no group reaches past them to another pack or a
// boss. Lord Blackwood 14695 is the exception the census records: a Turtle boss that stands in the leg-7 hall pack, fought as its member.
func TestScholomancePacksAreSixOnOneFloorAndTheirRadiiHoldOnlyThem(t *testing.T) {
	p := loadPlan(t, scholomancePlan)
	spawns := loadScholomanceSpawns(t)
	bossPos := map[uint32]scholomanceSpawn{}
	for _, l := range p.Legs {
		if l.Boss != nil {
			bossPos[l.Boss.Entry] = scholomanceSpawn{entry: l.Boss.Entry, x: float64(l.Boss.Position.X), y: float64(l.Boss.Position.Y), z: float64(l.Boss.Position.Z)}
		}
	}
	for li, l := range p.Legs {
		for gi, g := range l.PullGroups {
			if len(g.Entries) > 6 {
				t.Errorf("leg %d pull group %d: %d members, want at most 6", li, gi, len(g.Entries))
			}
			if g.Radius < 5 || g.Radius > 16 {
				t.Errorf("leg %d pull group %d: radius %g, want 5-16", li, gi, g.Radius)
			}
			members := make([]scholomanceSpawn, 0, len(g.Positions))
			for k, at := range g.Positions {
				pos := scholomanceSpawn{x: float64(at.X), y: float64(at.Y), z: float64(at.Z)}
				found := false
				for _, s := range spawns {
					if s.entry == g.Entries[k] && dist3S(s, pos) < 0.2 {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("leg %d pull group %d position %d: no spawn of %d at %v", li, gi, k, g.Entries[k], at)
				}
				members = append(members, pos)
			}
			minZ, maxZ := math.Inf(1), math.Inf(-1)
			var cx, cy, cz float64
			for _, m := range members {
				cx += m.x
				cy += m.y
				cz += m.z
				minZ = math.Min(minZ, m.z)
				maxZ = math.Max(maxZ, m.z)
			}
			n := float64(len(members))
			if n == 0 {
				continue
			}
			c := scholomanceSpawn{x: cx / n, y: cy / n, z: cz / n}
			if maxZ-minZ > 6 {
				t.Errorf("leg %d pull group %d spans %.1f yd of z: another floor", li, gi, maxZ-minZ)
			}
			for _, m := range members {
				if d := dist3S(m, c); d > float64(g.Radius)+0.01 {
					t.Errorf("leg %d pull group %d: a member is %.1f yd from the centre, outside radius %g", li, gi, d, g.Radius)
				}
			}
			for e, b := range bossPos {
				if e == 14695 {
					continue // Blackwood is fought with the hall pack (the plan notes)
				}
				if d := dist3S(b, c); d <= float64(g.Radius) {
					t.Errorf("leg %d pull group %d: boss %d is %.1f yd from the centre, inside radius %g", li, gi, e, d, g.Radius)
				}
			}
		}
	}
}
