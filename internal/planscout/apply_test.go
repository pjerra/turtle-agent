package planscout

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// stepStand is a step an area trigger takes, standing at x yards.
func stepStand(x float32) dungeon.Step {
	return dungeon.Step{AreaTrigger: &dungeon.AreaTrigger{Stand: dungeon.Point{X: x}}}
}

// standPlan is one leg with three stand-bearing steps and one after_boss step with a stand.
func standPlan() *dungeon.Plan {
	return &dungeon.Plan{Version: 1, Name: "t", Map: 1, Legs: []dungeon.Leg{{
		Name:      "one",
		Steps:     []dungeon.Step{stepStand(10), stepStand(20), stepStand(30)},
		AfterBoss: []dungeon.Step{stepStand(40)},
	}}}
}

func stepJSON(t *testing.T, s dungeon.Step) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestSetPointEditsOnlyTheNamedStep: a step origin edits the element it names, in the list it names; its neighbours stay byte-identical.
func TestSetPointEditsOnlyTheNamedStep(t *testing.T) {
	pt := dungeon.Point{X: 99, Y: 1, Z: 2}
	cases := []struct {
		name string
		ref  planexport.RowOrigin
		// want is "steps" or "after_boss": the list the one changed element sits in, with its index.
		list string
		idx  int
	}{
		{"steps 2", planexport.RowOrigin{Leg: 0, Part: "step", Group: -1, Index: 1, List: "steps"}, "steps", 1},
		{"after_boss 0", planexport.RowOrigin{Leg: 0, Part: "step", Group: -1, Index: 0, List: "after_boss"}, "after_boss", 0},
		{"no list (an older fix file) is steps", planexport.RowOrigin{Leg: 0, Part: "step", Group: -1, Index: 2}, "steps", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := standPlan()
			before := map[string]string{}
			for i, s := range p.Legs[0].Steps {
				before[key("steps", i)] = stepJSON(t, s)
			}
			for i, s := range p.Legs[0].AfterBoss {
				before[key("after_boss", i)] = stepJSON(t, s)
			}
			if err := setPoint(p, c.ref, pt); err != nil {
				t.Fatal(err)
			}
			for i, s := range p.Legs[0].Steps {
				if c.list == "steps" && i == c.idx {
					if s.AreaTrigger.Stand != pt {
						t.Fatalf("steps[%d] stand %+v, want %+v", i, s.AreaTrigger.Stand, pt)
					}
					continue
				}
				if got := stepJSON(t, s); got != before[key("steps", i)] {
					t.Errorf("steps[%d] changed: %s", i, got)
				}
			}
			for i, s := range p.Legs[0].AfterBoss {
				if c.list == "after_boss" && i == c.idx {
					if s.AreaTrigger.Stand != pt {
						t.Fatalf("after_boss[%d] stand %+v, want %+v", i, s.AreaTrigger.Stand, pt)
					}
					continue
				}
				if got := stepJSON(t, s); got != before[key("after_boss", i)] {
					t.Errorf("after_boss[%d] changed: %s", i, got)
				}
			}
		})
	}
}

func key(list string, i int) string {
	return fmt.Sprintf("%s/%d", list, i)
}

// TestSetPointRefusesAStepItCannotEdit: an index outside its list, or a step with no stand, is an error and changes nothing.
func TestSetPointRefusesAStepItCannotEdit(t *testing.T) {
	pt := dungeon.Point{X: 99}
	p := standPlan()
	p.Legs[0].Steps[0] = dungeon.Step{WaitFor: &dungeon.WaitFor{}}
	before := stepJSON(t, p.Legs[0].Steps[0])
	cases := map[string]planexport.RowOrigin{
		"steps index past the list":      {Leg: 0, Part: "step", Group: -1, Index: 3, List: "steps"},
		"after_boss index past the list": {Leg: 0, Part: "step", Group: -1, Index: 1, List: "after_boss"},
		"step with no stand":             {Leg: 0, Part: "step", Group: -1, Index: 0, List: "steps"},
		"negative index":                 {Leg: 0, Part: "step", Group: -1, Index: -1, List: "steps"},
	}
	for name, ref := range cases {
		if err := setPoint(p, ref, pt); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if got := stepJSON(t, p.Legs[0].Steps[0]); got != before {
		t.Errorf("a refused edit changed steps[0]: %s", got)
	}
	if p.Legs[0].Steps[1].AreaTrigger.Stand == pt || p.Legs[0].AfterBoss[0].AreaTrigger.Stand == pt {
		t.Error("a refused edit changed a neighbour")
	}
}
