package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
)

const (
	specFile  = "plans.json"
	plansDirT = "../../dungeons"
	tableFile = "../../docs/PLANS.md"
)

// instanceMaps are the 40 instance maps of the server (the folders of ~/turtle-agent-minimaps): vanilla dungeons and raids and Turtle's customs 800-822.
var instanceMaps = []uint32{33, 34, 35, 36, 43, 47, 48, 70, 90, 109, 129, 189, 209, 229, 230, 249, 269, 289, 309, 329, 349, 389, 409, 429, 469, 509, 531, 532, 533,
	800, 802, 807, 808, 814, 815, 816, 818, 819, 820, 822}

func loadSpecT(t *testing.T) *SpecFile {
	t.Helper()
	sf, err := LoadSpec(specFile)
	if err != nil {
		t.Fatal(err)
	}
	return sf
}

// Every instance map has at least one plan, and the spec names every plan file in dungeons/ (bar wailing_caverns-extra, a record).
func TestSpecCoversEveryInstanceMapAndFile(t *testing.T) {
	sf := loadSpecT(t)
	have := map[uint32]bool{}
	named := map[string]bool{"wailing_caverns-extra": true}
	for _, ps := range sf.Plans {
		have[ps.Map] = true
		named[ps.Name] = true
	}
	for _, m := range instanceMaps {
		if !have[m] {
			t.Errorf("map %d has no plan in the spec", m)
		}
	}
	files, _ := filepath.Glob(filepath.Join(plansDirT, "*.json"))
	for _, f := range files {
		if n := strings.TrimSuffix(filepath.Base(f), ".json"); !named[n] {
			t.Errorf("%s is in dungeons/ but not in the spec", f)
		}
	}
	if len(files) != len(sf.Plans)+1 {
		t.Errorf("%d plan files for %d spec plans (+ wailing_caverns-extra)", len(files), len(sf.Plans))
	}
}

// The committed drafts are what the generator writes from the spec and the exports: regenerate with
// `go run ./tools/dungeongen -spec tools/dungeongen/plans.json -out-dir dungeons -table docs/PLANS.md` after changing either.
func TestDraftPlansAreWhatTheGeneratorWrites(t *testing.T) {
	sf := loadSpecT(t)
	out := t.TempDir()
	files, err := GenerateAll(sf, "testdata", out)
	if err != nil {
		t.Fatal(err)
	}
	// Exactly the spec's generated (not handwritten) plans: a prepped plan leaves the generated set on purpose, so a fixed
	// floor broke with every prep; an exact count still catches a generator that silently writes fewer (or none).
	generated := 0
	for _, ps := range sf.Plans {
		if !ps.Handwritten {
			generated++
		}
	}
	if generated == 0 || len(files) != generated {
		t.Fatalf("%d plans generated, want the spec's %d generated plans", len(files), generated)
	}
	for _, f := range files {
		want, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(plansDirT, filepath.Base(f)))
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(f), err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("dungeons/%s is not what the generator writes from the spec: regenerate it", filepath.Base(f))
		}
	}
}

// Every generated plan loads, is a draft, has the fields its kind needs, and passes the creature check: no boss or pack member is an NPC
// (npc_flags) or friendly (faction 35) unless the spec says a script turns it hostile.
func TestDraftPlansLoadAndPassTheCheck(t *testing.T) {
	sf := loadSpecT(t)
	for _, ps := range sf.Plans {
		if ps.Handwritten {
			continue
		}
		p, err := dungeon.Load(filepath.Join(plansDirT, ps.Name+".json"))
		if err != nil {
			t.Errorf("%s: %v", ps.Name, err)
			continue
		}
		if p.Name != ps.Name || p.Map != ps.Map || p.Kind != ps.Kind || p.Status != dungeon.DraftStatus {
			t.Errorf("%s: name %q map %d kind %q status %q", ps.Name, p.Name, p.Map, p.Kind, p.Status)
		}
		if lo, hi := p.Band(); lo == 0 || hi == 0 {
			t.Errorf("%s: no level band", ps.Name)
		}
		if len(p.Legs) != len(ps.Bosses) {
			t.Errorf("%s: %d legs for %d bosses", ps.Name, len(p.Legs), len(ps.Bosses))
		}
		if ps.Trigger != 0 && (p.EntranceTrigger != ps.Trigger || p.Portal == nil) {
			t.Errorf("%s: trigger %d portal %+v", ps.Name, p.EntranceTrigger, p.Portal)
		}
		if ps.Kind == dungeon.KindRaid {
			if p.Raid == nil || p.Raid.Size != ps.Size || len(p.HireComposition) != 0 {
				t.Errorf("%s: raid block %+v", ps.Name, p.Raid)
			}
		} else if n := len(p.HireComposition); n < 2 || n > 5 {
			t.Errorf("%s: hire_composition %v", ps.Name, p.HireComposition)
		}
		for i, l := range p.Legs {
			if l.Boss == nil || l.Boss.Entry == 0 {
				t.Errorf("%s legs[%d]: no boss", ps.Name, i)
			}
		}
		cs, err := readCreatures(filepath.Join("testdata", ps.Data, "creatures.tsv"))
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range Check(p, cs, CheckOptions{Scripted: toSet(ps.Scripted)}) {
			if strings.Contains(w, "is an NPC, not a mob") || strings.Contains(w, "is friendly to") {
				t.Errorf("%s: %s", ps.Name, w)
			}
		}
		// every boss is a real spawn of the export (the generator already fails otherwise) and no boss.first entry is also a pull-group member
		for i, l := range p.Legs {
			for _, e := range l.Boss.First {
				for j, g := range l.PullGroups {
					for _, m := range g.Entries {
						if m == e {
							t.Errorf("%s legs[%d].pull_groups[%d]: %d is a boss.first entry", ps.Name, i, j, e)
						}
					}
				}
			}
		}
	}
}

func TestPlansTableIsCurrent(t *testing.T) {
	sf := loadSpecT(t)
	want, err := Table(sf, plansDirT)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(tableFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Error("docs/PLANS.md is out of date: regenerate it with `go run ./tools/dungeongen -spec tools/dungeongen/plans.json -table docs/PLANS.md`")
	}
	for _, m := range sf.VMMaps {
		if !slices.Contains(instanceMaps, m) && m != 1 {
			t.Errorf("vm_mmaps names map %d: not an instance map (or map 1)", m)
		}
	}
}

func TestSpecErrors(t *testing.T) {
	dir := t.TempDir()
	write := func(s string) string {
		f := filepath.Join(dir, "s.json")
		if err := os.WriteFile(f, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		return f
	}
	for _, c := range []struct{ in, want string }{
		{`{"plans":[{"name":"a","title":"A","map":1,"kind":"zoo","data":"x","bosses":[1]}]}`, "kind is dungeon or raid"},
		{`{"plans":[{"name":"a","title":"A","map":1,"kind":"raid","data":"x","bosses":[1]}]}`, "needs a size"},
		{`{"plans":[{"name":"a","title":"A","map":1,"kind":"dungeon","bosses":[1]}]}`, "data and bosses are required"},
		{`{"plans":[{"name":"a","title":"A","map":1,"kind":"dungeon","data":"x","bosses":[1]},{"name":"a","title":"A","map":1,"kind":"dungeon","data":"x","bosses":[1]}]}`, "used twice"},
		{`{"plans":[{"name":"a","title":"A","map":1,"kind":"dungeon","data":"x","bosses":[{"entry":1,"frist":[2]}]}]}`, "unknown field"},
	} {
		if _, err := LoadSpec(write(c.in)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", c.in, err, c.want)
		}
	}
}

// A boss with first entries keeps them out of the packs; route order walks nearest-first with the last boss last; the band comes from the packs.
func TestGenerateSpecSynthetic(t *testing.T) {
	dir := t.TempDir()
	d := filepath.Join(dir, "m1")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	head := "guid\tentry\tname\tlevel_min\tlevel_max\trank\tfaction\tnpc_flags\tx\ty\tz\twander\tmovement_type\tgroup_leader\tpatrol\tauras\n"
	row := func(guid, entry int, name string, lvl, rank, faction int, x float64) string {
		return strings.Join([]string{itoa(guid), itoa(entry), name, itoa(lvl), itoa(lvl), itoa(rank), itoa(faction), "0", ftoa(x), "0", "0", "0", "0", "0", "0", ""}, "\t") + "\n"
	}
	cs := head + row(1, 10, "Trash A", 20, 0, 59, 10) + row(2, 11, "Trash B", 22, 0, 59, 100) + row(3, 12, "Trash C", 24, 0, 59, 200) +
		row(4, 900, "Near Boss", 25, 3, 59, 40) + row(5, 901, "Far Boss", 25, 3, 59, 300) + row(6, 902, "Add", 25, 3, 59, 302)
	files := map[string]string{
		"creatures.tsv":   cs,
		"gameobjects.tsv": "guid\tentry\tname\ttype\tflags\tx\ty\tz\tstate\n",
		"entrances.tsv":   "id\tname\ttarget_map\tx\ty\tz\to\n7\tIn\t1\t0\t0\t0\t0\n",
		"portals.tsv":     "target_map\tid\tname\trequired_level\touter_map\tx\ty\tz\tradius\tbox_x\tbox_y\tbox_z\ttarget_x\ttarget_y\ttarget_z\n1\t7\tIn\t10\t0\t5\t6\t7\t3\t0\t0\t0\t0\t0\t0\n",
	}
	for n, c := range files {
		if err := os.WriteFile(filepath.Join(d, n), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ps := PlanSpec{Name: "syn", Title: "Syn", Map: 1, Kind: dungeon.KindDungeon, Data: "m1", Trigger: 7, Route: true,
		Bosses: []BossSpec{{Entry: 901, First: []uint32{902}}, {Entry: 900}}}
	p, err := GenerateSpec(ps, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Legs) != 2 || p.Legs[0].Boss.Entry != 900 || p.Legs[1].Boss.Entry != 901 || len(p.Legs[1].Boss.First) != 1 {
		t.Fatalf("%+v", p.Legs)
	}
	for _, l := range p.Legs {
		for _, g := range l.PullGroups {
			for _, e := range g.Entries {
				if e == 902 {
					t.Errorf("the first entry 902 is a pack")
				}
			}
		}
	}
	if p.Portal == nil || p.Portal.Map != 0 || p.Portal.X != 5 || p.EntranceTrigger != 7 {
		t.Errorf("portal %+v trigger %d", p.Portal, p.EntranceTrigger)
	}
	if lo, hi := p.Band(); lo != 20 || hi != 25 { // median 22 -> 20; p90 24 but at least median + 3 = 25
		t.Errorf("band %d-%d", lo, hi)
	}
	if p.Status != dungeon.DraftStatus || len(p.HireComposition) != 5 {
		t.Errorf("status %q composition %v", p.Status, p.HireComposition)
	}
	// a bbox keeps the bosses and drops the packs outside
	ps.Bbox = [][4]float64{{0, -10, 50, 10}}
	p, err = GenerateSpec(ps, dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, l := range p.Legs {
		n += len(l.PullGroups)
	}
	if n != 1 {
		t.Errorf("%d packs in the box, want 1", n)
	}
}

func itoa(n int) string     { return strconv.Itoa(n) }
func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// ApplySnaps moves a listed point wherever it stands (to, pull positions, boss), marks the ones with no floor, and leaves the rest.
func TestApplySnaps(t *testing.T) {
	pt := func(x float32) dungeon.Point { return dungeon.Point{X: x, Y: 1, Z: 2} }
	to := pt(10)
	p := &dungeon.Plan{Legs: []dungeon.Leg{{To: pt(1), PullGroups: []dungeon.PullGroup{{Positions: []dungeon.Point{pt(1), pt(2)}}, {Positions: []dungeon.Point{pt(3)}}}, Boss: &dungeon.Boss{Position: pt(4)}}}}
	moved, marked := ApplySnaps(p, []Snap{{From: pt(1), To: &to}, {From: pt(3), NeedsWalk: true}, {From: pt(4), NeedsWalk: true}})
	l := p.Legs[0]
	if moved != 2 || marked != 2 || l.To != to || l.PullGroups[0].Positions[0] != to || l.PullGroups[0].Positions[1] != pt(2) {
		t.Fatalf("%d %d %+v", moved, marked, l)
	}
	if l.NeedsWalk || l.PullGroups[0].NeedsWalk || !l.PullGroups[1].NeedsWalk || !l.Boss.NeedsWalk || l.PullGroups[1].Positions[0] != pt(3) {
		t.Fatalf("%+v", l)
	}
}

// closestInPoly: a point inside stays, one outside goes to the nearest edge point moved a little inside, either winding.
func TestClosestInPoly(t *testing.T) {
	sq := []navmesh.Vec3{{0, 0, 0}, {10, 0, 0}, {10, 10, 0}, {0, 10, 0}}
	for _, vs := range [][]navmesh.Vec3{sq, {sq[3], sq[2], sq[1], sq[0]}} {
		if x, y, d := closestInPoly(vs, 5, 5); x != 5 || y != 5 || d != 0 {
			t.Errorf("inside: %v %v %v", x, y, d)
		}
		x, y, d := closestInPoly(vs, 13, 5)
		if d != 3 || x < 9.5 || x >= 10 || y < 4.9 || y > 5.1 {
			t.Errorf("outside: %v %v %v", x, y, d)
		}
	}
}

// TestZulGurubMarliStandsOutOfSpiderAggro: walk 25's Mar'li leg walked the raid onto the spawn of the patrolling
// Skitterer 49202 (pack 0's leader) and, with the fight drifting west, onto pack 1 (leader 49201) as well: 20-21
// elites at once, 6 of 7 wipes (TortoiseBots-audit research/zg-w25/MARLI-PULL.md sections 0-2). The fix pulls both
// spider packs as stand pulls from the east, so the leg's `to` and the two stands must be out of both packs' aggro:
// every creature of pack 0 (group_leader 49202) and pack 1 (group_leader 49201 plus the loose Venombrood 49246)
// stays at least marliStandYd away. 40 is twice the 20 yd detection range (a bot at ~20 yd wakes the pack); the
// leg 2 packs' members are the only ones listed, and a pack is aggro-together (creature_groups flags 14).
func TestZulGurubMarliStandsOutOfSpiderAggro(t *testing.T) {
	p, err := dungeon.Load(filepath.Join(plansDirT, "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	cs, err := readCreatures(filepath.Join("testdata", "map309", "creatures.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	// pack0 and pack1 are the spawns of each pack: its own members and the loose Venombrood 49246 beside pack 1.
	var pack0, pack1 []creature
	for _, c := range cs {
		switch {
		case c.GroupLeader == 49202:
			pack0 = append(pack0, c)
		case c.GroupLeader == 49201 || c.GUID == 49246:
			pack1 = append(pack1, c)
		}
	}
	if len(pack0) != 9 || len(pack1) != 12 {
		t.Fatalf("pack 0 has %d spawns (want 9), pack 1 %d (want 12)", len(pack0), len(pack1))
	}
	var leg *dungeon.Leg
	for i := range p.Legs {
		if p.Legs[i].Name == "high_priestess_marli" {
			leg = &p.Legs[i]
		}
	}
	if leg == nil {
		t.Fatal("no high_priestess_marli leg")
	}
	// (a) the leg's `to` stands clear of both packs (today it is 0.1 yd from 49202's spawn).
	const marliStandYd = 40
	for _, c := range append(append([]creature{}, pack0...), pack1...) {
		if d := dist(leg.To, c.Pos); d < marliStandYd {
			t.Errorf("to %.1f %.1f %.1f is %.1f yd from %s %d, want >= %d", leg.To.X, leg.To.Y, leg.To.Z, d, c.Name, c.GUID, marliStandYd)
		}
	}
	if len(leg.PullGroups) < 2 {
		t.Fatalf("%d pull groups", len(leg.PullGroups))
	}
	// (b) both spider rows are stand pulls with a stand (their last approach point).
	stands := make([]dungeon.Point, 2)
	for i, g := range leg.PullGroups[:2] {
		if !g.StandPull {
			t.Errorf("pull_groups[%d] is not a stand pull", i)
		}
		if len(g.Approach) == 0 {
			t.Errorf("pull_groups[%d] has no stand (approach point)", i)
			continue
		}
		stands[i] = g.Approach[len(g.Approach)-1]
	}
	// (c) pack 0's stand clears every unit of both packs; (d) pack 1's stand clears pack 1.
	// If plan-check refuses S1 (-12250 -1683) for sight or range, the fallback -12256 -1680 is 35.5 yd from
	// pack 1, so (d)'s bound is 35 then (the brief's fallback); 40 is the bound with the S1 stand.
	check := func(i int, targets []creature, want float64) {
		for _, c := range targets {
			if d := dist(stands[i], c.Pos); d < want {
				t.Errorf("pull_groups[%d] stand %.1f %.1f %.1f is %.1f yd from %s %d, want >= %g", i, stands[i].X, stands[i].Y, stands[i].Z, d, c.Name, c.GUID, want)
			}
		}
	}
	both := append(append([]creature{}, pack0...), pack1...)
	check(0, both, marliStandYd)
	check(1, pack1, marliStandYd)
}
