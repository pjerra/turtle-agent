package raidwalk

import (
	"fmt"
	"path/filepath"
	"slices"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// allRunLegs is a run of every leg of a plan these tests build: no leg is optional or skip_export, so every leg is in
// the run. A walk's own run legs come from its rows (PlanRunLegs).
func allRunLegs(p *dungeon.Plan) RunLegs {
	run := RunLegs{}
	if p != nil {
		for i := range p.Legs {
			run[i] = true
		}
	}
	return run
}

func TestBossRowsCounts(t *testing.T) {
	plan := &dungeon.Plan{Legs: []dungeon.Leg{
		// 10 is also pulled as trash (ambiguous); 50 never is (an encounter unit like Stalagg).
		{Name: "a", PullGroups: []dungeon.PullGroup{{Entries: []uint32{10, 77}}},
			Boss: &dungeon.Boss{Entry: 1, Position: dungeon.Point{X: 0, Y: 0, Z: 50}, First: []uint32{10, 50}, PhaseEntries: []uint32{60}}},
		// Boss b lists 10 too, and lists boss a's entry 1 as a first add.
		{Name: "b", PullGroups: []dungeon.PullGroup{{Entries: []uint32{1, 30}}},
			Boss: &dungeon.Boss{Entry: 2, Position: dungeon.Point{X: 1000, Y: 0}, First: []uint32{10, 1}}},
		// Boss c has no position in the plan; its first add 30 is pulled as trash on leg b.
		{Name: "c", Boss: &dungeon.Boss{Entry: 3, First: []uint32{30}}},
		{Name: "d", Boss: &dungeon.Boss{Entry: 4, Position: dungeon.Point{X: -500, Y: -500}, Hold: &dungeon.Hold{Entries: []uint32{40}}}},
	}}
	b := NewBossRows(plan, allRunLegs(plan))
	if got, want := b.Ambiguous(), []uint32{10, 30}; !slices.Equal(got, want) {
		t.Fatalf("ambiguous %v, want %v (1 is a boss entry, 50 never in a pull group)", got, want)
	}
	for _, tc := range []struct {
		name  string
		entry uint32
		x, y  float32
		want  bool
	}{
		{"not listed", 99, 0, 0, false},
		{"plain trash entry", 77, 0, 0, false},
		{"boss far from its spot", 2, 5000, 5000, true},
		{"hold entry far away", 40, 5000, 5000, true},
		{"phase entry far away", 60, 5000, 5000, true},
		{"boss entry that is also a first add of another boss (and in a pull group)", 1, 5000, 5000, true},
		{"first-only, never in a pull group, 70 yd (Stalagg)", 50, 70, 0, true},
		{"ambiguous near boss a", 10, 20, 20, true},
		{"ambiguous near boss b (listed by two bosses)", 10, 1030, 10, true},
		{"ambiguous between both bosses", 10, 500, 0, false},
		{"ambiguous at 70 yd", 10, 70, 0, false},
		{"ambiguous at exactly 40 yd", 10, 40, 0, true},
		{"ambiguous just past 40 yd", 10, 40.5, 0, false},
		{"ambiguous of a boss with no position", 30, 5000, 5000, true},
	} {
		if got := b.Counts(tc.entry, tc.x, tc.y); got != tc.want {
			t.Errorf("%s: Counts(%d, %v, %v) = %v, want %v", tc.name, tc.entry, tc.x, tc.y, got, tc.want)
		}
	}
	if got := NewBossRows(nil, nil).Counts(1, 0, 0); got {
		t.Error("a nil plan has no boss rows")
	}
	for entry, want := range map[uint32]string{
		1:  "boss 1",
		40: "boss 40",
		60: "boss 60",
		10: "boss row 10 (first add of a, b)",
		30: "boss row 30 (first add of c)",
	} {
		if got := b.Words(entry); got != want {
			t.Errorf("Words(%d) = %q, want %q", entry, got, want)
		}
	}
}

// TestBossRowsAnyOfEntriesAreCore: a boss's any_of alternatives (the one boss of several an event makes active, Zul'Gurub's
// Edge of Madness 15082/15083/15084/15085) are that row's boss: the gates, the one-shot latch and the injector's filter
// (Counts), and the guid rule that keys the boss on its own entry (Owns), must read each of them as the boss.
func TestBossRowsAnyOfEntriesAreCore(t *testing.T) {
	plan := &dungeon.Plan{Legs: []dungeon.Leg{{
		Name: "edge_of_madness",
		Boss: &dungeon.Boss{Entry: 15082, AnyOf: []uint32{15082, 15083, 15084, 15085}},
	}}}
	b := NewBossRows(plan, allRunLegs(plan))
	for _, e := range []uint32{15082, 15083, 15084, 15085} {
		if !b.Owns(e) {
			t.Errorf("Owns(%d) = false, want true: it is the row's entry or one of its any_of alternatives", e)
		}
		if !b.Counts(e, 0, 0) {
			t.Errorf("Counts(%d) = false, want true: a unit of that entry at any spot is the boss row's boss", e)
		}
	}
	if b.Owns(15086) || b.Counts(15086, 0, 0) {
		t.Error("15086 is no unit of the row")
	}
}

// TestBossRowsAmbiguousRealPlans pins the ambiguous first entries of the real plans: Zul'Gurub's is exactly the
// Zulian Tiger 11361 (Thekal's add, also ZG trash), and Naxxramas has none (the Horsemen, Stalagg/Feugen and the
// crypt guards count wherever they stand). Thekal's phase entries are core boss rows. A new ambiguous entry must be
// a deliberate change: a summoned encounter add listed in a pull group gets the 40 yd radius and could fail open
// (the walk would revive into its fight), so update this list only after checking the add really is also trash.
func TestBossRowsAmbiguousRealPlans(t *testing.T) {
	for _, tc := range []struct {
		plan string
		want []uint32
	}{
		{"zul_gurub", []uint32{11361}}, // review round 2: the hall's east side (the guard, the north camp and the lone mistresses) is its own pull groups again, so the Jin'do boss row has no First list or radius and 14825/14882/14883 are not its adds; only Thekal's tiger 11361 is trash that is also a boss add
		{"deadmines", []uint32{657, 1731, 3450}},
		{"uldaman", []uint32{4847, 4853, 4855, 4863, 7030}},
		{"wailing_caverns", []uint32{3840, 5053, 5056, 5756, 5761, 8886, 61966, 61967}},
		{"naxxramas", nil},
		{"temple_of_ahnqiraj", nil},
		{"zulfarrak", nil},
	} {
		p, err := dungeon.Load(filepath.Join("..", "..", "dungeons", tc.plan+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if got := NewBossRows(p, allRunLegs(p)).Ambiguous(); !slices.Equal(got, tc.want) {
			t.Errorf("%s ambiguous %v, want %v", tc.plan, got, tc.want)
		}
	}
	p, err := dungeon.Load(filepath.Join("..", "..", "dungeons", "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	b := NewBossRows(p, allRunLegs(p))
	for _, e := range []uint32{14509, 15068} {
		if !b.Counts(e, 0, 0) || b.Words(e) != fmt.Sprintf("boss %d", e) {
			t.Errorf("Thekal phase entry %d: Counts %v Words %q, want a core boss row", e, b.Counts(e, 0, 0), b.Words(e))
		}
	}
}

// TestInjectAddPackNeverPicksFarEncounterUnit: the injector's add-pack candidate skips boss rows by the same rule.
// Naxxramas's Stalagg 15929 (a Thaddius first add the plan never pulls as trash) standing 70 yd from Thaddius's
// plan position is still an encounter unit and is never picked; plain trash at the same spot is.
func TestInjectAddPackNeverPicksFarEncounterUnit(t *testing.T) {
	p, err := dungeon.Load(filepath.Join("..", "..", "dungeons", "naxxramas.json"))
	if err != nil {
		t.Fatal(err)
	}
	// The run's legs: the boss rule reads only the legs the rows carry.
	rows := planexport.Rows{}
	for i, l := range p.Legs {
		rows.Legs = append(rows.Legs, planexport.LegRow{Leg: i, Name: l.Name})
	}
	in := NewInjector(nil, RosterNames{MT: "Mt"}, rows, p)
	thadd := dungeon.Point{X: 3518, Y: -2925.7, Z: 291.7}
	lead := dungeon.Point{X: thadd.X + 70 - 40, Y: thadd.Y, Z: thadd.Z}
	at := dungeon.Point{X: thadd.X + 70, Y: thadd.Y, Z: thadd.Z}
	f := &injectFault{spec: InjectSpec{Kind: InjectAddPack, MaxYd: 80}}
	obs := func(entry uint32) InjectObs {
		return InjectObs{Lead: "Mt", Bots: map[string]BotObs{"Mt": {GUID: 1, Pos: lead}},
			Hostiles: []HostileObs{{GUID: 500, Entry: entry, Pos: at, HPPct: 100}}}
	}
	if g, _ := in.addPackCandidate(f, obs(15929)); g != 0 {
		t.Fatalf("Stalagg 70 yd from Thaddius picked as an add-pack target (%v)", g)
	}
	if g, _ := in.addPackCandidate(f, obs(99999)); g != 500 {
		t.Fatalf("plain trash at the same spot not picked (%v): the test does not reach the boss filter", g)
	}
}
