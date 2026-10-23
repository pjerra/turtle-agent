package dungeon

import (
	"bytes"
	"strings"
	"testing"
)

// A wait_for may carry a hold (its run leader form) and a hold may carry a value (a script value of its instance data): both parse, keep
// their form through Marshal, and a bad hold on a wait is refused like one on a pull group.
func TestWaitForHoldAndHoldValueParse(t *testing.T) {
	p := mustPlan(t, eventPlan(`{"wait_for":{"unit":{"entries":[7796],"state":"seen"},"timeout_s":600,"hold":{"instance_data":1,"value":7,"spot":{"x":5,"y":6,"z":7},"radius":30,"timeout_s":600,"entries":[7787,7788]}}},{"wait_for":{"unit":{"entries":[7],"state":"seen"}}}`, ""))
	h := p.Legs[0].Steps[0].WaitFor.Hold
	if h == nil || h.Data != 1 || h.Value == nil || *h.Value != 7 || h.Spot != (Point{5, 6, 7}) || h.Radius != 30 || h.TimeoutS != 600 || len(h.Entries) != 2 {
		t.Fatalf("hold %+v", h)
	}
	if p.Legs[0].Steps[0].WaitFor.Unit == nil {
		t.Fatal("the wait's own unit condition must stay (the agent's driver runs it)")
	}
	if p.Legs[0].Steps[1].WaitFor.Hold != nil {
		t.Fatal("a wait without a hold has none")
	}
	out, err := Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"instance_data": 1,`+"\n"+`              "value": 7,`) {
		t.Fatalf("the value is not next to its instance_data:\n%s", out)
	}
	q, err := Parse("again.json", out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if again, _ := Marshal(q); !bytes.Equal(out, again) {
		t.Fatalf("%s", out)
	}
	if _, err := Parse("bad.json", []byte(eventPlan(`{"wait_for":{"unit":{"entries":[7796],"state":"seen"},"hold":{"instance_data":0,"spot":{"x":5,"y":6,"z":7},"radius":30,"timeout_s":600}}}`, ""))); err == nil || !strings.Contains(err.Error(), "wait_for.hold.instance_data") {
		t.Fatalf("a hold without its instance data must be refused: %v", err)
	}
}

// The run leader's party stands on a wait hold's spot (plan-export writes the wait as its hold row), so the reach of a boss after it is
// measured from there, not from the leg's to.
func TestRunLeaderReachCountsAWaitHoldSpot(t *testing.T) {
	plan := func(hold string) *Plan {
		return mustPlan(t, `{"version":1,"name":"t","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[{"to":{"x":0,"y":0,"z":0},
		 "steps":[{"wait_for":{"unit":{"entries":[7],"state":"seen"}`+hold+`}}],"boss":{"entry":9,"position":{"x":100,"y":0,"z":0}}}]}`)
	}
	if n := len(plan("").RunLeaderReach(0)); n != 1 {
		t.Fatalf("without a hold the boss is 100 yd from the to: %d warnings", n)
	}
	if w := plan(`,"hold":{"instance_data":1,"value":7,"spot":{"x":80,"y":0,"z":0},"radius":30,"timeout_s":600}`).RunLeaderReach(0); len(w) != 0 {
		t.Fatalf("the hold spot is 20 yd from the boss: %v", w)
	}
}

// A hold's value: never 0 (instance data starts at 0, so the hold would end at once), and only on a wait's or a boss's hold (a pull group's
// hold is an encounter that ends DONE; a boss pair such as Mograine and Whitemane keeps its own numbers in its data, so its hold takes one).
func TestHoldValueIsRefusedWhereItCannotWork(t *testing.T) {
	const hold = `"hold":{"instance_data":1,%s"spot":{"x":5,"y":6,"z":7},"radius":30,"timeout_s":600}`
	h := func(value string) string { return strings.Replace(hold, "%s", value, 1) }
	for _, tc := range []struct{ name, js, want string }{
		{"value 0 on a wait", eventPlan(`{"wait_for":{"unit":{"entries":[7],"state":"seen"},`+h(`"value":0,`)+`}}`, ""), "wait_for.hold.value: 0 is the value instance data starts at"},
		{"value on a pull group", `{"version":1,"name":"t","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[{"to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[4857],"positions":[{"x":5,"y":6,"z":7}],` + h(`"value":5,`) + `}]}]}`, "hold.value: only a wait_for's or a boss's hold takes a value"},
		{"value 0 on a boss", `{"version":1,"name":"t","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[{"to":{"x":0,"y":0,"z":0},"boss":{"entry":2748,"position":{"x":5,"y":6,"z":7},` + h(`"value":0,`) + `}}]}`, "boss.hold.value: 0 is the value instance data starts at"},
	} {
		if _, err := Parse("bad.json", []byte(tc.js)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	for _, ok := range []string{
		eventPlan(`{"wait_for":{"unit":{"entries":[7],"state":"seen"},`+h(`"value":7,`)+`}}`, ""),
		`{"version":1,"name":"t","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[{"to":{"x":0,"y":0,"z":0},"boss":{"entry":2748,"position":{"x":5,"y":6,"z":7},` + h(``) + `}}]}`,
		`{"version":1,"name":"t","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[{"to":{"x":0,"y":0,"z":0},"boss":{"entry":2748,"position":{"x":5,"y":6,"z":7},` + h(`"value":4,`) + `}}]}`,
	} {
		if _, err := Parse("ok.json", []byte(ok)); err != nil {
			t.Errorf("%v", err)
		}
	}
}
