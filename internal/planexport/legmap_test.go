package planexport

import (
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// legMapPlan: a, then a middle leg that is skip_export (wing) or optional (gong) or, in the default, the plan's own leg, then b. Each
// hop into a middle leg or into b is long (cut), and each of those legs has a leave spawn on its cut path: 2 (wing or gong's own) at
// 300 and 3 (b's own) at 1500. The via points the cutter returns sit on those spawns, so a hop that checks the wrong leg's list is cut.
const legMapPlanJS = `{"version":1,"name":"lv","status":"live-checked (test)","map":209,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","to":{"x":10,"y":0,"z":0},"pull_groups":[{"entries":[100],"positions":[{"x":10,"y":0,"z":0}]}]},
 {"name":"%s","to":{"x":500,"y":0,"z":0}%s,"leave":[{"entry":2,"spawn":2,"at":{"x":300,"y":0,"z":0},"why":"middle mob"}]},
 {"name":"b","to":{"x":2000,"y":0,"z":0},"pull_groups":[{"entries":[102],"positions":[{"x":2000,"y":0,"z":0}]}],"leave":[{"entry":3,"spawn":3,"at":{"x":1500,"y":0,"z":0},"why":"b mob"}]}]}`

// legMapCut returns the via point of each long hop: 300 for a hop to the middle leg, 1500 for a hop to b.
func legMapCut(_ uint32, _ dungeon.Point, c dungeon.Point) ([]dungeon.Point, []CutNote, error) {
	switch c.X {
	case 500:
		return []dungeon.Point{{X: 300}}, nil, nil
	case 2000:
		return []dungeon.Point{{X: 1500}}, nil, nil
	}
	return nil, nil, nil
}

func legMapPlan(t *testing.T, middle string) *dungeon.Plan {
	t.Helper()
	extra := `,"skip_export":true,"pull_groups":[{"entries":[101],"positions":[{"x":500,"y":0,"z":0}]}]`
	if middle == "gong" {
		extra = `,"optional":{"item":9240,"name":"Mallet"},"steps":[{"use_object":{"entry":900,"near":{"x":500,"y":0,"z":0},"requires_item":{"entry":9240}}}]`
	}
	return parsePlan(t, strings.Replace(strings.Replace(legMapPlanJS, "%s", middle, 1), "%s", extra, 1))
}

// The leave list a hop is checked against is the one of the leg the row leg is: with --with-legs (or --with-optional) the middle leg's
// rows are numbered 1 and b's 2, so a hop row 1 is the middle leg's and must see its own spawn 2, and b's hop (row 2) its own spawn 3.
// Both hops are then left alone. Before, b's row took number 1 and the middle leg's hop was checked against b's list only.
func TestLeaveConflictUsesTheExportedLeg(t *testing.T) {
	cases := []struct {
		name, middle string
		opt          Options
		legs         string // the rows' leg names
		warn         []string
	}{
		{"with-legs", "wing", Options{WithLegs: []string{"wing"}}, "a,wing,b",
			[]string{"leg 1 step", "left-alone 2 (spawn 2)", "leg 2 step", "left-alone 3 (spawn 3)"}},
		{"with-optional", "gong", Options{WithOptional: []string{"gong"}}, "a,gong,b",
			[]string{"leg 1 step", "left-alone 2 (spawn 2)", "leg 2 step", "left-alone 3 (spawn 3)"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := legMapPlan(t, c.middle)
			opt := c.opt
			opt.Source, opt.Cut = "test", legMapCut
			res, err := Build(p, opt)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, l := range res.Rows.Legs {
				names = append(names, l.Name)
			}
			if got := strings.Join(names, ","); got != c.legs {
				t.Fatalf("rows legs %s, want %s", got, c.legs)
			}
			w := strings.Join(res.Warnings, "\n")
			for _, want := range c.warn {
				if !strings.Contains(w, want) {
					t.Errorf("warnings lack %q:\n%s", want, w)
				}
			}
			for _, s := range res.Rows.Steps {
				if s.Kind == KindMove && s.Comment == "path" {
					t.Errorf("a hop the leave list of its own leg marks was cut: %+v", s)
				}
			}
		})
	}
}

// The default export is unchanged: the middle leg is left out, b is row leg 1 and a hop into b is checked against b's own list.
func TestLeaveConflictDefaultExportUnchanged(t *testing.T) {
	p := legMapPlan(t, "wing")
	res, err := Build(p, Options{Source: "test", Cut: legMapCut})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows.Legs) != 2 || res.Rows.Legs[0].Name != "a" || res.Rows.Legs[1].Name != "b" {
		t.Fatalf("default rows legs %+v, want a and b", res.Rows.Legs)
	}
	if w := strings.Join(res.Warnings, "\n"); !strings.Contains(w, "leg 1 step") || !strings.Contains(w, "left-alone 3 (spawn 3)") || strings.Contains(w, "left-alone 2") {
		t.Errorf("warnings %v", res.Warnings)
	}
	for _, s := range res.Rows.Steps {
		if s.Comment == "path" {
			t.Errorf("the hop into b was cut past its own spawn 3: %+v", s)
		}
	}
	if n, ok := res.Rows.LegNumber(p, 1); ok {
		t.Errorf("the left-out middle leg has row leg %d", n)
	}
	if n, ok := res.Rows.LegNumber(p, 2); !ok || n != 1 {
		t.Errorf("b's row leg %d ok %v, want 1", n, ok)
	}
}

// Rows.LegNumber and the package's LegNumber read the same map: with --with-legs the middle leg is row leg 1 and b row leg 2.
func TestRowsLegNumberFollowsTheExport(t *testing.T) {
	p := legMapPlan(t, "wing")
	res, err := Build(p, Options{Source: "test", WithLegs: []string{"wing"}, Cut: legMapCut})
	if err != nil {
		t.Fatal(err)
	}
	for leg, want := range map[int]int{0: 0, 1: 1, 2: 2} {
		if got, ok := res.Rows.LegNumber(p, leg); !ok || got != want {
			t.Errorf("leg %d: row leg %d ok %v, want %d", leg, got, ok, want)
		}
	}
	if got, ok := PlanLeg(res.Rows.LegMap(p), 2); !ok || got != 2 {
		t.Errorf("row leg 2 is plan leg %d ok %v, want 2", got, ok)
	}
}

// The map an export builds is the legs its rows carry, by name and in order, for every option set (the one rule is legExported).
func TestExportedLegsAreTheRowsLegs(t *testing.T) {
	const both = `{"version":1,"name":"lv","status":"live-checked (test)","map":209,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","to":{"x":10,"y":0,"z":0},"pull_groups":[{"entries":[100],"positions":[{"x":10,"y":0,"z":0}]}]},
 {"name":"gong","to":{"x":20,"y":0,"z":0},"optional":{"item":9240,"name":"Mallet"},"steps":[{"use_object":{"entry":900,"near":{"x":20,"y":0,"z":0},"requires_item":{"entry":9240}}}]},
 {"name":"wing","to":{"x":30,"y":0,"z":0},"skip_export":true,"pull_groups":[{"entries":[101],"positions":[{"x":30,"y":0,"z":0}]}]},
 {"name":"skipme","to":{"x":40,"y":0,"z":0},"pull_groups":[{"entries":[102],"positions":[{"x":40,"y":0,"z":0}]}]},
 {"name":"b","to":{"x":50,"y":0,"z":0},"pull_groups":[{"entries":[103],"positions":[{"x":50,"y":0,"z":0}]}]}]}`
	p := parsePlan(t, both)
	sets := []Options{
		{},
		{WithOptional: []string{"gong"}},
		{WithLegs: []string{"wing"}},
		{WithOptional: []string{"gong"}, WithLegs: []string{"wing"}},
		{SkipLegs: []string{"skipme"}, WithOptional: []string{"gong"}, WithLegs: []string{"wing"}},
	}
	for _, opt := range sets {
		opt.Source = "test"
		res, err := Build(p, opt)
		if err != nil {
			t.Fatalf("%+v: %v", opt, err)
		}
		m := ExportedLegs(p, opt)
		if len(m) != len(res.Rows.Legs) {
			t.Fatalf("%+v: map %v for %d rows", opt, m, len(res.Rows.Legs))
		}
		for li, row := range m {
			if res.Rows.Legs[row].Name != p.Legs[li].Name || res.Rows.Legs[row].Leg != row {
				t.Errorf("%+v: plan leg %d (%s) is row leg %d, but the row leg there is %+v", opt, li, p.Legs[li].Name, row, res.Rows.Legs[row])
			}
		}
	}
}
