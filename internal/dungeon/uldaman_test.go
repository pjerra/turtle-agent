package dungeon

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/objects"
)

const (
	uldamanPlan = "../../dungeons/uldaman.json"
	uldamanData = "../../tools/dungeongen/testdata/uldaman" // the world database export the plan was written from
)

func loadUldaman(t *testing.T) *Plan {
	t.Helper()
	p, err := Load(uldamanPlan)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUldamanPlanLoads(t *testing.T) {
	p := loadUldaman(t)
	if p.Map != 70 || p.Name != "uldaman" || len(p.Legs) != 14 {
		t.Fatalf("%+v", p)
	}
	legs := p.Legs
	p.Legs = p.Legs[:4] // legs 0-3 (Baelog to Ironaya) are asserted below as they were; TestUldamanFullPlan holds the rest
	var names []string
	var bosses []uint32
	for _, l := range p.Legs {
		names = append(names, l.Name)
		var b uint32
		if l.Boss != nil {
			b = l.Boss.Entry
		}
		bosses = append(bosses, b)
	}
	if fmt.Sprint(names) != "[baelog revelosh keystone ironaya]" || fmt.Sprint(bosses) != "[6906 6910 0 7228]" {
		t.Fatalf("%v %v", names, bosses)
	}
	// Eric and Olaf are fought before Baelog: no script in the world database makes them die with him.
	if b := p.Legs[0].Boss; fmt.Sprint(b.First) != "[6907 6908]" || len(p.Legs[0].Skip) != 0 {
		t.Fatalf("%+v skip %v", b, p.Legs[0].Skip)
	}
	// The Lost Dwarves are the Alliance's friends (faction 122, questgivers): the boss is for the Horde, and the chest after it is
	// looted by both for the Gni'kiv Medallion.
	if b := p.Legs[0].Boss; b.Team != TeamHorde || len(p.Legs[0].Steps) != 0 || len(p.Legs[0].AfterBoss) != 1 {
		t.Fatalf("%+v", p.Legs[0])
	}
	lo := p.Legs[0].AfterBoss[0].LootObject
	if lo == nil || lo.Entry != 123329 || lo.ExpectItem == nil || lo.ExpectItem.Entry != 7740 {
		t.Fatalf("%+v", p.Legs[0].AfterBoss)
	}
	// Revelosh drops the Shaft of Tsol; the Medallion and the Shaft make the Staff, which the Keystone needs.
	rv := p.Legs[1]
	if rv.Boss.CompanionsLoot == nil || *rv.Boss.CompanionsLoot {
		t.Fatalf("the companions must not loot Revelosh: %+v", rv.Boss)
	}
	if rv.Boss.LootItem == nil || rv.Boss.LootItem.Entry != 7741 || len(rv.AfterBoss) != 1 || rv.Boss.Team != "" {
		t.Fatalf("%+v", rv)
	}
	if ui := rv.AfterBoss[0].UseItem; ui == nil || ui.Entry != 7740 || ui.RequiresItem.Entry != 7741 || ui.ExpectItem.Entry != 7733 {
		t.Fatalf("%+v", rv.AfterBoss)
	}
	// The Keystone leg has no boss; the door steps are the Keystone, then the Seal of Khaz'Mul opening.
	k := p.Legs[2]
	if k.Boss != nil || len(k.Steps) != 2 || k.Steps[0].UseObject.Entry != 124371 || k.Steps[0].UseObject.Stand == nil ||
		k.Steps[1].WaitFor.GameObject.Entry != 124372 || k.Steps[1].WaitFor.GameObject.State != "active" {
		t.Fatalf("%+v", k)
	}
	// The enter phase's data: the game_tele name and the portal on the outer map.
	if p.EntranceTele != "ulda" || p.Portal == nil || p.Portal.Map != 0 || p.EntranceTrigger != 286 {
		t.Errorf("tele %q portal %+v trigger %d", p.EntranceTele, p.Portal, p.EntranceTrigger)
	}
	p.Legs = legs
	// The file is kept in the form Marshal writes, so regenerating it shows only real changes.
	raw, err := os.ReadFile(uldamanPlan)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, out) {
		t.Error("dungeons/uldaman.json is not in Marshal's form: run `go run ./tools/dungeongen -format dungeons/uldaman.json`")
	}
}

// The legs after Ironaya (issue #73, #83): the Obsidian Sentinel behind the Echomok Door, the Ancient Stone Keeper, Galgann Firehammer,
// Grimlok, the Altar of the Keepers with its four Stone Keepers, and Archaedas behind the Altar of Archaedas. The altars are
// summoning rituals (game object type 18) of three participants in the world database.
func TestUldamanFullPlan(t *testing.T) {
	p := loadUldaman(t)
	var names []string
	var bosses []uint32
	for _, l := range p.Legs[4:] {
		names = append(names, l.Name)
		var b uint32
		if l.Boss != nil {
			b = l.Boss.Entry
		}
		bosses = append(bosses, b)
	}
	if fmt.Sprint(names) != "[scorpid_cave sentinel ancient_stone_keeper galgann grimlok keepers_door keepers_altar keepers descent archaedas]" ||
		fmt.Sprint(bosses) != "[0 7023 7206 7291 4854 0 0 0 0 2748]" {
		t.Fatalf("%v %v", names, bosses)
	}
	// The doors a player clicks (lock 86, no script) and the ones the instance script opens, each in the leg that needs it.
	use := func(l Leg) (out []uint32) {
		for _, s := range l.Steps {
			if s.UseObject != nil {
				out = append(out, s.UseObject.Entry)
			}
		}
		return
	}
	leg := func(name string) Leg {
		for _, l := range p.Legs {
			if l.Name == name {
				return l
			}
		}
		t.Fatalf("no leg %q", name)
		return Leg{}
	}
	for name, want := range map[string]string{"sentinel": "[]", "keepers_door": "[124368]", "keepers_altar": "[]", "archaedas": "[141869]"} {
		if got := fmt.Sprint(use(leg(name))); got != want {
			t.Errorf("%s: use_object steps %s, want %s", name, got, want)
		}
	}
	// The Stone Keepers wake one after the other and the door 124367 opens when the fourth is dead: that wait is the leg's ceiling.
	k := leg("keepers")
	if len(k.Steps) != 1 || k.Steps[0].WaitFor == nil || k.Steps[0].WaitFor.GameObject == nil || k.Steps[0].WaitFor.GameObject.Entry != 124367 ||
		k.Steps[0].WaitFor.GameObject.State != "active" || k.Steps[0].WaitFor.TimeoutS != MaxTimeoutS {
		t.Errorf("keepers: %+v", k.Steps)
	}
	if len(k.PullGroups) != 1 || fmt.Sprint(k.PullGroups[0].Entries) != "[4857 4857 4857 4857]" {
		t.Errorf("keepers: %+v", k.PullGroups)
	}
	// The altars are rituals of three: a ritual step (three bots in turn, remote runs) whose final answer is the result. No wait for the altar
	// to be active follows it: the bots' release ends their channel and the core sends the altar back to ready.
	for name, altar := range map[string]uint32{"keepers_altar": 130511, "archaedas": 133234} {
		l := leg(name)
		var found bool
		for _, st := range l.Steps {
			if r := st.Ritual; r != nil && r.Entry == altar {
				found = r.Participants == 3 && r.Stand != nil && dist3(r.Position, *r.Stand) < 6
			}
			if w := st.WaitFor; w != nil && w.GameObject != nil && w.GameObject.Entry == altar {
				t.Errorf("%s: a wait for altar %d (it reads ready again after the ritual): %+v", name, altar, w)
			}
		}
		if !found {
			t.Errorf("%s: no ritual step of 3 with a stand point for altar %d: %+v", name, altar, l.Steps)
		}
	}
	// The Sentinel's room: the Relic Hunter camp (five Hunters and a Geologist, the two ledge Hunters among them 23-24 yd from the Sentinel)
	// is one pack that a pull wakes together with the Sentinel (hires-3 wipe): one pull group of six, so that the big-pack CC counts it all,
	// and no boss.first, the camp is cleared before the boss.
	sen := leg("sentinel")
	if f := sen.Boss.First; len(f) != 0 {
		t.Errorf("sentinel first %v", f)
	}
	if n := len(sen.PullGroups); n != 5 {
		t.Errorf("sentinel has %d pull groups, want 5 (scorpid, basilisk, scorpids, basilisk, the camp)", n)
	} else if camp := sen.PullGroups[n-1]; fmt.Sprint(camp.Entries) != "[4847 4847 4847 4847 4847 7030]" || len(camp.Positions) != 6 {
		t.Errorf("sentinel camp: %+v", camp)
	}
	// Archaedas: the adds that wake with him are not waited for; the Vault opens when he dies.
	a := leg("archaedas")
	if fmt.Sprint(a.Skip) != "[7076 7077 7309 10120]" || len(a.AfterBoss) != 1 || a.AfterBoss[0].WaitFor == nil || a.AfterBoss[0].WaitFor.GameObject.Entry != 124369 {
		t.Errorf("archaedas: skip %v after_boss %+v", a.Skip, a.AfterBoss)
	}
	// Grimlok and Galgann come with their groups (creature_groups of the world database).
	if f := leg("grimlok").Boss.First; fmt.Sprint(f) != "[4855 4853 4863]" {
		t.Errorf("grimlok first %v", f)
	}
	if f := leg("galgann").Boss.First; fmt.Sprint(f) != "[7030 7030]" {
		t.Errorf("galgann first %v", f)
	}
	// Both altars are summoning rituals in the export (type 18); the doors are type 0.
	types := map[string]string{}
	for _, m := range tsv(t, "gameobjects.tsv") {
		types[m["entry"]] = m["type"]
	}
	for _, e := range []string{"130511", "133234"} {
		if types[e] != "18" {
			t.Errorf("game object %s is type %q, want 18 (summoning ritual)", e, types[e])
		}
	}
	for _, e := range []string{"124370", "124368", "124367", "141869", "124369"} {
		if types[e] != "0" {
			t.Errorf("game object %s is type %q, want 0 (door)", e, types[e])
		}
	}
}

func tsv(t *testing.T, name string) []map[string]string {
	t.Helper()
	f, err := os.Open(filepath.Join(uldamanData, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.Comma, r.LazyQuotes = '\t', true
	recs, err := r.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]string
	for _, rec := range recs[1:] {
		m := map[string]string{}
		for i, h := range recs[0] {
			m[h] = rec[i]
		}
		out = append(out, m)
	}
	return out
}

func near(m map[string]string, p Point) bool {
	f := func(k string) float64 { v, _ := strconv.ParseFloat(m[k], 64); return v }
	return math.Abs(f("x")-float64(p.X)) < 0.06 && math.Abs(f("y")-float64(p.Y)) < 0.06 && math.Abs(f("z")-float64(p.Z)) < 0.06
}

// Every spawn the plan names is a spawn in the world database export, at that place; the plan invents none.
func TestUldamanPlanMatchesTheWorldExport(t *testing.T) {
	p := loadUldaman(t)
	cs, gos := tsv(t, "creatures.tsv"), tsv(t, "gameobjects.tsv")
	hasCreature := func(entry uint32, at Point) bool {
		for _, m := range cs {
			if m["entry"] == strconv.Itoa(int(entry)) && near(m, at) {
				return true
			}
		}
		return false
	}
	hasObject := func(entry uint32, at Point) bool {
		for _, m := range gos {
			if m["entry"] == strconv.Itoa(int(entry)) && near(m, at) {
				return true
			}
		}
		return false
	}
	for i, l := range p.Legs {
		for j, g := range l.PullGroups {
			for k, at := range g.Positions {
				if !hasCreature(g.Entries[k], at) {
					t.Errorf("legs[%d].pull_groups[%d]: no spawn of %d at %v", i, j, g.Entries[k], at)
				}
			}
		}
		if l.Boss != nil && !hasCreature(l.Boss.Entry, l.Boss.Position) {
			t.Errorf("legs[%d].boss: no spawn of %d at %v", i, l.Boss.Entry, l.Boss.Position)
		}
		if l.Boss != nil {
			for _, e := range l.Boss.First { // killed before the boss: they stand within the encounter's reach of it
				found := false
				for _, m := range cs {
					x, _ := strconv.ParseFloat(m["x"], 64)
					y, _ := strconv.ParseFloat(m["y"], 64)
					z, _ := strconv.ParseFloat(m["z"], 64)
					if m["entry"] == strconv.Itoa(int(e)) && dist3(l.Boss.Position, Point{float32(x), float32(y), float32(z)}) < 25 {
						found = true
					}
				}
				if !found {
					t.Errorf("legs[%d].boss.first: no spawn of %d within 25 yd of the boss", i, e)
				}
			}
		}
		for j, s := range append(append([]Step{}, l.Steps...), l.AfterBoss...) {
			for _, r := range []*ObjectRef{objRef(s)} {
				if r != nil && r.Near != nil && !hasObject(r.Entry, *r.Near) {
					t.Errorf("legs[%d].steps[%d]: no game object %d at %v", i, j, r.Entry, *r.Near)
				}
			}
		}
	}
}

func objRef(s Step) *ObjectRef {
	switch {
	case s.UseObject != nil:
		return &s.UseObject.ObjectRef
	case s.LootObject != nil:
		return &s.LootObject.ObjectRef
	case s.WaitFor != nil && s.WaitFor.GameObject != nil:
		return &s.WaitFor.GameObject.ObjectRef
	case s.Ritual != nil:
		at := s.Ritual.Position
		return &ObjectRef{Entry: s.Ritual.Entry, Near: &at}
	}
	return nil
}

// mmapsDir finds the server's navmesh files of map 70 (070.mmap and the 070yyxx.mmtile tiles): $TURTLE_AGENT_MMAPS,
// or one of the folders where the test VM and the server host keep them. "" when there are none.
func mmapsDir() string {
	home, _ := os.UserHomeDir()
	for _, d := range []string{os.Getenv("TURTLE_AGENT_MMAPS"), filepath.Join(home, "tb-ci", "mmaps-turtle070"), filepath.Join(home, "tortoise-wow-server", "data", "mmaps")} {
		if d == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(d, "070.mmap")); err != nil {
			continue
		}
		if tiles, _ := filepath.Glob(filepath.Join(d, "070*.mmtile")); len(tiles) > 0 {
			return d
		}
	}
	return ""
}

// waypoints lists the places a leg sends the player to, in the order the driver goes: the leg's To, each pack member
// (the approach to the pull), the stand points of the steps, the boss.
func waypoints(l Leg) []Point {
	pts, _ := waypointsDirect(l)
	return pts
}

// waypointsDirect is waypoints with a flag per point: true for a direct step (Leg.Via with Direct), which the navmesh does not link to
// the point before it.
func waypointsDirect(l Leg) (pts []Point, direct []bool) {
	add := func(p Point, d bool) { pts, direct = append(pts, p), append(direct, d) }
	for _, w := range l.Via {
		add(w.Point, w.Direct)
	}
	add(l.To, false)
	for _, g := range l.PullGroups {
		for _, a := range g.Approach {
			add(a, false)
		}
		for _, gp := range g.Positions {
			add(gp, false)
		}
	}
	for _, s := range l.Steps {
		if s.UseObject != nil && s.UseObject.Stand != nil {
			add(*s.UseObject.Stand, false)
		}
		if s.Ritual != nil && s.Ritual.Stand != nil {
			add(*s.Ritual.Stand, false)
		}
		if s.LootObject != nil && s.LootObject.Stand != nil {
			add(*s.LootObject.Stand, false)
		}
		if s.AreaTrigger != nil {
			add(s.AreaTrigger.Stand, false)
		}
	}
	if l.Boss != nil {
		add(l.Boss.Position, false)
	}
	for _, s := range l.AfterBoss { // the driver walks to a chest's own position unless the step says where to stand
		if lo := s.LootObject; lo != nil {
			switch {
			case lo.Stand != nil:
				add(*lo.Stand, false)
			case lo.Near != nil:
				add(*lo.Near, false)
			}
		}
	}
	return pts, direct
}

// sentinelLeg is the index of the leg that crosses the one hole of the map 70 navmesh: its via waypoints are the west lip at -163.5 298.7
// (where the leg before it ends, the scorpid pit), a direct step to -161.1 284.5 on the pit floor (the last navmesh point before the ramp)
// and a direct step up the ramp to -158.4 283.7, the first point of the east mesh.
const sentinelLeg = 5

// pitFloor is the first direct step's end since 2026-10-17 (live run uldaman-events-2): the old point under the rock overhang,
// -162.8 294.8 -51.2, was 2.6 yd off the navmesh, so a follower had no way to the lead bot's spot and the next direct step summoned it.
// This one is on the mesh with a complete path from the lip (17.0 yd), and the step up the ramp from it is 4.2 yd. Its sight lines were
// checked on the live map 70 vmap (lostool on the test VM, ~/tb-ci/vmaps70): clear 1.5, 2 and 2.5 yd up, to the ramp top both ways and
// from the lip; vmap floor -52.32.
var pitFloor = Point{X: -161.1, Y: 284.5, Z: -52.1}

// holeFloor are plan points inside the navmesh hole at the scorpid pit, between two direct steps: no mesh floor stands in for the
// server's ground check there, so it was checked on the live map 70 vmap with the core's own VMapManager2::getHeight and
// isInLineOfSight (lostool on the test VM, ~/tb-ci/vmaps70, 2026-10-14): floor -51.25 and 3.1 yd clear above it, in sight (2 yd up,
// both ways) of the lip and of the ramp top from every floor point within 1 yd of them. A hole point must be followed by a direct step.
// Since 2026-10-17 the plan has none (pitFloor is on the mesh); the map stays for a later point that needs it.
var holeFloor = map[Point]string{}

// shelfPacks are pack positions on a navmesh island next to the route: no path reaches them, the pack is pulled from its group's approach
// point (it comes down to the party), and the walk goes on from there. The path toward one must end within shelfReach of it.
var shelfPacks = map[Point]string{
	{X: -179.0, Y: 321.7, Z: -52.7}: "Jadespine Basilisk 30084 on the shelf north of the scorpid pit: the pit's path ends 12.4 yd short, at -173.9 310.7 -50.0",
}

const shelfReach = 15

// Every waypoint of the Uldaman plan is on the server's navmesh and reachable from the one before it, walking the way
// the driver will (path_to / walk_path). Runs only where the map 70 mmaps are (see mmapsDir); needs the cgo build.
func TestUldamanOnNavmesh(t *testing.T) {
	dir := mmapsDir()
	if dir == "" {
		t.Skip("no map 70 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 070.mmap and the 070*.mmtile tiles")
	}
	p := loadUldaman(t)
	pl := navmesh.NewPlanner(dir)
	defer pl.Close()
	from := [3]float32{p.Entrance.X, p.Entrance.Y, p.Entrance.Z}
	checked := 0
	for i, l := range p.Legs {
		pts, direct := waypointsDirect(l)
		for j, w := range pts {
			to := [3]float32{w.X, w.Y, w.Z}
			path, err := pl.Plan(p.Map, from, to)
			if errors.Is(err, navmesh.ErrNoEngine) {
				t.Skip("built without cgo: no navmesh engine")
			}
			if err != nil {
				t.Fatalf("legs[%d] waypoint %d: %v", i, j, err)
			}
			if direct[j] {
				// A direct step (the scorpid pit's ramp: the one known hole of the map 70 navmesh, found by path_to and confirmed by the live
				// map walk of 2026-10-14) is the part of the walk the navmesh does not link; the path to it is partial or, with a mesh that
				// closes the hole, complete. It needs navmesh floor at its end (TestUldamanPlanPointsOnMesh) and is short (TestUldamanDirectSteps).
				if path.Status != navmesh.Complete {
					if len(path.Points) == 0 {
						t.Fatalf("legs[%d] waypoint %d: partial path with no points", i, j)
					}
					end := path.Points[len(path.Points)-1]
					if gap := math.Hypot(float64(end[0]-w.X), float64(end[1]-w.Y)); gap > 20 {
						t.Errorf("legs[%d] waypoint %d: the navmesh hole is %.0f yd wide, the plan assumes at most about 16", i, j, gap)
					}
				}
				from = to
				checked++
				continue
			}
			if _, shelf := shelfPacks[w]; shelf && len(path.Points) > 0 {
				end := path.Points[len(path.Points)-1]
				if d := math.Hypot(float64(end[0]-w.X), float64(end[1]-w.Y)); d > shelfReach {
					t.Errorf("legs[%d] waypoint %d: the path toward the shelf pack %v ends %.1f yd from it", i, j, w, d)
				}
				checked++
				continue // the party stays where it pulled from
			}
			if path.Status != navmesh.Complete {
				t.Errorf("legs[%d] (%s) waypoint %d %v from %v: %s %s", i, l.Name, j, w, from, path.Status, path.Reason)
				continue
			}
			end := path.Points[len(path.Points)-1]
			if d := math.Hypot(float64(end[0]-w.X), float64(end[1]-w.Y)); d > 1 || math.Abs(float64(end[2]-w.Z)) > 4 {
				t.Errorf("legs[%d] waypoint %d: the path ends %.1f yd (z %.1f) from %v", i, j, d, end[2]-w.Z, w)
			}
			from = to
			checked++
		}
	}
	if checked < 30 {
		t.Fatalf("only %d waypoints checked", checked)
	}
	t.Logf("%d waypoints on the map 70 navmesh in %s", checked, dir)
}

// The groups marked ambush are exactly the ones whose members all spawn stealthed in the export (creature template
// and spawn auras), and the Seal wait is long enough for the 27 s it took live.
func TestUldamanAmbushGroupsAreTheStealthedOnes(t *testing.T) {
	p := loadUldaman(t)
	cs := tsv(t, "creatures.tsv")
	stealthed := func(entry uint32, at Point) bool {
		for _, m := range cs {
			if m["entry"] == strconv.Itoa(int(entry)) && near(m, at) {
				var ids []uint32
				for _, f := range strings.Fields(m["auras"]) {
					n, _ := strconv.ParseUint(f, 10, 32)
					ids = append(ids, uint32(n))
				}
				return StealthedAuras(ids)
			}
		}
		t.Errorf("no spawn of %d at %v", entry, at)
		return false
	}
	n := 0
	for i, l := range p.Legs {
		for j, g := range l.PullGroups {
			all := true
			for k, at := range g.Positions {
				all = all && stealthed(g.Entries[k], at)
			}
			if all != g.Ambush {
				t.Errorf("legs[%d].pull_groups[%d] %v: all members stealthed %v, ambush %v", i, j, g.Entries, all, g.Ambush)
			}
			if g.Ambush {
				n++
			}
		}
	}
	if n != 4 {
		t.Errorf("%d ambush groups, want 4 (the lurkers)", n)
	}
	if w := p.Legs[2].Steps[1].WaitFor; w.TimeoutS != 60 {
		t.Errorf("the Seal wait is %d s", w.TimeoutS)
	}
}

// The plan's honest run for the Alliance, read by the Stepper: leg 0 pulls its packs, skips Baelog (a Horde fight) and loots the chest
// for the Medallion; leg 1 kills Revelosh and combines; no GM item is needed for the Keystone. The Horde fights Baelog first.
func TestUldamanHonestRunSteps(t *testing.T) {
	p := loadUldaman(t)
	run := func(team string) (kinds []ActionKind, bosses []uint32, skipped []string) {
		w := teamWorld{&fakeWorld{objs: []objects.GameObject{{GUID: 0x77, Entry: 123329, X: -356, Y: 112.8, Z: -43.2, State: "ready"}}}, team}
		s := NewStepperAt(p, w, nil, 0)
		for i := 0; i < 80; i++ {
			a := s.Next()
			skipped = append(skipped, s.Skipped()...)
			if a.Kind == ActDone || a.Leg > 1 {
				break
			}
			if a.Kind == ActWalk {
				w.pos = a.To
			}
			if a.Kind == ActKill {
				bosses = append(bosses, a.Boss.Entry)
			}
			if a.Kind == ActLoot || a.Kind == ActItem {
				kinds = append(kinds, a.Kind)
			}
			s.Complete()
		}
		return
	}
	kinds, bosses, skipped := run("alliance")
	if fmt.Sprint(kinds) != "[loot_object use_item]" || fmt.Sprint(bosses) != "[6910]" || len(skipped) != 1 || !strings.Contains(skipped[0], "boss 6906 is for team horde, the leader is alliance") {
		t.Fatalf("alliance: %v %v %q", kinds, bosses, skipped)
	}
	kinds, bosses, skipped = run("horde")
	if fmt.Sprint(kinds) != "[loot_object use_item]" || fmt.Sprint(bosses) != "[6906 6910]" || len(skipped) != 0 {
		t.Fatalf("horde: %v %v %q", kinds, bosses, skipped)
	}
}

// A non-GM run from the start asks for no item it makes itself, and a run from the keystone leg still wants the Staff.
func TestUldamanRequiredItemsChain(t *testing.T) {
	p := loadUldaman(t)
	g := newFake()
	g.bag = []objects.Item{{Entry: 6948, Count: 1}}
	d := NewDriver(p, g, Config{GM: GMNone}, g.clock())
	if err := d.checkRequiredItems(0); err != nil {
		t.Fatalf("from the start the run makes the Staff: %v", err)
	}
	// From revelosh on the Medallion is nobody's product: a run without it (or the Staff) fails at the start.
	if err := d.checkRequiredItems(1); err == nil || !strings.Contains(err.Error(), "Gni'kiv Medallion") {
		t.Fatalf("from revelosh the Medallion must be carried: %v", err)
	}
	g.bag = append(g.bag, objects.Item{Entry: 7740, Count: 1})
	if err := d.checkRequiredItems(1); err != nil {
		t.Fatalf("from revelosh the Shaft comes from the boss and the Staff from the use_item: %v", err)
	}
}

func TestPlanEntryFields(t *testing.T) {
	const head = `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},`
	const legs = `"legs":[{"to":{"x":1,"y":0,"z":0},"steps":[{"wait_for":{"hostiles_gone":{"radius":30},"timeout_s":10}}]}]}`
	p, err := Parse("t.json", []byte(head+`"entrance_tele":"ulda","portal":{"map":0,"x":-6053.7,"y":-2954.6,"z":213.7},`+legs))
	if err != nil || p.EntranceTele != "ulda" || p.Portal == nil || p.Portal.X != -6053.7 || p.Portal.Map != 0 {
		t.Fatalf("%v %+v", err, p)
	}
	if out, err := Marshal(p); err != nil || !bytes.Contains(out, []byte(`"entrance_tele": "ulda"`)) {
		t.Fatalf("%v %s", err, out)
	}
	for in, want := range map[string]string{
		`"entrance_tele":"ulda; .gm on",`:             "entrance_tele",
		`"portal":{"x":1,"y":2,"z":3},`:               "portal.map",
		`"portal":{"map":0,"x":1,"y":2},`:             "portal.z",
		`"portal":{"map":0,"x":1,"y":2,"z":3,"w":4},`: "unknown field",
		`"entrance_tele":"a b",`:                      "entrance_tele",
	} {
		if _, err := Parse("t.json", []byte(head+in+legs)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", in, err)
		}
	}
}

// onMeshTolerance is how far (yards) a plan point may be from the navmesh floor under it: 3 vertically, 1 on the ground plane.
const (
	meshMaxDZ = 3.0
	meshMaxDH = 1.0
)

// planPoints lists every point of a leg the driver walks to or pulls from, with a label for the failure message: the leg's To, each
// pull group's member positions (the pullback anchors), the step stand points, the boss and the after-boss stands.
func planPoints(l Leg) (pts []Point, labels []string, direct []bool) {
	add := func(label string, p Point) {
		pts, labels = append(pts, p), append(labels, label)
		direct = append(direct, false)
	}
	for vi, w := range l.Via {
		add(fmt.Sprintf("via[%d]", vi), w.Point)
		direct[len(direct)-1] = w.Direct
	}
	add("to", l.To)
	for gi, g := range l.PullGroups {
		for ai, a := range g.Approach {
			add(fmt.Sprintf("pull_groups[%d].approach[%d]", gi, ai), a)
		}
		for pi, p := range g.Positions {
			add(fmt.Sprintf("pull_groups[%d].positions[%d]", gi, pi), p)
		}
	}
	for si, s := range l.Steps {
		if s.UseObject != nil && s.UseObject.Stand != nil {
			add(fmt.Sprintf("steps[%d].use_object.stand", si), *s.UseObject.Stand)
		}
		if s.Ritual != nil && s.Ritual.Stand != nil {
			add(fmt.Sprintf("steps[%d].ritual.stand", si), *s.Ritual.Stand)
		}
		if s.LootObject != nil && s.LootObject.Stand != nil {
			add(fmt.Sprintf("steps[%d].loot_object.stand", si), *s.LootObject.Stand)
		}
		if s.AreaTrigger != nil {
			add(fmt.Sprintf("steps[%d].area_trigger.stand", si), s.AreaTrigger.Stand)
		}
	}
	if l.Boss != nil {
		add("boss.position", l.Boss.Position)
	}
	for si, s := range l.AfterBoss {
		if lo := s.LootObject; lo != nil {
			switch {
			case lo.Stand != nil:
				add(fmt.Sprintf("after_boss[%d].loot_object.stand", si), *lo.Stand)
			case lo.Near != nil:
				add(fmt.Sprintf("after_boss[%d].loot_object.near", si), *lo.Near)
			}
		}
	}
	return pts, labels, direct
}

// Every point the Uldaman plan walks to or pulls from has navmesh floor under it (within meshMaxDZ yd vertically, meshMaxDH on the
// ground), and each point is reachable from the one before it with a complete path_to, in the order the driver goes (legs in order, a
// leg's points as planPoints lists them). The one exception is the direct step across the known hole at the scorpid pit (see sentinelLeg), which the navmesh does not link. Checked against the hires-1 trace (2026-10-14): the 23 `unreachable` refusals were
// `cmd move` orders to the lead bot's own position after a fight (the server cannot path to where it already stands), not plan points,
// and all 23 of those spots are on this mesh. Runs where the map 70 mmaps are (see mmapsDir); needs the cgo build.
func TestUldamanPlanPointsOnMesh(t *testing.T) {
	dir := mmapsDir()
	if dir == "" {
		t.Skip("no map 70 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 070.mmap and the 070*.mmtile tiles")
	}
	p := loadUldaman(t)
	pl := navmesh.NewPlanner(dir)
	defer pl.Close()
	prev := Point{X: p.Entrance.X, Y: p.Entrance.Y, Z: p.Entrance.Z}
	prevLabel := "entrance"
	checked := 0
	for i, l := range p.Legs {
		pts, labels, direct := planPoints(l)
		for j, w := range pts {
			where := fmt.Sprintf("legs[%d] (%s) %s %v", i, l.Name, labels[j], w)
			n, ok, err := pl.Nearest(p.Map, navmesh.Vec3{w.X, w.Y, w.Z})
			if errors.Is(err, navmesh.ErrNoEngine) {
				t.Skip("built without cgo: no navmesh engine")
			}
			if err != nil {
				t.Fatalf("%s: %v", where, err)
			}
			if _, hole := holeFloor[w]; hole && direct[j] {
				// inside the hole: its floor is checked on the vmap (holeFloor)
			} else if !ok {
				t.Errorf("%s is not on the navmesh (no walkable polygon within 4 yd)", where)
			} else if dz, dh := math.Abs(float64(n[2]-w.Z)), math.Hypot(float64(n[0]-w.X), float64(n[1]-w.Y)); dz > meshMaxDZ || dh > meshMaxDH {
				t.Errorf("%s: the nearest navmesh floor %.1f %.1f %.1f is %.1f yd above or below it and %.1f yd off on the ground", where, n[0], n[1], n[2], dz, dh)
			}
			path, err := pl.Plan(p.Map, navmesh.Vec3{prev.X, prev.Y, prev.Z}, navmesh.Vec3{w.X, w.Y, w.Z})
			if err != nil {
				t.Fatalf("%s: %v", where, err)
			}
			if _, shelf := shelfPacks[w]; shelf {
				if len(path.Points) == 0 {
					t.Fatalf("%s: no path toward the shelf pack", where)
				}
				end := path.Points[len(path.Points)-1]
				if d := math.Hypot(float64(end[0]-w.X), float64(end[1]-w.Y)); d > shelfReach {
					t.Errorf("%s: the path toward the shelf pack ends %.1f yd from it", where, d)
				}
				checked++
				continue // the party stays where it pulled from
			}
			switch {
			case path.Status == navmesh.Complete:
			case direct[j]:
				// The hole: the walk from the pit lip goes partial and ends on the west side (hires-1, 12:41:02: the bots stopped on the pit
				// floor at -161.1 284.5 -52.1, 4.4 yd from the old goal). The plan crosses it with a direct step (Leg.Via), which the
				// navmesh does not link to the point before it; the end needs floor (checked above) and the step is short (TestUldamanDirectSteps).
				if len(path.Points) == 0 {
					t.Fatalf("%s: partial path with no points", where)
				}
			default:
				t.Errorf("%s: path_to from %s %v is %s: %s", where, prevLabel, prev, path.Status, path.Reason)
			}
			prev, prevLabel = w, labels[j]
			checked++
		}
	}
	if checked < 100 {
		t.Fatalf("only %d points checked", checked)
	}
	t.Logf("%d plan points on the map 70 navmesh in %s", checked, dir)
}

// The plan crosses the scorpid pit's navmesh hole with two direct steps (`cmd move ... direct`), and a direct step is short: within the
// limits TortoiseBots enforces (25 yd across, 8 up or down) of the via point before it, which is where the party gathers. Without the
// navmesh files only the lengths are checked; with them, both ends have navmesh floor (the server's own check is the map height within
// 3 yd, which the mesh floor stands in for here). The east end is also the leg's `to`, so the walk to `to` after the step is no walk.
func TestUldamanDirectSteps(t *testing.T) {
	p := loadUldaman(t)
	var pl *navmesh.Planner
	if dir := mmapsDir(); dir != "" {
		pl = navmesh.NewPlanner(dir)
		defer pl.Close()
	}
	found := 0
	for i, l := range p.Legs {
		for j, w := range l.Via {
			if !w.Direct {
				continue
			}
			found++
			if i != sentinelLeg {
				t.Errorf("legs[%d] (%s) via[%d]: only the scorpid pit's ramp needs a direct step", i, l.Name, j)
			}
			if j == 0 {
				t.Fatalf("legs[%d] via[0] is direct: no via point before it", i)
			}
			from := l.Via[j-1].Point
			if !DirectStepOK(from, w.Point) {
				t.Errorf("legs[%d] via[%d]: %.1f yd across and %.1f up or down from %v: over the limits %g and %g", i, j, hdist(from, w.Point), math.Abs(float64(w.Z-from.Z)), from, DirectMaxDistance, DirectMaxVertical)
			}
			// The direct steps are the last via points, in a row, and the last one ends on the leg's `to`.
			if j == len(l.Via)-1 && w.Point != l.To {
				t.Errorf("legs[%d] via[%d]: the last direct step should end on the leg's `to`", i, j)
			}
			if j < len(l.Via)-1 && !l.Via[j+1].Direct {
				t.Errorf("legs[%d] via[%d]: a direct step should be followed only by direct steps", i, j)
			}
			if pl == nil {
				continue
			}
			if _, hole := holeFloor[w.Point]; hole && (j == len(l.Via)-1 || !l.Via[j+1].Direct) {
				t.Errorf("legs[%d] via[%d] %v: a point inside the navmesh hole must be followed by a direct step", i, j, w.Point)
			}
			for label, at := range map[string]Point{"start": from, "end": w.Point} {
				if _, hole := holeFloor[at]; hole {
					continue // inside the hole: its floor is checked on the vmap (holeFloor)
				}
				n, ok, err := pl.Nearest(p.Map, navmesh.Vec3{at.X, at.Y, at.Z})
				if errors.Is(err, navmesh.ErrNoEngine) {
					t.Skip("built without cgo: no navmesh engine")
				}
				if err != nil {
					t.Fatal(err)
				}
				if dz := math.Abs(float64(n[2] - at.Z)); !ok || dz > DirectGroundTolerance || math.Hypot(float64(n[0]-at.X), float64(n[1]-at.Y)) > meshMaxDH {
					t.Errorf("legs[%d] via[%d] %s %v: no navmesh floor under it (nearest %v, found %v)", i, j, label, at, n, ok)
				}
			}
		}
	}
	if found != 2 {
		t.Fatalf("the plan has %d direct steps, want the two across the scorpid pit's hole (to the pit floor, then up the ramp to the east mesh)", found)
	}
	if v := p.Legs[sentinelLeg].Via; len(v) != 3 || !v[1].Direct || !v[2].Direct {
		t.Fatalf("legs[%d] via %v: want the lip, then the two direct steps", sentinelLeg, v)
	} else if v[1].Point != pitFloor {
		t.Errorf("legs[%d] via[1] %v: the first direct step should end on the vmap-checked pit floor point %v (pitFloor)", sentinelLeg, v[1].Point, pitFloor)
	}
	if p.Legs[sentinelLeg].Name != "sentinel" {
		t.Fatalf("legs[%d] is %q, the sentinel leg", sentinelLeg, p.Legs[sentinelLeg].Name)
	}
}

// Uldaman A run 6 (2026-10-14): Grimlok's corridor wiped at pull group 4 because the trio of two Brawlers and a Geomancer 25 yd east of the
// group of three at 55 417 aggroed on the tank's way to it. Every Stonevault Brawler and Geomancer of the corridor that is not Grimlok's own
// group is a position of a pull group, and the trio is pulled before the group of three.
func TestUldamanGrimlokCorridorHasNoUnplannedPack(t *testing.T) {
	p := loadUldaman(t)
	var leg *Leg
	for i := range p.Legs {
		if p.Legs[i].Name == "grimlok" {
			leg = &p.Legs[i]
		}
	}
	if leg == nil {
		t.Fatal("no grimlok leg")
	}
	planned := func(m map[string]string) int {
		for gi, g := range leg.PullGroups {
			for _, at := range g.Positions {
				if c := mustPoint(m); dist3(Point{at.X, at.Y, 0}, Point{c.X, c.Y, 0}) < 0.5 {
					return gi
				}
			}
		}
		return -1
	}
	for _, m := range tsv(t, "creatures.tsv") {
		x, y := mustPoint(m).X, mustPoint(m).Y
		if (m["entry"] != "4853" && m["entry"] != "4855") || m["group_leader"] == "27528" || x < 25 || y < 360 || y > 450 {
			continue
		}
		if planned(m) < 0 {
			t.Errorf("creature %s (entry %s) at %.1f %.1f is in no pull group of the grimlok leg", m["guid"], m["entry"], x, y)
		}
	}
	trio, three := -1, -1
	for gi, g := range leg.PullGroups {
		for _, at := range g.Positions {
			if dist3(Point{at.X, at.Y, 0}, Point{78.9, 404.9, 0}) < 1 {
				trio = gi
			}
			if dist3(Point{at.X, at.Y, 0}, Point{55.8, 416.1, 0}) < 1 {
				three = gi
			}
		}
	}
	if trio < 0 || three < 0 || trio >= three || fmt.Sprint(leg.PullGroups[trio].Entries) != "[4855 4855 4853]" {
		t.Errorf("the trio at 79 405 is group %d, the group of three at 56 416 is group %d: the trio comes first", trio, three)
	}
}

func mustPoint(m map[string]string) Point {
	f := func(k string) float32 { v, _ := strconv.ParseFloat(m[k], 32); return float32(v) }
	return Point{f("x"), f("y"), f("z")}
}

// The rest gates of the legs after the Sentinel (Uldaman A run 5/6: the healer pulled at 63 % mana with the default 50 % and Galgann's
// section wiped; with these gates it went clean): the party is full before every pull of the big sections.
func TestUldamanLateLegsRestUntilTheHealerIsFull(t *testing.T) {
	p := loadUldaman(t)
	for i := 6; i <= 12; i++ {
		r := p.Legs[i].Rest
		if r == nil || r.Health != 95 || r.HealerMana != 90 || r.Mana != 70 || r.MaxS != 150 {
			t.Errorf("leg %d (%s): rest %+v", i, p.Legs[i].Name, r)
		}
	}
	// The casters that run at 15 % health are named, so that the driver kills them before they are out of reach.
	if fmt.Sprint(p.Fleers) != "[4853 4848 4849 7321]" {
		t.Errorf("fleers %v", p.Fleers)
	}
}
