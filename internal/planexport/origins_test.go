package planexport

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// testPlan is one leg with two vias, a pull group with a stand and a boss: every emitter of Origins runs.
func testPlan() *dungeon.Plan {
	return &dungeon.Plan{
		Version:  1,
		Name:     "origins_test",
		Kind:     "raid",
		Map:      1,
		Entrance: dungeon.Point{X: 0, Y: 0, Z: 0},
		Legs: []dungeon.Leg{{
			Name: "one",
			Via:  []dungeon.Waypoint{{Point: dungeon.Point{X: 5, Y: 0, Z: 0}}, {Point: dungeon.Point{X: 8, Y: 0, Z: 0}}},
			To:   dungeon.Point{X: 10, Y: 0, Z: 0},
			PullGroups: []dungeon.PullGroup{{
				Entries:   []uint32{100},
				Positions: []dungeon.Point{{X: 20, Y: 0, Z: 0}},
				Approach:  []dungeon.Point{{X: 15, Y: 0, Z: 0}, {X: 18, Y: 0, Z: 0}},
				StandPull: true,
			}},
			Boss: &dungeon.Boss{Entry: 200, Position: dungeon.Point{X: 30, Y: 0, Z: 0}},
		}},
	}
}

func TestOriginsCoverEveryRow(t *testing.T) {
	p := testPlan()
	res, err := Build(p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Origins) != len(res.Rows.Steps) {
		t.Fatalf("%d origins for %d rows", len(res.Origins), len(res.Rows.Steps))
	}
	want := []RowOrigin{
		{Leg: 0, Part: "via", Group: -1, Index: 0},
		{Leg: 0, Part: "via", Group: -1, Index: 1},
		{Leg: 0, Part: "to", Group: -1, Index: 0},
		{Leg: 0, Part: "approach", Group: 0, Index: 0},
		{Leg: 0, Part: "approach", Group: 0, Index: 1},
		{Leg: 0, Part: "pull", Group: 0, Index: 0},
		{Leg: 0, Part: "boss", Group: -1, Index: 0},
	}
	if len(res.Origins) != len(want) {
		t.Fatalf("%d origins, want %d: %+v", len(res.Origins), len(want), res.Origins)
	}
	for i := range want {
		if res.Origins[i] != want[i] {
			t.Errorf("row %d origin %+v, want %+v", i+1, res.Origins[i], want[i])
		}
	}
	// A non-cut row's point is the plan element it names.
	pt := func(o RowOrigin) dungeon.Point {
		l := p.Legs[o.Leg]
		switch o.Part {
		case "via":
			return l.Via[o.Index].Point
		case "to":
			return l.To
		case "approach":
			return l.PullGroups[o.Group].Approach[o.Index]
		case "pull":
			// The row point is the group's centre: the single position.
			return l.PullGroups[o.Group].Positions[0]
		case "boss":
			return l.Boss.Position
		}
		t.Fatalf("unexpected part %q", o.Part)
		return dungeon.Point{}
	}
	for i, o := range res.Origins {
		s := res.Rows.Steps[i]
		w := pt(o)
		if s.X != w.X || s.Y != w.Y || s.Z != w.Z {
			t.Errorf("row %d (%s) at %.1f %.1f %.1f, origin point %.1f %.1f %.1f", i+1, o.Part, s.X, s.Y, s.Z, w.X, w.Y, w.Z)
		}
	}
}

// TestCutRowsCarryTheViaIndexThecutPrecedes: a cut inserts move rows before the row whose hop it cuts; their origin names the
// plan element the cut row precedes (for `to`: the via count).
func TestCutRowsCarryTheViaIndexTheCutPrecedes(t *testing.T) {
	p := testPlan()
	p.Legs[0].To = dungeon.Point{X: 100, Y: 0, Z: 0}
	cut := func(mapID uint32, from, to dungeon.Point) ([]dungeon.Point, []CutNote, error) {
		return []dungeon.Point{{X: 30, Y: 0, Z: 0}, {X: 60, Y: 0, Z: 0}}, nil, nil
	}
	res, err := Build(p, Options{Cut: cut})
	if err != nil {
		t.Fatal(err)
	}
	var beforeTo []RowOrigin
	for i, o := range res.Origins {
		if o.Part != "cut" {
			continue
		}
		if res.Rows.Steps[i].Comment != "path" {
			t.Errorf("row %d: comment %q", i+1, res.Rows.Steps[i].Comment)
		}
		if o.Leg != 0 || o.Group != -1 {
			t.Errorf("cut origin %+v, want leg 0 and group -1", o)
		}
		if o.Index == 2 {
			beforeTo = append(beforeTo, o)
		}
	}
	if len(beforeTo) != 2 {
		t.Fatalf("%d cut rows before `to`, want 2: %+v", len(beforeTo), res.Origins)
	}
	// The cut rows with index 2 are the ones right before the `to` row (the via count).
	for i, o := range res.Origins {
		if o.Part == "cut" && o.Index == 2 && res.Rows.Steps[i].Comment != "path" {
			t.Errorf("row %d: cut origin before `to`", i+1)
		}
	}
}

// TestSQLUnchangedByOrigins pins the SQL the exporter writes for the two plans the origins work touched: row origins are a
// side table and never change a row.
func TestSQLUnchangedByOrigins(t *testing.T) {
	for _, c := range []struct {
		plan string
		sha  string
	}{
		{"zul_gurub", "3a1bcd9d4ac49bbcdf99c6fdff4454824a5ab73507313d82ef306b25e81b091c"}, // walk-34 plan: the Mandokir 11391 stand-pull flag and the Jin'do group-12 midpoint row
		{"onyxias_lair", "142c1d58928ad1efd9aca2b773c4762e2ddcbae05c308540080a1fedd6e3b38e"},
	} {
		p, err := dungeon.Load(filepath.Join("..", "..", "dungeons", c.plan+".json"))
		if err != nil {
			t.Fatal(err)
		}
		res, err := Build(p, Options{})
		if err != nil {
			t.Fatal(err)
		}
		got := fmt.Sprintf("%x", sha256.Sum256([]byte(WriteSQL([]Rows{res.Rows}))))
		if got != c.sha {
			t.Errorf("%s: SQL sha256 %s, want %s", c.plan, got, c.sha)
		}
	}
}

// TestStepOriginsNameTheirList: a step row's origin names the list the step is in (steps or after_boss) and its index there.
func TestStepOriginsNameTheirList(t *testing.T) {
	p := testPlan()
	p.Legs[0].Steps = []dungeon.Step{{AreaTrigger: &dungeon.AreaTrigger{ID: 1, Stand: dungeon.Point{X: 12}}}}
	p.Legs[0].AfterBoss = []dungeon.Step{{AreaTrigger: &dungeon.AreaTrigger{ID: 2, Stand: dungeon.Point{X: 40}}}}
	res, err := Build(p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var got []RowOrigin
	for _, o := range res.Origins {
		if o.Part == "step" {
			got = append(got, o)
		}
	}
	want := []RowOrigin{{Leg: 0, Part: "step", Group: -1, Index: 0, List: "steps"},
		{Leg: 0, Part: "step", Group: -1, Index: 0, List: "after_boss"}}
	if len(got) != len(want) {
		t.Fatalf("step origins %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("step origin %d %+v, want %+v", i, got[i], want[i])
		}
	}
}
