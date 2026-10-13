package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

func pt(x, y, z float32) dungeon.Point { return dungeon.Point{X: x, Y: y, Z: z} }

func TestGenerateSynthetic(t *testing.T) {
	cs := []creature{
		{GUID: 1, Entry: 100, Name: "Near A", Faction: 59, Pos: pt(10, 0, 0)},
		{GUID: 2, Entry: 101, Name: "Near B", Faction: 59, Pos: pt(15, 0, 0)},                    // 5 yd from A: one pack
		{GUID: 3, Entry: 102, Name: "Far", Faction: 59, Pos: pt(150, 0, 0)},                      // its own pack, nearer to boss 2
		{GUID: 4, Entry: 103, Name: "Upstairs", Faction: 59, Pos: pt(12, 0, 30)},                 // 30 yd above A: another pack
		{GUID: 5, Entry: 104, Name: "Statue", Faction: 35, Pos: pt(11, 0, 0)},                    // friendly: dropped
		{GUID: 6, Entry: 105, Name: "Vendor", Faction: 59, NPCFlags: 128, Pos: pt(12, 0, 0)},     // vendor: dropped
		{GUID: 7, Entry: 106, Name: "Rare", Faction: 59, Rank: 4, Pos: pt(11, 1, 0)},             // rare: skipped, never a pack member
		{GUID: 8, Entry: 107, Name: "Linked 1", Faction: 59, GroupLeader: 8, Pos: pt(200, 0, 0)}, // creature group: one pack although 80 yd apart
		{GUID: 9, Entry: 108, Name: "Linked 2", Faction: 59, GroupLeader: 8, Pos: pt(280, 0, 0)},
		{GUID: 10, Entry: 109, Name: "Patrol", Faction: 59, Patrol: true, Pos: pt(14, 1, 0)}, // a patrol joins no pack
		{GUID: 50, Entry: 900, Name: "First Boss", Faction: 59, Rank: 3, Pos: pt(20, 0, 0)},
		{GUID: 51, Entry: 901, Name: "Second Boss", Faction: 59, Rank: 3, Pos: pt(250, 0, 0)},
	}
	objs := []object{
		{GUID: 1, Entry: 5000, Name: "Gate", Type: 0, Pos: pt(18, 0, 0)},
		{GUID: 2, Entry: 5001, Name: "Chest", Type: 3, Pos: pt(18, 0, 0)},    // not a door
		{GUID: 3, Entry: 5002, Name: "Faraway", Type: 0, Pos: pt(900, 0, 0)}, // not near any leg
	}
	e := pt(0, 0, 0)
	p, err := Generate(cs, objs, Options{Name: "syn", Map: 9, Bosses: []uint32{900, 901}, Entrance: &e, Cluster: 12, NoteRange: 20, IgnoreFactions: map[int]bool{35: true}, Skip: []uint32{777}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Legs) != 2 || p.Legs[0].Boss.Entry != 900 || p.Legs[1].Boss.Entry != 901 || p.Legs[0].Name != "first_boss" {
		t.Fatalf("%+v", p.Legs)
	}
	var l0, l1 [][]uint32
	for _, g := range p.Legs[0].PullGroups {
		l0 = append(l0, g.Entries)
	}
	for _, g := range p.Legs[1].PullGroups {
		l1 = append(l1, g.Entries)
	}
	// Leg 0: the pack A+B (pulled first: nearest to the entrance), the patrol standing beside it, then the spawn upstairs.
	if len(l0) != 3 || len(l0[0]) != 2 || l0[0][0] != 100 || l0[0][1] != 101 || l0[1][0] != 109 || l0[2][0] != 103 {
		t.Fatalf("leg 0: %v", l0)
	}
	// Leg 1: the far one, then the linked pair (80 yd apart, one creature group).
	if len(l1) != 2 || l1[0][0] != 102 || len(l1[1]) != 2 {
		t.Fatalf("leg 1: %v", l1)
	}
	seen := map[uint32]bool{}
	for _, l := range p.Legs {
		for _, g := range l.PullGroups {
			if len(g.Entries) != len(g.Positions) {
				t.Fatalf("one position per entry: %+v", g)
			}
			for _, en := range g.Entries {
				seen[en] = true
			}
		}
	}
	for _, en := range []uint32{104, 105, 106, 900, 901} {
		if seen[en] {
			t.Errorf("entry %d is no trash", en)
		}
	}
	if !seen[109] {
		t.Error("a patrol is a pack of its own, not dropped")
	}
	if got := p.Legs[0].Skip; len(got) != 2 || got[0] != 106 || got[1] != 777 {
		t.Fatalf("skip: the rare and the flag: %v", got)
	}
	if n := p.Legs[0].Notes; len(n) != 1 || !strings.Contains(n[0], `door "Gate" entry 5000`) {
		t.Fatalf("only doors near the leg: %v", n)
	}
	// The output reads back as a valid plan.
	b, err := dungeon.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dungeon.Parse("syn.json", b); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}

	if _, err := Generate(cs, nil, Options{Name: "x", Map: 9, Bosses: []uint32{12345}, Entrance: &e}); err == nil || !strings.Contains(err.Error(), "boss 12345 has no spawn") {
		t.Fatalf("%v", err)
	}
	if _, err := Generate(cs, nil, Options{Name: "x", Map: 9, Entrance: &e}); err == nil {
		t.Fatal("no bosses accepted")
	}
}

// The committed export of Uldaman (map 70) gives a valid, repeatable skeleton that holds every hostile spawn once.
func TestGenerateUldaman(t *testing.T) {
	gen := func() (*dungeon.Plan, []byte) {
		o := Options{Name: "uldaman", Map: 70, Bosses: []uint32{6906, 6910, 7228}, Cluster: 12, NoteRange: 20, IgnoreFactions: map[int]bool{35: true}}
		p, err := run(filepath.Join("testdata", "uldaman"), o)
		if err != nil {
			t.Fatal(err)
		}
		b, err := dungeon.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		return p, b
	}
	p, b := gen()
	if _, b2 := gen(); !bytes.Equal(b, b2) {
		t.Fatal("a second run gives another skeleton")
	}
	if _, err := dungeon.Parse("uldaman.skeleton", b); err != nil {
		t.Fatal(err)
	}
	if p.Entrance != pt(-228.859, 46.1018, -46.0186) || len(p.Legs) != 3 {
		t.Fatalf("%+v", p.Entrance)
	}
	cs, err := readCreatures(filepath.Join("testdata", "uldaman", "creatures.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	want := 0
	for _, c := range cs {
		if c.Faction != 35 && c.NPCFlags == 0 && c.Rank != 3 && c.Entry != 6910 && c.Entry != 7228 && c.Entry != 6906 {
			want++
		}
	}
	got := 0
	for _, l := range p.Legs {
		for _, g := range l.PullGroups {
			got += len(g.Entries)
		}
	}
	if got != want || want < 100 {
		t.Fatalf("pack members %d, hostile spawns %d", got, want)
	}
	if !strings.Contains(string(b), `entry 124371 at -234.7 239.6 -50.9`) {
		t.Error("the Keystone door is not among the notes")
	}
}

func TestReadErrors(t *testing.T) {
	dir := t.TempDir()
	write := func(name, s string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if _, err := readCreatures(write("a.tsv", "guid\tentry\n1\t2\n")); err == nil || !strings.Contains(err.Error(), `no "name" column`) {
		t.Fatalf("%v", err)
	}
	head := "guid\tentry\tname\trank\tfaction\tnpc_flags\tx\ty\tz\tgroup_leader\tpatrol\n"
	if _, err := readCreatures(write("b.tsv", head+"1\t2\tWolf\t0\t59\t0\tnorth\t0\t0\t0\t0\n")); err == nil || !strings.Contains(err.Error(), `"north" in column x is not a number`) {
		t.Fatalf("%v", err)
	}
	if _, err := readCreatures(write("c.tsv", head+"1\t2\tWolf\n")); err == nil || !strings.Contains(err.Error(), "c.tsv:2: 3 fields") {
		t.Fatalf("%v", err)
	}
	cs, err := readCreatures(write("d.tsv", head+"1\t2\tEric \"The Swift\"\t1\t122\t2\t1.5\t2.5\t3.5\t0\t1\n"))
	if err != nil || len(cs) != 1 || cs[0].Name != `Eric "The Swift"` || cs[0].Pos != pt(1.5, 2.5, 3.5) || !cs[0].Patrol {
		t.Fatalf("%+v %v", cs, err)
	}
	if _, err := parseEntries("1,x"); err == nil {
		t.Fatal("accepted")
	}
	if _, err := parsePoint("1,2"); err == nil {
		t.Fatal("accepted")
	}
	if p, err := parsePoint("1, 2, 3"); err != nil || p != pt(1, 2, 3) {
		t.Fatalf("%v %v", p, err)
	}
}
