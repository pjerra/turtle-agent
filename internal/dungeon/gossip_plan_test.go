package dungeon

import (
	"strings"
	"testing"
	"time"
)

// The gossip step in the plan file, the Stepper and the plan check (gossip.go, plan.go, plancheck.go).

func gossipPlanJSON(step string) string {
	return `{"version":1,"name":"t","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[{"to":{"x":0,"y":0,"z":0},"steps":[` + step + `]}]}`
}

const weegliStep = `{"gossip":{"npc":7607,"near":{"x":1858.2,"y":1114.4,"z":10.5},"option":0,"by":"tank","stand":{"x":1856,"y":1114,"z":10.5},"retry_s":20,"fail_text":"the event is not over"}}`

func TestGossipStepLoads(t *testing.T) {
	p := mustPlan(t, gossipPlanJSON(weegliStep+`,{"gossip":{"npc":7604,"near":{"x":1,"y":2,"z":3},"text":"Bly"}}`))
	g := p.Legs[0].Steps[0].Gossip
	if g.NPC != 7607 || g.Near != (Point{1858.2, 1114.4, 10.5}) || g.Option == nil || *g.Option != 0 || g.Text != "" || g.By != GossipByTank ||
		g.Stand == nil || *g.Stand != (Point{1856, 1114, 10.5}) || g.RetryS != 20 || g.Retry() != 20*time.Second || g.FailText != "the event is not over" {
		t.Fatalf("%+v", g)
	}
	// The defaults: the agent talks, the menu is asked again for 15 s, the option is a text.
	d := p.Legs[0].Steps[1].Gossip
	if d.Talker() != GossipByAgent || d.Retry() != 15*time.Second || d.Option != nil || d.Text != "Bly" || d.Stand != nil || d.OptionText() != `"Bly"` {
		t.Fatalf("%+v", d)
	}
	// It writes back as it was read.
	b, err := Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	q, err := Parse("again.json", b)
	if err != nil || *q.Legs[0].Steps[0].Gossip.Option != 0 || q.Legs[0].Steps[1].Gossip.Text != "Bly" || !strings.Contains(string(b), `"option": 0`) {
		t.Fatalf("%v\n%s", err, b)
	}
}

func TestGossipStepErrors(t *testing.T) {
	near := `"near":{"x":0,"y":0,"z":0}`
	cases := []struct{ name, step, want string }{
		{"no npc", `{"gossip":{` + near + `,"option":0}}`, "legs[0].steps[0].gossip.npc: required"},
		{"no near", `{"gossip":{"npc":1,"option":0}}`, "legs[0].steps[0].gossip.near: required"},
		{"no option", `{"gossip":{"npc":1,` + near + `}}`, "gossip: needs option (the menu's index) or text"},
		{"both", `{"gossip":{"npc":1,` + near + `,"option":0,"text":"x"}}`, "gossip: has both option and text"},
		{"negative option", `{"gossip":{"npc":1,` + near + `,"option":-1}}`, "gossip.option: -1 is outside 0-99"},
		{"huge option", `{"gossip":{"npc":1,` + near + `,"option":100}}`, "gossip.option: 100 is outside 0-99"},
		{"bad by", `{"gossip":{"npc":1,` + near + `,"option":0,"by":"healer"}}`, `gossip.by: "healer": use "agent", "carrier" or "tank"`},
		{"retry", `{"gossip":{"npc":1,` + near + `,"option":0,"retry_s":0}}`, "gossip.retry_s: 0 s is outside 1-120"},
		{"retry big", `{"gossip":{"npc":1,` + near + `,"option":0,"retry_s":900}}`, "gossip.retry_s: 900 s is outside 1-120"},
		{"stand", `{"gossip":{"npc":1,` + near + `,"option":0,"stand":{"x":0,"y":0}}}`, "gossip.stand.z: required"},
		{"fail_text", `{"gossip":{"npc":1,` + near + `,"option":0,"fail_text":"a\nb"}}`, "gossip.fail_text"},
		{"unknown field", `{"gossip":{"npc":1,` + near + `,"option":0,"colour":1}}`, `unknown field "colour"`},
		{"two things", `{"gossip":{"npc":1,` + near + `,"option":0},"use_object":{"entry":1}}`, "has more than one of use_object"},
		{"text a bot cannot be told", `{"gossip":{"npc":1,` + near + `,"text":"Where's Bly?","by":"carrier"}}`, `"Where's Bly?" cannot be carried in the order`},
		{"number as text", `{"gossip":{"npc":1,` + near + `,"text":"2","by":"tank"}}`, "is a number"},
		{"padded text", `{"gossip":{"npc":1,` + near + `,"text":" x"}}`, "gossip.text"},
	}
	for _, c := range cases {
		_, err := Parse("t.json", []byte(gossipPlanJSON(c.step)))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want %q, got %v", c.name, c.want, err)
		}
	}
	// What the agent does may say anything; a bot's order can carry words only.
	for _, ok := range []string{
		`{"gossip":{"npc":1,` + near + `,"text":"Where's Bly?"}}`,
		`{"gossip":{"npc":1,` + near + `,"text":"blow the door","by":"carrier"}}`,
		`{"gossip":{"npc":1,` + near + `,"option":3,"by":"tank"}}`,
	} {
		if _, err := Parse("t.json", []byte(gossipPlanJSON(ok))); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
}

func TestStepperReturnsTheGossipAction(t *testing.T) {
	p := mustPlan(t, gossipPlanJSON(weegliStep+`,{"wait_for":{"gameobject":{"entry":1,"state":"active"},"timeout_s":10}}`))
	w := &fakeWorld{}
	s := NewStepper(p, w, (&fakeClock{t: time.Unix(1000, 0)}).now)
	a := s.Next() // the player stands at the leg's To already
	if a.Kind != ActGossip || a.Gossip == nil || a.Gossip.NPC != 7607 || a.Step != 0 || !strings.Contains(a.Label, "gossip with NPC 7607 (option 0, by tank)") {
		t.Fatalf("%+v", a)
	}
	s.Complete()
	if a = s.Next(); a.Kind != ActWait || a.Step != 1 {
		t.Fatalf("the step after a gossip is looked at next: %+v", a)
	}
}

func TestPlanCheckWalksTheGossipStand(t *testing.T) {
	npc := Point{0, 100, 0}
	near, far, bad := Point{0, 98, 0}, Point{0, 90, 0}, Point{0, 99, 0}
	tank := func(stand *Point) Leg {
		return Leg{Name: "a", To: Point{0, 0, 0}, Steps: []Step{{Gossip: &Gossip{NPC: 7607, Near: npc, Text: "x", By: GossipByTank, Stand: stand}}}}
	}
	// A stand within 5 yd of the spawn, on the mesh: accepted. No stand: nothing to walk.
	for _, st := range []*Point{&near, nil} {
		if res := checkLegs(t, fakeNav{}, 0, tank(st)); !res.OK() {
			t.Errorf("%v: %s", st, res.Summary())
		}
	}
	// A stand 10 yd from the spawn, or off the mesh, is reported.
	if got, want := issueReasons(checkLegs(t, fakeNav{}, 0, tank(&far)).Issues), "0/steps[0].gossip.stand/0/too far"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
	if got, want := issueReasons(checkLegs(t, fakeNav{off: map[Point]bool{bad: true}}, 0, tank(&bad)).Issues), "0/steps[0].gossip.stand/0/off the mesh"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
	// A stand the party cannot reach.
	nav := fakeNav{block: func(_, to Point) bool { return to == near }}
	if got, want := issueReasons(checkLegs(t, nav, 0, tank(&near)).Issues), "0/steps[0].gossip.stand/0/unreachable"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
	// By the agent the party stays where it was: the boss after it is looked at from the leg's To, not from the stand.
	leg := tank(&near)
	leg.Steps[0].Gossip.By = GossipByAgent
	leg.Boss = &Boss{Entry: 9, Position: Point{0, 250, 0}}
	nav = fakeNav{block: func(from, to Point) bool { return to.Y == 250 && from.Y != 0 }}
	if res := checkLegs(t, nav, 0, leg); !res.OK() {
		t.Errorf("the party was taken to the agent's stand: %s", res.Summary())
	}
}
