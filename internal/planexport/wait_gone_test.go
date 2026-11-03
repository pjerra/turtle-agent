package planexport

import (
	"strings"
	"testing"
)

// A wait_for gameobject with gone_ok is a wait_object row with flag 256 (the module's kPlanFlagGoneIsDone): the object being gone after it
// was seen counts as done (Shadowfang Keep's Courtyard Door 18895, removed by Adamant's escort). Without gone_ok the row carries no flag.
func TestWaitGoneOKIsFlag256(t *testing.T) {
	p := parsePlan(t, `{"version":1,"name":"sfk","status":"live-checked (test)","map":33,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"door","to":{"x":10,"y":0,"z":0},"steps":[
  {"wait_for":{"gameobject":{"entry":18895,"near":{"x":-242.58,"y":2159.05,"z":90.62},"state":"active","gone_ok":true},"timeout_s":25}},
  {"wait_for":{"gameobject":{"entry":18972,"near":{"x":-129.03,"y":2166.16,"z":129.19},"state":"active"},"timeout_s":60}}]}]}`)
	res, err := Build(p, Options{Source: "test"})
	if err != nil {
		t.Fatal(err)
	}
	var waits []StepRow
	for _, s := range res.Rows.Steps {
		if s.Kind == KindWaitObject {
			waits = append(waits, s)
		}
	}
	if len(waits) != 2 || waits[0].Entry != 18895 || waits[0].Flags != FlagGoneIsDone || waits[0].Param != 1 || waits[0].TimeoutS != 25 ||
		!strings.Contains(waits[0].Comment, "or gone") || waits[1].Entry != 18972 || waits[1].Flags != 0 {
		t.Fatalf("%+v", waits)
	}
	if err := Validate(res.Rows); err != nil {
		t.Fatal(err)
	}
}

// The exporter's own rule: flag 256 is for a loot, open or wait_object row.
func TestGoneFlagOnAWaitObjectValidatesButNotOnAWaitClear(t *testing.T) {
	r := Rows{Plan: PlanRow{Plan: "sfk"}, Steps: []StepRow{{Kind: KindWaitObject, Entry: 18895, Param: 1, Flags: FlagGoneIsDone}}}
	if err := Validate(r); err != nil {
		t.Fatalf("wait_object: %v", err)
	}
	r.Steps[0] = StepRow{Kind: KindWaitClear, Radius: 5, Flags: FlagGoneIsDone}
	if err := Validate(r); err == nil || !strings.Contains(err.Error(), "gone-is-done flag is for a loot, open or wait_object row") {
		t.Fatalf("wait_clear: %v", err)
	}
}
