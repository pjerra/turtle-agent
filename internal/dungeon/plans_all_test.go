package dungeon

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

const plansDir = "../../dungeons"

func parseT(t *testing.T, doc string) (*Plan, error) {
	t.Helper()
	return Parse("t.json", []byte(doc))
}

const planHead = `{"version":1,"name":"t",%s"map":1,"entrance":{"x":0,"y":0,"z":0},%s"legs":[{"to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":0,"y":0,"z":0}]}]}]}`

func TestPlanKindStatusAndParty(t *testing.T) {
	doc := func(pre, post string) string { return fmt.Sprintf(planHead, pre, post) }
	p, err := parseT(t, doc(`"kind":"dungeon","status":"draft (not mapped, not live-checked)",`, `"hire_composition":["warrior:tank","priest:healer","mage:dps"],`))
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != KindDungeon || p.IsRaid() || p.Status != DraftStatus || len(p.HireComposition) != 3 {
		t.Fatalf("%+v", p)
	}
	r, err := parseT(t, doc(`"kind":"raid","status":"live-checked",`, `"raid":{"size":40,"tanks":4,"healers":10,"dps":26},`))
	if err != nil || !r.IsRaid() || r.Raid.Size != 40 || r.Status != "live-checked" {
		t.Fatalf("%v %+v", err, r)
	}
	for _, c := range []struct{ pre, post, want string }{
		{`"kind":"zoo",`, ``, `kind: "zoo"`},
		{`"status":"nearly done",`, ``, `status: "nearly done": start with draft, mapped or live-checked`},
		{``, `"hire_composition":["warrior:tank"],`, "hire_composition"},
		{``, `"hire_composition":["warrior:tank","priest:healer","mage:healer"],`, "hire_composition"},
		{`"kind":"raid",`, ``, "raid: required for a raid plan"},
		{``, `"raid":{"size":40,"tanks":4,"healers":10,"dps":26},`, "raid: only a plan of kind"},
		{`"kind":"raid",`, `"raid":{"size":25,"tanks":4,"healers":6,"dps":15},`, "raid.size: 25: use 10, 20 or 40"},
		{`"kind":"raid",`, `"raid":{"size":40,"tanks":4,"healers":10,"dps":20},`, "is 34, the size is 40"},
		{`"kind":"raid",`, `"raid":{"size":10,"tanks":0,"healers":3,"dps":7},`, "at least one tank"},
		{`"kind":"raid",`, `"hire_composition":["warrior:tank","priest:healer"],"raid":{"size":10,"tanks":2,"healers":3,"dps":5},`, "a raid plan has a raid block instead"},
	} {
		if _, err := parseT(t, doc(c.pre, c.post)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %s: got %v, want %q", c.pre, c.post, err, c.want)
		}
	}
}

// offlineMapped are the plans whose legs, doors and steps were written by hand (from the export, the scripts and the walks), so they carry
// steps and are still drafts. A plan leaves the list when its status moves on: TestOfflineMappedPlansAreStillDrafts fails for one that does not.
var offlineMapped = map[string]bool{"deadmines": true, "molten_core": true, "blackwing_lair": true, "wailing_caverns-extra": true, "temple_of_ahnqiraj": true, "ruins_of_ahnqiraj": true, "lower_karazhan_halls": true, "emerald_sanctum": true, "naxxramas": true, "zul_gurub": true}

// Every plan with a level band carries its tank_min_hp, from the tank's max health in the 2026-10 test runs
// (~/TortoiseBots-audit/specs/ta-266-tank-floors.md: 0.9 x the weakest clean run of the plan, scaled to the hire level; Ragefire
// Chasm keeps the issue's own 800). A raid carries none (its tank's gear tier is the raid roster's), and neither does a plan
// without a band.
func TestDungeonPlansCarryTheTankFloor(t *testing.T) {
	want := map[string]uint32{
		"black_morass":          5300,
		"blackfathom_deeps":     1500,
		"blackrock_depths":      5000,
		"crescent_grove":        2700,
		"deadmines":             1200,
		"dire_maul_east":        5100,
		"dire_maul_north":       5300,
		"dire_maul_west":        5200,
		"dragonmaw_retreat":     2050,
		"frostmane_hollow":      750,
		"gilneas_city":          3450,
		"gnomeregan":            2250,
		"hateforge_quarry":      4600,
		"karazhan_crypt":        5300,
		"lower_blackrock_spire": 5200,
		"maraudon_orange":       3700,
		"maraudon_purple":       3250,
		"ragefire_chasm":        800,
		"razorfen_downs":        3100,
		"razorfen_kraul":        2050,
		"scarlet_armory":        3100,
		"scarlet_cathedral":     3150,
		"scarlet_graveyard":     2550,
		"scarlet_library":       2850,
		"scholomance":           5300,
		"shadowfang_keep":       1350,
		"stormwind_stockade":    1550,
		"stormwind_vault":       5300,
		"stormwrought_ruins":    2850,
		"stratholme_live":       5300,
		"stratholme_undead":     5300,
		"sunken_temple":         4000,
		"uldaman":               2850,
		"wailing_caverns":       850,
		"windhorn_canyon":       1750,
		"zulfarrak":             2700,
	}
	files, err := filepath.Glob(filepath.Join(plansDir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("%d plan files: %v", len(files), err)
	}
	seen := map[string]bool{}
	for _, f := range files {
		p, err := Load(f)
		if err != nil {
			t.Errorf("%v", err)
			continue
		}
		seen[p.Name] = true
		if floor, ok := want[p.Name]; ok {
			if p.TankMinHP != floor {
				t.Errorf("%s tank_min_hp is %d, want %d", f, p.TankMinHP, floor)
			}
			continue
		}
		if lo, _ := p.Band(); !p.IsRaid() && lo != 0 {
			t.Errorf("%s has a level band but no floor in the table: give it a tank_min_hp or take it off the band", f)
			continue
		}
		if p.TankMinHP != 0 {
			t.Errorf("%s carries tank_min_hp %d but is a raid or has no band", f, p.TankMinHP)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("the floor table names %s.json, which does not load", name)
		}
	}
}

func TestOfflineMappedPlansAreStillDrafts(t *testing.T) {
	for name := range offlineMapped {
		p, err := Load(filepath.Join(plansDir, name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(p.Status, StatusDraft) {
			t.Errorf("%s is %q now: take it off offlineMapped (and drop the exemptions that are tied to it)", name, p.Status)
		}
	}
}

// Every plan file loads, says how far it has been checked, and is kept in the form Marshal writes. A raid plan has its group; a draft is a
// draft all the way (no hand-written steps in it, which would make "not mapped" a lie only in part).
func TestEveryPlanFileLoads(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(plansDir, "*.json"))
	if err != nil || len(files) < 40 {
		t.Fatalf("%d plan files: %v", len(files), err)
	}
	drafts, raids := 0, 0
	for _, f := range files {
		p, err := Load(f)
		if err != nil {
			t.Errorf("%v", err)
			continue
		}
		if p.Status == "" {
			t.Errorf("%s: no status", f)
		}
		raw, _ := os.ReadFile(f)
		if out, err := Marshal(p); err != nil || !bytes.Equal(raw, out) {
			t.Errorf("%s is not in Marshal's form: run `go run ./tools/dungeongen -format %s`", f, f)
		}
		if p.IsRaid() {
			raids++
			if p.Raid == nil {
				t.Errorf("%s: raid plan without a raid block", f)
			}
		}
		if strings.HasPrefix(p.Status, StatusDraft) {
			drafts++
			for i, l := range p.Legs {
				if offlineMapped[p.Name] {
					continue // written by hand from the export, the scripts and the navmesh; still a draft until the mapping walk
				}
				if len(l.Steps) != 0 || len(l.AfterBoss) != 0 {
					t.Errorf("%s legs[%d]: a draft has no steps; they come with the mapping walk", f, i)
				}
			}
		}
	}
	if drafts < 40 || raids < 10 {
		t.Errorf("%d drafts, %d raids", drafts, raids)
	}
}

// mapMmaps finds the navmesh files of an instance map (<map>.mmap and <map>*.mmtile, three digits): $TURTLE_AGENT_MMAPS, the VM's per-map folder
// or the server host's folder.
func mapMmaps(m uint32) string {
	home, _ := os.UserHomeDir()
	n := fmt.Sprintf("%03d", m)
	for _, d := range []string{os.Getenv("TURTLE_AGENT_MMAPS"), filepath.Join(home, "tb-ci", "mmaps-turtle"+n), filepath.Join(home, "tortoise-wow-server", "data", "mmaps")} {
		if d == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(d, n+".mmap")); err != nil {
			continue
		}
		if tiles, _ := filepath.Glob(filepath.Join(d, n+"*.mmtile")); len(tiles) > 0 {
			return d
		}
	}
	return ""
}

// Every plan's walk points against the navmesh of their map, where the machine has it: each must have mesh floor under it (meshMaxDZ up or down,
// meshMaxDH sideways; a pull position, a creature's own, gets 4 and 2.5). A point the generator could not snap to the mesh (tools/dungeongen
// snaps.json) carries needs_walk and is left out here, and counted in the log. The maps without navmesh files are listed, so a run says what
// it did not check.
func TestPlansOnNavmeshWhereTheMachineHasIt(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(plansDir, "*.json"))
	var missing []string
	seen := map[uint32]bool{}
	checkedAny := false
	for _, f := range files {
		p, err := Load(f)
		if err != nil {
			t.Fatal(err)
		}
		if p.Name == "uldaman" || strings.HasPrefix(p.Name, "zulfarrak") {
			continue
		}
		dir := mapMmaps(p.Map)
		if dir == "" {
			if !seen[p.Map] {
				missing = append(missing, fmt.Sprint(p.Map))
				seen[p.Map] = true
			}
			continue
		}
		pl := navmesh.NewPlanner(dir)
		on, total, walk := 0, 0, 0
		for i, l := range p.Legs {
			pts, labels, _ := planPoints(l)
			flagged := needsWalkPoints(l)
			for j, w := range pts {
				if flagged[w] {
					walk++
					continue
				}
				n, ok, err := pl.Nearest(p.Map, navmesh.Vec3{w.X, w.Y, w.Z})
				if errors.Is(err, navmesh.ErrNoEngine) {
					pl.Close()
					t.Skip("built without cgo: no navmesh engine")
				}
				if err != nil {
					pl.Close()
					t.Fatalf("%s: %v", p.Name, err)
				}
				total++
				maxDZ, maxDH := meshMaxDZ, meshMaxDH
				if isPullPosition(l, w) {
					maxDZ, maxDH = 4, 2.5
				}
				if ok && math.Abs(float64(n[2]-w.Z)) <= maxDZ && math.Hypot(float64(n[0]-w.X), float64(n[1]-w.Y)) <= maxDH {
					on++
				} else {
					t.Errorf("%s legs[%d] (%s) %s %v is not on the navmesh: run `go run ./tools/dungeongen -spec tools/dungeongen/plans.json -snap-update ~/tb-ci` and regenerate, or mark it needs_walk", p.Name, i, l.Name, labels[j], w)
				}
			}
		}
		pl.Close()
		checkedAny = true
		t.Logf("%s (%s): %d of %d points on the map %d navmesh in %s (%d more marked needs_walk)", p.Name, p.Status, on, total, p.Map, dir, walk)
	}
	if len(missing) > 0 {
		t.Logf("no navmesh files for maps %s (copy <map>.mmap and <map>*.mmtile from m910q's ~/tortoise-wow-server/data/mmaps to ~/tb-ci/mmaps-turtle<map>)", strings.Join(missing, ", "))
	}
	if !checkedAny {
		t.Skip("no draft plan's map has navmesh files here")
	}
}

// needsWalkPoints are the points of a leg marked needs_walk: its To, a flagged pull group's positions, a flagged boss's position,
// and a via point with a check_skip reason (a hop the navmesh cannot judge on purpose, e.g. Karazhan Crypt's swim up the flooded
// shaft: the mmaps have no swim volume).
func needsWalkPoints(l Leg) map[Point]bool {
	m := map[Point]bool{}
	if l.NeedsWalk {
		m[l.To] = true
	}
	for _, v := range l.Via {
		if v.CheckSkip != "" {
			m[v.Point] = true
		}
	}
	for _, g := range l.PullGroups {
		if g.NeedsWalk {
			for _, q := range g.Positions {
				m[q] = true
			}
		}
	}
	if l.Boss != nil && l.Boss.NeedsWalk {
		m[l.Boss.Position] = true
	}
	return m
}

// A plan's own party is the hire step's default; the prepare block still wins.
func TestHireTakesThePlansComposition(t *testing.T) {
	g := newHireFake(t)
	g.plan.HireComposition = []string{"warrior:tank", "priest:healer", "mage:dps"}
	rep, err := g.run(hireSpec())
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if got := strings.Join(g.hireLines, "|"); got != "hire warrior tank level=43|hire priest healer level=43|hire mage dps level=43" {
		t.Fatalf("%s", got)
	}
	g = newHireFake(t)
	g.plan.HireComposition = []string{"warrior:tank", "priest:healer", "mage:dps"}
	spec := hireSpec()
	spec.Hire = &HireSpec{Composition: []string{"warrior", "priest"}}
	if rep, err = g.run(spec); err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if got := strings.Join(g.hireLines, "|"); got != "hire warrior tank level=43|hire priest healer level=43" {
		t.Fatalf("%s", got)
	}
}
