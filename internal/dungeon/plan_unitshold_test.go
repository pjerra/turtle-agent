package dungeon

import (
	"fmt"
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

// skip_if_dead_of_gossip is a units hold whose starting NPC was seen dead: the run leader ends it when no unit of it is hostile (the
// survivors of a dead NPC's crew stay friendly for good; zulfarrak.cpp npc_sergeant_blyAI turns them only from his script), at once when
// all lie dead. Only a boss's units hold takes it, and the leg needs the gossip step that starts the encounter (the boss's entry) with
// dead_is_done, since the run leader only knows the NPC was dead from that row.
func TestSkipIfDeadOfGossipRules(t *testing.T) {
	const near = `{"x":5,"y":6,"z":7}`
	gossip := func(npc int, dead bool) string {
		g := `{"gossip":{"npc":` + fmt.Sprint(npc) + `,"near":` + near + `,"option":0,"by":"tank"`
		if dead {
			g += `,"dead_is_done":true`
		}
		return g + `}}`
	}
	bossPlan := func(steps, hold string) string {
		return `{"version":1,"name":"t","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[{"to":{"x":0,"y":0,"z":0},"steps":[` + steps +
			`],"boss":{"entry":7604,"position":` + near + `,"first":[7608],"hold":` + hold + `}}]}`
	}
	const units = `{"spot":` + near + `,"radius":25,"timeout_s":300,"skip_if_dead_of_gossip":true}`
	p, err := Parse("ok.json", []byte(bossPlan(gossip(7604, true), units)))
	if err != nil {
		t.Fatalf("a flagged units hold with its dead gossip: %v", err)
	}
	if h := p.Legs[0].Boss.Hold; h == nil || !h.SkipIfDeadOfGossip || h.Data != 0 || h.Value != nil {
		t.Fatalf("boss hold %+v", h)
	}
	out, err := Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"skip_if_dead_of_gossip": true`) {
		t.Fatalf("the flag is not written: %s", out)
	}
	for _, tc := range []struct{ name, js, want string }{
		{"no gossip", bossPlan("", units), "needs a gossip step of this leg that starts the encounter (npc 7604, the boss) before the hold"},
		{"a live gossip", bossPlan(gossip(7604, false), units), "does not carry dead_is_done"},
		{"another NPC", bossPlan(gossip(7605, true), units), "needs a gossip step of this leg that starts the encounter (npc 7604, the boss) before the hold"},
		{"a data hold", bossPlan(gossip(7604, true), `{"instance_data":2,"spot":`+near+`,"radius":25,"timeout_s":300,"skip_if_dead_of_gossip":true}`),
			"only a boss's hold done by its units (instance_data left out) takes it"},
		{"a pull group's hold", `{"version":1,"name":"t","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[{"to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[4857],"positions":[` + near + `],"hold":{"instance_data":2,"spot":` + near + `,"radius":25,"timeout_s":300,"skip_if_dead_of_gossip":true}}]}]}`,
			"only a boss's hold done by its units (instance_data left out) takes it"},
	} {
		if _, err := Parse("bad.json", []byte(tc.js)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}
