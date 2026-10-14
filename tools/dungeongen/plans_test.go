package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
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

// Every instance map has at least one plan, and the spec names every plan file in dungeons/ (bar zulfarrak-extra, a record).
func TestSpecCoversEveryInstanceMapAndFile(t *testing.T) {
	sf := loadSpecT(t)
	have := map[uint32]bool{}
	named := map[string]bool{"zulfarrak-extra": true}
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
		t.Errorf("%d plan files for %d spec plans (+ zulfarrak-extra)", len(files), len(sf.Plans))
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
	if len(files) < 40 {
		t.Fatalf("only %d plans generated", len(files))
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
		if m != 70 && m != 209 {
			t.Errorf("vm_mmaps names map %d: only 70 and 209 were copied to the VM (update this test when more are)", m)
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
