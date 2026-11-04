package dungeon

import (
	"strconv"
	"strings"
	"testing"
)

// boss.summon_open_step names the use_object step whose object summons the boss (the object the run leader repeats to bring a
// despawned boss back): the plan loader takes the step that exports to an open/open_proof row and refuses a pointer elsewhere.
func TestSummonOpenStepRules(t *testing.T) {
	const near = `{"x":5,"y":6,"z":7}`
	plan := func(steps string, step int) string {
		field := ""
		if step != 0 {
			field = `,"summon_open_step":` + strconv.Itoa(step)
		}
		return `{"version":1,"name":"t","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[{"to":{"x":0,"y":0,"z":0},"steps":[` + steps +
			`],"boss":{"entry":14515,"position":` + near + field + `}}]}`
	}
	open := `{"use_object":{"entry":180526,"near":` + near + `}}`
	proof := `{"use_object":{"entry":148830,"near":` + near + `,"proof":{"gameobject":148883,"radius":5}}}`
	useOn := `{"use_object":{"entry":141073,"near":` + near + `,"requires_item":{"entry":9240},"via_item":true}}`
	moveThenTrigger := `{"use_object":{"entry":180526,"near":` + near + `}},{"area_trigger":{"id":962,"center":` + near + `,"radius":10,"stand":` + near + `,"by":"bot"}}`
	p, err := Parse("ok.json", []byte(plan(open, 1)))
	if err != nil {
		t.Fatalf("the annotated open step: %v", err)
	}
	if got := p.Legs[0].Boss.SummonOpenStep; got != 1 {
		t.Fatalf("summon_open_step = %d, want 1", got)
	}
	out, err := Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"summon_open_step": 1`) {
		t.Fatalf("the field is not written: %s", out)
	}
	p, err = Parse("ok2.json", []byte(plan(proof, 1)))
	if err != nil {
		t.Fatalf("a proof open step: %v", err)
	}
	if got := p.Legs[0].Boss.SummonOpenStep; got != 1 {
		t.Fatalf("the proof step's summon_open_step = %d, want 1", got)
	}
	for _, tc := range []struct {
		name, steps string
		step        int
		want        string
	}{
		{"past the steps", open, 2, "is no step of this leg (it has 1)"},
		{"before the steps", open, -1, "is no step of this leg (it has 1)"},
		{"a trigger step", moveThenTrigger, 2, "steps[1] is a area_trigger step, not a use_object step"},
		{"a use_on step", useOn, 1, "steps[0] is a use_on step (via_item)"},
		{"no steps", "", 1, "is no step of this leg (it has 0)"},
	} {
		if _, err := Parse("bad.json", []byte(plan(tc.steps, tc.step))); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
	}
}
