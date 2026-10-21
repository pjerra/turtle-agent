package dungeon

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

// PLAN-ULDAMAN-GRIMLOK (2026-10-21, eight Uldaman runs): every wipe and near wipe at the Ancient Stone Keeper and in Grimlok's corridor was a
// pull that grew from 2-4 to 5-11 mobs because a patrol, a neighbouring pack or a pack of another leg was still alive. These tests hold the plan
// data that fixes the order: the Keeper is pulled with nothing alive near it, and Grimlok's corridor is cleared in an order where the
// groups that woke each other are pulled one by one, from stands that keep clear of whatever is still alive.

// The two patrols of the changed ground (creature_movement of the tortoise-wow 5d79486 world database: guid, point, x, y; the plan has no patrol
// rows for them: the tsv carries no waypoints). The Stone Steward 27473 walks between its two points; the Brawler 27505 along three.
var uldamanPatrols = map[string][]Point{
	"27473": {{X: -42.417, Y: 234.082}, {X: -41.454, Y: 274.43}, {X: -42.417, Y: 234.082}},
	"27505": {{X: 57.9032, Y: 373.95}, {X: 75.2625, Y: 395.951}, {X: 62.5435, Y: 409.664}, {X: 75.2625, Y: 395.951}, {X: 57.9032, Y: 373.95}},
}

// uldamanSpawn is a creature row of the world export.
type uldamanSpawn struct {
	guid  string
	entry uint32
	at    Point
}

func uldamanSpawns(t *testing.T) []uldamanSpawn {
	t.Helper()
	var out []uldamanSpawn
	for _, m := range tsv(t, "creatures.tsv") {
		e, _ := strconv.ParseUint(m["entry"], 10, 32)
		out = append(out, uldamanSpawn{m["guid"], uint32(e), mustPoint(m)})
	}
	return out
}

// uldamanHostileEntries are the entries the plan fights in the legs from the Sentinel on: a pull group's or a boss's units.
func uldamanHostileEntries(p *Plan) map[uint32]bool {
	out := map[uint32]bool{}
	for _, l := range p.Legs[5:] {
		for _, g := range l.PullGroups {
			for _, e := range g.Entries {
				out[e] = true
			}
		}
		if l.Boss != nil {
			out[l.Boss.Entry] = true
			for _, e := range l.Boss.First {
				out[e] = true
			}
		}
	}
	return out
}

func uldamanLeg(t *testing.T, p *Plan, name string) int {
	t.Helper()
	for i, l := range p.Legs {
		if l.Name == name {
			return i
		}
	}
	t.Fatalf("no leg %q", name)
	return -1
}

// uldamanPulledBefore lists the positions of every pull group that is pulled (so dead) before group `group` of leg `leg`: all the groups of the
// legs before it and the leg's own groups before that index (len(PullGroups) for the leg's boss).
func uldamanPulledBefore(p *Plan, leg, group int) []Point {
	var out []Point
	for li := 0; li <= leg; li++ {
		for gi, g := range p.Legs[li].PullGroups {
			if li == leg && gi >= group {
				break
			}
			out = append(out, g.Positions...)
		}
	}
	return out
}

// uldamanDistance is how near a spawn comes to a point: its spawn, or for a patrol the nearest point of its walk.
func uldamanDistance(s uldamanSpawn, at Point) float64 {
	d := groundDist(s.at, at)
	if path, ok := uldamanPatrols[s.guid]; ok {
		for i := 0; i+1 < len(path); i++ {
			d = math.Min(d, pointToSegYd(at, path[i], path[i+1]))
		}
	}
	return d
}

// uldamanAliveWithin lists the spawns of the hostile entries within yd of `at` that no earlier pull group of the plan has killed by the time of
// group `group` of leg `leg`. `skip` leaves a spawn out (the boss itself).
func uldamanAliveWithin(p *Plan, spawns []uldamanSpawn, leg, group int, at Point, yd float64, skip func(uldamanSpawn) bool) []string {
	hostile := uldamanHostileEntries(p)
	dead := uldamanPulledBefore(p, leg, group)
	var out []string
	for _, s := range spawns {
		if !hostile[s.entry] || (skip != nil && skip(s)) {
			continue
		}
		pulled := false
		for _, d := range dead {
			pulled = pulled || groundDist(s.at, d) < 0.2
		}
		if pulled {
			continue
		}
		if d := uldamanDistance(s, at); d < yd {
			out = append(out, fmt.Sprintf("%s (entry %d at %.1f %.1f) %.1f yd", s.guid, s.entry, s.at.X, s.at.Y, d))
		}
	}
	return out
}

// uldamanOldStoneKeeperOrder puts the Steward and the Sculptor trio back in the galgann leg and drops the southern pair: the order of the plan
// every run before 2026-10-21 walked.
func uldamanOldStoneKeeperOrder(t *testing.T, p *Plan) {
	t.Helper()
	sk, ga := uldamanLeg(t, p, "ancient_stone_keeper"), uldamanLeg(t, p, "galgann")
	g := p.Legs[sk].PullGroups
	n := len(g)
	if n < 10 || fmt.Sprint(g[n-3].Entries, g[n-2].Entries, g[n-1].Entries) != "[4860] [7012 7012 7011] [7011 7011]" {
		t.Fatalf("the Stone Keeper leg ends in %v", g[n-3:])
	}
	steward, sculptors := g[n-3], g[n-2]
	steward.Approach, sculptors.Approach = nil, nil
	p.Legs[sk].PullGroups = g[:n-3]
	gg := p.Legs[ga].PullGroups
	p.Legs[ga].PullGroups = append([]PullGroup{gg[0], steward, sculptors}, gg[1:]...)
}

// Neither the Stone Steward's patrol, nor the Sculptors, nor the southern Rocksmashers may be alive when the party pulls the Ancient Stone Keeper
// (r4 slot1: wipe with the Steward, a Rocksmasher and two Sculptors in the fight; r6 slot5: seven mobs, two deaths). Every Earthen guard within 45 yd of
// the Keeper, a patrol counted by the nearest point of its walk, is the target of a pull group before the boss.
func TestUldamanStoneKeeperIsPulledWithNothingAliveNearIt(t *testing.T) {
	spawns := uldamanSpawns(t)
	keeper := func(s uldamanSpawn) bool { return s.entry == 7206 }
	const nearYd = 45
	check := func(p *Plan) []string {
		sk := uldamanLeg(t, p, "ancient_stone_keeper")
		boss := p.Legs[sk].Boss
		if boss == nil || boss.Entry != 7206 {
			t.Fatalf("leg %d has no Stone Keeper boss", sk)
		}
		return uldamanAliveWithin(p, spawns, sk, len(p.Legs[sk].PullGroups), boss.Position, nearYd, keeper)
	}
	if alive := check(loadUldaman(t)); len(alive) != 0 {
		t.Errorf("alive within %d yd of the Stone Keeper when it is pulled:\n%s", nearYd, strings.Join(alive, "\n"))
	}
	// The order before: the Steward (patrol end 13.6 yd from the Keeper), the Sculptor trio and the two southern Rocksmashers are alive.
	old := loadUldaman(t)
	uldamanOldStoneKeeperOrder(t, old)
	alive := strings.Join(check(old), "\n")
	for _, guid := range []string{"27473", "28784", "28789", "28795", "28547"} {
		if !strings.Contains(alive, guid+" ") {
			t.Errorf("the old order should leave %s alive at the Stone Keeper:\n%s", guid, alive)
		}
	}
	// And the Steward and the trio are not pulled twice: the galgann leg no longer lists them.
	p := loadUldaman(t)
	for _, g := range p.Legs[uldamanLeg(t, p, "galgann")].PullGroups {
		if len(g.Entries) > 0 && (g.Entries[0] == 4860 || (len(g.Entries) == 3 && g.Entries[0] == 7012 && g.Positions[0].X < -40)) {
			t.Errorf("the galgann leg still pulls %v at %v", g.Entries, g.Positions)
		}
	}
}

// uldamanGroupOf finds the pull group (leg name, index) whose positions hold the spawn.
func uldamanGroupOf(p *Plan, s uldamanSpawn) string {
	for _, l := range p.Legs {
		for gi, g := range l.PullGroups {
			for _, pos := range g.Positions {
				if groundDist(pos, s.at) < 0.15 {
					return fmt.Sprintf("%s/%d", l.Name, gi)
				}
			}
		}
	}
	return ""
}

// Grimlok's corridor, pull by pull (PLAN-ULDAMAN-GRIMLOK #2): the Brawler 30105 that joined pull 0 in 8 of 8 runs is in it; the first three groups of the
// keepers_door leg are pulled next, before the east groups; the patrol Brawler and the trio that always came with it are one pull; the pair is
// the last pull before Grimlok.
func TestUldamanGrimlokCorridorPullOrder(t *testing.T) {
	p := loadUldaman(t)
	byGuid := map[string]uldamanSpawn{}
	for _, s := range uldamanSpawns(t) {
		byGuid[s.guid] = s
	}
	for want, guids := range map[string][]string{
		"grimlok/0": {"27527", "27526", "30105"},
		"grimlok/1": {"27521", "27522"},
		"grimlok/2": {"30106", "27525"},
		"grimlok/3": {"27505", "30091", "30092", "30093"},
		"grimlok/4": {"27512", "27513", "27514"},
		"grimlok/5": {"30094", "30098", "30099"},
		"grimlok/6": {"30101", "30100"},
	} {
		for _, g := range guids {
			s, ok := byGuid[g]
			if !ok {
				t.Fatalf("no creature %s in the export", g)
			}
			if got := uldamanGroupOf(p, s); got != want {
				t.Errorf("creature %s (entry %d at %.1f %.1f) is in %q, want %q", g, s.entry, s.at.X, s.at.Y, got, want)
			}
		}
	}
	gr := p.Legs[uldamanLeg(t, p, "grimlok")]
	if n := len(gr.PullGroups); n != 7 {
		t.Fatalf("grimlok has %d pull groups, want 7", n)
	}
	// Group 0 counts the third Brawler and keeps the Brawler at 55.4 379 (the patrol's trio) out of its radius; the merged group has the patrol's
	// four units and a radius that holds the trio around it.
	g0 := gr.PullGroups[0]
	if fmt.Sprint(g0.Entries) != "[4853 4855 4855]" || g0.Radius != 16 {
		t.Errorf("grimlok pull 0: %v radius %g", g0.Entries, g0.Radius)
	}
	c := PackCentre(g0)
	if d := groundDist(c, byGuid["30092"].at); d <= float64(g0.Radius) {
		t.Errorf("the trio's Brawler 30092 is %.1f yd from pull 0's centre, inside its radius %g", d, g0.Radius)
	}
	for _, g := range []string{"27527", "27526", "30105"} {
		if d := groundDist(c, byGuid[g].at); d > float64(g0.Radius) {
			t.Errorf("%s is %.1f yd from pull 0's centre, outside its radius %g", g, d, g0.Radius)
		}
	}
	if m := gr.PullGroups[3]; fmt.Sprint(m.Entries) != "[4855 4855 4853 4855]" || m.Radius != 18 || len(m.Positions) != 4 {
		t.Errorf("grimlok merged pull: %+v", m)
	}
	// The keepers_door leg keeps only what is south of the corridor (its old groups 3-7), and no spawn is in two legs.
	kd := p.Legs[uldamanLeg(t, p, "keepers_door")]
	if len(kd.PullGroups) != 5 || fmt.Sprint(kd.PullGroups[0].Entries) != "[4855 4855 4853]" {
		t.Errorf("keepers_door pull groups: %d, first %v", len(kd.PullGroups), kd.PullGroups[0].Entries)
	}
	seen := map[Point]string{}
	for _, l := range p.Legs {
		for gi, g := range l.PullGroups {
			for _, pos := range g.Positions {
				key := Point{X: pos.X, Y: pos.Y}
				if prev, dup := seen[key]; dup && !(l.Name == "galgann" && gi == 0) {
					t.Errorf("%s/%d lists %v, already in %s", l.Name, gi, pos, prev)
				}
				seen[key] = fmt.Sprintf("%s/%d", l.Name, gi)
			}
		}
	}
}

// uldamanGuardedStands are the approach points of the changed groups held to the clearance rule: every one stays 20 yd from every unit that is
// still alive when the group is pulled (its own pack, the groups pulled later, the bosses and the units no group lists, a patrol by its walk) and
// 23 yd from the Stone Keeper, where the Keeper's own pull would otherwise begin.
func TestUldamanChangedStandsKeepClearOfWhatIsAlive(t *testing.T) {
	spawns := uldamanSpawns(t)
	check := func(p *Plan) []string {
		var out []string
		for li, l := range p.Legs {
			if l.Name != "ancient_stone_keeper" && l.Name != "grimlok" {
				continue
			}
			for gi, g := range l.PullGroups {
				if !uldamanGuarded[Point{X: g.Positions[0].X, Y: g.Positions[0].Y}] || len(g.Approach) == 0 {
					continue
				}
				stand := g.Approach[len(g.Approach)-1]
				for _, a := range uldamanAliveWithin(p, spawns, li, gi, stand, 20, nil) {
					out = append(out, fmt.Sprintf("%s/%d stand %.1f %.1f: %s", l.Name, gi, stand.X, stand.Y, a))
				}
				if l.Name == "ancient_stone_keeper" {
					if d := groundDist(stand, l.Boss.Position); d < 23 {
						out = append(out, fmt.Sprintf("%s/%d stand %.1f %.1f is %.1f yd from the Stone Keeper", l.Name, gi, stand.X, stand.Y, d))
					}
				}
			}
		}
		return out
	}
	if bad := check(loadUldaman(t)); len(bad) != 0 {
		t.Errorf("%s", strings.Join(bad, "\n"))
	}
	// The rule has teeth: the Steward's stand at the galgann leg's `to` (-39 243, where the old order started its walk up the passage) is 12.7 yd from
	// a Sculptor and 22 from the Keeper.
	p := loadUldaman(t)
	sk := p.Legs[uldamanLeg(t, p, "ancient_stone_keeper")]
	sk.PullGroups[len(sk.PullGroups)-3].Approach = []Point{{X: -39, Y: 243, Z: -48}}
	if bad := check(p); len(bad) < 2 {
		t.Errorf("a stand at -39 243 passes: %v", bad)
	}
}

// The approach-clearance rule of PR #281 on the changed groups, with the two patrols of the ground (the plan has no patrol rows for them) laid on
// their groups: none of the changed groups has a finding, a stand is 15 yd or more from every patrol walk.
func TestUldamanChangedGroupsPassTheApproachClearanceRule(t *testing.T) {
	p := loadUldaman(t)
	byGuid := map[string]uldamanSpawn{}
	for _, s := range uldamanSpawns(t) {
		byGuid[s.guid] = s
	}
	for guid, path := range uldamanPatrols {
		s := byGuid[guid]
		var found bool
		for li := range p.Legs {
			for gi := range p.Legs[li].PullGroups {
				g := &p.Legs[li].PullGroups[gi]
				for _, pos := range g.Positions {
					if groundDist(pos, s.at) < 0.15 && g.Patrol == nil {
						g.Patrol = &Patrol{Entries: []uint32{s.entry}, Path: path}
						found = true
					}
				}
			}
		}
		if !found {
			t.Fatalf("no pull group holds the patrol %s", guid)
		}
	}
	for _, x := range approachClearanceOf(p, uldamanGuarded) {
		t.Errorf("%s", x)
	}
}

// The pair at 38-40 439-445 is pulled from a stand with a line to both of them on the navmesh (the old stand at 59.7 408.2 had none to the Geomancer at
// 37.6 438.8: the run leader's party walked north to find one, and the pull woke Grimlok's group, r6 slot5 step 93), and the stand keeps 29 yd from
// Grimlok's group.
func TestUldamanPairStandSeesBothOfThePair(t *testing.T) {
	dir := mmapsDir()
	if dir == "" {
		t.Skip("no map 70 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 070.mmap and the 070*.mmtile tiles")
	}
	p := loadUldaman(t)
	pl := navmesh.NewPlanner(dir)
	defer pl.Close()
	gr := p.Legs[uldamanLeg(t, p, "grimlok")]
	pair := gr.PullGroups[len(gr.PullGroups)-1]
	if fmt.Sprint(pair.Entries) != "[4853 4855]" || len(pair.Approach) != 1 {
		t.Fatalf("the last grimlok group: %+v", pair)
	}
	clear := func(from Point) (blocked []Point) {
		for _, at := range pair.Positions {
			r, err := pl.Raycast(p.Map, vec(from), vec(at))
			if errors.Is(err, navmesh.ErrNoEngine) {
				t.Skip("built without cgo: no navmesh engine")
			}
			if err != nil {
				t.Fatal(err)
			}
			if !r.Known || !r.Clear {
				blocked = append(blocked, at)
			}
		}
		return blocked
	}
	stand := pair.Approach[0]
	if b := clear(stand); len(b) != 0 {
		t.Errorf("no navmesh line from the stand %v to %v", stand, b)
	}
	if b := clear(Point{X: 59.7, Y: 408.2, Z: -37.5}); len(b) == 0 {
		t.Errorf("the old stand 59.7 408.2 has a line to the pair: this test no longer shows why the stand moved")
	}
	for _, s := range uldamanSpawns(t) {
		if s.entry == 4854 || s.entry == 4863 || s.guid == "27530" || s.guid == "27531" {
			if d := groundDist(stand, s.at); d < 29 {
				t.Errorf("the stand %v is %.1f yd from %s (entry %d)", stand, d, s.guid, s.entry)
			}
		}
	}
}
