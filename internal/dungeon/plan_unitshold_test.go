package dungeon

import (
	"strings"
	"testing"
)

// A boss's hold may leave instance_data out: the event is done by its units (Zul'Farrak's Sergeant Bly, friendly until his gossip option
// turns him). A pull group's or a wait's hold still needs it, and a value needs it everywhere.
func TestHoldWithoutInstanceDataOnlyOnABoss(t *testing.T) {
	const units = `"hold":{%s"spot":{"x":5,"y":6,"z":7},"radius":25,"timeout_s":300}`
	h := func(extra string) string { return strings.Replace(units, "%s", extra, 1) }
	boss := func(hold string) string {
		return `{"version":1,"name":"t","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[{"to":{"x":0,"y":0,"z":0},"boss":{"entry":7604,"position":{"x":5,"y":6,"z":7},"first":[7608],` + hold + `}}]}`
	}
	p, err := Parse("ok.json", []byte(boss(h(""))))
	if err != nil {
		t.Fatalf("a boss's hold without instance_data: %v", err)
	}
	if bh := p.Legs[0].Boss.Hold; bh == nil || bh.Data != 0 || bh.Value != nil || bh.Radius != 25 || bh.TimeoutS != 300 {
		t.Fatalf("boss hold %+v", bh)
	}
	for _, tc := range []struct{ name, js, want string }{
		{"pull group", `{"version":1,"name":"t","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[{"to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[4857],"positions":[{"x":5,"y":6,"z":7}],` + h("") + `}]}]}`,
			"hold.instance_data: required (the index of the event's instance data, 1 or more); only a boss's hold may leave it out"},
		{"wait", eventPlan(`{"wait_for":{"unit":{"entries":[7],"state":"seen"},`+h("")+`}}`, ""),
			"hold.instance_data: required (the index of the event's instance data, 1 or more); only a boss's hold may leave it out"},
		{"wait with a value", eventPlan(`{"wait_for":{"unit":{"entries":[7],"state":"seen"},`+h(`"value":8,`)+`}}`, ""),
			"hold.instance_data: required with a value"},
	} {
		if _, err := Parse("bad.json", []byte(tc.js)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}

// agent_only marks a unit wait the run leader needs no row for: only on a unit wait, never with a hold, and with a one-line reason.
func TestAgentOnlyWaitRules(t *testing.T) {
	p, err := Parse("ok.json", []byte(eventPlan(`{"wait_for":{"unit":{"entries":[7607],"state":"gone"},"timeout_s":120,"agent_only":"the door wait covers it"}}`, "")))
	if err != nil {
		t.Fatal(err)
	}
	if w := p.Legs[0].Steps[0].WaitFor; w.AgentOnly != "the door wait covers it" || w.Unit == nil || w.TimeoutS != 120 {
		t.Fatalf("%+v", w)
	}
	const hold = `"hold":{"instance_data":1,"value":8,"spot":{"x":5,"y":6,"z":7},"radius":30,"timeout_s":600}`
	for _, tc := range []struct{ name, step, want string }{
		{"object wait", `{"wait_for":{"gameobject":{"entry":146084,"near":{"x":1,"y":1,"z":1},"state":"active"},"agent_only":"x"}}`, "agent_only: only a unit wait takes it"},
		{"hostiles wait", `{"wait_for":{"hostiles_gone":{"radius":20},"agent_only":"x"}}`, "agent_only: only a unit wait takes it"},
		{"with a hold", `{"wait_for":{"unit":{"entries":[7],"state":"seen"},` + hold + `,"agent_only":"x"}}`, "agent_only: a wait with a hold is the hold's row"},
		{"empty", `{"wait_for":{"unit":{"entries":[7],"state":"seen"},"agent_only":"  "}}`, "agent_only: say why"},
		{"line break", `{"wait_for":{"unit":{"entries":[7],"state":"seen"},"agent_only":"a\nb"}}`, "agent_only: say why"},
		{"too long", `{"wait_for":{"unit":{"entries":[7],"state":"seen"},"agent_only":"` + strings.Repeat("a", MaxFailText+1) + `"}}`, "agent_only: say why"},
	} {
		if _, err := Parse("bad.json", []byte(eventPlan(tc.step, ""))); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}
