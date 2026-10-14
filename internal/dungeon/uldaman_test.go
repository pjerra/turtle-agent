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
	if p.Map != 70 || p.Name != "uldaman" || len(p.Legs) != 13 {
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
	if fmt.Sprint(names) != "[sentinel ancient_stone_keeper galgann grimlok keepers_door keepers_altar keepers descent archaedas]" ||
		fmt.Sprint(bosses) != "[7023 7206 7291 4854 0 0 0 0 2748]" {
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
	for name, want := range map[string]string{"sentinel": "[124370]", "keepers_door": "[124368]", "keepers_altar": "[130511]", "archaedas": "[141869 133234]"} {
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
	pts := []Point{l.To}
	for _, g := range l.PullGroups {
		pts = append(pts, g.Positions...)
	}
	for _, s := range l.Steps {
		if s.UseObject != nil && s.UseObject.Stand != nil {
			pts = append(pts, *s.UseObject.Stand)
		}
	}
	if l.Boss != nil {
		pts = append(pts, l.Boss.Position)
	}
	for _, s := range l.AfterBoss { // the driver walks to a chest's own position unless the step says where to stand
		if lo := s.LootObject; lo != nil {
			switch {
			case lo.Stand != nil:
				pts = append(pts, *lo.Stand)
			case lo.Near != nil:
				pts = append(pts, *lo.Near)
			}
		}
	}
	return pts
}

// sentinelLeg is the index of the first leg after Ironaya's: its first waypoint is across the navmesh hole north of Ironaya's room.
const sentinelLeg = 4

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
		for j, w := range waypoints(l) {
			to := [3]float32{w.X, w.Y, w.Z}
			path, err := pl.Plan(p.Map, from, to)
			if errors.Is(err, navmesh.ErrNoEngine) {
				t.Skip("built without cgo: no navmesh engine")
			}
			if err != nil {
				t.Fatalf("legs[%d] waypoint %d: %v", i, j, err)
			}
			if i == sentinelLeg && j == 0 && path.Status != navmesh.Complete {
				// The one known hole of the map 70 navmesh: Ironaya's room (west part) is not linked to the Back Entrance corridor
				// (east part) by a tile of the mesh; the way is a passage of about 45 yd north of the room that the mesh lacks. The
				// walk crosses it on the straight line (server-side movement), which this test cannot check: the waypoints after it
				// are checked from the leg's own start. A mesh that closes the hole makes this branch dead; delete it then.
				end := path.Points[len(path.Points)-1]
				if gap := math.Hypot(float64(end[0]-w.X), float64(end[1]-w.Y)); gap > 60 {
					t.Errorf("legs[%d] waypoint 0: the navmesh hole is %.0f yd wide, the plan assumes about 45-55", i, gap)
				}
				from = to
				checked++
				continue
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
